// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

// The seam these tests hold: an operator registers someone else's Home Assistant
// in the external-app registry, and the real apps/ha-mcp configurator ends up
// writing an env file carrying that endpoint and that token.
//
// Nothing else crosses this boundary end to end. apps/ha-mcp/configurator_test.go
// hand-builds the AppAPIBinding the graph would produce, so it proves the
// renderer and not the resolver; the resolver tests stop at the AppState, so they
// prove the resolver and not the renderer. Between two hand-built halves a
// renamed contract, a `requires:` list that dropped `password`, or a binding
// field the configurator stopped reading would leave every existing test green
// while ha-mcp came up with no credential and dialed nothing.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hamcp "codeberg.org/d-buckner/bloud/apps/ha-mcp"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const (
	// The two graph nodes apps/ha-mcp declares. The graph sees containers, so the
	// server and its bearer gate are separate nodes with separate configurators.
	haMCPServerNode = "apps-ha-mcp"
	haMCPGateNode   = "apps-ha-mcp-gate"

	// The operator's remote instance and the credential they minted there.
	seamHAURL   = "https://ha.example.net:8123"
	seamHAToken = "operator-long-lived-token"
)

// haMCPSeam wires the resolver and the real ha-mcp configurator to one secrets
// store. The shared store is the point: the operator's token is written by the
// external-app intent and read back by the configurator's pass, so two separate
// fakes would only be testing two fakes agreeing.
type haMCPSeam struct {
	orch    *Orchestrator
	g       *graph.Graph
	secrets *fakeSecrets
	rt      *liveRuntime

	// envFile is the path the container spec says the runtime reads the
	// container's environment from, resolved from the real container def rather
	// than spelled out here. It is what makes "the configurator wrote it" the same
	// statement as "the container will get it": if the two ever name different
	// files, this is the assertion that fails.
	envFile string
}

func newHaMCPSeam(t *testing.T) *haMCPSeam {
	t.Helper()

	haMCP := realCatalogApp(t, "ha-mcp")

	cache := NewFakeCatalogCache()
	// The consumer, plus the provider it declares, because the resolver reads the
	// provider's `provides:` block to answer the contract at all. Neither is
	// installed, so neither contributes a graph node: the Home Assistant in this
	// test is the operator's, and the system apps the catalog lists (Traefik,
	// Authentik) stay out of the cache so a convergence pass does not install them.
	cache.AddApp(haMCP)
	cache.AddApp(realCatalogApp(t, "homeassistant"))

	appStore := NewFakeAppStore()
	install(t, appStore, "ha-mcp", nil)

	extStore := store.NewExternalAppStore(testdb.SetupTestDB(t))
	secrets := newFakeSecrets()
	dataDir := t.TempDir()

	spec, err := ContainerSpecFromDef(containerDef(t, haMCP, haMCPServerNode), "ha-mcp", dataDir, nil)
	require.NoError(t, err)
	require.NotEmpty(t, spec.EnvFile,
		"apps/ha-mcp must declare an envFile: the image reads its configuration from process env only")

	server := hamcp.NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: newTestLogger()})
	registry := new(MockConfiguratorRegistry)
	registry.On("Get", haMCPServerNode).Return(server)
	// The gate is a different node with a different contract: its own PostStart
	// drives an MCP `initialize` through a live nginx. This package already covers
	// the gate where it belongs, and a convergence pass here is not the place to
	// reach for a port nothing is listening on.
	registry.On("Get", haMCPGateNode).Return(&switchableConfigurator{node: haMCPGateNode})

	g := graph.New(graph.NewMapRepository())
	rt := newLiveRuntime()

	orch := NewOrchestrator(g, registry, cache, dataDir, newTestLogger(), OrchestratorConfig{
		Tuning:  TuningConfig{HealthCheckTimeout: 100 * time.Millisecond},
		Runtime: RuntimeConfig{Containers: rt},
		Stores:  StoresConfig{AppStore: appStore, Secrets: secrets, ExternalApps: extStore},
	})
	orch.settings = newFakeSettings()
	orch.registerContainerOwner(haMCPServerNode, "ha-mcp")
	orch.registerContainerOwner(haMCPGateNode, "ha-mcp")

	return &haMCPSeam{
		orch:    orch,
		g:       g,
		secrets: secrets,
		rt:      rt,
		envFile: spec.EnvFile,
	}
}

// containerDef returns one container definition from a catalog app.
func containerDef(t *testing.T, app *catalog.App, name string) catalog.ContainerDef {
	t.Helper()
	for _, def := range app.Containers {
		if def.Name == name {
			return def
		}
	}
	t.Fatalf("%s declares no container named %s", app.CatalogID, name)
	return catalog.ContainerDef{}
}

// externalHAIntent is the operator registering a Home Assistant down the hall:
// the endpoint they typed and the long-lived token they minted in that instance's
// own UI. Nothing else, which is the point: the contract carries a username, but
// a token stands on its own and no consumer asks who it belongs to.
func externalHAIntent() AddExternalAppIntent {
	return NewAddExternalAppIntent(ExternalAppSpec{
		ID:      "ext-ha",
		Kind:    string(store.ExternalAppKindProvider),
		Source:  store.ExternalAppSourceForApp("homeassistant"),
		Name:    "Garage Home Assistant",
		URL:     seamHAURL,
		Secrets: map[string]string{"appApi": seamHAToken},
	})
}

// markRunning puts the app in the state a working install sits in between
// passes: both nodes converged, both containers up.
func (s *haMCPSeam) markRunning(t *testing.T) {
	t.Helper()
	for _, node := range []string{haMCPServerNode, haMCPGateNode} {
		require.NoError(t, s.g.AddNode(node))
		require.NoError(t, s.g.SetTargetStatus(node, graph.StatusRunning))
		require.NoError(t, s.g.SetActualStatus(node, graph.StatusRunning, ""))
		s.rt.set(node, containerruntime.State{Exists: true, Running: true})
	}
}

// readEnv reads the file the container spec names.
func (s *haMCPSeam) readEnv(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(s.envFile)
	require.NoError(t, err, "the env file the container spec declares must exist at %s", s.envFile)
	return string(content)
}

// TestExternalHomeAssistantReachesTheRealHaMCPConfigurator is the seam: the
// operator's record resolves, and the real configurator's PreStart puts those
// operator values into the bytes the container is created from.
//
// The variable names are spelled out rather than imported from the app package
// (they are unexported there, and that is correct): they are the upstream image's
// contract, and a configurator that renamed one should fail here rather than in a
// container that starts and then cannot reach Home Assistant.
func TestExternalHomeAssistantReachesTheRealHaMCPConfigurator(t *testing.T) {
	seam := newHaMCPSeam(t)
	seam.orch.applyAddExternalAppIntent(externalHAIntent())

	state := seam.orch.buildAppState(haMCPServerNode)
	require.Len(t, state.Integrations.AppAPIs, 1, "the resolver handed the consumer its provider")
	require.Equal(t, seamHAURL, state.Integrations.AppAPIs[0].BaseURL)
	require.Equal(t, seamHAToken, state.Integrations.AppAPIs[0].Password.String())

	server := hamcp.NewConfigurator(0, configurator.Deps{Secrets: seam.secrets, Logger: newTestLogger()})
	result, err := server.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, result.RestartNeeded, "the env file did not exist, so the container has to be created from it")

	env := seam.readEnv(t)
	assert.Contains(t, env, "HOMEASSISTANT_URL='"+seamHAURL+"'",
		"the MCP server must dial the operator's endpoint, not a container that does not exist")
	assert.Contains(t, env, "HOMEASSISTANT_TOKEN='"+seamHAToken+"'",
		"and authenticate with the token they minted there")
}

// TestAddExternalAppReconvergesARunningConsumer is the timing half of the seam.
// The consumer is already RUNNING when the operator registers the provider, so
// nothing about the graph changed: no node was added, no target moved, no
// dependency was unblocked. The only thing that can reach the config is the
// resync every RUNNING node takes, and it has to happen in the same pass that
// applied the intent, because the alternative is a companion running without a
// Home Assistant until some later, unrelated event happens to disturb it.
func TestAddExternalAppReconvergesARunningConsumer(t *testing.T) {
	seam := newHaMCPSeam(t)
	seam.markRunning(t)

	// Steady state first: ha-mcp is up, and its env file carries no Home
	// Assistant credential because nothing has resolved yet.
	require.NoError(t, seam.orch.Reconcile(context.Background()))
	before := seam.readEnv(t)
	require.NotContains(t, before, "HOMEASSISTANT_URL")

	seam.rt.resetCounts()
	seam.orch.converge(context.Background(), []Intent{externalHAIntent()})

	after := seam.readEnv(t)
	assert.NotEqual(t, before, after, "the pass that registered the provider rewrote the config")
	assert.Contains(t, after, "HOMEASSISTANT_URL='"+seamHAURL+"'")
	assert.Contains(t, after, "HOMEASSISTANT_TOKEN='"+seamHAToken+"'")

	ensures, removes, _ := seam.rt.counts()
	assert.Equal(t, 1, removes, "the node whose config changed is recreated once")
	assert.Equal(t, 1, ensures, "and its replacement is created from the new file")

	node, err := seam.g.GetNode(haMCPServerNode)
	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus,
		"the recreate finishes in the pass that asked for it, so the node is not left at STARTING")

	// The next pass must find nothing left to change. Without this the seam would
	// be a restart loop wearing a test's clothing.
	seam.rt.resetCounts()
	require.NoError(t, seam.orch.Reconcile(context.Background()))
	_, removes, _ = seam.rt.counts()
	assert.Equal(t, 0, removes, "a resync over the same bindings touches no container")
}
