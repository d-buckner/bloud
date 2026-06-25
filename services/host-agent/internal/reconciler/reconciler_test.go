package reconciler

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestReconciler() *Reconciler {
	r := New(testLogger(), nil)
	r.queue = NewIntentQueue(5 * time.Millisecond)
	return r
}

func TestReconciler_Start_StartsAndStopsCleanly(t *testing.T) {
	r := newTestReconciler()

	go r.Start(context.Background())

	// Give the loop a moment to enter WaitAndDrain.
	time.Sleep(10 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Stop returned promptly.
	case <-time.After(time.Second):
		t.Fatal("Stop did not return promptly")
	}
}

func TestReconciler_Enqueue_IntentsAreDrainedByLoop(t *testing.T) {
	r := newTestReconciler()

	go r.Start(context.Background())
	defer r.Stop()

	r.Enqueue(NewInstallAppIntent("jellyfin"))

	// Wait for the loop to drain.
	require.Eventually(t, func() bool {
		return r.queue.PendingCount() == 0
	}, time.Second, 5*time.Millisecond, "queue should be drained by the loop")
}

func TestReconciler_Enqueue_MultipleIntentsWithinDebounce_DrainedTogether(t *testing.T) {
	r := newTestReconciler()

	go r.Start(context.Background())
	defer r.Stop()

	r.Enqueue(NewInstallAppIntent("jellyfin"))
	r.Enqueue(NewUninstallAppIntent("radarr", true))
	r.Enqueue(NewRenameAppIntent("sonarr", "Sonarr TV"))

	require.Eventually(t, func() bool {
		return r.queue.PendingCount() == 0
	}, time.Second, 5*time.Millisecond, "all 3 intents should be drained")
}

func TestReconciler_Stop_DrainsRemainingIntents(t *testing.T) {
	// Use a long debounce so the loop doesn't drain before Stop is called.
	r := New(testLogger(), nil)
	r.queue = NewIntentQueue(5 * time.Second)

	go r.Start(context.Background())

	// Give the loop a moment to start.
	time.Sleep(10 * time.Millisecond)

	r.Enqueue(NewInstallAppIntent("jellyfin"))

	// Stop cancels context, which causes WaitAndDrain to return pending intents.
	r.Stop()

	assert.Equal(t, 0, r.queue.PendingCount(), "queue should be empty after Stop")
}

func TestReconciler_Stop_IsIdempotent(t *testing.T) {
	r := newTestReconciler()

	go r.Start(context.Background())

	time.Sleep(10 * time.Millisecond)

	// Call Stop twice — should not panic or hang.
	r.Stop()
	r.Stop()
}

func TestReconciler_LoopProcessesMultipleBatches(t *testing.T) {
	r := newTestReconciler()

	go r.Start(context.Background())
	defer r.Stop()

	// First batch.
	r.Enqueue(NewInstallAppIntent("jellyfin"))

	require.Eventually(t, func() bool {
		return r.queue.PendingCount() == 0
	}, time.Second, 5*time.Millisecond, "first batch should be drained")

	// Second batch (proves the loop continues after processing).
	r.Enqueue(NewUninstallAppIntent("radarr", false))

	require.Eventually(t, func() bool {
		return r.queue.PendingCount() == 0
	}, time.Second, 5*time.Millisecond, "second batch should be drained")
}
