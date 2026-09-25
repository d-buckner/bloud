// SPDX-License-Identifier: AGPL-3.0-only

package immich

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/bootstrap"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const appName = "immich"

// bootstrapAdmin is the internal-only admin account the configurator creates
// so the server is "initialized" (login page instead of the first-admin
// registration page). End users authenticate via SSO; this account is never
// exposed and its password is not shared.
const (
	bootstrapAdminEmail = "bloud-admin@localhost"
	bootstrapAdminName  = "Bloud Admin"
)

// configFileName is the Immich config file written in PreStart and mounted
// into the server container at /config/immich/immich-config.yaml.
const configFileName = "immich-config.yaml"

// mountFolders are Immich's system-integrity check folders (the values of
// the server's StorageFolder enum, v3.1.x).
var mountFolders = []string{"thumbs", "upload", "backups", "library", "profile", "encoded-video"}

// Configurator handles Immich configuration: it writes the OAuth config file
// before the server starts (PreStart) and bootstraps the server admin after
// it starts (PostStart) so SSO login is possible from the first boot.
type Configurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
	api     *immichAPI

	// baseURL is a test seam: when set, the API client resolves to it
	// instead of localhost:port. Never used to build request URLs by hand.
	baseURL string
}

// NewConfigurator creates a new Immich configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:    port,
		secrets: deps.Secrets,
		logger:  logger.With("app", "immich"),
	}
	c.api = newAPI(deps.HTTP, func() string {
		if c.baseURL != "" {
			return c.baseURL
		}
		return fmt.Sprintf("http://localhost:%d", c.port)
	})
	return c
}

// nodeName is the graph node and container name the host-agent reconciles
// this configurator under. Registration and Name() both read it, so the two
// cannot drift apart.
const nodeName = "apps-immich-server"

// defaultPort is the app's own web port, the value the constructor uses
// when registration passes 0. It matches metadata.yaml's `port`.
const defaultPort = 2283

func (c *Configurator) Name() string {
	return nodeName
}

// PreStart writes the Immich config file with the native-oidc OAuth settings
// so OAuth is enabled from the very first boot. Returns configChanged=true
// when the file content changed so the orchestrator recreates the container.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	// Immich v3.1 crash-loops at startup when a .immich mount marker is
	// missing: the startup check only re-verifies (reads) markers whose pass
	// was already recorded in its database and never recreates missing ones.
	// Ensure them here: PreStart runs on every reconciliation cycle, so
	// this is idempotent and self-healing after any data-dir wipe.
	if err := ensureMountMarkers(state.DataPath, c.logger); err != nil {
		return configurator.NoRestart(), err
	}

	if state.OIDC == nil {
		// SSO not configured for this app: leave Immich defaults in place.
		return configurator.NoRestart(), nil
	}

	dir := filepath.Join(state.DataPath, "config")
	path := filepath.Join(dir, configFileName)
	content := renderConfigFile(state.OIDC)

	changed, err := managedfile.Write(path, []byte(content), managedfile.ModeSharedConfig)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing config file: %w", err)
	}
	if changed {
		c.logger.Info("wrote Immich OAuth config file", "path", path)
	}
	return configurator.RestartIf(changed, "Immich OAuth config rewritten"), nil
}

// PostStart bootstraps the server admin. Immich shows a first-admin
// registration page until an admin exists, so SSO login would be unreachable
// without this step. The sequence and the failure policy live in
// pkg/bootstrap; this only maps them onto Immich's API, after the server is
// reachable.
func (c *Configurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if err := c.api.waitServer(ctx); err != nil {
		return fmt.Errorf("waiting for immich server: %w", err)
	}

	_, _, err := bootstrap.Ensure(ctx, c.logger, appName, c.secrets,
		bootstrap.Account{FullName: bootstrapAdminName, Email: bootstrapAdminEmail},
		bootstrap.Ops{
			Login: func(ctx context.Context, acct bootstrap.Account, password string) (string, error) {
				return c.api.login(ctx, acct.Email, password)
			},
			Create: func(ctx context.Context, acct bootstrap.Account, password string) error {
				return c.api.createAdmin(ctx, acct.FullName, acct.Email, password)
			},
		})
	return err
}

// ensureMountMarkers creates the upload subfolders and .immich marker files
// when absent (Immich writes the current timestamp into the marker; any
// content satisfies its read-back verification).
func ensureMountMarkers(dataPath string, logger *slog.Logger) error {
	for _, folder := range mountFolders {
		dir := filepath.Join(dataPath, "upload", folder)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating mount folder %s: %w", folder, err)
		}
		marker := filepath.Join(dir, ".immich")
		if _, err := os.Stat(marker); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("checking mount marker %s: %w", marker, err)
		}
		//nolint:forbidigo // create-only-if-absent marker; managedfile.Write
		// compares content and would rewrite (and report a change) every pass.
		if err := os.WriteFile(marker, []byte(strconv.FormatInt(time.Now().UnixMilli(), 10)), 0644); err != nil {
			return fmt.Errorf("writing mount marker %s: %w", marker, err)
		}
		logger.Info("created Immich mount marker", "folder", folder, "path", marker)
	}
	return nil
}

// --- Config file ---

// renderConfigFile renders the Immich YAML config file. Only keys that
// override defaults are set; Immich merges the file over its built-in
func renderConfigFile(oidc *configurator.OIDCOutput) string {
	return fmt.Sprintf(`# Generated by Bloud - DO NOT EDIT MANUALLY
# Managed by the Bloud host agent (immich configurator).
oauth:
  enabled: true
  autoLaunch: true
  autoRegister: true
  # The issuer is served over plain HTTP in dev environments; openid-client
  # refuses insecure issuers unless this is enabled.
  allowInsecureRequests: true
  issuerUrl: %q
  clientId: %q
  clientSecret: %q
  scope: "openid email profile"
  # The default claim (preferred_username) collides with the bootstrap
  # admin, whose storage label is hardcoded to "admin". Derive the label
  # from the email instead so every SSO user gets a unique label.
  storageLabelClaim: email
passwordLogin:
  # SSO is the only login path.
  enabled: false
`, oidc.IssuerURL, oidc.ClientID, oidc.ClientSecret)
}
