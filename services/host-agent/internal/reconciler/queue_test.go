package reconciler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDebounce = 10 * time.Millisecond

func newTestQueue() *IntentQueue {
	return NewIntentQueue(testDebounce)
}

// ============================================================================
// Drain Tests
// ============================================================================

func TestIntentQueue_Drain_EmptyQueueReturnsNil(t *testing.T) {
	q := newTestQueue()
	assert.Nil(t, q.Drain())
}

func TestIntentQueue_Enqueue_SingleIntent_DrainReturnsFIFO(t *testing.T) {
	q := newTestQueue()
	intent := NewInstallAppIntent("jellyfin")
	q.Enqueue(intent)

	items := q.Drain()
	require.Len(t, items, 1)
	assert.Equal(t, intent.IntentID(), items[0].IntentID())
}

func TestIntentQueue_Enqueue_MultipleIntents_DrainPreservesFIFOOrder(t *testing.T) {
	q := newTestQueue()

	intents := []Intent{
		NewInstallAppIntent("jellyfin"),
		NewUninstallAppIntent("radarr", true),
		NewRenameAppIntent("sonarr", "Sonarr TV"),
		NewCreateShareIntent("jellyfin"),
		NewClearAppDataIntent("radarr"),
	}
	for _, intent := range intents {
		q.Enqueue(intent)
	}

	items := q.Drain()
	require.Len(t, items, 5)
	for i, item := range items {
		assert.Equal(t, intents[i].IntentID(), item.IntentID(), "intent at position %d should match", i)
	}
}

func TestIntentQueue_Drain_ClearsQueue(t *testing.T) {
	q := newTestQueue()
	q.Enqueue(NewInstallAppIntent("jellyfin"))

	first := q.Drain()
	require.Len(t, first, 1)

	second := q.Drain()
	assert.Nil(t, second)
}

// ============================================================================
// PendingCount Tests
// ============================================================================

func TestIntentQueue_PendingCount_ReflectsQueueDepth(t *testing.T) {
	q := newTestQueue()
	assert.Equal(t, 0, q.PendingCount())

	q.Enqueue(NewInstallAppIntent("jellyfin"))
	assert.Equal(t, 1, q.PendingCount())

	q.Enqueue(NewInstallAppIntent("radarr"))
	assert.Equal(t, 2, q.PendingCount())

	q.Drain()
	assert.Equal(t, 0, q.PendingCount())
}

// ============================================================================
// Concurrency Tests
// ============================================================================

func TestIntentQueue_Concurrency_SafeToEnqueueFromMultipleGoroutines(t *testing.T) {
	q := newTestQueue()

	const goroutines = 50
	const intentsPerGoroutine = 10

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < intentsPerGoroutine; j++ {
				q.Enqueue(NewInstallAppIntent("app"))
			}
		}()
	}

	wg.Wait()

	items := q.Drain()
	assert.Len(t, items, goroutines*intentsPerGoroutine)
}

// ============================================================================
// WaitAndDrain Tests
// ============================================================================

func TestIntentQueue_WaitAndDrain_BlocksUntilIntentArrives(t *testing.T) {
	q := newTestQueue()
	ctx := context.Background()

	done := make(chan []Intent, 1)
	go func() {
		done <- q.WaitAndDrain(ctx)
	}()

	// Ensure the goroutine is blocked.
	select {
	case <-done:
		t.Fatal("WaitAndDrain should block until an intent arrives")
	case <-time.After(50 * time.Millisecond):
	}

	q.Enqueue(NewInstallAppIntent("jellyfin"))

	select {
	case items := <-done:
		require.Len(t, items, 1)
	case <-time.After(time.Second):
		t.Fatal("WaitAndDrain did not return after enqueue")
	}
}

func TestIntentQueue_WaitAndDrain_DebounceAccumulatesMultipleIntents(t *testing.T) {
	q := newTestQueue()
	ctx := context.Background()

	done := make(chan []Intent, 1)
	go func() {
		done <- q.WaitAndDrain(ctx)
	}()

	// Enqueue 3 intents rapidly within the debounce window.
	q.Enqueue(NewInstallAppIntent("a"))
	q.Enqueue(NewInstallAppIntent("b"))
	q.Enqueue(NewInstallAppIntent("c"))

	select {
	case items := <-done:
		assert.Len(t, items, 3)
	case <-time.After(time.Second):
		t.Fatal("WaitAndDrain did not return after debounce")
	}
}

func TestIntentQueue_WaitAndDrain_DebounceResetsOnNewIntent(t *testing.T) {
	q := newTestQueue()
	ctx := context.Background()

	done := make(chan []Intent, 1)
	go func() {
		done <- q.WaitAndDrain(ctx)
	}()

	// First intent starts the debounce timer.
	q.Enqueue(NewInstallAppIntent("first"))

	// Wait for most of the debounce window, then enqueue another — this should
	// reset the timer so both intents are returned together.
	time.Sleep(testDebounce * 3 / 4)
	q.Enqueue(NewInstallAppIntent("second"))

	select {
	case items := <-done:
		assert.Len(t, items, 2, "both intents should be returned in a single drain")
	case <-time.After(time.Second):
		t.Fatal("WaitAndDrain did not return after debounce reset")
	}
}

func TestIntentQueue_WaitAndDrain_ContextCancellation_BeforeIntentArrives(t *testing.T) {
	q := newTestQueue()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan []Intent, 1)
	go func() {
		done <- q.WaitAndDrain(ctx)
	}()

	// Cancel before any intent is enqueued.
	cancel()

	select {
	case items := <-done:
		assert.Nil(t, items)
	case <-time.After(time.Second):
		t.Fatal("WaitAndDrain did not return after context cancellation")
	}
}

func TestIntentQueue_WaitAndDrain_ContextCancellation_DuringDebounce_ReturnsPending(t *testing.T) {
	q := newTestQueue()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan []Intent, 1)
	go func() {
		done <- q.WaitAndDrain(ctx)
	}()

	// Enqueue an intent, then cancel during the debounce window.
	q.Enqueue(NewInstallAppIntent("jellyfin"))
	time.Sleep(testDebounce / 2)
	cancel()

	select {
	case items := <-done:
		require.NotNil(t, items, "should return pending intents on cancellation during debounce")
		assert.Len(t, items, 1)
	case <-time.After(time.Second):
		t.Fatal("WaitAndDrain did not return after context cancellation during debounce")
	}
}

func TestIntentQueue_WaitAndDrain_MixedIntentTypes_DrainedAsInterface(t *testing.T) {
	q := newTestQueue()
	ctx := context.Background()

	q.Enqueue(NewInstallAppIntent("jellyfin"))
	q.Enqueue(NewUninstallAppIntent("radarr", true))
	q.Enqueue(NewSetTailnetIntent("home", "tailscale", "key", "url"))

	done := make(chan []Intent, 1)
	go func() {
		done <- q.WaitAndDrain(ctx)
	}()

	select {
	case items := <-done:
		require.Len(t, items, 3)

		_, ok0 := items[0].(InstallAppIntent)
		assert.True(t, ok0, "first item should be InstallAppIntent")

		_, ok1 := items[1].(UninstallAppIntent)
		assert.True(t, ok1, "second item should be UninstallAppIntent")

		_, ok2 := items[2].(SetTailnetIntent)
		assert.True(t, ok2, "third item should be SetTailnetIntent")
	case <-time.After(time.Second):
		t.Fatal("WaitAndDrain did not return")
	}
}
