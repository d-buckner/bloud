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

	// Prowlarr has a non-required integration compatible with radarr
	prowlarrCatalog := fixtureCatalogAppWithOptionalIntegration("prowlarr", "movieManager", "radarr")

	apps := map[string]*store.InstalledApp{
		"radarr":   fixtureInstalledApp("radarr", "running"),
		"prowlarr": fixtureInstalledApp("prowlarr", "running"),
	}

	tr.cache.On("Get", "prowlarr").Return(prowlarrCatalog, nil)
	tr.cache.On("Get", "radarr").Return(fixtureRadarr(), nil)

	levels := tr.reconciler.computeLevels(apps)

	require.Len(t, levels, 2, "prowlarr should be at a higher level than radarr")
	assert.Equal(t, []string{"radarr"}, levels[0])
	assert.Equal(t, []string{"prowlarr"}, levels[1])
}

func TestComputeLevels_OptionalIntegration_UninstalledCompatibleAppIgnored(t *testing.T) {
	tr := newTestReconcilerWithCache()

	prowlarrCatalog := fixtureCatalogAppWithOptionalIntegration("prowlarr", "movieManager", "radarr")

	// Only prowlarr is installed — radarr is absent
	apps := map[string]*store.InstalledApp{
		"prowlarr": fixtureInstalledApp("prowlarr", "running"),
	}

	tr.cache.On("Get", "prowlarr").Return(prowlarrCatalog, nil)

	levels := tr.reconciler.computeLevels(apps)

	require.Len(t, levels, 1, "prowlarr should stay at level 0 when optional dep is not installed")
	assert.Equal(t, []string{"prowlarr"}, levels[0])
}

func TestComputeLevels_MultipleOptionalIntegrations_AllInstalledOrderedCorrectly(t *testing.T) {
	tr := newTestReconcilerWithCache()

	// Prowlarr optionally integrates with both radarr and sonarr
	prowlarrCatalog := &catalog.App{
		Name: "prowlarr",
		Integrations: map[string]catalog.Integration{
			"movieManager": {Required: false, Compatible: []catalog.CompatibleApp{{App: "radarr"}}},
			"tvManager":    {Required: false, Compatible: []catalog.CompatibleApp{{App: "sonarr"}}},
		},
	}

	apps := map[string]*store.InstalledApp{
		"radarr":   fixtureInstalledApp("radarr", "running"),
		"sonarr":   fixtureInstalledApp("sonarr", "running"),
		"prowlarr": fixtureInstalledApp("prowlarr", "running"),
	}

	tr.cache.On("Get", "prowlarr").Return(prowlarrCatalog, nil)
	tr.cache.On("Get", "radarr").Return(fixtureRadarr(), nil)
	tr.cache.On("Get", "sonarr").Return(fixtureSonarr(), nil)

	levels := tr.reconciler.computeLevels(apps)

	require.Len(t, levels, 2)
	assert.Equal(t, []string{"prowlarr"}, levels[1], "prowlarr must run after both radarr and sonarr")
}

// ============================================================================
// buildAppState — optional integration resolution
// ============================================================================

func TestBuildAppState_OptionalIntegration_IncludedWhenCompatibleAppInstalled(t *testing.T) {
	tr := newTestReconcilerWithCache()

	prowlarrCatalog := fixtureCatalogAppWithOptionalIntegration("prowlarr", "movieManager", "radarr")
	prowlarrApp := fixtureInstalledApp("prowlarr", "running")

	installedApps := map[string]*store.InstalledApp{
		"prowlarr": prowlarrApp,
		"radarr":   fixtureInstalledApp("radarr", "running"),
	}

	tr.cache.On("Get", "prowlarr").Return(prowlarrCatalog, nil)

	state := tr.reconciler.buildAppState(prowlarrApp, installedApps)

	assert.Equal(t, []string{"radarr"}, state.Integrations["movieManager"])
}

func TestBuildAppState_OptionalIntegration_NotIncludedWhenCompatibleAppAbsent(t *testing.T) {
	tr := newTestReconcilerWithCache()

	prowlarrCatalog := fixtureCatalogAppWithOptionalIntegration("prowlarr", "movieManager", "radarr")
	prowlarrApp := fixtureInstalledApp("prowlarr", "running")

	installedApps := map[string]*store.InstalledApp{
		"prowlarr": prowlarrApp,
		// radarr not installed
	}

	tr.cache.On("Get", "prowlarr").Return(prowlarrCatalog, nil)

	state := tr.reconciler.buildAppState(prowlarrApp, installedApps)

	_, hasMovieManager := state.Integrations["movieManager"]
	assert.False(t, hasMovieManager, "movieManager should be absent when radarr is not installed")
}

func TestBuildAppState_RequiredIntegrationsUnaffected(t *testing.T) {
	tr := newTestReconcilerWithCache()

	// App has a required integration already in IntegrationConfig
	radarrApp := fixtureInstalledAppWithIntegrations("radarr", "running", map[string]string{
		"downloadClient": "qbittorrent",
	})

	installedApps := map[string]*store.InstalledApp{
		"radarr":      radarrApp,
		"qbittorrent": fixtureInstalledApp("qbittorrent", "running"),
	}

	// radarr catalog has no optional integrations
	tr.cache.On("Get", "radarr").Return(fixtureRadarr(), nil)

	state := tr.reconciler.buildAppState(radarrApp, installedApps)

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
