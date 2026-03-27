package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
)

// MockReconfigDispatcher implements ReconfigDispatcher for testing.
type MockReconfigDispatcher struct {
	mock.Mock
}

func (m *MockReconfigDispatcher) DispatchReconfig(ctx context.Context, appName string, installedApps map[string]*store.InstalledApp) {
	m.Called(ctx, appName, installedApps)
}

// ============================================================================
// Optional-dep transition detection
// ============================================================================

// TestReconcile_OptionalDep_NewlyHealthy_DispatchesReconfigForParent verifies the core
// use case: Prowlarr is already running; Radarr is installed later. On the reconcile
// where Radarr first becomes healthy, Prowlarr's reconfig should be dispatched.
func TestReconcile_OptionalDep_NewlyHealthy_DispatchesReconfigForParent(t *testing.T) {
	tr := newTestReconcilerWithCache()
	dispatcher := new(MockReconfigDispatcher)
	tr.reconciler.SetReconfigDispatcher(dispatcher)

	prowlarrCatalog := fixtureCatalogAppWithOptionalIntegration("prowlarr", "movieManager", "radarr")
	tr.cache.On("Get", "prowlarr").Return(prowlarrCatalog, nil)
	tr.cache.On("Get", "radarr").Return(fixtureRadarr(), nil)

	mockProwlarrCfg := new(MockConfigurator)
	mockRadarrCfg := new(MockConfigurator)
	tr.registry.On("Get", "prowlarr").Return(mockProwlarrCfg)
	tr.registry.On("Get", "radarr").Return(mockRadarrCfg)

	mockProwlarrCfg.On("PreStart", mock.Anything, mock.Anything).Return(nil)
	mockProwlarrCfg.On("HealthCheck", mock.Anything).Return(nil)
	mockProwlarrCfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)
	mockRadarrCfg.On("PreStart", mock.Anything, mock.Anything).Return(nil)
	mockRadarrCfg.On("HealthCheck", mock.Anything).Return(nil)
	mockRadarrCfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	// First reconcile: only Prowlarr installed
	tr.appStore.On("GetAll").Return([]*store.InstalledApp{
		fixtureInstalledApp("prowlarr", "running"),
	}, nil).Once()

	err := tr.reconciler.Reconcile(context.Background())
	require.NoError(t, err)
	dispatcher.AssertNotCalled(t, "DispatchReconfig")

	// Second reconcile: Radarr now installed and healthy for the first time
	tr.appStore.On("GetAll").Return([]*store.InstalledApp{
		fixtureInstalledApp("prowlarr", "running"),
		fixtureInstalledApp("radarr", "running"),
	}, nil).Once()

	dispatcher.On("DispatchReconfig", mock.Anything, "prowlarr", mock.Anything).Return()

	err = tr.reconciler.Reconcile(context.Background())
	require.NoError(t, err)

	dispatcher.AssertCalled(t, "DispatchReconfig", mock.Anything, "prowlarr", mock.Anything)
	dispatcher.AssertNumberOfCalls(t, "DispatchReconfig", 1)
}

// TestReconcile_OptionalDep_AlreadyHealthy_NoDispatchOnRepeat verifies that a parent
// is not re-dispatched on every subsequent reconcile once both apps are healthy.
// Uses 3 cycles: Prowlarr-only → Radarr appears (dispatch) → steady state (no dispatch).
func TestReconcile_OptionalDep_AlreadyHealthy_NoDispatchOnRepeat(t *testing.T) {
	tr := newTestReconcilerWithCache()
	dispatcher := new(MockReconfigDispatcher)
	tr.reconciler.SetReconfigDispatcher(dispatcher)

	prowlarrCatalog := fixtureCatalogAppWithOptionalIntegration("prowlarr", "movieManager", "radarr")
	tr.cache.On("Get", "prowlarr").Return(prowlarrCatalog, nil)
	tr.cache.On("Get", "radarr").Return(fixtureRadarr(), nil)

	mockProwlarrCfg := new(MockConfigurator)
	mockRadarrCfg := new(MockConfigurator)
	tr.registry.On("Get", "prowlarr").Return(mockProwlarrCfg)
	tr.registry.On("Get", "radarr").Return(mockRadarrCfg)

	mockProwlarrCfg.On("PreStart", mock.Anything, mock.Anything).Return(nil)
	mockProwlarrCfg.On("HealthCheck", mock.Anything).Return(nil)
	mockProwlarrCfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)
	mockRadarrCfg.On("PreStart", mock.Anything, mock.Anything).Return(nil)
	mockRadarrCfg.On("HealthCheck", mock.Anything).Return(nil)
	mockRadarrCfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	bothInstalled := []*store.InstalledApp{
		fixtureInstalledApp("prowlarr", "running"),
		fixtureInstalledApp("radarr", "running"),
	}

	// Cycle 1: only Prowlarr — establishes prevHealthy = {prowlarr}
	tr.appStore.On("GetAll").Return([]*store.InstalledApp{
		fixtureInstalledApp("prowlarr", "running"),
	}, nil).Once()
	require.NoError(t, tr.reconciler.Reconcile(context.Background()))
	dispatcher.AssertNotCalled(t, "DispatchReconfig")

	// Cycle 2: Radarr appears — Prowlarr was in prevHealthy, dispatch fires
	tr.appStore.On("GetAll").Return(bothInstalled, nil).Once()
	dispatcher.On("DispatchReconfig", mock.Anything, "prowlarr", mock.Anything).Return()
	require.NoError(t, tr.reconciler.Reconcile(context.Background()))
	dispatcher.AssertNumberOfCalls(t, "DispatchReconfig", 1)

	// Cycle 3: same apps, both already tracked — no further dispatch
	tr.appStore.On("GetAll").Return(bothInstalled, nil).Once()
	require.NoError(t, tr.reconciler.Reconcile(context.Background()))
	dispatcher.AssertNumberOfCalls(t, "DispatchReconfig", 1) // still exactly 1
}

// TestReconcile_OptionalDep_NoParent_NoDispatch verifies that no dispatch happens
// when a newly-healthy app has no installed parent with an optional integration on it.
func TestReconcile_OptionalDep_NoParent_NoDispatch(t *testing.T) {
	tr := newTestReconcilerWithCache()
	dispatcher := new(MockReconfigDispatcher)
	tr.reconciler.SetReconfigDispatcher(dispatcher)

	// qBittorrent has no optional parents
	tr.cache.On("Get", "qbittorrent").Return(fixtureQBittorrent(), nil)

	mockQbtCfg := new(MockConfigurator)
	tr.registry.On("Get", "qbittorrent").Return(mockQbtCfg)
	mockQbtCfg.On("PreStart", mock.Anything, mock.Anything).Return(nil)
	mockQbtCfg.On("HealthCheck", mock.Anything).Return(nil)
	mockQbtCfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	tr.appStore.On("GetAll").Return([]*store.InstalledApp{
		fixtureInstalledApp("qbittorrent", "running"),
	}, nil)

	require.NoError(t, tr.reconciler.Reconcile(context.Background()))

	dispatcher.AssertNotCalled(t, "DispatchReconfig")
}

// TestReconcile_OptionalDep_ParentNotYetHealthy_NoDispatch verifies that if the
// parent app is also new this cycle (not in prevHealthy), no dispatch fires —
// the level-ordering from bloud-yjl ensures Prowlarr's PostStart handles it directly.
func TestReconcile_OptionalDep_ParentNotYetHealthy_NoDispatch(t *testing.T) {
	tr := newTestReconcilerWithCache()
	dispatcher := new(MockReconfigDispatcher)
	tr.reconciler.SetReconfigDispatcher(dispatcher)

	prowlarrCatalog := fixtureCatalogAppWithOptionalIntegration("prowlarr", "movieManager", "radarr")
	tr.cache.On("Get", "prowlarr").Return(prowlarrCatalog, nil)
	tr.cache.On("Get", "radarr").Return(fixtureRadarr(), nil)

	mockProwlarrCfg := new(MockConfigurator)
	mockRadarrCfg := new(MockConfigurator)
	tr.registry.On("Get", "prowlarr").Return(mockProwlarrCfg)
	tr.registry.On("Get", "radarr").Return(mockRadarrCfg)

	mockProwlarrCfg.On("PreStart", mock.Anything, mock.Anything).Return(nil)
	mockProwlarrCfg.On("HealthCheck", mock.Anything).Return(nil)
	mockProwlarrCfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)
	mockRadarrCfg.On("PreStart", mock.Anything, mock.Anything).Return(nil)
	mockRadarrCfg.On("HealthCheck", mock.Anything).Return(nil)
	mockRadarrCfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	// Both installed fresh in the same reconcile — Prowlarr is also a new transition
	tr.appStore.On("GetAll").Return([]*store.InstalledApp{
		fixtureInstalledApp("prowlarr", "running"),
		fixtureInstalledApp("radarr", "running"),
	}, nil)

	require.NoError(t, tr.reconciler.Reconcile(context.Background()))

	// Prowlarr was not in prevHealthy, so no dispatch — its own PostStart handled it
	dispatcher.AssertNotCalled(t, "DispatchReconfig")
}
