// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// Contract: the drive path reports phase boundaries into the operations
// store (docs/plans/operation-state-design.md) so a crashed drive is
// diagnosable across restarts, while the store never gates convergence.

func newRecordingOrchestrator(t *testing.T, ops *store.OperationStore, apps store.AppStoreInterface) (*Orchestrator, *graph.Graph, *MockConfiguratorRegistry) {
	t.Helper()
	g := graph.New(graph.NewMapRepository())
	registry := new(MockConfiguratorRegistry)
	orch := NewOrchestrator(
		g, registry, nil, "/tmp/bloud-test", newTestLogger(),
		OrchestratorConfig{
			HealthCheckTimeout: 100 * time.Millisecond,
			Operations:         ops,
			AppStore:           apps,
		},
	)
	return orch, g, registry
}

func TestRecorder_PreStartFailureFailsDriveRow(t *testing.T) {
	db := testdb.SetupTestDB(t)
	ops := store.NewOperationStore(db)
	orch, g, registry := newRecordingOrchestrator(t, ops, NewFakeAppStore())

	require.NoError(t, g.AddNode("app1"))
	require.NoError(t, g.SetTargetStatus("app1", graph.StatusRunning))

	cfg := new(MockConfigurator)
	registry.On("Get", "app1").Return(cfg)
	cfg.On("PreStart", mock.Anything, mock.Anything).Return(configurator.NoRestart(), errors.New("config error"))

	require.NoError(t, orch.Reconcile(context.Background()))

	op, err := ops.Get("app1")
	require.NoError(t, err)
	require.NotNil(t, op, "failed drive must leave an operation row")
	assert.Equal(t, store.OpTypeReconcile, op.Type, "unit-driven drive is reconcile")
	assert.Equal(t, store.OpStatusFailed, op.Status)
	assert.Equal(t, store.OpPhasePrestart, op.Phase)
	assert.Contains(t, op.Cause, "config error")
	assert.True(t, op.Retryable)
}

func TestRecorder_RunningRowCompletesViaStatusSync(t *testing.T) {
	db := testdb.SetupTestDB(t)
	ops := store.NewOperationStore(db)
	orch, g, registry := newRecordingOrchestrator(t, ops, NewFakeAppStore())

	require.NoError(t, g.AddNode("app1"))
	require.NoError(t, g.SetTargetStatus("app1", graph.StatusRunning))

	cfg := new(MockConfigurator)
	registry.On("Get", "app1").Return(cfg)
	cfg.On("PreStart", mock.Anything, mock.Anything).Return(configurator.NoRestart(), nil)
	cfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	require.NoError(t, orch.Reconcile(context.Background()))

	op, err := ops.Get("app1")
	require.NoError(t, err)
	require.NotNil(t, op)
	assert.Equal(t, store.OpStatusDone, op.Status)
	assert.Equal(t, store.OpPhaseComplete, op.Phase)
}

func TestRecorder_SteadyStateWritesNothing(t *testing.T) {
	db := testdb.SetupTestDB(t)
	ops := store.NewOperationStore(db)
	orch, g, registry := newRecordingOrchestrator(t, ops, NewFakeAppStore())

	require.NoError(t, g.AddNode("app1"))
	require.NoError(t, g.SetTargetStatus("app1", graph.StatusRunning))

	cfg := new(MockConfigurator)
	registry.On("Get", "app1").Return(cfg)
	cfg.On("PreStart", mock.Anything, mock.Anything).Return(configurator.NoRestart(), nil)
	cfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)

	require.NoError(t, orch.Reconcile(context.Background()))
	before, err := ops.Get("app1")
	require.NoError(t, err)
	require.Equal(t, store.OpStatusDone, before.Status)

	// Second reconcile over an already-RUNNING node must not touch the
	// operation row (amplification guard, design §8).
	require.NoError(t, orch.Reconcile(context.Background()))
	after, err := ops.Get("app1")
	require.NoError(t, err)
	assert.Equal(t, before.ID, after.ID, "no new drive started")
	assert.Equal(t, before.Status, after.Status)
	assert.Equal(t, before.Phase, after.Phase)
	assert.Equal(t, before.UpdatedAt, after.UpdatedAt, "no silent write to the row")
}

func TestRecorder_RemoveAppTerminalStates(t *testing.T) {
	db := testdb.SetupTestDB(t)
	ops := store.NewOperationStore(db)
	orch, g, registry := newRecordingOrchestrator(t, ops, NewFakeAppStore())

	require.NoError(t, g.AddNode("app1"))
	require.NoError(t, g.SetTargetStatus("app1", graph.StatusRunning))

	// Failure: the uninstall row records the remove error.
	cfgErr := new(MockConfigurator)
	registry.On("Get", "app1").Return(cfgErr)
	cfgErr.On("Remove", mock.Anything, mock.Anything, false).Return(errors.New("still running"))
	err := orch.RemoveApp(context.Background(), "app1", false)
	require.Error(t, err)

	op, _ := ops.Get("app1")
	require.NotNil(t, op)
	assert.Equal(t, store.OpStatusFailed, op.Status)
	assert.Equal(t, store.OpPhaseTopology, op.Phase)
	assert.Contains(t, op.Cause, "still running")

	// Success: the row completes.
	cfgOK := new(MockConfigurator)
	registry.On("Get", "app2").Return(cfgOK)
	cfgOK.On("Remove", mock.Anything, mock.Anything, false).Return(nil)
	require.NoError(t, g.AddNode("app2"))
	require.NoError(t, g.SetTargetStatus("app2", graph.StatusRunning))
	// Seed the uninstall row that Submit would have created.
	require.NoError(t, ops.Start("app2", "op-u", store.OpTypeUninstall, store.OpPhaseTopology))

	require.NoError(t, orch.RemoveApp(context.Background(), "app2", false))
	op, _ = ops.Get("app2")
	require.NotNil(t, op)
	assert.Equal(t, store.OpStatusDone, op.Status)
}

func TestRecorder_StartFlipsOrphans(t *testing.T) {
	db := testdb.SetupTestDB(t)
	ops := store.NewOperationStore(db)
	orch, _, _ := newRecordingOrchestrator(t, ops, NewFakeAppStore())

	// A running row from a previous process = drive died mid-phase.
	require.NoError(t, ops.Start("app1", "op-x", store.OpTypeInstall, store.OpPhaseHealth))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	go func() {
		orch.Start(ctx)
		close(started)
	}()

	// The flip happens before first convergence completes; poll for it.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		op, err := ops.Get("app1")
		require.NoError(t, err)
		if op != nil && op.Status == store.OpStatusFailed {
			assert.True(t, op.Retryable)
			assert.Contains(t, op.Cause, "restart")
			assert.Equal(t, store.OpPhaseHealth, op.Phase, "phase preserved at last entry")
			cancel()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("orchestrator did not stop after cancel")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("orphan row was never flipped to failed/interrupted")
}
