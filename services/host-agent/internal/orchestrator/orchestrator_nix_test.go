package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/nixgen"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
)

// newTestLogger creates a logger that doesn't output during tests
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
}

// testOrchestrator holds all mocks for easy access in tests
type testOrchestrator struct {
	orch            *Orchestrator
	graph           *MockAppGraph
	cache           *MockCatalogCache
	appStore        *MockAppStore
	generator       *MockNixGenerator
	rebuilder       *MockRebuilder
	traefikGen      *MockTraefikGenerator
	blueprintGen    *MockBlueprintGenerator
	authentikClient *MockAuthentikClient
}

// newTestOrchestrator creates a Orchestrator with mocked dependencies
func newTestOrchestratorWithMocks() *testOrchestrator {
	t := &testOrchestrator{
		graph:           new(MockAppGraph),
		cache:           new(MockCatalogCache),
		appStore:        new(MockAppStore),
		generator:       new(MockNixGenerator),
		rebuilder:       new(MockRebuilder),
		traefikGen:      new(MockTraefikGenerator),
		blueprintGen:    new(MockBlueprintGenerator),
		authentikClient: new(MockAuthentikClient),
	}

	t.orch = &Orchestrator{
		graph:           t.graph,
		catalogCache:    t.cache,
		appStore:        t.appStore,
		generator:       t.generator,
		rebuilder:       t.rebuilder,
		traefikGen:      t.traefikGen,
		blueprintGen:    t.blueprintGen,
		authentikClient: t.authentikClient,
		dataDir:         "/tmp/bloud-test",
		logger:          newTestLogger(),
	}

	return t
}

// setupSuccessfulInstall sets up mocks for a successful app installation
func (t *testOrchestrator) setupSuccessfulInstall(appName string, app *catalog.App) {
	plan := fixtureInstallPlanCanInstall(appName)

	t.graph.On("PlanInstall", appName).Return(plan, nil)
	t.graph.On("SetInstalled", mock.Anything).Return()

	t.cache.On("Get", appName).Return(app, nil)
	t.cache.On("GetAll").Return([]*catalog.App{app}, nil)

	t.generator.On("LoadCurrent").Return(fixtureEmptyTransaction(), nil)
	t.generator.On("Preview", mock.Anything).Return("preview")
	t.generator.On("Apply", mock.Anything).Return(nil)

	t.appStore.On("Install", appName, app.DisplayName, "", mock.Anything, mock.Anything).Return(nil)
	t.appStore.On("UpdateStatus", appName, "starting").Return(nil)
	t.appStore.On("GetInstalledNames").Return([]string{appName}, nil)

	t.rebuilder.On("Switch", mock.Anything).Return(fixtureRebuildSuccess(), nil)
	t.rebuilder.On("ReloadAndRestartApps", mock.Anything).Return(nil)

	t.traefikGen.On("SetAuthentikEnabled", false).Return()
	t.traefikGen.On("Generate", mock.Anything).Return(nil)
}

// setupSuccessfulUninstall sets up mocks for a successful app uninstallation
func (t *testOrchestrator) setupSuccessfulUninstall(appName string, app *catalog.App) {
	plan := fixtureRemovePlanCanRemove(appName)
	installedApp := fixtureInstalledApp(appName, "running")

	t.graph.On("PlanRemove", appName).Return(plan, nil)
	t.graph.On("SetInstalled", mock.Anything).Return()

	t.cache.On("Get", appName).Return(app, nil)
	t.cache.On("GetAll").Return([]*catalog.App{}, nil)

	t.appStore.On("GetByName", appName).Return(installedApp, nil)
	t.appStore.On("UpdateStatus", appName, "uninstalling").Return(nil)
	t.appStore.On("Uninstall", appName).Return(nil)
	t.appStore.On("GetInstalledNames").Return([]string{}, nil)

	t.generator.On("LoadCurrent").Return(fixtureTransactionWithApp(appName), nil)
	t.generator.On("Apply", mock.Anything).Return(nil)

	t.rebuilder.On("Switch", mock.Anything).Return(fixtureRebuildSuccess(), nil)
	t.rebuilder.On("StopUserService", mock.Anything, appName).Return(nil)

	t.traefikGen.On("SetAuthentikEnabled", false).Return()
	t.traefikGen.On("Generate", mock.Anything).Return(nil)

	// cleanupSSO always calls DeleteBlueprint, even for non-SSO apps
	t.blueprintGen.On("DeleteBlueprint", appName).Return(nil)
}

// ============================================================================
// Install Tests - Happy Path
// ============================================================================

func TestInstall_SimpleApp_NoIntegrations(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureQBittorrent()
	to.setupSuccessfulInstall("qbittorrent", app)

	ctx := context.Background()
	result, err := to.orch.Install(ctx, InstallRequest{App: "qbittorrent"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsSuccess())
	assert.Equal(t, "qbittorrent", result.GetApp())

	to.graph.AssertExpectations(t)
	to.appStore.AssertExpectations(t)
	to.generator.AssertExpectations(t)
	to.rebuilder.AssertExpectations(t)
}

func TestInstall_SSOBlueprintGenerated(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureMiniflux() // Has native-oidc SSO
	to.setupSuccessfulInstall("miniflux", app)

	// Blueprint should be generated for SSO apps
	to.blueprintGen.On("GenerateForApp", mock.MatchedBy(func(a *catalog.App) bool {
		return a.Name == "miniflux" && a.SSO.Strategy == "native-oidc"
	})).Return(nil)

	ctx := context.Background()
	result, err := to.orch.Install(ctx, InstallRequest{App: "miniflux"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsSuccess())

	to.blueprintGen.AssertCalled(t, "GenerateForApp", mock.Anything)
}

// ============================================================================
// Install Tests - Error Handling
// ============================================================================

func TestInstall_UnknownApp(t *testing.T) {
	to := newTestOrchestratorWithMocks()

	// PlanInstall returns error for unknown app
	to.graph.On("PlanInstall", "unknown-app").Return(nil, errors.New("unknown app: unknown-app"))

	ctx := context.Background()
	result, err := to.orch.Install(ctx, InstallRequest{App: "unknown-app"})

	require.NoError(t, err) // Error is in result, not returned
	require.NotNil(t, result)
	assert.False(t, result.IsSuccess())
	assert.Contains(t, result.GetError(), "failed to plan install")
}

func TestInstall_Blockers(t *testing.T) {
	to := newTestOrchestratorWithMocks()

	// App is blocked
	plan := fixtureInstallPlanBlocked("radarr", []string{"requires download-client"})
	to.graph.On("PlanInstall", "radarr").Return(plan, nil)

	ctx := context.Background()
	result, err := to.orch.Install(ctx, InstallRequest{App: "radarr"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsSuccess())
	assert.Contains(t, result.GetError(), "cannot install")
}

func TestInstall_TransactionBuildFails(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureQBittorrent()
	plan := fixtureInstallPlanCanInstall("qbittorrent")

	to.graph.On("PlanInstall", "qbittorrent").Return(plan, nil)
	to.graph.On("GetApps").Return(map[string]*catalog.AppDefinition{
		"qbittorrent": fixtureAppDefinition("qbittorrent"),
	})

	to.cache.On("Get", "qbittorrent").Return(app, nil)

	// LoadCurrent fails
	to.generator.On("LoadCurrent").Return(nil, errors.New("state file corrupted"))

	ctx := context.Background()
	result, err := to.orch.Install(ctx, InstallRequest{App: "qbittorrent"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsSuccess())
	assert.Contains(t, result.GetError(), "failed to build transaction")
}

func TestInstall_RebuildFails(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureQBittorrent()
	plan := fixtureInstallPlanCanInstall("qbittorrent")

	to.graph.On("PlanInstall", "qbittorrent").Return(plan, nil)
	to.graph.On("GetApps").Return(map[string]*catalog.AppDefinition{
		"qbittorrent": fixtureAppDefinition("qbittorrent"),
	})

	to.cache.On("Get", "qbittorrent").Return(app, nil)

	to.generator.On("LoadCurrent").Return(fixtureEmptyTransaction(), nil)
	to.generator.On("Preview", mock.Anything).Return("preview")
	to.generator.On("Apply", mock.Anything).Return(nil)

	to.appStore.On("Install", "qbittorrent", "qBittorrent", "", mock.Anything, mock.Anything).Return(nil)
	to.appStore.On("UpdateStatus", "qbittorrent", "failed").Return(nil) // Should mark as failed

	// Rebuild fails
	to.rebuilder.On("Switch", mock.Anything).Return(fixtureRebuildFailure("nix build failed"), nil)

	ctx := context.Background()
	result, err := to.orch.Install(ctx, InstallRequest{App: "qbittorrent"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsSuccess())

	// Verify status was updated to failed
	to.appStore.AssertCalled(t, "UpdateStatus", "qbittorrent", "failed")
}

func TestInstall_IntentRecordingFails(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureQBittorrent()
	plan := fixtureInstallPlanCanInstall("qbittorrent")

	to.graph.On("PlanInstall", "qbittorrent").Return(plan, nil)
	to.graph.On("GetApps").Return(map[string]*catalog.AppDefinition{
		"qbittorrent": fixtureAppDefinition("qbittorrent"),
	})

	to.cache.On("Get", "qbittorrent").Return(app, nil)

	to.generator.On("LoadCurrent").Return(fixtureEmptyTransaction(), nil)
	to.generator.On("Preview", mock.Anything).Return("preview")

	// Database write fails
	to.appStore.On("Install", "qbittorrent", "qBittorrent", "", mock.Anything, mock.Anything).Return(errors.New("database locked"))

	ctx := context.Background()
	result, err := to.orch.Install(ctx, InstallRequest{App: "qbittorrent"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsSuccess())
	assert.Contains(t, result.GetError(), "failed to record intent")
}

func TestInstall_SSOBlueprintFails_NonFatal(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureMiniflux()
	to.setupSuccessfulInstall("miniflux", app)

	// Blueprint generation fails - should NOT fail install
	to.blueprintGen.On("GenerateForApp", mock.Anything).Return(errors.New("authentik not available"))

	ctx := context.Background()
	result, err := to.orch.Install(ctx, InstallRequest{App: "miniflux"})

	// Install should still succeed despite blueprint failure
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsSuccess())
}

// ============================================================================
// Uninstall Tests
// ============================================================================

func TestUninstall_SimpleApp(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureQBittorrent()
	to.setupSuccessfulUninstall("qbittorrent", app)

	ctx := context.Background()
	result, err := to.orch.Uninstall(ctx, UninstallRequest{App: "qbittorrent"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsSuccess())
}

func TestUninstall_Blocked(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureQBittorrent()

	plan := fixtureRemovePlanBlocked("qbittorrent", []string{"radarr requires a download-client"})

	to.cache.On("Get", "qbittorrent").Return(app, nil)
	to.graph.On("PlanRemove", "qbittorrent").Return(plan, nil)

	ctx := context.Background()
	result, err := to.orch.Uninstall(ctx, UninstallRequest{App: "qbittorrent"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsSuccess())
	assert.Contains(t, result.GetError(), "cannot remove")
}

func TestUninstall_SSOCleanup(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureMiniflux() // Has native-oidc SSO
	to.setupSuccessfulUninstall("miniflux", app)

	// SSO cleanup via Authentik API (DeleteBlueprint is already set up in setupSuccessfulUninstall)
	to.authentikClient.On("DeleteAppSSO", "miniflux", "Miniflux", "native-oidc").Return(nil)

	ctx := context.Background()
	result, err := to.orch.Uninstall(ctx, UninstallRequest{App: "miniflux"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsSuccess())

	// Verify SSO cleanup was called
	to.blueprintGen.AssertCalled(t, "DeleteBlueprint", "miniflux")
	to.authentikClient.AssertCalled(t, "DeleteAppSSO", "miniflux", "Miniflux", "native-oidc")
}

// ============================================================================
// Helper method tests
// ============================================================================

func TestInstallResult_IsSuccess(t *testing.T) {
	tests := []struct {
		name     string
		result   *InstallResult
		expected bool
	}{
		{
			name:     "success",
			result:   &InstallResult{Success: true},
			expected: true,
		},
		{
			name:     "failure",
			result:   &InstallResult{Success: false, Error: "something failed"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.result.IsSuccess())
		})
	}
}

func TestInstallResult_GetApp(t *testing.T) {
	result := &InstallResult{App: "qbittorrent"}
	assert.Equal(t, "qbittorrent", result.GetApp())
}

func TestInstallResult_GetError(t *testing.T) {
	result := &InstallResult{Error: "build failed"}
	assert.Equal(t, "build failed", result.GetError())
}

// ============================================================================
// Deep Assertion Tests - Verify actual data passed to mocks
// ============================================================================

func TestInstall_TransactionStructure(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureQBittorrent()
	plan := fixtureInstallPlanCanInstall("qbittorrent")

	to.graph.On("PlanInstall", "qbittorrent").Return(plan, nil)
	to.graph.On("SetInstalled", mock.Anything).Return()

	to.cache.On("Get", "qbittorrent").Return(app, nil)
	to.cache.On("GetAll").Return([]*catalog.App{app}, nil)

	to.generator.On("LoadCurrent").Return(fixtureEmptyTransaction(), nil)
	to.generator.On("Preview", mock.Anything).Return("preview")

	// Capture the transaction for deep inspection
	var capturedTx interface{}
	to.generator.On("Apply", mock.Anything).Run(func(args mock.Arguments) {
		capturedTx = args.Get(0)
	}).Return(nil)

	to.appStore.On("Install", "qbittorrent", "qBittorrent", "", mock.Anything, mock.Anything).Return(nil)
	to.appStore.On("UpdateStatus", "qbittorrent", "starting").Return(nil)
	to.appStore.On("GetInstalledNames").Return([]string{"qbittorrent"}, nil)

	to.rebuilder.On("Switch", mock.Anything).Return(fixtureRebuildSuccess(), nil)
	to.rebuilder.On("ReloadAndRestartApps", mock.Anything).Return(nil)

	to.traefikGen.On("SetAuthentikEnabled", false).Return()
	to.traefikGen.On("Generate", mock.Anything).Return(nil)

	ctx := context.Background()
	result, err := to.orch.Install(ctx, InstallRequest{App: "qbittorrent"})

	require.NoError(t, err)
	require.True(t, result.IsSuccess())

	// Deep assertions on the captured transaction
	require.NotNil(t, capturedTx, "transaction should have been passed to Apply")

	// Type assert and verify structure
	tx := capturedTx.(*nixgen.Transaction)
	require.NotNil(t, tx.Apps)

	// Verify the app was added with correct config
	qbApp, exists := tx.Apps["qbittorrent"]
	require.True(t, exists, "qbittorrent should be in transaction")
	assert.True(t, qbApp.Enabled, "app should be enabled")
	assert.Equal(t, "qbittorrent", qbApp.Name)
}

func TestInstall_WithDependency_TransactionIncludesBoth(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureMiniflux()

	// Plan includes auto-config for postgres dependency
	plan := &catalog.InstallPlan{
		App:        "miniflux",
		CanInstall: true,
		AutoConfig: []catalog.ConfigTask{
			{Target: "miniflux", Integration: "database", Source: "postgres"},
		},
	}

	to.graph.On("PlanInstall", "miniflux").Return(plan, nil)
	to.graph.On("SetInstalled", mock.Anything).Return()

	to.cache.On("Get", "miniflux").Return(app, nil)
	to.cache.On("Get", "postgres").Return(&catalog.App{Name: "postgres", DisplayName: "PostgreSQL", Port: 5432, IsSystem: true}, nil)
	to.cache.On("GetAll").Return([]*catalog.App{app}, nil)

	to.generator.On("LoadCurrent").Return(fixtureEmptyTransaction(), nil)
	to.generator.On("Preview", mock.Anything).Return("preview")

	// Capture the transaction
	var capturedTx *nixgen.Transaction
	to.generator.On("Apply", mock.Anything).Run(func(args mock.Arguments) {
		capturedTx = args.Get(0).(*nixgen.Transaction)
	}).Return(nil)

	to.appStore.On("Install", "miniflux", "Miniflux", "", mock.Anything, mock.Anything).Return(nil)
	to.appStore.On("GetByName", "postgres").Return(nil, nil) // Not yet installed
	to.appStore.On("Install", "postgres", "PostgreSQL", "", mock.Anything, mock.Anything).Return(nil)
	to.appStore.On("UpdateStatus", "miniflux", "starting").Return(nil)
	to.appStore.On("UpdateStatus", "postgres", "starting").Return(nil)
	to.appStore.On("GetInstalledNames").Return([]string{"miniflux", "postgres"}, nil)

	to.rebuilder.On("Switch", mock.Anything).Return(fixtureRebuildSuccess(), nil)
	to.rebuilder.On("ReloadAndRestartApps", mock.Anything).Return(nil)

	to.traefikGen.On("SetAuthentikEnabled", false).Return()
	to.traefikGen.On("Generate", mock.Anything).Return(nil)

	to.blueprintGen.On("GenerateForApp", mock.Anything).Return(nil)

	ctx := context.Background()
	result, err := to.orch.Install(ctx, InstallRequest{App: "miniflux"})

	require.NoError(t, err)
	require.True(t, result.IsSuccess())

	// Verify transaction includes both apps
	require.NotNil(t, capturedTx)

	// Main app should have integration configured
	miniflux, exists := capturedTx.Apps["miniflux"]
	require.True(t, exists)
	assert.True(t, miniflux.Enabled)
	assert.Equal(t, "postgres", miniflux.Integrations["database"])

	// Dependency should be enabled
	postgres, exists := capturedTx.Apps["postgres"]
	require.True(t, exists)
	assert.True(t, postgres.Enabled)
}

func TestInstall_InstallOptionsPassedCorrectly(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureQBittorrent()
	plan := fixtureInstallPlanCanInstall("qbittorrent")

	to.graph.On("PlanInstall", "qbittorrent").Return(plan, nil)
	to.graph.On("SetInstalled", mock.Anything).Return()

	to.cache.On("Get", "qbittorrent").Return(app, nil)
	to.cache.On("GetAll").Return([]*catalog.App{app}, nil)

	to.generator.On("LoadCurrent").Return(fixtureEmptyTransaction(), nil)
	to.generator.On("Preview", mock.Anything).Return("preview")
	to.generator.On("Apply", mock.Anything).Return(nil)

	// Capture install options to verify port and system flag
	var capturedPort int
	var capturedOpts interface{}
	to.appStore.On("Install", "qbittorrent", "qBittorrent", "", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		capturedOpts = args.Get(4) // 5th argument (index 4) is InstallOptions
	}).Return(nil)
	to.appStore.On("UpdateStatus", "qbittorrent", "starting").Return(nil)
	to.appStore.On("GetInstalledNames").Return([]string{"qbittorrent"}, nil)

	to.rebuilder.On("Switch", mock.Anything).Return(fixtureRebuildSuccess(), nil)
	to.rebuilder.On("ReloadAndRestartApps", mock.Anything).Return(nil)

	to.traefikGen.On("SetAuthentikEnabled", false).Return()
	to.traefikGen.On("Generate", mock.Anything).Return(nil)

	ctx := context.Background()
	_, err := to.orch.Install(ctx, InstallRequest{App: "qbittorrent"})

	require.NoError(t, err)

	// Verify install options contain correct port from catalog
	require.NotNil(t, capturedOpts)
	opts := capturedOpts.(*store.InstallOptions)
	capturedPort = opts.Port
	assert.Equal(t, 8180, capturedPort, "port should come from catalog")
}

func TestUninstall_TransactionDisablesApp(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureQBittorrent()
	plan := fixtureRemovePlanCanRemove("qbittorrent")
	installedApp := fixtureInstalledApp("qbittorrent", "running")

	to.graph.On("PlanRemove", "qbittorrent").Return(plan, nil)
	to.graph.On("SetInstalled", mock.Anything).Return()

	to.cache.On("Get", "qbittorrent").Return(app, nil)
	to.cache.On("GetAll").Return([]*catalog.App{}, nil)

	to.appStore.On("GetByName", "qbittorrent").Return(installedApp, nil)
	to.appStore.On("UpdateStatus", "qbittorrent", "uninstalling").Return(nil)
	to.appStore.On("Uninstall", "qbittorrent").Return(nil)
	to.appStore.On("GetInstalledNames").Return([]string{}, nil)

	// Start with app enabled
	currentTx := fixtureTransactionWithApp("qbittorrent")
	to.generator.On("LoadCurrent").Return(currentTx, nil)

	// Capture the transaction to verify app is disabled
	var capturedTx *nixgen.Transaction
	to.generator.On("Apply", mock.Anything).Run(func(args mock.Arguments) {
		capturedTx = args.Get(0).(*nixgen.Transaction)
	}).Return(nil)

	to.rebuilder.On("Switch", mock.Anything).Return(fixtureRebuildSuccess(), nil)
	to.rebuilder.On("StopUserService", mock.Anything, "qbittorrent").Return(nil)

	to.traefikGen.On("SetAuthentikEnabled", false).Return()
	to.traefikGen.On("Generate", mock.Anything).Return(nil)

	to.blueprintGen.On("DeleteBlueprint", "qbittorrent").Return(nil)

	ctx := context.Background()
	result, err := to.orch.Uninstall(ctx, UninstallRequest{App: "qbittorrent"})

	require.NoError(t, err)
	require.True(t, result.IsSuccess())

	// Verify the app is disabled in the transaction
	require.NotNil(t, capturedTx)
	qbApp, exists := capturedTx.Apps["qbittorrent"]
	require.True(t, exists)
	assert.False(t, qbApp.Enabled, "app should be disabled in uninstall transaction")
}

func TestInstall_TraefikRoutesIncludeAllInstalledApps(t *testing.T) {
	to := newTestOrchestratorWithMocks()
	app := fixtureQBittorrent()
	plan := fixtureInstallPlanCanInstall("qbittorrent")

	to.graph.On("PlanInstall", "qbittorrent").Return(plan, nil)
	to.graph.On("SetInstalled", mock.Anything).Return()

	to.cache.On("Get", "qbittorrent").Return(app, nil)
	to.cache.On("GetAll").Return([]*catalog.App{app}, nil)

	to.generator.On("LoadCurrent").Return(fixtureEmptyTransaction(), nil)
	to.generator.On("Preview", mock.Anything).Return("preview")
	to.generator.On("Apply", mock.Anything).Return(nil)

	to.appStore.On("Install", "qbittorrent", "qBittorrent", "", mock.Anything, mock.Anything).Return(nil)
	to.appStore.On("UpdateStatus", "qbittorrent", "starting").Return(nil)
	to.appStore.On("GetInstalledNames").Return([]string{"qbittorrent"}, nil)

	to.rebuilder.On("Switch", mock.Anything).Return(fixtureRebuildSuccess(), nil)
	to.rebuilder.On("ReloadAndRestartApps", mock.Anything).Return(nil)

	to.traefikGen.On("SetAuthentikEnabled", false).Return()

	// Capture the apps passed to Traefik generator
	var capturedApps []*catalog.App
	to.traefikGen.On("Generate", mock.Anything).Run(func(args mock.Arguments) {
		capturedApps = args.Get(0).([]*catalog.App)
	}).Return(nil)

	ctx := context.Background()
	result, err := to.orch.Install(ctx, InstallRequest{App: "qbittorrent"})

	require.NoError(t, err)
	require.True(t, result.IsSuccess())

	// Verify Traefik received the correct apps
	require.NotNil(t, capturedApps)
	require.Len(t, capturedApps, 1)
	assert.Equal(t, "qbittorrent", capturedApps[0].Name)
}

// ============================================================================
// RegenerateRoutes Tests
// ============================================================================

func TestRegenerateRoutes_NoInstalledApps(t *testing.T) {
	to := newTestOrchestratorWithMocks()

	to.appStore.On("GetInstalledNames").Return([]string{}, nil)
	to.traefikGen.On("SetAuthentikEnabled", false).Return()
	to.traefikGen.On("Generate", mock.Anything).Return(nil)

	err := to.orch.RegenerateRoutes()

	require.NoError(t, err)
	to.traefikGen.AssertCalled(t, "SetAuthentikEnabled", false)
}

func TestRegenerateRoutes_WithAuthentik(t *testing.T) {
	to := newTestOrchestratorWithMocks()

	to.appStore.On("GetInstalledNames").Return([]string{"authentik", "miniflux"}, nil)
	to.cache.On("Get", "authentik").Return(&catalog.App{Name: "authentik", Port: 9000}, nil)
	to.cache.On("Get", "miniflux").Return(fixtureMiniflux(), nil)
	to.traefikGen.On("SetAuthentikEnabled", true).Return()
	to.traefikGen.On("Generate", mock.Anything).Return(nil)

	err := to.orch.RegenerateRoutes()

	require.NoError(t, err)
	// Should enable authentik since it's installed
	to.traefikGen.AssertCalled(t, "SetAuthentikEnabled", true)
}

func TestRegenerateRoutes_AppStoreError(t *testing.T) {
	to := newTestOrchestratorWithMocks()

	to.appStore.On("GetInstalledNames").Return(nil, errors.New("database error"))

	err := to.orch.RegenerateRoutes()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "database error")
}

func TestRegenerateRoutes_GenerateFails(t *testing.T) {
	to := newTestOrchestratorWithMocks()

	to.appStore.On("GetInstalledNames").Return([]string{"qbittorrent"}, nil)
	to.cache.On("Get", "qbittorrent").Return(fixtureQBittorrent(), nil)
	to.traefikGen.On("SetAuthentikEnabled", false).Return()
	to.traefikGen.On("Generate", mock.Anything).Return(errors.New("traefik error"))

	err := to.orch.RegenerateRoutes()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "traefik error")
}

// ============================================================================
// RecheckFailedApps Tests
// ============================================================================

func TestRecheckFailedApps_NoApps(t *testing.T) {
	to := newTestOrchestratorWithMocks()

	to.appStore.On("GetAll").Return([]*store.InstalledApp{}, nil)

	// Should not panic
	to.orch.RecheckFailedApps()

	to.appStore.AssertCalled(t, "GetAll")
}

func TestRecheckFailedApps_NoFailedApps(t *testing.T) {
	to := newTestOrchestratorWithMocks()

	apps := []*store.InstalledApp{
		{Name: "qbittorrent", Status: "running"},
		{Name: "miniflux", Status: "starting"},
	}
	to.appStore.On("GetAll").Return(apps, nil)

	to.orch.RecheckFailedApps()

	// No health checks should start since no apps are failed
	to.appStore.AssertCalled(t, "GetAll")
}

func TestRecheckFailedApps_AppStoreError(t *testing.T) {
	to := newTestOrchestratorWithMocks()

	to.appStore.On("GetAll").Return(nil, errors.New("database error"))

	// Should not panic
	to.orch.RecheckFailedApps()
}
