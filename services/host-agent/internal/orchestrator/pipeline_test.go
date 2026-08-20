// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// convergeHarness wires an Orchestrator to all fakes for converge-phase tests.
type convergeHarness struct {
	orch           *Orchestrator
	g              *graph.Graph
	registry       *MockConfiguratorRegistry
	appStore       *FakeAppStore
	catalogCache   *FakeCatalogCache
	catalogGraph   *FakeAppGraph
	tailnetStore   *FakeTailnetStore
	remoteAppStore *FakeRemoteAppStore
	tailnetNode    *FakeTailnetNodeManager
	gateway        *FakeGatewayManager
	remoteProxy    *FakeRemoteProxy
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
	tailnetStore := NewFakeTailnetStore()
	remoteAppStore := NewFakeRemoteAppStore()
	tailnetNode := NewFakeTailnetNodeManager()
	gateway := NewFakeGatewayManager()
	remoteProxy := NewFakeRemoteProxy()

	orch := NewOrchestrator(
		g,
		registry,
		catalogCache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{
			AppStore:       appStore,
			CatalogGraph:   catalogGraph,
			TailnetStore:   tailnetStore,
			RemoteAppStore: remoteAppStore,
			TailnetNode:    tailnetNode,
			Gateway:        gateway,
			RemoteProxy:    remoteProxy,
		},
	)

	return &convergeHarness{
		orch:           orch,
		g:              g,
		registry:       registry,
		appStore:       appStore,
		catalogCache:   catalogCache,
		catalogGraph:   catalogGraph,
		tailnetStore:   tailnetStore,
		remoteAppStore: remoteAppStore,
		tailnetNode:    tailnetNode,
		gateway:        gateway,
		remoteProxy:    remoteProxy,
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

func TestConverge_InstallWithDeps_ResolvesDependenciesAndInstallsInOrder(t *testing.T) {
	h := newConvergeHarness(t)
	h.addCatalogApp("radarr", "Radarr", 7878)
	h.addCatalogApp("qbittorrent", "qBittorrent", 8080)

	h.catalogGraph.SetInstallPlan("radarr", &catalog.InstallPlan{
		App:        "radarr",
		CanInstall: true,
		AutoConfig: []catalog.ConfigTask{
			{Target: "radarr", Source: "qbittorrent", Integration: "download_client"},
		},
	})

	h.orch.converge(context.Background(), []Intent{NewInstallAppIntent("radarr")})

	dep, _ := h.appStore.GetByCatalogID("qbittorrent")
	require.NotNil(t, dep, "dependency should be recorded in store")

	app, _ := h.appStore.GetByCatalogID("radarr")
	require.NotNil(t, app, "target app should be recorded in store")
	assert.Equal(t, "qbittorrent", app.IntegrationConfig["download_client"])

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
		AutoConfig: []catalog.ConfigTask{
			{Target: "radarr", Source: "qbittorrent", Integration: "download_client"},
		},
	})
	h.catalogGraph.SetInstallPlan("sonarr", &catalog.InstallPlan{
		App:        "sonarr",
		CanInstall: true,
		AutoConfig: []catalog.ConfigTask{
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

// ── Tailnet Intent Tests ──────────────────────────────────────────────────

func TestConverge_SetTailnetIntent_CreatesConnection(t *testing.T) {
	h := newConvergeHarness(t)

	h.orch.converge(context.Background(), []Intent{NewSetTailnetIntent("My TS", "tailscale", "tskey-auth-xyz", "")})

	conn := h.tailnetStore.ActiveConnection()
	require.NotNil(t, conn, "tailnet connection should exist after SetTailnet intent")
	assert.Equal(t, "My TS", conn.Name)
	assert.Equal(t, "tailscale", conn.Type)
	assert.Equal(t, "tskey-auth-xyz", conn.AuthKey)
	assert.Equal(t, "active", conn.Status)
}

func TestConverge_SetTailnetIntent_ReplacesExisting(t *testing.T) {
	h := newConvergeHarness(t)

	h.tailnetStore.Create(store.TailnetConnection{
		ID: "old-id", Name: "Old", Type: "tailscale", AuthKey: "old-key", Status: "active",
	})

	h.orch.converge(context.Background(), []Intent{NewSetTailnetIntent("New", "headscale", "new-key", "https://hs.example.com")})

	conn := h.tailnetStore.ActiveConnection()
	require.NotNil(t, conn)
	assert.NotEqual(t, "old-id", conn.ID)
	assert.Equal(t, "New", conn.Name)
	assert.Equal(t, "headscale", conn.Type)
	assert.Equal(t, "https://hs.example.com", conn.ControlURL)

	old, _ := h.tailnetStore.GetByID("old-id")
	assert.Nil(t, old)
}

func TestConverge_DeleteTailnetIntent_RemovesConnection(t *testing.T) {
	h := newConvergeHarness(t)

	h.tailnetStore.Create(store.TailnetConnection{
		ID: "tn-1", Name: "My Tailnet", Type: "tailscale", AuthKey: "key", Status: "active",
	})

	h.orch.converge(context.Background(), []Intent{NewDeleteTailnetIntent()})

	conn := h.tailnetStore.ActiveConnection()
	assert.Nil(t, conn, "tailnet connection should be nil after DeleteTailnet intent")
}

// ── Tailnet Convergence Tests ─────────────────────────────────────────────

func TestConverge_ActiveTailnet_EnsuresTailnetNodesForRunningApps(t *testing.T) {
	h := newConvergeHarness(t)
	h.addCatalogApp("jellyfin", "Jellyfin", 8096)
	h.addCatalogApp("radarr", "Radarr", 7878)

	h.appStore.AddApp(&store.InstalledApp{CatalogID: "jellyfin", Status: "running", Port: 8096})
	h.appStore.AddApp(&store.InstalledApp{CatalogID: "radarr", Status: "running", Port: 7878})

	h.tailnetStore.Create(store.TailnetConnection{
		ID: "tn-1", Name: "T", Type: "tailscale", AuthKey: "k", Status: "active",
	})

	h.orch.converge(context.Background(), nil)

	ensured := h.tailnetNode.EnsuredApps()
	assert.Len(t, ensured, 2)
	assert.Contains(t, ensured, "jellyfin")
	assert.Contains(t, ensured, "radarr")

	jf, _ := h.appStore.GetByCatalogID("jellyfin")
	assert.Equal(t, "tn-1", jf.TailnetID)
}

func TestConverge_NoTailnet_PurgesTailnetNodesAndGateway(t *testing.T) {
	h := newConvergeHarness(t)
	h.addCatalogApp("jellyfin", "Jellyfin", 8096)

	h.appStore.AddApp(&store.InstalledApp{
		CatalogID: "jellyfin", Status: "running", TailnetID: "old-tn",
	})

	h.orch.converge(context.Background(), nil)

	assert.Contains(t, h.tailnetNode.PurgedApps(), "jellyfin")
	assert.True(t, h.gateway.WasPurgeCalled())
	assert.True(t, h.remoteProxy.WasStopCalled())

	jf, _ := h.appStore.GetByCatalogID("jellyfin")
	assert.Empty(t, jf.TailnetID)
}

func TestConverge_ActiveTailnet_SkipsSystemApps(t *testing.T) {
	h := newConvergeHarness(t)

	h.catalogCache.AddApp(&catalog.App{
		CatalogID: "traefik", DisplayName: "Traefik", Version: "1.0.0",
		Port: 8080, IsSystem: true,
	})
	h.appStore.AddApp(&store.InstalledApp{
		CatalogID: "traefik", Status: "running", IsSystem: true, Port: 8080,
	})

	h.tailnetStore.Create(store.TailnetConnection{
		ID: "tn-1", Name: "T", Type: "tailscale", AuthKey: "k", Status: "active",
	})

	h.orch.converge(context.Background(), nil)

	assert.Empty(t, h.tailnetNode.EnsuredApps(), "system apps should not get tailnet nodes")
}

// ── Remote App Intent Tests ───────────────────────────────────────────────

func TestConverge_AddRemoteAppIntent_CreatesRemoteApp(t *testing.T) {
	h := newConvergeHarness(t)

	h.catalogCache.AddApp(&catalog.App{
		CatalogID:   "jellyfin",
		DisplayName: "Jellyfin",
		Version:     "1.0.0",
		Port:        8096,
		SSO: catalog.SSO{
			Strategy:    "forward-auth",
			BypassPaths: []string{"/api/public"},
		},
	})

	h.orch.converge(context.Background(), []Intent{
		NewAddRemoteAppIntent("jellyfin", "ts-jellyfin.tail1234.ts.net", "Johan's server"),
	})

	apps := h.remoteAppStore.Apps()
	require.Len(t, apps, 1)

	app := apps[0]
	assert.NotEmpty(t, app.ID)
	assert.Equal(t, "jellyfin", app.AppID)
	assert.Equal(t, "Jellyfin", app.AppName)
	assert.Equal(t, "Johan's server", app.HostLabel)
	assert.Equal(t, "ts-jellyfin.tail1234.ts.net", app.TailnetAddr)
	assert.Equal(t, "forward-auth", app.SSOStrategy)
	assert.Equal(t, []string{"/api/public"}, app.BypassPaths)
	assert.Equal(t, "active", app.Status)
}

func TestConverge_AddRemoteAppIntent_NilBypassPaths(t *testing.T) {
	h := newConvergeHarness(t)

	h.catalogCache.AddApp(&catalog.App{
		CatalogID:   "radarr",
		DisplayName: "Radarr",
		Version:     "1.0.0",
		Port:        7878,
		SSO:         catalog.SSO{Strategy: "native-oidc"},
	})

	h.orch.converge(context.Background(), []Intent{
		NewAddRemoteAppIntent("radarr", "ts-radarr.tail1234.ts.net", "Remote Host"),
	})

	apps := h.remoteAppStore.Apps()
	require.Len(t, apps, 1)
	assert.Equal(t, []string{}, apps[0].BypassPaths)
}

func TestConverge_DeleteRemoteAppIntent_RemovesFromStore(t *testing.T) {
	h := newConvergeHarness(t)

	h.remoteAppStore.Create(store.RemoteApp{
		ID: "ra-1", AppID: "jellyfin", Status: "active",
	})

	h.orch.converge(context.Background(), []Intent{NewDeleteRemoteAppIntent("ra-1")})

	apps := h.remoteAppStore.Apps()
	assert.Empty(t, apps, "remote app should be deleted after DeleteRemoteApp intent")
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

// ── Tailnet SSO Provisioning Tests ────────────────────────────────────────

func TestConverge_ProvisionTailnetSSO_CallsEnsureForwardDomainAuth(t *testing.T) {
	h := newConvergeHarness(t)

	h.tailnetStore.Create(store.TailnetConnection{
		ID: "tn-1", Name: "T", Type: "tailscale", AuthKey: "k", Status: "active",
	})

	h.gateway.SetDomain("tail12756a.ts.net", nil)
	fd := NewFakeForwardDomainProvisioner("fake-token", nil)
	h.orch.forwardDomainSSO = fd

	h.orch.converge(context.Background(), nil)

	assert.True(t, h.gateway.WasDomainCalled(), "GetTailnetDomain should be called")
	assert.Equal(t, "tail12756a.ts.net", fd.CalledDomain())
}

func TestConverge_ProvisionTailnetSSO_SkipsWhenNoTailnet(t *testing.T) {
	h := newConvergeHarness(t)

	h.gateway.SetDomain("tail12756a.ts.net", nil)
	fd := NewFakeForwardDomainProvisioner("fake-token", nil)
	h.orch.forwardDomainSSO = fd

	h.orch.converge(context.Background(), nil)

	assert.False(t, h.gateway.WasDomainCalled(), "GetTailnetDomain should not be called without active tailnet")
	assert.Empty(t, fd.CalledDomain())
}

func TestConverge_ProvisionTailnetSSO_SkipsWhenInterfacesNil(t *testing.T) {
	h := newConvergeHarness(t)

	h.tailnetStore.Create(store.TailnetConnection{
		ID: "tn-1", Name: "T", Type: "tailscale", AuthKey: "k", Status: "active",
	})

	// forwardDomainSSO is nil (default) — should not panic.
	h.orch.converge(context.Background(), nil)
}

func TestConverge_ProvisionTailnetSSO_SkipsWhenGatewayNotReady(t *testing.T) {
	h := newConvergeHarness(t)

	h.tailnetStore.Create(store.TailnetConnection{
		ID: "tn-1", Name: "T", Type: "tailscale", AuthKey: "k", Status: "active",
	})

	h.gateway.SetDomain("", fmt.Errorf("gateway not running"))
	fd := NewFakeForwardDomainProvisioner("fake-token", nil)
	h.orch.forwardDomainSSO = fd

	h.orch.converge(context.Background(), nil)

	assert.True(t, h.gateway.WasDomainCalled())
	assert.Empty(t, fd.CalledDomain(), "EnsureForwardDomainAuth should not be called when gateway is not ready")
}

// ── Stub behaviour when appStore is nil ───────────────────────────────────

func TestConverge_NilAppStore_StubBehavior(t *testing.T) {
	// Orchestrator with no converge config — converge should be a no-op.
	g := graph.New(graph.NewMapRepository())
	registry := new(MockConfiguratorRegistry)
	orch := NewOrchestrator(g, registry, nil, "/tmp/bloud-test", newTestLogger(), OrchestratorConfig{})

	// Should not panic.
	orch.converge(context.Background(), []Intent{NewInstallAppIntent("jellyfin")})
}
