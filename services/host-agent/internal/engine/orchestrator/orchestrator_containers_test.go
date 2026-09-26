// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

func newSyncOrchestrator(apps *FakeAppStore, cat catalog.CacheInterface, rt containerruntime.Runtime) *Orchestrator {
	return newSyncOrchestratorWithGraph(graph.New(graph.NewMapRepository()), apps, cat, rt)
}

func newSyncOrchestratorWithGraph(g *graph.Graph, apps *FakeAppStore, cat catalog.CacheInterface, rt containerruntime.Runtime) *Orchestrator {
	return NewOrchestrator(
		g,
		new(MockConfiguratorRegistry),
		cat,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{AppStore: apps, Containers: rt},
	)
}

// seedRunningNode puts a node into the graph as if a previous pass had
// completed it: target RUNNING, actual RUNNING.
func seedRunningNode(t *testing.T, g *graph.Graph, id string, status graph.NodeStatus) {
	t.Helper()
	require.NoError(t, g.AddNode(id))
	require.NoError(t, g.SetTargetStatus(id, graph.StatusRunning))
	require.NoError(t, g.SetActualStatus(id, status, ""))
}

// An installed row whose catalog entry is gone (the app directory was
// removed or renamed, so the loader never produced a metadata.yaml for it)
// must not dereference the nil catalog result. There is no recover()
// anywhere in host-agent, so the old code killed the daemon on the next
// convergence pass; SyncContainerState runs on every pass.
func TestSyncContainerState_CatalogMissDoesNotPanic(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "ghost", DisplayName: "Ghost", Status: "running"})
	rt := new(MockContainerRuntime)
	orch := newSyncOrchestrator(apps, NewFakeCatalogCache(), rt)

	require.NotPanics(t, func() {
		orch.SyncContainerState(context.Background())
	})

	// Nothing was inspectable: the missing app is skipped before the runtime.
	rt.AssertNotCalled(t, "Inspect", mock.Anything, mock.Anything)
	row, err := apps.GetByCatalogID("ghost")
	require.NoError(t, err)
	assert.Equal(t, "running", row.Status, "a catalog miss must not alter the stored status")
}

// The repair path itself, pinned alongside the guard: a single-container app
// whose container is gone is marked stopped so a later pass can re-create
// it.
func TestSyncContainerState_SingleContainerGoneMarksStopped(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", Status: "running"})
	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{
		CatalogID:  "jellyfin",
		Containers: []catalog.ContainerDef{{Name: "apps-jellyfin"}},
	})
	rt := new(MockContainerRuntime)
	rt.On("Inspect", mock.Anything, "apps-jellyfin").
		Return(containerruntime.State{Exists: false}, nil)
	orch := newSyncOrchestrator(apps, cat, rt)

	orch.SyncContainerState(context.Background())

	row, err := apps.GetByCatalogID("jellyfin")
	require.NoError(t, err)
	assert.Equal(t, "stopped", row.Status, "a vanished container must be re-driveable")
}

// A multi-container app is no longer out of scope. Each of its nodes drifts
// on its own, and each one gets the same repair: the node whose container is
// gone goes back to INITIALIZING, the healthy one is left alone.
func TestSyncContainerState_MultiContainerDriftRepaired(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "authentik", DisplayName: "Authentik", Status: "running"})
	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{
		CatalogID: "authentik",
		Containers: []catalog.ContainerDef{
			{Name: "apps-authentik-server"},
			{Name: "apps-authentik-worker"},
		},
	})
	g := graph.New(graph.NewMapRepository())
	seedRunningNode(t, g, "apps-authentik-server", graph.StatusRunning)
	seedRunningNode(t, g, "apps-authentik-worker", graph.StatusRunning)

	rt := new(MockContainerRuntime)
	rt.On("Inspect", mock.Anything, "apps-authentik-server").
		Return(containerruntime.State{Exists: true, Running: true}, nil)
	rt.On("Inspect", mock.Anything, "apps-authentik-worker").
		Return(containerruntime.State{Exists: false}, nil)
	orch := newSyncOrchestratorWithGraph(g, apps, cat, rt)

	orch.SyncContainerState(context.Background())

	server, err := g.GetNode("apps-authentik-server")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, server.ActualStatus,
		"a container that is actually running must not be disturbed")

	worker, err := g.GetNode("apps-authentik-worker")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusInitializing, worker.ActualStatus,
		"the drifted node must be back on the lifecycle path")
	assert.Equal(t, graph.StatusRunning, worker.TargetStatus,
		"the target stays RUNNING so the reset is actually driven")
}

// The core of the bug: the store correction alone never repaired anything.
// The node kept reading RUNNING, target equalled actual, and the reconciler
// had nothing to do, so a killed container stayed dead until host-agent
// restarted.
func TestSyncContainerState_RunningNodeWithMissingContainerIsRedriven(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", Status: "running"})
	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{
		CatalogID:  "jellyfin",
		Containers: []catalog.ContainerDef{{Name: "apps-jellyfin"}},
	})
	g := graph.New(graph.NewMapRepository())
	seedRunningNode(t, g, "apps-jellyfin", graph.StatusRunning)

	rt := new(MockContainerRuntime)
	rt.On("Inspect", mock.Anything, "apps-jellyfin").
		Return(containerruntime.State{Exists: false}, nil)
	orch := newSyncOrchestratorWithGraph(g, apps, cat, rt)

	orch.SyncContainerState(context.Background())

	node, err := g.GetNode("apps-jellyfin")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusInitializing, node.ActualStatus,
		"drift must put the node back where collectWorkForLevel will pick it up")
	assert.NotEmpty(t, node.Error, "the reset must say why it happened")

	// And the reset must actually be work: target differs from actual.
	assert.NotEqual(t, node.TargetStatus, node.ActualStatus)
}

// ERROR is terminal: never retry without an explicit status reset. Drift
// repair must not quietly turn a terminal failure into a retry.
func TestSyncContainerState_ErrorNodeIsNotRedriven(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", Status: "error"})
	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{
		CatalogID:  "jellyfin",
		Containers: []catalog.ContainerDef{{Name: "apps-jellyfin"}},
	})
	g := graph.New(graph.NewMapRepository())
	seedRunningNode(t, g, "apps-jellyfin", graph.StatusError)

	rt := new(MockContainerRuntime)
	rt.On("Inspect", mock.Anything, "apps-jellyfin").
		Return(containerruntime.State{Exists: false}, nil)
	orch := newSyncOrchestratorWithGraph(g, apps, cat, rt)

	orch.SyncContainerState(context.Background())

	node, err := g.GetNode("apps-jellyfin")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusError, node.ActualStatus,
		"drift repair must not silently retry a terminal ERROR")
}

// An app that is on its way out must not be re-driven back to life. Its
// containers are expected to be missing, which is not drift.
func TestSyncContainerState_UninstallingAppIsNotRedriven(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", Status: "uninstalling"})
	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{
		CatalogID:  "jellyfin",
		Containers: []catalog.ContainerDef{{Name: "apps-jellyfin"}},
	})
	g := graph.New(graph.NewMapRepository())
	seedRunningNode(t, g, "apps-jellyfin", graph.StatusRunning)

	rt := new(MockContainerRuntime)
	// Container still present: the removal has not finished, so nothing is
	// uninstalled yet and nothing is re-driven.
	rt.On("Inspect", mock.Anything, "apps-jellyfin").
		Return(containerruntime.State{Exists: true, Running: false}, nil)
	orch := newSyncOrchestratorWithGraph(g, apps, cat, rt)

	orch.SyncContainerState(context.Background())

	node, err := g.GetNode("apps-jellyfin")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus,
		"an uninstalling app must never be pushed back onto the lifecycle path")
	row, err := apps.GetByCatalogID("jellyfin")
	require.NoError(t, err)
	assert.Equal(t, "uninstalling", row.Status,
		"the row is only removed once every container is gone")
}

// The no-drift case, so the repair cannot become a per-pass recreate loop:
// a RUNNING node whose container is genuinely running is left untouched.
func TestSyncContainerState_RunningNodeWithRunningContainerIsUntouched(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", Status: "running"})
	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{
		CatalogID:  "jellyfin",
		Containers: []catalog.ContainerDef{{Name: "apps-jellyfin"}},
	})
	g := graph.New(graph.NewMapRepository())
	seedRunningNode(t, g, "apps-jellyfin", graph.StatusRunning)

	rt := new(MockContainerRuntime)
	rt.On("Inspect", mock.Anything, "apps-jellyfin").
		Return(containerruntime.State{Exists: true, Running: true}, nil)
	orch := newSyncOrchestratorWithGraph(g, apps, cat, rt)

	orch.SyncContainerState(context.Background())

	node, err := g.GetNode("apps-jellyfin")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus)
	assert.Empty(t, node.Error)
	row, err := apps.GetByCatalogID("jellyfin")
	require.NoError(t, err)
	assert.Equal(t, "running", row.Status)
}
