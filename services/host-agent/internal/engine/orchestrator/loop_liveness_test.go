// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
)

// Contract: the intent loop stops only when its context is cancelled. A
// stale signal token (the coalescing timer winning the select while a
// token was still buffered) used to surface as a nil batch that Start read
// as shutdown: one unlucky interleaving ended reconciliation forever
// while every later Submit kept answering 202.

func TestStart_LoopSurvivesStaleSignalTokens(t *testing.T) {
	db := testdb.SetupTestDB(t)
	ops := store.NewOperationStore(db)
	orch, _, _ := newRecordingOrchestrator(t, ops, NewFakeAppStore())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan struct{})
	go func() {
		orch.Start(ctx)
		close(returned)
	}()
	<-orch.Ready()

	// Recreate the stale-token state three times: enqueue an intent, then
	// drain the items away, leaving the buffered signal token behind.
	for i := 0; i < 3; i++ {
		orch.queue.Enqueue(NewInstallAppIntent("ghost"))
		orch.queue.Drain()
	}

	// The loop wakes on each token with an empty (or, if the drain raced,
	// a trivially handled single-item) batch and must keep waiting either
	// way. The wait spans the debounce window so the coalescing branch is
	// exercised too; under the old contract the loop had exited here.
	select {
	case <-returned:
		t.Fatal("Start exited on an empty drain; only cancellation may stop the loop")
	case <-time.After(DefaultDebounce + 500*time.Millisecond):
	}

	cancel()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not exit after cancel")
	}
}

func TestStart_ExposesLoopLivenessAndConvergenceStamp(t *testing.T) {
	db := testdb.SetupTestDB(t)
	ops := store.NewOperationStore(db)
	orch, _, _ := newRecordingOrchestrator(t, ops, NewFakeAppStore())

	require.True(t, orch.LastConverged().IsZero(), "no convergence before Start")
	require.False(t, orch.Stopped(), "not started, not stopped")

	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() {
		orch.Start(ctx)
		close(returned)
	}()
	<-orch.Ready()

	assert.False(t, orch.Stopped(), "an idle-but-running loop is not stopped")
	conv := orch.LastConverged()
	require.False(t, conv.IsZero(), "the first convergence pass stamps the clock")
	assert.False(t, orch.Status().LoopStopped)
	assert.Equal(t, conv, orch.Status().LastConverged)

	cancel()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not exit after cancel")
	}

	assert.True(t, orch.Stopped(), "a dead loop reports stopped so health can see it")
	assert.True(t, orch.Status().LoopStopped)
}
