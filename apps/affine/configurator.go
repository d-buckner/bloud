// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package affine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
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
}

// NewConfigurator creates a new AFFiNE configurator.
// ssoBaseURL supplies the current Bloud base URL (e.g. "http://localhost:8080");
// the app's public URL is derived from it the same way routes and OIDC
// redirect URIs are (affine.<host>). It is a function so host changes made in
// the UI take effect without re-registering the configurator.
func NewConfigurator(port int, ssoBaseURL func() string, secrets configurator.AppSecretsProvider, logger *slog.Logger) *Configurator {
	if port == 0 {
		port = 3010
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Configurator{
		port:       port,
		ssoBaseURL: ssoBaseURL,
		secrets:    secrets,
		logger:     logger.With("app", "affine"),
	}
}

func (c *Configurator) Name() string {
	return "apps-affine"
}

// appExternalURL returns the public URL the browser uses to reach AFFiNE,
// e.g. "http://affine.localhost:8080". It must match the OIDC redirect URI
// base registered by the host-agent (app subdomain + callbackPath).
func (c *Configurator) appExternalURL() string {
	baseURL := ""
	if c.ssoBaseURL != nil {
		baseURL = c.ssoBaseURL()
	}
	if baseURL == "" {
		return fmt.Sprintf("http://affine.localhost:8080")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" {
		return fmt.Sprintf("http://affine.localhost:8080")
	}
	parsed.Host = appName + "." + parsed.Host
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.RawPath = ""
	parsed.User = nil
	return strings.TrimSuffix(parsed.String(), "/")
}

// PreStart writes the AFFiNE config file so the server comes up with the
// correct public URL and OIDC provider on the very first boot. Returns
// configChanged=true when the file content changed so the orchestrator
// recreates the container.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (bool, error) {
	dir := filepath.Join(state.DataPath, "config")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("creating config directory: %w", err)
	}

	path := filepath.Join(dir, configFileName)
	content := renderConfigFile(c.appExternalURL(), state.OIDC)

	existing, _ := os.ReadFile(path)
	if bytes.Equal(existing, []byte(content)) {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return false, fmt.Errorf("writing config file: %w", err)
	}
	c.logger.Info("wrote AFFiNE config file", "path", path, "sso", state.OIDC != nil)
	return true, nil
}

// Remove is a no-op for the AFFiNE configurator; container and data removal
// are handled at a higher level by the orchestrator.
func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
}

// PostStart verifies the server answers, creates the first-run owner
// account when the instance is uninitialized, and — when SSO is configured —
// verifies the OIDC provider is live: a preflight request must return the
// authorization URL, which proves config.json loaded, issuer discovery
// succeeded, and the PKCE flow is ready. Idempotent on every reconciliation.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	if err := c.waitForServer(ctx); err != nil {
		return fmt.Errorf("waiting for affine server: %w", err)
	}

	if err := c.ensureBootstrapAdmin(ctx); err != nil {
		return fmt.Errorf("bootstrapping owner account: %w", err)
	}

	if state.OIDC == nil {
		return nil
	}
	if err := c.waitForOIDCPreflight(ctx); err != nil {
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

	body, _ := json.Marshal(map[string]string{
		"name":     bootstrapAdminName,
		"email":    bootstrapAdminEmail,
		"password": password,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/api/setup/create-admin-user", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		io.Copy(io.Discard, resp.Body)
		c.logger.Info("owner account created")
		return nil
	case http.StatusForbidden:
		b, _ := io.ReadAll(resp.Body)
		// Idempotency: the first user already exists (created by an earlier
		// pass or manually). Anything else is a real rejection.
		if strings.Contains(string(b), "First user already created") {
			return nil
		}
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	default:
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
}

// --- Config file ---

// renderConfigFile renders the AFFiNE config.json. Only keys that override
// defaults are set; AFFiNE merges the file over its built-in defaults.
func renderConfigFile(externalURL string, oidc *configurator.OIDCOutput) string {
	cfg := map[string]any{
		"server": map[string]any{
			"externalUrl": externalURL,
		},
	}
	if oidc != nil {
		cfg["oauth"] = map[string]any{
			"providers": map[string]any{
				"oidc": map[string]any{
					"clientId":          oidc.ClientID,
					"clientSecret":      oidc.ClientSecret,
					"issuer":            oidc.IssuerURL,
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
		return "" // unreachable: all values are marshalable
	}
	return string(out) + "\n"
}

// --- Server API ---

func (c *Configurator) baseURL() string {
	return fmt.Sprintf("http://localhost:%d", c.port)
}

// waitForServer polls /info (public, no auth) until the server answers or
// the context is cancelled. The first boot runs prisma migrations before
// the HTTP listener opens, which can take a while.
func (c *Configurator) waitForServer(ctx context.Context) error {
	deadline := time.Now().Add(5 * time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+"/info", nil)
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
			lastErr = fmt.Errorf("info status %d: %s", resp.StatusCode, body)
		} else {
			lastErr = err
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("server did not become ready: %w", lastErr)
}

// waitForOIDCPreflight polls the public OAuth preflight endpoint until it
// returns the authorization URL. The server validates the issuer
// asynchronously after boot (with backoff), so allow a generous window.
func (c *Configurator) waitForOIDCPreflight(ctx context.Context) error {
	deadline := time.Now().Add(3 * time.Minute)
	var lastErr error
	body := []byte(`{"provider":"OIDC","client":"web","client_nonce":"bloud-poststart-check"}`)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/api/oauth/preflight", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(data), `"url"`) {
				return nil
			}
			lastErr = fmt.Errorf("preflight status %d: %s", resp.StatusCode, data)
		} else {
			lastErr = err
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("OIDC preflight did not become ready: %w", lastErr)
}
