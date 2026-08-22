// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package podman

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PullProgress is one image pull progress update.
type PullProgress struct {
	// Phase is "pulling" while the image is downloading and "done" when the
	// pull finished (including the no-op case where the image is already
	// local).
	Phase string `json:"phase"`
	// Percent is the overall pull percentage (0-100) when the registry
	// reports blob sizes; 0 when unknown.
	Percent int `json:"percent,omitempty"`
	// Current and Total are the pulled and overall bytes across all blobs
	// when the registry reports sizes; 0 when unknown.
	Current int64 `json:"current,omitempty"`
	Total   int64 `json:"total,omitempty"`
	// Detail is the most recent human-readable status line from the pull
	// stream (e.g. "Copying blob sha256:…").
	Detail string `json:"detail,omitempty"`
}

// pullMinReportInterval throttles progress callbacks to ~2/s.
const pullMinReportInterval = 500 * time.Millisecond

// pullStreamEvent is one JSON event from podman's streaming pull endpoint
// (POST /libpod/images/pull?decoding=true). Only the fields Bloud consumes
// are decoded; unknown fields are ignored.
type pullStreamEvent struct {
	ID             string `json:"id"`
	From           string `json:"from"`
	Status         string `json:"status"`
	Progress       int64  `json:"progress"`
	ProgressDetail struct {
		Start   int64 `json:"start"`
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
	Error string `json:"error"`
}

// pullDefinitiveError marks a pull failure reported by podman itself (bad
// reference, registry error). Definitive failures are not retried via the
// CLI fallback — the retry would hit the same error.
type pullDefinitiveError struct{ err error }

func (e pullDefinitiveError) Error() string { return e.err.Error() }
func (e pullDefinitiveError) Unwrap() error { return e.err }

// pullTracker aggregates per-blob progress events into an overall pull
// percentage. Registries report progress per blob (each with its own
// total), so the tracker sums completed blob sizes plus the in-flight blob's
// progress over all known blob sizes.
type pullTracker struct {
	blobTotals map[string]int64
	blobDone   map[string]bool
	currentID  string
	current    int64
	lastStatus string
}

func newPullTracker() *pullTracker {
	return &pullTracker{
		blobTotals: make(map[string]int64),
		blobDone:   make(map[string]bool),
	}
}

// add records one pull stream event and returns the overall progress so far.
func (t *pullTracker) add(evt pullStreamEvent) PullProgress {
	if evt.Status != "" {
		t.lastStatus = evt.Status
	}
	if evt.ID != "" && evt.ProgressDetail.Total > 0 {
		if _, ok := t.blobTotals[evt.ID]; !ok {
			t.blobTotals[evt.ID] = evt.ProgressDetail.Total
		}
		t.currentID = evt.ID
		t.current = evt.ProgressDetail.Current
		if evt.ProgressDetail.Current >= evt.ProgressDetail.Total {
			t.blobDone[evt.ID] = true
		}
	}
	var done, total int64
	for id, size := range t.blobTotals {
		total += size
		if t.blobDone[id] {
			done += size
		}
	}
	var current int64
	if t.currentID != "" && !t.blobDone[t.currentID] {
		current = t.current
	}
	percent := 0
	if total > 0 {
		percent = int(100 * (done + current) / total)
	}
	return PullProgress{
		Phase:   "pulling",
		Percent: percent,
		Current: done + current,
		Total:   total,
		Detail:  t.lastStatus,
	}
}

// hasProgress reports whether any blob size information has been seen.
// Status-only streams (e.g. "image already exists") produce no progress.
func (t *pullTracker) hasProgress() bool {
	return len(t.blobTotals) > 0
}

// doneProgress builds the final "done" update. A stream that ended without
// error means the image is fully pulled, so the totals are reported as 100%.
func (t *pullTracker) doneProgress() PullProgress {
	var total, done int64
	for id, size := range t.blobTotals {
		total += size
		if t.blobDone[id] {
			done += size
		}
	}
	progress := PullProgress{Phase: "done", Detail: t.lastStatus}
	if total > 0 {
		progress.Percent = 100
		progress.Current = total
		progress.Total = total
	}
	return progress
}

// pullThrottler coalesces progress updates so onProgress fires at most
// ~1/interval apart. The last update inside a window is kept and flushed at
// the end of the stream so the final percent is never lost.
type pullThrottler struct {
	interval   time.Duration
	lastEmit   time.Time
	pending    PullProgress
	hasPending bool
}

func newPullThrottler(interval time.Duration) *pullThrottler {
	return &pullThrottler{interval: interval}
}

func (t *pullThrottler) emit(progress PullProgress, onProgress func(PullProgress)) {
	if time.Since(t.lastEmit) < t.interval {
		t.pending = progress
		t.hasPending = true
		return
	}
	onProgress(progress)
	t.lastEmit = time.Now()
	t.hasPending = false
	t.pending = PullProgress{}
}

func (t *pullThrottler) flush(onProgress func(PullProgress)) {
	if !t.hasPending {
		return
	}
	onProgress(t.pending)
	t.lastEmit = time.Now()
	t.hasPending = false
	t.pending = PullProgress{}
}

// PullImageWithProgress pulls an image and reports progress through
// onProgress (throttled to ~2/s, always ending in a "done" update). The pull
// streams podman's JSON progress events over the socket API. If the socket
// path is unavailable for an infrastructure reason it retries once via
// `podman pull` (no progress) so installs never regress; definitive failures
// reported inside the stream and context cancellation are returned as-is.
func (c *Client) PullImageWithProgress(ctx context.Context, image string, onProgress func(PullProgress)) error {
	if image == "" || strings.ContainsAny(image, "\r\n") {
		return fmt.Errorf("invalid image reference %q", image)
	}
	if onProgress == nil {
		onProgress = func(PullProgress) {}
	}

	err := c.pullViaSocket(ctx, image, onProgress)
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("podman pull %s: %w", image, ctxErr)
	}
	if _, definitive := err.(pullDefinitiveError); definitive {
		return err
	}

	// Socket API pull failed for an infrastructure reason. Retry via the
	// CLI so installs never regress; this path reports no progress.
	if output, execErr := c.runExecPull(ctx, image); execErr != nil {
		return fmt.Errorf("podman pull %s failed: %v; cli fallback: %w: %s", image, err, execErr, strings.TrimSpace(string(output)))
	}
	onProgress(PullProgress{Phase: "done"})
	return nil
}

// pullViaSocket streams the pull over the Podman HTTP API with decoding=true
// and reports parsed progress. It uses a dedicated HTTP client without the
// shared 30 s timeout so long pulls are bounded only by ctx.
func (c *Client) pullViaSocket(ctx context.Context, image string, onProgress func(PullProgress)) error {
	if c.socketPath == "" {
		return fmt.Errorf("podman socket not configured")
	}
	q := url.Values{}
	q.Set("reference", image)
	q.Set("decoding", "true")
	target := "http://podman/v5.0.0/libpod/images/pull?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return fmt.Errorf("build pull request: %w", err)
	}

	streamClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", c.socketPath)
			},
		},
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("pull request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("pull returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := parsePullStream(resp.Body, onProgress); err != nil {
		return err
	}
	return nil
}

// parsePullStream consumes one streamed pull response, reporting throttled
// progress updates and a final "done" update. Non-JSON lines are tolerated
// (podman versions differ in exact framing).
func parsePullStream(r io.Reader, onProgress func(PullProgress)) error {
	tracker := newPullTracker()
	throttler := newPullThrottler(pullMinReportInterval)

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var evt pullStreamEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		if evt.Error != "" {
			return pullDefinitiveError{err: fmt.Errorf("image pull failed: %s", evt.Error)}
		}
		carriedProgress := evt.ProgressDetail.Total > 0 || evt.Progress > 0
		if !carriedProgress {
			tracker.add(evt)
			continue
		}
		throttler.emit(tracker.add(evt), onProgress)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read pull stream: %w", err)
	}
	throttler.flush(onProgress)
	onProgress(tracker.doneProgress())
	return nil
}

// runExecPull pulls an image via the podman CLI (no progress reporting).
func (c *Client) runExecPull(ctx context.Context, image string) ([]byte, error) {
	if c.runner == nil {
		c.runner = execRunner{}
	}
	return c.runner.Run(ctx, "podman", "pull", image)
}
