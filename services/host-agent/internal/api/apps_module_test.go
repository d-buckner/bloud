// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/go-chi/chi/v5"
)

// ---- Test helpers ----

// addAppToCache is a test helper to add an app to the existing FakeCatalogCache
func addAppToCache(cache *FakeCatalogCache, app *catalog.App) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.apps[app.CatalogID] = app
}

// FakeOrchestrator collects submitted intents for testing.
//
// When appStore is set, Submit emulates the real orchestrator's
// record-at-enqueue behavior: an install intent upserts the app row
// (status "installing") synchronously before being recorded, so handler
// tests can verify the 202-response app record.
type FakeOrchestrator struct {
	mu       sync.Mutex
	intents  []orchestrator.Intent
	readyCh  chan struct{}
	appStore store.AppStoreInterface
}

func newFakeOrchestrator() *FakeOrchestrator {
	return &FakeOrchestrator{readyCh: make(chan struct{})}
}

func (f *FakeOrchestrator) Submit(intent orchestrator.Intent) {
	if f.appStore != nil {
		if i, ok := intent.(orchestrator.InstallAppIntent); ok {
			_ = f.appStore.Install(i.AppName, i.AppName, "", nil, nil)
		}
	}
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
	orch.appStore = appStore // emulate record-at-enqueue

	ref, app, err := mod.Install("jellyfin")
	require.NoError(t, err)
	assert.NotEmpty(t, ref.ID)
	assert.Equal(t, 1, orch.IntentCount())

	intent, ok := orch.LastIntent().(orchestrator.InstallAppIntent)
	assert.True(t, ok, "expected InstallAppIntent")
	assert.Equal(t, "jellyfin", intent.AppName)

	// The app record is read back after submit and returned alongside the
	// intent ref.
	require.NotNil(t, app)
	assert.Equal(t, "jellyfin", app.CatalogID)
	assert.Equal(t, "installing", app.Status)
}

func TestAppsModule_Install_NotFound(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)

	_, _, err := mod.Install("nonexistent")
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

	apps, err := mod.GetCatalog(context.Background())
	require.NoError(t, err)
	assert.Len(t, apps, 2)
}

func TestAppsModule_GetCatalog_FillsSizeFromLocalImages(t *testing.T) {
	cache := NewFakeCatalogCache()
	// Declared estimate wins; shared images counted once; missing images skipped.
	addAppToCache(cache, &catalog.App{
		CatalogID:       "jellyfin",
		DisplayName:     "Jellyfin",
		EstimatedSizeMB: 1150,
		Containers: []catalog.ContainerDef{
			{Name: "apps-jellyfin", Image: "docker.io/jellyfin/jellyfin:10.11.11"},
		},
	})
	addAppToCache(cache, &catalog.App{
		CatalogID:   "authentik",
		DisplayName: "Authentik",
		Containers: []catalog.ContainerDef{
			{Name: "apps-authentik-server", Image: "ghcr.io/goauthentik/server:2025.10.3"},
			{Name: "apps-authentik-worker", Image: "ghcr.io/goauthentik/server:2025.10.3"}, // shared
			{Name: "apps-authentik-postgres", Image: "docker.io/postgres:16-alpine"},
			{Name: "apps-authentik-redis", Image: "docker.io/redis:7-alpine"}, // not local
		},
	})
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)
	mod.SetImageSizeResolver(func(_ context.Context, image string) (int64, bool) {
		switch image {
		case "ghcr.io/goauthentik/server:2025.10.3":
			return 524288000, true // 500 MiB
		case "docker.io/postgres:16-alpine":
			return 262144000, true // 250 MiB
		default:
			return 0, false
		}
	})

	apps, err := mod.GetCatalog(context.Background())
	require.NoError(t, err)

	byID := map[string]catalog.App{}
	for _, app := range apps {
		byID[app.CatalogID] = *app
	}
	assert.Equal(t, 1150, byID["jellyfin"].EstimatedSizeMB, "declared estimate must not be overwritten")
	assert.Equal(t, 750, byID["authentik"].EstimatedSizeMB, "shared image counted once, missing image skipped")
}

func TestAppsModule_GetCatalog_NoResolverKeepsZero(t *testing.T) {
	cache := NewFakeCatalogCache()
	addAppToCache(cache, &catalog.App{
		CatalogID:   "navidrome",
		DisplayName: "Navidrome",
		Containers:  []catalog.ContainerDef{{Name: "apps-navidrome", Image: "docker.io/deluan/navidrome:latest"}},
	})
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)

	apps, err := mod.GetCatalog(context.Background())
	require.NoError(t, err)
	assert.Len(t, apps, 1)
	assert.Equal(t, 0, apps[0].EstimatedSizeMB)
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
	appMod := mod
	r := chi.NewRouter(); NewAppsRouter(appMod, r)

	req := httptest.NewRequest("GET", "/apps/installed", nil)
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

func TestAppsHTTP_Install_Returns202WithAppRecord(t *testing.T) {
	cache := NewFakeCatalogCache()
	addAppToCache(cache, &catalog.App{CatalogID: "jellyfin", DisplayName: "Jellyfin"})
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)
	orch.appStore = appStore // emulate record-at-enqueue
	appMod := mod
	r := chi.NewRouter(); NewAppsRouter(appMod, r)

	req := httptest.NewRequest("POST", "/apps/jellyfin/install", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	var resp struct {
		IntentID string `json:"intentId"`
		App      *struct {
			CatalogID string `json:"catalog_id"`
			Status    string `json:"status"`
		} `json:"app"`
	}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.IntentID)
	require.NotNil(t, resp.App, "202 must carry the installing app record")
	assert.Equal(t, "jellyfin", resp.App.CatalogID)
	assert.Equal(t, "installing", resp.App.Status)
}

func TestAppsHTTP_Install_NotFound(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAppsModule(cache, appStore, orch, logger)
	appMod := mod
	r := chi.NewRouter(); NewAppsRouter(appMod, r)

	req := httptest.NewRequest("POST", "/apps/nonexistent/install", nil)
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
	appMod := mod
	r := chi.NewRouter(); NewAppsRouter(appMod, r)

	req := httptest.NewRequest("POST", "/apps/jellyfin/uninstall", strings.NewReader(`{"clearData":true}`))
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
	appMod := mod
	r := chi.NewRouter(); NewAppsRouter(appMod, r)

	body := `{"displayName":"My Jellyfin"}`
	req := httptest.NewRequest("PATCH", "/apps/jellyfin/rename", strings.NewReader(body))
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
	appMod := mod
	r := chi.NewRouter(); NewAppsRouter(appMod, r)

	body := `{"displayName":""}`
	req := httptest.NewRequest("PATCH", "/apps/jellyfin/rename", strings.NewReader(body))
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
	appMod := mod
	r := chi.NewRouter(); NewAppsRouter(appMod, r)

	req := httptest.NewRequest("GET", "/apps/jellyfin/metadata", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp catalog.App
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "jellyfin", resp.CatalogID)
}

func TestAppsHTTP_Icon_ServesFile(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, "jellyfin")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "icon.png"), []byte("png-bytes"), 0o644))

	mod := NewAppsModule(cache, appStore, orch, logger)
	mod.SetAppsDir(tmpDir)
	r := chi.NewRouter(); NewAppsRouter(mod, r)

	req := httptest.NewRequest("GET", "/apps/jellyfin/icon", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "png-bytes", w.Body.String())
	assert.Equal(t, "public, max-age=86400", w.Header().Get("Cache-Control"))
}

func TestAppsHTTP_Icon_Missing(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	tmpDir := t.TempDir()
	mod := NewAppsModule(cache, appStore, orch, logger)
	mod.SetAppsDir(tmpDir)
	r := chi.NewRouter(); NewAppsRouter(mod, r)

	req := httptest.NewRequest("GET", "/apps/jellyfin/icon", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAppsHTTP_RefreshCatalog(t *testing.T) {
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	tmpDir := t.TempDir()
	mod := NewAppsModule(cache, appStore, orch, logger)
	appMod := mod
	appMod.SetAppsDir(tmpDir)
	r := chi.NewRouter(); NewAppsRouter(appMod, r)

	req := httptest.NewRequest("POST", "/apps/refresh-catalog", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}
