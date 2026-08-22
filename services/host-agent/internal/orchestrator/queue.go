// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"context"
	"sync"
	"time"
)

// DefaultDebounce is the coalescing window for bursts of intents that
// arrive while the orchestrator is already processing a batch. A lone
// intent is processed immediately (no wait); see WaitAndDrain.
const DefaultDebounce = 750 * time.Millisecond

// IntentQueue is a thread-safe FIFO queue for intents with debounce support.
type IntentQueue struct {
	mu       sync.Mutex
	items    []Intent
	signal   chan struct{}
	debounce time.Duration
}

// NewIntentQueue creates a new IntentQueue with the given debounce duration.
func NewIntentQueue(debounce time.Duration) *IntentQueue {
	return &IntentQueue{
		signal:   make(chan struct{}, 1),
		debounce: debounce,
	}
}

// Enqueue adds an intent to the queue and signals any waiting consumer.
func (q *IntentQueue) Enqueue(intent Intent) {
	q.mu.Lock()
	q.items = append(q.items, intent)
	q.mu.Unlock()

	// Non-blocking send: if signal is already pending, no need to send again.
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// Drain removes and returns all queued intents. Returns nil if the queue is empty.
func (q *IntentQueue) Drain() []Intent {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.items) == 0 {
		return nil
	}

	items := q.items
	q.items = nil
	return items
}

// PendingCount returns the number of intents currently in the queue.
func (q *IntentQueue) PendingCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// WaitAndDrain blocks until an intent is available, then returns a batch:
//
//   - If the queue is empty on entry it blocks until the first intent arrives
//     and drains immediately — a lone intent is processed without any delay.
//   - If intents are already queued (they arrived while the orchestrator was
//     processing a previous batch) it waits out the debounce window, resetting
//     on each new arrival, so a burst of intents coalesces into one batch.
//
// If ctx is cancelled before any intent arrives, returns nil. If ctx is
// cancelled while coalescing, returns the accumulated intents.
func (q *IntentQueue) WaitAndDrain(ctx context.Context) []Intent {
	q.mu.Lock()
	pending := len(q.items) > 0
	q.mu.Unlock()

	if !pending {
		// Wait for the first intent or cancellation, then drain right away.
		select {
		case <-q.signal:
		case <-ctx.Done():
			return nil
		}
		return q.Drain()
	}

	// Intents accumulated during processing: coalesce the burst before
	// draining so one convergence pass handles the whole batch.
	timer := time.NewTimer(q.debounce)
	defer timer.Stop()
	for {
		select {
		case <-q.signal:
			// New intent arrived — reset the debounce timer.
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(q.debounce)

		case <-timer.C:
			// Coalescing window expired — drain and return.
			return q.Drain()

		case <-ctx.Done():
			// Context cancelled — return whatever we have.
			return q.Drain()
		}
	}
}
