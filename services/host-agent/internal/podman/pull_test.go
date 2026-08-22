// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamPullHandler serves a canned pull event stream on the pull endpoint.
func streamPullHandler(t *testing.T, events []string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/libpod/images/pull" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		assert.Equal(t, "true", r.URL.Query().Get("decoding"))
		assert.NotEmpty(t, r.URL.Query().Get("reference"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, evt := range events {
			fmt.Fprintln(w, evt)
			flusher.Flush()
		}
	})
}

// collectProgress records every progress update delivered to onProgress.
type collectProgress struct {
	updates []PullProgress
}

func (c *collectProgress) fn() func(PullProgress) {
	return func(p PullProgress) {
		c.updates = append(c.updates, p)
	}
}

func TestPullImageWithProgressStreamsPercentAndDone(t *testing.T) {
	events := []string{
		`{"id":"blob1","status":"Copying blob blob1","progress":100,"progressDetail":{"start":0,"current":100,"total":300}}`,
		`{"id":"blob1","status":"Copying blob blob1","progress":200,"progressDetail":{"start":0,"current":200,"total":300}}`,
		`{"id":"blob1","status":"Copying blob blob1","progress":300,"progressDetail":{"start":0,"current":300,"total":300}}`,
		`{"id":"blob2","status":"Copying blob blob2","progress":150,"progressDetail":{"start":0,"current":150,"total":300}}`,
		`{"id":"blob2","status":"Copying blob blob2","progress":300,"progressDetail":{"start":0,"current":300,"total":300}}`,
		`{"status":"Writing image sha256:abc"}`,
		`{"status":"Image pulled"}`,
	}

	socketPath, cleanup := setupMockPodman(t, streamPullHandler(t, events))
	defer cleanup()

	client, err := NewClientWithSocket(socketPath)
	require.NoError(t, err)

	collected := &collectProgress{}
	require.NoError(t, client.PullImageWithProgress(context.Background(), "docker.io/jellyfin/jellyfin:10.11.11", collected.fn()))

	// All progress events land in one throttle window: the first emits
	// immediately, the rest coalesce into the final flush, then the done
	// event. The first update only knows blob1 (300 bytes) because blob2
	// hasn't been announced yet.
	require.GreaterOrEqual(t, len(collected.updates), 2)
	for i, u := range collected.updates[:len(collected.updates)-1] {
		assert.Equal(t, "pulling", u.Phase, "update %d", i)
	}
	first := collected.updates[0]
	assert.Equal(t, 33, first.Percent, "100 of 300 known bytes pulled")
	assert.Equal(t, int64(100), first.Current)
	assert.Equal(t, int64(300), first.Total)
	flushed := collected.updates[len(collected.updates)-2]
	assert.Equal(t, 100, flushed.Percent)
	assert.Equal(t, int64(600), flushed.Total)

	last := collected.updates[len(collected.updates)-1]
	assert.Equal(t, "done", last.Phase)
	assert.Equal(t, 100, last.Percent)
	assert.Equal(t, "Image pulled", last.Detail)

	// Percentages are monotonic non-decreasing.
	for i := 1; i < len(collected.updates); i++ {
		assert.GreaterOrEqual(t, collected.updates[i].Percent, collected.updates[i-1].Percent)
	}
}

func TestPullImageWithProgressAlreadyExists(t *testing.T) {
	events := []string{
		`{"id":"docker.io/library/nginx:1.25","status":"Image exists"}`,
	}

	socketPath, cleanup := setupMockPodman(t, streamPullHandler(t, events))
	defer cleanup()

	client, err := NewClientWithSocket(socketPath)
	require.NoError(t, err)

	collected := &collectProgress{}
	require.NoError(t, client.PullImageWithProgress(context.Background(), "nginx:1.25", collected.fn()))

	// No blob sizes: no fake progress, just the done event.
	require.Len(t, collected.updates, 1)
	assert.Equal(t, "done", collected.updates[0].Phase)
	assert.Equal(t, 0, collected.updates[0].Percent)
	assert.Equal(t, "Image exists", collected.updates[0].Detail)
}

func TestPullImageWithProgressStreamErrorIsDefinitive(t *testing.T) {
	events := []string{
		`{"error":"manifest for docker.io/library/missing:v1 not found: manifest unknown"}`,
	}

	socketPath, cleanup := setupMockPodman(t, streamPullHandler(t, events))
	defer cleanup()

	runner := &fakeCommandRunner{}
	client, err := NewClientWithSocket(socketPath)
	require.NoError(t, err)
	client.runner = runner

	collected := &collectProgress{}
	err = client.PullImageWithProgress(context.Background(), "docker.io/library/missing:v1", collected.fn())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "manifest unknown")

	// A definitive pull failure must not be retried via the CLI.
	assert.Empty(t, runner.commands)
}

func TestPullImageWithProgressFallsBackToExecOnInfraFailure(t *testing.T) {
	socketPath, cleanup := setupMockPodman(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer cleanup()

	runner := &fakeCommandRunner{}
	client, err := NewClientWithSocket(socketPath)
	require.NoError(t, err)
	client.runner = runner

	collected := &collectProgress{}
	require.NoError(t, client.PullImageWithProgress(context.Background(), "nginx:1.25", collected.fn()))

	require.Equal(t, [][]string{{"podman", "pull", "nginx:1.25"}}, runner.commands)
	require.NotEmpty(t, collected.updates)
	last := collected.updates[len(collected.updates)-1]
	assert.Equal(t, "done", last.Phase)
}

func TestPullImageWithProgressCancelledSkipsFallback(t *testing.T) {
	// Point the client at a socket that does not exist so the pull request
	// fails; a cancelled context must surface without the CLI fallback.
	runner := &fakeCommandRunner{}
	client := &Client{socketPath: "/nonexistent/bloud-test.sock", runner: runner}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	collected := &collectProgress{}
	err := client.PullImageWithProgress(ctx, "nginx:1.25", collected.fn())
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, runner.commands)
}

func TestPullImageWithProgressRejectsUnsafeImageReference(t *testing.T) {
	runner := &fakeCommandRunner{}
	client := &Client{runner: runner}

	err := client.PullImageWithProgress(context.Background(), "image\n--flag", nil)
	require.ErrorContains(t, err, "invalid image reference")
	assert.Empty(t, runner.commands)
}

func TestPullTrackerAggregatesBlobs(t *testing.T) {
	tracker := newPullTracker()

	add := func(id string, current, total int64) PullProgress {
		var evt pullStreamEvent
		evt.ID = id
		evt.Progress = current
		evt.ProgressDetail.Current = current
		evt.ProgressDetail.Total = total
		evt.Status = fmt.Sprintf("Copying blob %s", id)
		return tracker.add(evt)
	}

	var statusOnly pullStreamEvent
	require.NoError(t, json.Unmarshal([]byte(`{"status":"Downloading"}`), &statusOnly))
	assert.Equal(t, 0, tracker.add(statusOnly).Percent)

	p := add("a", 50, 200)
	assert.Equal(t, 25, p.Percent)
	assert.Equal(t, int64(50), p.Current)
	assert.Equal(t, int64(200), p.Total)

	// Second blob arrives: total grows, no bytes done yet.
	p = add("b", 0, 200)
	assert.Equal(t, 0, p.Percent)
	assert.Equal(t, int64(400), p.Total)

	// First blob completes.
	p = add("a", 200, 200)
	assert.Equal(t, 50, p.Percent)

	// Second blob completes.
	p = add("b", 200, 200)
	assert.Equal(t, 100, p.Percent)

	done := tracker.doneProgress()
	assert.Equal(t, "done", done.Phase)
	assert.Equal(t, 100, done.Percent)
	assert.Equal(t, int64(400), done.Total)
}

func TestPullThrottlerCoalescesBursts(t *testing.T) {
	throttler := newPullThrottler(500 * time.Millisecond)
	var delivered []PullProgress
	emit := func(p PullProgress) { delivered = append(delivered, p) }

	for i := 0; i < 10; i++ {
		throttler.emit(PullProgress{Phase: "pulling", Percent: i * 10}, emit)
	}
	require.Len(t, delivered, 1, "burst coalesces to one emit")
	assert.Equal(t, 0, delivered[0].Percent)

	throttler.flush(emit)
	require.Len(t, delivered, 2, "flush delivers the last coalesced update")
	assert.Equal(t, 90, delivered[1].Percent)

	// After the interval passes, the next emit goes through immediately.
	time.Sleep(550 * time.Millisecond)
	throttler.emit(PullProgress{Phase: "pulling", Percent: 100}, emit)
	assert.Len(t, delivered, 3)
}
