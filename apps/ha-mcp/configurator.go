// SPDX-License-Identifier: AGPL-3.0-only

// Package hamcp configures the standalone Home Assistant MCP server (ha-mcp) as
// a Bloud app.
//
// The app is two containers, and so two graph nodes with two configurators:
//
//   - apps-ha-mcp, the MCP server itself. Its PreStart writes the env file the
//     container is created from, because the image reads its whole configuration
//     from process environment and nothing else, and three of the values it
//     needs are resolved bindings or generated secrets rather than static
//     metadata.
//   - apps-ha-mcp-gate, an nginx front that makes the bearer the `mcp` contract
//     promises actually enforced. Its PreStart writes the nginx config, and its
//     PostStart is where the composed endpoint is proven, because the gate is the
//     last node to come up and the only address anything outside this app can
//     dial.
//
// The two nodes share two secrets, both minted here: the bearer consumers present
// and the high-entropy path the server listens on. See INTEGRATION.md for what
// each one is the boundary for.
package hamcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
)

const (
	appName = "ha-mcp"

	// nodeName is the MCP server container; gateNodeName is the bearer front.
	// Both are graph nodes, and each has its own configurator: the graph sees
	// containers, not apps.
	nodeName     = "apps-ha-mcp"
	gateNodeName = "apps-ha-mcp-gate"

	defaultPort = 8086

	// configDir is the subdirectory of the app data tree holding everything
	// this app generates. Both containers read out of it, and neither mounts
	// the env file: the runtime reads that on the host and passes the values in
	// at create time.
	configDir   = "config"
	envFileName = "env"

	// The environment variable names ha-mcp reads. Kept as constants so the
	// renderer and the tests name the same keys the upstream image expects.
	envURLKey   = "HOMEASSISTANT_URL"
	envTokenKey = "HOMEASSISTANT_TOKEN"
	envPathKey  = "MCP_SECRET_PATH"

	// httpTokenKey is the secret name the `mcp` contract carries: the bearer a
	// consumer presents and the gate compares.
	httpTokenKey = "httpToken"

	// endpointPathKey is not a contract secret: nothing outside this app is told
	// it. It is the path the server actually listens on, which the gate maps the
	// published path onto. The image's own security model treats this value as
	// the credential, so it is generated with the same care as the bearer even
	// though no consumer needs to know it.
	endpointPathKey = "endpointPath"

	// endpointPathPrefix is the literal opening of the internal path. It is kept
	// recognisable on purpose: a server log line shows which path was called,
	// and "mcp-" says what it is without saying anything secret.
	endpointPathPrefix = "/mcp-"
)

// Configurator drives the MCP server container: write the env file it is created
// from, and nothing else. Readiness is the gate's to prove, because the server
// publishes no port this process can dial.
type Configurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
}

// NewConfigurator builds the server node's configurator.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Configurator{
		port:    port,
		secrets: deps.Secrets,
		logger:  logger.With("app", appName, "node", nodeName),
	}
}

// Name returns the graph node this configurator owns.
func (c *Configurator) Name() string { return nodeName }

// Port is the app port this node was constructed with, which is the catalog
// `port:` when registration passed 0. The conformance harness compares it
// against metadata.yaml, because only one of the two routes traffic.
func (c *Configurator) Port() int { return c.port }

// PreStart writes the env file the container spec declares as its envFile. It
// carries the three values `environment:` cannot render: the Home Assistant
// address from the `appApi` binding, the long-lived token from that binding's
// secret, and the internal endpoint path this app mints.
//
// A binding that has not resolved yet is written as absent rather than as an
// empty value, so "not ready" stays distinguishable from "configured with
// nothing". The image refuses to start on an empty HOMEASSISTANT_TOKEN, and that
// is the right outcome: the graph orders Home Assistant first, so an absent
// credential here means Home Assistant has not published its token yet, and the
// next reconciliation pass fixes it.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	_, endpointPath, err := ensureCredentials(c.secrets, c.logger)
	if err != nil {
		return configurator.NoRestart(), err
	}

	baseURL, hasURL := homeAssistantURL(state)
	token, hasToken := homeAssistantToken(state)
	content, err := renderEnvFile(baseURL, hasURL, token, hasToken, endpointPath)
	if err != nil {
		return configurator.NoRestart(), err
	}

	envPath := filepath.Join(state.DataPath, configDir, envFileName)
	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		return configurator.NoRestart(), fmt.Errorf("creating the %s config directory: %w", appName, err)
	}
	changed, err := managedfile.Write(envPath, []byte(content), managedfile.ModeSharedConfig)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing %s config: %w", appName, err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}
	c.logger.Info("wrote ha-mcp config", "path", envPath,
		"home_assistant", hasURL, "token", hasToken, "endpoint_path", endpointPath != "")
	return configurator.MustRestart("ha-mcp config rewritten"), nil
}

// PostStart does nothing on this node. The server publishes no port, so there is
// nothing this process can reach: the only address into this app is the gate's,
// and the gate is a dependent node that has not started yet when this phase runs.
// apps-ha-mcp-gate's PostStart proves the endpoint once both exist.
func (c *Configurator) PostStart(_ context.Context, _ *configurator.AppState) error {
	return nil
}

// --- shared credentials ---

// ensureCredentials returns the bearer consumers present and the internal
// endpoint path, generating and publishing either on first use.
//
// Both containers need both values, and either node's PreStart may run first, so
// this is idempotent by construction: read, and mint only what is missing.
// Without a secrets provider nothing can be stored, and the caller renders the
// "not ready" shape rather than inventing a value that would change on every
// pass.
func ensureCredentials(secrets configurator.AppSecretsProvider, logger *slog.Logger) (bearer, endpointPath string, err error) {
	if secrets == nil {
		return "", "", nil
	}
	if bearer = secrets.GetAppSecret(appName, httpTokenKey); bearer == "" {
		bearer, err = randomToken()
		if err != nil {
			return "", "", fmt.Errorf("generating the %s MCP bearer: %w", appName, err)
		}
		if err := secrets.SetAppSecret(appName, httpTokenKey, bearer); err != nil {
			return "", "", fmt.Errorf("publishing the %s MCP bearer: %w", appName, err)
		}
		logger.Info("generated the ha-mcp MCP bearer")
	}
	if endpointPath = secrets.GetAppSecret(appName, endpointPathKey); endpointPath == "" {
		path, err := randomToken()
		if err != nil {
			return bearer, "", fmt.Errorf("generating the %s endpoint path: %w", appName, err)
		}
		endpointPath = endpointPathPrefix + path
		if err := secrets.SetAppSecret(appName, endpointPathKey, endpointPath); err != nil {
			return bearer, "", fmt.Errorf("publishing the %s endpoint path: %w", appName, err)
		}
		logger.Info("generated the ha-mcp internal endpoint path")
	}
	return bearer, endpointPath, nil
}

// currentBearer reads the bearer without generating one, so a probe always
// presents whatever is current even if a rotation landed between the two phases.
func currentBearer(secrets configurator.AppSecretsProvider) string {
	if secrets == nil {
		return ""
	}
	return secrets.GetAppSecret(appName, httpTokenKey)
}

// randomToken returns 32 bytes of entropy as URL-safe base64. The character set
// matters as much as the entropy here: both consumers of this value put it into
// a config file (an nginx string comparison and an env line), and base64url needs
// no quoting in either.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// --- bindings ---

// homeAssistantURL returns the address the MCP server dials for its target: the
// container address of the Home Assistant node. The server-side call is the
// whole reason this is the container address and not the browser-facing one.
func homeAssistantURL(state *configurator.AppState) (string, bool) {
	if state == nil {
		return "", false
	}
	for _, b := range state.Integrations.AppAPIs {
		if !b.Installed || b.BaseURL == "" {
			continue
		}
		return b.BaseURL, true
	}
	return "", false
}

// homeAssistantToken returns the long-lived access token Home Assistant
// published under its `appApi` offer. An empty password is the provider's "not
// published yet", never a usable credential.
func homeAssistantToken(state *configurator.AppState) (string, bool) {
	if state == nil {
		return "", false
	}
	for _, b := range state.Integrations.AppAPIs {
		if !b.Installed || !b.Password.Published() {
			continue
		}
		return b.Password.String(), true
	}
	return "", false
}

// --- config rendering ---

// renderEnvFile renders the env file the container is created from: one
// KEY=value line per setting, single-quoted. Only values that resolved are
// written, so an unresolved binding is an absent variable rather than an empty
// one, which the server reads differently: an absent HOMEASSISTANT_TOKEN is a
// configuration error it reports, an empty one is a credential it will try.
//
// A value that cannot be quoted is an error rather than an escape. Every value
// here is generated (a base64url token) or resolved from catalog metadata (a
// container URL), so none should ever contain a quote or a line break; a file
// whose quoting no longer means what it says is worse than no file.
func renderEnvFile(baseURL string, hasURL bool, token string, hasToken bool, endpointPath string) (string, error) {
	var b strings.Builder
	b.WriteString("# Generated by Bloud; rewritten on every reconciliation.\n")

	settings := make([][2]string, 0, 3)
	if hasURL {
		settings = append(settings, [2]string{envURLKey, baseURL})
	}
	if hasToken {
		settings = append(settings, [2]string{envTokenKey, token})
	}
	if endpointPath != "" {
		settings = append(settings, [2]string{envPathKey, endpointPath})
	}
	for _, s := range settings {
		if !safeSingleQuoted(s[1]) {
			return "", fmt.Errorf("refusing to write %s: its value cannot be quoted on one line", s[0])
		}
		fmt.Fprintf(&b, "%s='%s'\n", s[0], s[1])
	}
	return b.String(), nil
}

// safeSingleQuoted reports whether a value can sit inside single quotes on one
// line: no single quote, no line break, no carriage return.
func safeSingleQuoted(v string) bool {
	return !strings.ContainsAny(v, "'\n\r")
}
