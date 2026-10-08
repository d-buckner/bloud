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
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
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

// TestHomeModule_GetLayout_ClientAccess pins the flag the dashboard's
// right-click menu reads to offer the reveal surface only for apps that publish
// a clientAccess credential. The wire shape is asserted too, since the frontend
// reads it straight out of the JSON.
func TestHomeModule_GetLayout_ClientAccess(t *testing.T) {
	posStore := NewFakePositionStore()
	appStore := NewFakeAppStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	appStore.AddApp(&store.InstalledApp{CatalogID: "hermes-webui", DisplayName: "Hermes Web UI"})
	appStore.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin"})

	mod := NewHomeModule(posStore, appStore, func() map[string]string { return nil }, logger)
	mod.SetClientAccessLookup(func() map[string]bool { return map[string]bool{"hermes-webui": true} })

	layout, err := mod.GetLayout("alice")
	require.NoError(t, err)
	require.Len(t, layout.Apps, 2)

	marked := map[string]bool{}
	for _, app := range layout.Apps {
		marked[app.CatalogID] = app.HasClientAccess
	}
	assert.True(t, marked["hermes-webui"])
	assert.False(t, marked["jellyfin"])

	payload, err := json.Marshal(layout)
	require.NoError(t, err)
	assert.Contains(t, string(payload), `"has_client_access":true`)
	assert.NotContains(t, string(payload), `"has_client_access":false`)
}

func TestHomeModule_Launchers(t *testing.T) {
	posStore := NewFakePositionStore()
	appStore := NewFakeAppStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	extStore := newExternalAppsTestStore(t)

	require.NoError(t, extStore.Upsert(&store.ExternalApp{
		ID: "l1", Kind: string(store.ExternalAppKindLauncher), Name: "Photos", URL: "https://photos.example.com",
	}))

	mod := NewHomeModule(posStore, appStore, func() map[string]string { return nil }, logger)
	mod.SetExternalApps(extStore)

	layout, err := mod.GetLayout("alice")
	require.NoError(t, err)
	require.Len(t, layout.Launchers, 1)
	assert.Equal(t, "l1", layout.Launchers[0].ID)
	assert.Equal(t, "Photos", layout.Launchers[0].Name)
	assert.Equal(t, "https://photos.example.com", layout.Launchers[0].URL)
}

// An external record's grid position round-trips the same way an app's does:
// save the settled layout with the record's ID, read the home payload back,
// and the tile carries the position it was given. Rename and remove ride the
// same record, so the three together are the grid contract for a thing that
// is not an installed app.
func TestHomeModule_ExternalRecordGridRoundTrip(t *testing.T) {
	posStore := NewFakePositionStore()
	appStore := NewFakeAppStore()
	extStore := store.NewExternalAppStore(testdb.SetupTestDB(t))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewHomeModule(posStore, appStore, func() map[string]string { return nil }, logger)
	mod.SetExternalApps(extStore)

	require.NoError(t, extStore.Upsert(&store.ExternalApp{
		ID: "nas-router", Kind: string(store.ExternalAppKindLauncher),
		Name: "Router", URL: "http://192.168.1.1",
	}))

	// Position it.
	x, y := 3, 2
	require.NoError(t, mod.SetLayout("alice", []store.Position{
		{ElementID: "nas-router", ElementType: "launcher", X: &x, Y: &y, W: 2, H: 1},
	}))

	home, err := mod.GetLayout("alice")
	require.NoError(t, err)
	require.Len(t, home.Launchers, 1)
	assert.Equal(t, "nas-router", home.Launchers[0].ID)
	require.NotNil(t, home.Launchers[0].X)
	assert.Equal(t, 3, *home.Launchers[0].X)
	assert.Equal(t, 2, home.Launchers[0].W)

	// Rename: the tile follows the record, the position stays put.
	require.NoError(t, extStore.Upsert(&store.ExternalApp{
		ID: "nas-router", Kind: string(store.ExternalAppKindLauncher),
		Name: "Home Router", URL: "http://10.0.0.1",
	}))
	home, err = mod.GetLayout("alice")
	require.NoError(t, err)
	require.Len(t, home.Launchers, 1)
	assert.Equal(t, "Home Router", home.Launchers[0].Name)
	assert.Equal(t, "http://10.0.0.1", home.Launchers[0].URL)
	require.NotNil(t, home.Launchers[0].X)
	assert.Equal(t, 3, *home.Launchers[0].X, "renaming must not lose the position")

	// Remove: the tile is gone and its position row goes with it.
	require.NoError(t, extStore.Delete("nas-router"))
	home, err = mod.GetLayout("alice")
	require.NoError(t, err)
	assert.Empty(t, home.Launchers)
}

// A remote install of a catalog app carries the catalog ID on its tile so the
// dashboard can borrow that app's icon. A launcher has no catalog entry to
// borrow from and so carries none.
func TestHomeModule_RemoteInstallTileCarriesItsCatalogApp(t *testing.T) {
	extStore := store.NewExternalAppStore(testdb.SetupTestDB(t))
	mod := NewHomeModule(NewFakePositionStore(), NewFakeAppStore(),
		func() map[string]string { return nil },
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	mod.SetExternalApps(extStore)

	require.NoError(t, extStore.Upsert(&store.ExternalApp{
		ID: "remote-jf", Kind: string(store.ExternalAppKindProvider),
		Source: store.ExternalAppSourceForApp("jellyfin"),
		Name:   "Jellyfin", URL: "https://media.example.com",
	}))
	require.NoError(t, extStore.Upsert(&store.ExternalApp{
		ID: "plain-launch", Kind: string(store.ExternalAppKindLauncher),
		Name: "Router", URL: "http://192.168.1.1",
	}))

	home, err := mod.GetLayout("alice")
	require.NoError(t, err)
	byID := map[string]launcherWithPosition{}
	for _, l := range home.Launchers {
		byID[l.ID] = l
	}
	require.Len(t, home.Launchers, 2)
	assert.Equal(t, "jellyfin", byID["remote-jf"].App, "the tile knows which catalog app it stands for")
	assert.Empty(t, byID["plain-launch"].App, "a launcher has no catalog app to borrow")
}
