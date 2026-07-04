package api

import (
	"encoding/json"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/reconciler"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// tailnetResponse is the API response for a tailnet connection.
// The auth key is never exposed to the frontend.
type tailnetResponse struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	HasAuthKey bool   `json:"hasAuthKey"`
	ControlURL string `json:"controlUrl"`
	Status     string `json:"status"`
}

// setTailnetRequest is the request body for POST /api/settings/tailnet.
type setTailnetRequest struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	AuthKey    string `json:"authKey"`
	ControlURL string `json:"controlUrl"`
}

func toTailnetResponse(conn *store.TailnetConnection) tailnetResponse {
	return tailnetResponse{
		ID:         conn.ID,
		Name:       conn.Name,
		Type:       conn.Type,
		HasAuthKey: conn.AuthKey != "",
		ControlURL: conn.ControlURL,
		Status:     conn.Status,
	}
}

// handleGetTailnet returns the current active tailnet connection, or null if none.
func (s *Server) handleGetTailnet(w http.ResponseWriter, r *http.Request) {
	conn, err := s.tailnetStore.GetActive()
	if err != nil {
		s.logger.Error("failed to get tailnet connection", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get tailnet connection")
		return
	}

	if conn == nil {
		respondJSON(w, http.StatusOK, nil)
		return
	}

	respondJSON(w, http.StatusOK, toTailnetResponse(conn))
}

// handleSetTailnet validates the request and enqueues a SetTailnetIntent.
func (s *Server) handleSetTailnet(w http.ResponseWriter, r *http.Request) {
	var req setTailnetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Type != "tailscale" && req.Type != "headscale" {
		respondError(w, http.StatusBadRequest, "type must be 'tailscale' or 'headscale'")
		return
	}
	if req.AuthKey == "" {
		respondError(w, http.StatusBadRequest, "authKey is required")
		return
	}
	if req.Type == "headscale" && req.ControlURL == "" {
		respondError(w, http.StatusBadRequest, "controlUrl is required for headscale")
		return
	}

	if s.intentReconciler == nil {
		respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
		return
	}

	intent := reconciler.NewSetTailnetIntent(req.Name, req.Type, req.AuthKey, req.ControlURL)
	s.intentReconciler.Enqueue(intent)

	respondJSON(w, http.StatusAccepted, map[string]string{
		"intentId": intent.IntentID(),
	})
}

// handleDeleteTailnet validates that a connection exists and enqueues a DeleteTailnetIntent.
func (s *Server) handleDeleteTailnet(w http.ResponseWriter, r *http.Request) {
	conn, err := s.tailnetStore.GetActive()
	if err != nil {
		s.logger.Error("failed to get tailnet connection", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get tailnet connection")
		return
	}
	if conn == nil {
		respondError(w, http.StatusNotFound, "no tailnet connection configured")
		return
	}

	if s.intentReconciler == nil {
		respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
		return
	}

	intent := reconciler.NewDeleteTailnetIntent()
	s.intentReconciler.Enqueue(intent)

	respondJSON(w, http.StatusAccepted, map[string]string{
		"intentId": intent.IntentID(),
	})
}
