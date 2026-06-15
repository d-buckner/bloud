package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	integrationdomain "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/integration"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
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

	bindings, err := resolveIntegrationBindings(minifluxApp, installedApps, minifluxCatalog)

	require.NoError(t, err)
	assert.Equal(t, integrationBinding("movieManager", "jellyfin"), bindings)
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

	bindings, err := resolveIntegrationBindings(minifluxApp, installedApps, minifluxCatalog)

	require.NoError(t, err)
	assert.Empty(t, bindings)
}

func TestBuildAppState_BoundProviderTakesPrecedenceOverOptionalDiscovery(t *testing.T) {
	tr := newTestReconcilerWithCache()
	app := fixtureInstalledAppWithIntegrations("app", "running", map[string]string{
		"database": "mariadb",
	})
	catalogApp := &catalog.App{
		Name: "app",
		Integrations: map[string]catalog.Integration{
			"database": {
				Required: false,
				Compatible: []catalog.CompatibleApp{
					{App: "postgres"},
					{App: "mariadb"},
				},
			},
		},
	}

	tr.cache.On("Get", "app").Return(catalogApp, nil)

	bindings, err := resolveIntegrationBindings(app, map[string]*store.InstalledApp{
		"app":      app,
		"postgres": fixtureInstalledApp("postgres", "running"),
		"mariadb":  fixtureInstalledApp("mariadb", "running"),
	}, catalogApp)

	require.NoError(t, err)
	assert.Equal(t, integrationBinding("database", "mariadb"), bindings)
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

	catalogApp := fixtureJellyfin()
	catalogApp.Integrations = map[string]catalog.Integration{
		"downloadClient": {
			Required:   true,
			Compatible: []catalog.CompatibleApp{{App: "qbittorrent"}},
		},
	}
	tr.cache.On("Get", "jellyfin").Return(catalogApp, nil)

	bindings, err := resolveIntegrationBindings(jellyfinApp, installedApps, catalogApp)

	require.NoError(t, err)
	assert.Equal(t, integrationBinding("downloadClient", "qbittorrent"), bindings)
}

func TestBuildAppState_JellyfinSyntheticSSOStrategyPreserved(t *testing.T) {
	tr := newTestReconcilerWithCache()
	jellyfin := fixtureInstalledApp("jellyfin", "running")
	catalogApp := fixtureJellyfin()
	catalogApp.SSO = catalog.SSO{Strategy: "ldap"}

	tr.cache.On("Get", "jellyfin").Return(catalogApp, nil)

	state, err := tr.reconciler.buildAppState(jellyfin, map[string]*store.InstalledApp{
		"jellyfin": jellyfin,
	})

	require.NoError(t, err)
	assert.True(t, state.SSOEnabled)
}

func TestBuildAppState_ImmichDeclaredSSOStrategyEnablesSSO(t *testing.T) {
	tr := newTestReconcilerWithCache()
	immich := fixtureInstalledApp("immich", "running")
	authentik := fixtureInstalledApp("authentik", "running")
	catalogApp := &catalog.App{
		Name: "immich",
		SSO:  catalog.SSO{Strategy: "native-oidc"},
		Integrations: map[string]catalog.Integration{
			"sso": {
				Required:   false,
				Compatible: []catalog.CompatibleApp{{App: "authentik"}},
			},
		},
	}

	tr.cache.On("Get", "immich").Return(catalogApp, nil)

	state, err := tr.reconciler.buildAppState(immich, map[string]*store.InstalledApp{
		"immich":    immich,
		"authentik": authentik,
	})

	require.NoError(t, err)
	assert.True(t, state.SSOEnabled)
}

func TestBuildAppState_SSOBindingWithoutStrategyDoesNotEnableSSO(t *testing.T) {
	tr := newTestReconcilerWithCache()
	immich := fixtureInstalledAppWithIntegrations("immich", "running", map[string]string{
		"sso": "authentik",
	})
	catalogApp := &catalog.App{
		Name: "immich",
		Integrations: map[string]catalog.Integration{
			"sso": {
				Compatible: []catalog.CompatibleApp{{App: "authentik"}},
			},
		},
	}
	tr.cache.On("Get", "immich").Return(catalogApp, nil)

	state, err := tr.reconciler.buildAppState(immich, nil)

	require.NoError(t, err)
	assert.False(t, state.SSOEnabled)
}

func TestBuildAppState_IncompatibleBoundProviderReturnsError(t *testing.T) {
	tr := newTestReconcilerWithCache()
	app := fixtureInstalledAppWithIntegrations("immich", "running", map[string]string{
		"database": "mariadb",
	})
	catalogApp := &catalog.App{
		Name: "immich",
		Integrations: map[string]catalog.Integration{
			"database": {
				Required:   true,
				Compatible: []catalog.CompatibleApp{{App: "postgres"}},
			},
		},
	}
	tr.cache.On("Get", "immich").Return(catalogApp, nil)

	_, err := tr.reconciler.buildAppState(app, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "incompatible provider mariadb")
}

// ============================================================================
// buildAppState — LDAPOutput population
// ============================================================================

func TestBuildAppState_LDAPOutputPopulatedWhenStrategyIsLDAP(t *testing.T) {
	tr := newTestReconcilerWithCache()
	tr.reconciler.config.LDAPOutput = &configurator.LDAPOutput{
		Host:         "apps-authentik-ldap",
		Port:         3389,
		BaseDN:       "dc=ldap,dc=goauthentik,dc=io",
		BindUser:     "cn=ldap-service,ou=users,dc=ldap,dc=goauthentik,dc=io",
		BindPassword: "test-password",
	}

	jellyfin := fixtureInstalledApp("jellyfin", "running")
	catalogApp := fixtureJellyfin()
	catalogApp.SSO = catalog.SSO{Strategy: "ldap"}

	tr.cache.On("Get", "jellyfin").Return(catalogApp, nil)

	state, err := tr.reconciler.buildAppState(jellyfin, map[string]*store.InstalledApp{
		"jellyfin": jellyfin,
	})

	require.NoError(t, err)
	assert.True(t, state.SSOEnabled)
	require.NotNil(t, state.LDAP)
	assert.Equal(t, "apps-authentik-ldap", state.LDAP.Host)
	assert.Equal(t, 3389, state.LDAP.Port)
	assert.Equal(t, "test-password", state.LDAP.BindPassword)
}

func TestBuildAppState_LDAPOutputNilWhenStrategyIsOIDC(t *testing.T) {
	tr := newTestReconcilerWithCache()
	tr.reconciler.config.LDAPOutput = &configurator.LDAPOutput{
		Host:         "apps-authentik-ldap",
		Port:         3389,
		BaseDN:       "dc=ldap,dc=goauthentik,dc=io",
		BindUser:     "cn=ldap-service,ou=users,dc=ldap,dc=goauthentik,dc=io",
		BindPassword: "test-password",
	}

	immich := fixtureInstalledApp("immich", "running")
	catalogApp := &catalog.App{
		Name: "immich",
		SSO:  catalog.SSO{Strategy: "native-oidc"},
	}

	tr.cache.On("Get", "immich").Return(catalogApp, nil)

	state, err := tr.reconciler.buildAppState(immich, map[string]*store.InstalledApp{
		"immich": immich,
	})

	require.NoError(t, err)
	assert.True(t, state.SSOEnabled)
	assert.Nil(t, state.LDAP, "LDAP output should be nil for OIDC strategy")
}

func TestBuildAppState_LDAPOutputNilWhenNoLDAPConfigured(t *testing.T) {
	tr := newTestReconcilerWithCache()
	// No LDAPOutput in ReconcileConfig (default nil)

	jellyfin := fixtureInstalledApp("jellyfin", "running")
	catalogApp := fixtureJellyfin()
	catalogApp.SSO = catalog.SSO{Strategy: "ldap"}

	tr.cache.On("Get", "jellyfin").Return(catalogApp, nil)

	state, err := tr.reconciler.buildAppState(jellyfin, map[string]*store.InstalledApp{
		"jellyfin": jellyfin,
	})

	require.NoError(t, err)
	assert.True(t, state.SSOEnabled)
	assert.Nil(t, state.LDAP, "LDAP output should be nil when ReconcileConfig has no LDAPOutput")
}

func integrationBinding(integrationType, provider string) integrationdomain.Bindings {
	return integrationdomain.Bindings{
		integrationdomain.Type(integrationType): integrationdomain.AppID(provider),
	}
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
