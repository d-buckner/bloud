// SPDX-License-Identifier: AGPL-3.0-only

// Package hermeswebui wires Hermes Web UI into Bloud.
//
// Hermes Web UI is a browser front end for the Hermes agent. The image is
// only the front end: the agent itself is a Python source tree the
// entrypoint installs into the venv it builds on first boot, from wherever
// it is mounted. So this configurator has two jobs the metadata cannot do.
//
// PreStart puts the agent source in place, from a pinned, digest-verified
// archive, and converges the agent's own config.yaml with the model Bloud's
// AI settings point at. PostStart asks the running app, through its own
// profile endpoint, whether the agent it resolved is the one Bloud
// configured.
//
// The full story, including why the SSO strategy is forward-auth rather than
// the app's own OIDC client, is in INTEGRATION.md.
package hermeswebui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appasset"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const (
	// appName is the catalog ID (and secrets/registry key) for this app.
	appName = "hermes-webui"
	// nodeName is this app's graph node and container name.
	nodeName = "apps-hermes-webui"
	// defaultPort is the host-side port the app is published on, and the
	// value the constructor uses when registration passes 0. It matches
	// metadata.yaml's `port`.
	defaultPort = 8787
)

// Configurator handles the Hermes Web UI node lifecycle.
type Configurator struct {
	port   int
	logger *slog.Logger
	api    *webuiAPI

	// assets installs the pinned Hermes agent source (fetch/verify/stage/
	// commit). The zero value is usable but has no cache, which is how a
	// CLI or unit-test context is detected; see canInstallAssets.
	assets appasset.Installer

	// exec runs a command inside the running container. It is the fallback
	// reader for config.yaml, which the host agent cannot read once the
	// container owns it (see readConfig). Nil in CLI/test contexts.
	exec configurator.ExecFunc

	// baseURL is a test seam: when set, the API client resolves to it
	// instead of localhost:port.
	baseURL string
}

// NewConfigurator creates a Hermes Web UI configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:   port,
		logger: logger.With("app", appName),
		assets: deps.Assets,
		exec:   deps.Exec,
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

// canInstallAssets reports whether the host provided a provisioned asset
// installer. The real runtime always sets a cache directory; a zero-value
// Installer is what a CLI or unit-test context produces, and there is no
// host to install into from there.
func (c *Configurator) canInstallAssets() bool {
	return c.assets.CacheDir != ""
}

// PreStart makes the two things true that the container needs before it
// starts: the agent source is mounted in from a tree Bloud installed, and
// the agent's config.yaml carries the Bloud model if the host still owns
// the file.
//
// The config half is a seed, not a convergence. Before the first start the
// host owns the whole app tree and can write the file directly, which is
// what makes a fresh install come up with the instance's model already
// wired. After that first start the agent owns it and PreStart cannot write
// it any more, so it logs and leaves the real convergence to PostStart,
// which runs against a live container.
//
// Only the agent source asks for a restart. A config change does not: the
// app reads config.yaml live rather than caching it at boot, which was
// measured, not assumed.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	if state == nil {
		return configurator.NoRestart(), nil
	}

	agentChanged, err := c.ensureAgentSource(ctx, state.DataPath)
	if err != nil {
		return configurator.NoRestart(), err
	}

	if _, err := c.convergeConfig(ctx, state.DataPath, state, false); err != nil {
		return configurator.NoRestart(), err
	}

	return configurator.RestartIf(agentChanged, "Hermes agent source installed"), nil
}

// PostStart converges the agent config against the instance's AI settings
// and verifies the app is serving the model that was written.
//
// This is where the config work has to live. The PostStart resync re-runs
// on every pass over a running node, which is the only way a change to
// Settings -> AI reaches an app that is already installed, and the running
// container is the only process with a right to write its own config file.
// No restart is asked for, because none is needed: the app re-reads the
// file rather than holding a boot-time snapshot.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	if err := c.verifyServing(ctx); err != nil {
		return err
	}

	if _, err := c.convergeConfig(ctx, dataDirOf(state), state, true); err != nil {
		return err
	}

	c.reportAgentProfile(ctx, state)
	return nil
}

// verifyServing asks the app whether it is alive.
//
// The container health check has already passed by the time this runs, so
// an answer that says otherwise is a real disagreement about the app's
// state rather than a race, and it is worth failing the node over. A
// transport failure is the opposite: the app was just healthy, so the read
// is warned about and the pass continues.
func (c *Configurator) verifyServing(ctx context.Context) error {
	health, err := c.api.health(ctx)
	if err != nil {
		c.logger.Warn("could not read the app health endpoint", "path", healthPath, "error", err)
		return nil
	}
	if status := strings.ToLower(strings.TrimSpace(health.Status)); status != "ok" {
		return fmt.Errorf("hermes-webui reported health status %q at %s", health.Status, healthPath)
	}
	c.logger.Info("hermes-webui is serving", "status", health.Status)
	return nil
}

// dataDirOf returns the app's own data directory, or an empty string when
// there is no state to speak of.
func dataDirOf(state *configurator.AppState) string {
	if state == nil {
		return ""
	}
	return state.DataPath
}

// reportAgentProfile reads the app's own view of the agent it resolved and
// says whether it matches the Bloud inference binding.
//
// The read is a report rather than a gate. A model the operator picked
// inside the app outranks Bloud's default by design, and parking a working
// container in a terminal ERROR because someone chose differently in the UI
// would be the worse failure.
func (c *Configurator) reportAgentProfile(ctx context.Context, state *configurator.AppState) {
	_, hasInference := inferenceBinding(state)
	if !hasInference {
		c.logger.Info("no Bloud inference binding; the agent is left on whatever model it was configured with")
		return
	}

	profiles, err := c.api.profiles(ctx)
	if err != nil {
		c.logger.Warn("could not read the agent profile list to verify the model wiring",
			"path", profilesPath, "error", err)
		return
	}
	active, ok := profiles.active()
	if !ok {
		c.logger.Warn("the app reported no Hermes profiles; the agent has nothing to run")
		return
	}

	if !providerIsBloud(active.Provider) {
		c.logger.Warn("the active agent profile is not using the Bloud provider; "+
			"a model chosen inside the app outranks the instance default",
			"profile", active.Name, "provider", active.Provider, "model", active.Model,
			"expectedProvider", inferenceProviderSlug)
		return
	}
	c.logger.Info("the active agent profile is wired to Bloud's model",
		"profile", active.Name, "provider", active.Provider, "model", active.Model)
}

// providerIsBloud reports whether a provider string the app reported refers
// to the Bloud entry. The app renders the selection slug in more than one
// shape across its endpoints (the bare key and the `custom:`-qualified
// slug), so both are accepted; anything else is a provider someone chose by
// hand.
func providerIsBloud(reported string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(reported))
	return trimmed == inferenceProviderKey || trimmed == strings.ToLower(inferenceProviderSlug)
}
