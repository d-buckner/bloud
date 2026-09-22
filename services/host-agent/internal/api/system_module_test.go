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
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
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
		logger:       logger,
	}
}

// systemModuleOpts lets tests customize the module.
type systemModuleOpts struct {
	// healthCheck wires the system health check the health endpoint answers
	// from. Left nil, the endpoint stays a liveness echo.
	healthCheck func() error
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
