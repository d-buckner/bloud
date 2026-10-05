// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHomeModule_GetLayout(t *testing.T) {
	posStore := NewFakePositionStore()
	appStore := NewFakeAppStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	appStore.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", IsSystem: false})
	appStore.AddApp(&store.InstalledApp{CatalogID: "traefik", DisplayName: "Traefik", IsSystem: true})

	getLaunchPaths := func() map[string]string {
		return map[string]string{"jellyfin": "/watch"}
	}

	mod := NewHomeModule(posStore, appStore, getLaunchPaths, logger)
	layout, err := mod.GetLayout("alice")
	require.NoError(t, err)
	assert.Len(t, layout.Apps, 1)
	assert.Equal(t, "jellyfin", layout.Apps[0].CatalogID)
	assert.Equal(t, "/watch", layout.Apps[0].SSOLaunchPath)
	assert.Len(t, layout.Widgets, 0)
}

func TestHomeModule_GetLayout_WithPositions(t *testing.T) {
	posStore := NewFakePositionStore()
	appStore := NewFakeAppStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	appStore.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", IsSystem: false})

	x, y := 0, 0
	_ = posStore.SetForUser("alice", []store.Position{
		{ElementID: "jellyfin", ElementType: "app", X: &x, Y: &y, W: 2, H: 2},
	})

	getLaunchPaths := func() map[string]string { return nil }
	mod := NewHomeModule(posStore, appStore, getLaunchPaths, logger)
	layout, err := mod.GetLayout("alice")
	require.NoError(t, err)
	assert.Len(t, layout.Apps, 1)
	assert.Equal(t, 2, layout.Apps[0].W)
}

func TestHomeModule_GetLayout_Empty(t *testing.T) {
	posStore := NewFakePositionStore()
	appStore := NewFakeAppStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	getLaunchPaths := func() map[string]string { return nil }
	mod := NewHomeModule(posStore, appStore, getLaunchPaths, logger)
	layout, err := mod.GetLayout("nobody")
	require.NoError(t, err)
	assert.Len(t, layout.Apps, 0)
}

// TestHomeModule_GetLayout_Headless pins the flag the dashboard reads to keep a
// UI-less app off the grid. The app stays in the payload, because its status
// still drives the snapshot and the transition toasts; only the flag changes
// what renders. The wire shape is asserted too, since the frontend reads it
// straight out of the JSON.
func TestHomeModule_GetLayout_Headless(t *testing.T) {
	posStore := NewFakePositionStore()
	appStore := NewFakeAppStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	appStore.AddApp(&store.InstalledApp{CatalogID: "affine-mcp", DisplayName: "AFFiNE MCP"})
	appStore.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin"})

	mod := NewHomeModule(posStore, appStore, func() map[string]string { return nil }, logger)
	mod.SetHeadlessLookup(func() map[string]bool { return map[string]bool{"affine-mcp": true} })

	layout, err := mod.GetLayout("alice")
	require.NoError(t, err)
	require.Len(t, layout.Apps, 2, "a headless app is still installed, so it stays in the payload")

	marked := map[string]bool{}
	for _, app := range layout.Apps {
		marked[app.CatalogID] = app.Headless
	}
	assert.True(t, marked["affine-mcp"])
	assert.False(t, marked["jellyfin"])

	payload, err := json.Marshal(layout)
	require.NoError(t, err)
	assert.Contains(t, string(payload), `"headless":true`)
	assert.NotContains(t, string(payload), `"headless":false`, "omitempty keeps the payload unchanged for every app with a UI")
}

// TestHomeModule_GetLayout_WithoutHeadlessLookup keeps the setter optional: a
// module wired without one marks nothing, so the payload is what it was before
// the flag existed.
func TestHomeModule_GetLayout_WithoutHeadlessLookup(t *testing.T) {
	posStore := NewFakePositionStore()
	appStore := NewFakeAppStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	appStore.AddApp(&store.InstalledApp{CatalogID: "affine-mcp", DisplayName: "AFFiNE MCP"})

	mod := NewHomeModule(posStore, appStore, func() map[string]string { return nil }, logger)
	layout, err := mod.GetLayout("alice")
	require.NoError(t, err)
	require.Len(t, layout.Apps, 1)
	assert.False(t, layout.Apps[0].Headless)
}

func TestHomeModule_SetLayout(t *testing.T) {
	posStore := NewFakePositionStore()
	appStore := NewFakeAppStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewHomeModule(posStore, appStore, func() map[string]string { return nil }, logger)
	err := mod.SetLayout("alice", []store.Position{})
	assert.NoError(t, err)
}

// ---- HTTP handler tests ----

func TestHomeHTTP_GetLayout(t *testing.T) {
	posStore := NewFakePositionStore()
	appStore := NewFakeAppStore()
	appStore.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", IsSystem: false})
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	getLaunchPaths := func() map[string]string { return nil }
	mod := NewHomeModule(posStore, appStore, getLaunchPaths, logger)
	r := chi.NewRouter()
	NewHomeRouter(mod, r)

	// Add a fake user to context
	req := httptest.NewRequest(http.MethodGet, "/user/home", nil)
	user := &store.User{Username: "alice", Role: store.RoleMember}
	ctx := context.WithValue(req.Context(), userContextKey, user)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp homeResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Len(t, resp.Apps, 1)
}

func TestHomeHTTP_SetLayout(t *testing.T) {
	posStore := NewFakePositionStore()
	appStore := NewFakeAppStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	getLaunchPaths := func() map[string]string { return nil }
	mod := NewHomeModule(posStore, appStore, getLaunchPaths, logger)
	r := chi.NewRouter()
	NewHomeRouter(mod, r)

	body := `[]`
	req := httptest.NewRequest(http.MethodPut, "/user/layout", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHomeHTTP_SetLayout_InvalidBody(t *testing.T) {
	posStore := NewFakePositionStore()
	appStore := NewFakeAppStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	getLaunchPaths := func() map[string]string { return nil }
	mod := NewHomeModule(posStore, appStore, getLaunchPaths, logger)
	r := chi.NewRouter()
	NewHomeRouter(mod, r)

	body := `not-json`
	req := httptest.NewRequest(http.MethodPut, "/user/layout", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
