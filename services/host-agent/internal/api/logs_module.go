// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/system"
	"github.com/go-chi/chi/v5"
)

// LogsModule encapsulates log streaming operations.
type logsModule struct {
	appStore store.AppStoreInterface
	logger   *slog.Logger
}

// NewLogsModule creates a new LogsModule.
func NewLogsModule(appStore store.AppStoreInterface, logger *slog.Logger) *logsModule {
	return &logsModule{appStore: appStore, logger: logger}
}

// CanStream checks if the named app exists in the store.
func (m *logsModule) CanStream(name string) error {
	app, err := m.appStore.GetByCatalogID(name)
	if err != nil || app == nil {
		return fmt.Errorf("app not found: %s", name)
	}
	return nil
}

// StreamLogsHandler streams app logs via SSE using podman logs.
// Both SSE routes are registered in the router's streaming group (no request
// timeout); see NewRouter.
func (m *logsModule) StreamLogsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")

		if err := m.CanStream(name); err != nil {
			respondError(w, http.StatusNotFound, "App not found")
			return
		}

		ctx := r.Context()

		// Resolve the primary container for the app. The host-agent owns
		// containers directly (no systemd units), so we look them up by the
		// io.bloud.app label rather than a fixed name.
		lookup := exec.CommandContext(ctx, "podman", "ps", "-a",
			"--filter", "label=io.bloud.app="+name,
			"--format", "{{.Names}}")
		lookupOut, err := lookup.Output()
		if err != nil {
			m.logger.Error("failed to resolve app container", "app", name, "error", err)
			respondError(w, http.StatusInternalServerError, "Failed to resolve app container")
			return
		}
		containerName := strings.TrimSpace(string(lookupOut))
		if containerName == "" {
			respondError(w, http.StatusNotFound, "App has no managed containers")
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

		m.logger.Info("SSE client connected for app logs", "app", name)

		cmd := exec.CommandContext(ctx, "podman", "logs",
			"-f",
			"-n", "100",
			"--timestamps",
			containerName,
		)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			m.logger.Error("failed to create stdout pipe", "error", err)
			respondError(w, http.StatusInternalServerError, "Failed to start log stream")
			return
		}

		if err := cmd.Start(); err != nil {
			m.logger.Error("failed to start podman logs", "error", err)
			respondError(w, http.StatusInternalServerError, "Failed to start log stream")
			return
		}

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()

			select {
			case <-ctx.Done():
				m.logger.Info("SSE client disconnected from app logs", "app", name)
				return
			default:
			}
		}

		if err := scanner.Err(); err != nil {
			m.logger.Error("scanner error reading logs", "error", err)
		}

		cmd.Wait()
		m.logger.Info("log stream ended", "app", name)
	}
}

// SystemStatusStreamHandler streams system stats via SSE.
func (m *logsModule) SystemStatusStreamHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		m.logger.Info("SSE client connected for system stats")

		for {
			select {
			case <-ctx.Done():
				m.logger.Info("SSE client disconnected")
				return
			case <-ticker.C:
				stats, err := system.GetStats()
				if err != nil {
					m.logger.Error("failed to get system stats for SSE", "error", err)
					continue
				}
				data, err := jsonMarshal(stats)
				if err != nil {
					m.logger.Error("failed to marshal stats for SSE", "error", err)
					continue
				}
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}

// jsonMarshal marshals v to JSON bytes. Kept as a function for testability.
func jsonMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}
