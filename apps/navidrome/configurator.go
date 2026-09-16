// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package navidrome

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const appName = "navidrome"

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
	ak         *authentikAPI

	// baseURL is a test seam: when set, the own-API client resolves to it
	// instead of localhost:port.
	baseURL string
}

// NewConfigurator creates a new Navidrome configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = 4533
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

func (c *Configurator) Name() string {
	return "apps-navidrome"
}

// PreStart creates the required data and music directories before the container starts.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (bool, error) {
	dirs := []string{
		filepath.Join(state.DataPath, "data"),
		filepath.Join(state.BloudDataPath, "media", "music"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return false, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return false, nil
}

// Remove is a no-op for the Navidrome configurator; container and data removal
// are handled at a higher level by the orchestrator.
func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
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

	// Read the Authentik API token from disk (written by the Authentik configurator).
	authentikToken, err := c.readAuthentikToken(state)
	if err != nil {
		c.logger.Warn("cannot read Authentik token, skipping user sync", "error", err)
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

	// No admin yet — bootstrap the first admin user.
	c.logger.Info("bootstrapping admin user")
	token, err := c.navi.createAdmin(ctx, bootstrapAdminUsername, password)
	if err != nil {
		return "", fmt.Errorf("creating admin: %w", err)
	}
	c.logger.Info("admin user created")
	return token, nil
}

// readAuthentikToken reads the Authentik API token from disk.
func (c *Configurator) readAuthentikToken(state *configurator.AppState) (string, error) {
	tokenPath := filepath.Join(state.BloudDataPath, "authentik", "api-token")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", err
	}
	token := string(bytes.TrimSpace(data))
	if token == "" {
		return "", fmt.Errorf("empty token file")
	}
	return token, nil
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
