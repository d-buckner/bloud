package orchestrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/nixgen"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/testdb"
)

// Integration tests use real PostgreSQL for the app store and fakes for external systems.
// This tests actual orchestrator behavior with real database persistence.

// integrationHarness provides a complete test environment
type integrationHarness struct {
	orch         *Orchestrator
	appStore     *store.AppStore
	db           *sql.DB
	graph        *FakeAppGraph
	cache        *FakeCatalogCache
	generator    *FakeGenerator
	rebuilder    *FakeRebuilder
	traefikGen   *FakeTraefikGenerator
	blueprintGen *FakeBlueprintGenerator
	authentik    *FakeAuthentikClient
	tempDir      string
}

func newIntegrationHarness(t *testing.T) *integrationHarness {
	tmpDir := t.TempDir()

	db := testdb.SetupTestDB(t)
	appStore := store.NewAppStore(db)

	h := &integrationHarness{
		appStore:     appStore,
		db:           db,
		graph:        NewFakeAppGraph(),
		cache:        NewFakeCatalogCache(),
		generator:    NewFakeGenerator(),
		rebuilder:    NewFakeRebuilder(),
		traefikGen:   NewFakeTraefikGenerator(),
		blueprintGen: NewFakeBlueprintGenerator(),
		authentik:    NewFakeAuthentikClient(),
		tempDir:      tmpDir,
	}

	// Create orchestrator with real app store and fakes for external systems
	h.orch = &Orchestrator{
		graph:           h.graph,
		catalogCache:    h.cache,
		appStore:        h.appStore,
		generator:       h.generator,
		rebuilder:       h.rebuilder,
		traefikGen:      h.traefikGen,
		blueprintGen:    h.blueprintGen,
		authentikClient: h.authentik,
		dataDir:         tmpDir,
		logger:          newTestLogger(),
	}

	return h
}

func (h *integrationHarness) Close() {
	// Database connection is cleaned up by testdb
}

// ============================================================================
// Install Integration Tests
// ============================================================================

func TestIntegration_Install_CreatesDBRecord(t *testing.T) {
	h := newIntegrationHarness(t)
	defer h.Close()

	// Setup catalog
	h.cache.AddApp(&catalog.App{
		Name:        "qbittorrent",
		DisplayName: "qBittorrent",
		Port:        8180,
	})

	// Install
	ctx := context.Background()
	result, err := h.orch.Install(ctx, InstallRequest{App: "qbittorrent"})

	require.NoError(t, err)
	require.True(t, result.IsSuccess())

	// Verify database record was created
	app, err := h.appStore.GetByName("qbittorrent")
	require.NoError(t, err)
	require.NotNil(t, app, "app should exist in database")

	assert.Equal(t, "qbittorrent", app.Name)
	assert.Equal(t, 8180, app.Port)
	assert.Equal(t, "starting", app.Status) // Status after successful rebuild
}

func TestIntegration_Install_TransactionContent(t *testing.T) {
	h := newIntegrationHarness(t)
	defer h.Close()

	h.cache.AddApp(&catalog.App{
		Name:        "miniflux",
		DisplayName: "Miniflux",
		Port:        8280,
	})

	// Set up install plan with auto-config
	h.graph.SetInstallPlan("miniflux", &catalog.InstallPlan{
		App:        "miniflux",
		CanInstall: true,
		AutoConfig: []catalog.ConfigTask{
			{Target: "miniflux", Integration: "database", Source: "postgres"},
		},
	})

	ctx := context.Background()
	result, err := h.orch.Install(ctx, InstallRequest{App: "miniflux"})

	require.NoError(t, err)
	require.True(t, result.IsSuccess())

	// Verify transaction was applied with correct content
	tx := h.generator.LastTransaction()
	require.NotNil(t, tx, "transaction should have been applied")

	// Main app should be enabled
	miniflux := tx.Apps["miniflux"]
	assert.True(t, miniflux.Enabled)
	assert.Equal(t, "postgres", miniflux.Integrations["database"])

	// Dependency should also be enabled
	postgres := tx.Apps["postgres"]
	assert.True(t, postgres.Enabled)
}

func TestIntegration_Install_GraphUpdated(t *testing.T) {
	h := newIntegrationHarness(t)
	defer h.Close()

	h.cache.AddApp(&catalog.App{Name: "qbittorrent"})

	ctx := context.Background()
	_, err := h.orch.Install(ctx, InstallRequest{App: "qbittorrent"})
	require.NoError(t, err)

	// Verify graph was updated with installed apps
	installedApps := h.graph.InstalledApps()
	assert.Contains(t, installedApps, "qbittorrent")
}

func TestIntegration_Install_TraefikRegenerated(t *testing.T) {
	h := newIntegrationHarness(t)
	defer h.Close()

	h.cache.AddApp(&catalog.App{
		Name:        "miniflux",
		DisplayName: "Miniflux",
		Port:        8280,
	})

	ctx := context.Background()
	_, err := h.orch.Install(ctx, InstallRequest{App: "miniflux"})
	require.NoError(t, err)

	// Verify Traefik routes were regenerated
	generatedApps := h.traefikGen.LastGeneratedApps()
	require.NotNil(t, generatedApps, "traefik should regenerate routes")

	var found bool
	for _, app := range generatedApps {
		if app.Name == "miniflux" {
			found = true
			break
		}
	}
	assert.True(t, found, "miniflux should be in generated apps")
}

func TestIntegration_Install_SSOBlueprintGenerated(t *testing.T) {
	h := newIntegrationHarness(t)
	defer h.Close()

	h.cache.AddApp(&catalog.App{
		Name:        "miniflux",
		DisplayName: "Miniflux",
		Port:        8280,
		SSO: catalog.SSO{
			Strategy:     "native-oidc",
			CallbackPath: "/oauth2/callback",
		},
	})

	ctx := context.Background()
	_, err := h.orch.Install(ctx, InstallRequest{App: "miniflux"})
	require.NoError(t, err)

	// Verify SSO blueprint was generated
	generatedApps := h.blueprintGen.GeneratedApps()
	require.Len(t, generatedApps, 1)
	assert.Equal(t, "miniflux", generatedApps[0].Name)
	assert.Equal(t, "native-oidc", generatedApps[0].SSO.Strategy)
}

func TestIntegration_Install_WithDependencies_BothRecorded(t *testing.T) {
	h := newIntegrationHarness(t)
	defer h.Close()

	h.cache.AddApp(&catalog.App{Name: "postgres", Port: 5432, IsSystem: true})
	h.cache.AddApp(&catalog.App{Name: "miniflux", Port: 8280})

	h.graph.SetInstallPlan("miniflux", &catalog.InstallPlan{
		App:        "miniflux",
		CanInstall: true,
		AutoConfig: []catalog.ConfigTask{
			{Target: "miniflux", Integration: "database", Source: "postgres"},
		},
	})

	ctx := context.Background()
	result, err := h.orch.Install(ctx, InstallRequest{App: "miniflux"})
	require.NoError(t, err)
	require.True(t, result.IsSuccess())

	// Both apps should be in database
	miniflux, err := h.appStore.GetByName("miniflux")
	require.NoError(t, err)
	require.NotNil(t, miniflux)
	assert.Equal(t, "postgres", miniflux.IntegrationConfig["database"])

	postgres, err := h.appStore.GetByName("postgres")
	require.NoError(t, err)
	require.NotNil(t, postgres)
	assert.True(t, postgres.IsSystem)
}

func TestIntegration_Install_RebuildFails_StatusUpdatedToFailed(t *testing.T) {
	h := newIntegrationHarness(t)
	defer h.Close()

	h.cache.AddApp(&catalog.App{Name: "qbittorrent", Port: 8180})

	// Make rebuild fail
	h.rebuilder.SetResult(&nixgen.RebuildResult{
		Success:      false,
		ErrorMessage: "nix build failed",
	})

	ctx := context.Background()
	result, err := h.orch.Install(ctx, InstallRequest{App: "qbittorrent"})
	require.NoError(t, err)
	assert.False(t, result.IsSuccess())

	// Verify status was updated to failed
	app, err := h.appStore.GetByName("qbittorrent")
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.Equal(t, "failed", app.Status)
}

// ============================================================================
// Uninstall Integration Tests
// ============================================================================

func TestIntegration_Uninstall_RemovesDBRecord(t *testing.T) {
	h := newIntegrationHarness(t)
	defer h.Close()

	h.cache.AddApp(&catalog.App{Name: "qbittorrent"})

	// First install
	ctx := context.Background()
	_, err := h.orch.Install(ctx, InstallRequest{App: "qbittorrent"})
	require.NoError(t, err)

	// Verify it exists
	app, err := h.appStore.GetByName("qbittorrent")
	require.NoError(t, err)
	require.NotNil(t, app)

	// Uninstall
	result, err := h.orch.Uninstall(ctx, UninstallRequest{App: "qbittorrent"})
	require.NoError(t, err)
	require.True(t, result.IsSuccess())

	// Verify it's gone
	app, err = h.appStore.GetByName("qbittorrent")
	require.NoError(t, err)
	assert.Nil(t, app, "app should be removed from database")
}

func TestIntegration_Uninstall_TransactionDisablesApp(t *testing.T) {
	h := newIntegrationHarness(t)
	defer h.Close()

	h.cache.AddApp(&catalog.App{Name: "qbittorrent"})

	// Set up initial state with app enabled
	h.generator.SetCurrentState(&nixgen.Transaction{
		Apps: map[string]nixgen.AppConfig{
			"qbittorrent": {Name: "qbittorrent", Enabled: true},
		},
	})

	// Install to create DB record
	ctx := context.Background()
	_, err := h.orch.Install(ctx, InstallRequest{App: "qbittorrent"})
	require.NoError(t, err)

	// Uninstall
	result, err := h.orch.Uninstall(ctx, UninstallRequest{App: "qbittorrent"})
	require.NoError(t, err)
	require.True(t, result.IsSuccess())

	// Verify transaction disabled the app
	tx := h.generator.LastTransaction()
	require.NotNil(t, tx)

	qb := tx.Apps["qbittorrent"]
	assert.False(t, qb.Enabled, "app should be disabled in transaction")
}

func TestIntegration_Uninstall_SSOCleanedUp(t *testing.T) {
	h := newIntegrationHarness(t)
	defer h.Close()

	h.cache.AddApp(&catalog.App{
		Name:        "miniflux",
		DisplayName: "Miniflux",
		SSO: catalog.SSO{
			Strategy: "native-oidc",
		},
	})

	// Set up initial state
	h.generator.SetCurrentState(&nixgen.Transaction{
		Apps: map[string]nixgen.AppConfig{
			"miniflux": {Name: "miniflux", Enabled: true},
		},
	})

	// Install to create DB record
	ctx := context.Background()
	_, err := h.orch.Install(ctx, InstallRequest{App: "miniflux"})
	require.NoError(t, err)

	// Uninstall
	result, err := h.orch.Uninstall(ctx, UninstallRequest{App: "miniflux"})
	require.NoError(t, err)
	require.True(t, result.IsSuccess())

	// Verify SSO was cleaned up
	deletedBlueprints := h.blueprintGen.DeletedBlueprints()
	assert.Contains(t, deletedBlueprints, "miniflux")

	deletedApps := h.authentik.DeletedApps()
	assert.Contains(t, deletedApps, "miniflux")
}

// ============================================================================
// Status Transition Tests
// ============================================================================

func TestIntegration_StatusTransition_InstallToStarting(t *testing.T) {
	h := newIntegrationHarness(t)
	defer h.Close()

	h.cache.AddApp(&catalog.App{Name: "qbittorrent", Port: 8180})

	ctx := context.Background()
	result, err := h.orch.Install(ctx, InstallRequest{App: "qbittorrent"})
	require.NoError(t, err)
	require.True(t, result.IsSuccess())

	// After successful install, status should be "starting"
	app, err := h.appStore.GetByName("qbittorrent")
	require.NoError(t, err)
	assert.Equal(t, "starting", app.Status)
}

func TestIntegration_StatusTransition_UninstallToRemoved(t *testing.T) {
	h := newIntegrationHarness(t)
	defer h.Close()

	h.cache.AddApp(&catalog.App{Name: "qbittorrent"})
	h.generator.SetCurrentState(&nixgen.Transaction{
		Apps: map[string]nixgen.AppConfig{
			"qbittorrent": {Name: "qbittorrent", Enabled: true},
		},
	})

	// Install first
	ctx := context.Background()
	_, err := h.orch.Install(ctx, InstallRequest{App: "qbittorrent"})
	require.NoError(t, err)

	// Uninstall
	result, err := h.orch.Uninstall(ctx, UninstallRequest{App: "qbittorrent"})
	require.NoError(t, err)
	require.True(t, result.IsSuccess())

	// App should be completely gone
	app, err := h.appStore.GetByName("qbittorrent")
	require.NoError(t, err)
	assert.Nil(t, app, "app should be removed after uninstall")
}
