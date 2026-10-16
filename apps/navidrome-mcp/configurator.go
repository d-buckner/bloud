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
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
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
)

// Configurator drives the single container this app owns.
type Configurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
	api     *mcpAPI

	// baseURL is a test seam: when set, the readiness probe dials it instead of
	// localhost on the app port.
	baseURL string
}

// NewConfigurator builds the node's configurator.
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
		logger:  logger.With("app", appName, "node", nodeName),
	}
	if deps.HTTP != nil {
		c.api = newAPI(deps.HTTP, func() string {
			if c.baseURL != "" {
				return c.baseURL
			}
			return fmt.Sprintf("http://localhost:%d", c.port)
		})
	}
	return c
}

// Name returns the graph node this configurator owns.
func (c *Configurator) Name() string { return nodeName }

// Port is the app port this node was constructed with, which is the catalog
// `port:` when registration passed 0. The conformance harness compares it against
// metadata.yaml, because only one of the two routes traffic.
func (c *Configurator) Port() int { return c.port }

// PreStart writes the env file the container spec declares as its envFile, and
// prepares the directory the image mounts at /config.
//
// A provider that has not resolved yet is written as absent rather than as an
// empty value, so "not ready" stays distinguishable from "configured with
// nothing". The graph orders the music server first, so an absent credential here
// means the provider has not published one yet and the next pass fixes it.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	bearer, err := c.ensureBearer()
	if err != nil {
		return configurator.NoRestart(), err
	}

	provider, hasProvider := mediaServerProvider(state)
	content, omitted := renderEnvFile(provider, hasProvider, bearer)
	if omitted != "" {
		// The value is never echoed: it is the Navidrome account password.
		c.logger.Warn("the Navidrome credential could not be written to the env file; the variable is absent from it", "key", omitted)
	}

	dir := filepath.Join(state.DataPath, configDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return configurator.NoRestart(), fmt.Errorf("creating the %s config directory: %w", appName, err)
	}
	// Only the mounted half of this directory needs the wide mode, and a chmod is
	// idempotent, so it runs on every pass rather than only on the first.
	if err := os.Chmod(dir, configDirPerm); err != nil {
		return configurator.NoRestart(), fmt.Errorf("opening the %s config directory to the container's uid: %w", appName, err)
	}

	envPath := filepath.Join(dir, envFileName)
	changed, err := managedfile.Write(envPath, []byte(content), managedfile.ModeHostOnly)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing %s config: %w", appName, err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}
	c.logger.Info("wrote navidrome-mcp config", "path", envPath, "provider", hasProvider, "bearer", bearer != "")
	return configurator.MustRestart("navidrome-mcp config rewritten"), nil
}

// PostStart proves the published MCP endpoint completes an `initialize` handshake
// with the bearer this app published.
//
// The container's health check cannot answer this: /healthz is an unauthenticated
// liveness route that makes no Navidrome call by design, so it says 200 for a
// server that cannot reach its library. The handshake is what separates "serving
// MCP" from "something is listening".
func (c *Configurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if c.api == nil {
		return nil
	}
	if err := c.api.waitServing(ctx, c.currentBearer()); err != nil {
		return fmt.Errorf("waiting for the navidrome-mcp server: %w", err)
	}
	c.logger.Info("navidrome-mcp is serving MCP")
	return nil
}

// --- credentials ---

// ensureBearer returns the bearer consumers present, publishing one on the first
// pass and reusing it afterwards. Without a secrets provider nothing can be
// stored, and the caller renders the "not ready" shape rather than inventing a
// value that would change on every pass.
func (c *Configurator) ensureBearer() (string, error) {
	if c.secrets == nil {
		return "", nil
	}
	if existing := c.secrets.GetAppSecret(appName, httpTokenKey); existing != "" {
		return existing, nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating the %s MCP bearer: %w", appName, err)
	}
	// base64url needs no quoting in an env line, which is the only place it lands.
	token := base64.RawURLEncoding.EncodeToString(buf)
	if err := c.secrets.SetAppSecret(appName, httpTokenKey, token); err != nil {
		return "", fmt.Errorf("publishing the %s MCP bearer: %w", appName, err)
	}
	c.logger.Info("generated the navidrome-mcp MCP bearer")
	return token, nil
}

// currentBearer reads the bearer without generating one, so a probe always
// presents whatever is current even if a rotation landed between the two phases.
func (c *Configurator) currentBearer() string {
	if c.secrets == nil {
		return ""
	}
	return c.secrets.GetAppSecret(appName, httpTokenKey)
}

// --- bindings ---

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
