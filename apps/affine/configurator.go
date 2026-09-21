// SPDX-License-Identifier: AGPL-3.0-only

package affine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const appName = "affine"

// bootstrapAdmin is the internal-only owner account the configurator creates
// through AFFiNE's first-run setup endpoint. Until it exists, every request
// is redirected to /admin/setup, which would block all end users. End users
// authenticate via SSO; this account is never exposed and its password is not
// shared.
const (
	// The domain must carry a TLD: AFFiNE validates the address with zod's
	// default email regex, which rejects single-label domains like
	// "localhost". affine.localhost is the app's own (unroutable-elsewhere)
	// internal domain.
	bootstrapAdminEmail = "bloud-admin@affine.localhost"
	bootstrapAdminName  = "Bloud Admin"
)

// configFileName is the AFFiNE application config file written in PreStart
// and mounted into the server container at /root/.affine/config/config.json.
const configFileName = "config.json"

// Configurator handles AFFiNE configuration: it writes the application
// config (public URL + OIDC provider) before the server starts (PreStart),
// bootstraps the first-run owner account, and verifies the OIDC login
// round-trip is wired up after the server starts (PostStart). SSO users are
// created on first login by AFFiNE (auth.allowSignupForOauth is on by
// default), so the only account the configurator manages is the internal
// owner.
type Configurator struct {
	port       int
	ssoBaseURL func() string // current Bloud base URL (host-set aware; read on every PreStart)
	secrets    configurator.AppSecretsProvider
	logger     *slog.Logger
	api        *affineAPI

	// baseURL is a test seam: when set, the API client resolves to it
	// instead of localhost:port. Never used to build request URLs by hand.
	baseURL string
}

// NewConfigurator creates a new AFFiNE configurator from the host Deps.
// deps.PrimaryBaseURL supplies the current Bloud base URL (e.g.
// "http://localhost:8080"); the app's public URL is derived from it the same
// way routes and OIDC redirect URIs are (affine.<host>). It is a function so
// host changes made in the UI take effect without re-registering.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = 3010
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:       port,
		ssoBaseURL: deps.PrimaryBaseURL,
		secrets:    deps.Secrets,
		logger:     logger.With("app", "affine"),
	}
	c.api = newAPI(deps.HTTP, func() string {
		if c.baseURL != "" {
			return c.baseURL
		}
		return fmt.Sprintf("http://localhost:%d", c.port)
	})
	return c
}

func (c *Configurator) Name() string {
	return "apps-affine"
}

// appExternalURL returns the public URL the browser uses to reach AFFiNE,
// e.g. "http://affine.localhost:8080". It must match the OIDC redirect URI
// base registered by the host-agent (app subdomain + callbackPath).
func (c *Configurator) appExternalURL() string {
	return configurator.AppExternalURL(c.ssoBaseURL, appName)
}

// PreStart writes the AFFiNE config file so the server comes up with the
// correct public URL and OIDC provider on the very first boot. Returns
// configChanged=true when the file content changed so the orchestrator
// recreates the container.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (bool, error) {
	path := filepath.Join(state.DataPath, "config", configFileName)
	content, err := renderConfigFile(c.appExternalURL(), state.OIDC)
	if err != nil {
		return false, err
	}
	changed, err := managedfile.Write(path, []byte(content), 0600)
	if err != nil {
		return false, fmt.Errorf("writing config file: %w", err)
	}
	if changed {
		c.logger.Info("wrote AFFiNE config file", "path", path, "sso", state.OIDC != nil)
	}
	return changed, nil
}

// PostStart verifies the server answers, creates the first-run owner
// account when the instance is uninitialized, and (when SSO is configured)
// verifies the OIDC provider is live: a preflight request must return the
// authorization URL, which proves config.json loaded, issuer discovery
// succeeded, and the PKCE flow is ready. Idempotent on every reconciliation.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	if err := c.api.waitServer(ctx); err != nil {
		return fmt.Errorf("waiting for affine server: %w", err)
	}

	if err := c.ensureBootstrapAdmin(ctx); err != nil {
		return fmt.Errorf("bootstrapping owner account: %w", err)
	}

	if state.OIDC == nil {
		return nil
	}
	if err := c.api.waitForOIDCPreflight(ctx); err != nil {
		return fmt.Errorf("verifying OIDC provider: %w", err)
	}
	c.logger.Info("OIDC login flow verified", "issuer", state.OIDC.IssuerURL)
	return nil
}

// ensureBootstrapAdmin creates the first-run owner account when the instance
// is uninitialized. AFFiNE only accepts the call before any user exists and
// answers "First user already created" otherwise, which is the idempotency
// signal for subsequent reconciliation passes.
func (c *Configurator) ensureBootstrapAdmin(ctx context.Context) error {
	if c.secrets == nil {
		return fmt.Errorf("no secrets provider")
	}
	password, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		return fmt.Errorf("generating admin password: %w", err)
	}

	changed, err := c.api.ensureOwner(ctx, bootstrapAdminName, bootstrapAdminEmail, password)
	if err != nil {
		return err
	}
	if changed {
		c.logger.Info("owner account created")
	}
	return nil
}

// --- Config file ---

// renderConfigFile renders the AFFiNE config.json. Only keys that override
// defaults are set; AFFiNE merges the file over its built-in defaults.
func renderConfigFile(externalURL string, oidc *configurator.OIDCOutput) (string, error) {
	cfg := map[string]any{
		"server": map[string]any{
			"externalUrl": externalURL,
		},
	}
	if oidc != nil {
		cfg["oauth"] = map[string]any{
			"providers": map[string]any{
				"oidc": map[string]any{
					"clientId":            oidc.ClientID,
					"clientSecret":        oidc.ClientSecret,
					"issuer":              oidc.IssuerURL,
					"allowPrivateNetwork": true,
				},
			},
		}
	}
	// json.Marshal sorts map keys alphabetically, so the rendering is
	// deterministic: an unchanged config never churns the file across
	// reconciliation cycles.
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("rendering config file: %w", err)
	}
	return string(out) + "\n", nil
}
