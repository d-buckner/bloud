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
		Name:        "navidrome",
		DisplayName: "Navidrome",
		SSO: catalog.SSO{
			Strategy: "forward-auth",
		},
	})

	// Add the app as installed
	appStore.AddApp(&store.InstalledApp{
		Name:        "navidrome",
		DisplayName: "Navidrome",
		Status:      "running",
	})

	server := &Server{
		cfg: ServerConfig{
			SSOHostSecret: "test-secret-key-for-hmac-signing-at-least-32-chars",
			HostLabel:     "Test Host",
		},
		router:     chi.NewRouter(),
		catalog:    catalogCache,
		appStore:   appStore,
		appHub:     appHub,
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

	body := `{"appId": "navidrome", "guestLabel": "Bob"}`
	req := httptest.NewRequest("POST", "/api/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp createInviteResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotEmpty(t, resp.ShareID)
	assert.NotEmpty(t, resp.Token)
	assert.Contains(t, resp.Token, ".")

	// Verify share was stored
	shares := server.shareStore.(*fakeShareStore).shares
	assert.Len(t, shares, 1)
	storedShare := shares[resp.ShareID]
	require.NotNil(t, storedShare)
	assert.Equal(t, "navidrome", storedShare.AppID)
	assert.Equal(t, "forward-auth", storedShare.SSOStrategy)
	assert.Equal(t, "Bob", storedShare.GuestLabel)
	assert.Equal(t, "active", storedShare.Status)
}

func TestHandleCreateInvite_AppNotInstalled(t *testing.T) {
	server := setupSharingTestServer(t)

	body := `{"appId": "jellyfin", "guestLabel": "Bob"}`
	req := httptest.NewRequest("POST", "/api/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleCreateInvite_SidecarNotAvailable(t *testing.T) {
	server := setupSharingTestServer(t)
	server.sidecar = nil

	body := `{"appId": "navidrome", "guestLabel": "Bob"}`
	req := httptest.NewRequest("POST", "/api/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestHandleCreateInvite_SidecarAddrError(t *testing.T) {
	server := setupSharingTestServer(t)
	server.sidecar = &fakeSidecar{addrErr: fmt.Errorf("container not running")}

	body := `{"appId": "navidrome", "guestLabel": "Bob"}`
	req := httptest.NewRequest("POST", "/api/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestHandleCreateInvite_MissingAppId(t *testing.T) {
	server := setupSharingTestServer(t)

	body := `{"guestLabel": "Bob"}`
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
		AppID:       "navidrome",
		SSOStrategy: "forward-auth",
		GuestLabel:  "Bob",
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
		AppID:  "navidrome",
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
