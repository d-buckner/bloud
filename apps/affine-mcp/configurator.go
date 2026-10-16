// SPDX-License-Identifier: AGPL-3.0-only

// Package affinemcp wires the third-party affine-mcp-server into Bloud.
//
// AFFiNE ships its own MCP server and provides the `mcp` contract directly; a
// wrapper is the wrong shape for a target that already has both a server and a
// scoped-credential mechanism. This app exists because that built-in server is
// read-only and narrow, while affine-mcp-server exposes a much larger
// read-write tool surface. So Bloud runs both: the app keeps `affine`'s
// namespace, and this wrapper adds `affine-mcp`'s, and a harness picks.
//
// The wrapper is not a browser and cannot join the identity provider, so it
// consumes the `appApi` contract instead: AFFiNE publishes the bootstrap
// owner's username and password, and this configurator writes them into the
// config file the image reads at startup. The wrapper's own MCP bearer is the
// reverse direction: Bloud generates it, stores it, writes it into the same
// file, and publishes it under `provides.mcp`, so a harness authenticates
// against a credential this app actually validates.
package affinemcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
	"codeberg.org/d-buckner/bloud/pkg/mcpwrapper"
)

const (
	// appName is the catalog ID (and secrets/registry key) for this app.
	appName = "affine-mcp"
	// nodeName is the graph node and container name; see NodeLifecycle.Name.
	nodeName = "apps-affine-mcp"
	// defaultPort is the port the server binds and the host publishes. It must
	// match metadata.yaml's `port`: the orchestrator routes to the published
	// host port, and the binding's BaseURL addresses the container on the same
	// number. 9222 is chosen to stay clear of the host-agent's own 3000.
	defaultPort = 9222

	// configDirName is the app's own data subdirectory that metadata.yaml mounts
	// at /data/config. Under it the image looks for `affine-mcp/config` because
	// XDG_CONFIG_HOME is set to the mount point.
	configDirName = "config"
	// configSubdir is the fixed directory the image appends to
	// XDG_CONFIG_HOME/affine-mcp/config.
	configSubdir = "affine-mcp"
	// configFileName is the saved-config filename (no extension).
	configFileName = "config"

	// httpTokenKey is the secret name the `mcp` contract carries. It must match
	// the contract registry and metadata.yaml's provides.mcp.secrets.
	httpTokenKey = "httpToken"

	// readyPath is the image's readiness probe. Unlike /healthz (process
	// liveness), it checks the configured AFFiNE GraphQL endpoint and returns
	// 503 with the failing component.
	readyPath = "/readyz"
)

// NewConfigurator builds the wrapper's configurator. The shared half (bearer
// mint, file write, readiness probe) lives in pkg/mcpwrapper; what is here is
// the app-specific half: where the file lives, how its bytes are rendered, and
// which probe proves the server can reach AFFiNE.
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
		// The image runs as its own non-root user, a subordinate uid under
		// rootless podman that is not the host uid writing this file, so 0600
		// would leave the server unable to read its own config.
		// ModeSharedConfig bounds the credential by the app's data directory
		// instead of by the file mode.
		Mode: managedfile.ModeSharedConfig,
		Path: func(state *configurator.AppState) string {
			return filepath.Join(state.DataPath, configDirName, configSubdir, configFileName)
		},
		Render: func(state *configurator.AppState, token string) (string, error) {
			binding, hasBinding := affineBinding(state)
			if !hasBinding {
				logger.Warn("affine-mcp: no published affine appApi credential yet; writing an unauthenticated config")
			}
			return renderConfig(binding, hasBinding, token)
		},
		// The probe is unauthenticated (/readyz), so a rejection here is a
		// transport or config fault, never a missing harness bearer.
		Probe: func(ctx context.Context, cl *appclient.Client, _ string) error {
			return cl.GET(readyPath).
				Wait(appclient.StatusIs(http.StatusOK)).
				Within(60 * time.Second).
				Do(ctx)
		},
	})
}

// affineBinding returns the one usable `appApi` provider. The contract is
// required and single-provider, so the first complete binding is the answer. A
// binding missing its password or username is the provider's "not published
// yet" state, not a usable credential, and writing it would configure a sign-in
// that cannot succeed.
func affineBinding(state *configurator.AppState) (bindings.AppAPIBinding, bool) {
	if state == nil {
		return bindings.AppAPIBinding{}, false
	}
	for _, b := range state.Integrations.AppAPIs {
		if !b.Installed || b.BaseURL == "" || b.Username == "" || !b.Password.Published() {
			continue
		}
		return b, true
	}
	return bindings.AppAPIBinding{}, false
}

// renderConfig renders the image's key=value saved config. Order is fixed so an
// unchanged set of values produces byte-identical output and PreStart reports no
// change, which is what stops the container being recreated on every pass.
//
// The parser reads a line up to its first '=' and takes the rest verbatim, so a
// value containing a newline would inject a second setting. Both values here are
// machine-generated (a base64 password and token) or an operator email the app
// validates, but the guard is cheap and makes the file's integrity a property of
// this function rather than of upstream's input validation.
func renderConfig(binding bindings.AppAPIBinding, hasBinding bool, token string) (string, error) {
	var b strings.Builder
	b.WriteString("# Generated by Bloud; the host-agent rewrites this file on every\n")
	b.WriteString("# reconciliation and restarts the container when its content changes.\n")

	settings := make([][2]string, 0, 5)
	if hasBinding {
		settings = append(settings,
			[2]string{"AFFINE_BASE_URL", binding.BaseURL},
			[2]string{"AFFINE_EMAIL", binding.Username},
			[2]string{"AFFINE_PASSWORD", binding.Password.String()},
		)
		// Pin the workspace Bloud provisions. Every workspace-scoped tool in the
		// wrapper resolves `args.workspaceId || AFFINE_WORKSPACE_ID`, so without
		// this an agent has to list workspaces and choose one on every call, and
		// can choose the wrong one. A provider that published no scope leaves
		// this unset, which is the wrapper's documented "agent supplies the id"
		// mode.
		if binding.WorkspaceID != "" {
			settings = append(settings, [2]string{"AFFINE_WORKSPACE_ID", binding.WorkspaceID})
		}
	}
	settings = append(settings, [2]string{"AFFINE_MCP_AUTH_MODE", "bearer"})
	if token != "" {
		settings = append(settings, [2]string{"AFFINE_MCP_HTTP_TOKEN", token})
	}

	for _, kv := range settings {
		if strings.ContainsAny(kv[1], "\r\n") {
			return "", fmt.Errorf("config value for %s contains a line break", kv[0])
		}
		fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
	}
	return b.String(), nil
}
