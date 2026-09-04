// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// sseStream reads SSE events from a stream. One reader goroutine per stream:
// creating a new bufio.Scanner per read would let abandoned goroutines steal
// bytes (and whole events) from the underlying body, and a second scanner on
// the same io.Reader is a data race. The single goroutine lives until the
// body closes (test teardown); its buffered channel keeps it unblocked.
type sseStream struct {
	events chan sseResult
}

type sseResult struct {
	event string
	data  []byte
	err   error
}

func newSSEStream(r io.Reader) *sseStream {
	s := &sseStream{events: make(chan sseResult, 64)}
	go func() {
		scanner := bufio.NewScanner(r)
		var event string
		var data []byte
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if event != "" {
					s.events <- sseResult{event: event, data: data}
					event, data = "", nil
				}
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = append(data, []byte(strings.TrimPrefix(line, "data: "))...)
			}
		}
		if err := scanner.Err(); err != nil {
			s.events <- sseResult{err: err}
		}
	}()
	return s
}

// next returns the next SSE event (name + JSON data), timing out if the
// stream stalls. Comments (": ping") are ignored by the reader loop.
func (s *sseStream) next(t *testing.T) (string, []byte) {
	t.Helper()
	select {
	case res := <-s.events:
		if res.err != nil {
			t.Fatalf("reading SSE stream: %v", res.err)
		}
		return res.event, res.data
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading SSE event")
		return "", nil
	}
}

// TestEventsModule_StreamSnapshotAndEvents verifies the handler contract at
// the module level: snapshot on connect, activity events delivered, and a
// fresh snapshot after apps-changed.
func TestEventsModule_StreamSnapshotAndEvents(t *testing.T) {
	bus := eventbus.New()
	layoutCalls := 0
	layout := func(username string) (*homeResponse, error) {
		layoutCalls++
		return &homeResponse{Apps: []appWithPosition{}, Widgets: []widgetPosition{}}, nil
	}
	mod := NewEventsModule(bus, layout, newTestSlogger())

	srv := httptest.NewServer(http.HandlerFunc(mod.StreamHandler()))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	stream := newSSEStream(resp.Body)

	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// First event must be a snapshot.
	evt, payload := stream.next(t)
	assert.Equal(t, "snapshot", evt)
	var home homeResponse
	require.NoError(t, json.Unmarshal(payload, &home))
	assert.Equal(t, 1, layoutCalls)

	// Activity events are delivered as-is.
	bus.Publish(eventbus.Event{
		Type:     eventbus.TypeActivity,
		Activity: &eventbus.ActivityInfo{Event: "converge_start", Detail: "1"},
	})
	evt, payload = stream.next(t)
	assert.Equal(t, "activity", evt)
	var act eventbus.ActivityInfo
	require.NoError(t, json.Unmarshal(payload, &act))
	assert.Equal(t, "converge_start", act.Event)

	// Node events carry the phase mapping.
	bus.Publish(eventbus.Event{
		Type: eventbus.TypeNode,
		Node: &eventbus.NodeInfo{App: "jellyfin", Container: "jellyfin", Phase: "pulling"},
	})
	evt, payload = stream.next(t)
	assert.Equal(t, "node", evt)
	var node eventbus.NodeInfo
	require.NoError(t, json.Unmarshal(payload, &node))
	assert.Equal(t, "jellyfin", node.App)
	assert.Equal(t, "pulling", node.Phase)

	// Pull progress events carry the owning app, image, phase and detail.
	bus.Publish(eventbus.Event{
		Type: eventbus.TypePull,
		Pull: &eventbus.PullInfo{App: "immich", Image: "ghcr.io/immich-app/immich-server:v1",
			Phase: "pulling", Detail: "34% — 340.0 MiB of 1.0 GiB"},
	})
	evt, payload = stream.next(t)
	assert.Equal(t, "pull", evt)
	var pull eventbus.PullInfo
	require.NoError(t, json.Unmarshal(payload, &pull))
	assert.Equal(t, "immich", pull.App)
	assert.Equal(t, "pulling", pull.Phase)
	assert.Equal(t, "34% — 340.0 MiB of 1.0 GiB", pull.Detail)

	// apps-changed triggers a fresh snapshot.
	bus.Publish(eventbus.Event{Type: eventbus.TypeAppsChanged})
	evt, _ = stream.next(t)
	assert.Equal(t, "snapshot", evt)
	assert.Equal(t, 2, layoutCalls)
}

// newEventsTestRouterMux builds a full router with fake stores (no real
// orchestrator), matching the api_test.go setup pattern.
func newEventsTestRouterMux(t *testing.T) (http.Handler, *FakeAppStore) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, initTestDB(db))

	cfg := ServerConfig{
		AppsDir:           tmpDir,
		DataDir:           tmpDir,
		TraefikDynamicDir: tmpDir,
		Port:              8080,
	}

	fCatalog := NewFakeCatalogCache()
	loader := catalog.NewLoader(tmpDir)
	require.NoError(t, fCatalog.Refresh(loader))

	fAppStore := NewFakeAppStore()

	router, _ := NewRouter(db, cfg, newTestSlogger(), func(o *routerOptions) {
		o.catalog = fCatalog
		o.appStore = fAppStore
		o.remoteAppStore = NewFakeRemoteAppStore()
		o.noOrchestrator = true
	})
	return router, fAppStore
}

// TestEventsHTTP_StreamSnapshotAndResync verifies the full router path:
// loopback auth, snapshot on connect, and resnapshot after a store change.
func TestEventsHTTP_StreamSnapshotAndResync(t *testing.T) {
	router, fAppStore := newEventsTestRouterMux(t)
	fAppStore.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", Status: "running"})

	srv := httptest.NewServer(router)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/apps/events", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	stream := newSSEStream(resp.Body)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	evt, payload := stream.next(t)
	require.Equal(t, "snapshot", evt)
	var home struct {
		Apps []map[string]any `json:"apps"`
	}
	require.NoError(t, json.Unmarshal(payload, &home))
	require.Len(t, home.Apps, 1)
	assert.Equal(t, "jellyfin", home.Apps[0]["catalog_id"])
	assert.Equal(t, "running", home.Apps[0]["status"])

	// A store change must produce a fresh snapshot reflecting new state.
	require.NoError(t, fAppStore.UpdateStatus("jellyfin", "error"))
	evt, payload = stream.next(t)
	require.Equal(t, "snapshot", evt)
	require.NoError(t, json.Unmarshal(payload, &home))
	require.Len(t, home.Apps, 1)
	assert.Equal(t, "error", home.Apps[0]["status"])
}

// TestEventsHTTP_RequiresAuth verifies the stream route sits behind the auth
// middleware (non-local, no cookie → 401).
func TestEventsHTTP_RequiresAuth(t *testing.T) {
	router, _ := newEventsTestRouterMux(t)

	req := httptest.NewRequest("GET", "/api/apps/events", nil)
	// httptest default RemoteAddr (192.0.2.1) is not loopback → no bypass.
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func newTestSlogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}
