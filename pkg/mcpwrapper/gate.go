// SPDX-License-Identifier: AGPL-3.0-only

package mcpwrapper

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
)

// nginxFileName is the host-side path of the rendered gate config, under the
// app's config directory. Both gates already used this name; the container
// mounts it wherever its entrypoint expects.
const nginxFileName = "nginx.conf"

// GateConfig declares the app-specific half of a bearer gate node: an nginx
// container that publishes the app's port and enforces the app's own bearer on the
// published API path, so a server with no bearer notion of its own still keeps its
// contract's promise. The caller supplies the probe, because what proves "serving"
// is a fact about the surface rather than about the gate.
type GateConfig struct {
	// App is the catalog id, for the secrets store and log identity.
	App string
	// Node is the gate's graph node and container name.
	Node string
	// TokenKey is the secret name holding the bearer the gate enforces.
	TokenKey string
	// Render builds the gate's nginx config from the secrets provider and the
	// logger. The app resolves its own credentials inside, the shared bearer
	// plus any app-specific extras.
	Render func(secrets configurator.AppSecretsProvider, logger *slog.Logger) (string, error)
	// Probe verifies the composed endpoint. Use mcp.Probe for a bearer
	// handshake; a gate over a different surface supplies its own.
	Probe func(ctx context.Context, cl *appclient.Client, bearer string) error
}

// Gate implements configurator.NodeLifecycle and configurator.PortReporter for
// a bearer gate node.
type Gate struct {
	cfg     GateConfig
	port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
	api     *appclient.Client
	baseURL string // test seam
}

// NewGate builds the gate's configurator. port is the app's published port,
// which the gate listens on and probes.
func NewGate(port int, deps configurator.Deps, cfg GateConfig) *Gate {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	g := &Gate{
		cfg:     cfg,
		port:    port,
		secrets: deps.Secrets,
		logger:  logger.With("app", cfg.App, "node", cfg.Node),
	}
	if deps.HTTP != nil {
		g.api = deps.HTTP.New(appclient.Spec{Name: cfg.App, BaseURLFn: func() string {
			if g.baseURL != "" {
				return g.baseURL
			}
			return fmt.Sprintf("http://localhost:%d", g.port)
		}})
	}
	return g
}

// SetBaseURL points the probe client at a fixed address (test seam).
func (g *Gate) SetBaseURL(u string) { g.baseURL = u }

// Name returns the graph node this gate owns.
func (g *Gate) Name() string { return g.cfg.Node }

// Port returns the app port this gate publishes and listens on.
func (g *Gate) Port() int { return g.port }

// PreStart renders the gate's nginx config and writes it. It reports a recreate
// only when the bytes changed, so a steady-state pass is a read-only diff.
func (g *Gate) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	content, err := g.cfg.Render(g.secrets, g.logger)
	if err != nil {
		return configurator.NoRestart(), err
	}

	confPath := filepath.Join(state.DataPath, "config", nginxFileName)
	if err := os.MkdirAll(filepath.Dir(confPath), 0o755); err != nil {
		return configurator.NoRestart(), fmt.Errorf("creating the %s config directory: %w", g.cfg.App, err)
	}
	changed, err := managedfile.Write(confPath, []byte(content), managedfile.ModeSharedConfig)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing the %s gate config: %w", g.cfg.App, err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}
	g.logger.Info("wrote "+g.cfg.App+" gate config", "path", confPath)
	return configurator.MustRestart(g.cfg.App + " gate config rewritten"), nil
}

// PostStart proves the composed endpoint answers. A gate with no bearer yet is
// deliberately locked, so that state is a no-op rather than an error: the next
// pass retries once the credential exists.
func (g *Gate) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if g.api == nil {
		return nil
	}
	bearer := ""
	if g.secrets != nil {
		bearer = g.secrets.GetAppSecret(g.cfg.App, g.cfg.TokenKey)
	}
	if bearer == "" {
		g.logger.Warn("the " + g.cfg.App + " API endpoint is locked until its bearer exists")
		return nil
	}
	if err := g.cfg.Probe(ctx, g.api, bearer); err != nil {
		return fmt.Errorf("waiting for the %s endpoint: %w", g.cfg.App, err)
	}
	g.logger.Info(g.cfg.App + " is serving its API through the gate")
	return nil
}
