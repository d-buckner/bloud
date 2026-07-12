package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/system"
)

// handleSystemStatusStream streams system stats via SSE
func (s *Server) handleSystemStatusStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	ctx := r.Context()
	s.logger.Info("SSE client connected for system stats")

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("SSE client disconnected")
			return
		case <-ticker.C:
			stats, err := system.GetStats()
			if err != nil {
				s.logger.Error("failed to get system stats for SSE", "error", err)
				continue
			}
			data, err := json.Marshal(stats)
			if err != nil {
				s.logger.Error("failed to marshal stats for SSE", "error", err)
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handleRebuildStream is no longer supported (Nix runtime removed).
func (s *Server) handleRebuildStream(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Rebuild not supported (Nix runtime removed)", http.StatusGone)
}
