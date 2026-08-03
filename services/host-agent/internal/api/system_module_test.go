package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- Test helpers ----

// fakeSystemOrchestrator implements orchestratorStatusCaller for testing.
type fakeSystemOrchestrator struct {
	mu      sync.Mutex
	status  orchestrator.OrchestratorStatus
}

func newFakeSystemOrchestrator() *fakeSystemOrchestrator {
	return &fakeSystemOrchestrator{}
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

// FakeGateway is a fake gateway manager for testing.
type FakeGateway struct {
	running bool
	domain  string
}

func (f *FakeGateway) EnsureRunning(_ context.Context) error              { return nil }
func (f *FakeGateway) Stop(_ context.Context) error               { return nil }
func (f *FakeGateway) StopAndPurge(_ context.Context) error       { return nil }
func (f *FakeGateway) IsRunning(_ context.Context) bool                       { return f.running }
func (f *FakeGateway) GetTailnetDomain(_ context.Context) (string, error)     { return f.domain, nil }

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
		logger:       logger,
	}
}

// systemModuleOpts lets tests customize the module.
type systemModuleOpts struct{}

// FakeAppGraph is a fake catalog.AppGraphInterface for testing.
type FakeAppGraph struct {
	apps       map[string]*catalog.AppDefinition
	installed  []string
}

func (f *FakeAppGraph) PlanInstall(appName string) (*catalog.InstallPlan, error) { return nil, nil }
func (f *FakeAppGraph) PlanRemove(appName string) (*catalog.RemovePlan, error)   { return nil, nil }
func (f *FakeAppGraph) SetInstalled(installed []string)                          { f.installed = installed }
func (f *FakeAppGraph) IsInstalled(appName string) bool                         { return true }
func (f *FakeAppGraph) FindDependents(appName string) []catalog.ConfigTask      { return nil }
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
	r := NewSystemRouter(mod)

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
	r := NewSystemRouter(mod)

	req := httptest.NewRequest("GET", "/api/system/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---- Storage tests ----

func TestSystemHTTP_Storage(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	r := NewSystemRouter(mod)

	req := httptest.NewRequest("GET", "/api/system/storage", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---- Developer graph tests ----

func TestSystemHTTP_DeveloperGraph_Empty(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	r := NewSystemRouter(mod)

	req := httptest.NewRequest("GET", "/api/system/developer", nil)
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

	r := NewSystemRouter(mod)

	req := httptest.NewRequest("GET", "/api/system/developer", nil)
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

	r := NewSystemRouter(mod)

	req := httptest.NewRequest("GET", "/api/system/developer", nil)
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

// ---- Router registration ----

func TestSystemRouter_RegistersRoutes(t *testing.T) {
	mod := newSystemModule(t, systemModuleOpts{})
	r := NewSystemRouter(mod)

	routes := []struct {
		method string
		path   string
	}{
		{"GET", "/health"},
		{"GET", "/api/system/status"},
		{"GET", "/api/system/storage"},
		{"GET", "/api/system/developer"},
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

var _ SystemModule = (*systemModule)(nil)

var _ = io.EOF
var _ = chi.NewRouter

// Suppress unused
var _ = strings.NewReader
