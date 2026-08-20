// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
)

// RemoteAppsModule encapsulates remote app management.
type remoteAppsModule struct {
	remoteAppStore store.RemoteAppStoreInterface
	catalog        catalog.CacheInterface
	orch           orchestratorCaller
	logger         *slog.Logger
}

// NewRemoteAppsModule creates a new RemoteAppsModule.
func NewRemoteAppsModule(
	remoteAppStore store.RemoteAppStoreInterface,
	catalog catalog.CacheInterface,
	orch orchestratorCaller,
	logger *slog.Logger,
) *remoteAppsModule {
	return &remoteAppsModule{
		remoteAppStore: remoteAppStore,
		catalog:        catalog,
		orch:           orch,
		logger:         logger,
	}
}

// List returns all remote apps.
func (m *remoteAppsModule) List() ([]*store.RemoteApp, error) {
	apps, err := m.remoteAppStore.List()
	if err != nil {
		return nil, fmt.Errorf("list remote apps: %w", err)
	}
	if apps == nil {
		apps = []*store.RemoteApp{}
	}
	return apps, nil
}

// Add validates and enqueues an add remote app intent.
func (m *remoteAppsModule) Add(appID, tailnetAddr, hostLabel string) (*IntentRef, error) {
	if appID == "" {
		return nil, fmt.Errorf("appId is required")
	}
	if tailnetAddr == "" {
		return nil, fmt.Errorf("tailnetAddr is required")
	}
	if hostLabel == "" {
		return nil, fmt.Errorf("hostLabel is required")
	}
	if _, err := m.catalog.Get(appID); err != nil {
		return nil, fmt.Errorf("unknown app: %s", appID)
	}
	if m.orch == nil {
		return nil, fmt.Errorf("orchestrator not available")
	}
	intent := orchestrator.NewAddRemoteAppIntent(appID, tailnetAddr, hostLabel)
	m.orch.Enqueue(intent)
	m.logger.Info("add remote app intent enqueued", "app", appID)
	return &IntentRef{ID: intent.IntentID()}, nil
}

// Delete validates and enqueues a delete remote app intent.
func (m *remoteAppsModule) Delete(id string) (*IntentRef, error) {
	app, err := m.remoteAppStore.GetByID(id)
	if err != nil || app == nil {
		return nil, fmt.Errorf("%w: %s", errRemoteAppNotFound, id)
	}
	if m.orch == nil {
		return nil, fmt.Errorf("orchestrator not available")
	}
	intent := orchestrator.NewDeleteRemoteAppIntent(id)
	m.orch.Enqueue(intent)
	m.logger.Info("delete remote app intent enqueued", "id", id)
	return &IntentRef{ID: intent.IntentID()}, nil
}

// NewRemoteAppsRouter registers remote app routes on the given router.
func NewRemoteAppsRouter(mod *remoteAppsModule, r chi.Router) {
	r.Get("/sharing/remote-apps", mod.ListHandler())
	r.Post("/sharing/remote-apps", mod.AddHandler())
	r.Delete("/sharing/remote-apps/{id}", mod.DeleteHandler())
}

// ListHandler returns all remote apps.
func (m *remoteAppsModule) ListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apps, err := m.List()
		if err != nil {
			m.logger.Error("failed to list remote apps", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to list remote apps")
			return
		}
		respondJSON(w, http.StatusOK, apps)
	}
}

// AddHandler creates a remote app.
func (m *remoteAppsModule) AddHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AppID       string `json:"appId"`
			TailnetAddr string `json:"tailnetAddr"`
			HostLabel   string `json:"hostLabel"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		ref, err := m.Add(req.AppID, req.TailnetAddr, req.HostLabel)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusAccepted, map[string]string{"intentId": ref.ID})
	}
}

// DeleteHandler removes a remote app.
func (m *remoteAppsModule) DeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		ref, err := m.Delete(id)
		if err != nil {
			if errors.Is(err, errRemoteAppNotFound) {
				respondError(w, http.StatusNotFound, "remote app not found")
				return
			}
			respondError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		respondJSON(w, http.StatusAccepted, map[string]string{"intentId": ref.ID})
	}
}
