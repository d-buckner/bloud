// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/go-chi/chi/v5"
)

func TestLogsModule_CanStream_ExistingApp(t *testing.T) {
	appStore := NewFakeAppStore()
	appStore.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin"})
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewLogsModule(appStore, logger)
	err := mod.CanStream("jellyfin")
	assert.NoError(t, err)
}

func TestLogsModule_CanStream_NonExistentApp(t *testing.T) {
	appStore := NewFakeAppStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewLogsModule(appStore, logger)
	err := mod.CanStream("nonexistent")
	assert.Error(t, err)
}

func TestLogsHTTP_StreamLogs_NotFound(t *testing.T) {
	appStore := NewFakeAppStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewLogsModule(appStore, logger)
	logsMod := mod
	r := chi.NewRouter(); NewLogsRouter(logsMod, r)

	req := httptest.NewRequest("GET", "/apps/nonexistent/logs", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}
