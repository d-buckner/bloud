// SPDX-License-Identifier: AGPL-3.0-only

// Package mcpwrapper owns the shared half of a single-container MCP wrapper:
// mint the bearer, render and write the generated config file, and prove the
// server is serving. The app supplies the app-specific half in a Config: where
// the file lives, how its bytes are rendered, and how readiness is probed.
//
// A wrapper is not a browser and cannot join the identity provider, so it
// consumes another app's contract for its target credential, and publishes its
// own bearer under the `mcp` contract in return. The bearer is the one piece of
// shared state this package owns: it mints one on the first pass, stores it,
// and reuses it afterwards, so a restart does not invalidate the namespace a
// harness registered.
package mcpwrapper

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
)

// Config declares the app-specific half of one MCP wrapper.
type Config struct {
	App         string
	Node        string
	DefaultPort int

	// TokenKey is the secret name the `mcp` contract publishes: the bearer a
	// harness presents.
	TokenKey string

	// Mode is the file mode the generated config is written with.
	Mode os.FileMode

	// Path returns the config file path for a state.
	Path func(state *configurator.AppState) string

	// Prepare runs before the write, for directories the image must own. It
	// may be nil when the write needs no directory work.
	Prepare func(state *configurator.AppState) error

	// Render builds the file bytes from the state and the minted bearer. The
	// app resolves its own bindings inside this function.
	Render func(state *configurator.AppState, bearer string) (string, error)

	// Probe verifies the server is serving. It receives the app client and the
	// current bearer. Use mcp.Probe for the streamable-HTTP handshake; a
	// wrapper whose readiness is a plain liveness route supplies its own.
	Probe func(ctx context.Context, cl *appclient.Client, bearer string) error
}

// Wrapper implements configurator.NodeLifecycle and configurator.PortReporter
// for a single-container MCP wrapper.
type Wrapper struct {
	cfg     Config
	port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
	api     *appclient.Client
	baseURL string // test seam
}

// New builds the wrapper from host Deps and the app's Config.
func New(port int, deps configurator.Deps, cfg Config) *Wrapper {
	if port == 0 {
		port = cfg.DefaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	w := &Wrapper{
		cfg:     cfg,
		port:    port,
		secrets: deps.Secrets,
		logger:  logger.With("app", cfg.App),
	}
	if deps.HTTP != nil {
		w.api = deps.HTTP.New(appclient.Spec{Name: cfg.App, BaseURLFn: func() string {
			if w.baseURL != "" {
				return w.baseURL
			}
			return fmt.Sprintf("http://localhost:%d", w.port)
		}})
	}
	return w
}

// SetBaseURL points the API client at a fixed address (test seam).
func (w *Wrapper) SetBaseURL(u string) { w.baseURL = u }

// Name returns the graph node this wrapper owns.
func (w *Wrapper) Name() string { return w.cfg.Node }

// Port returns the app port this wrapper was constructed with.
func (w *Wrapper) Port() int { return w.port }

// PreStart mints the bearer, renders the config, and writes it. It reports a
// recreate only when the bytes changed, so a steady-state pass is a read-only
// diff that disturbs nothing.
func (w *Wrapper) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	bearer, minted, err := configurator.EnsureSecret(w.secrets, w.cfg.App, w.cfg.TokenKey, 32, "MCP bearer")
	if err != nil {
		return configurator.NoRestart(), err
	}
	if minted {
		w.logger.Info("generated the " + w.cfg.App + " MCP bearer")
	}

	content, err := w.cfg.Render(state, bearer)
	if err != nil {
		return configurator.NoRestart(), err
	}

	if w.cfg.Prepare != nil {
		if err := w.cfg.Prepare(state); err != nil {
			return configurator.NoRestart(), err
		}
	}

	path := w.cfg.Path(state)
	changed, err := managedfile.Write(path, []byte(content), w.cfg.Mode)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing %s config: %w", w.cfg.App, err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}
	w.logger.Info("wrote "+w.cfg.App+" config", "path", path, "bearer", bearer != "")
	return configurator.MustRestart(w.cfg.App + " config rewritten"), nil
}

// PostStart proves the server is serving through the app's declared probe.
func (w *Wrapper) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if w.api == nil {
		return nil
	}
	bearer := ""
	if w.secrets != nil {
		bearer = w.secrets.GetAppSecret(w.cfg.App, w.cfg.TokenKey)
	}
	if err := w.cfg.Probe(ctx, w.api, bearer); err != nil {
		return fmt.Errorf("waiting for the %s server: %w", w.cfg.App, err)
	}
	w.logger.Info(w.cfg.App + " is serving MCP")
	return nil
}
