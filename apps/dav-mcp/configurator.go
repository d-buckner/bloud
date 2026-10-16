// SPDX-License-Identifier: AGPL-3.0-only

// Package davmcp wires dav-mcp (PhilflowIO/dav-mcp) into Bloud as an MCP
// tool server for the calendars and contacts Radicale already serves.
//
// dav-mcp serves streamable HTTP itself, so there is no bridge process in front
// of it: this app is one container that speaks MCP on one port and talks
// CalDAV/CardDAV out to Radicale. The wrapper is not a browser and cannot join
// the identity provider, so it authenticates to Radicale with the
// caldav-service account credential Bloud provisions and publishes under
// Radicale's `appApi` offer; the address comes from the `caldav` contract.
//
// The image reads its configuration from process environment variables and
// nothing else, so the resolved bindings reach it through a generated env file
// declared as the container's `envFile`. That is the same shape apps/affine-mcp
// uses to hand over a resolved base URL; the sink differs only because this
// image cannot read a config file.
package davmcp

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
	"codeberg.org/d-buckner/bloud/pkg/mcp"
	"codeberg.org/d-buckner/bloud/pkg/mcpwrapper"
)

const (
	// appName is the catalog ID (and secrets/registry key) for this app.
	appName = "dav-mcp"
	// nodeName is the graph node and container name; see NodeLifecycle.Name.
	nodeName = "apps-dav-mcp"
	// defaultPort is the server's bind port and the host-published port. It
	// must match metadata.yaml's `port`. 9333 stays clear of the other
	// published ports.
	defaultPort = 9333

	// configDir is the subdirectory of the app data tree that holds the
	// generated env file, and envFileName the file itself. The directory is
	// not mounted into the container: the runtime reads the file on the host
	// and passes its values in at create time.
	configDir   = "config"
	envFileName = "env"

	// The environment variable names dav-mcp reads. Kept as constants so the
	// renderer and the tests name the same keys the upstream image expects.
	envServerURLKey = "CALDAV_SERVER_URL"
	envUsernameKey  = "CALDAV_USERNAME"
	envPasswordKey  = "CALDAV_PASSWORD"
	envBearerKey    = "BEARER_TOKEN"

	// httpTokenKey is the secret name the `mcp` contract carries. It must match
	// the contract registry and metadata.yaml's provides.mcp.secrets.
	httpTokenKey = "httpToken"

	// mcpEndpoint is the streamable-HTTP path the server exposes.
	mcpEndpoint = "/mcp"
)

// NewConfigurator builds the wrapper's configurator. The shared half (bearer
// mint, file write, handshake probe) lives in pkg/mcpwrapper; what is here is
// the app-specific half: the CalDAV bindings and the env-file renderer.
func NewConfigurator(port int, deps configurator.Deps) *mcpwrapper.Wrapper {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("app", appName)

	return mcpwrapper.New(port, deps, mcpwrapper.Config{
		App:         appName,
		Node:        nodeName,
		DefaultPort: defaultPort,
		TokenKey:    httpTokenKey,
		Mode:        managedfile.ModeSharedConfig,
		Path: func(state *configurator.AppState) string {
			return filepath.Join(state.DataPath, configDir, envFileName)
		},
		Render: func(state *configurator.AppState, token string) (string, error) {
			baseURL, hasAddress := caldavBaseURL(state)
			cred, hasCred := caldavCredential(state)
			if !hasAddress {
				logger.Warn("dav-mcp: no CalDAV address bound yet; writing a config without one")
			}
			if !hasCred {
				logger.Warn("dav-mcp: no published caldav-service credential yet; writing an unauthenticated config")
			}
			if token == "" {
				logger.Warn("dav-mcp: no MCP bearer available; the server will refuse every request")
			}
			return renderEnvFile(baseURL, hasAddress, cred, hasCred, token), nil
		},
		Probe: func(ctx context.Context, cl *appclient.Client, bearer string) error {
			return mcp.Probe(ctx, cl, mcpEndpoint, bearer, 30*time.Second)
		},
	})
}

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
		return strings.TrimSuffix(b.BaseURL, "/") + "/", true
	}
	return "", false
}

// caldavCredential returns the caldav-service account credential from the
// `appApi` binding. A binding missing its password or username is the
// provider's "not published yet" state, not a usable credential.
func caldavCredential(state *configurator.AppState) (bindings.AppAPIBinding, bool) {
	if state == nil {
		return bindings.AppAPIBinding{}, false
	}
	for _, b := range state.Integrations.AppAPIs {
		if !b.Installed || b.Username == "" || !b.Password.Published() {
			continue
		}
		return b, true
	}
	return bindings.AppAPIBinding{}, false
}

// --- config rendering ---

// renderEnvFile renders the env file the container is created from: one
// KEY=value line per setting, single-quoted. Only values that resolved are
// written, so an unresolved binding is an absent variable rather than an empty
// one, which the server reads differently.
//
// A value containing a quote or a line break is rejected rather than escaped.
// Every value here is generated (a base64url token) or resolved from catalog
// metadata (a container URL, an account name), so none should ever contain
// one; failing loudly beats writing a file whose quoting no longer means what it
// says.
func renderEnvFile(baseURL string, hasAddress bool, cred bindings.AppAPIBinding, hasCred bool, token string) string {
	var b strings.Builder
	b.WriteString("# Generated by Bloud; rewritten on every reconciliation.\n")

	settings := make([][2]string, 0, 4)
	if hasAddress {
		settings = append(settings, [2]string{envServerURLKey, baseURL})
	}
	if hasCred {
		settings = append(settings, [2]string{envUsernameKey, cred.Username})
		settings = append(settings, [2]string{envPasswordKey, cred.Password.String()})
	}
	if token != "" {
		settings = append(settings, [2]string{envBearerKey, token})
	}
	for _, kv := range settings {
		if strings.ContainsAny(kv[1], "'\r\n") {
			// Never echo the value: one of these keys is a password.
			fmt.Fprintf(&b, "# %s omitted: contains a quote or line break\n", kv[0])
			continue
		}
		fmt.Fprintf(&b, "%s='%s'\n", kv[0], kv[1])
	}
	return b.String()
}
