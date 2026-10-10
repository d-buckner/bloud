// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// containerGraph puts a multi-container app in the store and its catalog entry
// in the cache, then hands back the graph built from the given node states.
func containerGraph(t *testing.T, states map[string]orchestrator.NodeState) developerGraph {
	t.Helper()
	mod := newSystemModule(t, systemModuleOpts{})
	mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID: "immich", DisplayName: "Immich", Status: "running",
	})
	mod.catalog.(*FakeCatalogCache).AddApp(&catalog.App{
		CatalogID:   "immich",
		DisplayName: "Immich",
		Containers: []catalog.ContainerDef{
			{Name: "apps-immich-postgres"},
			{Name: "apps-immich-server", DependsOn: []string{"apps-immich-postgres"}},
		},
	})
	mod.orch.(*fakeSystemOrchestrator).states = states
	return fetchDeveloperGraph(t, mod)
}

func graphNodeByName(nodes []graphNode, name string) (graphNode, bool) {
	for _, n := range nodes {
		if n.ID == name {
			return n, true
		}
	}
	return graphNode{}, false
}

// The app is not a node the engine drives, so its phase is whichever container
// is furthest behind: an app with one container still starting is starting, and
// saying "running" because the other one is up would be the graph claiming a
// convergence that has not happened.
func TestSystemHTTP_DeveloperGraph_AppNodeRollsUpItsContainers(t *testing.T) {
	resp := containerGraph(t, map[string]orchestrator.NodeState{
		"apps-immich-postgres": {Phase: "running", Target: "running"},
		"apps-immich-server":   {Phase: "starting", Target: "running", InFlight: true},
	})

	app, ok := graphNodeByName(resp.Nodes, "immich")
	require.True(t, ok)
	assert.Equal(t, "starting", app.Phase)
	assert.True(t, app.InFlight, "the app is in flight while any container is")
	assert.Empty(t, app.Reason)
}

// A failed container wins the rollup even when another is further along, and
// its reason travels with it: that text is the only answer to "why is this
// stuck" that does not require reading the log.
func TestSystemHTTP_DeveloperGraph_AppNodeRollsUpTheFailure(t *testing.T) {
	resp := containerGraph(t, map[string]orchestrator.NodeState{
		"apps-immich-postgres": {Phase: "failed", Reason: "image pull: dial tcp: i/o timeout"},
		"apps-immich-server":   {Phase: "starting"},
	})

	app, ok := graphNodeByName(resp.Nodes, "immich")
	require.True(t, ok)
	assert.Equal(t, "failed", app.Phase)
	assert.Equal(t, "image pull: dial tcp: i/o timeout", app.Reason)
	assert.False(t, app.InFlight, "an errored node is stalled, not being worked on")
}

// With every container at its target the app is done: nothing is in flight, and
// the steady-state picture has to be distinguishable from a pass in progress.
func TestSystemHTTP_DeveloperGraph_AppNodeSettledWhenContainersAre(t *testing.T) {
	resp := containerGraph(t, map[string]orchestrator.NodeState{
		"apps-immich-postgres": {Phase: "running", Target: "running"},
		"apps-immich-server":   {Phase: "running", Target: "running"},
	})

	app, ok := graphNodeByName(resp.Nodes, "immich")
	require.True(t, ok)
	assert.Equal(t, "running", app.Phase)
	assert.False(t, app.InFlight)
}

// A container node carries the live phase, the reason it stopped, and whether
// the engine has it in hand, which is what lets the graph light up the current
// node instead of the whole canvas at once.
func TestSystemHTTP_DeveloperGraph_ContainerNodesCarryLiveState(t *testing.T) {
	resp := containerGraph(t, map[string]orchestrator.NodeState{
		"apps-immich-postgres": {Phase: "running", Target: "running"},
		"apps-immich-server": {
			Phase: "configuring", Target: "running",
			Reason: "postgres not reachable", InFlight: true,
		},
	})

	server, ok := graphNodeByName(resp.Nodes, "apps-immich-server")
	require.True(t, ok)
	assert.Equal(t, "configuring", server.Phase)
	assert.Equal(t, "postgres not reachable", server.Reason)
	assert.True(t, server.InFlight)

	postgres, ok := graphNodeByName(resp.Nodes, "apps-immich-postgres")
	require.True(t, ok)
	assert.Equal(t, "running", postgres.Phase)
	assert.False(t, postgres.InFlight)
}

// With no engine wired the nodes keep their stored status and claim no phase of
// their own, so the display falls back rather than inventing a lifecycle.
func TestSystemHTTP_DeveloperGraph_NodesWithoutAnEngineReportNoPhase(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	mod.orch = nil
	mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID: "immich", DisplayName: "Immich", Status: "running",
	})

	resp := fetchDeveloperGraph(t, mod)

	node, ok := graphNodeByName(resp.Nodes, "immich")
	require.True(t, ok)
	assert.Equal(t, "running", node.Status)
	assert.Empty(t, node.Phase)
	assert.False(t, node.InFlight)
}
