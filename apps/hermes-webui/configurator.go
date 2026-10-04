// SPDX-License-Identifier: AGPL-3.0-only

// Package hermeswebui wires Hermes Web UI into Bloud.
//
// Hermes Web UI is a browser front end for the Hermes agent. The image is
// only the front end: the agent itself is a Python source tree the
// entrypoint installs into the venv it builds on first boot, from wherever
// it is mounted.
//
// The app shares its $HERMES_HOME with apps/hermes rather than keeping its
// own, which is what makes it a front end instead of a second agent: one
// config.yaml, one memory, one session store, one set of MCP namespaces.
// That sharing decides this configurator's shape. It owns exactly two things
// -- putting the pinned agent source where the entrypoint can build from it,
// and reading back what the running app resolved -- and it owns no write
// path to the agent's configuration, because apps/hermes is that writer.
//
// The full story, including why the SSO strategy is forward-auth rather
// than the app's own OIDC client, is in INTEGRATION.md.
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

// PreStart puts the agent source in place. That is all this app owns.
//
// It does not write the agent's config.yaml, because it does not own that
// file. This container mounts the same $HERMES_HOME tree apps/hermes does,
// and Hermes is the writer: the SSO block, the model selection, and every
// MCP namespace Bloud resolves for Hermes land in that one file, and this
// UI reads them from there. Two configurators writing one file is a churn
// loop and an ownership lie, so there is one.
//
// Only the agent source asks for a restart. A new source tree has to be
// installed at boot, when the entrypoint builds the venv from it.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	if state == nil {
		return configurator.NoRestart(), nil
	}

	agentChanged, err := c.ensureAgentSource(ctx, state.DataPath)
	if err != nil {
		return configurator.NoRestart(), err
	}

	return configurator.RestartIf(agentChanged, "Hermes agent source installed"), nil
}

// PostStart verifies the app is serving, then reads back the two things the
// shared-home design is supposed to produce: the agent profile the app
// resolved out of the shared config.yaml, and the MCP namespaces it can see.
//
// Both reads are reports, not gates. This app owns neither the model choice
// nor the MCP set; Hermes writes them. Failing this node over a value that
// lives in another app's convergence would blame the wrong thing. What the
// reads buy is visibility: if the shared tree is not really shared, or
// Hermes never wrote what it should have, the log says so instead of the
// operator finding out by opening the chat and seeing no tools.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	if err := c.verifyServing(ctx); err != nil {
		return err
	}

	c.reportAgentProfile(ctx)
	c.reportMCPServers(ctx)
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

// reportAgentProfile reads the app's own view of the agent it resolved and
// logs it. The profile is the shared config.yaml as the running server read
// it, so this is where "the two containers share one brain" is either
// visible or visibly broken.
func (c *Configurator) reportAgentProfile(ctx context.Context) {
	profiles, err := c.api.profiles(ctx)
	if err != nil {
		c.logger.Warn("could not read the agent profile list to verify the shared agent config",
			"path", profilesPath, "error", err)
		return
	}
	active, ok := profiles.active()
	if !ok {
		c.logger.Warn("the app reported no Hermes profiles; the agent has nothing to run")
		return
	}
	c.logger.Info("the web UI resolved the shared agent profile",
		"profile", active.Name, "provider", active.Provider, "model", active.Model)
}

// reportMCPServers logs the MCP namespaces the running agent can see. This
// is the read that proves the shared home works: the set is whatever Bloud
// wrote into the shared config.yaml for Hermes, surfaced through the web
// UI's own endpoint.
//
// A warning rather than a failure when the list is empty. Hermes' `mcp` is
// an optional contract, so an instance with no MCP-capable app installed
// has nothing to share, and that is a valid state rather than a broken one.
func (c *Configurator) reportMCPServers(ctx context.Context) {
	servers, err := c.api.mcpServers(ctx)
	if err != nil {
		c.logger.Warn("could not read the MCP server list to verify the shared agent config",
			"path", mcpServersPath, "error", err)
		return
	}
	if len(servers.Servers) == 0 {
		c.logger.Info("the shared agent home carries no MCP servers; installing an "+
			"MCP-capable app will reach this UI without a change here",
			"path", mcpServersPath)
		return
	}
	c.logger.Info("the web UI sees Hermes' MCP namespaces through the shared agent home",
		"servers", mcpServerSummary(servers))
}

// mcpServerSummary renders the namespaces and their state for the log line,
// so an operator can tell "configured" from "active" without opening the
// UI.
func mcpServerSummary(r mcpServersResponse) []string {
	out := make([]string, 0, len(r.Servers))
	for _, s := range r.Servers {
		out = append(out, s.Name+"="+s.Status)
	}
	return out
}
