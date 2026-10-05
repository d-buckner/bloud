// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/system"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- Test helpers ----

// fakeSystemOrchestrator implements orchestratorStatusCaller for testing.
type fakeSystemOrchestrator struct {
	mu     sync.Mutex
	status orchestrator.OrchestratorStatus
	phases map[string]string
}

func newFakeSystemOrchestrator() *fakeSystemOrchestrator {
	return &fakeSystemOrchestrator{phases: make(map[string]string)}
}

func (f *fakeSystemOrchestrator) Enqueue(intent orchestrator.Intent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = orchestrator.OrchestratorStatus{}
}

func (f *fakeSystemOrchestrator) Status() orchestrator.OrchestratorStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeSystemOrchestrator) NodePhases() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.phases
}

// ---- New system module helper ----

func newSystemModule(t *testing.T, opts systemModuleOpts) *systemModule {
	t.Helper()
	appStore := NewFakeAppStore()
	catalogCache := NewFakeCatalogCache()
	orch := newFakeSystemOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	return &systemModule{
		appStore:    appStore,
		catalog:     catalogCache,
		orch:        orch,
		healthCheck: opts.healthCheck,
		aiSettings:  opts.aiSettings,
		logger:      logger,
	}
}

// systemModuleOpts lets tests customize the module.
type systemModuleOpts struct {
	// healthCheck wires the system health check the health endpoint answers
	// from. Left nil, the endpoint stays a liveness echo.
	healthCheck func() error
	// aiSettings is the store behind Settings -> AI, which decides whether
	// the developer graph shows the AI Model node.
	aiSettings store.SettingsStoreInterface
}

// fakeAISettings answers Get from a fixed map, so a test can state exactly
// what Settings -> AI holds.
type fakeAISettings struct {
	values map[string]string
	err    error
}

func (f *fakeAISettings) Get(key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.values[key], nil
}

func (f *fakeAISettings) Set(key, value string) error {
	if f.values == nil {
		f.values = make(map[string]string)
	}
	f.values[key] = value
	return nil
}

var _ store.SettingsStoreInterface = (*fakeAISettings)(nil)

// aiSettingsWith renders an upstream list for the settings store.
func aiSettingsWith(upstreamsJSON string) *fakeAISettings {
	return &fakeAISettings{values: map[string]string{inference.SettingUpstreams: upstreamsJSON}}
}

// inferenceConsumerDef is the catalog entry of an app that declares the
// inference contract against the instance, the shape hermes/metadata.yaml
// carries.
func inferenceConsumerDef(catalogID string) *catalog.App {
	return &catalog.App{
		CatalogID: catalogID,
		Integrations: map[string]catalog.Integration{
			"inference": {
				Compatible: []catalog.CompatibleApp{{Source: catalog.InstanceProviderSource, Default: true}},
			},
		},
	}
}

// graphNodeByID finds one node in a decoded developer graph.
func graphNodeByID(nodes []graphNode, id string) (graphNode, bool) {
	for _, n := range nodes {
		if n.ID == id {
			return n, true
		}
	}
	return graphNode{}, false
}

// graphEdgePresent reports whether one edge is in the decoded graph.
func graphEdgePresent(edges []graphEdge, source, target string) bool {
	for _, e := range edges {
		if e.Source == source && e.Target == target {
			return true
		}
	}
	return false
}

// graphEdgeLabel returns the label of the edge between two nodes, or "" when
// no such edge is drawn. A test asserting a contract by name catches an edge
// that points the right way but lost its label.
func graphEdgeLabel(edges []graphEdge, source, target string) string {
	for _, e := range edges {
		if e.Source == source && e.Target == target {
			return e.Label
		}
	}
	return ""
}

// ---- Health tests ----

func TestSystemHTTP_Health(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp["status"])
}

// A failing check is 503 {"status":"unhealthy"}. That body is what the bootstrap
// page reads as "up and broken", as opposed to the gate's 503
// {"error":"starting"}. The reason itself stays in the log: this endpoint is
// public, and a driver error can name a path.
func TestSystemHTTP_Health_Unhealthy(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{
		healthCheck: func() error {
			return errors.New("database connection failed: /var/lib/bloud/bloud.db: no such file")
		},
	})
	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "unhealthy", resp["status"])
	assert.NotContains(t, w.Body.String(), "bloud.db", "the reason must not reach the wire")
}

// With no check wired the endpoint answers as it always did: the process is up.
func TestSystemHTTP_Health_UnwiredCheck(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	mod.healthCheck = nil
	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp["status"])
}

// ---- System status tests ----

func TestSystemHTTP_Status(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	req := httptest.NewRequest(http.MethodGet, "/system/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---- Storage tests ----

func TestSystemHTTP_Storage(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	req := httptest.NewRequest(http.MethodGet, "/system/storage", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---- Developer graph tests ----

func TestSystemHTTP_DeveloperGraph_Empty(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	req := httptest.NewRequest(http.MethodGet, "/system/developer", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSystemHTTP_DeveloperGraph_WithApps(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})

	// Add apps to the store
	appStore := mod.appStore.(*FakeAppStore)
	appStore.AddApp(&store.InstalledApp{
		CatalogID: "traefik", DisplayName: "Traefik", IsSystem: true, Status: "running",
	})
	appStore.AddApp(&store.InstalledApp{
		CatalogID: "jellyfin", DisplayName: "Jellyfin", IsSystem: false, Status: "running",
	})

	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	req := httptest.NewRequest(http.MethodGet, "/system/developer", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp developerGraph
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	// Should have traefik, jellyfin, and other graph nodes
	assert.Greater(t, len(resp.Nodes), 2)
}

// fetchDeveloperGraph runs the developer graph endpoint and decodes it.
func fetchDeveloperGraph(t *testing.T, mod *systemModule) developerGraph {
	t.Helper()
	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	req := httptest.NewRequest(http.MethodGet, "/system/developer", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp developerGraph
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	return resp
}

// installInferenceConsumer puts a hermes-shaped inference consumer into the
// store and its catalog entry into the cache the graph reads its integration
// declarations from.
func installInferenceConsumer(mod *systemModule) {
	mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID: "hermes", DisplayName: "Hermes", IsSystem: false, Status: "running",
	})
	mod.catalog.(*FakeCatalogCache).AddApp(inferenceConsumerDef("hermes"))
}

// The AI Model node is the instance's own provider, so it shows exactly when
// Settings -> AI has something to serve: an enabled upstream.
func TestSystemHTTP_DeveloperGraph_AINodeShownWhenConfigured(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{
		aiSettings: aiSettingsWith(
			`[{"id":"u1","name":"Main","baseUrl":"https://api.example.com/v1","enabled":true}]`),
	})
	installInferenceConsumer(mod)

	resp := fetchDeveloperGraph(t, mod)

	node, ok := graphNodeByID(resp.Nodes, AINodeID)
	require.True(t, ok, "an enabled upstream should put the AI Model node in the graph")
	assert.Equal(t, "AI Model", node.DisplayName)
	assert.Equal(t, "service", node.NodeType)
	assert.True(t, graphEdgePresent(resp.Edges, "hermes", AINodeID))
}

// With nothing configured the node is absent, and so is the edge that was
// headed for it: an edge naming a node that is not in the payload reaches the
// browser anyway and draws an arrow into empty space.
func TestSystemHTTP_DeveloperGraph_AINodeHiddenWhenUnconfigured(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{aiSettings: aiSettingsWith("")})
	installInferenceConsumer(mod)

	resp := fetchDeveloperGraph(t, mod)

	_, ok := graphNodeByID(resp.Nodes, AINodeID)
	assert.False(t, ok)
	assert.False(t, graphEdgePresent(resp.Edges, "hermes", AINodeID))
}

// A disabled upstream is not configured. The entry is kept so the toggle is
// reversible, but nothing is served.
func TestSystemHTTP_DeveloperGraph_AINodeHiddenWhenUpstreamDisabled(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{
		aiSettings: aiSettingsWith(
			`[{"id":"u1","name":"Main","baseUrl":"https://api.example.com/v1","enabled":false}]`),
	})
	installInferenceConsumer(mod)

	resp := fetchDeveloperGraph(t, mod)

	_, ok := graphNodeByID(resp.Nodes, AINodeID)
	assert.False(t, ok)
	assert.False(t, graphEdgePresent(resp.Edges, "hermes", AINodeID))
}

// A settings store that cannot answer reads as unconfigured. The graph is a
// display, and a read error is not a reason to invent a provider.
func TestSystemHTTP_DeveloperGraph_AINodeHiddenWhenStoreFails(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{
		aiSettings: &fakeAISettings{err: errors.New("database is closed")},
	})
	installInferenceConsumer(mod)

	resp := fetchDeveloperGraph(t, mod)

	_, ok := graphNodeByID(resp.Nodes, AINodeID)
	assert.False(t, ok)
}

// Unwired is the same as unconfigured, so a module built without the
// settings store never shows the node instead of panicking on a nil read.
func TestSystemHTTP_DeveloperGraph_AINodeHiddenWhenUnwired(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	installInferenceConsumer(mod)

	resp := fetchDeveloperGraph(t, mod)

	_, ok := graphNodeByID(resp.Nodes, AINodeID)
	assert.False(t, ok)
}

// An app that does not declare the contract gets no edge, configured or not:
// the graph renders what the catalog declares, never what a provider could
// theoretically serve.
// The graph reads the same settings key the Settings -> AI API writes, so
// wire the real SQLite store rather than a fake and drive both sides of it.
// A key-name drift between the two would otherwise read as "never configured"
// and the node would silently never appear.
func TestSystemHTTP_DeveloperGraph_AINodeRoundTripsThroughRealStore(t *testing.T) {
	db := testdb.SetupTestDB(t)
	settings := store.NewSettingsStore(db)

	mod := newSystemModule(t, systemModuleOpts{})
	mod.aiSettings = settings
	installInferenceConsumer(mod)

	_, ok := graphNodeByID(fetchDeveloperGraph(t, mod).Nodes, AINodeID)
	require.False(t, ok, "a fresh instance has no AI configured")

	encoded, err := inference.EncodeUpstreams([]inference.Upstream{
		{ID: "u1", Name: "Main", BaseURL: "https://api.example.com/v1", Enabled: true},
	})
	require.NoError(t, err)
	require.NoError(t, settings.Set(inference.SettingUpstreams, encoded))

	resp := fetchDeveloperGraph(t, mod)
	aiNode, ok := graphNodeByID(resp.Nodes, AINodeID)
	require.True(t, ok, "the settings the API writes must be the settings the graph reads")
	assert.Equal(t, "service", aiNode.NodeType)
	require.True(t, graphEdgePresent(resp.Edges, "hermes", AINodeID))

	// Turning the upstream off takes the node and its edge back out, so the
	// graph tracks the setting rather than remembering that it was once on.
	require.NoError(t, settings.Set(inference.SettingUpstreams,
		`[{"id":"u1","name":"Main","baseUrl":"https://api.example.com/v1","enabled":false}]`))

	resp = fetchDeveloperGraph(t, mod)
	assert.False(t, graphEdgePresent(resp.Edges, "hermes", AINodeID))
	_, ok = graphNodeByID(resp.Nodes, AINodeID)
	assert.False(t, ok)
}

func TestSystemHTTP_DeveloperGraph_NonConsumerGetsNoAIEdge(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{
		aiSettings: aiSettingsWith(
			`[{"id":"u1","name":"Main","baseUrl":"https://api.example.com/v1","enabled":true}]`),
	})
	mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID: "jellyfin", DisplayName: "Jellyfin", IsSystem: false, Status: "running",
	})
	mod.catalog.(*FakeCatalogCache).AddApp(&catalog.App{
		CatalogID:    "jellyfin",
		DisplayName:  "Jellyfin",
		Integrations: map[string]catalog.Integration{},
	})

	resp := fetchDeveloperGraph(t, mod)

	assert.False(t, graphEdgePresent(resp.Edges, "jellyfin", AINodeID))
}

// An optional contract that declares no `default: true` still draws its edge.
// The orchestrator binds every compatible provider of an optional contract, so
// a display that required the flag would hide wiring that exists. This is the
// shape apps/affine/metadata.yaml carries for inference.
func TestSystemHTTP_DeveloperGraph_InferenceEdgeWithoutDefault(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{
		aiSettings: aiSettingsWith(
			`[{"id":"u1","name":"Main","baseUrl":"https://api.example.com/v1","enabled":true}]`),
	})
	mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID: "affine", DisplayName: "AFFiNE", Status: "running",
	})
	mod.catalog.(*FakeCatalogCache).AddApp(&catalog.App{
		CatalogID:   "affine",
		DisplayName: "AFFiNE",
		Integrations: map[string]catalog.Integration{
			"inference": {Compatible: []catalog.CompatibleApp{{Source: catalog.InstanceProviderSource}}},
		},
	})

	resp := fetchDeveloperGraph(t, mod)

	assert.True(t, graphEdgePresent(resp.Edges, "affine", AINodeID),
		"a source:instance consumer with no declared default still wires the instance")
}

// A compatible provider that is not installed is a possibility the metadata
// allows, not a wiring that exists, so it gets no edge. Without that filter the
// browser receives an edge naming a node it was never given and draws an arrow
// into empty space.
func TestSystemHTTP_DeveloperGraph_NoEdgeToUninstalledProvider(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID: "seerr", DisplayName: "Seerr", Status: "running",
	})
	mod.catalog.(*FakeCatalogCache).AddApp(&catalog.App{
		CatalogID:   "seerr",
		DisplayName: "Seerr",
		Integrations: map[string]catalog.Integration{
			"mediaServer": {Compatible: []catalog.CompatibleApp{{App: "jellyfin", Default: true}}},
		},
	})

	resp := fetchDeveloperGraph(t, mod)

	assert.False(t, graphEdgePresent(resp.Edges, "seerr", "jellyfin"),
		"jellyfin is not installed, so nothing may point at a node absent from the payload")
}

// An optional multi-provider contract draws one edge per installed provider:
// the consumer wires all of them, so the graph shows all of them rather than
// the one a single-value recorded choice could name.
func TestSystemHTTP_DeveloperGraph_MultiProviderContractDrawsEveryInstalledProvider(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	for _, id := range []string{"prowlarr", "sonarr", "radarr"} {
		mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
			CatalogID: id, DisplayName: id, Status: "running",
		})
	}
	mod.catalog.(*FakeCatalogCache).AddApp(&catalog.App{
		CatalogID:   "prowlarr",
		DisplayName: "Prowlarr",
		Integrations: map[string]catalog.Integration{
			"pvr": {Multi: true, Compatible: []catalog.CompatibleApp{{App: "sonarr"}, {App: "radarr"}}},
		},
	})

	resp := fetchDeveloperGraph(t, mod)

	assert.True(t, graphEdgePresent(resp.Edges, "prowlarr", "sonarr"))
	assert.True(t, graphEdgePresent(resp.Edges, "prowlarr", "radarr"))
}

// A required contract draws its installed declared provider, not a provider
// a defunct choice system recorded. The record names deluge, which is not
// even installed; the wiring is the declaration intersected with the installed
// set, so only qbittorrent draws.
func TestSystemHTTP_DeveloperGraph_RequiredContractDrawsTheInstalledProvider(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID: "qbittorrent", DisplayName: "qBittorrent", Status: "running",
	})
	mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID:         "radarr",
		DisplayName:       "Radarr",
		Status:            "running",
		IntegrationConfig: map[string]string{"downloadClient": "deluge"},
	})
	mod.catalog.(*FakeCatalogCache).AddApp(&catalog.App{
		CatalogID:   "radarr",
		DisplayName: "Radarr",
		Integrations: map[string]catalog.Integration{
			"downloadClient": {
				Required:   true,
				Compatible: []catalog.CompatibleApp{{App: "qbittorrent", Default: true}},
			},
		},
	})

	resp := fetchDeveloperGraph(t, mod)

	assert.True(t, graphEdgePresent(resp.Edges, "radarr", "qbittorrent"),
		"the installed declared provider draws")
	assert.False(t, graphEdgePresent(resp.Edges, "radarr", "deluge"),
		"the stale record names a provider that is not installed; it draws no edge")
}

// An optional contract draws every installed declared provider. The value
// recorded at install time names one of them, but the set is the declaration:
// nothing is chosen, and a provider installed later is wiring that exists.
// This is the #233 shape with the catalog shrunk to a fake.
func TestSystemHTTP_DeveloperGraph_OptionalContractDrawsEveryInstalledProvider(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	for _, id := range []string{"sonarr", "radarr"} {
		mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
			CatalogID: id, DisplayName: id, Status: "running",
		})
	}
	mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID:         "prowlarr",
		DisplayName:       "Prowlarr",
		Status:            "running",
		IntegrationConfig: map[string]string{"pvr": "radarr"},
	})
	mod.catalog.(*FakeCatalogCache).AddApp(&catalog.App{
		CatalogID:   "prowlarr",
		DisplayName: "Prowlarr",
		Integrations: map[string]catalog.Integration{
			"pvr": {
				Multi:      true,
				Compatible: []catalog.CompatibleApp{{App: "sonarr"}, {App: "radarr"}},
			},
		},
	})

	resp := fetchDeveloperGraph(t, mod)

	assert.True(t, graphEdgePresent(resp.Edges, "prowlarr", "radarr"))
	assert.True(t, graphEdgePresent(resp.Edges, "prowlarr", "sonarr"),
		"every installed declared provider draws; the recorded value does not trim the set")
}

// A declared provider that is not installed is a possibility the metadata
// allows, not a wiring that exists, so it gets no edge. The set model is
// declaration intersected with the installed set, and the second half of that
// intersection must still hold.
func TestSystemHTTP_DeveloperGraph_DeclaredButUninstalledProviderDrawsNoEdge(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID:         "prowlarr",
		DisplayName:       "Prowlarr",
		Status:            "running",
		IntegrationConfig: map[string]string{"pvr": "radarr"},
	})
	mod.catalog.(*FakeCatalogCache).AddApp(&catalog.App{
		CatalogID:   "prowlarr",
		DisplayName: "Prowlarr",
		Integrations: map[string]catalog.Integration{
			"pvr": {Multi: true, Compatible: []catalog.CompatibleApp{{App: "sonarr"}, {App: "radarr"}}},
		},
	})

	resp := fetchDeveloperGraph(t, mod)

	assert.False(t, graphEdgePresent(resp.Edges, "prowlarr", "radarr"),
		"radarr is declared but not installed, so nothing may point at a node absent from the payload")
	assert.False(t, graphEdgePresent(resp.Edges, "prowlarr", "sonarr"))
}

// The real catalog through the real cache, pinning the exact wiring issue #233
// reported: Hermes' install row carries `mcp: affine-mcp` from a time before
// dav-mcp existed. The set model reads the declaration plus the installed
// set and ignores that stale record, so the graph must draw
// hermes -> dav-mcp alongside hermes -> affine-mcp.
func TestSystemHTTP_DeveloperGraph_RealCatalogWiresCalendarMcpIntoHermes(t *testing.T) {
	cache := catalog.NewMemoryCache()
	require.NoError(t, cache.Refresh(catalog.NewLoader(filepath.Join("..", "..", "..", "..", "apps"))))

	hermesDef, err := cache.Get("hermes")
	require.NoError(t, err)
	mcp := hermesDef.Integrations["mcp"]
	require.True(t, mcp.Multi, "hermes' mcp contract is expected to be multi")
	require.False(t, mcp.Required, "hermes' mcp contract is expected to be optional")
	require.Contains(t, compatibleAppNames(mcp), "dav-mcp")

	mod := newSystemModule(t, systemModuleOpts{})
	mod.catalog = cache
	for _, id := range []string{"hermes", "affine-mcp", "dav-mcp", "traefik", "authentik"} {
		mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
			CatalogID: id, DisplayName: id, Status: "running",
		})
	}
	// The stale recorded value from before dav-mcp shipped. It must be
	// ignored: the wiring is the declaration intersected with the installed set.
	require.NoError(t, mod.appStore.UpdateIntegrationConfig(
		"hermes", map[string]string{"mcp": "affine-mcp"},
	))

	resp := fetchDeveloperGraph(t, mod)

	assert.True(t, graphEdgePresent(resp.Edges, "hermes", "affine-mcp"))
	assert.True(t, graphEdgePresent(resp.Edges, "hermes", "dav-mcp"),
		"dav-mcp is installed and declared compatible, so the orchestrator wires it "+
			"and the graph must draw it regardless of the stale record")
	assert.Equal(t, "mcp", graphEdgeLabel(resp.Edges, "hermes", "dav-mcp"))
}

// compatibleAppNames lists the catalog apps an integration's compatible list
// names, so a test can assert a declaration without reaching into the struct.
func compatibleAppNames(integration catalog.Integration) []string {
	var names []string
	for _, compat := range integration.Compatible {
		if compat.App != "" {
			names = append(names, compat.App)
		}
	}
	return names
}

// The real catalog through the real cache. Production reads its integration
// declarations from the catalog cache, so a test that injects a hand-built
// definition proves nothing about whether the shipped metadata produces the
// edge. This loads apps/ and pins both sides: hermes declares its instance
// provider with `default: true`, affine without it, and both must wire.
func TestSystemHTTP_DeveloperGraph_RealCatalogWiresInferenceConsumers(t *testing.T) {
	cache := catalog.NewMemoryCache()
	require.NoError(t, cache.Refresh(catalog.NewLoader(filepath.Join("..", "..", "..", "..", "apps"))))

	mod := newSystemModule(t, systemModuleOpts{
		aiSettings: aiSettingsWith(
			`[{"id":"u1","name":"Main","baseUrl":"https://api.example.com/v1","enabled":true}]`),
	})
	mod.catalog = cache
	for _, id := range []string{"hermes", "affine", "traefik", "authentik"} {
		mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
			CatalogID: id, DisplayName: id, Status: "running",
		})
	}

	resp := fetchDeveloperGraph(t, mod)

	assert.True(t, graphEdgePresent(resp.Edges, "hermes", AINodeID))
	assert.True(t, graphEdgePresent(resp.Edges, "affine", AINodeID),
		"affine declares source: instance with no `default: true`; the edge must not depend on the flag")
}

func TestSystemHTTP_DeveloperGraph_ContainerNodes(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	appStore := mod.appStore.(*FakeAppStore)
	appStore.AddApp(&store.InstalledApp{
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
	mod.orch.(*fakeSystemOrchestrator).phases = map[string]string{
		"apps-immich-postgres": "running",
		"apps-immich-server":   "starting",
	}

	r := chi.NewRouter()
	NewSystemRouter(mod, r)
	req := httptest.NewRequest(http.MethodGet, "/system/developer", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp developerGraph
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

	containers := map[string]graphNode{}
	for _, n := range resp.Nodes {
		if n.ParentID == "immich" {
			containers[n.ID] = n
		}
	}
	require.Len(t, containers, 2)
	assert.Equal(t, "container", containers["apps-immich-server"].NodeType)
	assert.Equal(t, "server", containers["apps-immich-server"].DisplayName)
	assert.Equal(t, "starting", containers["apps-immich-server"].Status)
	assert.Equal(t, "postgres", containers["apps-immich-postgres"].DisplayName)
	assert.Equal(t, "running", containers["apps-immich-postgres"].Status)

	assert.Contains(t, resp.Edges, graphEdge{
		Source: "apps-immich-server", Target: "apps-immich-postgres",
	})
}

func TestSystemHTTP_DeveloperGraph_ContainersWithoutCatalogEntry(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID: "legacy", DisplayName: "Legacy", Status: "running",
	})

	r := chi.NewRouter()
	NewSystemRouter(mod, r)
	req := httptest.NewRequest(http.MethodGet, "/system/developer", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp developerGraph
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

	withParent := 0
	for _, n := range resp.Nodes {
		if n.ParentID != "legacy" {
			continue
		}
		withParent++
		assert.Equal(t, "legacy", n.ID)
		assert.Equal(t, "container", n.NodeType)
		assert.Equal(t, "running", n.Status)
	}
	assert.Equal(t, 1, withParent, "a container-less app still gets one node inside its box")
}

func TestContainerLabel(t *testing.T) {
	assert.Equal(t, "postgres", containerLabel("apps-immich-postgres", "immich"))
	assert.Equal(t, "traefik", containerLabel("apps-traefik", "traefik"))
	assert.Equal(t, "homeassistant", containerLabel("apps-homeassistant", "homeassistant"))
	assert.Equal(t, "custom", containerLabel("custom", "traefik"))
}

// ---- Router registration ----
func TestSystemRouter_RegistersRoutes(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	routes := []struct {
		method string
		path   string
	}{
		{"GET", "/health"},
		{"GET", "/system/status"},
		{"GET", "/system/storage"},
		{"GET", "/system/developer"},
	}

	for _, route := range routes {
		t.Run(route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			assert.NotEqual(t, http.StatusNotFound, w.Code,
				"route %s %s should not return 404", route.method, route.path)
		})
	}
}

// ---- Diagnostics ----

// fakeDNSProber is a ContainerProber that is never consulted: the tests here
// use a public URL the diagnostic skips, so they pin the handler wiring rather
// than the check itself (covered in internal/system).
type fakeDNSProber struct{}

func (fakeDNSProber) ListContainers(context.Context) ([]podman.Container, error) { return nil, nil }

func (fakeDNSProber) ExecWithEnv(context.Context, string, map[string]string, []string) ([]byte, error) {
	return nil, errors.New("unused")
}

func TestSystemHTTP_Diagnostics(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	mod.SetDNSDiagnostics(system.NewDNSDiagnostics(fakeDNSProber{}, func() string { return "http://localhost:8080" }))

	req := httptest.NewRequest(http.MethodGet, "/api/system/diagnostics", nil)
	w := httptest.NewRecorder()
	mod.DiagnosticsHandler()(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got system.DNSDiagnostic
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.True(t, got.Skipped, "localhost public URL must be skipped")
}

func TestSystemHTTP_Diagnostics_Unwired(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})

	req := httptest.NewRequest(http.MethodGet, "/api/system/diagnostics", nil)
	w := httptest.NewRecorder()
	mod.DiagnosticsHandler()(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// ---- Interface contract ----

var _ = io.EOF
var _ = chi.NewRouter

// Suppress unused
var _ = strings.NewReader
