package api

import (
	"encoding/json"
	"net/http"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// createInviteRequest is the request body for POST /api/sharing/invites.
type createInviteRequest struct {
	AppID      string `json:"appId"`
	GuestLabel string `json:"guestLabel"`
}

// createInviteResponse is the response for POST /api/sharing/invites.
type createInviteResponse struct {
	ShareID string `json:"shareId"`
	Token   string `json:"token"`
}

// handleCreateInvite creates an invite token for sharing an app.
func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	var req createInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.AppID == "" {
		respondError(w, http.StatusBadRequest, "appId is required")
		return
	}

	// Validate app is installed
	app, err := s.appStore.GetByName(req.AppID)
	if err != nil || app == nil {
		respondError(w, http.StatusNotFound, "app not installed")
		return
	}

	// Get SSO strategy and bypass paths from catalog
	catalogApp, err := s.catalog.Get(req.AppID)
	if err != nil {
		respondError(w, http.StatusNotFound, "app not found in catalog")
		return
	}

	// Get sidecar tailnet address
	if s.sidecar == nil {
		respondError(w, http.StatusServiceUnavailable, "sharing not available: sidecar manager not configured")
		return
	}

	addr, err := s.sidecar.GetAddr(r.Context(), req.AppID)
	if err != nil {
		s.logger.Error("failed to get sidecar address", "app", req.AppID, "error", err)
		respondError(w, http.StatusServiceUnavailable, "sidecar not ready")
		return
	}

	shareID := uuid.New().String()

	// Store the share record
	share := store.Share{
		ID:          shareID,
		AppID:       req.AppID,
		SSOStrategy: catalogApp.SSO.Strategy,
		GuestLabel:  req.GuestLabel,
		Status:      "active",
	}
	if err := s.shareStore.Create(share); err != nil {
		s.logger.Error("failed to create share", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to create share")
		return
	}

	// Generate invite token
	payload := sharing.InvitePayload{
		ShareID:            shareID,
		AppID:              req.AppID,
		AppName:            catalogApp.DisplayName,
		HostLabel:          s.cfg.HostLabel,
		SSOStrategy:        catalogApp.SSO.Strategy,
		BypassPaths:        catalogApp.SSO.BypassPaths,
		SidecarTailnetAddr: addr,
		Exp:                time.Now().Add(sharing.TokenExpiry).Unix(),
	}

	token, err := sharing.GenerateToken(payload, s.cfg.SSOHostSecret)
	if err != nil {
		s.logger.Error("failed to generate invite token", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	respondJSON(w, http.StatusOK, createInviteResponse{
		ShareID: shareID,
		Token:   token,
	})
}

// handleListShares returns all shares.
func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	shares, err := s.shareStore.List()
	if err != nil {
		s.logger.Error("failed to list shares", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to list shares")
		return
	}

	// Ensure JSON encodes as [] not null when empty.
	if shares == nil {
		shares = []*store.Share{}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"shares": shares,
	})
}

// handleRevokeShare revokes a share by ID.
func (s *Server) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if err := s.shareStore.Revoke(id); err != nil {
		s.logger.Error("failed to revoke share", "id", id, "error", err)
		respondError(w, http.StatusNotFound, "share not found")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status": "revoked",
	})
}
