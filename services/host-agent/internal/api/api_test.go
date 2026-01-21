package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FakeAppStore implements store.AppStoreInterface for testing
type FakeAppStore struct {
	mu       sync.RWMutex
	apps     map[string]*store.InstalledApp
	onChange func()
}

func NewFakeAppStore() *FakeAppStore {
	return &FakeAppStore{
		apps: make(map[string]*store.InstalledApp),
	}
}

func (f *FakeAppStore) GetAll() ([]*store.InstalledApp, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var apps []*store.InstalledApp
	for _, app := range f.apps {
		apps = append(apps, app)
	}
	return apps, nil
}

func (f *FakeAppStore) GetByName(name string) (*store.InstalledApp, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.apps[name], nil
}

func (f *FakeAppStore) GetInstalledNames() ([]string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var names []string
	for name := range f.apps {
		names = append(names, name)
	}
	return names, nil
}

func (f *FakeAppStore) Install(name, displayName, version string, integrationConfig map[string]string, opts *store.InstallOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	app := &store.InstalledApp{
		Name:              name,
		DisplayName:       displayName,
		Version:           version,
		Status:            "installing",
		IntegrationConfig: integrationConfig,
		InstalledAt:       time.Now(),
		UpdatedAt:         time.Now(),
	}
	if opts != nil {
		app.Port = opts.Port
		app.IsSystem = opts.IsSystem
	}
	f.apps[name] = app
	f.notify()
	return nil
}

func (f *FakeAppStore) UpdateStatus(name, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[name]; ok {
		app.Status = status
		app.UpdatedAt = time.Now()
		f.notify()
	}
	return nil
}

func (f *FakeAppStore) EnsureSystemApp(name, displayName string, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[name] = &store.InstalledApp{
		Name:        name,
		DisplayName: displayName,
		Port:        port,
		Status:      "running",
		IsSystem:    true,
		InstalledAt: time.Now(),
		UpdatedAt:   time.Now(),
	}
	f.notify()
	return nil
}

func (f *FakeAppStore) UpdateIntegrationConfig(name string, config map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[name]; ok {
		app.IntegrationConfig = config
		app.UpdatedAt = time.Now()
	}
	return nil
}

func (f *FakeAppStore) UpdateDisplayName(name, displayName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[name]; ok {
		app.DisplayName = displayName
		app.UpdatedAt = time.Now()
		f.notify()
	}
	return nil
}

func (f *FakeAppStore) Uninstall(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.apps, name)
	f.notify()
	return nil
}

func (f *FakeAppStore) IsInstalled(name string) (bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	_, ok := f.apps[name]
	return ok, nil
}

func (f *FakeAppStore) SetOnChange(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onChange = fn
}

func (f *FakeAppStore) notify() {
	if f.onChange != nil {
		f.onChange()
	}
}

// AddApp is a test helper to add an installed app
func (f *FakeAppStore) AddApp(app *store.InstalledApp) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[app.Name] = app
}

// FakeCatalogCache implements catalog.CacheInterface for testing
type FakeCatalogCache struct {
	mu   sync.RWMutex
	apps map[string]*catalog.App
}

func NewFakeCatalogCache() *FakeCatalogCache {
	return &FakeCatalogCache{
		apps: make(map[string]*catalog.App),
	}
}

func (f *FakeCatalogCache) Get(name string) (*catalog.App, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if app, ok := f.apps[name]; ok {
		return app, nil
	}
	return nil, fmt.Errorf("app not found: %s", name)
}

func (f *FakeCatalogCache) GetAll() ([]*catalog.App, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var apps []*catalog.App
	for _, app := range f.apps {
		apps = append(apps, app)
	}
	return apps, nil
}

func (f *FakeCatalogCache) GetUserApps() ([]*catalog.App, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var apps []*catalog.App
	for _, app := range f.apps {
		if !catalog.IsSystemApp(app) {
			apps = append(apps, app)
		}
	}
	return apps, nil
}

func (f *FakeCatalogCache) IsSystemAppByName(name string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if app, ok := f.apps[name]; ok {
		return catalog.IsSystemApp(app)
	}
	return false
}

func (f *FakeCatalogCache) Refresh(loader *catalog.Loader) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	apps, err := loader.LoadAll()
	if err != nil {
		return err
	}
	f.apps = apps
	return nil
}

// AddApp is a test helper to add an app to the cache
func (f *FakeCatalogCache) AddApp(app *catalog.App) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[app.Name] = app
}

// setupTestServer creates a test server with fake stores and test catalog
func setupTestServer(t *testing.T) (*Server, string) {
	tmpDir := t.TempDir()

	// Create test app directory with metadata.yaml
	testAppDir := filepath.Join(tmpDir, "test-app")
	require.NoError(t, os.MkdirAll(testAppDir, 0755))

	testAppYAML := `name: test-app
displayName: Test App
description: A test application
category: testing
version: 1.0.0
dependencies: []
resources:
  minRam: 128
  minDisk: 1
  gpu: false
sso:
  enabled: false
  protocol: ""
  blueprint: ""
defaultConfig: {}
healthCheck:
  path: /health
  interval: 30
  timeout: 5
docs:
  homepage: https://example.com
  source: https://github.com/example/test-app
tags:
  - test
`
	require.NoError(t, os.WriteFile(filepath.Join(testAppDir, "metadata.yaml"), []byte(testAppYAML), 0644))

	// Create fake stores
	appStore := NewFakeAppStore()
	catalogCache := NewFakeCatalogCache()

	// Load catalog from test files
	loader := catalog.NewLoader(tmpDir)
	require.NoError(t, catalogCache.Refresh(loader))

	// Create logger that doesn't output during tests
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	// Create app event hub
	appHub := NewAppEventHub(appStore)
	appStore.SetOnChange(appHub.Broadcast)

	// Load graph for integration planning
	graph, err := loader.LoadGraph()
	require.NoError(t, err)

	// Sync installed state
	names, _ := appStore.GetInstalledNames()
	graph.SetInstalled(names)

	// Create server with fakes
	server := &Server{
		router:       chi.NewRouter(),
		catalog:      catalogCache,
		graph:        graph,
		appStore:     appStore,
		appHub:       appHub,
		appsDir:      tmpDir,
		nixConfigDir: filepath.Join(tmpDir, "nix"),
		dataDir:      tmpDir,
		port:         8080,
		logger:       logger,
	}

	server.setupMiddleware()
	server.setupRoutes()

	return server, tmpDir
}

// setupTestServerWithGraph creates a server with apps that have integrations
func setupTestServerWithGraph(t *testing.T) *Server {
	tmpDir := t.TempDir()

	// Create apps with integrations (each in its own directory)
	apps := map[string]string{
		"qbittorrent": `name: qbittorrent
displayName: qBittorrent
description: Torrent download client
category: downloads
image: qbittorrent:latest
integrations: {}
`,
		"radarr": `name: radarr
displayName: Radarr
description: Movie collection manager
category: media
image: radarr:latest
integrations:
  downloadClient:
    required: true
    multi: false
    compatible:
      - app: qbittorrent
        default: true
`,
		"jellyseerr": `name: jellyseerr
displayName: Jellyseerr
description: Request management and media discovery tool
category: media
image: jellyseerr:latest
integrations:
  pvr:
    required: true
    multi: true
    compatible:
      - app: radarr
        category: movies
`,
	}

	for appName, content := range apps {
		appDir := filepath.Join(tmpDir, appName)
		require.NoError(t, os.MkdirAll(appDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(appDir, "metadata.yaml"), []byte(content), 0644))
	}

	// Create fake stores
	appStore := NewFakeAppStore()
	catalogCache := NewFakeCatalogCache()

	// Load catalog from test files
	loader := catalog.NewLoader(tmpDir)
	require.NoError(t, catalogCache.Refresh(loader))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create app event hub
	appHub := NewAppEventHub(appStore)
	appStore.SetOnChange(appHub.Broadcast)

	// Load graph
	graph, err := loader.LoadGraph()
	require.NoError(t, err)

	// Create server
	server := &Server{
		router:       chi.NewRouter(),
		catalog:      catalogCache,
		graph:        graph,
		appStore:     appStore,
		appHub:       appHub,
		appsDir:      tmpDir,
		nixConfigDir: filepath.Join(tmpDir, "nix"),
		dataDir:      tmpDir,
		port:         8080,
		logger:       logger,
	}

	server.setupMiddleware()
	server.setupRoutes()

	return server
}

func TestAPI_Health(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)

	assert.Equal(t, "ok", response["status"])
}

func TestAPI_ListApps(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/apps", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)

	apps, ok := response["apps"].([]interface{})
	require.True(t, ok, "response should contain apps array")
	require.Len(t, apps, 1, "should have exactly 1 app")

	// Check first app
	app := apps[0].(map[string]interface{})
	assert.Equal(t, "test-app", app["name"])
	assert.Equal(t, "Test App", app["displayName"])
}

func TestAPI_ListInstalledApps_Empty(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/apps/installed", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var apps []interface{}
	err := json.NewDecoder(w.Body).Decode(&apps)
	require.NoError(t, err)

	assert.Empty(t, apps, "should have 0 installed apps")
}

func TestAPI_SystemStatus(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/system/status", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var stats map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&stats)
	require.NoError(t, err)

	// Check that required fields exist
	assert.Contains(t, stats, "cpu", "response should contain cpu field")
	assert.Contains(t, stats, "memory", "response should contain memory field")
	assert.Contains(t, stats, "disk", "response should contain disk field")
}

func TestAPI_Storage(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/system/storage", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var storage map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&storage)
	require.NoError(t, err)

	// Check that required fields exist
	assert.Contains(t, storage, "used", "response should contain used field")
	assert.Contains(t, storage, "total", "response should contain total field")
	assert.Contains(t, storage, "free", "response should contain free field")
	assert.Contains(t, storage, "percentage", "response should contain percentage field")
	assert.Contains(t, storage, "path", "response should contain path field")

	// Verify values are sensible
	used := storage["used"].(float64)
	total := storage["total"].(float64)
	free := storage["free"].(float64)

	assert.Greater(t, total, float64(0), "total should be greater than 0")
	assert.GreaterOrEqual(t, free, float64(0), "free should be >= 0")
	assert.GreaterOrEqual(t, used, float64(0), "used should be >= 0")
	assert.Equal(t, "/", storage["path"], "path should be root")
}

func TestAPI_RefreshCatalog(t *testing.T) {
	server, appsDir := setupTestServer(t)

	// Add another app to catalog
	newAppDir := filepath.Join(appsDir, "new-app")
	require.NoError(t, os.MkdirAll(newAppDir, 0755))

	newAppYAML := `name: new-app
displayName: New App
description: A newly added app
category: testing
version: 2.0.0
dependencies: []
resources:
  minRam: 256
  minDisk: 2
  gpu: false
sso:
  enabled: false
  protocol: ""
  blueprint: ""
defaultConfig: {}
healthCheck:
  path: /
  interval: 30
  timeout: 5
docs:
  homepage: https://example.com
  source: https://github.com/example/new-app
tags:
  - new
`
	newAppFile := filepath.Join(newAppDir, "metadata.yaml")
	err := os.WriteFile(newAppFile, []byte(newAppYAML), 0644)
	require.NoError(t, err, "should be able to write new app file")

	// Refresh catalog
	req := httptest.NewRequest("POST", "/api/apps/refresh-catalog", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify new app is in catalog
	req = httptest.NewRequest("GET", "/api/apps", nil)
	w = httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	var response map[string]interface{}
	err = json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)

	apps, ok := response["apps"].([]interface{})
	require.True(t, ok, "response should contain apps array")
	assert.Len(t, apps, 2, "should have 2 apps after refresh")
}

func TestAPI_PlanInstall(t *testing.T) {
	server := setupTestServerWithGraph(t)

	req := httptest.NewRequest("GET", "/api/apps/radarr/plan-install", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var plan map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&plan)
	require.NoError(t, err)

	assert.Equal(t, "radarr", plan["app"])
	assert.Equal(t, true, plan["canInstall"])

	// Should have a choice for downloadClient (nothing installed yet)
	choices := plan["choices"].([]interface{})
	assert.Len(t, choices, 1)

	choice := choices[0].(map[string]interface{})
	assert.Equal(t, "downloadClient", choice["integration"])
	assert.Equal(t, "qbittorrent", choice["recommended"])
}

func TestAPI_PlanInstall_WithDependencyInstalled(t *testing.T) {
	server := setupTestServerWithGraph(t)

	// Mark qbittorrent as installed using the fake store
	fakeStore := server.appStore.(*FakeAppStore)
	fakeStore.AddApp(&store.InstalledApp{
		Name:        "qbittorrent",
		DisplayName: "qBittorrent",
		Status:      "running",
	})

	// Refresh to sync installed state
	server.syncInstalledState()

	req := httptest.NewRequest("GET", "/api/apps/radarr/plan-install", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var plan map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&plan)
	require.NoError(t, err)

	// Should have no choices (qbittorrent auto-selected)
	choices := plan["choices"].([]interface{})
	assert.Len(t, choices, 0)

	// Should have auto-config for qbittorrent
	autoConfig := plan["autoConfig"].([]interface{})
	assert.Len(t, autoConfig, 1)
	assert.Equal(t, "qbittorrent", autoConfig[0].(map[string]interface{})["source"])
}

func TestAPI_PlanRemove_Blocked(t *testing.T) {
	server := setupTestServerWithGraph(t)

	// Install qbittorrent and radarr using the fake store
	fakeStore := server.appStore.(*FakeAppStore)
	fakeStore.AddApp(&store.InstalledApp{
		Name:        "qbittorrent",
		DisplayName: "qBittorrent",
		Status:      "running",
	})
	fakeStore.AddApp(&store.InstalledApp{
		Name:        "radarr",
		DisplayName: "Radarr",
		Status:      "running",
	})

	server.syncInstalledState()

	// Try to remove qbittorrent (radarr depends on it)
	req := httptest.NewRequest("GET", "/api/apps/qbittorrent/plan-remove", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var plan map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&plan)
	require.NoError(t, err)

	assert.Equal(t, false, plan["canRemove"])
	blockers := plan["blockers"].([]interface{})
	assert.Len(t, blockers, 1)
}

func TestAPI_PlanInstall_NotFound(t *testing.T) {
	server := setupTestServerWithGraph(t)

	req := httptest.NewRequest("GET", "/api/apps/nonexistent/plan-install", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPI_PlanRemove_Allowed(t *testing.T) {
	server := setupTestServerWithGraph(t)

	// Install only radarr (no dependents)
	fakeStore := server.appStore.(*FakeAppStore)
	fakeStore.AddApp(&store.InstalledApp{
		Name:        "radarr",
		DisplayName: "Radarr",
		Status:      "running",
	})

	server.syncInstalledState()

	// Try to remove radarr (no dependents, should be allowed)
	req := httptest.NewRequest("GET", "/api/apps/radarr/plan-remove", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var plan map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&plan)
	require.NoError(t, err)

	assert.Equal(t, true, plan["canRemove"])
}

func TestAPI_PlanRemove_NotFound(t *testing.T) {
	server := setupTestServerWithGraph(t)

	req := httptest.NewRequest("GET", "/api/apps/nonexistent/plan-remove", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPI_AppMetadata(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/apps/test-app/metadata", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var app map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&app)
	require.NoError(t, err)

	assert.Equal(t, "test-app", app["name"])
	assert.Equal(t, "Test App", app["displayName"])
	assert.Equal(t, "A test application", app["description"])
}

func TestAPI_AppMetadata_NotFound(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/apps/nonexistent/metadata", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPI_AppIcon(t *testing.T) {
	server, appsDir := setupTestServer(t)

	// Create an icon file
	iconPath := filepath.Join(appsDir, "test-app", "icon.png")
	iconData := []byte{0x89, 0x50, 0x4E, 0x47} // PNG magic bytes
	require.NoError(t, os.WriteFile(iconPath, iconData, 0644))

	req := httptest.NewRequest("GET", "/api/apps/test-app/icon", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Cache-Control"), "max-age")
}

func TestAPI_AppIcon_NotFound(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/apps/test-app/icon", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPI_Install_NoOrchestrator(t *testing.T) {
	server, _ := setupTestServer(t)
	server.orchestrator = nil // Ensure no orchestrator

	req := httptest.NewRequest("POST", "/api/apps/test-app/install", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAPI_Uninstall_NoOrchestrator(t *testing.T) {
	server, _ := setupTestServer(t)
	server.orchestrator = nil

	req := httptest.NewRequest("POST", "/api/apps/test-app/uninstall", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAPI_Rollback_NoNixOrchestrator(t *testing.T) {
	server, _ := setupTestServer(t)
	server.orchestrator = nil

	req := httptest.NewRequest("POST", "/api/system/rollback", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAPI_ClearData_NotFound(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("POST", "/api/apps/nonexistent/clear-data", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPI_ClearData_OrphanedData(t *testing.T) {
	server, appsDir := setupTestServer(t)
	server.orchestrator = nil // No orchestrator needed for orphaned data cleanup

	// Create orphaned data directory
	dataDir := filepath.Join(appsDir, "data")
	appDataDir := filepath.Join(dataDir, "test-app")
	require.NoError(t, os.MkdirAll(appDataDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(appDataDir, "data.txt"), []byte("test"), 0644))

	server.dataDir = dataDir

	req := httptest.NewRequest("POST", "/api/apps/test-app/clear-data", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.Equal(t, "data cleared", response["status"])
}

// AppEventHub tests

func TestAppEventHub_Subscribe(t *testing.T) {
	server, _ := setupTestServer(t)

	ch := server.appHub.Subscribe()
	assert.NotNil(t, ch)
	assert.Equal(t, 1, server.appHub.SubscriberCount())

	server.appHub.Unsubscribe(ch)
	assert.Equal(t, 0, server.appHub.SubscriberCount())
}

func TestAppEventHub_MultipleSubscribers(t *testing.T) {
	server, _ := setupTestServer(t)

	ch1 := server.appHub.Subscribe()
	ch2 := server.appHub.Subscribe()
	ch3 := server.appHub.Subscribe()

	assert.Equal(t, 3, server.appHub.SubscriberCount())

	server.appHub.Unsubscribe(ch1)
	assert.Equal(t, 2, server.appHub.SubscriberCount())

	server.appHub.Unsubscribe(ch2)
	server.appHub.Unsubscribe(ch3)
	assert.Equal(t, 0, server.appHub.SubscriberCount())
}

func TestAppEventHub_Broadcast(t *testing.T) {
	server, _ := setupTestServer(t)

	// Add an app using the fake store
	fakeStore := server.appStore.(*FakeAppStore)
	fakeStore.AddApp(&store.InstalledApp{
		Name:        "broadcast-app",
		DisplayName: "Broadcast App",
		Version:     "1.0.0",
		Status:      "running",
	})

	ch := server.appHub.Subscribe()
	defer server.appHub.Unsubscribe(ch)

	// Broadcast
	server.appHub.Broadcast()

	// Should receive the app list
	select {
	case apps := <-ch:
		assert.Len(t, apps, 1)
		assert.Equal(t, "broadcast-app", apps[0].Name)
	default:
		t.Fatal("expected to receive broadcast")
	}
}

func TestAppEventHub_BroadcastChannelFull(t *testing.T) {
	server, _ := setupTestServer(t)

	ch := server.appHub.Subscribe()
	defer server.appHub.Unsubscribe(ch)

	// Fill the channel (buffer size is 10)
	for i := 0; i < 15; i++ {
		server.appHub.Broadcast()
	}

	// Should not panic - channel full is handled gracefully
	assert.Equal(t, 1, server.appHub.SubscriberCount())
}

// Utility function tests

func TestRespondJSON(t *testing.T) {
	w := httptest.NewRecorder()

	respondJSON(w, http.StatusCreated, map[string]string{"key": "value"})

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.Equal(t, "value", response["key"])
}

func TestRespondError(t *testing.T) {
	w := httptest.NewRecorder()

	respondError(w, http.StatusBadRequest, "something went wrong")

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.Equal(t, "something went wrong", response["error"])
}

// FakeSessionStore implements a simple in-memory session store for testing
type FakeSessionStore struct {
	sessions map[string]*store.Session
	mu       sync.RWMutex
}

func NewFakeSessionStore() *FakeSessionStore {
	return &FakeSessionStore{
		sessions: make(map[string]*store.Session),
	}
}

func (f *FakeSessionStore) Create(userID string, username string) *store.Session {
	f.mu.Lock()
	defer f.mu.Unlock()

	session := &store.Session{
		ID:        fmt.Sprintf("test-session-%d", len(f.sessions)+1),
		UserID:    userID,
		Username:  username,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	f.sessions[session.ID] = session
	return session
}

func (f *FakeSessionStore) Get(sessionID string) *store.Session {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.sessions[sessionID]
}

func (f *FakeSessionStore) Delete(sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, sessionID)
}

// setupTestServerWithAuth creates a test server with auth enabled
func setupTestServerWithAuth(t *testing.T) (*Server, *FakeSessionStore) {
	server, _ := setupTestServer(t)

	// Create a fake session store wrapper that mimics the real one
	fakeStore := NewFakeSessionStore()

	// Create a real session store adapter that uses the fake
	server.sessionStore = &store.SessionStore{}

	// Re-setup routes to include auth middleware
	server.router = chi.NewRouter()
	server.setupMiddleware()
	server.setupRoutes()

	return server, fakeStore
}

// Auth Middleware Tests

func TestAuthMiddleware_NoSession(t *testing.T) {
	server, _ := setupTestServer(t)

	// Create a mock session store to enable auth
	server.sessionStore = &store.SessionStore{}

	// Re-setup routes to include auth middleware
	server.router = chi.NewRouter()
	server.setupMiddleware()
	server.setupRoutes()

	// Request a protected endpoint without session cookie
	req := httptest.NewRequest("GET", "/api/apps", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.Equal(t, "Not authenticated", response["error"])
}

// Note: TestAuthMiddleware_InvalidSession requires a real Redis connection
// and is tested via integration tests rather than unit tests.

func TestAPI_PublicEndpoints_NoAuthRequired(t *testing.T) {
	server, _ := setupTestServer(t)

	// Enable auth by setting a session store
	server.sessionStore = &store.SessionStore{}

	// Re-setup routes
	server.router = chi.NewRouter()
	server.setupMiddleware()
	server.setupRoutes()

	// Test public endpoints without session
	publicEndpoints := []struct {
		method string
		path   string
	}{
		{"GET", "/api/health"},
		{"GET", "/api/setup/status"},
		{"GET", "/api/auth/me"},
	}

	for _, ep := range publicEndpoints {
		t.Run(ep.path, func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, nil)
			w := httptest.NewRecorder()

			server.router.ServeHTTP(w, req)

			// Should not return 401 (may return other codes, but not Unauthorized)
			// Health returns 200, setup/status returns 200/500, auth/me returns 401 (expected for unauthenticated)
			if ep.path == "/api/auth/me" {
				assert.Equal(t, http.StatusUnauthorized, w.Code, "auth/me should return 401 when unauthenticated")
			} else {
				assert.NotEqual(t, http.StatusUnauthorized, w.Code, "public endpoint %s should not require auth", ep.path)
			}
		})
	}
}

func TestAPI_GetCurrentUser_Unauthenticated(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.Equal(t, "Not authenticated", response["error"])
}

func TestAPI_Login_NoAuthConfig(t *testing.T) {
	server, _ := setupTestServer(t)

	// No auth config set
	req := httptest.NewRequest("GET", "/auth/login", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAPI_Callback_NoAuthConfig(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/auth/callback?code=test", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAPI_Logout_ClearsCookie(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("POST", "/auth/logout", nil)
	req.AddCookie(&http.Cookie{
		Name:  "bloud_session",
		Value: "some-session-id",
	})
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	// Should redirect to home
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/", w.Header().Get("Location"))

	// Check that session cookie is cleared
	cookies := w.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "bloud_session" {
			sessionCookie = c
			break
		}
	}
	require.NotNil(t, sessionCookie, "session cookie should be set")
	assert.Equal(t, "", sessionCookie.Value, "session cookie value should be empty")
	assert.True(t, sessionCookie.MaxAge < 0, "session cookie should have negative MaxAge to delete it")
}

// Test getUserFromContext
func TestGetUserFromContext(t *testing.T) {
	t.Run("no user in context", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		user := getUserFromContext(req.Context())
		assert.Nil(t, user)
	})

	t.Run("user in context", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		expectedUser := &store.User{
			ID:       "550e8400-e29b-41d4-a716-446655440000",
			Username: "testuser",
		}
		ctx := req.Context()
		ctx = context.WithValue(ctx, userContextKey, expectedUser)
		req = req.WithContext(ctx)

		user := getUserFromContext(req.Context())
		require.NotNil(t, user)
		assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", user.ID)
		assert.Equal(t, "testuser", user.Username)
	})
}

// Test generateState
func TestGenerateState(t *testing.T) {
	state1, err := generateState()
	require.NoError(t, err)
	assert.Len(t, state1, 32) // 16 bytes = 32 hex chars

	state2, err := generateState()
	require.NoError(t, err)
	assert.Len(t, state2, 32)

	// Should be unique
	assert.NotEqual(t, state1, state2)
}
