package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
)

// newTestReconcilerWithCache creates a Reconciler with a real catalog cache mock wired in.
func newTestReconcilerWithCache() *testReconcilerWithCache {
	cache := new(MockCatalogCache)
	registry := new(MockConfiguratorRegistry)
	appStore := new(MockAppStore)
	r := NewReconciler(
		registry,
		appStore,
		cache,
		"/tmp/bloud-test",
		newTestLogger(),
		ReconcileConfig{HealthCheckTimeout: 100 * time.Millisecond},
	)
	return &testReconcilerWithCache{
		reconciler: r,
		cache:      cache,
		registry:   registry,
		appStore:   appStore,
	}
}

type testReconcilerWithCache struct {
	reconciler *Reconciler
	cache      *MockCatalogCache
	registry   *MockConfiguratorRegistry
	appStore   *MockAppStore
}

// ============================================================================
// computeLevels — optional integration ordering
// ============================================================================

func TestComputeLevels_OptionalIntegration_InstalledCompatibleAppAddsOrderingConstraint(t *testing.T) {
	tr := newTestReconcilerWithCache()

	// Miniflux has a non-required integration compatible with jellyfin
	minifluxCatalog := fixtureCatalogAppWithOptionalIntegration("miniflux", "movieManager", "jellyfin")

	apps := map[string]*store.InstalledApp{
		"jellyfin": fixtureInstalledApp("jellyfin", "running"),
		"miniflux": fixtureInstalledApp("miniflux", "running"),
	}

	tr.cache.On("Get", "miniflux").Return(minifluxCatalog, nil)
	tr.cache.On("Get", "jellyfin").Return(fixtureJellyfin(), nil)

	levels := tr.reconciler.computeLevels(apps)

	require.Len(t, levels, 2, "miniflux should be at a higher level than jellyfin")
	assert.Equal(t, []string{"jellyfin"}, levels[0])
	assert.Equal(t, []string{"miniflux"}, levels[1])
}

func TestComputeLevels_OptionalIntegration_UninstalledCompatibleAppIgnored(t *testing.T) {
	tr := newTestReconcilerWithCache()

	minifluxCatalog := fixtureCatalogAppWithOptionalIntegration("miniflux", "movieManager", "jellyfin")

	// Only miniflux is installed — jellyfin is absent
	apps := map[string]*store.InstalledApp{
		"miniflux": fixtureInstalledApp("miniflux", "running"),
	}

	tr.cache.On("Get", "miniflux").Return(minifluxCatalog, nil)

	levels := tr.reconciler.computeLevels(apps)

	require.Len(t, levels, 1, "miniflux should stay at level 0 when optional dep is not installed")
	assert.Equal(t, []string{"miniflux"}, levels[0])
}

func TestComputeLevels_MultipleOptionalIntegrations_AllInstalledOrderedCorrectly(t *testing.T) {
	tr := newTestReconcilerWithCache()

	// Miniflux optionally integrates with both jellyfin and qbittorrent
	minifluxCatalog := &catalog.App{
		Name: "miniflux",
		Integrations: map[string]catalog.Integration{
			"movieManager": {Required: false, Compatible: []catalog.CompatibleApp{{App: "jellyfin"}}},
			"tvManager":    {Required: false, Compatible: []catalog.CompatibleApp{{App: "qbittorrent"}}},
		},
	}

	apps := map[string]*store.InstalledApp{
		"jellyfin":    fixtureInstalledApp("jellyfin", "running"),
		"qbittorrent": fixtureInstalledApp("qbittorrent", "running"),
		"miniflux":    fixtureInstalledApp("miniflux", "running"),
	}

	tr.cache.On("Get", "miniflux").Return(minifluxCatalog, nil)
	tr.cache.On("Get", "jellyfin").Return(fixtureJellyfin(), nil)
	tr.cache.On("Get", "qbittorrent").Return(fixtureQBittorrent(), nil)

	levels := tr.reconciler.computeLevels(apps)

	require.Len(t, levels, 2)
	assert.Equal(t, []string{"miniflux"}, levels[1], "miniflux must run after both jellyfin and qbittorrent")
}

// ============================================================================
// buildAppState — optional integration resolution
// ============================================================================

func TestBuildAppState_OptionalIntegration_IncludedWhenCompatibleAppInstalled(t *testing.T) {
	tr := newTestReconcilerWithCache()

	minifluxCatalog := fixtureCatalogAppWithOptionalIntegration("miniflux", "movieManager", "jellyfin")
	minifluxApp := fixtureInstalledApp("miniflux", "running")

	installedApps := map[string]*store.InstalledApp{
		"miniflux": minifluxApp,
		"jellyfin": fixtureInstalledApp("jellyfin", "running"),
	}

	tr.cache.On("Get", "miniflux").Return(minifluxCatalog, nil)

	state := tr.reconciler.buildAppState(minifluxApp, installedApps)

	assert.Equal(t, []string{"jellyfin"}, state.Integrations["movieManager"])
}

func TestBuildAppState_OptionalIntegration_NotIncludedWhenCompatibleAppAbsent(t *testing.T) {
	tr := newTestReconcilerWithCache()

	minifluxCatalog := fixtureCatalogAppWithOptionalIntegration("miniflux", "movieManager", "jellyfin")
	minifluxApp := fixtureInstalledApp("miniflux", "running")

	installedApps := map[string]*store.InstalledApp{
		"miniflux": minifluxApp,
		// jellyfin not installed
	}

	tr.cache.On("Get", "miniflux").Return(minifluxCatalog, nil)

	state := tr.reconciler.buildAppState(minifluxApp, installedApps)

	_, hasMovieManager := state.Integrations["movieManager"]
	assert.False(t, hasMovieManager, "movieManager should be absent when jellyfin is not installed")
}

func TestBuildAppState_RequiredIntegrationsUnaffected(t *testing.T) {
	tr := newTestReconcilerWithCache()

	// App has a required integration already in IntegrationConfig
	jellyfinApp := fixtureInstalledAppWithIntegrations("jellyfin", "running", map[string]string{
		"downloadClient": "qbittorrent",
	})

	installedApps := map[string]*store.InstalledApp{
		"jellyfin":    jellyfinApp,
		"qbittorrent": fixtureInstalledApp("qbittorrent", "running"),
	}

	// jellyfin catalog has no optional integrations
	tr.cache.On("Get", "jellyfin").Return(fixtureJellyfin(), nil)

	state := tr.reconciler.buildAppState(jellyfinApp, installedApps)

	assert.Equal(t, []string{"qbittorrent"}, state.Integrations["downloadClient"])
}

// ============================================================================
// Fixtures
// ============================================================================

func fixtureCatalogAppWithOptionalIntegration(appName, integrationName, compatibleApp string) *catalog.App {
	return &catalog.App{
		Name: appName,
		Integrations: map[string]catalog.Integration{
			integrationName: {
				Required:   false,
				Compatible: []catalog.CompatibleApp{{App: compatibleApp}},
			},
		},
	}
}
