// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	_ = s.Create(store.RemoteApp{ID: "app1", AppID: "jellyfin", TailnetAddr: "ts-jellyfin.ts.net", HostLabel: "John's server"})
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
	_ = s.Create(store.RemoteApp{ID: "app1", AppID: "jellyfin"})
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
	_ = s.Create(store.RemoteApp{ID: "app1", AppID: "jellyfin", HostLabel: "John's"})
	cache := NewFakeCatalogCache()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewRemoteAppsModule(s, cache, orch, logger)
	r := chi.NewRouter()
	NewRemoteAppsRouter(mod, r)

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
	r := chi.NewRouter()
	NewRemoteAppsRouter(mod, r)

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
	r := chi.NewRouter()
	NewRemoteAppsRouter(mod, r)

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
	r := chi.NewRouter()
	NewRemoteAppsRouter(mod, r)

	req := httptest.NewRequest("DELETE", "/sharing/remote-apps/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ---------------------------------------------------------------------------
// HTTP-level (end-to-end through the router) tests for the remote-apps surface.
// Merged from the former remote_apps_test.go; the stray TestSlugify that lived
// there was dropped as a strict subset of pkg/slug's own TestSlugify.
// ---------------------------------------------------------------------------

func TestAPI_AddRemoteApp(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	body := strings.NewReader(`{"appId":"test-app","tailnetAddr":"ts-test.tail123.ts.net","hostLabel":"Johan"}`)
	w := serverRequest(t, server, "POST", "/api/sharing/remote-apps", body)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.NotEmpty(t, response["intentId"], "response should contain intentId")
}

func TestAPI_AddRemoteApp_WithSSO(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	// Add a catalog app with SSO config
	server.catalog.(*FakeCatalogCache).AddApp(&catalog.App{
		CatalogID:   "navidrome",
		DisplayName: "Navidrome",
		SSO: catalog.SSO{
			Strategy:    "forward-auth",
			BypassPaths: []string{"/rest/"},
		},
	})

	body := strings.NewReader(`{"appId":"navidrome","tailnetAddr":"ts-nav.tail123.ts.net","hostLabel":"Johan"}`)
	w := serverRequest(t, server, "POST", "/api/sharing/remote-apps", body)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.NotEmpty(t, response["intentId"], "response should contain intentId")
}

func TestAPI_AddRemoteApp_MissingFields(t *testing.T) {
	server, _ := setupTestServer(t)

	tests := []struct {
		name string
		body string
		want string
	}{
		{"missing appId", `{"tailnetAddr":"ts.net","hostLabel":"X"}`, "appId is required"},
		{"missing tailnetAddr", `{"appId":"test-app","hostLabel":"X"}`, "tailnetAddr is required"},
		{"missing hostLabel", `{"appId":"test-app","tailnetAddr":"ts.net"}`, "hostLabel is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.NewReader(tt.body)
			w := serverRequest(t, server, "POST", "/api/sharing/remote-apps", body)

			assert.Equal(t, http.StatusBadRequest, w.Code)

			var resp map[string]string
			require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
			assert.Equal(t, tt.want, resp["error"])
		})
	}
}

func TestAPI_AddRemoteApp_UnknownApp(t *testing.T) {
	server, _ := setupTestServer(t)

	body := strings.NewReader(`{"appId":"nonexistent","tailnetAddr":"ts.net","hostLabel":"X"}`)
	w := serverRequest(t, server, "POST", "/api/sharing/remote-apps", body)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAPI_ListRemoteApps_Empty(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	w := serverRequest(t, server, "GET", "/api/sharing/remote-apps", nil)

	assert.Equal(t, http.StatusOK, w.Code)

	var apps []store.RemoteApp
	err := json.NewDecoder(w.Body).Decode(&apps)
	require.NoError(t, err)
	assert.Empty(t, apps)
}

func TestAPI_ListRemoteApps_WithApps(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	// Add a remote app directly to the store
	fakeStore := server.remoteAppStore.(*FakeRemoteAppStore)
	_ = fakeStore.Create(store.RemoteApp{
		ID:          "test-id-1",
		HostLabel:   "Johan",
		AppID:       "jellyfin",
		AppName:     "Jellyfin",
		TailnetAddr: "ts-jf.tail123.ts.net",
		Status:      "active",
		BypassPaths: []string{},
	})

	w := serverRequest(t, server, "GET", "/api/sharing/remote-apps", nil)

	assert.Equal(t, http.StatusOK, w.Code)

	var apps []store.RemoteApp
	err := json.NewDecoder(w.Body).Decode(&apps)
	require.NoError(t, err)
	assert.Len(t, apps, 1)
	assert.Equal(t, "Johan", apps[0].HostLabel)
}

func TestAPI_DeleteRemoteApp(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	// Add a remote app
	fakeStore := server.remoteAppStore.(*FakeRemoteAppStore)
	_ = fakeStore.Create(store.RemoteApp{
		ID:          "delete-me",
		HostLabel:   "Johan",
		AppID:       "jellyfin",
		AppName:     "Jellyfin",
		Status:      "active",
		BypassPaths: []string{},
	})

	w := serverRequest(t, server, "DELETE", "/api/sharing/remote-apps/delete-me", nil)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.NotEmpty(t, response["intentId"], "response should contain intentId")
}

func TestAPI_DeleteRemoteApp_NotFound(t *testing.T) {
	server, _ := setupTestServerWithWorkingOrchestrator(t)

	w := serverRequest(t, server, "DELETE", "/api/sharing/remote-apps/nonexistent", nil)

	assert.Equal(t, http.StatusNotFound, w.Code)
}
