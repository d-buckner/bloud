// SPDX-License-Identifier: AGPL-3.0-only

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

func NewSharingModule(
	shareStore store.ShareStoreInterface,
	guestStore store.GuestStoreInterface,
	appStore store.AppStoreInterface,
	catalog catalog.CacheInterface,
	tailnetNode sharing.TailnetNodeManagerInterface,
	hostLabel string,
	ssoHostSecret string,
	logger *slog.Logger,
) *sharingModule {
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
		src, ok := m.loadCommunitySources(w)
		if !ok {
			return
		}

		// Add host node
		nodeMap := map[string]communityNode{
			"__host__": {ID: "__host__", Label: m.hostDisplayLabel(), NodeType: "person"},
		}
		edgeSet := make(map[string]communityEdge)

		for _, share := range src.shares {
			if share.Status != "active" {
				continue
			}

			installedApp, ok := src.appByDBID[share.AppID]
			if !ok {
				continue
			}
			appNodeID := m.communityAppNode(nodeMap, installedApp.CatalogID)
			guestNodeID := communityGuestNode(nodeMap, src.guestByID, share.GuestID)
			addCommunityEdge(edgeSet, "__host__", appNodeID)
			addCommunityEdge(edgeSet, appNodeID, guestNodeID)
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

// communitySources is the three store reads the community graph needs, with
// the shares already indexed by the keys the loop resolves.
type communitySources struct {
	shares    []*store.Share
	appByDBID map[int]*store.InstalledApp
	guestByID map[string]*store.Guest
}

// loadCommunitySources reads shares, installed apps, and guests. Any read
// failing ends the request: a graph built from a partial picture would render a
// host that appears to share nothing, which is worse than an error.
func (m *sharingModule) loadCommunitySources(w http.ResponseWriter) (communitySources, bool) {
	shares, err := m.shareStore.List()
	if err != nil {
		m.logger.Error("failed to list shares", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to list shares")
		return communitySources{}, false
	}

	// Build app ID → catalog_id lookup from installed apps
	allApps, err := m.appStore.GetAll()
	if err != nil {
		m.logger.Error("failed to list apps", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to list apps")
		return communitySources{}, false
	}
	appByDBID := make(map[int]*store.InstalledApp, len(allApps))
	for _, a := range allApps {
		appByDBID[a.ID] = a
	}

	guests, err := m.guestStore.List()
	if err != nil {
		m.logger.Error("failed to list guests", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to list guests")
		return communitySources{}, false
	}
	guestByID := make(map[string]*store.Guest)
	for _, g := range guests {
		guestByID[g.ID] = g
	}

	return communitySources{shares: shares, appByDBID: appByDBID, guestByID: guestByID}, true
}

// hostDisplayLabel is the label for the local host node, with a fallback for an
// instance that was never given a name.
func (m *sharingModule) hostDisplayLabel() string {
	if m.hostLabel == "" {
		return "My Server"
	}
	return m.hostLabel
}

// communityAppNode returns the app's node id, adding the node on first use. The
// map key is the dedupe, so N shares of one app render one app node.
func (m *sharingModule) communityAppNode(nodeMap map[string]communityNode, catalogID string) string {
	appNodeID := "app:" + catalogID
	if _, exists := nodeMap[appNodeID]; exists {
		return appNodeID
	}
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
	return appNodeID
}

// communityGuestNode returns the guest's node id, adding the node on first use
// and falling back to the raw id when the guest record is gone.
func communityGuestNode(nodeMap map[string]communityNode, guestByID map[string]*store.Guest, guestID string) string {
	guestNodeID := "guest:" + guestID
	if _, exists := nodeMap[guestNodeID]; exists {
		return guestNodeID
	}
	guestName := guestID
	if guest, ok := guestByID[guestID]; ok {
		guestName = guest.Name
	}
	nodeMap[guestNodeID] = communityNode{
		ID:       guestNodeID,
		Label:    guestName,
		NodeType: "person",
	}
	return guestNodeID
}

// addCommunityEdge records one directed edge, keyed by its endpoints so a
// repeated share of the same pair does not draw the line twice.
func addCommunityEdge(edgeSet map[string]communityEdge, source, target string) {
	key := source + "->" + target
	if _, exists := edgeSet[key]; !exists {
		edgeSet[key] = communityEdge{Source: source, Target: target}
	}
}

// ---- Invites ----

// CreateInviteHandler creates an invite token for sharing an app.
func (m *sharingModule) CreateInviteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := decodeCreateInviteRequest(w, r)
		if !ok {
			return
		}
		resolved, ok := m.resolveInvite(w, r, req)
		if !ok {
			return
		}

		shareID := uuid.New().String()
		share := store.Share{
			ID:            shareID,
			AppID:         resolved.app.ID,
			SSOStrategy:   resolved.catalogApp.SSO.Strategy,
			GuestID:       req.GuestID,
			NodeShareLink: req.NodeShareLink,
			Status:        "active",
		}
		if err := m.shareStore.Create(share); err != nil {
			m.logger.Error("failed to create share", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to create share")
			return
		}

		token, err := sharing.GenerateToken(resolved.invitePayload(req), m.ssoHostSecret)
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

// inviteResolution is what an invite needs before it can be written: the
// installed app row, its catalog entry, and the tailnet address the guest will
// dial.
type inviteResolution struct {
	app         *store.InstalledApp
	catalogApp  *catalog.App
	tailnetAddr string
	hostLabel   string
}

// invitePayload builds the signed envelope the guest redeems on the far side.
func (r inviteResolution) invitePayload(req createInviteRequest) sharing.InvitePayload {
	return sharing.InvitePayload{
		AppID:         req.AppID,
		AppName:       r.catalogApp.DisplayName,
		HostLabel:     r.hostLabel,
		TailnetAddr:   r.tailnetAddr,
		NodeShareLink: req.NodeShareLink,
	}
}

// decodeCreateInviteRequest reads the invite request and refuses it when any of
// the four things it must name is missing.
func decodeCreateInviteRequest(w http.ResponseWriter, r *http.Request) (createInviteRequest, bool) {
	var req createInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return createInviteRequest{}, false
	}
	for _, field := range []struct{ name, value string }{
		{"appId", req.AppID},
		{"guestId", req.GuestID},
		{"nodeShareLink", req.NodeShareLink},
	} {
		if field.value == "" {
			respondError(w, http.StatusBadRequest, field.name+" is required")
			return createInviteRequest{}, false
		}
	}
	return req, true
}

// resolveInvite checks the three things an invite depends on: the app is
// installed and in the catalog, the guest exists, and the tailnet node has an
// address to hand out.
func (m *sharingModule) resolveInvite(w http.ResponseWriter, r *http.Request, req createInviteRequest) (inviteResolution, bool) {
	app, err := m.appStore.GetByCatalogID(req.AppID)
	if err != nil || app == nil {
		respondError(w, http.StatusNotFound, "app not installed")
		return inviteResolution{}, false
	}

	catalogApp, err := m.catalog.Get(req.AppID)
	if err != nil {
		respondError(w, http.StatusNotFound, "app not found in catalog")
		return inviteResolution{}, false
	}

	guest, err := m.guestStore.GetByID(req.GuestID)
	if err != nil || guest == nil {
		respondError(w, http.StatusBadRequest, "guest not found")
		return inviteResolution{}, false
	}

	if m.tailnetNode == nil {
		respondError(w, http.StatusServiceUnavailable, "sharing not available: tailnet node manager not configured")
		return inviteResolution{}, false
	}

	addr, err := m.tailnetNode.GetAddr(r.Context(), req.AppID)
	if err != nil {
		m.logger.Error("failed to get tailnet node address", "app", req.AppID, "error", err)
		respondError(w, http.StatusServiceUnavailable, "tailnet node not ready")
		return inviteResolution{}, false
	}

	return inviteResolution{
		app:         app,
		catalogApp:  catalogApp,
		tailnetAddr: addr,
		hostLabel:   m.hostLabel,
	}, true
}

// ---- Shares ----

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

// NewSharingRouter registers all sharing-related routes on the given router.
func NewSharingRouter(mod *sharingModule, r chi.Router) {
	r.Get("/sharing/community", mod.CommunityGraphHandler())
	r.Post("/sharing/invites", mod.CreateInviteHandler())
	r.Get("/sharing/shares", mod.ListSharesHandler())
	r.Delete("/sharing/shares/{id}", mod.RevokeShareHandler())
	r.Get("/sharing/guests", mod.ListGuestsHandler())
	r.Post("/sharing/guests", mod.CreateGuestHandler())
}

// ---- Types ----

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
	NodeType string `json:"nodeType"`        // "person" | "app"
	AppID    string `json:"appId,omitempty"` // for app nodes: catalog app name (icon lookup)
}

// communityEdge represents a directional edge in the community graph.
type communityEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// createGuestRequest is the request body for POST /api/sharing/guests.
type createGuestRequest struct {
	Name string `json:"name"`
}
