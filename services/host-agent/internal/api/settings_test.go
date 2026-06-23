package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Fake Tailnet Store ─────────────────────────────────────────────────────

type fakeTailnetStore struct {
	conns map[string]*store.TailnetConnection
}

func newFakeTailnetStore() *fakeTailnetStore {
	return &fakeTailnetStore{conns: make(map[string]*store.TailnetConnection)}
}

func (f *fakeTailnetStore) Create(conn store.TailnetConnection) error {
	f.conns[conn.ID] = &conn
	return nil
}

func (f *fakeTailnetStore) GetByID(id string) (*store.TailnetConnection, error) {
	c, ok := f.conns[id]
	if !ok {
		return nil, nil
	}
	return c, nil
}

func (f *fakeTailnetStore) GetActive() (*store.TailnetConnection, error) {
	for _, c := range f.conns {
		if c.Status == "active" {
			return c, nil
		}
	}
	return nil, nil
}

func (f *fakeTailnetStore) List() ([]*store.TailnetConnection, error) {
	var result []*store.TailnetConnection
	for _, c := range f.conns {
		result = append(result, c)
	}
	return result, nil
}

func (f *fakeTailnetStore) Delete(id string) error {
	if _, ok := f.conns[id]; !ok {
		return fmt.Errorf("tailnet connection not found: %s", id)
	}
	delete(f.conns, id)
	return nil
}

// ── Test Helpers ───────────────────────────────────────────────────────────

func setupSettingsTestServer(t *testing.T) *Server {
	t.Helper()

	appStore := NewFakeAppStore()
	catalogCache := NewFakeCatalogCache()
	appHub := NewAppEventHub(appStore)
	appStore.SetOnChange(appHub.Broadcast)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	server := &Server{
		cfg:          ServerConfig{},
		router:       chi.NewRouter(),
		catalog:      catalogCache,
		appStore:     appStore,
		appHub:       appHub,
		shareStore:   newFakeShareStore(),
		tailnetStore: newFakeTailnetStore(),
		logger:       logger,
	}

	server.setupMiddleware()
	server.setupRoutes()

	return server
}

// ── Tests ──────────────────────────────────────────────────────────────────

func TestHandleGetTailnet_Empty(t *testing.T) {
	server := setupSettingsTestServer(t)

	req := httptest.NewRequest("GET", "/api/settings/tailnet", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "null\n", w.Body.String())
}

func TestHandleGetTailnet_WithData(t *testing.T) {
	server := setupSettingsTestServer(t)

	// Seed a connection
	server.tailnetStore.(*fakeTailnetStore).conns["tn-1"] = &store.TailnetConnection{
		ID:      "tn-1",
		Name:    "My Tailnet",
		Type:    "tailscale",
		AuthKey: "tskey-auth-abc123",
		Status:  "active",
	}

	req := httptest.NewRequest("GET", "/api/settings/tailnet", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp tailnetResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "tn-1", resp.ID)
	assert.Equal(t, "My Tailnet", resp.Name)
	assert.Equal(t, "tailscale", resp.Type)
	assert.Equal(t, "****c123", resp.MaskedAuthKey)
	assert.Equal(t, "active", resp.Status)
}

func TestHandleSetTailnet_Tailscale(t *testing.T) {
	server := setupSettingsTestServer(t)

	body := `{"name": "My TS", "type": "tailscale", "authKey": "tskey-auth-xyz789"}`
	req := httptest.NewRequest("POST", "/api/settings/tailnet", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp tailnetResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotEmpty(t, resp.ID)
	assert.Equal(t, "My TS", resp.Name)
	assert.Equal(t, "tailscale", resp.Type)
	assert.Equal(t, "****z789", resp.MaskedAuthKey)

	// Verify stored in fake store
	conns := server.tailnetStore.(*fakeTailnetStore).conns
	assert.Len(t, conns, 1)
}

func TestHandleSetTailnet_Headscale(t *testing.T) {
	server := setupSettingsTestServer(t)

	body := `{"name": "My HS", "type": "headscale", "authKey": "hskey-abc", "controlUrl": "https://hs.example.com"}`
	req := httptest.NewRequest("POST", "/api/settings/tailnet", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp tailnetResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "headscale", resp.Type)
	assert.Equal(t, "https://hs.example.com", resp.ControlURL)
}

func TestHandleSetTailnet_HeadscaleMissingControlURL(t *testing.T) {
	server := setupSettingsTestServer(t)

	body := `{"name": "My HS", "type": "headscale", "authKey": "hskey-abc"}`
	req := httptest.NewRequest("POST", "/api/settings/tailnet", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleSetTailnet_InvalidType(t *testing.T) {
	server := setupSettingsTestServer(t)

	body := `{"name": "Bad", "type": "wireguard", "authKey": "key123"}`
	req := httptest.NewRequest("POST", "/api/settings/tailnet", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleSetTailnet_MissingAuthKey(t *testing.T) {
	server := setupSettingsTestServer(t)

	body := `{"name": "My TS", "type": "tailscale"}`
	req := httptest.NewRequest("POST", "/api/settings/tailnet", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleSetTailnet_ReplacesExisting(t *testing.T) {
	server := setupSettingsTestServer(t)

	// Create first connection
	server.tailnetStore.(*fakeTailnetStore).conns["tn-old"] = &store.TailnetConnection{
		ID:      "tn-old",
		Name:    "Old",
		Type:    "tailscale",
		AuthKey: "old-key",
		Status:  "active",
	}

	body := `{"name": "New", "type": "tailscale", "authKey": "new-key-1234"}`
	req := httptest.NewRequest("POST", "/api/settings/tailnet", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	// Old connection should be gone, new one present
	conns := server.tailnetStore.(*fakeTailnetStore).conns
	assert.Len(t, conns, 1)
	_, hasOld := conns["tn-old"]
	assert.False(t, hasOld)
}

func TestHandleDeleteTailnet_Success(t *testing.T) {
	server := setupSettingsTestServer(t)

	server.tailnetStore.(*fakeTailnetStore).conns["tn-1"] = &store.TailnetConnection{
		ID:      "tn-1",
		Name:    "My Tailnet",
		Type:    "tailscale",
		AuthKey: "key",
		Status:  "active",
	}

	req := httptest.NewRequest("DELETE", "/api/settings/tailnet", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "deleted", resp["status"])

	conns := server.tailnetStore.(*fakeTailnetStore).conns
	assert.Empty(t, conns)
}

func TestHandleDeleteTailnet_Empty(t *testing.T) {
	server := setupSettingsTestServer(t)

	req := httptest.NewRequest("DELETE", "/api/settings/tailnet", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestMaskAuthKey(t *testing.T) {
	assert.Equal(t, "****c123", maskAuthKey("tskey-auth-abc123"))
	assert.Equal(t, "****", maskAuthKey("abc"))
	assert.Equal(t, "****", maskAuthKey("abcd"))
	assert.Equal(t, "****", maskAuthKey(""))
}
