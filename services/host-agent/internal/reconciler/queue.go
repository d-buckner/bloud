package reconciler

import (
	"context"
	"sync"
	"time"
)

// DefaultDebounce is the default debounce duration for WaitAndDrain.
const DefaultDebounce = 5 * time.Second

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

// WaitAndDrain blocks until at least one intent arrives, then waits for the
// debounce window to expire (resetting on each new arrival) before draining.
// If ctx is cancelled before any intent arrives, returns nil.
// If ctx is cancelled during debounce, returns any accumulated intents.
func (q *IntentQueue) WaitAndDrain(ctx context.Context) []Intent {
	// Wait for the first intent or cancellation.
	select {
	case <-q.signal:
	case <-ctx.Done():
		return nil
	}

	// Start debounce timer.
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
			// Debounce window expired — drain and return.
			return q.Drain()

		case <-ctx.Done():
			// Context cancelled — return whatever we have.
			return q.Drain()
		}
	}
}
