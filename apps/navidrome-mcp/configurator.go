// SPDX-License-Identifier: AGPL-3.0-only

// Package navidromemcp configures the standalone Navidrome MCP server
// (navidrome-mcp) as a Bloud app.
//
// The app is one container and one graph node. Its PreStart writes the env file
// the container is created from, because the image reads its whole configuration
// from process environment and from a settings.json it does not watch, and three
// of the values it needs are a resolved binding and generated secrets rather than
// static metadata. Its PostStart proves the MCP endpoint answers, because the
// container's own health check only says the process is up.
//
// See INTEGRATION.md for what the app does not get: audio, and the Last.fm
// discovery that needs a key of the operator's.
package navidromemcp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
	"codeberg.org/d-buckner/bloud/pkg/mcp"
	"codeberg.org/d-buckner/bloud/pkg/mcpwrapper"
)

const (
	appName  = "navidrome-mcp"
	nodeName = "apps-navidrome-mcp"

	defaultPort = 4534

	// configDir is the subdirectory of the app data tree holding everything this
	// app generates. The env file is read by the host at create time and never
	// mounted; the same directory is mounted at /config because the image declares
	// that path as a volume.
	configDir   = "config"
	envFileName = "env"

	// configDirPerm is the mode the mounted directory carries. The image runs as
	// its own `node` uid, which Bloud cannot set on a bind mount, so the directory
	// has to be writable by whatever uid arrives. Same constraint and treatment as
	// apps/arr-mcp's config directory.
	configDirPerm = 0o777

	// The environment variable names the image reads. Kept as constants so the
	// renderer and the tests name the same keys upstream expects.
	envNavidromeURL  = "NAVIDROME_URL"
	envNavidromeUser = "NAVIDROME_USERNAME"
	envNavidromePass = "NAVIDROME_PASSWORD"
	envAuthToken     = "MCP_HTTP_AUTH_TOKEN"

	// httpTokenKey is the secret name the `mcp` contract carries: the bearer a
	// consumer presents and the image compares in constant time.
	httpTokenKey = "httpToken"

	// mcpPath is the endpoint the app publishes under its `mcp` offer, and the
	// path the image serves the streamable-HTTP transport on.
	mcpPath = "/mcp"
)

// NewConfigurator builds the wrapper's configurator. The shared half (bearer
// mint, file write, handshake probe) lives in pkg/mcpwrapper; what is here is
// the app-specific half: the music-server binding, the env-file renderer, and
// the directory the image mounts.
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
		// The env file holds the music server's account password, and only the
		// host reads it: the runtime passes the values in at create time and
		// never mounts the file.
		Mode: managedfile.ModeHostOnly,
		Path: func(state *configurator.AppState) string {
			return filepath.Join(state.DataPath, configDir, envFileName)
		},
		Prepare: func(state *configurator.AppState) error {
			dir := filepath.Join(state.DataPath, configDir)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("creating the %s config directory: %w", appName, err)
			}
			// Only the mounted half needs the wide mode, and a chmod is
			// idempotent, so it runs on every pass rather than only on the
			// first.
			if err := os.Chmod(dir, configDirPerm); err != nil {
				return fmt.Errorf("opening the %s config directory to the container's uid: %w", appName, err)
			}
			return nil
		},
		Render: func(state *configurator.AppState, bearer string) (string, error) {
			provider, hasProvider := mediaServerProvider(state)
			content, omitted := renderEnvFile(provider, hasProvider, bearer)
			if omitted != "" {
				// The value is never echoed: it is the Navidrome account
				// password.
				logger.Warn("the Navidrome credential could not be written to the env file; the variable is absent from it", "key", omitted)
			}
			return content, nil
		},
		Probe: func(ctx context.Context, cl *appclient.Client, bearer string) error {
			return mcp.Probe(ctx, cl, mcpPath, bearer, 60*time.Second)
		},
	})
}

// mediaServerProvider returns the installed music server this app signs in with.
//
// The same reader answers a Navidrome Bloud booted and one the operator
// registered from off-host, because the binding carries the address either way:
// a container name on the app network for the first, the operator's own origin
// for the second.
func mediaServerProvider(state *configurator.AppState) (bindings.MediaServerBinding, bool) {
	if state == nil {
		return bindings.MediaServerBinding{}, false
	}
	for _, b := range state.Integrations.MediaServers {
		if !b.Installed || b.BaseURL == "" {
			continue
		}
		return b, true
	}
	return bindings.MediaServerBinding{}, false
}
