package api

import (
	"context"
	"encoding/json"
	"net/http"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"github.com/google/uuid"
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

// handleSetTailnet creates or replaces the tailnet connection (MVP: single connection).
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

	// MVP: single connection — delete any existing before creating new.
	existing, _ := s.tailnetStore.GetActive()
	if existing != nil {
		if err := s.tailnetStore.Delete(existing.ID); err != nil {
			s.logger.Error("failed to delete existing tailnet connection", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to replace tailnet connection")
			return
		}
	}

	conn := store.TailnetConnection{
		ID:         uuid.New().String(),
		Name:       req.Name,
		Type:       req.Type,
		AuthKey:    req.AuthKey,
		ControlURL: req.ControlURL,
		Status:     "active",
	}

	if err := s.tailnetStore.Create(conn); err != nil {
		s.logger.Error("failed to create tailnet connection", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to create tailnet connection")
		return
	}

	// Start sidecars for all running non-system apps in the background.
	s.ensureSidecarsForRunningApps()

	// Ensure gateway and reconcile remote app proxies in the background.
	s.ensureGatewayAndProxies()

	respondJSON(w, http.StatusOK, toTailnetResponse(&conn))
}

// handleDeleteTailnet removes the active tailnet connection.
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

	if err := s.tailnetStore.Delete(conn.ID); err != nil {
		s.logger.Error("failed to delete tailnet connection", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to delete tailnet connection")
		return
	}

	// Stop gateway and remote proxies — tailnet connectivity is gone.
	if s.gateway != nil {
		if err := s.gateway.Stop(context.Background()); err != nil {
			s.logger.Warn("failed to stop gateway", "error", err)
		}
	}
	if s.remoteProxy != nil {
		s.remoteProxy.StopAll()
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status": "deleted",
	})
}

// ensureSidecarsForRunningApps starts sidecars for all running non-system apps.
// Called in the background after a tailnet connection is saved via the Settings API.
func (s *Server) ensureSidecarsForRunningApps() {
	if s.sidecar == nil {
		return
	}

	go func() {
		apps, err := s.appStore.GetAll()
		if err != nil {
			s.logger.Error("failed to list apps for sidecar startup", "error", err)
			return
		}

		for _, app := range apps {
			if app.IsSystem || app.Status != "running" {
				continue
			}

			catalogApp, err := s.catalog.Get(app.Name)
			if err != nil || catalogApp.Port == 0 {
				continue
			}

			if err := s.sidecar.EnsureRunning(context.Background(), app.Name, catalogApp.Port); err != nil {
				s.logger.Warn("failed to start sidecar for running app", "app", app.Name, "error", err)
				continue
			}
			if active, _ := s.tailnetStore.GetActive(); active != nil {
				s.appStore.SetTailnetID(app.Name, active.ID)
			}
			s.logger.Info("started sidecar for running app", "app", app.Name)
		}
	}()
}

// ensureGatewayAndProxies starts the gateway container and reconciles remote app
// reverse proxies. Called in the background after a tailnet connection is saved.
func (s *Server) ensureGatewayAndProxies() {
	if s.gateway == nil || s.remoteProxy == nil || s.orchestrator == nil {
		return
	}

	go func() {
		// Regenerate routes — this internally calls gateway.EnsureRunning
		// and remoteProxy.Reconcile for any configured remote apps.
		if err := s.orchestrator.RegenerateRoutes(); err != nil {
			s.logger.Warn("failed to regenerate routes after tailnet save", "error", err)
		}
	}()
}
