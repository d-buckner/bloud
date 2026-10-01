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
	"strings"
	"sync"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
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

// FakeGateway is a fake gateway manager for testing.
type FakeGateway struct {
	running bool
	domain  string
}

func (f *FakeGateway) EnsureRunning(_ context.Context) error              { return nil }
func (f *FakeGateway) Stop(_ context.Context) error                       { return nil }
func (f *FakeGateway) StopAndPurge(_ context.Context) error               { return nil }
func (f *FakeGateway) IsRunning(_ context.Context) bool                   { return f.running }
func (f *FakeGateway) GetTailnetDomain(_ context.Context) (string, error) { return f.domain, nil }

var _ sharing.GatewayManagerInterface = (*FakeGateway)(nil)

// ---- New system module helper ----

func newSystemModule(t *testing.T, opts systemModuleOpts) *systemModule {
	t.Helper()
	appStore := NewFakeAppStore()
	catalogCache := NewFakeCatalogCache()
	appGraph := &FakeAppGraph{}
	gateway := &FakeGateway{running: true, domain: "bloud.ts.net"}
	tailnetStore := &FakeTailnetStore{}
	orch := newFakeSystemOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	return &systemModule{
		appStore:     appStore,
		catalog:      catalogCache,
		graph:        appGraph,
		gateway:      gateway,
		tailnetStore: tailnetStore,
		orch:         orch,
		healthCheck:  opts.healthCheck,
		aiSettings:   opts.aiSettings,
		logger:       logger,
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
func inferenceConsumerDef() *catalog.AppDefinition {
	return &catalog.AppDefinition{
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

// FakeAppGraph is a fake catalog.AppGraphInterface for testing.
type FakeAppGraph struct {
	apps      map[string]*catalog.AppDefinition
	installed []string
}

func (f *FakeAppGraph) PlanInstall(appName string) (*catalog.InstallPlan, error) { return nil, nil }
func (f *FakeAppGraph) PlanRemove(appName string) (*catalog.RemovePlan, error)   { return nil, nil }
func (f *FakeAppGraph) SetInstalled(installed []string)                          { f.installed = installed }
func (f *FakeAppGraph) IsInstalled(appName string) bool                          { return true }
func (f *FakeAppGraph) FindDependents(appName string) []catalog.ConfigTask       { return nil }
func (f *FakeAppGraph) GetCompatibleApps(appName string, integrationName string) (installed []catalog.CompatibleApp, available []catalog.CompatibleApp) {
	return nil, nil
}
func (f *FakeAppGraph) GetApps() map[string]*catalog.AppDefinition {
	if f.apps != nil {
		return f.apps
	}
	return make(map[string]*catalog.AppDefinition)
}

// ---- Health tests ----

func TestSystemHTTP_Health(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	req := httptest.NewRequest("GET", "/health", nil)
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

	req := httptest.NewRequest("GET", "/health", nil)
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

	req := httptest.NewRequest("GET", "/health", nil)
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

	req := httptest.NewRequest("GET", "/system/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---- Storage tests ----

func TestSystemHTTP_Storage(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	req := httptest.NewRequest("GET", "/system/storage", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---- Developer graph tests ----

func TestSystemHTTP_DeveloperGraph_Empty(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	req := httptest.NewRequest("GET", "/system/developer", nil)
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

	// Add a tailnet connection
	tailnetStore := mod.tailnetStore.(*FakeTailnetStore)
	tailnetStore.active = &store.TailnetConnection{
		ID: "ts-1", Name: "My Tailscale", Type: "tailscale", Status: "active",
	}

	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	req := httptest.NewRequest("GET", "/system/developer", nil)
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

	req := httptest.NewRequest("GET", "/system/developer", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp developerGraph
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	return resp
}

// installInferenceConsumer puts a hermes-shaped inference consumer into the
// store and its catalog definition into the graph.
func installInferenceConsumer(mod *systemModule) {
	mod.appStore.(*FakeAppStore).AddApp(&store.InstalledApp{
		CatalogID: "hermes", DisplayName: "Hermes", IsSystem: false, Status: "running",
	})
	appGraph := mod.graph.(*FakeAppGraph)
	appGraph.apps = map[string]*catalog.AppDefinition{"hermes": inferenceConsumerDef()}
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
	mod.graph.(*FakeAppGraph).apps = map[string]*catalog.AppDefinition{
		"jellyfin": {Integrations: map[string]catalog.Integration{}},
	}

	resp := fetchDeveloperGraph(t, mod)

	assert.False(t, graphEdgePresent(resp.Edges, "jellyfin", AINodeID))
}

func TestSystemHTTP_DeveloperGraph_WithTailnetNodes(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})

	// Add traefik + a shared app with tailnet ID
	appStore := mod.appStore.(*FakeAppStore)
	appStore.AddApp(&store.InstalledApp{
		CatalogID: "traefik", DisplayName: "Traefik", IsSystem: true, Status: "running",
	})
	appStore.AddApp(&store.InstalledApp{
		CatalogID: "jellyfin", DisplayName: "Jellyfin", IsSystem: false, Status: "running",
		TailnetID: "tn-1",
	})

	r := chi.NewRouter()
	NewSystemRouter(mod, r)

	req := httptest.NewRequest("GET", "/system/developer", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp developerGraph
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)

	// Should have a ts:jellyfin tunnel node
	hasTSNode := false
	for _, n := range resp.Nodes {
		if n.ID == "ts:jellyfin" {
			hasTSNode = true
			break
		}
	}
	assert.True(t, hasTSNode, "should have ts:jellyfin tunnel node")
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
	req := httptest.NewRequest("GET", "/system/developer", nil)
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
	req := httptest.NewRequest("GET", "/system/developer", nil)
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

// ---- Interface contract ----

var _ = io.EOF
var _ = chi.NewRouter

// Suppress unused
var _ = strings.NewReader
