// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planTestApps is the same integration shape catalog/plan_test.go uses: radarr
// and sonarr each require a download client, jellyseerr requires a media
// server plus a multi-valued pvr slot. Real resolution, not a stub, so these
// tests prove the handler carries what the planner actually decided rather
// than what a fake was told to return.
func planTestApps() []*catalog.App {
	return []*catalog.App{
		{CatalogID: "qbittorrent", DisplayName: "qBittorrent"},
		{CatalogID: "deluge", DisplayName: "Deluge"},
		{CatalogID: "jellyfin", DisplayName: "Jellyfin"},
		{CatalogID: "radarr", DisplayName: "Radarr", Integrations: map[string]catalog.Integration{
			"downloadClient": {
				Required:   true,
				Multi:      false,
				Compatible: []catalog.CompatibleApp{{App: "qbittorrent", Default: true}, {App: "deluge"}},
			},
		}},
		{CatalogID: "jellyseerr", DisplayName: "Jellyseerr", Integrations: map[string]catalog.Integration{
			"mediaServer": {
				Required:   true,
				Multi:      false,
				Compatible: []catalog.CompatibleApp{{App: "jellyfin", Default: true}},
			},
		}},
	}
}

// newPlanRouter builds the app router over a real AppGraph seeded with the
// given installed set, and registers every catalog app in the cache the
// handler checks against.
func newPlanRouter(t *testing.T, installed []string, wirePlans bool) *chi.Mux {
	t.Helper()

	apps := planTestApps()
	cache := NewFakeCatalogCache()
	for _, app := range apps {
		addAppToCache(cache, app)
	}

	mod := NewAppsModule(cache, NewFakeAppStore(), newFakeOrchestrator(),
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	if wirePlans {
		graph := catalog.NewGraph(apps)
		graph.SetInstalled(installed)
		mod.SetPlanSource(graph)
	}

	r := chi.NewRouter()
	NewAppsRouter(mod, r)
	return r
}

func getJSON(t *testing.T, r *chi.Mux, path string, out interface{}) int {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), out))
	}
	return w.Code
}

func TestInstallPlanHandler_ReportsChoiceWhenNothingInstalled(t *testing.T) {
	r := newPlanRouter(t, nil, true)

	var plan catalog.InstallPlan
	code := getJSON(t, r, "/apps/radarr/install-plan", &plan)

	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "radarr", plan.App)
	assert.True(t, plan.CanInstall)
	require.Len(t, plan.Choices, 1, "a required integration with no provider installed must surface as a choice")
	assert.Equal(t, "downloadClient", plan.Choices[0].Integration)
	assert.True(t, plan.Choices[0].Required)
	assert.Len(t, plan.Choices[0].Available, 2)
	assert.Equal(t, "qbittorrent", plan.Choices[0].Recommended, "the metadata default must carry through")
	assert.Empty(t, plan.AutoConfig)
}

func TestInstallPlanHandler_ReflectsTheInstalledSet(t *testing.T) {
	// The same request against a graph where the provider is already present
	// must resolve to auto-config rather than a choice: the plan reads the
	// live graph the orchestrator keeps current, not a snapshot taken at
	// router construction.
	r := newPlanRouter(t, []string{"qbittorrent"}, true)

	var plan catalog.InstallPlan
	require.Equal(t, http.StatusOK, getJSON(t, r, "/apps/radarr/install-plan", &plan))

	assert.True(t, plan.CanInstall)
	assert.Empty(t, plan.Choices, "nothing to choose once exactly one compatible provider is installed")
	require.Len(t, plan.AutoConfig, 1)
	assert.Equal(t, "qbittorrent", plan.AutoConfig[0].Source)
	assert.Equal(t, "downloadClient", plan.AutoConfig[0].Integration)
}

func TestInstallPlanHandler_ReportsDependents(t *testing.T) {
	// Installing Jellyfin must report that the already-installed Jellyseerr
	// will be reconfigured to point at it.
	r := newPlanRouter(t, []string{"jellyseerr"}, true)

	var plan catalog.InstallPlan
	require.Equal(t, http.StatusOK, getJSON(t, r, "/apps/jellyfin/install-plan", &plan))

	require.Len(t, plan.Dependents, 1)
	assert.Equal(t, "jellyseerr", plan.Dependents[0].Target)
	assert.Equal(t, "jellyfin", plan.Dependents[0].Source)
	assert.Equal(t, "mediaServer", plan.Dependents[0].Integration)
}

func TestRemovePlanHandler_AllowedWithAlternative(t *testing.T) {
	r := newPlanRouter(t, []string{"radarr", "qbittorrent", "deluge"}, true)

	var plan catalog.RemovePlan
	require.Equal(t, http.StatusOK, getJSON(t, r, "/apps/qbittorrent/remove-plan", &plan))

	assert.Equal(t, "qbittorrent", plan.App)
	assert.True(t, plan.CanRemove, "deluge can fill the slot, so removal is disruptive but permitted")
	assert.Equal(t, []string{"radarr"}, plan.WillUnconfigure)
	assert.Empty(t, plan.Blockers)
}

func TestRemovePlanHandler_BlockedWhenNoAlternative(t *testing.T) {
	r := newPlanRouter(t, []string{"radarr", "qbittorrent"}, true)

	var plan catalog.RemovePlan
	require.Equal(t, http.StatusOK, getJSON(t, r, "/apps/qbittorrent/remove-plan", &plan))

	assert.False(t, plan.CanRemove, "radarr requires a download client and nothing else is installed")
	require.Len(t, plan.Blockers, 1)
	assert.Contains(t, plan.Blockers[0], "radarr")
}

func TestPlanHandlers_UnknownAppIs404(t *testing.T) {
	r := newPlanRouter(t, nil, true)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/apps/not-a-real-app/install-plan", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/apps/not-a-real-app/remove-plan", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestPlanHandlers_UnwiredSourceIs503 pins the distinction between "the plan
// says there is nothing to do" and "nothing was able to tell me". A handler
// that returned an empty plan here would let a client conclude an app needs
// no dependencies when in fact no graph was ever wired.
func TestPlanHandlers_UnwiredSourceIs503(t *testing.T) {
	r := newPlanRouter(t, nil, false)

	for _, path := range []string{"/apps/radarr/install-plan", "/apps/radarr/remove-plan"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		assert.Equal(t, http.StatusServiceUnavailable, w.Code, path)
		assert.NotContains(t, w.Body.String(), `"canInstall"`, path)
	}
}

// TestPlanHandlers_UnknownAppCheckedBeforeSource pins the ordering: a name
// outside the catalog is a 404 whether or not a plan source exists, so a
// typo does not read as a server fault.
func TestPlanHandlers_UnknownAppCheckedBeforeSource(t *testing.T) {
	r := newPlanRouter(t, nil, false)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/apps/not-a-real-app/install-plan", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
}
