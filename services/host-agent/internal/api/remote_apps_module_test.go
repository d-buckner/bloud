package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/go-chi/chi/v5"
)

func TestRemoteAppsModule_List_Empty(t *testing.T) {
	store := NewFakeRemoteAppStore()
	cache := NewFakeCatalogCache()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewRemoteAppsModule(store, cache, orch, logger)
	apps, err := mod.List()
	require.NoError(t, err)
	assert.Len(t, apps, 0)
}

func TestRemoteAppsModule_List_WithApps(t *testing.T) {
	s := NewFakeRemoteAppStore()
	s.Create(store.RemoteApp{ID: "app1", AppID: "jellyfin", TailnetAddr: "ts-jellyfin.ts.net", HostLabel: "John's server"})
	cache := NewFakeCatalogCache()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewRemoteAppsModule(s, cache, orch, logger)
	apps, err := mod.List()
	require.NoError(t, err)
	assert.Len(t, apps, 1)
}

func TestRemoteAppsModule_Add_Valid(t *testing.T) {
	s := NewFakeRemoteAppStore()
	cache := NewFakeCatalogCache()
	addAppToCache(cache, &catalog.App{CatalogID: "jellyfin", DisplayName: "Jellyfin"})
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewRemoteAppsModule(s, cache, orch, logger)
	ref, err := mod.Add("jellyfin", "ts-jellyfin.ts.net", "John's server")
	require.NoError(t, err)
	assert.NotEmpty(t, ref.ID)
	assert.Equal(t, 1, orch.IntentCount())
}

func TestRemoteAppsModule_Add_MissingFields(t *testing.T) {
	s := NewFakeRemoteAppStore()
	cache := NewFakeCatalogCache()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewRemoteAppsModule(s, cache, orch, logger)

	_, err := mod.Add("", "addr", "label")
	assert.Error(t, err)

	_, err = mod.Add("jellyfin", "", "label")
	assert.Error(t, err)

	_, err = mod.Add("jellyfin", "addr", "")
	assert.Error(t, err)
}

func TestRemoteAppsModule_Add_UnknownApp(t *testing.T) {
	s := NewFakeRemoteAppStore()
	cache := NewFakeCatalogCache()
	// Empty catalog
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewRemoteAppsModule(s, cache, orch, logger)
	_, err := mod.Add("unknown", "addr", "label")
	assert.Error(t, err)
}

func TestRemoteAppsModule_Delete_Valid(t *testing.T) {
	s := NewFakeRemoteAppStore()
	s.Create(store.RemoteApp{ID: "app1", AppID: "jellyfin"})
	cache := NewFakeCatalogCache()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewRemoteAppsModule(s, cache, orch, logger)
	ref, err := mod.Delete("app1")
	require.NoError(t, err)
	assert.NotEmpty(t, ref.ID)
	assert.Equal(t, 1, orch.IntentCount())
}

func TestRemoteAppsModule_Delete_NotFound(t *testing.T) {
	s := NewFakeRemoteAppStore()
	cache := NewFakeCatalogCache()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewRemoteAppsModule(s, cache, orch, logger)
	_, err := mod.Delete("nonexistent")
	assert.Error(t, err)
}

// ---- HTTP handler tests ----

func TestRemoteAppsHTTP_List(t *testing.T) {
	s := NewFakeRemoteAppStore()
	s.Create(store.RemoteApp{ID: "app1", AppID: "jellyfin", HostLabel: "John's"})
	cache := NewFakeCatalogCache()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewRemoteAppsModule(s, cache, orch, logger)
	r := chi.NewRouter(); NewRemoteAppsRouter(mod, r)

	req := httptest.NewRequest("GET", "/sharing/remote-apps", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRemoteAppsHTTP_Add(t *testing.T) {
	s := NewFakeRemoteAppStore()
	cache := NewFakeCatalogCache()
	addAppToCache(cache, &catalog.App{CatalogID: "jellyfin", DisplayName: "Jellyfin"})
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewRemoteAppsModule(s, cache, orch, logger)
	r := chi.NewRouter(); NewRemoteAppsRouter(mod, r)

	body := `{"appId":"jellyfin","tailnetAddr":"ts-jellyfin.ts.net","hostLabel":"John's"}`
	req := httptest.NewRequest("POST", "/sharing/remote-apps", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestRemoteAppsHTTP_Add_UnknownApp(t *testing.T) {
	s := NewFakeRemoteAppStore()
	cache := NewFakeCatalogCache()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewRemoteAppsModule(s, cache, orch, logger)
	r := chi.NewRouter(); NewRemoteAppsRouter(mod, r)

	body := `{"appId":"unknown","tailnetAddr":"addr","hostLabel":"label"}`
	req := httptest.NewRequest("POST", "/sharing/remote-apps", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRemoteAppsHTTP_Delete_NotFound(t *testing.T) {
	s := NewFakeRemoteAppStore()
	cache := NewFakeCatalogCache()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewRemoteAppsModule(s, cache, orch, logger)
	r := chi.NewRouter(); NewRemoteAppsRouter(mod, r)

	req := httptest.NewRequest("DELETE", "/sharing/remote-apps/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}
