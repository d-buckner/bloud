package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/nixgen"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/system"
)

// handleSystemStatusStream streams system stats via SSE
func (s *Server) handleSystemStatusStream(w http.ResponseWriter, r *http.Request) {
	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Make sure the response writer supports flushing
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Create a ticker for periodic updates (500ms)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// Get client disconnect notification
	ctx := r.Context()

	s.logger.Info("SSE client connected for system stats")

	for {
		select {
		case <-ctx.Done():
			// Client disconnected
			s.logger.Info("SSE client disconnected")
			return

		case <-ticker.C:
			// Get current system stats
			stats, err := system.GetStats()
			if err != nil {
				s.logger.Error("failed to get system stats for SSE", "error", err)
				continue
			}

			// Marshal to JSON
			data, err := json.Marshal(stats)
			if err != nil {
				s.logger.Error("failed to marshal stats for SSE", "error", err)
				continue
			}

			// Send SSE event
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handleRebuildStream triggers a nixos-rebuild and streams output via SSE
func (s *Server) handleRebuildStream(w http.ResponseWriter, r *http.Request) {
	// Check if we have a Nix orchestrator
	nixOrch, ok := s.orchestrator.(*orchestrator.Orchestrator)
	if !ok {
		http.Error(w, "Rebuild only available with Nix orchestrator", http.StatusServiceUnavailable)
		return
	}

	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	s.logger.Info("SSE client connected for rebuild stream")

	// Create channel for rebuild events
	events := make(chan nixgen.RebuildEvent, 100)

	// Start rebuild in background
	go nixOrch.RebuildStream(r.Context(), events)

	// Stream events to client
	for event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			s.logger.Error("failed to marshal rebuild event", "error", err)
			continue
		}

		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()

		// Check if client disconnected
		select {
		case <-r.Context().Done():
			s.logger.Info("SSE client disconnected during rebuild")
			return
		default:
		}
	}

	s.logger.Info("rebuild stream complete")
}

// handleAppEvents streams app state updates via SSE
func (s *Server) handleAppEvents(w http.ResponseWriter, r *http.Request) {
	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	s.logger.Info("SSE client connected for app events")

	// Subscribe to app updates
	ch := s.appHub.Subscribe()
	defer s.appHub.Unsubscribe(ch)

	// Send initial app list
	apps, err := s.appStore.GetAll()
	if err != nil {
		s.logger.Error("failed to get apps for SSE", "error", err)
	} else {
		data, _ := json.Marshal(apps)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// Stream updates
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("SSE client disconnected from app events")
			return
		case apps, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(apps)
			if err != nil {
				s.logger.Error("failed to marshal apps for SSE", "error", err)
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
