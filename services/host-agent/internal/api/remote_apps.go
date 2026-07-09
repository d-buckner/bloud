package api

import (
	"encoding/json"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
)

// addRemoteAppRequest is the request body for POST /api/sharing/remote-apps.
type addRemoteAppRequest struct {
	AppID       string `json:"appId"`       // catalog app identifier (e.g. "jellyfin")
	TailnetAddr string `json:"tailnetAddr"` // tailnet node address (e.g. "ts-jellyfin.tail1275sa.ts.net")
	HostLabel   string `json:"hostLabel"`   // display label for the remote host (e.g. "Johan's server")
}

// handleAddRemoteApp adds a shared app from a remote host.
func (s *Server) handleAddRemoteApp(w http.ResponseWriter, r *http.Request) {
	var req addRemoteAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.AppID == "" {
		respondError(w, http.StatusBadRequest, "appId is required")
		return
	}
	if req.TailnetAddr == "" {
		respondError(w, http.StatusBadRequest, "tailnetAddr is required")
		return
	}
	if req.HostLabel == "" {
		respondError(w, http.StatusBadRequest, "hostLabel is required")
		return
	}

	// Validate app exists in catalog
	if _, err := s.catalog.Get(req.AppID); err != nil {
		respondError(w, http.StatusBadRequest, "unknown app: "+req.AppID)
		return
	}

	if s.orch == nil {
		respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
		return
	}

	intent := orchestrator.NewAddRemoteAppIntent(req.AppID, req.TailnetAddr, req.HostLabel)
	s.orch.Enqueue(intent)

	respondJSON(w, http.StatusAccepted, map[string]string{
		"intentId": intent.IntentID(),
	})
}

// handleListRemoteApps returns all remote apps.
func (s *Server) handleListRemoteApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.remoteAppStore.List()
	if err != nil {
		s.logger.Error("failed to list remote apps", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to list remote apps")
		return
	}

	if apps == nil {
		apps = []*store.RemoteApp{}
	}

	respondJSON(w, http.StatusOK, apps)
}

// handleDeleteRemoteApp removes a remote app by ID.
func (s *Server) handleDeleteRemoteApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Validate remote app exists.
	app, err := s.remoteAppStore.GetByID(id)
	if err != nil || app == nil {
		respondError(w, http.StatusNotFound, "remote app not found")
		return
	}

	if s.orch == nil {
		respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
		return
	}

	intent := orchestrator.NewDeleteRemoteAppIntent(id)
	s.orch.Enqueue(intent)

	respondJSON(w, http.StatusAccepted, map[string]string{
		"intentId": intent.IntentID(),
	})
}

