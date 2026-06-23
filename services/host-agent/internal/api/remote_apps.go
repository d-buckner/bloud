package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// addRemoteAppRequest is the request body for POST /api/sharing/remote-apps.
type addRemoteAppRequest struct {
	AppID       string `json:"appId"`       // catalog app identifier (e.g. "jellyfin")
	TailnetAddr string `json:"tailnetAddr"` // sidecar tailnet address (e.g. "ts-jellyfin.tail1275sa.ts.net")
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
	catalogApp, err := s.catalog.Get(req.AppID)
	if err != nil {
		respondError(w, http.StatusBadRequest, "unknown app: "+req.AppID)
		return
	}

	id := uuid.New().String()

	ssoStrategy := catalogApp.SSO.Strategy
	bypassPaths := catalogApp.SSO.BypassPaths
	if bypassPaths == nil {
		bypassPaths = []string{}
	}

	app := store.RemoteApp{
		ID:                 id,
		HostLabel:          req.HostLabel,
		AppID:              req.AppID,
		AppName:            catalogApp.DisplayName,
		SSOStrategy:        ssoStrategy,
		BypassPaths:        bypassPaths,
		SidecarTailnetAddr: req.TailnetAddr,
		Status:             "active",
	}

	if err := s.remoteAppStore.Create(app); err != nil {
		s.logger.Error("failed to create remote app", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to create remote app")
		return
	}

	// Regenerate Traefik routes to include the new remote app
	if s.orchestrator != nil {
		if err := s.orchestrator.RegenerateRoutes(); err != nil {
			s.logger.Warn("failed to regenerate routes after adding remote app", "error", err)
		}
	}

	respondJSON(w, http.StatusCreated, app)
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

	if err := s.remoteAppStore.Delete(id); err != nil {
		s.logger.Error("failed to delete remote app", "id", id, "error", err)
		respondError(w, http.StatusNotFound, "remote app not found")
		return
	}

	// Regenerate Traefik routes to remove the deleted remote app
	if s.orchestrator != nil {
		if err := s.orchestrator.RegenerateRoutes(); err != nil {
			s.logger.Warn("failed to regenerate routes after removing remote app", "error", err)
		}
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status": "deleted",
	})
}

var slugRegex = regexp.MustCompile(`[^a-z0-9]+`)

// slugify converts a string to a URL-safe slug.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = slugRegex.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}
