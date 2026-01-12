package orchestrator

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// testReconciler holds mocks for reconciler tests
type testReconciler struct {
	reconciler *Reconciler
	registry   *MockConfiguratorRegistry
	appStore   *MockAppStore
}

// newTestReconciler creates a Reconciler with mocked dependencies
func newTestReconciler() *testReconciler {
	t := &testReconciler{
		registry: new(MockConfiguratorRegistry),
		appStore: new(MockAppStore),
	}

	t.reconciler = NewReconciler(
		t.registry,
		t.appStore,
		"/tmp/bloud-test",
		newTestLogger(),
		ReconcileConfig{
			WatchdogInterval:   100 * time.Millisecond, // Fast for tests
			HealthCheckTimeout: 100 * time.Millisecond,
		},
	)

	return t
}

// ============================================================================
// computeLevels Tests
// ============================================================================

func TestComputeLevels_NoDependencies(t *testing.T) {
	tr := newTestReconciler()

	apps := map[string]*store.InstalledApp{
		"qbittorrent":  fixtureInstalledApp("qbittorrent", "running"),
		"adguard-home": fixtureInstalledApp("adguard-home", "running"),
		"miniflux":     fixtureInstalledApp("miniflux", "running"),
	}

	levels := tr.reconciler.computeLevels(apps)

	// All apps should be at level 0 since none have dependencies
	require.Len(t, levels, 1)
	assert.Len(t, levels[0], 3)

	// Sort for consistent comparison
	sort.Strings(levels[0])
	assert.Equal(t, []string{"adguard-home", "miniflux", "qbittorrent"}, levels[0])
}

func TestComputeLevels_LinearChain(t *testing.T) {
	tr := newTestReconciler()

	// Chain: postgres -> miniflux -> (no dependents)
	apps := map[string]*store.InstalledApp{
		"postgres": fixtureInstalledApp("postgres", "running"),
		"miniflux": fixtureInstalledAppWithIntegrations("miniflux", "running", map[string]string{
			"database": "postgres",
		}),
	}

	levels := tr.reconciler.computeLevels(apps)

	require.Len(t, levels, 2)
	assert.Equal(t, []string{"postgres"}, levels[0])
	assert.Equal(t, []string{"miniflux"}, levels[1])
}

func TestComputeLevels_DiamondDependency(t *testing.T) {
	tr := newTestReconciler()

	// Diamond: postgres is dep for both miniflux and actual-budget
	apps := map[string]*store.InstalledApp{
		"postgres": fixtureInstalledApp("postgres", "running"),
		"miniflux": fixtureInstalledAppWithIntegrations("miniflux", "running", map[string]string{
			"database": "postgres",
		}),
		"actual-budget": fixtureInstalledAppWithIntegrations("actual-budget", "running", map[string]string{
			"database": "postgres",
		}),
	}

	levels := tr.reconciler.computeLevels(apps)

	require.Len(t, levels, 2)
	assert.Equal(t, []string{"postgres"}, levels[0])

	// Both should be at level 1
	sort.Strings(levels[1])
	assert.Equal(t, []string{"actual-budget", "miniflux"}, levels[1])
}

func TestComputeLevels_MixedDeps(t *testing.T) {
	tr := newTestReconciler()

	// Mixed: qbittorrent (L0), radarr depends on qbittorrent (L1)
	apps := map[string]*store.InstalledApp{
		"qbittorrent": fixtureInstalledApp("qbittorrent", "running"),
		"radarr": fixtureInstalledAppWithIntegrations("radarr", "running", map[string]string{
			"download-client": "qbittorrent",
		}),
		"postgres": fixtureInstalledApp("postgres", "running"),
		"miniflux": fixtureInstalledAppWithIntegrations("miniflux", "running", map[string]string{
			"database": "postgres",
		}),
	}

	levels := tr.reconciler.computeLevels(apps)

	require.Len(t, levels, 2)

	// Level 0: apps with no deps
	sort.Strings(levels[0])
	assert.Equal(t, []string{"postgres", "qbittorrent"}, levels[0])

	// Level 1: apps that depend on level 0
	sort.Strings(levels[1])
	assert.Equal(t, []string{"miniflux", "radarr"}, levels[1])
}

func TestComputeLevels_UninstalledDepsIgnored(t *testing.T) {
	tr := newTestReconciler()

	// Radarr depends on qbittorrent, but qbittorrent isn't in the installed map
	apps := map[string]*store.InstalledApp{
		"radarr": fixtureInstalledAppWithIntegrations("radarr", "running", map[string]string{
			"download-client": "qbittorrent", // Not installed
		}),
	}

	levels := tr.reconciler.computeLevels(apps)

	// Radarr should be at level 0 since its dependency isn't installed
	require.Len(t, levels, 1)
	assert.Equal(t, []string{"radarr"}, levels[0])
}

// ============================================================================
// Reconcile Full Cycle Tests
// ============================================================================

func TestReconcile_EmptyApps(t *testing.T) {
	tr := newTestReconciler()

	tr.appStore.On("GetAll").Return([]*store.InstalledApp{}, nil)

	err := tr.reconciler.Reconcile(context.Background())

	require.NoError(t, err)
	tr.appStore.AssertExpectations(t)
}

func TestReconcile_SingleApp_NoConfigurator(t *testing.T) {
	tr := newTestReconciler()

	apps := []*store.InstalledApp{
		fixtureInstalledApp("qbittorrent", "running"),
	}

	tr.appStore.On("GetAll").Return(apps, nil)
	tr.registry.On("Get", "qbittorrent").Return(nil) // No configurator

	err := tr.reconciler.Reconcile(context.Background())

	require.NoError(t, err)
	tr.appStore.AssertExpectations(t)
	tr.registry.AssertExpectations(t)
}

func TestReconcile_SingleApp_WithConfigurator(t *testing.T) {
	tr := newTestReconciler()
	mockCfg := new(MockConfigurator)

	apps := []*store.InstalledApp{
		fixtureInstalledAppWithPort("qbittorrent", "running", 8180),
	}

	tr.appStore.On("GetAll").Return(apps, nil)
	tr.registry.On("Get", "qbittorrent").Return(mockCfg)

	// Phase 1: PreStart
	mockCfg.On("PreStart", mock.Anything, mock.MatchedBy(func(s *configurator.AppState) bool {
		return s.Name == "qbittorrent" && s.Port == 8180
	})).Return(nil)

	// Phase 2: HealthCheck
	mockCfg.On("HealthCheck", mock.Anything).Return(nil)

	// Phase 3: PostStart
	mockCfg.On("PostStart", mock.Anything, mock.MatchedBy(func(s *configurator.AppState) bool {
		return s.Name == "qbittorrent"
	})).Return(nil)

	err := tr.reconciler.Reconcile(context.Background())

	require.NoError(t, err)
	mockCfg.AssertExpectations(t)
}

func TestReconcile_MultiLevel_CorrectOrder(t *testing.T) {
	tr := newTestReconciler()
	mockPostgresCfg := new(MockConfigurator)
	mockMinifluxCfg := new(MockConfigurator)

	// Miniflux depends on postgres
	apps := []*store.InstalledApp{
		fixtureInstalledApp("postgres", "running"),
		fixtureInstalledAppWithIntegrations("miniflux", "running", map[string]string{
			"database": "postgres",
		}),
	}

	tr.appStore.On("GetAll").Return(apps, nil)
	tr.registry.On("Get", "postgres").Return(mockPostgresCfg)
	tr.registry.On("Get", "miniflux").Return(mockMinifluxCfg)

	// Track call order
	var callOrder []string

	// Postgres PreStart (Phase 1)
	mockPostgresCfg.On("PreStart", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		callOrder = append(callOrder, "postgres-prestart")
	}).Return(nil)

	// Miniflux PreStart (Phase 1)
	mockMinifluxCfg.On("PreStart", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		callOrder = append(callOrder, "miniflux-prestart")
	}).Return(nil)

	// Postgres HealthCheck + PostStart (Level 0)
	mockPostgresCfg.On("HealthCheck", mock.Anything).Run(func(args mock.Arguments) {
		callOrder = append(callOrder, "postgres-healthcheck")
	}).Return(nil)
	mockPostgresCfg.On("PostStart", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		callOrder = append(callOrder, "postgres-poststart")
	}).Return(nil)

	// Miniflux HealthCheck + PostStart (Level 1)
	mockMinifluxCfg.On("HealthCheck", mock.Anything).Run(func(args mock.Arguments) {
		callOrder = append(callOrder, "miniflux-healthcheck")
	}).Return(nil)
	mockMinifluxCfg.On("PostStart", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		callOrder = append(callOrder, "miniflux-poststart")
	}).Return(nil)

	err := tr.reconciler.Reconcile(context.Background())

	require.NoError(t, err)

	// Verify postgres health+post happen before miniflux health+post
	postgresHealthIdx := indexOf(callOrder, "postgres-healthcheck")
	minifluxHealthIdx := indexOf(callOrder, "miniflux-healthcheck")
	assert.True(t, postgresHealthIdx < minifluxHealthIdx,
		"postgres should be health-checked before miniflux, got order: %v", callOrder)
}

func TestReconcile_UninstallingSkipped(t *testing.T) {
	tr := newTestReconciler()
	mockCfg := new(MockConfigurator)

	apps := []*store.InstalledApp{
		fixtureInstalledApp("qbittorrent", "running"),
		fixtureInstalledApp("radarr", "uninstalling"), // Should be skipped
	}

	tr.appStore.On("GetAll").Return(apps, nil)
	tr.registry.On("Get", "qbittorrent").Return(mockCfg)
	// Note: registry.Get for "radarr" should NOT be called

	mockCfg.On("PreStart", mock.Anything, mock.Anything).Return(nil)
	mockCfg.On("HealthCheck", mock.Anything).Return(nil)
	mockCfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	err := tr.reconciler.Reconcile(context.Background())

	require.NoError(t, err)

	// Verify radarr was never looked up
	tr.registry.AssertNotCalled(t, "Get", "radarr")
}

func TestReconcile_PreStartFails(t *testing.T) {
	tr := newTestReconciler()
	mockCfg := new(MockConfigurator)

	apps := []*store.InstalledApp{
		fixtureInstalledApp("qbittorrent", "running"),
	}

	tr.appStore.On("GetAll").Return(apps, nil)
	tr.registry.On("Get", "qbittorrent").Return(mockCfg)

	// PreStart fails
	mockCfg.On("PreStart", mock.Anything, mock.Anything).Return(errors.New("config error"))

	// HealthCheck and PostStart should still be called (PreStart failure is logged, not fatal)
	mockCfg.On("HealthCheck", mock.Anything).Return(nil)
	mockCfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	err := tr.reconciler.Reconcile(context.Background())

	// Reconcile doesn't return error for individual app failures
	require.NoError(t, err)
	mockCfg.AssertExpectations(t)
}

func TestReconcile_HealthCheckFails(t *testing.T) {
	tr := newTestReconciler()
	mockCfg := new(MockConfigurator)

	apps := []*store.InstalledApp{
		fixtureInstalledApp("qbittorrent", "running"),
	}

	tr.appStore.On("GetAll").Return(apps, nil)
	tr.registry.On("Get", "qbittorrent").Return(mockCfg)

	mockCfg.On("PreStart", mock.Anything, mock.Anything).Return(nil)
	mockCfg.On("HealthCheck", mock.Anything).Return(errors.New("not healthy"))
	// PostStart should NOT be called when HealthCheck fails

	err := tr.reconciler.Reconcile(context.Background())

	require.NoError(t, err)
	mockCfg.AssertNotCalled(t, "PostStart", mock.Anything, mock.Anything)
}

func TestReconcile_PostStartFails(t *testing.T) {
	tr := newTestReconciler()
	mockCfg := new(MockConfigurator)

	apps := []*store.InstalledApp{
		fixtureInstalledApp("qbittorrent", "running"),
	}

	tr.appStore.On("GetAll").Return(apps, nil)
	tr.registry.On("Get", "qbittorrent").Return(mockCfg)

	mockCfg.On("PreStart", mock.Anything, mock.Anything).Return(nil)
	mockCfg.On("HealthCheck", mock.Anything).Return(nil)
	mockCfg.On("PostStart", mock.Anything, mock.Anything).Return(errors.New("api call failed"))

	err := tr.reconciler.Reconcile(context.Background())

	// PostStart failure is logged, not returned
	require.NoError(t, err)
	mockCfg.AssertExpectations(t)
}

func TestReconcile_AppStoreError(t *testing.T) {
	tr := newTestReconciler()

	tr.appStore.On("GetAll").Return(nil, errors.New("database error"))

	err := tr.reconciler.Reconcile(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get installed apps")
}

// ============================================================================
// buildAppState Tests
// ============================================================================

func TestBuildAppState_BasicFields(t *testing.T) {
	tr := newTestReconciler()

	app := fixtureInstalledAppWithPort("qbittorrent", "running", 8180)

	state := tr.reconciler.buildAppState(app)

	assert.Equal(t, "qbittorrent", state.Name)
	assert.Equal(t, "/tmp/bloud-test/qbittorrent", state.DataPath)
	assert.Equal(t, "/tmp/bloud-test", state.BloudDataPath)
	assert.Equal(t, 8180, state.Port)
	assert.NotNil(t, state.Options)
}

func TestBuildAppState_Integrations(t *testing.T) {
	tr := newTestReconciler()

	app := fixtureInstalledAppWithIntegrations("radarr", "running", map[string]string{
		"download-client": "qbittorrent",
		"media-server":    "jellyfin",
	})

	state := tr.reconciler.buildAppState(app)

	assert.Equal(t, []string{"qbittorrent"}, state.Integrations["download-client"])
	assert.Equal(t, []string{"jellyfin"}, state.Integrations["media-server"])
}

// ============================================================================
// Watchdog Tests
// ============================================================================

func TestWatchdog_InitialReconcile(t *testing.T) {
	tr := newTestReconciler()

	tr.appStore.On("GetAll").Return([]*store.InstalledApp{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr.reconciler.StartWatchdog(ctx)

	// Wait for initial reconcile
	time.Sleep(50 * time.Millisecond)

	tr.reconciler.StopWatchdog()

	// Verify GetAll was called (initial reconcile)
	tr.appStore.AssertCalled(t, "GetAll")
}

func TestWatchdog_Stop(t *testing.T) {
	tr := newTestReconciler()

	// Return empty apps each time
	tr.appStore.On("GetAll").Return([]*store.InstalledApp{}, nil)

	ctx := context.Background()
	tr.reconciler.StartWatchdog(ctx)

	// Wait briefly
	time.Sleep(50 * time.Millisecond)

	// Stop should not panic
	tr.reconciler.StopWatchdog()

	// Wait a bit more to ensure goroutine stopped
	time.Sleep(50 * time.Millisecond)
}

func TestWatchdog_ContextCanceled(t *testing.T) {
	tr := newTestReconciler()

	tr.appStore.On("GetAll").Return([]*store.InstalledApp{}, nil)

	ctx, cancel := context.WithCancel(context.Background())

	tr.reconciler.StartWatchdog(ctx)

	// Wait for initial reconcile
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	// Wait for goroutine to exit
	time.Sleep(50 * time.Millisecond)

	// Verify GetAll was called at least once
	tr.appStore.AssertCalled(t, "GetAll")
}

// ============================================================================
// Helper Functions
// ============================================================================

func indexOf(slice []string, item string) int {
	for i, s := range slice {
		if s == item {
			return i
		}
	}
	return -1
}
