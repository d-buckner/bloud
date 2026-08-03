package api

import (
	"context"
	"encoding/json"
	"fmt"
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
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- Test helpers ----

// addAppToCache is a test helper to add an app to the existing FakeCatalogCache
func addAppToCache(cache *FakeCatalogCache, app *catalog.App) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.apps[app.CatalogID] = app
}

// FakeOrchestrator collects enqueued intents for testing
type FakeOrchestrator struct {
	mu      sync.Mutex
	intents []orchestrator.Intent
	readyCh chan struct{}
}

func newFakeOrchestrator() *FakeOrchestrator {
	return &FakeOrchestrator{readyCh: make(chan struct{})}
}

func (f *FakeOrchestrator) Enqueue(intent orchestrator.Intent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.intents = append(f.intents, intent)
}

func (f *FakeOrchestrator) Start(ctx context.Context) {
	close(f.readyCh)
}

func (f *FakeOrchestrator) Ready() <-chan struct{} {
	return f.readyCh
}

func (f *FakeOrchestrator) Stop() {}

func (f *FakeOrchestrator) Status() orchestrator.OrchestratorStatus {
	return orchestrator.OrchestratorStatus{}
}

func (f *FakeOrchestrator) LastIntent() orchestrator.Intent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.intents) == 0 {
		return nil
	}
	return f.intents[len(f.intents)-1]
}

func (f *FakeOrchestrator) IntentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.intents)
}

// ---- AppsModule unit tests ----

func TestAppsModule_Install_EnqueuesIntent(t *testing.T) {
	cache := NewFakeCatalogCache()
	addAppToCache(cache, &catalog.App{CatalogID: "jellyfin", DisplayName: "Jellyfin"})
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)

	ref, err := mod.Install("jellyfin")
	require.NoError(t, err)
	assert.NotEmpty(t, ref.ID)
	assert.Equal(t, 1, orch.IntentCount())

	intent, ok := orch.LastIntent().(orchestrator.InstallAppIntent)
	assert.True(t, ok, "expected InstallAppIntent")
	assert.Equal(t, "jellyfin", intent.AppName)
}

func TestAppsModule_Install_NotFound(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)

	_, err := mod.Install("nonexistent")
	assert.Error(t, err)
	assert.Equal(t, 0, orch.IntentCount())
}

func TestAppsModule_Uninstall_DefaultsClearDataFalse(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)

	ref, err := mod.Uninstall("jellyfin", false)
	require.NoError(t, err)

	intent, ok := orch.LastIntent().(orchestrator.UninstallAppIntent)
	assert.True(t, ok)
	assert.Equal(t, "jellyfin", intent.AppName)
	assert.False(t, intent.ClearData)
	assert.NotEmpty(t, ref.ID)
}

func TestAppsModule_Uninstall_ClearData(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)

	ref, err := mod.Uninstall("jellyfin", true)
	require.NoError(t, err)

	intent, ok := orch.LastIntent().(orchestrator.UninstallAppIntent)
	assert.True(t, ok)
	assert.True(t, intent.ClearData)
	assert.NotEmpty(t, ref.ID)
}

func TestAppsModule_Rename_EnqueuesIntent(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)

	ref, err := mod.Rename("jellyfin", "My Jellyfin")
	require.NoError(t, err)

	intent, ok := orch.LastIntent().(orchestrator.RenameAppIntent)
	assert.True(t, ok)
	assert.Equal(t, "jellyfin", intent.AppName)
	assert.Equal(t, "My Jellyfin", intent.DisplayName)
	assert.NotEmpty(t, ref.ID)
}

func TestAppsModule_GetCatalog(t *testing.T) {
	cache := NewFakeCatalogCache()
	addAppToCache(cache, &catalog.App{CatalogID: "jellyfin", DisplayName: "Jellyfin"})
	addAppToCache(cache, &catalog.App{CatalogID: "navidrome", DisplayName: "Navidrome"})
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)

	apps, err := mod.GetCatalog()
	require.NoError(t, err)
	assert.Len(t, apps, 2)
}

func TestAppsModule_GetInstalled_ExcludesSystem(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	appStore.AddApp(&store.InstalledApp{CatalogID: "traefik", DisplayName: "Traefik", IsSystem: true})
	appStore.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", IsSystem: false})
	appStore.AddApp(&store.InstalledApp{CatalogID: "navidrome", DisplayName: "Navidrome", IsSystem: false})

	mod := NewAppsModule(cache, appStore, orch, logger)

	installed, err := mod.GetInstalled()
	require.NoError(t, err)
	assert.Len(t, installed, 2)
	for _, app := range installed {
		assert.False(t, app.IsSystem, "should not include system apps")
	}
}

func TestAppsModule_GetInstalled_Empty(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)

	installed, err := mod.GetInstalled()
	require.NoError(t, err)
	assert.Len(t, installed, 0)
}

func TestAppsModule_AppMetadata(t *testing.T) {
	cache := NewFakeCatalogCache()
	addAppToCache(cache, &catalog.App{CatalogID: "jellyfin", DisplayName: "Jellyfin", Description: "A media server"})
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)

	app, err := mod.AppMetadata("jellyfin")
	require.NoError(t, err)
	assert.Equal(t, "jellyfin", app.CatalogID)
	assert.Equal(t, "Jellyfin", app.DisplayName)
}

func TestAppsModule_AppMetadata_NotFound(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)

	_, err := mod.AppMetadata("nonexistent")
	assert.Error(t, err)
}

// ---- HTTP handler tests ----

func TestAppsHTTP_ListInstalledApps(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	appStore.AddApp(&store.InstalledApp{
		CatalogID: "jellyfin", DisplayName: "Jellyfin", Status: "running", IsSystem: false,
	})

	mod := NewAppsModule(cache, appStore, orch, logger)
	appMod := mod.(*appsModule)
	r := NewAppsRouter(appMod)

	req := httptest.NewRequest("GET", "/api/apps/installed", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	apps, ok := resp["apps"].([]interface{})
	require.True(t, ok)
	assert.Len(t, apps, 1)
}

func TestAppsHTTP_Install_Returns202(t *testing.T) {
	cache := NewFakeCatalogCache()
	addAppToCache(cache, &catalog.App{CatalogID: "jellyfin", DisplayName: "Jellyfin"})
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)
	appMod := mod.(*appsModule)
	r := NewAppsRouter(appMod)

	req := httptest.NewRequest("POST", "/api/apps/jellyfin/install", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.NotEmpty(t, resp["intentId"])
}

func TestAppsHTTP_Install_NotFound(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)
	appMod := mod.(*appsModule)
	r := NewAppsRouter(appMod)

	req := httptest.NewRequest("POST", "/api/apps/nonexistent/install", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAppsHTTP_Uninstall_Returns202(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)
	appMod := mod.(*appsModule)
	r := NewAppsRouter(appMod)

	req := httptest.NewRequest("POST", "/api/apps/jellyfin/uninstall", strings.NewReader(`{"clearData":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestAppsHTTP_Rename_Returns202(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)
	appMod := mod.(*appsModule)
	r := NewAppsRouter(appMod)

	body := `{"displayName":"My Jellyfin"}`
	req := httptest.NewRequest("PATCH", "/api/apps/jellyfin/rename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestAppsHTTP_Rename_MissingDisplayName(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)
	appMod := mod.(*appsModule)
	r := NewAppsRouter(appMod)

	body := `{"displayName":""}`
	req := httptest.NewRequest("PATCH", "/api/apps/jellyfin/rename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAppsHTTP_AppMetadata(t *testing.T) {
	cache := NewFakeCatalogCache()
	addAppToCache(cache, &catalog.App{CatalogID: "jellyfin", DisplayName: "Jellyfin"})
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)
	appMod := mod.(*appsModule)
	r := NewAppsRouter(appMod)

	req := httptest.NewRequest("GET", "/api/apps/jellyfin/metadata", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp catalog.App
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "jellyfin", resp.CatalogID)
}

func TestAppsHTTP_RefreshCatalog(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	tmpDir := t.TempDir()
	mod := NewAppsModule(cache, appStore, orch, logger)
	appMod := mod.(*appsModule)
	appMod.SetAppsDir(tmpDir)
	r := NewAppsRouter(appMod)

	req := httptest.NewRequest("POST", "/api/apps/refresh-catalog", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// Ensure the interface contract
var _ AppsModule = (*appsModule)(nil)

// Suppress unused import
var _ = io.EOF

// catalog error helper for test assertions
var _ = fmt.Errorf
