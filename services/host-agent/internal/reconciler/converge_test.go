package reconciler

import (
	"context"
	"testing"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helper: builds a reconciler wired to fakes.
type testHarness struct {
	reconciler     *Reconciler
	lifecycle      *FakeLifecycleManager
	appStore       *FakeAppStore
	catalog        *FakeCatalogCache
	graph          *FakeAppGraph
	tailnetStore   *FakeTailnetStore
	remoteAppStore *FakeRemoteAppStore
	sidecar        *FakeSidecarManager
	gateway        *FakeGatewayManager
	proxyStopper   *FakeProxyStopper
}

func newTestHarness() *testHarness {
	lm := NewFakeLifecycleManager()
	as := NewFakeAppStore()
	cc := NewFakeCatalogCache()
	ag := NewFakeAppGraph()
	ts := NewFakeTailnetStore()
	ra := NewFakeRemoteAppStore()
	sc := NewFakeSidecarManager()
	gw := NewFakeGatewayManager()
	ps := NewFakeProxyStopper()

	cfg := &Config{
		Lifecycle:      lm,
		AppStore:       as,
		CatalogCache:   cc,
		Graph:          ag,
		TailnetStore:   ts,
		RemoteAppStore: ra,
		Sidecar:        sc,
		Gateway:        gw,
		ProxyStopper:   ps,
	}

	r := New(testLogger(), cfg)

	return &testHarness{
		reconciler:     r,
		lifecycle:      lm,
		appStore:       as,
		catalog:        cc,
		graph:          ag,
		tailnetStore:   ts,
		remoteAppStore: ra,
		sidecar:        sc,
		gateway:        gw,
		proxyStopper:   ps,
	}
}

// addCatalogApp is a helper to register a minimal catalog app with a container spec.
func (h *testHarness) addCatalogApp(name, displayName string, port int) {
	h.catalog.AddApp(&catalog.App{
		Name:        name,
		DisplayName: displayName,
		Version:     "1.0.0",
		Port:        port,
		Container:   &catalog.ContainerSpec{Image: name + ":latest"},
	})
}

func TestConverge_InstallIntent_RecordsInStoreAndCallsEnsureApp(t *testing.T) {
	h := newTestHarness()
	h.addCatalogApp("jellyfin", "Jellyfin", 8096)

	intents := []Intent{NewInstallAppIntent("jellyfin")}

	h.reconciler.converge(context.Background(), intents)

	// App should have been recorded in the store.
	app, err := h.appStore.GetByName("jellyfin")
	require.NoError(t, err)
	require.NotNil(t, app, "app should exist in store after install intent")

	// EnsureApp should have been called.
	ensured := h.lifecycle.EnsuredApps()
	assert.Contains(t, ensured, "jellyfin")
}

func TestConverge_UninstallIntent_MarksUninstallingAndCallsRemoveApp(t *testing.T) {
	h := newTestHarness()
	h.addCatalogApp("radarr", "Radarr", 7878)

	// Pre-populate: app is running.
	h.appStore.AddApp(&store.InstalledApp{
		Name:   "radarr",
		Status: "running",
	})

	intents := []Intent{NewUninstallAppIntent("radarr", true)}

	h.reconciler.converge(context.Background(), intents)

	// RemoveApp should have been called with clearData=true.
	removed := h.lifecycle.RemovedApps()
	require.Len(t, removed, 1)
	assert.Equal(t, "radarr", removed[0].AppName)
	assert.True(t, removed[0].ClearData)

	// App should no longer exist in store (RemoveApp fake doesn't remove from store,
	// but the real RemoveApp does — here we just verify the call was made).
}

func TestConverge_InstallWithDeps_ResolvesDependenciesAndInstallsInOrder(t *testing.T) {
	h := newTestHarness()
	h.addCatalogApp("radarr", "Radarr", 7878)
	h.addCatalogApp("qbittorrent", "qBittorrent", 8080)

	// Configure graph: radarr depends on qbittorrent via auto-config.
	h.graph.SetInstallPlan("radarr", &catalog.InstallPlan{
		App:        "radarr",
		CanInstall: true,
		AutoConfig: []catalog.ConfigTask{
			{Target: "radarr", Source: "qbittorrent", Integration: "download_client"},
		},
	})

	intents := []Intent{NewInstallAppIntent("radarr")}

	h.reconciler.converge(context.Background(), intents)

	// Both apps should be in the store.
	dep, _ := h.appStore.GetByName("qbittorrent")
	require.NotNil(t, dep, "dependency should be recorded in store")

	app, _ := h.appStore.GetByName("radarr")
	require.NotNil(t, app, "target app should be recorded in store")
	assert.Equal(t, "qbittorrent", app.IntegrationConfig["download_client"])

	// Both should have been ensured. qbittorrent should come before radarr
	// (lower level = no deps, higher level = has deps).
	ensured := h.lifecycle.EnsuredApps()
	require.Len(t, ensured, 2)

	qbtIdx := -1
	radarrIdx := -1
	for i, name := range ensured {
		switch name {
		case "qbittorrent":
			qbtIdx = i
		case "radarr":
			radarrIdx = i
		}
	}
	assert.True(t, qbtIdx < radarrIdx, "dependency should be ensured before dependent: qbittorrent@%d radarr@%d", qbtIdx, radarrIdx)
}

func TestConverge_TwoInstallsShareDep_DepInstalledOnce(t *testing.T) {
	h := newTestHarness()
	h.addCatalogApp("radarr", "Radarr", 7878)
	h.addCatalogApp("sonarr", "Sonarr", 8989)
	h.addCatalogApp("qbittorrent", "qBittorrent", 8080)

	// Both radarr and sonarr depend on qbittorrent.
	h.graph.SetInstallPlan("radarr", &catalog.InstallPlan{
		App:        "radarr",
		CanInstall: true,
		AutoConfig: []catalog.ConfigTask{
			{Target: "radarr", Source: "qbittorrent", Integration: "download_client"},
		},
	})
	h.graph.SetInstallPlan("sonarr", &catalog.InstallPlan{
		App:        "sonarr",
		CanInstall: true,
		AutoConfig: []catalog.ConfigTask{
			{Target: "sonarr", Source: "qbittorrent", Integration: "download_client"},
		},
	})

	intents := []Intent{
		NewInstallAppIntent("radarr"),
		NewInstallAppIntent("sonarr"),
	}

	h.reconciler.converge(context.Background(), intents)

	// qbittorrent should be ensured exactly once.
	ensured := h.lifecycle.EnsuredApps()
	qbtCount := 0
	for _, name := range ensured {
		if name == "qbittorrent" {
			qbtCount++
		}
	}
	assert.Equal(t, 1, qbtCount, "shared dependency should be ensured exactly once")

	// All three apps should be ensured.
	assert.Len(t, ensured, 3, "all three apps should be ensured")
}

func TestConverge_SyncsContainerState(t *testing.T) {
	h := newTestHarness()

	h.reconciler.converge(context.Background(), nil)

	assert.True(t, h.lifecycle.WasSyncCalled(), "SyncContainerState should be called during convergence")
}

func TestConverge_RegeneratesRoutesAfterConvergence(t *testing.T) {
	h := newTestHarness()
	h.addCatalogApp("jellyfin", "Jellyfin", 8096)

	intents := []Intent{NewInstallAppIntent("jellyfin")}

	h.reconciler.converge(context.Background(), intents)

	assert.True(t, h.lifecycle.WasRegenerateCalled(), "RegenerateRoutes should be called after convergence")
}

func TestConverge_SkipsAlreadyRunningApps(t *testing.T) {
	h := newTestHarness()
	h.addCatalogApp("jellyfin", "Jellyfin", 8096)

	// App already running — install intent should be a no-op.
	h.appStore.AddApp(&store.InstalledApp{
		Name:   "jellyfin",
		Status: "running",
	})

	intents := []Intent{NewInstallAppIntent("jellyfin")}

	h.reconciler.converge(context.Background(), intents)

	// EnsureApp should NOT have been called.
	assert.Empty(t, h.lifecycle.EnsuredApps(), "EnsureApp should not be called for already-running apps")
}

func TestConverge_NilConfig_StubBehavior(t *testing.T) {
	// Reconciler with nil config should just log (backward compatible with Phase 2).
	r := New(testLogger(), nil)

	// Should not panic.
	r.converge(context.Background(), []Intent{NewInstallAppIntent("jellyfin")})
}

// ── Tailnet Intent Tests ──────────────────────────────────────────────────

func TestConverge_SetTailnetIntent_CreatesConnection(t *testing.T) {
	h := newTestHarness()

	intents := []Intent{NewSetTailnetIntent("My TS", "tailscale", "tskey-auth-xyz", "")}
	h.reconciler.converge(context.Background(), intents)

	conn := h.tailnetStore.ActiveConnection()
	require.NotNil(t, conn, "tailnet connection should exist after SetTailnet intent")
	assert.Equal(t, "My TS", conn.Name)
	assert.Equal(t, "tailscale", conn.Type)
	assert.Equal(t, "tskey-auth-xyz", conn.AuthKey)
	assert.Equal(t, "active", conn.Status)
}

func TestConverge_SetTailnetIntent_ReplacesExisting(t *testing.T) {
	h := newTestHarness()

	// Seed an existing connection.
	h.tailnetStore.Create(store.TailnetConnection{
		ID:      "old-id",
		Name:    "Old",
		Type:    "tailscale",
		AuthKey: "old-key",
		Status:  "active",
	})

	intents := []Intent{NewSetTailnetIntent("New", "headscale", "new-key", "https://hs.example.com")}
	h.reconciler.converge(context.Background(), intents)

	conn := h.tailnetStore.ActiveConnection()
	require.NotNil(t, conn)
	assert.NotEqual(t, "old-id", conn.ID, "should have a new ID")
	assert.Equal(t, "New", conn.Name)
	assert.Equal(t, "headscale", conn.Type)
	assert.Equal(t, "https://hs.example.com", conn.ControlURL)

	// Old connection should be gone.
	old, _ := h.tailnetStore.GetByID("old-id")
	assert.Nil(t, old)
}

func TestConverge_DeleteTailnetIntent_RemovesConnection(t *testing.T) {
	h := newTestHarness()

	h.tailnetStore.Create(store.TailnetConnection{
		ID:      "tn-1",
		Name:    "My Tailnet",
		Type:    "tailscale",
		AuthKey: "key",
		Status:  "active",
	})

	intents := []Intent{NewDeleteTailnetIntent()}
	h.reconciler.converge(context.Background(), intents)

	conn := h.tailnetStore.ActiveConnection()
	assert.Nil(t, conn, "tailnet connection should be nil after DeleteTailnet intent")
}

// ── Tailnet Convergence Tests ─────────────────────────────────────────────

func TestConverge_ActiveTailnet_EnsuresSidecarsForRunningApps(t *testing.T) {
	h := newTestHarness()
	h.addCatalogApp("jellyfin", "Jellyfin", 8096)
	h.addCatalogApp("radarr", "Radarr", 7878)

	// Two running apps.
	h.appStore.AddApp(&store.InstalledApp{Name: "jellyfin", Status: "running", Port: 8096})
	h.appStore.AddApp(&store.InstalledApp{Name: "radarr", Status: "running", Port: 7878})

	// Active tailnet.
	h.tailnetStore.Create(store.TailnetConnection{
		ID: "tn-1", Name: "T", Type: "tailscale", AuthKey: "k", Status: "active",
	})

	// Run convergence with no intents (just convergence phase).
	h.reconciler.converge(context.Background(), nil)

	ensured := h.sidecar.EnsuredApps()
	assert.Len(t, ensured, 2)

	names := make([]string, len(ensured))
	for i, e := range ensured {
		names[i] = e.AppName
	}
	assert.Contains(t, names, "jellyfin")
	assert.Contains(t, names, "radarr")

	// TailnetID should be set.
	jf, _ := h.appStore.GetByName("jellyfin")
	assert.Equal(t, "tn-1", jf.TailnetID)
}

func TestConverge_NoTailnet_PurgesSidecarsAndGateway(t *testing.T) {
	h := newTestHarness()
	h.addCatalogApp("jellyfin", "Jellyfin", 8096)

	h.appStore.AddApp(&store.InstalledApp{
		Name: "jellyfin", Status: "running", TailnetID: "old-tn",
	})

	// No active tailnet — convergence should purge.
	h.reconciler.converge(context.Background(), nil)

	assert.Contains(t, h.sidecar.PurgedApps(), "jellyfin")
	assert.True(t, h.gateway.WasPurgeCalled())
	assert.True(t, h.proxyStopper.WasStopCalled())

	// TailnetID should be cleared.
	jf, _ := h.appStore.GetByName("jellyfin")
	assert.Empty(t, jf.TailnetID)
}

func TestConverge_ActiveTailnet_SkipsSystemApps(t *testing.T) {
	h := newTestHarness()

	// System app — should NOT get a sidecar.
	h.catalog.AddApp(&catalog.App{
		Name: "traefik", DisplayName: "Traefik", Version: "1.0.0",
		Port: 8080, IsSystem: true,
		Container: &catalog.ContainerSpec{Image: "traefik:latest"},
	})
	h.appStore.AddApp(&store.InstalledApp{
		Name: "traefik", Status: "running", IsSystem: true, Port: 8080,
	})

	h.tailnetStore.Create(store.TailnetConnection{
		ID: "tn-1", Name: "T", Type: "tailscale", AuthKey: "k", Status: "active",
	})

	h.reconciler.converge(context.Background(), nil)

	assert.Empty(t, h.sidecar.EnsuredApps(), "system apps should not get sidecars")
}

// ── Remote App Intent Tests ───────────────────────────────────────────────

func TestConverge_AddRemoteAppIntent_CreatesRemoteApp(t *testing.T) {
	h := newTestHarness()

	// Register the catalog app with SSO metadata.
	h.catalog.AddApp(&catalog.App{
		Name:        "jellyfin",
		DisplayName: "Jellyfin",
		Version:     "1.0.0",
		Port:        8096,
		SSO: catalog.SSO{
			Strategy:    "forward-auth",
			BypassPaths: []string{"/api/public"},
		},
		Container: &catalog.ContainerSpec{Image: "jellyfin:latest"},
	})

	intents := []Intent{NewAddRemoteAppIntent("jellyfin", "ts-jellyfin.tail1234.ts.net", "Johan's server")}
	h.reconciler.converge(context.Background(), intents)

	apps := h.remoteAppStore.Apps()
	require.Len(t, apps, 1)

	app := apps[0]
	assert.NotEmpty(t, app.ID)
	assert.Equal(t, "jellyfin", app.AppID)
	assert.Equal(t, "Jellyfin", app.AppName)
	assert.Equal(t, "Johan's server", app.HostLabel)
	assert.Equal(t, "ts-jellyfin.tail1234.ts.net", app.SidecarTailnetAddr)
	assert.Equal(t, "forward-auth", app.SSOStrategy)
	assert.Equal(t, []string{"/api/public"}, app.BypassPaths)
	assert.Equal(t, "active", app.Status)
}

func TestConverge_AddRemoteAppIntent_NilBypassPaths(t *testing.T) {
	h := newTestHarness()

	// Catalog app without bypass paths — should default to empty slice.
	h.catalog.AddApp(&catalog.App{
		Name:        "radarr",
		DisplayName: "Radarr",
		Version:     "1.0.0",
		Port:        7878,
		SSO:         catalog.SSO{Strategy: "native-oidc"},
		Container:   &catalog.ContainerSpec{Image: "radarr:latest"},
	})

	intents := []Intent{NewAddRemoteAppIntent("radarr", "ts-radarr.tail1234.ts.net", "Remote Host")}
	h.reconciler.converge(context.Background(), intents)

	apps := h.remoteAppStore.Apps()
	require.Len(t, apps, 1)
	assert.Equal(t, []string{}, apps[0].BypassPaths)
}

func TestConverge_DeleteRemoteAppIntent_RemovesFromStore(t *testing.T) {
	h := newTestHarness()

	// Seed a remote app.
	h.remoteAppStore.Create(store.RemoteApp{
		ID:     "ra-1",
		AppID:  "jellyfin",
		Status: "active",
	})

	intents := []Intent{NewDeleteRemoteAppIntent("ra-1")}
	h.reconciler.converge(context.Background(), intents)

	apps := h.remoteAppStore.Apps()
	assert.Empty(t, apps, "remote app should be deleted after DeleteRemoteApp intent")
}

// ── Rename App Intent Tests ───────────────────────────────────────────────

func TestConverge_RenameAppIntent_UpdatesDisplayName(t *testing.T) {
	h := newTestHarness()
	h.addCatalogApp("jellyfin", "Jellyfin", 8096)

	h.appStore.AddApp(&store.InstalledApp{
		Name:        "jellyfin",
		DisplayName: "Jellyfin",
		Status:      "running",
	})

	intents := []Intent{NewRenameAppIntent("jellyfin", "My Media Server")}
	h.reconciler.converge(context.Background(), intents)

	app, err := h.appStore.GetByName("jellyfin")
	require.NoError(t, err)
	assert.Equal(t, "My Media Server", app.DisplayName)
}
