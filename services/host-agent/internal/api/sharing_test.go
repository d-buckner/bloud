package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Fake Sidecar Manager ────────────────────────────────────────────────────

type fakeSidecar struct {
	addr    string
	addrErr error
}

func (f *fakeSidecar) EnsureRunning(_ context.Context, _ string, _ int) error { return nil }
func (f *fakeSidecar) Stop(_ context.Context, _ string) error                 { return nil }
func (f *fakeSidecar) StopAndPurge(_ context.Context, _ string) error         { return nil }
func (f *fakeSidecar) GetAddr(_ context.Context, _ string) (string, error) {
	return f.addr, f.addrErr
}

// ── Fake Share Store ────────────────────────────────────────────────────────

type fakeShareStore struct {
	shares map[string]*store.Share
}

func newFakeShareStore() *fakeShareStore {
	return &fakeShareStore{shares: make(map[string]*store.Share)}
}

func (f *fakeShareStore) Create(share store.Share) error {
	f.shares[share.ID] = &share
	return nil
}

func (f *fakeShareStore) GetByID(id string) (*store.Share, error) {
	s, ok := f.shares[id]
	if !ok {
		return nil, nil
	}
	return s, nil
}

func (f *fakeShareStore) List() ([]*store.Share, error) {
	var result []*store.Share
	for _, s := range f.shares {
		result = append(result, s)
	}
	return result, nil
}

func (f *fakeShareStore) Revoke(id string) error {
	if _, ok := f.shares[id]; !ok {
		return fmt.Errorf("share not found: %s", id)
	}
	now := time.Now()
	f.shares[id].Status = "revoked"
	f.shares[id].RevokedAt = &now
	return nil
}

// ── Fake Guest Store ────────────────────────────────────────────────────────

type fakeGuestStore struct {
	guests map[string]*store.Guest
}

func newFakeGuestStore() *fakeGuestStore {
	return &fakeGuestStore{guests: make(map[string]*store.Guest)}
}

func (f *fakeGuestStore) Create(guest store.Guest) error {
	for _, g := range f.guests {
		if g.Name == guest.Name {
			return fmt.Errorf("UNIQUE constraint failed: guests.name")
		}
	}
	f.guests[guest.ID] = &guest
	return nil
}

func (f *fakeGuestStore) GetByID(id string) (*store.Guest, error) {
	g, ok := f.guests[id]
	if !ok {
		return nil, nil
	}
	return g, nil
}

func (f *fakeGuestStore) List() ([]*store.Guest, error) {
	var result []*store.Guest
	for _, g := range f.guests {
		result = append(result, g)
	}
	return result, nil
}

func (f *fakeGuestStore) Delete(id string) error {
	if _, ok := f.guests[id]; !ok {
		return fmt.Errorf("guest not found: %s", id)
	}
	delete(f.guests, id)
	return nil
}

// ── Test Helpers ────────────────────────────────────────────────────────────

func setupSharingTestServer(t *testing.T) *Server {
	t.Helper()

	appStore := NewFakeAppStore()
	catalogCache := NewFakeCatalogCache()
	appHub := NewAppEventHub(appStore)
	appStore.SetOnChange(appHub.Broadcast)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Add a catalog app with SSO strategy
	catalogCache.AddApp(&catalog.App{
		CatalogID:   "navidrome",
		DisplayName: "Navidrome",
		SSO: catalog.SSO{
			Strategy: "forward-auth",
		},
	})

	// Add the app as installed (ID=1 simulates auto-increment)
	appStore.AddApp(&store.InstalledApp{
		ID:          1,
		CatalogID:   "navidrome",
		DisplayName: "Navidrome",
		Status:      "running",
	})

	guestStore := newFakeGuestStore()
	guestStore.guests["guest-bob"] = &store.Guest{
		ID:        "guest-bob",
		Name:      "Bob",
		CreatedAt: time.Now(),
	}

	server := &Server{
		cfg: ServerConfig{
			SSOHostSecret: "test-secret-key-for-hmac-signing-at-least-32-chars",
			HostLabel:     "Test Host",
		},
		router:     chi.NewRouter(),
		catalog:    catalogCache,
		appStore:   appStore,
		appHub:     appHub,
		guestStore: guestStore,
		shareStore: newFakeShareStore(),
		sidecar:    &fakeSidecar{addr: "100.64.1.2"},
		logger:     logger,
	}

	server.setupMiddleware()
	server.setupRoutes()

	return server
}

// ── Tests ───────────────────────────────────────────────────────────────────

func TestHandleCreateInvite_Success(t *testing.T) {
	server := setupSharingTestServer(t)

	body := `{"appId": "navidrome", "guestId": "guest-bob", "nodeShareLink": "https://login.tailscale.com/admin/invite/abc123"}`
	req := httptest.NewRequest("POST", "/api/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp createInviteResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotEmpty(t, resp.ShareID)
	assert.NotEmpty(t, resp.Token)

	// Token should be decodable as unsigned base64 JSON
	payload, err := sharing.DecodeToken(resp.Token)
	require.NoError(t, err)
	assert.Equal(t, "navidrome", payload.AppID)
	assert.Equal(t, "Navidrome", payload.AppName)
	assert.Equal(t, "Test Host", payload.HostLabel)
	assert.Equal(t, "100.64.1.2", payload.SidecarTailnetAddr)
	assert.Equal(t, "https://login.tailscale.com/admin/invite/abc123", payload.NodeShareLink)

	// Verify share was stored
	shares := server.shareStore.(*fakeShareStore).shares
	assert.Len(t, shares, 1)
	storedShare := shares[resp.ShareID]
	require.NotNil(t, storedShare)
	assert.Equal(t, 1, storedShare.AppID)
	assert.Equal(t, "forward-auth", storedShare.SSOStrategy)
	assert.Equal(t, "guest-bob", storedShare.GuestID)
	assert.Equal(t, "https://login.tailscale.com/admin/invite/abc123", storedShare.NodeShareLink)
	assert.Equal(t, "active", storedShare.Status)
}

func TestHandleCreateInvite_MissingNodeShareLink(t *testing.T) {
	server := setupSharingTestServer(t)

	body := `{"appId": "navidrome", "guestId": "guest-bob"}`
	req := httptest.NewRequest("POST", "/api/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleCreateInvite_AppNotInstalled(t *testing.T) {
	server := setupSharingTestServer(t)

	body := `{"appId": "jellyfin", "guestId": "guest-bob", "nodeShareLink": "https://example.com"}`
	req := httptest.NewRequest("POST", "/api/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleCreateInvite_SidecarNotAvailable(t *testing.T) {
	server := setupSharingTestServer(t)
	server.sidecar = nil

	body := `{"appId": "navidrome", "guestId": "guest-bob", "nodeShareLink": "https://example.com"}`
	req := httptest.NewRequest("POST", "/api/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestHandleCreateInvite_SidecarAddrError(t *testing.T) {
	server := setupSharingTestServer(t)
	server.sidecar = &fakeSidecar{addrErr: fmt.Errorf("container not running")}

	body := `{"appId": "navidrome", "guestId": "guest-bob", "nodeShareLink": "https://example.com"}`
	req := httptest.NewRequest("POST", "/api/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestHandleCreateInvite_MissingAppId(t *testing.T) {
	server := setupSharingTestServer(t)

	body := `{"guestId": "guest-bob", "nodeShareLink": "https://example.com"}`
	req := httptest.NewRequest("POST", "/api/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleListShares_Empty(t *testing.T) {
	server := setupSharingTestServer(t)

	req := httptest.NewRequest("GET", "/api/sharing/shares", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

	var shares []interface{}
	require.NoError(t, json.Unmarshal(resp["shares"], &shares))
	assert.Empty(t, shares)
}

func TestHandleListShares_WithData(t *testing.T) {
	server := setupSharingTestServer(t)

	// Seed a share
	server.shareStore.(*fakeShareStore).shares["share-1"] = &store.Share{
		ID:          "share-1",
		AppID:       1,
		SSOStrategy: "forward-auth",
		GuestID:     "guest-bob",
		Status:      "active",
		CreatedAt:   time.Now(),
	}

	req := httptest.NewRequest("GET", "/api/sharing/shares", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	shares := resp["shares"].([]interface{})
	assert.Len(t, shares, 1)
}

func TestHandleRevokeShare_Success(t *testing.T) {
	server := setupSharingTestServer(t)

	// Seed a share
	server.shareStore.(*fakeShareStore).shares["share-1"] = &store.Share{
		ID:     "share-1",
		AppID:  1,
		Status: "active",
	}

	req := httptest.NewRequest("DELETE", "/api/sharing/shares/share-1", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "revoked", resp["status"])

	// Verify the share status was updated
	s := server.shareStore.(*fakeShareStore).shares["share-1"]
	assert.Equal(t, "revoked", s.Status)
}

func TestHandleRevokeShare_NotFound(t *testing.T) {
	server := setupSharingTestServer(t)

	req := httptest.NewRequest("DELETE", "/api/sharing/shares/nonexistent", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleListGuests(t *testing.T) {
	server := setupSharingTestServer(t)

	req := httptest.NewRequest("GET", "/api/sharing/guests", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	guests := resp["guests"].([]interface{})
	assert.Len(t, guests, 1) // Seeded "Bob" from setup
}

func TestHandleCreateGuest(t *testing.T) {
	server := setupSharingTestServer(t)

	body := `{"name": "Alice"}`
	req := httptest.NewRequest("POST", "/api/sharing/guests", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var guest store.Guest
	require.NoError(t, json.NewDecoder(w.Body).Decode(&guest))
	assert.NotEmpty(t, guest.ID)
	assert.Equal(t, "Alice", guest.Name)
}

func TestHandleCreateGuest_EmptyName(t *testing.T) {
	server := setupSharingTestServer(t)

	body := `{"name": ""}`
	req := httptest.NewRequest("POST", "/api/sharing/guests", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleCreateGuest_DuplicateName(t *testing.T) {
	server := setupSharingTestServer(t)

	body := `{"name": "Bob"}`
	req := httptest.NewRequest("POST", "/api/sharing/guests", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleCreateInvite_InvalidGuestID(t *testing.T) {
	server := setupSharingTestServer(t)

	body := `{"appId": "navidrome", "guestId": "nonexistent", "nodeShareLink": "https://example.com"}`
	req := httptest.NewRequest("POST", "/api/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleCommunityGraph_Empty(t *testing.T) {
	server := setupSharingTestServer(t)

	req := httptest.NewRequest("GET", "/api/sharing/community", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp communityGraphResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

	// Only the host node, no edges
	assert.Len(t, resp.Nodes, 1)
	assert.Equal(t, "__host__", resp.Nodes[0].ID)
	assert.Equal(t, "person", resp.Nodes[0].NodeType)
	assert.Equal(t, "Test Host", resp.Nodes[0].Label)
	assert.Empty(t, resp.Edges)
}

func TestHandleCommunityGraph_WithActiveShares(t *testing.T) {
	server := setupSharingTestServer(t)

	// Seed active shares
	server.shareStore.(*fakeShareStore).shares["share-1"] = &store.Share{
		ID:      "share-1",
		AppID:   1,
		GuestID: "guest-bob",
		Status:  "active",
	}

	req := httptest.NewRequest("GET", "/api/sharing/community", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp communityGraphResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

	// 3 nodes: host, app:navidrome, guest:guest-bob
	assert.Len(t, resp.Nodes, 3)

	// 2 edges: host→app, app→guest
	assert.Len(t, resp.Edges, 2)

	// Verify node types
	nodeMap := make(map[string]communityNode)
	for _, n := range resp.Nodes {
		nodeMap[n.ID] = n
	}

	assert.Equal(t, "person", nodeMap["__host__"].NodeType)
	assert.Equal(t, "app", nodeMap["app:navidrome"].NodeType)
	assert.Equal(t, "Navidrome", nodeMap["app:navidrome"].Label) // resolved from catalog
	assert.Equal(t, "navidrome", nodeMap["app:navidrome"].AppID)
	assert.Equal(t, "person", nodeMap["guest:guest-bob"].NodeType)
	assert.Equal(t, "Bob", nodeMap["guest:guest-bob"].Label) // resolved from guest store
}

func TestHandleCommunityGraph_SkipsRevokedShares(t *testing.T) {
	server := setupSharingTestServer(t)

	now := time.Now()
	server.shareStore.(*fakeShareStore).shares["share-1"] = &store.Share{
		ID:        "share-1",
		AppID:     1,
		GuestID:   "guest-bob",
		Status:    "revoked",
		RevokedAt: &now,
	}

	req := httptest.NewRequest("GET", "/api/sharing/community", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp communityGraphResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

	// Only host node — revoked share is excluded
	assert.Len(t, resp.Nodes, 1)
	assert.Empty(t, resp.Edges)
}
