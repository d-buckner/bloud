package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// SharingModule encapsulates all sharing operations: community graph, shares,
// invites, and guests management.
type SharingModule interface {
	CommunityGraphHandler() http.HandlerFunc
	CreateInviteHandler() http.HandlerFunc
	ListSharesHandler() http.HandlerFunc
	RevokeShareHandler() http.HandlerFunc
	ListGuestsHandler() http.HandlerFunc
	CreateGuestHandler() http.HandlerFunc
}

type sharingModule struct {
	shareStore    store.ShareStoreInterface
	guestStore    store.GuestStoreInterface
	appStore      store.AppStoreInterface
	catalog       catalog.CacheInterface
	tailnetNode   sharing.TailnetNodeManagerInterface
	hostLabel     string
	ssoHostSecret string
	logger        *slog.Logger
}

// NewSharingModule creates a new SharingModule.
func NewSharingModule(
	shareStore store.ShareStoreInterface,
	guestStore store.GuestStoreInterface,
	appStore store.AppStoreInterface,
	catalog catalog.CacheInterface,
	tailnetNode sharing.TailnetNodeManagerInterface,
	hostLabel string,
	ssoHostSecret string,
	logger *slog.Logger,
) SharingModule {
	return &sharingModule{
		shareStore:    shareStore,
		guestStore:    guestStore,
		appStore:      appStore,
		catalog:       catalog,
		tailnetNode:   tailnetNode,
		hostLabel:     hostLabel,
		ssoHostSecret: ssoHostSecret,
		logger:        logger,
	}
}

// ---- Community Graph ----

// CommunityGraphHandler returns a graph of host → apps → guests for active shares.
func (m *sharingModule) CommunityGraphHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		shares, err := m.shareStore.List()
		if err != nil {
			m.logger.Error("failed to list shares", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to list shares")
			return
		}

		// Build app ID → catalog_id lookup from installed apps
		allApps, err := m.appStore.GetAll()
		if err != nil {
			m.logger.Error("failed to list apps", "error", err)
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
		hostLabel := m.hostLabel
		if hostLabel == "" {
			hostLabel = "My Server"
		}
		nodeMap["__host__"] = communityNode{
			ID:       "__host__",
			Label:    hostLabel,
			NodeType: "person",
		}

		guests, err := m.guestStore.List()
		if err != nil {
			m.logger.Error("failed to list guests", "error", err)
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

			installedApp, ok := appByDBID[share.AppID]
			if !ok {
				continue
			}
			catalogID := installedApp.CatalogID

			// App node
			appNodeID := "app:" + catalogID
			if _, exists := nodeMap[appNodeID]; !exists {
				displayName := catalogID
				if catalogApp, err := m.catalog.Get(catalogID); err == nil {
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

		respondJSON(w, http.StatusOK, communityGraphResponse{
			Nodes: nodes,
			Edges: edges,
		})
	}
}

// ---- Invites ----

// CreateInviteHandler creates an invite token for sharing an app.
func (m *sharingModule) CreateInviteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createInviteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		if req.AppID == "" {
			respondError(w, http.StatusBadRequest, "appId is required")
			return
		}

		if req.GuestID == "" {
			respondError(w, http.StatusBadRequest, "guestId is required")
			return
		}

		if req.NodeShareLink == "" {
			respondError(w, http.StatusBadRequest, "nodeShareLink is required")
			return
		}

		// Validate app is installed and get its integer ID
		app, err := m.appStore.GetByCatalogID(req.AppID)
		if err != nil || app == nil {
			respondError(w, http.StatusNotFound, "app not installed")
			return
		}

		// Get display name from catalog
		catalogApp, err := m.catalog.Get(req.AppID)
		if err != nil {
			respondError(w, http.StatusNotFound, "app not found in catalog")
			return
		}

		// Validate guest exists
		guest, err := m.guestStore.GetByID(req.GuestID)
		if err != nil || guest == nil {
			respondError(w, http.StatusBadRequest, "guest not found")
			return
		}

		// Get tailnet node address
		if m.tailnetNode == nil {
			respondError(w, http.StatusServiceUnavailable, "sharing not available: tailnet node manager not configured")
			return
		}

		addr, err := m.tailnetNode.GetAddr(r.Context(), req.AppID)
		if err != nil {
			m.logger.Error("failed to get tailnet node address", "app", req.AppID, "error", err)
			respondError(w, http.StatusServiceUnavailable, "tailnet node not ready")
			return
		}

		shareID := uuid.New().String()

		share := store.Share{
			ID:            shareID,
			AppID:         app.ID,
			SSOStrategy:   catalogApp.SSO.Strategy,
			GuestID:       req.GuestID,
			NodeShareLink: req.NodeShareLink,
			Status:        "active",
		}
		if err := m.shareStore.Create(share); err != nil {
			m.logger.Error("failed to create share", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to create share")
			return
		}

		payload := sharing.InvitePayload{
			AppID:         req.AppID,
			AppName:       catalogApp.DisplayName,
			HostLabel:     m.hostLabel,
			TailnetAddr:   addr,
			NodeShareLink: req.NodeShareLink,
		}

		token, err := sharing.GenerateToken(payload, m.ssoHostSecret)
		if err != nil {
			m.logger.Error("failed to generate invite token", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to generate token")
			return
		}

		respondJSON(w, http.StatusOK, createInviteResponse{
			ShareID: shareID,
			Token:   token,
		})
	}
}

// ---- Shares ----

// ListSharesHandler returns all shares.
func (m *sharingModule) ListSharesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		shares, err := m.shareStore.List()
		if err != nil {
			m.logger.Error("failed to list shares", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to list shares")
			return
		}

		if shares == nil {
			shares = []*store.Share{}
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"shares": shares,
		})
	}
}

// RevokeShareHandler revokes a share by ID.
func (m *sharingModule) RevokeShareHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")

		if err := m.shareStore.Revoke(id); err != nil {
			m.logger.Error("failed to revoke share", "id", id, "error", err)
			respondError(w, http.StatusNotFound, "share not found")
			return
		}

		respondJSON(w, http.StatusOK, map[string]string{
			"status": "revoked",
		})
	}
}

// ---- Guests ----

// ListGuestsHandler returns all guests.
func (m *sharingModule) ListGuestsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guests, err := m.guestStore.List()
		if err != nil {
			m.logger.Error("failed to list guests", "error", err)
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
}

// CreateGuestHandler creates a new guest.
func (m *sharingModule) CreateGuestHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		if err := m.guestStore.Create(guest); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint") {
				respondError(w, http.StatusConflict, "guest with this name already exists")
				return
			}
			m.logger.Error("failed to create guest", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to create guest")
			return
		}

		respondJSON(w, http.StatusCreated, guest)
	}
}

// ---- Router ----

// NewSharingRouter returns a chi.Router with all sharing-related routes.
func NewSharingRouter(mod *sharingModule) *chi.Mux {
	r := chi.NewRouter()

	r.Get("/api/sharing/community", mod.CommunityGraphHandler())
	r.Post("/api/sharing/invites", mod.CreateInviteHandler())
	r.Get("/api/sharing/shares", mod.ListSharesHandler())
	r.Delete("/api/sharing/shares/{id}", mod.RevokeShareHandler())
	r.Get("/api/sharing/guests", mod.ListGuestsHandler())
	r.Post("/api/sharing/guests", mod.CreateGuestHandler())

	return r
}
