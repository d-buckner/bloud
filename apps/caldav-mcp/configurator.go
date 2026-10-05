// SPDX-License-Identifier: AGPL-3.0-only

// Package caldavmcp wires caldav-mcp (io.github.dominik1001/caldav-mcp) into
// Bloud as an MCP tool server for the calendars Radicale already serves.
//
// caldav-mcp is a stdio MCP server, and Bloud's `mcp` contract is
// streamable-HTTP, so this app runs it behind supergateway, which bridges the
// two transports and enforces the bearer Bloud generates. The wrapper is not a
// browser and cannot join the identity provider, so it authenticates to
// Radicale with the caldav-service account credential Bloud provisions and
// publishes under Radicale's `appApi` offer; the address comes from the
// `caldav` contract. Radicale's rights model bounds what that account can read
// (the operator's tree, read-only).
package caldavmcp

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

const (
	// appName is the catalog ID (and secrets/registry key) for this app.
	appName = "caldav-mcp"
	// nodeName is the graph node and container name; see NodeLifecycle.Name.
	nodeName = "apps-caldav-mcp"
	// defaultPort is the server's bind port and the host-published port. It
	// must match metadata.yaml's `port`. 9333 stays clear of the other
	// catalog apps.
	defaultPort = 9333

	// configFileName is the shell script supergateway runs as its stdio server.
	// It exports the dynamic CALDAV_* environment (the password is a resolved
	// binding, not a static value metadata.yaml can carry) and execs the pinned
	// package.
	configFileName = "run.sh"

	// httpTokenKey is the secret name the `mcp` contract carries. It must match
	// the contract registry and metadata.yaml's provides.mcp.secrets.
	httpTokenKey = "httpToken"

	// caldavPackage is the pinned npm package the run script launches.
	caldavPackage = "caldav-mcp@0.10.0"

	// healthEndpoint is the gateway's liveness probe, which PostStart checks.
	healthEndpoint = "/healthz"
)

// Configurator handles the caldav-mcp node lifecycle: write the supergateway
// config file (PreStart) and verify the gateway is serving (PostStart).
type Configurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
	api     *mcpAPI

	// baseURL is a test seam: when set, the API client resolves to it instead
	// of localhost:port.
	baseURL string
}

// NewConfigurator creates a new caldav-mcp configurator from the host Deps.
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
		logger:  logger.With("app", appName),
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
	return nodeName
}

// PreStart writes the supergateway config so the container comes up bridging
// caldav-mcp over streamable HTTP and already enforcing Bloud's bearer.
//
// The bearer is generated on the first pass and reused afterwards, so a restart
// does not invalidate the namespace a harness registered. The CalDAV address
// and the caldav-service credential come from the resolved bindings; if the
// credential is not published yet the config is written without it and a later
// pass restarts the container once it lands.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	_, err := c.ensureHTTPToken()
	if err != nil {
		return configurator.NoRestart(), err
	}

	baseURL, hasAddress := caldavBaseURL(state)
	cred, hasCred := caldavCredential(state)
	if !hasAddress {
		c.logger.Warn("caldav-mcp: no CalDAV address bound yet; writing a config without one")
	}
	if !hasCred {
		c.logger.Warn("caldav-mcp: no published caldav-service credential yet; writing an unauthenticated config")
	}

	content, err := renderRunScript(baseURL, hasAddress, cred, hasCred)
	if err != nil {
		return configurator.NoRestart(), err
	}

	path := filepath.Join(state.DataPath, "config", configFileName)
	changed, err := managedfile.Write(path, []byte(content), managedfile.ModeSharedConfig)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing %s config: %w", appName, err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}
	c.logger.Info("wrote caldav-mcp config", "path", path, "address", hasAddress, "credential", hasCred)
	return configurator.MustRestart("caldav-mcp config rewritten"), nil
}

// PostStart verifies the gateway is serving its health endpoint. A wrapper that
// is not serving is not doing its job, so a failing probe is returned and the
// node is retried by the self-healing pass.
func (c *Configurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if err := c.api.waitReady(ctx); err != nil {
		return fmt.Errorf("waiting for the caldav-mcp gateway: %w", err)
	}
	c.logger.Info("caldav-mcp is serving MCP")
	return nil
}

// --- MCP bearer ---

// ensureHTTPToken returns the bearer this app validates, generating and
// publishing one on the first pass and reusing it afterwards.
func (c *Configurator) ensureHTTPToken() (string, error) {
	if c.secrets == nil {
		return "", nil
	}
	if existing := c.secrets.GetAppSecret(appName, httpTokenKey); existing != "" {
		return existing, nil
	}
	token, err := randomToken()
	if err != nil {
		return "", fmt.Errorf("generating the %s bearer: %w", appName, err)
	}
	if err := c.secrets.SetAppSecret(appName, httpTokenKey, token); err != nil {
		return "", fmt.Errorf("publishing the %s bearer: %w", appName, err)
	}
	c.logger.Info("generated the caldav-mcp MCP bearer")
	return token, nil
}

// randomToken returns 32 bytes of entropy as URL-safe base64.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// --- bindings ---

// caldavBaseURL returns the DAV root a machine client dials: the container
// address plus the provider's declared path. The server-side fetch is the whole
// reason this is the container address rather than the browser-facing one.
func caldavBaseURL(state *configurator.AppState) (string, bool) {
	if state == nil {
		return "", false
	}
	for _, b := range state.Integrations.CalDAVServers {
		if !b.Installed || b.BaseURL == "" {
			continue
		}
		return b.BaseURL + b.Path, true
	}
	return "", false
}

// caldavCredential returns the caldav-service account credential from the
// `appApi` binding. A binding missing its password or username is the
// provider's "not published yet" state, not a usable credential.
func caldavCredential(state *configurator.AppState) (configurator.AppAPIBinding, bool) {
	if state == nil {
		return configurator.AppAPIBinding{}, false
	}
	for _, b := range state.Integrations.AppAPIs {
		if !b.Installed || b.Username == "" || b.Password == "" {
			continue
		}
		return b, true
	}
	return configurator.AppAPIBinding{}, false
}

// --- config rendering ---

// renderRunScript renders the shell script supergateway runs as its stdio
// server. The CALDAV_* environment is exported here because the password is a
// resolved binding, not a static value metadata.yaml can carry; supergateway's
// stdio child inherits its environment, so the exports reach caldav-mcp. The
// values are single-quoted and rejected if they contain a quote or a newline.
func renderRunScript(baseURL string, hasAddress bool, cred configurator.AppAPIBinding, hasCred bool) (string, error) {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# Generated by Bloud; rewritten on every reconciliation.\n")

	settings := make([][2]string, 0, 3)
	if hasAddress {
		settings = append(settings, [2]string{"CALDAV_BASE_URL", baseURL})
	}
	if hasCred {
		settings = append(settings, [2]string{"CALDAV_USERNAME", cred.Username})
		settings = append(settings, [2]string{"CALDAV_PASSWORD", cred.Password})
	}
	for _, kv := range settings {
		if strings.ContainsAny(kv[1], "'\r\n") {
			return "", fmt.Errorf("config value for %s contains a quote or line break", kv[0])
		}
		fmt.Fprintf(&b, "export %s='%s'\n", kv[0], kv[1])
	}
	fmt.Fprintf(&b, "exec npx -y %s\n", caldavPackage)
	return b.String(), nil
}
