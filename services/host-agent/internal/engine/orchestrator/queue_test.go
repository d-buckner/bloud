// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drained captures both WaitAndDrain results for goroutine handoff.
type drained struct {
	batch []Intent
	live  bool
}

// TestWaitAndDrain_FirstIntentIsImmediate verifies the core M3 latency fix:
// a lone intent arriving at an idle waiter is drained without waiting out
// the debounce window.
func TestWaitAndDrain_FirstIntentIsImmediate(t *testing.T) {
	q := NewIntentQueue(10 * time.Second)
	done := make(chan drained, 1)
	start := time.Now()
	go func() {
		batch, live := q.WaitAndDrain(context.Background())
		done <- drained{batch, live}
	}()

	// Let the waiter block on the empty queue, then deliver the lone intent.
	time.Sleep(50 * time.Millisecond)
	intent := NewInstallAppIntent("jellyfin")
	q.Enqueue(intent)

	select {
	case res := <-done:
		require.True(t, res.live)
		require.Len(t, res.batch, 1)
		assert.Equal(t, intent.IntentID(), res.batch[0].IntentID())
		assert.Less(t, time.Since(start), time.Second, "lone intent must not wait for the debounce window")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out: lone intent was not drained immediately")
	}
}

// TestWaitAndDrain_CoalescesIntentsFromProcessing verifies that intents
// which arrived while the orchestrator was processing a previous batch
// wait out the coalescing window before being drained.
func TestWaitAndDrain_CoalescesIntentsFromProcessing(t *testing.T) {
	q := NewIntentQueue(300 * time.Millisecond)
	intent := NewInstallAppIntent("jellyfin")
	// Enqueued "during processing": before WaitAndDrain is called.
	q.Enqueue(intent)

	start := time.Now()
	batch, live := q.WaitAndDrain(context.Background())
	elapsed := time.Since(start)

	require.True(t, live)
	require.Len(t, batch, 1)
	assert.GreaterOrEqual(t, elapsed, 250*time.Millisecond,
		"pre-queued intents must wait out the coalescing window")
}

// TestWaitAndDrain_ResetsOnNewArrival verifies classic debounce semantics on
// the coalescing path: each new arrival resets the window, so a burst keeps
// the batch open until it settles, then one drain returns the whole burst.
func TestWaitAndDrain_ResetsOnNewArrival(t *testing.T) {
	const window = 300 * time.Millisecond
	q := NewIntentQueue(window)
	q.Enqueue(NewInstallAppIntent("first"))

	done := make(chan drained, 1)
	start := time.Now()
	go func() {
		batch, live := q.WaitAndDrain(context.Background())
		done <- drained{batch, live}
	}()

	// Arrive a new intent every 100 ms for ~600 ms: each one resets the 300 ms
	// window, so the drain cannot fire before the last arrival + window.
	stop := time.Now().Add(600 * time.Millisecond)
	var lastEnqueue time.Time
	for time.Now().Before(stop) {
		q.Enqueue(NewInstallAppIntent("burst"))
		lastEnqueue = time.Now()
		time.Sleep(100 * time.Millisecond)
	}

	// The last arrival extended the window past now; the drain must not have
	// fired yet (earliest possible is last arrival + window).
	select {
	case <-done:
		t.Fatalf("drained after %v: coalescing window should still be open", time.Since(start))
	case <-time.After(50 * time.Millisecond):
	}

	select {
	case res := <-done:
		assert.True(t, res.live)
		assert.Len(t, res.batch, 7, "first intent plus six burst arrivals")
		// The window was honored after the final arrival (timer jitter is a
		// few ms late, never early).
		assert.GreaterOrEqual(t, time.Since(lastEnqueue), 280*time.Millisecond)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the coalesced batch")
	}
}

// TestWaitAndDrain_CtxCancelledOnEmptyQueue reports live=false when
// cancelled before any intent arrives. Cancellation is the only shutdown
// signal WaitAndDrain gives.
func TestWaitAndDrain_CtxCancelledOnEmptyQueue(t *testing.T) {
	q := NewIntentQueue(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	batch, live := q.WaitAndDrain(ctx)
	assert.Nil(t, batch)
	assert.False(t, live, "cancellation must be reported as shutdown")
}

// TestWaitAndDrain_CtxCancelledDuringCoalesce returns the accumulated intents
// instead of dropping them, and reports shutdown.
func TestWaitAndDrain_CtxCancelledDuringCoalesce(t *testing.T) {
	q := NewIntentQueue(5 * time.Second)
	q.Enqueue(NewInstallAppIntent("jellyfin"))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	batch, live := q.WaitAndDrain(ctx)
	require.Len(t, batch, 1)
	assert.False(t, live, "cancellation must be reported as shutdown")
}

// TestWaitAndDrain_StaleTokenIsLiveEmptyBatch reproduces the exact state
// that used to end reconciliation forever: a coalescing timer that won the
// select while a signal token was still buffered (emulated here by draining
// the items away under the race-free conditions the interleaving produces).
// Waking on a token with nothing queued must be an empty LIVE batch, never
// a shutdown report: under the old nil-means-stopped contract, one such
// wake stopped the orchestrator forever while Submits kept answering 202.
func TestWaitAndDrain_StaleTokenIsLiveEmptyBatch(t *testing.T) {
	q := NewIntentQueue(50 * time.Millisecond)
	q.Enqueue(NewInstallAppIntent("jellyfin"))
	q.Drain() // items gone, token pending

	// Must not block and must not report shutdown.
	batch, live := q.WaitAndDrain(context.Background())
	assert.Empty(t, batch, "woke on the stale token with an empty queue")
	assert.True(t, live, "a stale token is not shutdown")

	// The queue still delivers after the stale wake.
	late := NewInstallAppIntent("navidrome")
	q.Enqueue(late)
	batch, live = q.WaitAndDrain(context.Background())
	require.True(t, live)
	require.Len(t, batch, 1)
	assert.Equal(t, late.IntentID(), batch[0].IntentID())
}
