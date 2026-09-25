// SPDX-License-Identifier: AGPL-3.0-only

// Package vaultwarden configures Vaultwarden: it generates the app's
// environment file before the container starts (public URL, signup policy,
// OpenID Connect client) and verifies after start that the running app
// actually picked it up.
package vaultwarden

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const appName = "vaultwarden"

// envFileName is the dotenv file written in PreStart, mounted read-only into
// the container at /config/vaultwarden.env and selected by the ENV_FILE
// environment variable declared in metadata.yaml.
const envFileName = "vaultwarden.env"

// ssoScopes is the scope list Vaultwarden requests from the provider (openid is
// implicit: Vaultwarden adds it itself, so listing it would send it twice).
// offline_access is what lets the app keep the session alive with the
// provider's refresh token. It must contain every entry of metadata.yaml's
// sso.scopes, which is what makes the host-agent attach those scopes to the
// provider (a test pins the two together).
const ssoScopes = "email profile offline_access"

// devSwitchEnv is the host-agent environment variable that opts a development
// install into serving the web vault over plain HTTP (see devAllowHTTP).
const devSwitchEnv = "BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP"

// devSwitchKey is the line the generated env file carries when the switch is on.
// metadata.yaml's container command greps for exactly this line, so the two must
// agree (a test pins them together).
const devSwitchKey = "BLOUD_DEV_ALLOW_HTTP"

// Configurator handles Vaultwarden configuration.
type Configurator struct {
	port       int
	ssoBaseURL func() string // current Bloud base URL (host-set aware; read on every PreStart)
	logger     *slog.Logger
	api        *vaultwardenAPI

	// baseURL is a test seam: when set, the API client resolves to it instead
	// of localhost:port. Never used to build request URLs by hand.
	baseURL string

	// getenv reads the host-agent's environment (os.Getenv in production).
	getenv func(string) string
}

// NewConfigurator creates a Vaultwarden configurator from the host Deps.
// deps.PrimaryBaseURL supplies the current Bloud base URL (e.g.
// "http://localhost:8080"); the app's public URL is derived from it the same
// way routes and OIDC redirect URIs are (vaultwarden.<host>). It is a function
// so host changes made in the UI take effect without re-registering.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = 8222
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:       port,
		ssoBaseURL: deps.PrimaryBaseURL,
		logger:     logger.With("app", appName),
		getenv:     os.Getenv,
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
	return "apps-vaultwarden"
}

// appExternalURL returns the public URL the browser uses to reach Vaultwarden,
// e.g. "http://vaultwarden.localhost:8080". It is the app's DOMAIN, from which
// Vaultwarden derives its OIDC redirect URI, so it must match the redirect URI
// base the host-agent registers (app subdomain + callbackPath).
func (c *Configurator) appExternalURL() string {
	return configurator.AppExternalURL(c.ssoBaseURL, appName)
}

// devAllowHTTP reports whether this install has opted into the plain-HTTP dev
// switch: BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP is set on the host-agent AND the app's
// public host is a localhost name.
//
// Why it exists: the Bitwarden web client refuses to talk to any server whose URL
// is not https:// (unless its build-time isDev() is true), so a Bloud install
// that serves apps over plain HTTP cannot use the web vault at all. The switch
// makes the container flip that constant so development and browser tests can
// proceed until Bloud serves HTTPS. It is a security downgrade for a password
// manager, so it is off by default and refused for anything but localhost names:
// a real LAN or domain install can never be switched into it by an environment
// variable.
func (c *Configurator) devAllowHTTP(publicURL string) bool {
	switch strings.ToLower(strings.TrimSpace(c.getenv(devSwitchEnv))) {
	case "1", "true", "yes":
	default:
		return false
	}
	parsed, err := url.Parse(publicURL)
	host := ""
	if err == nil {
		host = parsed.Hostname()
	}
	if host != "localhost" && !strings.HasSuffix(host, ".localhost") {
		c.logger.Warn("ignoring "+devSwitchEnv+": the plain-HTTP dev switch is only honored for localhost names",
			"publicURL", publicURL)
		return false
	}
	return true
}

// PreStart writes the environment file so the container comes up with the right
// public URL, signup policy, and OIDC client on its very first boot. Returns
// configChanged=true when the file content changed so the orchestrator
// recreates the container.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	path := filepath.Join(state.DataPath, "config", envFileName)

	publicURL := c.appExternalURL()
	allowHTTP := c.devAllowHTTP(publicURL)
	content, err := renderEnv(publicURL, state.OIDC, allowHTTP)
	if err != nil {
		return configurator.NoRestart(), err
	}

	// Mode 0600: the file carries the OIDC client secret. Vaultwarden runs as
	// root inside the container, which under rootless podman is the same host
	// user that writes this file, so it can read it.
	changed, err := managedfile.Write(path, []byte(content), 0600)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing env file: %w", err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}
	c.logger.Info("wrote Vaultwarden env file", "path", path, "sso", state.OIDC != nil)
	if allowHTTP {
		c.logger.Warn("Vaultwarden web vault HTTPS enforcement is DISABLED by "+devSwitchEnv+" (development only)", "publicURL", publicURL)
	}
	return configurator.MustRestart("Vaultwarden env file rewritten"), nil
}

// PostStart verifies against the running app that the configuration took
// effect. Called on every reconciliation, so every check is a read or an
// idempotent probe. Vaultwarden needs no bootstrap: there is no admin API
// account (the admin panel stays disabled) and SSO users are created by the app
// on their first sign-in.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	if err := c.api.waitAlive(ctx); err != nil {
		return fmt.Errorf("waiting for vaultwarden: %w", err)
	}
	if state.OIDC == nil {
		return nil
	}

	token, err := c.api.ssoPrevalidate(ctx)
	if err != nil {
		return fmt.Errorf("verifying SSO is enabled in the running app: %w", err)
	}
	if err := c.api.probeAuthorize(ctx, c.appExternalURL(), token); err != nil {
		return fmt.Errorf("verifying OIDC authorization redirect: %w", err)
	}
	c.logger.Info("OIDC sign-in verified", "issuer", state.OIDC.IssuerURL)
	return nil
}

// Remove is a no-op for the Vaultwarden configurator; container and data
// removal are handled at a higher level by the orchestrator.
func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
}

// renderEnv renders vaultwarden.env. The image loads it as a dotenv file and its
// own healthcheck script sources it as shell, so values are single-quoted: both
// readers take that verbatim (no expansion, no escapes).
//
// Sign-in policy follows the SSO wiring. With SSO, the identity provider is the
// only way in: local self-registration is closed (SIGNUPS_ALLOWED=false) and
// master-password login is turned off (SSO_ONLY=true), which also removes the
// "Other" login button and the "Create account" link from the web vault. Neither
// stops identity provider users from being created on first sign-in (SSO signup
// is its own switch, SSO_SIGNUPS_ALLOWED, on by default), and SSO_ONLY refuses
// only the password login grant: the SSO code exchange and token refresh are
// unaffected, and the master password still unlocks the vault on the client.
// Without SSO there is no other way to get an account or sign in, so the app's
// defaults (signups open, password login on) are kept.
func renderEnv(publicURL string, oidc *configurator.OIDCOutput, allowHTTP bool) (string, error) {
	settings := [][2]string{
		{"DOMAIN", publicURL},
	}
	if allowHTTP {
		// Read by the container's command in metadata.yaml, not by Vaultwarden.
		settings = append(settings, [2]string{devSwitchKey, "true"})
	}
	if oidc != nil {
		settings = append(settings,
			[2]string{"SIGNUPS_ALLOWED", "false"},
			[2]string{"SSO_ENABLED", "true"},
			[2]string{"SSO_ONLY", "true"},
			// The provider's issuer URL, with its trailing slash: Vaultwarden
			// compares it to the issuer claim exactly, and Authentik's
			// per-provider issuer ends in a slash.
			[2]string{"SSO_AUTHORITY", oidc.IssuerURL},
			[2]string{"SSO_CLIENT_ID", oidc.ClientID},
			[2]string{"SSO_CLIENT_SECRET", oidc.ClientSecret},
			[2]string{"SSO_SCOPES", ssoScopes},
			[2]string{"SSO_PKCE", "true"},
		)
	}

	var b strings.Builder
	b.WriteString("# Generated by Bloud; the host-agent rewrites this file on every\n")
	b.WriteString("# reconciliation and restarts the container when its content changes.\n")
	for _, kv := range settings {
		if strings.ContainsRune(kv[1], '\'') {
			return "", fmt.Errorf("env value for %s contains a single quote", kv[0])
		}
		fmt.Fprintf(&b, "%s='%s'\n", kv[0], kv[1])
	}
	return b.String(), nil
}
