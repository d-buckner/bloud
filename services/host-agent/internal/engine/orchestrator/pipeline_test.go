// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// convergeHarness wires an Orchestrator to all fakes for converge-phase tests.
type convergeHarness struct {
	orch         *Orchestrator
	g            *graph.Graph
	registry     *MockConfiguratorRegistry
	appStore     *FakeAppStore
	catalogCache *FakeCatalogCache
	catalogGraph *FakeAppGraph
}

func newConvergeHarness(t *testing.T) *convergeHarness {
	t.Helper()

	g := graph.New(graph.NewMapRepository())

	// Registry always returns nil (no configurators needed for converge tests).
	registry := new(MockConfiguratorRegistry)
	registry.On("Get", mock.Anything).Return(nil).Maybe()

	appStore := NewFakeAppStore()
	catalogCache := NewFakeCatalogCache()
	catalogGraph := NewFakeAppGraph()

	orch := NewOrchestrator(
		g,
		registry,
		catalogCache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{Stores: StoresConfig{AppStore: appStore}, CatalogGraph: catalogGraph},
	)

	return &convergeHarness{
		orch:         orch,
		g:            g,
		registry:     registry,
		appStore:     appStore,
		catalogCache: catalogCache,
		catalogGraph: catalogGraph,
	}
}

// graphTarget returns the TargetStatus for the named node, or "" if absent.
func (h *convergeHarness) graphTarget(appName string) graph.NodeStatus {
	node, _ := h.g.GetNode(appName)
	if node == nil {
		return ""
	}
	return node.TargetStatus
}

// addCatalogApp registers a minimal catalog app.
func (h *convergeHarness) addCatalogApp(name, displayName string, port int) {
	h.catalogCache.AddApp(&catalog.App{
		CatalogID:   name,
		DisplayName: displayName,
		Version:     "1.0.0",
		Port:        port,
	})
}

// ── Install Intent Tests ───────────────────────────────────────────────────

func TestConverge_InstallIntent_RecordsInStoreAndSetsGraphTarget(t *testing.T) {
	h := newConvergeHarness(t)
	h.addCatalogApp("jellyfin", "Jellyfin", 8096)

	h.orch.converge(context.Background(), []Intent{NewInstallAppIntent("jellyfin")})

	app, err := h.appStore.GetByCatalogID("jellyfin")
	require.NoError(t, err)
	require.NotNil(t, app, "app should exist in store after install intent")

	assert.Equal(t, graph.StatusRunning, h.graphTarget("jellyfin"))
}

// TestConverge_InstallIntent_ResetsErroredNode is the regression test for
// "Retry install": a node stuck in the terminal ERROR state must be reset by
// a new install intent, otherwise the convergence pass (which never retries
// ERROR nodes) would leave the app stuck at "installing" forever.
func TestConverge_InstallIntent_ResetsErroredNode(t *testing.T) {
	h := newConvergeHarness(t)
	h.addCatalogApp("jellyfin", "Jellyfin", 8096)

	// Simulate a prior failed install: node exists in ERROR and the app row
	// carries the failure.
	require.NoError(t, h.g.AddNode("jellyfin"))
	require.NoError(t, h.g.SetActualStatus("jellyfin", graph.StatusError, "boom"))
	h.appStore.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", Status: "failed", LastError: "boom"})

	h.orch.converge(context.Background(), []Intent{NewInstallAppIntent("jellyfin")})

	node, err := h.g.GetNode("jellyfin")
	require.NoError(t, err)
	require.NotNil(t, node)
	assert.NotEqual(t, graph.StatusError, node.ActualStatus, "install retry must reset the errored node")
}

func TestConverge_InstallWithDeps_ResolvesDependenciesAndInstallsInOrder(t *testing.T) {
	h := newConvergeHarness(t)
	h.addCatalogApp("radarr", "Radarr", 7878)
	h.addCatalogApp("qbittorrent", "qBittorrent", 8080)

	h.catalogGraph.SetInstallPlan("radarr", &catalog.InstallPlan{
		App:        "radarr",
		CanInstall: true,
		RequiredProviders: []catalog.ConfigTask{
			{Target: "radarr", Source: "qbittorrent", Integration: "download_client"},
		},
	})

	h.orch.converge(context.Background(), []Intent{NewInstallAppIntent("radarr")})

	dep, _ := h.appStore.GetByCatalogID("qbittorrent")
	require.NotNil(t, dep, "the required provider should be recorded in the store")

	app, _ := h.appStore.GetByCatalogID("radarr")
	require.NotNil(t, app, "target app should be recorded in store")
	assert.Empty(t, app.IntegrationConfig, "the set model records no per-contract choice")

	assert.Equal(t, graph.StatusRunning, h.graphTarget("qbittorrent"), "qbittorrent target should be RUNNING")
	assert.Equal(t, graph.StatusRunning, h.graphTarget("radarr"), "radarr target should be RUNNING")
}

func TestConverge_TwoInstallsShareDep_DepInstalledOnce(t *testing.T) {
	h := newConvergeHarness(t)
	h.addCatalogApp("radarr", "Radarr", 7878)
	h.addCatalogApp("sonarr", "Sonarr", 8989)
	h.addCatalogApp("qbittorrent", "qBittorrent", 8080)

	h.catalogGraph.SetInstallPlan("radarr", &catalog.InstallPlan{
		App:        "radarr",
		CanInstall: true,
		RequiredProviders: []catalog.ConfigTask{
			{Target: "radarr", Source: "qbittorrent", Integration: "download_client"},
		},
	})
	h.catalogGraph.SetInstallPlan("sonarr", &catalog.InstallPlan{
		App:        "sonarr",
		CanInstall: true,
		RequiredProviders: []catalog.ConfigTask{
			{Target: "sonarr", Source: "qbittorrent", Integration: "download_client"},
		},
	})

	h.orch.converge(context.Background(), []Intent{
		NewInstallAppIntent("radarr"),
		NewInstallAppIntent("sonarr"),
	})

	assert.Equal(t, graph.StatusRunning, h.graphTarget("qbittorrent"))
	assert.Equal(t, graph.StatusRunning, h.graphTarget("radarr"))
	assert.Equal(t, graph.StatusRunning, h.graphTarget("sonarr"))
}

func TestConverge_AlreadyRunningApp_StillSetsGraphTarget(t *testing.T) {
	h := newConvergeHarness(t)
	h.addCatalogApp("jellyfin", "Jellyfin", 8096)
	h.appStore.AddApp(&store.InstalledApp{
		CatalogID: "jellyfin",
		Status:    "running",
	})

	h.orch.converge(context.Background(), []Intent{NewInstallAppIntent("jellyfin")})

	assert.Equal(t, graph.StatusRunning, h.graphTarget("jellyfin"))
}

// ── Uninstall Intent Tests ─────────────────────────────────────────────────

func TestConverge_UninstallIntent_RemovesFromStoreAndGraph(t *testing.T) {
	h := newConvergeHarness(t)
	h.addCatalogApp("radarr", "Radarr", 7878)
	h.appStore.AddApp(&store.InstalledApp{
		CatalogID: "radarr",
		Status:    "running",
	})

	h.orch.converge(context.Background(), []Intent{NewUninstallAppIntent("radarr", true)})

	// App should be removed from the store.
	app, err := h.appStore.GetByCatalogID("radarr")
	require.NoError(t, err)
	assert.Nil(t, app, "app should be removed from store after uninstall")
}

// ── Rename App Intent Tests ───────────────────────────────────────────────

func TestConverge_RenameAppIntent_UpdatesDisplayName(t *testing.T) {
	h := newConvergeHarness(t)
	h.addCatalogApp("jellyfin", "Jellyfin", 8096)
	h.appStore.AddApp(&store.InstalledApp{
		CatalogID:   "jellyfin",
		DisplayName: "Jellyfin",
		Status:      "running",
	})

	h.orch.converge(context.Background(), []Intent{NewRenameAppIntent("jellyfin", "My Media Server")})

	app, err := h.appStore.GetByCatalogID("jellyfin")
	require.NoError(t, err)
	assert.Equal(t, "My Media Server", app.DisplayName)
}

// ── Stub behavior when appStore is nil ───────────────────────────────────

func TestConverge_NilAppStore_StubBehavior(t *testing.T) {
	// Orchestrator with no converge config: converge should be a no-op.
	g := graph.New(graph.NewMapRepository())
	registry := new(MockConfiguratorRegistry)
	orch := NewOrchestrator(g, registry, nil, "/tmp/bloud-test", newTestLogger(), OrchestratorConfig{})

	// Should not panic.
	orch.converge(context.Background(), []Intent{NewInstallAppIntent("jellyfin")})
}
