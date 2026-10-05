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
	// binding, not a static value metadata.yaml can carry) and execs the
	// package already installed in the persistent prefix.
	configFileName = "run.sh"

	// entrypointFileName is the script the container entrypoint runs ahead of
	// the gateway: it installs the pinned package into the persistent prefix
	// once, then execs supergateway with the flags metadata.yaml passes on.
	entrypointFileName = "entrypoint.sh"

	// runtimeManifestName is the npm manifest that pins the bridged package.
	// It is a managed file so the pin reads as a declared artifact in the
	// app's data tree rather than as a fragment of a shell string.
	runtimeManifestName = "package.json"

	// runtimeDir is the subdirectory of the app data tree that holds the npm
	// prefix, and runtimePrefix its container path. The prefix outlives the
	// container, so the install is once per pin, not once per spawn.
	runtimeDir          = "runtime"
	runtimePrefix       = "/runtime"
	runtimeManifestPath = runtimeDir + "/" + runtimeManifestName

	// caldavPackage and caldavVersion are the pinned npm coordinates. The
	// version is exact, not a range, so what installs is what the pin names.
	caldavPackage = "caldav-mcp"
	caldavVersion = "0.10.0"

	// caldavPin is the coordinate as it reads in logs and in the manifest.
	caldavPin = caldavPackage + "@" + caldavVersion

	// caldavEntry is the package's own CLI entrypoint inside the prefix: the
	// file run.sh execs directly, with no resolver in front of it.
	caldavEntry = runtimePrefix + "/node_modules/" + caldavPackage + "/dist/index.js"

	// httpTokenKey is the secret name the `mcp` contract carries. It must match
	// the contract registry and metadata.yaml's provides.mcp.secrets.
	httpTokenKey = "httpToken"
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

// PreStart writes the three files the container reads at start: the npm manifest
// that pins the bridged server, the entrypoint that installs it once into the
// persistent prefix, and the stdio run script that exports the CalDAV
// environment and execs the installed entrypoint directly.
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

	runScript, err := renderRunScript(baseURL, hasAddress, cred, hasCred)
	if err != nil {
		return configurator.NoRestart(), err
	}

	files := []struct {
		path    string
		content string
	}{
		{filepath.Join(state.DataPath, runtimeManifestPath), renderRuntimeManifest()},
		{filepath.Join(state.DataPath, "config", entrypointFileName), renderEntrypointScript()},
		{filepath.Join(state.DataPath, "config", configFileName), runScript},
	}

	changed := false
	for _, f := range files {
		wrote, err := managedfile.Write(f.path, []byte(f.content), managedfile.ModeSharedConfig)
		if err != nil {
			return configurator.NoRestart(), fmt.Errorf("writing %s config: %w", appName, err)
		}
		changed = changed || wrote
	}
	if !changed {
		return configurator.NoRestart(), nil
	}
	c.logger.Info("wrote caldav-mcp config", "dir", filepath.Join(state.DataPath, "config"),
		"address", hasAddress, "credential", hasCred)
	return configurator.MustRestart("caldav-mcp config rewritten"), nil
}

// PostStart verifies the bridged server answers the MCP handshake. The gateway's
// own /healthz is not enough: it returns 200 while the stdio child is absent,
// dead, or still resolving, so a probe against it promotes a node that cannot
// serve a single MCP request. This POSTs a real `initialize` to the same path a
// harness calls and requires the server's own identity back.
//
// Because PostStart also runs on every resync pass, a bridge that stops serving
// after it converged is surfaced rather than left silently up.
func (c *Configurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if err := c.api.waitServing(ctx); err != nil {
		return fmt.Errorf("waiting for the caldav-mcp server: %w", err)
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
	fmt.Fprintf(&b, "exec node %s\n", caldavEntry)
	return b.String(), nil
}

// renderRuntimeManifest renders the npm manifest that pins the bridged server.
// The version is exact rather than a range, so the install resolves the pin and
// nothing looser. It is written by hand rather than marshalled so the bytes are
// stable and reviewable: a map marshal would reorder on any key change and make a
// no-op resync look like a rewrite.
func renderRuntimeManifest() string {
	return fmt.Sprintf(`{
  "name": "bloud-%s-runtime",
  "private": true,
  "dependencies": {
    "%s": "%s"
  }
}
`, caldavPackage, caldavPackage, caldavVersion)
}

// renderEntrypointScript renders the container entrypoint: the one-time install
// of the pinned package into the persistent prefix, then the gateway.
//
// The install belongs here rather than in the stdio command because supergateway
// binds its port only after this script execs it, so a reachable port means the
// bridged package is already on disk. In run.sh it would land on the first MCP
// session's critical path, which is the cost this removes: `npx -y` resolves the
// package against the registry on every spawn, and that round trip exceeded the
// MCP client's connect timeout.
//
// The marker is named for the pinned version so a bump reinstalls rather than
// silently reusing the previous tree, and the entry file is checked alongside it
// so a half-populated prefix reinstalls instead of exec'ing a missing file.
func renderEntrypointScript() string {
	return fmt.Sprintf(`#!/bin/sh
# Generated by Bloud; rewritten on every reconciliation.
#
# Installs the pinned MCP server into the persistent prefix once, then execs the
# gateway with the flags metadata.yaml passes to this container.
set -eu

PREFIX='%s'
MARKER="$PREFIX/.installed-%s"
ENTRY='%s'

if [ ! -f "$MARKER" ] || [ ! -f "$ENTRY" ]; then
  echo "caldav-mcp: installing %s into $PREFIX" >&2
  if ! npm install --prefix "$PREFIX" --no-audit --no-fund --loglevel error; then
    echo "caldav-mcp: %s failed to install; the MCP server cannot start" >&2
    exit 1
  fi
  rm -f "$PREFIX"/.installed-*
  touch "$MARKER"
  echo "caldav-mcp: %s installed" >&2
fi

exec supergateway "$@"
`, runtimePrefix, caldavVersion, caldavEntry, caldavPin, caldavPin, caldavPin)
}
