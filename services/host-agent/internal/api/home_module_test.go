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
	posStore.SetForUser("alice", []store.Position{
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
	r := NewHomeRouter(mod.(*homeModuleSimple))

	// Add a fake user to context
	req := httptest.NewRequest("GET", "/api/user/home", nil)
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
	r := NewHomeRouter(mod.(*homeModuleSimple))

	body := `[]`
	req := httptest.NewRequest("PUT", "/api/user/layout", strings.NewReader(body))
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
	r := NewHomeRouter(mod.(*homeModuleSimple))

	body := `not-json`
	req := httptest.NewRequest("PUT", "/api/user/layout", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
