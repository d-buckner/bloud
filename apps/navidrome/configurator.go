// SPDX-License-Identifier: AGPL-3.0-only

package navidrome

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const appName = "navidrome"

// authentikAppName is the identity provider this app's user sync speaks to: the
// provider bound to its `sso` contract.
const authentikAppName = "authentik"

// bootstrapAdminUsername is the internal-only admin used by the configurator.
// It never appears in Authentik and is not meant for end users.
const bootstrapAdminUsername = "bloud-admin"

// Configurator handles Navidrome configuration.
type Configurator struct {
	port         int
	authentikURL string
	secrets      configurator.AppSecretsProvider
	logger       *slog.Logger
	navi         *navidromeAPI
	ak           *authentikAPI

	// baseURL is a test seam: when set, the own-API client resolves to it
	// instead of localhost:port.
	baseURL string
}

// NewConfigurator creates a new Navidrome configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:         port,
		authentikURL: deps.LocalTraefikURL(),
		secrets:      deps.Secrets,
		logger:       logger.With("app", "navidrome"),
	}
	c.navi = newNavidromeAPI(deps.HTTP, func() string {
		if c.baseURL != "" {
			return c.baseURL
		}
		return fmt.Sprintf("http://localhost:%d", c.port)
	})
	c.ak = newAuthentikAPI(deps.HTTP, func() string { return c.authentikURL })
	return c
}

// nodeName is the graph node and container name the host-agent reconciles
// this configurator under. Registration and Name() both read it, so the two
// cannot drift apart.
const nodeName = "apps-navidrome"

// defaultPort is the app's own web port, the value the constructor uses
// when registration passes 0. It matches metadata.yaml's `port`.
const defaultPort = 4533

func (c *Configurator) Name() string {
	return nodeName
}

// PreStart creates the required data and music directories before the container starts.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	dirs := []string{
		filepath.Join(state.DataPath, "data"),
		filepath.Join(state.BloudDataPath, "media", "music"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return configurator.NoRestart(), fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	// Directories only: Navidrome reads no Bloud-written config file, so there
	// is nothing here a running container would have to be replaced to pick up.
	return configurator.NoRestart(), nil
}

// PostStart syncs Authentik users into Navidrome so that forward-auth logins work.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	if !state.SSOEnabled {
		return nil
	}

	token, err := c.ensureAdminAndLogin(ctx)
	if err != nil {
		return fmt.Errorf("navidrome: admin bootstrap: %w", err)
	}

	// The Authentik API token comes from the binding Authentik publishes it
	// through, so this app reads no other app's files.
	authentikToken, err := authentikToken(state)
	if err != nil {
		c.logger.Warn("cannot read the Authentik API token, skipping user sync", "error", err)
		return nil
	}

	if err := c.syncUsersFromAuthentik(ctx, token, authentikToken); err != nil {
		return fmt.Errorf("navidrome: user sync: %w", err)
	}
	return nil
}

// --- Admin bootstrap ---

// ensureAdminAndLogin ensures the bootstrap admin exists and returns a valid token.
func (c *Configurator) ensureAdminAndLogin(ctx context.Context) (string, error) {
	if c.secrets == nil {
		return "", fmt.Errorf("no secrets provider")
	}
	password, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		return "", fmt.Errorf("generating admin password: %w", err)
	}

	// Fast path: try logging in with existing credentials.
	if token, err := c.navi.login(ctx, bootstrapAdminUsername, password); err == nil {
		return token, nil
	}

	// No admin yet: bootstrap the first admin user.
	c.logger.Info("bootstrapping admin user")
	token, err := c.navi.createAdmin(ctx, bootstrapAdminUsername, password)
	if err != nil {
		return "", fmt.Errorf("creating admin: %w", err)
	}
	c.logger.Info("admin user created")
	return token, nil
}

// authentikToken returns the API token Authentik publishes under its `sso`
// contract, the credential this app authenticates its user sync with. Authentik
// publishes it while it converges, so a missing token is "not ready yet" rather
// than a fault: the caller skips the sync and the next reconciliation retries.
func authentikToken(state *configurator.AppState) (string, error) {
	for _, binding := range state.Integrations.SSO {
		if binding.App != authentikAppName {
			continue
		}
		if binding.APIToken != "" {
			return binding.APIToken, nil
		}
		return "", fmt.Errorf("%s has not published its API token yet", authentikAppName)
	}
	return "", fmt.Errorf("no %s provider is bound to this app", authentikAppName)
}

// --- Sync logic ---

// syncUsersFromAuthentik creates any Authentik users that don't yet exist in
// Navidrome. Existing users are left untouched. The bootstrap admin is
// excluded from sync.
func (c *Configurator) syncUsersFromAuthentik(ctx context.Context, naviToken, authentikToken string) error {
	navUsers, err := c.navi.listUsers(ctx, naviToken)
	if err != nil {
		return fmt.Errorf("listing navidrome users: %w", err)
	}

	existing := make(map[string]struct{}, len(navUsers))
	for _, u := range navUsers {
		existing[u.UserName] = struct{}{}
	}

	akUsers, err := c.ak.listActiveUsers(ctx, authentikToken)
	if err != nil {
		return fmt.Errorf("listing authentik users: %w", err)
	}

	created := 0
	for _, u := range akUsers {
		// Skip the bootstrap admin (internal-only) and Authentik's own default admin.
		if u.Username == bootstrapAdminUsername || u.Username == "akadmin" {
			continue
		}
		if _, ok := existing[u.Username]; ok {
			continue
		}
		displayName := u.Name
		if displayName == "" {
			displayName = u.Username
		}
		c.logger.Info("creating user", "username", u.Username, "display_name", displayName)
		if err := c.navi.createUser(ctx, naviToken, u.Username, displayName, u.Email); err != nil {
			c.logger.Warn("failed to create user", "username", u.Username, "error", err)
			continue
		}
		created++
	}

	if created > 0 {
		c.logger.Info("synced users from Authentik", "count", created)
	} else {
		c.logger.Info("all Authentik users already present")
	}
	return nil
}
