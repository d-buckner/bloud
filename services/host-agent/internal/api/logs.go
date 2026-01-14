package api

import (
	"bufio"
	"fmt"
	"net/http"
	"os/exec"

	"github.com/go-chi/chi/v5"
)

// handleAppLogs streams app logs via SSE using journalctl
func (s *Server) handleAppLogs(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	// Verify app exists
	app, err := s.appStore.GetByName(name)
	if err != nil || app == nil {
		respondError(w, http.StatusNotFound, "App not found")
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

	s.logger.Info("SSE client connected for app logs", "app", name)

	// Start journalctl with context for cleanup on client disconnect
	ctx := r.Context()
	serviceName := fmt.Sprintf("podman-%s.service", name)
	cmd := exec.CommandContext(ctx, "journalctl", "--user",
		"-u", serviceName,
		"-f",           // follow (stream new entries)
		"-n", "100",    // show last 100 lines initially
		"--no-pager",
		"-o", "short-iso", // timestamp format
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.logger.Error("failed to create stdout pipe", "error", err)
		respondError(w, http.StatusInternalServerError, "Failed to start log stream")
		return
	}

	if err := cmd.Start(); err != nil {
		s.logger.Error("failed to start journalctl", "error", err)
		respondError(w, http.StatusInternalServerError, "Failed to start log stream")
		return
	}

	// Stream stdout line by line
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "data: %s\n\n", line)
		flusher.Flush()

		// Check if client disconnected
		select {
		case <-ctx.Done():
			s.logger.Info("SSE client disconnected from app logs", "app", name)
			return
		default:
		}
	}

	if err := scanner.Err(); err != nil {
		s.logger.Error("scanner error reading logs", "error", err)
	}

	cmd.Wait()
	s.logger.Info("log stream ended", "app", name)
}
