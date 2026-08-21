// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
)

// testOrchestrator groups a real Graph (in-memory) with a mock registry.
type testOrchestrator struct {
	orch     *Orchestrator
	g        *graph.Graph
	registry *MockConfiguratorRegistry
}

func newTestOrchestrator() *testOrchestrator {
	g := graph.New(graph.NewMapRepository())
	registry := new(MockConfiguratorRegistry)
	orch := NewOrchestrator(
		g,
		registry,
		nil, // no catalog needed for these tests
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{HealthCheckTimeout: 100 * time.Millisecond},
	)
	return &testOrchestrator{orch: orch, g: g, registry: registry}
}

// ============================================================================
// Refactor Cycle 1: RemoveApp calls NodeLifecycle.Remove and deletes graph node
// ============================================================================

func TestOrchestrator_RemoveApp_CallsRemoveAndDeletesNode(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("app1"))
	require.NoError(t, to.g.SetTargetStatus("app1", graph.StatusRunning))

	mockNL := new(MockConfigurator)
	to.registry.On("Get", "app1").Return(mockNL)
	mockNL.On("Remove", mock.Anything, mock.Anything, false).Return(nil)

	require.NoError(t, to.orch.RemoveApp(context.Background(), "app1", false))

	mockNL.AssertCalled(t, "Remove", mock.Anything, mock.Anything, false)

	node, err := to.g.GetNode("app1")
	require.NoError(t, err)
	assert.Nil(t, node, "node should be deleted from graph")
}

func TestOrchestrator_RemoveApp_NoConfigurator_DeletesNode(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("app1"))
	to.registry.On("Get", "app1").Return(nil)

	require.NoError(t, to.orch.RemoveApp(context.Background(), "app1", false))

	node, err := to.g.GetNode("app1")
	require.NoError(t, err)
	assert.Nil(t, node, "node should be deleted from graph even without configurator")
}

func TestOrchestrator_RemoveApp_ClearData_PassedToRemove(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("app1"))
	mockNL := new(MockConfigurator)
	to.registry.On("Get", "app1").Return(mockNL)
	mockNL.On("Remove", mock.Anything, mock.Anything, true).Return(nil)

	require.NoError(t, to.orch.RemoveApp(context.Background(), "app1", true))

	mockNL.AssertCalled(t, "Remove", mock.Anything, mock.Anything, true)
}

// ============================================================================
// Reconcile is a no-op without a route generator (traefikGen nil → no-op)
// ============================================================================

func TestOrchestrator_Reconcile_NoRouteGenerator_NoError(t *testing.T) {
	to := newTestOrchestrator()
	// No traefikGen set — RegenerateRoutes is a no-op, should not error.
	require.NoError(t, to.orch.Reconcile(context.Background()))
}

// ============================================================================
// Cycle 4: Basic lifecycle
// ============================================================================

func TestOrchestrator_EmptyGraph_NoError(t *testing.T) {
	to := newTestOrchestrator()
	require.NoError(t, to.orch.Reconcile(context.Background()))
}

func TestOrchestrator_FullLifecycle_OrderedPhases(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("qbittorrent"))
	require.NoError(t, to.g.SetTargetStatus("qbittorrent", graph.StatusRunning))

	mockCfg := new(MockConfigurator)
	to.registry.On("Get", "qbittorrent").Return(mockCfg)

	var callOrder []string
	mockCfg.On("PreStart", mock.Anything, mock.Anything).
		Run(func(_ mock.Arguments) { callOrder = append(callOrder, "prestart") }).
		Return(false, nil)
	mockCfg.On("PostStart", mock.Anything, mock.Anything).
		Run(func(_ mock.Arguments) { callOrder = append(callOrder, "poststart") }).
		Return(nil)

	require.NoError(t, to.orch.Reconcile(context.Background()))

	assert.Equal(t, []string{"prestart", "poststart"}, callOrder)

	node, err := to.g.GetNode("qbittorrent")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus)
}

func TestOrchestrator_PhaseStatusUpdates(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("qbittorrent"))
	require.NoError(t, to.g.SetTargetStatus("qbittorrent", graph.StatusRunning))

	mockCfg := new(MockConfigurator)
	to.registry.On("Get", "qbittorrent").Return(mockCfg)
	mockCfg.On("PreStart", mock.Anything, mock.Anything).Return(false, nil)
	mockCfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	// Capture actual status transitions via event listener.
	var mu sync.Mutex
	var statuses []graph.NodeStatus
	to.g.On(graph.EventNodeUpdated, func(node graph.Node) {
		mu.Lock()
		statuses = append(statuses, node.ActualStatus)
		mu.Unlock()
	})

	require.NoError(t, to.orch.Reconcile(context.Background()))

	mu.Lock()
	got := statuses
	mu.Unlock()

	// Without a container def (no catalog in unit tests), EnsureContainer and
	// HealthCheck are skipped. Status transitions: PreStartConfig → PostStartConfig → Running.
	assert.Equal(t, []graph.NodeStatus{
		graph.StatusPreStartConfig,
		graph.StatusPostStartConfig,
		graph.StatusRunning,
	}, got)
}

func TestOrchestrator_NoConfigurator_MarkedRunning(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("qbittorrent"))
	require.NoError(t, to.g.SetTargetStatus("qbittorrent", graph.StatusRunning))

	to.registry.On("Get", "qbittorrent").Return(nil) // no configurator

	require.NoError(t, to.orch.Reconcile(context.Background()))

	node, err := to.g.GetNode("qbittorrent")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus)
}

func TestOrchestrator_PreStartError_ErrorStatus_LaterPhasesSkipped(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("qbittorrent"))
	require.NoError(t, to.g.SetTargetStatus("qbittorrent", graph.StatusRunning))

	mockCfg := new(MockConfigurator)
	to.registry.On("Get", "qbittorrent").Return(mockCfg)
	mockCfg.On("PreStart", mock.Anything, mock.Anything).Return(false, errors.New("config error"))

	require.NoError(t, to.orch.Reconcile(context.Background())) // reconcile itself doesn't fail

	node, err := to.g.GetNode("qbittorrent")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusError, node.ActualStatus)
	mockCfg.AssertNotCalled(t, "PostStart", mock.Anything, mock.Anything)
}

// ============================================================================
// Cycle 5: Level ordering
// ============================================================================

func TestOrchestrator_LevelOrdering_DependencyRunsFirst(t *testing.T) {
	to := newTestOrchestrator()

	// B depends on A; A must complete its lifecycle before B's PreStart is called.
	require.NoError(t, to.g.AddNode("a"))
	require.NoError(t, to.g.AddNode("b"))
	require.NoError(t, to.g.AddEdge("b", "a")) // b depends on a
	require.NoError(t, to.g.SetTargetStatus("a", graph.StatusRunning))
	require.NoError(t, to.g.SetTargetStatus("b", graph.StatusRunning))

	mockA := new(MockConfigurator)
	mockB := new(MockConfigurator)
	to.registry.On("Get", "a").Return(mockA)
	to.registry.On("Get", "b").Return(mockB)

	var mu sync.Mutex
	var callOrder []string
	record := func(name string) func(mock.Arguments) {
		return func(_ mock.Arguments) {
			mu.Lock()
			callOrder = append(callOrder, name)
			mu.Unlock()
		}
	}

	mockA.On("PreStart", mock.Anything, mock.Anything).Run(record("a-prestart")).Return(false, nil)
	mockA.On("PostStart", mock.Anything, mock.Anything).Run(record("a-poststart")).Return(nil)

	mockB.On("PreStart", mock.Anything, mock.Anything).Run(record("b-prestart")).Return(false, nil)
	mockB.On("PostStart", mock.Anything, mock.Anything).Run(record("b-poststart")).Return(nil)

	require.NoError(t, to.orch.Reconcile(context.Background()))

	mu.Lock()
	order := callOrder
	mu.Unlock()

	aPostIdx := indexOf(order, "a-poststart")
	bPreIdx := indexOf(order, "b-prestart")
	assert.Greater(t, aPostIdx, -1, "a-poststart should have been called")
	assert.Greater(t, bPreIdx, -1, "b-prestart should have been called")
	assert.Less(t, aPostIdx, bPreIdx,
		"a's lifecycle must complete before b's starts; got order: %v", order)
}

func TestOrchestrator_LevelOrdering_DepErrorPreventsDependent(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("a"))
	require.NoError(t, to.g.AddNode("b"))
	require.NoError(t, to.g.AddEdge("b", "a"))
	require.NoError(t, to.g.SetTargetStatus("a", graph.StatusRunning))
	require.NoError(t, to.g.SetTargetStatus("b", graph.StatusRunning))

	mockA := new(MockConfigurator)
	to.registry.On("Get", "a").Return(mockA)
	mockA.On("PreStart", mock.Anything, mock.Anything).Return(false, errors.New("a failed"))

	require.NoError(t, to.orch.Reconcile(context.Background()))

	nodeA, _ := to.g.GetNode("a")
	nodeB, _ := to.g.GetNode("b")
	assert.Equal(t, graph.StatusError, nodeA.ActualStatus)
	assert.Equal(t, graph.StatusInitializing, nodeB.ActualStatus, "b should remain INITIALIZING")
	to.registry.AssertNotCalled(t, "Get", "b")
}

// ============================================================================
// Cycle 6: Within-level concurrency
// ============================================================================

func TestOrchestrator_WithinLevel_ConcurrentExecution(t *testing.T) {
	to := newTestOrchestrator()

	// A and B at level 0 (no edges between them).
	require.NoError(t, to.g.AddNode("a"))
	require.NoError(t, to.g.AddNode("b"))
	require.NoError(t, to.g.SetTargetStatus("a", graph.StatusRunning))
	require.NoError(t, to.g.SetTargetStatus("b", graph.StatusRunning))

	mockA := new(MockConfigurator)
	mockB := new(MockConfigurator)
	to.registry.On("Get", "a").Return(mockA)
	to.registry.On("Get", "b").Return(mockB)

	// Barrier: both PostStarts must run before either can complete.
	// If execution is sequential this deadlocks, proving concurrency is required.
	var reached int32
	ready := make(chan struct{})
	postStartFn := func(_ mock.Arguments) {
		if atomic.AddInt32(&reached, 1) == 2 {
			close(ready)
		}
		<-ready
	}

	mockA.On("PreStart", mock.Anything, mock.Anything).Return(false, nil)
	mockA.On("PostStart", mock.Anything, mock.Anything).Run(postStartFn).Return(nil)

	mockB.On("PreStart", mock.Anything, mock.Anything).Return(false, nil)
	mockB.On("PostStart", mock.Anything, mock.Anything).Run(postStartFn).Return(nil)

	// If not concurrent this deadlocks; use a timeout via context.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, to.orch.Reconcile(ctx))
	mockA.AssertExpectations(t)
	mockB.AssertExpectations(t)
}

// ============================================================================
// Cycle 7: changedIds staleness
// ============================================================================

func TestOrchestrator_Staleness_AlreadyRunning_RerunPostStartWhenDepChanges(t *testing.T) {
	to := newTestOrchestrator()

	// A → B (B depends on A). B is already RUNNING; A just transitioned.
	require.NoError(t, to.g.AddNode("a"))
	require.NoError(t, to.g.AddNode("b"))
	require.NoError(t, to.g.AddEdge("b", "a"))
	require.NoError(t, to.g.SetTargetStatus("a", graph.StatusRunning))
	require.NoError(t, to.g.SetTargetStatus("b", graph.StatusRunning))
	// Pre-seed B as already RUNNING (simulates a previous reconcile cycle).
	require.NoError(t, to.g.SetActualStatus("b", graph.StatusRunning, ""))

	mockA := new(MockConfigurator)
	mockB := new(MockConfigurator)
	to.registry.On("Get", "a").Return(mockA)
	to.registry.On("Get", "b").Return(mockB)

	mockA.On("PreStart", mock.Anything, mock.Anything).Return(false, nil)
	mockA.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	// B should only re-run PostStart; no other phases.
	mockB.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	require.NoError(t, to.orch.Reconcile(context.Background()))

	mockA.AssertExpectations(t)
	mockB.AssertExpectations(t)
	mockB.AssertNotCalled(t, "PreStart", mock.Anything, mock.Anything)
}

func TestOrchestrator_Staleness_NoRerun_WhenDepErrors(t *testing.T) {
	to := newTestOrchestrator()

	// A → B. B is already RUNNING; A errors this cycle.
	require.NoError(t, to.g.AddNode("a"))
	require.NoError(t, to.g.AddNode("b"))
	require.NoError(t, to.g.AddEdge("b", "a"))
	require.NoError(t, to.g.SetTargetStatus("a", graph.StatusRunning))
	require.NoError(t, to.g.SetTargetStatus("b", graph.StatusRunning))
	require.NoError(t, to.g.SetActualStatus("b", graph.StatusRunning, ""))

	mockA := new(MockConfigurator)
	to.registry.On("Get", "a").Return(mockA)
	mockA.On("PreStart", mock.Anything, mock.Anything).Return(false, errors.New("a failed"))

	require.NoError(t, to.orch.Reconcile(context.Background()))

	// B's PostStart should NOT be called — A is not in changedIDs.
	to.registry.AssertNotCalled(t, "Get", "b")
}

func TestOrchestrator_Staleness_SteadyState_NoRerun(t *testing.T) {
	to := newTestOrchestrator()

	// Both A and B already RUNNING; no changes this cycle.
	require.NoError(t, to.g.AddNode("a"))
	require.NoError(t, to.g.AddNode("b"))
	require.NoError(t, to.g.AddEdge("b", "a"))
	require.NoError(t, to.g.SetTargetStatus("a", graph.StatusRunning))
	require.NoError(t, to.g.SetTargetStatus("b", graph.StatusRunning))
	require.NoError(t, to.g.SetActualStatus("a", graph.StatusRunning, ""))
	require.NoError(t, to.g.SetActualStatus("b", graph.StatusRunning, ""))

	require.NoError(t, to.orch.Reconcile(context.Background()))

	// Neither A nor B should have any configurator calls.
	to.registry.AssertNotCalled(t, "Get", mock.Anything)
}

// ============================================================================
// Cycle 8: Error is terminal
// ============================================================================

func TestOrchestrator_ErrorIsTerminal_SkippedEvenIfTargetDiffers(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("a"))
	require.NoError(t, to.g.SetTargetStatus("a", graph.StatusRunning))
	// Pre-set A to ERROR (simulates a previous failure).
	require.NoError(t, to.g.SetActualStatus("a", graph.StatusError, "previous failure"))

	require.NoError(t, to.orch.Reconcile(context.Background()))

	node, err := to.g.GetNode("a")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusError, node.ActualStatus, "error node should remain in ERROR")
	to.registry.AssertNotCalled(t, "Get", "a")
}

func TestOrchestrator_ErrorIsTerminal_DependentAlsoSkipped(t *testing.T) {
	to := newTestOrchestrator()

	// A is already in ERROR; B depends on A and needs to start.
	require.NoError(t, to.g.AddNode("a"))
	require.NoError(t, to.g.AddNode("b"))
	require.NoError(t, to.g.AddEdge("b", "a"))
	require.NoError(t, to.g.SetTargetStatus("a", graph.StatusRunning))
	require.NoError(t, to.g.SetTargetStatus("b", graph.StatusRunning))
	require.NoError(t, to.g.SetActualStatus("a", graph.StatusError, "previous failure"))

	require.NoError(t, to.orch.Reconcile(context.Background()))

	nodeB, err := to.g.GetNode("b")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusInitializing, nodeB.ActualStatus, "b should stay INITIALIZING — dep is in ERROR")
	to.registry.AssertNotCalled(t, "Get", "b")
}

// ============================================================================
// Cycle 9: RUNNING deferred until after route generation
// ============================================================================

// TestOrchestrator_RunningDeferredUntilAfterReconcile verifies that a node is
// not promoted to RUNNING during its lifecycle phases — only after Reconcile
// has finished (i.e. after route generation). The UI must not show "installed"
// prematurely.
func TestOrchestrator_RunningDeferredUntilAfterReconcile(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("app"))
	require.NoError(t, to.g.SetTargetStatus("app", graph.StatusRunning))

	mockCfg := new(MockConfigurator)
	to.registry.On("Get", "app").Return(mockCfg)
	mockCfg.On("PreStart", mock.Anything, mock.Anything).Return(false, nil)

	// Capture the node's actual status at the moment PostStart runs — this is
	// the last lifecycle phase, so if RUNNING were set eagerly it would already
	// be visible here.
	var statusDuringPostStart graph.NodeStatus
	mockCfg.On("PostStart", mock.Anything, mock.Anything).
		Run(func(_ mock.Arguments) {
			node, _ := to.g.GetNode("app")
			statusDuringPostStart = node.ActualStatus
		}).
		Return(nil)

	require.NoError(t, to.orch.Reconcile(context.Background()))

	assert.Equal(t, graph.StatusPostStartConfig, statusDuringPostStart,
		"node must not be RUNNING during lifecycle — RUNNING is deferred until after route generation")

	node, err := to.g.GetNode("app")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus,
		"node must be RUNNING once Reconcile (including route generation) is complete")
}

// TestOrchestrator_DepUnblockedByChangedIDsNotRunning verifies that a dependent
// node can proceed when its dependency completed its lifecycle phases this pass,
// even though the dependency has not yet been promoted to RUNNING (RUNNING is
// deferred until after route generation). Without this, multi-level installs
// would block level N+1 waiting for level N to reach RUNNING.
func TestOrchestrator_DepUnblockedByChangedIDsNotRunning(t *testing.T) {
	to := newTestOrchestrator()

	// B depends on A; both are being installed in the same reconcile pass.
	require.NoError(t, to.g.AddNode("a"))
	require.NoError(t, to.g.AddNode("b"))
	require.NoError(t, to.g.AddEdge("b", "a"))
	require.NoError(t, to.g.SetTargetStatus("a", graph.StatusRunning))
	require.NoError(t, to.g.SetTargetStatus("b", graph.StatusRunning))

	mockA := new(MockConfigurator)
	mockB := new(MockConfigurator)
	to.registry.On("Get", "a").Return(mockA)
	to.registry.On("Get", "b").Return(mockB)

	mockA.On("PreStart", mock.Anything, mock.Anything).Return(false, nil)
	mockA.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	// When B's PreStart runs, A has completed its lifecycle phases but has NOT
	// yet been promoted to RUNNING (that happens after route generation).
	var aStatusWhenBStarted graph.NodeStatus
	mockB.On("PreStart", mock.Anything, mock.Anything).
		Run(func(_ mock.Arguments) {
			nodeA, _ := to.g.GetNode("a")
			aStatusWhenBStarted = nodeA.ActualStatus
		}).
		Return(false, nil)
	mockB.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	require.NoError(t, to.orch.Reconcile(context.Background()))

	assert.Equal(t, graph.StatusPostStartConfig, aStatusWhenBStarted,
		"A must not be at RUNNING when B starts — RUNNING is deferred until after route generation")

	mockB.AssertCalled(t, "PreStart", mock.Anything, mock.Anything)

	nodeA, err := to.g.GetNode("a")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, nodeA.ActualStatus, "A must be RUNNING after Reconcile completes")

	nodeB, err := to.g.GetNode("b")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, nodeB.ActualStatus, "B must be RUNNING after Reconcile completes")
}

// ============================================================================
// Config-change-driven container restart
// ============================================================================

func TestOrchestrator_PreStartChanged_TriggersContainerRemoveBeforeEnsure(t *testing.T) {
	g := graph.New(graph.NewMapRepository())
	registry := new(MockConfiguratorRegistry)
	mockRuntime := new(MockContainerRuntime)
	catalogCache := new(MockCatalogCache)

	orch := NewOrchestrator(
		g,
		registry,
		catalogCache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{
			HealthCheckTimeout: 100 * time.Millisecond,
			Containers:         mockRuntime,
		},
	)

	// Set up a multi-container app: "myapp" owns container "apps-myapp-server".
	containerName := "apps-myapp-server"
	require.NoError(t, g.AddNode(containerName))
	require.NoError(t, g.SetTargetStatus(containerName, graph.StatusRunning))
	orch.registerContainerOwner(containerName, "myapp")

	// Catalog returns the app with a container def.
	appDef := &catalog.App{
		CatalogID: "myapp",
		Containers: []catalog.ContainerDef{
			{Name: containerName, Image: "myapp:latest"},
		},
	}
	catalogCache.On("Get", "myapp").Return(appDef, nil)
	catalogCache.On("Get", containerName).Return(nil, nil)

	// Configurator: PreStart returns changed=true.
	mockCfg := new(MockConfigurator)
	registry.On("Get", containerName).Return(mockCfg)
	mockCfg.On("PreStart", mock.Anything, mock.Anything).Return(true, nil)
	mockCfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	// Container runtime: expect Remove (config changed) then Ensure.
	var callOrder []string
	mockRuntime.On("Remove", mock.Anything, containerName).
		Run(func(_ mock.Arguments) { callOrder = append(callOrder, "remove") }).
		Return(nil)
	mockRuntime.On("Ensure", mock.Anything, mock.Anything).
		Run(func(_ mock.Arguments) { callOrder = append(callOrder, "ensure") }).
		Return(containerruntime.EnsureResult{}, nil)

	require.NoError(t, orch.Reconcile(context.Background()))

	// Verify Remove was called before Ensure.
	assert.Equal(t, []string{"remove", "ensure"}, callOrder,
		"config change should trigger Remove before Ensure")

	node, err := g.GetNode(containerName)
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus)
}

func TestOrchestrator_PreStartNotChanged_NoContainerRemove(t *testing.T) {
	g := graph.New(graph.NewMapRepository())
	registry := new(MockConfiguratorRegistry)
	mockRuntime := new(MockContainerRuntime)
	catalogCache := new(MockCatalogCache)

	orch := NewOrchestrator(
		g,
		registry,
		catalogCache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{
			HealthCheckTimeout: 100 * time.Millisecond,
			Containers:         mockRuntime,
		},
	)

	containerName := "apps-myapp-server"
	require.NoError(t, g.AddNode(containerName))
	require.NoError(t, g.SetTargetStatus(containerName, graph.StatusRunning))
	orch.registerContainerOwner(containerName, "myapp")

	appDef := &catalog.App{
		CatalogID: "myapp",
		Containers: []catalog.ContainerDef{
			{Name: containerName, Image: "myapp:latest"},
		},
	}
	catalogCache.On("Get", "myapp").Return(appDef, nil)
	catalogCache.On("Get", containerName).Return(nil, nil)

	mockCfg := new(MockConfigurator)
	registry.On("Get", containerName).Return(mockCfg)
	mockCfg.On("PreStart", mock.Anything, mock.Anything).Return(false, nil)
	mockCfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	// Only Ensure should be called, not Remove.
	mockRuntime.On("Ensure", mock.Anything, mock.Anything).
		Return(containerruntime.EnsureResult{}, nil)

	require.NoError(t, orch.Reconcile(context.Background()))

	mockRuntime.AssertNotCalled(t, "Remove", mock.Anything, mock.Anything)

	node, err := g.GetNode(containerName)
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus)
}

// TestOrchestrator_EnsureSSO_UsesCatalogIDForContainerNode verifies that forward-auth
// SSO provisioning uses the owning app's catalog ID rather than the container node
// name when building the external URL and provider name. Regression test: navidrome's
// graph node is "apps-navidrome" but Authentik must be configured for "navidrome.localhost".
func TestOrchestrator_EnsureSSO_UsesCatalogIDForContainerNode(t *testing.T) {
	g := graph.New(graph.NewMapRepository())
	registry := new(MockConfiguratorRegistry)
	catalogCache := new(MockCatalogCache)
	ssoMock := new(MockSSOProvisioner)

	orch := NewOrchestrator(
		g,
		registry,
		catalogCache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{
			SSO:                ssoMock,
			SSOBaseURL:         "http://localhost:8080",
			HealthCheckTimeout: 100 * time.Millisecond,
		},
	)

	// navidrome is defined with a containers list, so its graph node is the
	// container name "apps-navidrome", owned by catalog ID "navidrome". The
	// single container is the app's primary node, so ensureSSO runs for it.
	containerName := "apps-navidrome"
	orch.registerContainerOwner(containerName, "navidrome")

	catalogCache.On("Get", "navidrome").Return(&catalog.App{
		CatalogID:   "navidrome",
		DisplayName: "Navidrome",
		Port:        4533,
		SSO:         catalog.SSO{Strategy: "forward-auth"},
		Containers:  []catalog.ContainerDef{{Name: "apps-navidrome"}},
	}, nil)

	ssoMock.On("EnsureForwardAuth", "navidrome", "Navidrome", "http://navidrome.localhost:8080").
		Return(nil)

	require.NoError(t, orch.ensureSSO(context.Background(), containerName))

	ssoMock.AssertExpectations(t)
	ssoMock.AssertCalled(t, "EnsureForwardAuth", "navidrome", "Navidrome", "http://navidrome.localhost:8080")
}

func TestOrchestrator_EnsureSSO_SkipsNonPrimaryContainerNodes(t *testing.T) {
	g := graph.New(graph.NewMapRepository())
	registry := new(MockConfiguratorRegistry)
	catalogCache := new(MockCatalogCache)
	ssoMock := new(MockSSOProvisioner)

	orch := NewOrchestrator(
		g,
		registry,
		catalogCache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{
			SSO:             ssoMock,
			SSOBaseURL:      "http://localhost:8080",
			SSOHostSecret:   "test-secret",
			SSOAuthentikURL: "http://localhost:9001",
		},
	)

	// Multi-container app: SSO must be provisioned exactly once, on the
	// primary node (last container). Non-primary nodes (postgres) skip it so
	// they never fail on a not-yet-ready SSO dependency.
	catalogCache.On("Get", "immich").Return(&catalog.App{
		CatalogID:   "immich",
		DisplayName: "Immich",
		Port:        2283,
		SSO:         catalog.SSO{Strategy: "native-oidc", CallbackPath: "/auth/login"},
		Containers: []catalog.ContainerDef{
			{Name: "apps-immich-postgres"},
			{Name: "apps-immich-server"},
		},
	}, nil)
	orch.registerContainerOwner("apps-immich-postgres", "immich")
	orch.registerContainerOwner("apps-immich-server", "immich")

	// No provisioner expectation: the non-primary node must be a no-op.
	require.NoError(t, orch.ensureSSO(context.Background(), "apps-immich-postgres"))
	ssoMock.AssertExpectations(t)

	// The primary node provisions exactly once.
	ssoMock.On("EnsureNativeOIDC", mock.Anything, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything).Return(nil)
	require.NoError(t, orch.ensureSSO(context.Background(), "apps-immich-server"))
	ssoMock.AssertExpectations(t)
}
func TestContainerSpecFromDef_CollectsAllNetworks(t *testing.T) {
	def := catalog.ContainerDef{
		Name:    "apps-authentik-ldap",
		Image:   "ghcr.io/goauthentik/ldap:2025.10.3",
		Network: "authentik-internal",
		Networks: []string{
			"authentik-internal",
			"apps-net",
		},
		Environment: map[string]string{"AUTHENTIK_HOST": "http://apps-authentik-server:9000"},
	}

	spec, err := ContainerSpecFromDef(def, "authentik", "/var/tmp/bloud", nil)
	require.NoError(t, err)

	assert.Equal(t, "apps-authentik-ldap", spec.Name)
	assert.Equal(t, []string{"authentik-internal", "apps-net"}, spec.Networks)
	assert.Equal(t, map[string]string{"io.bloud.app": "authentik"}, spec.Labels)
	assert.Equal(t, "http://apps-authentik-server:9000", spec.Environment["AUTHENTIK_HOST"])
}

func TestContainerSpecFromDef_SingularNetworkOnly(t *testing.T) {
	def := catalog.ContainerDef{
		Name:    "apps-jellyfin",
		Image:   "jellyfin:latest",
		Network: "apps-net",
	}

	spec, err := ContainerSpecFromDef(def, "jellyfin", "/var/tmp/bloud", nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"apps-net"}, spec.Networks)
}

func TestContainerSpecFromDef_NoNetwork(t *testing.T) {
	def := catalog.ContainerDef{
		Name:  "apps-worker",
		Image: "worker:latest",
	}

	spec, err := ContainerSpecFromDef(def, "myapp", "/var/tmp/bloud", nil)
	require.NoError(t, err)

	assert.Empty(t, spec.Networks)
}
