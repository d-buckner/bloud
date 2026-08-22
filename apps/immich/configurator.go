// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
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
}

// NewConfigurator creates a new Immich configurator.
func NewConfigurator(port int, secrets configurator.AppSecretsProvider, logger *slog.Logger) *Configurator {
	if port == 0 {
		port = 2283
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Configurator{
		port:    port,
		secrets: secrets,
		logger:  logger.With("app", "immich"),
	}
}

func (c *Configurator) Name() string {
	return "apps-immich-server"
}

// PreStart writes the Immich config file with the native-oidc OAuth settings
// so OAuth is enabled from the very first boot. Returns configChanged=true
// when the file content changed so the orchestrator recreates the container.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (bool, error) {
	// Immich v3.1 crash-loops at startup when a .immich mount marker is
	// missing: the startup check only re-verifies (reads) markers whose pass
	// was already recorded in its database and never recreates missing ones.
	// Ensure them here — PreStart runs on every reconciliation cycle, so
	// this is idempotent and self-healing after any data-dir wipe.
	if err := ensureMountMarkers(state.DataPath, c.logger); err != nil {
		return false, err
	}

	if state.OIDC == nil {
		// SSO not configured for this app: leave Immich defaults in place.
		return false, nil
	}

	dir := filepath.Join(state.DataPath, "config")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("creating config directory: %w", err)
	}

	path := filepath.Join(dir, configFileName)
	content := renderConfigFile(state.OIDC)

	existing, _ := os.ReadFile(path)
	if bytes.Equal(existing, []byte(content)) {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return false, fmt.Errorf("writing config file: %w", err)
	}
	c.logger.Info("wrote Immich OAuth config file", "path", path)
	return true, nil
}

// Remove is a no-op for the Immich configurator; container and data removal
// are handled at a higher level by the orchestrator.
func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
}

// PostStart bootstraps the server admin. Immich shows a first-admin
// registration page until an admin exists, so SSO login would be unreachable
// without this step. Idempotent: skips when the admin already logs in.
func (c *Configurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if c.secrets == nil {
		return fmt.Errorf("no secrets provider")
	}
	password, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		return fmt.Errorf("generating admin password: %w", err)
	}

	if err := c.waitForServer(ctx); err != nil {
		return fmt.Errorf("waiting for immich server: %w", err)
	}

	// Fast path: admin already exists and the known password works.
	if _, err := c.login(ctx, bootstrapAdminEmail, password); err == nil {
		return nil
	}

	c.logger.Info("bootstrapping admin user")
	if err := c.createAdmin(ctx, password); err != nil {
		return fmt.Errorf("creating admin: %w", err)
	}
	c.logger.Info("admin user created")
	return nil
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

// --- Server API ---

func (c *Configurator) baseURL() string {
	return fmt.Sprintf("http://localhost:%d", c.port)
}

// waitForServer polls /api/server/ping until the server answers or the
// context is cancelled. The first boot runs database migrations, which can
// take a while.
func (c *Configurator) waitForServer(ctx context.Context) error {
	deadline := time.Now().Add(5 * time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+"/api/server/ping", nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("ping status %d: %s", resp.StatusCode, body)
		} else {
			lastErr = err
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("server did not become ready: %w", lastErr)
}

// createAdmin registers the first admin. Only works while no admin exists;
// Immich rejects it with 400 otherwise (which is fine — an admin is present).
// Success is 201 with the created user.
func (c *Configurator) createAdmin(ctx context.Context, password string) error {
	body, _ := json.Marshal(map[string]string{
		"name":     bootstrapAdminName,
		"email":    bootstrapAdminEmail,
		"password": password,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/api/auth/admin-sign-up", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusCreated:
		io.Copy(io.Discard, resp.Body)
		return nil
	case resp.StatusCode == http.StatusBadRequest:
		b, _ := io.ReadAll(resp.Body)
		// Idempotency: a previous run already created the admin (possibly with
		// a different password). Anything else is a real validation error.
		if strings.Contains(strings.ToLower(string(b)), "already has an admin") {
			return nil
		}
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	default:
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
}

// login exchanges credentials for an access token.
func (c *Configurator) login(ctx context.Context, email, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login failed: status %d", resp.StatusCode)
	}

	var out struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("empty token")
	}
	return out.AccessToken, nil
}
