package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// createInviteRequest is the request body for POST /api/sharing/invites.
type createInviteRequest struct {
	AppID         string `json:"appId"`
	GuestID       string `json:"guestId"`
	NodeShareLink string `json:"nodeShareLink"`
}

// createInviteResponse is the response for POST /api/sharing/invites.
type createInviteResponse struct {
	ShareID string `json:"shareId"`
	Token   string `json:"token"`
}

// communityGraphResponse is the response for GET /api/sharing/community.
type communityGraphResponse struct {
	Nodes []communityNode `json:"nodes"`
	Edges []communityEdge `json:"edges"`
}

// communityNode represents a node in the community sharing graph.
type communityNode struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	NodeType string `json:"nodeType"`          // "person" | "app"
	AppID    string `json:"appId,omitempty"`    // for app nodes: catalog app name (icon lookup)
}

// communityEdge represents a directional edge in the community graph.
type communityEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// handleCommunityGraph returns a graph of host → apps → guests for active shares.
func (s *Server) handleCommunityGraph(w http.ResponseWriter, r *http.Request) {
	shares, err := s.shareStore.List()
	if err != nil {
		s.logger.Error("failed to list shares", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to list shares")
		return
	}

	// Build app ID → catalog_id lookup from installed apps
	allApps, err := s.appStore.GetAll()
	if err != nil {
		s.logger.Error("failed to list apps", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to list apps")
		return
	}
	appByDBID := make(map[int]*store.InstalledApp, len(allApps))
	for _, a := range allApps {
		appByDBID[a.ID] = a
	}

	nodeMap := make(map[string]communityNode)
	edgeSet := make(map[string]communityEdge)

	// Add host node
	hostLabel := s.cfg.HostLabel
	if hostLabel == "" {
		hostLabel = "My Server"
	}
	nodeMap["__host__"] = communityNode{
		ID:       "__host__",
		Label:    hostLabel,
		NodeType: "person",
	}

	guests, err := s.guestStore.List()
	if err != nil {
		s.logger.Error("failed to list guests", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to list guests")
		return
	}
	guestByID := make(map[string]*store.Guest)
	for _, g := range guests {
		guestByID[g.ID] = g
	}

	for _, share := range shares {
		if share.Status != "active" {
			continue
		}

		// Resolve integer app_id to catalog_id
		installedApp, ok := appByDBID[share.AppID]
		if !ok {
			continue // orphaned share, skip
		}
		catalogID := installedApp.CatalogID

		// App node
		appNodeID := "app:" + catalogID
		if _, exists := nodeMap[appNodeID]; !exists {
			displayName := catalogID
			if catalogApp, err := s.catalog.Get(catalogID); err == nil {
				displayName = catalogApp.DisplayName
			}
			nodeMap[appNodeID] = communityNode{
				ID:       appNodeID,
				Label:    displayName,
				NodeType: "app",
				AppID:    catalogID,
			}
		}

		// Guest node
		guestNodeID := "guest:" + share.GuestID
		if _, exists := nodeMap[guestNodeID]; !exists {
			guestName := share.GuestID
			if guest, ok := guestByID[share.GuestID]; ok {
				guestName = guest.Name
			}
			nodeMap[guestNodeID] = communityNode{
				ID:       guestNodeID,
				Label:    guestName,
				NodeType: "person",
			}
		}

		// Edges (deduplicate via map key)
		hostToApp := "__host__->" + appNodeID
		if _, exists := edgeSet[hostToApp]; !exists {
			edgeSet[hostToApp] = communityEdge{Source: "__host__", Target: appNodeID}
		}

		appToGuest := appNodeID + "->" + guestNodeID
		if _, exists := edgeSet[appToGuest]; !exists {
			edgeSet[appToGuest] = communityEdge{Source: appNodeID, Target: guestNodeID}
		}
	}

	nodes := make([]communityNode, 0, len(nodeMap))
	for _, n := range nodeMap {
		nodes = append(nodes, n)
	}
	edges := make([]communityEdge, 0, len(edgeSet))
	for _, e := range edgeSet {
		edges = append(edges, e)
	}

	// If no active shares, return only the host node with empty edges
	respondJSON(w, http.StatusOK, communityGraphResponse{
		Nodes: nodes,
		Edges: edges,
	})
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

	if req.NodeShareLink == "" {
		respondError(w, http.StatusBadRequest, "nodeShareLink is required")
		return
	}

	// Validate app is installed and get its integer ID
	app, err := s.appStore.GetByCatalogID(req.AppID)
	if err != nil || app == nil {
		respondError(w, http.StatusNotFound, "app not installed")
		return
	}

	// Get display name from catalog
	catalogApp, err := s.catalog.Get(req.AppID)
	if err != nil {
		respondError(w, http.StatusNotFound, "app not found in catalog")
		return
	}

	// Validate guest exists (if provided)
	if req.GuestID != "" {
		guest, err := s.guestStore.GetByID(req.GuestID)
		if err != nil || guest == nil {
			respondError(w, http.StatusBadRequest, "guest not found")
			return
		}
	}

	// Get tailnet node address
	if s.tailnetNode == nil {
		respondError(w, http.StatusServiceUnavailable, "sharing not available: tailnet node manager not configured")
		return
	}

	addr, err := s.tailnetNode.GetAddr(r.Context(), req.AppID)
	if err != nil {
		s.logger.Error("failed to get tailnet node address", "app", req.AppID, "error", err)
		respondError(w, http.StatusServiceUnavailable, "tailnet node not ready")
		return
	}

	shareID := uuid.New().String()

	// Store the share record (app_id is the integer FK)
	share := store.Share{
		ID:            shareID,
		AppID:         app.ID,
		SSOStrategy:   catalogApp.SSO.Strategy,
		GuestID:       req.GuestID,
		NodeShareLink: req.NodeShareLink,
		Status:        "active",
	}
	if err := s.shareStore.Create(share); err != nil {
		s.logger.Error("failed to create share", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to create share")
		return
	}

	// Generate unsigned invite token
	payload := sharing.InvitePayload{
		AppID:              req.AppID,
		AppName:            catalogApp.DisplayName,
		HostLabel:          s.cfg.HostLabel,
		TailnetAddr:        addr,
		NodeShareLink:      req.NodeShareLink,
	}

	token, err := sharing.GenerateToken(payload)
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

// createGuestRequest is the request body for POST /api/sharing/guests.
type createGuestRequest struct {
	Name string `json:"name"`
}

// handleListGuests returns all guests.
func (s *Server) handleListGuests(w http.ResponseWriter, r *http.Request) {
	guests, err := s.guestStore.List()
	if err != nil {
		s.logger.Error("failed to list guests", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to list guests")
		return
	}

	if guests == nil {
		guests = []*store.Guest{}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"guests": guests,
	})
}

// handleCreateGuest creates a new guest.
func (s *Server) handleCreateGuest(w http.ResponseWriter, r *http.Request) {
	var req createGuestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondError(w, http.StatusBadRequest, "name is required")
		return
	}

	guest := store.Guest{
		ID:   uuid.New().String(),
		Name: name,
	}

	if err := s.guestStore.Create(guest); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			respondError(w, http.StatusConflict, "guest with this name already exists")
			return
		}
		s.logger.Error("failed to create guest", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to create guest")
		return
	}

	respondJSON(w, http.StatusCreated, guest)
}
