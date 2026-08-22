// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"github.com/go-chi/chi/v5"
)

// sseHeartbeatInterval is how often an SSE comment line is sent to keep
// idle connections alive through intermediaries.
const sseHeartbeatInterval = 30 * time.Second

// eventsModule streams live host state to dashboard subscribers over SSE.
// It is strictly read-only: the orchestrator and stores own state (single
// writer invariant); this module only broadcasts changes.
type eventsModule struct {
	bus    *eventbus.Bus
	layout func(username string) (*homeResponse, error)
	logger *slog.Logger
}

// NewEventsModule creates an eventsModule. layout provides the full home
// payload (apps + positions) used for snapshot events.
func NewEventsModule(bus *eventbus.Bus, layout func(username string) (*homeResponse, error), logger *slog.Logger) *eventsModule {
	return &eventsModule{bus: bus, layout: layout, logger: logger}
}

// NewEventsRouter registers the SSE event stream route. It must be mounted
// on a router without a request timeout, because the stream outlives a
// single request (see NewRouter for the middleware split).
func NewEventsRouter(mod *eventsModule, r chi.Router) {
	r.Get("/apps/events", mod.StreamHandler())
}

// StreamHandler serves the live event stream:
//
//	snapshot  full home payload; sent on connect, after any app-store change,
//	            and as a forced resync if a subscriber fell behind
//	node      per-container lifecycle transition (phase + error)
//	activity  orchestrator activity entry
//	pull      image pull progress
//
// The snapshot-on-connect is the entire reconnect story: EventSource
// reconnects automatically and the client re-applies full state.
func (m *eventsModule) StreamHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		username := ""
		if user := getUserFromContext(r.Context()); user != nil {
			username = user.Username
		}

		if err := m.writeSnapshot(w, flusher, username); err != nil {
			m.logger.Debug("events stream: initial snapshot failed", "user", username, "error", err)
			return
		}

		events, cancel := m.bus.Subscribe()
		defer cancel()

		heartbeat := time.NewTicker(sseHeartbeatInterval)
		defer heartbeat.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-heartbeat.C:
				// SSE comment: keeps idle connections alive.
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			case evt, ok := <-events:
				if !ok {
					return
				}
				switch evt.Type {
				case eventbus.TypeAppsChanged:
					if err := m.writeSnapshot(w, flusher, username); err != nil {
						return
					}
				case eventbus.TypeNode:
					if err := writeSSE(w, flusher, "node", evt.Node); err != nil {
						return
					}
				case eventbus.TypeActivity:
					if err := writeSSE(w, flusher, "activity", evt.Activity); err != nil {
						return
					}
				case eventbus.TypePull:
					if err := writeSSE(w, flusher, "pull", evt.Pull); err != nil {
						return
					}
				}
			}
		}
	}
}

// writeSnapshot sends the full home payload as a snapshot event. The client
// applies it as the source of truth.
func (m *eventsModule) writeSnapshot(w http.ResponseWriter, flusher http.Flusher, username string) error {
	data, err := m.layout(username)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return writeSSERaw(w, flusher, "snapshot", payload)
}

// writeSSE writes one SSE event with a JSON-marshalled payload.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return writeSSERaw(w, flusher, event, b)
}

// writeSSERaw writes one SSE event with a pre-marshalled JSON payload.
func writeSSERaw(w http.ResponseWriter, flusher http.Flusher, event string, payload []byte) error {
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
