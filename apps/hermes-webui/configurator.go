// SPDX-License-Identifier: AGPL-3.0-only

// Package hermeswebui configures hermes-webui: it mints the client password
// the app hands to third-party clients, resolves the agent connection from the
// agentApi binding, and writes both plus the OIDC client into a dotenv file the
// container sources before the image's own init runs.
//
// The file is the delivery mechanism rather than the container environment
// because the values change and a container's environment does not. Writing
// them here means a rotation is a file rewrite plus a recreate, the secret never
// appears in the container spec, and a steady-state resync is a read-only diff
// that disturbs nothing.
package hermeswebui

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const appName = "hermes-webui"

// nodeName is this app's graph node and container name.
const nodeName = "apps-hermes-webui"

// defaultPort is the app's own web port, matching metadata.yaml's `port`.
const defaultPort = 8787

// envFileName is the dotenv file written into the app's config dir by PreStart
// and sourced by the container command before the image's init runs.
const envFileName = "bloud.env"

// clientPasswordSecret is the secret key this app publishes under its
// clientPassword contract. It is the value a user reveals once and pastes into
// a mobile client.
const clientPasswordSecret = "password"

// clientPasswordBytes is the entropy of the minted password. 24 bytes renders
// as 32 base64url characters: long enough that it is not guessable and short
// enough that pasting it into a phone is not an event.
const clientPasswordBytes = 24

// oidcScopes is what the app requests from the provider. `email` is required
// because Bloud's native-oidc mapping is built on a verified email; without it
// the app cannot link the identity.
const oidcScopes = "openid email profile"

type Configurator struct {
	port       int
	ssoBaseURL func() string
	secrets    configurator.AppSecretsProvider
	logger     *slog.Logger
}

// NewConfigurator builds the configurator from host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Configurator{
		port:       port,
		ssoBaseURL: deps.PrimaryBaseURL,
		secrets:    deps.Secrets,
		logger:     logger.With("app", appName),
	}
}

func (c *Configurator) Name() string {
	return nodeName
}

// PreStart writes the app's per-install config: the minted client password,
// the agent gateway connection, and the OIDC client.
//
// It reports a recreate whenever the file changed. That is not conservatism:
// the values are sourced into the process environment at container start, so a
// rewritten file is inert until the container is created again. A pass that
// wrote and did not ask for the recreate would leave the app running with the
// previous password, which is the worst of both: the reveal shows one value and
// the app authenticates another.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	if state == nil {
		return configurator.NoRestart(), fmt.Errorf("%s: no app state", appName)
	}

	agent, err := c.resolveAgent(state)
	if err != nil {
		return configurator.NoRestart(), err
	}

	password, err := c.ensureClientPassword()
	if err != nil {
		return configurator.NoRestart(), err
	}

	content := c.renderEnv(agent, state, password)
	path := filepath.Join(state.DataPath, "config", envFileName)

	// managedfile.Write reports changed=false when the bytes already match, so
	// the steady-state resync writes nothing and asks for nothing.
	changed, err := managedfile.Write(path, []byte(content), managedfile.ModeHostOnly)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("%s: write %s: %w", appName, envFileName, err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}

	c.logger.Info("wrote app config, recreate required to pick it up",
		"path", path,
		"oidc", state.OIDC != nil,
		"gateway", agent.Endpoint != "")

	return configurator.MustRestart("app config changed; the values are sourced at container start"), nil
}

// PostStart confirms the app came up with auth actually engaged.
//
// This is a best-effort check that logs rather than fails. The app can be
// healthy and still have auth disabled if neither the password nor OIDC reached
// it, and that is worth a WARN on the log; failing the node for it would be
// terminal for a condition the next pass may well resolve.
func (c *Configurator) PostStart(_ context.Context, _ *configurator.AppState) error {
	c.logger.Info("post-start: client password is published under the clientPassword contract",
		"secret", clientPasswordSecret)
	return nil
}

// agentConfig is the resolved upstream the web UI dials.
type agentConfig struct {
	Endpoint string
	APIKey   string
}

// resolveAgent reads the agentApi binding out of the resolved integrations the
// orchestrator handed this node.
//
// An empty endpoint or key means the provider is not wired yet. The required
// integration orders Hermes ahead of this app on the graph, so by the time this
// runs the binding should be populated; if it is not, writing a half config
// would give the app a gateway URL that rejects every request, which reads as a
// broken app rather than as a provider that has not published its credential.
func (c *Configurator) resolveAgent(state *configurator.AppState) (agentConfig, error) {
	for _, binding := range state.Integrations.AgentAPIs {
		if binding.Endpoint != "" && binding.APIKey != "" {
			return agentConfig{Endpoint: binding.Endpoint, APIKey: binding.APIKey}, nil
		}
	}
	return agentConfig{}, fmt.Errorf(
		"%s: no wired agentApi provider; the web UI has no backend to dial", appName)
}

// ensureClientPassword returns the app's client password, minting and
// persisting one on first use.
//
// The value is stored through the secrets provider under the key the contract
// publishes, which is what makes it revealable: the reveal endpoint reads the
// same key, and the loader already proved that key is one this app is allowed to
// publish.
func (c *Configurator) ensureClientPassword() (string, error) {
	if c.secrets == nil {
		// CLI and conformance contexts have no store. The config is written
		// without a client password rather than failing, which leaves the app
		// on its OIDC path alone, and the next real pass fills it in. Failing
		// here would make an offline validation pass look like a broken app.
		c.logger.Warn("no secrets provider; writing app config without a client password")
		return "", nil
	}
	if existing := c.secrets.GetAppSecret(appName, clientPasswordSecret); existing != "" {
		return existing, nil
	}

	minted, err := randomToken(clientPasswordBytes)
	if err != nil {
		return "", fmt.Errorf("%s: generate client password: %w", appName, err)
	}
	if err := c.secrets.SetAppSecret(appName, clientPasswordSecret, minted); err != nil {
		return "", fmt.Errorf("%s: persist client password: %w", appName, err)
	}
	c.logger.Info("minted client password", "secret", clientPasswordSecret)
	return minted, nil
}

// renderEnv builds the dotenv file. Values are single-quoted so the shell that
// sources them takes them literally; the generator's alphabet and the URL and
// client-id values contain no single quotes, and escapeSingleQuote guards the
// case where a future value does.
func (c *Configurator) renderEnv(agent agentConfig, state *configurator.AppState, password string) string {
	var b strings.Builder
	b.WriteString("# Generated by Bloud. Do not edit: the next reconcile overwrites this.\n")

	writeEnv(&b, "HERMES_WEBUI_PASSWORD", password)
	writeEnv(&b, "HERMES_WEBUI_CHAT_BACKEND", "gateway")
	writeEnv(&b, "HERMES_WEBUI_GATEWAY_BASE_URL", agent.Endpoint)
	writeEnv(&b, "HERMES_WEBUI_GATEWAY_API_KEY", agent.APIKey)

	if state.OIDC != nil {
		writeEnv(&b, "HERMES_WEBUI_OIDC_ISSUER", state.OIDC.IssuerURL)
		writeEnv(&b, "HERMES_WEBUI_OIDC_CLIENT_ID", state.OIDC.ClientID)
		writeEnv(&b, "HERMES_WEBUI_OIDC_CLIENT_SECRET", state.OIDC.ClientSecret)
		writeEnv(&b, "HERMES_WEBUI_OIDC_REDIRECT_URI", state.OIDC.RedirectURI)
		writeEnv(&b, "HERMES_WEBUI_OIDC_SCOPES", oidcScopes)
	}

	// The session cookie is Secure whenever the instance address is https, which
	// is the only mode this app's OIDC path supports anyway. Sending it over plain
	// http would put a live session on the wire in cleartext.
	writeEnv(&b, "HERMES_WEBUI_SECURE", boolEnv(isHTTPS(c.currentBaseURL())))

	return b.String()
}

// writeEnv emits one quoted assignment. Empty values are skipped rather than
// written as empty, because the app reads an unset variable as "not configured"
// and an empty string as "configured with nothing", which are different states
// and only the first one is true here.
func writeEnv(b *strings.Builder, key, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "%s='%s'\n", key, escapeSingleQuote(value))
}

func escapeSingleQuote(v string) string {
	// POSIX single-quoted strings have no escapes; the idiom is to close, emit
	// an escaped quote, and reopen.
	return strings.ReplaceAll(v, "'", `'\''`)
}

func boolEnv(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func isHTTPS(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "https://")
}

func (c *Configurator) currentBaseURL() string {
	if c.ssoBaseURL == nil {
		return ""
	}
	return c.ssoBaseURL()
}

// randomToken returns n bytes of cryptographic randomness as unpadded
// base64url. The alphabet is alphanumeric plus '-' and '_', which is safe to
// single-quote in a shell and safe to paste on a phone.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
