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

// TestWaitAndDrain_FirstIntentIsImmediate verifies the core M3 latency fix:
// a lone intent arriving at an idle waiter is drained without waiting out
// the debounce window.
func TestWaitAndDrain_FirstIntentIsImmediate(t *testing.T) {
	q := NewIntentQueue(10 * time.Second)
	done := make(chan []Intent, 1)
	start := time.Now()
	go func() {
		done <- q.WaitAndDrain(context.Background())
	}()

	// Let the waiter block on the empty queue, then deliver the lone intent.
	time.Sleep(50 * time.Millisecond)
	intent := NewInstallAppIntent("jellyfin")
	q.Enqueue(intent)

	select {
	case batch := <-done:
		require.Len(t, batch, 1)
		assert.Equal(t, intent.IntentID(), batch[0].IntentID())
		assert.Less(t, time.Since(start), time.Second, "lone intent must not wait for the debounce window")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out — lone intent was not drained immediately")
	}
}

// TestWaitAndDrain_CoalescesIntentsFromProcessing verifies that intents
// which arrived while the orchestrator was processing a previous batch
// wait out the coalescing window before being drained.
func TestWaitAndDrain_CoalescesIntentsFromProcessing(t *testing.T) {
	q := NewIntentQueue(300 * time.Millisecond)
	intent := NewInstallAppIntent("jellyfin")
	// Enqueued "during processing" — before WaitAndDrain is called.
	q.Enqueue(intent)

	start := time.Now()
	batch := q.WaitAndDrain(context.Background())
	elapsed := time.Since(start)

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

	done := make(chan []Intent, 1)
	start := time.Now()
	go func() {
		done <- q.WaitAndDrain(context.Background())
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
		t.Fatalf("drained after %v — coalescing window should still be open", time.Since(start))
	case <-time.After(50 * time.Millisecond):
	}

	select {
	case batch := <-done:
		assert.Len(t, batch, 7, "first intent plus six burst arrivals")
		// The window was honored after the final arrival (timer jitter is a
		// few ms late, never early).
		assert.GreaterOrEqual(t, time.Since(lastEnqueue), 280*time.Millisecond)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the coalesced batch")
	}
}

// TestWaitAndDrain_CtxCancelledOnEmptyQueue returns nil when cancelled before
// any intent arrives.
func TestWaitAndDrain_CtxCancelledOnEmptyQueue(t *testing.T) {
	q := NewIntentQueue(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.Nil(t, q.WaitAndDrain(ctx))
}

// TestWaitAndDrain_CtxCancelledDuringCoalesce returns the accumulated intents
// instead of dropping them.
func TestWaitAndDrain_CtxCancelledDuringCoalesce(t *testing.T) {
	q := NewIntentQueue(5 * time.Second)
	q.Enqueue(NewInstallAppIntent("jellyfin"))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	batch := q.WaitAndDrain(ctx)
	require.Len(t, batch, 1)
}
