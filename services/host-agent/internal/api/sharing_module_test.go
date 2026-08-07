package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/go-chi/chi/v5"
)

func newSharingModule(t *testing.T) (*sharingModule, *FakeShareStore, *FakeGuestStore, *FakeAppStore, *FakeCatalogCache) {
	t.Helper()
	shareStore := &FakeShareStore{}
	guestStore := &FakeGuestStore{}
	appStore := NewFakeAppStore()
	catalogCache := NewFakeCatalogCache()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := &sharingModule{
		shareStore:    shareStore,
		guestStore:    guestStore,
		appStore:      appStore,
		catalog:       catalogCache,
		tailnetNode:   NewFakeTailnetNode(),
		hostLabel:     "Test Server",
		ssoHostSecret: "test-secret-key",
		logger:        logger,
	}
	return mod, shareStore, guestStore, appStore, catalogCache
}

// ---- Fake stores for sharing tests ----

// FakeShareStore is an in-memory share store for testing.
type FakeShareStore struct {
	shares []*store.Share
}

func (f *FakeShareStore) Create(s store.Share) error {
	f.shares = append(f.shares, &s)
	return nil
}

func (f *FakeShareStore) GetByID(id string) (*store.Share, error) {
	for _, s := range f.shares {
		if s.ID == id {
			return s, nil
		}
	}
	return nil, nil
}

func (f *FakeShareStore) List() ([]*store.Share, error) {
	return f.shares, nil
}

func (f *FakeShareStore) Revoke(id string) error {
	for i, s := range f.shares {
		if s.ID == id {
			f.shares = append(f.shares[:i], f.shares[i+1:]...)
			return nil
		}
	}
	return assert.AnError
}

var _ store.ShareStoreInterface = (*FakeShareStore)(nil)

// FakeGuestStore is an in-memory guest store for testing.
type FakeGuestStore struct {
	guests []*store.Guest
}

func (f *FakeGuestStore) Create(g store.Guest) error {
	for _, g2 := range f.guests {
		if g2.Name == g.Name {
			return assert.AnError
		}
	}
	f.guests = append(f.guests, &g)
	return nil
}

func (f *FakeGuestStore) GetByID(id string) (*store.Guest, error) {
	for _, g := range f.guests {
		if g.ID == id {
			return g, nil
		}
	}
	return nil, nil
}

func (f *FakeGuestStore) List() ([]*store.Guest, error) {
	return f.guests, nil
}

func (f *FakeGuestStore) Delete(id string) error {
	for i, g := range f.guests {
		if g.ID == id {
			f.guests = append(f.guests[:i], f.guests[i+1:]...)
			return nil
		}
	}
	return assert.AnError
}

var _ store.GuestStoreInterface = (*FakeGuestStore)(nil)

// FakeTailnetNode is a fake tailnet node manager for testing.
type FakeTailnetNode struct {
	addrs map[string]string
}

func NewFakeTailnetNode() *FakeTailnetNode {
	return &FakeTailnetNode{addrs: make(map[string]string)}
}

func (f *FakeTailnetNode) EnsureRunning(_ context.Context, appName string) error {
	f.addrs[appName] = "app." + appName + ".ts.net"
	return nil
}

func (f *FakeTailnetNode) GetAddr(_ context.Context, appName string) (string, error) {
	if addr, ok := f.addrs[appName]; ok {
		return addr, nil
	}
	return "app." + appName + ".ts.net", nil
}

func (f *FakeTailnetNode) Stop(_ context.Context, appName string) error {
	delete(f.addrs, appName)
	return nil
}

func (f *FakeTailnetNode) StopAndPurge(_ context.Context, appName string) error {
	delete(f.addrs, appName)
	return nil
}

// ---- Community graph tests ----

func TestSharingHTTP_CommunityGraph_Empty(t *testing.T) {
	mod, _, _, _, _ := newSharingModule(t)
	r := chi.NewRouter(); NewSharingRouter(mod, r)

	req := httptest.NewRequest("GET", "/sharing/community", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp communityGraphResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	// Should have at least the host node
	assert.Len(t, resp.Nodes, 1)
	assert.Equal(t, "__host__", resp.Nodes[0].ID)
	assert.Equal(t, "Test Server", resp.Nodes[0].Label)
}

func TestSharingHTTP_CommunityGraph_WithShares(t *testing.T) {
	mod, shareStore, _, appStore, catalogCache := newSharingModule(t)

	// Add a catalog app
	addAppToCache(catalogCache, &catalog.App{
		CatalogID: "jellyfin", DisplayName: "Jellyfin",
	})

	// Add an installed app
	appStore.AddApp(&store.InstalledApp{
		ID: 1, CatalogID: "jellyfin", DisplayName: "Jellyfin",
	})

	// Add a share
	shareStore.shares = append(shareStore.shares, &store.Share{
		ID:      "s1",
		AppID:   1,
		GuestID: "g1",
		Status:  "active",
	})

	r := chi.NewRouter(); NewSharingRouter(mod, r)

	req := httptest.NewRequest("GET", "/sharing/community", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---- Invites ----

func TestSharingHTTP_CreateInvite_MissingFields(t *testing.T) {
	mod, _, _, _, _ := newSharingModule(t)
	r := chi.NewRouter(); NewSharingRouter(mod, r)

	body := `{}`
	req := httptest.NewRequest("POST", "/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSharingHTTP_CreateInvite_NoApp(t *testing.T) {
	mod, _, _, _, _ := newSharingModule(t)
	r := chi.NewRouter(); NewSharingRouter(mod, r)

	body := `{"appId":"","guestId":"g1","nodeShareLink":"link"}`
	req := httptest.NewRequest("POST", "/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSharingHTTP_CreateInvite_NotInstalled(t *testing.T) {
	mod, _, _, _, _ := newSharingModule(t)
	r := chi.NewRouter(); NewSharingRouter(mod, r)

	body := `{"appId":"jellyfin","guestId":"g1","nodeShareLink":"link"}`
	req := httptest.NewRequest("POST", "/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestSharingHTTP_CreateInvite_Success(t *testing.T) {
	mod, _, guestStore, appStore, catalogCache := newSharingModule(t)

	addAppToCache(catalogCache, &catalog.App{
		CatalogID: "jellyfin", DisplayName: "Jellyfin",
		SSO: catalog.SSO{Strategy: "oidc"},
	})
	appStore.AddApp(&store.InstalledApp{
		ID: 1, CatalogID: "jellyfin", DisplayName: "Jellyfin",
	})
	guestStore.guests = append(guestStore.guests, &store.Guest{
		ID: "g1", Name: "Alice",
	})

	// Ensure tailnet node has an address
	mod.tailnetNode.EnsureRunning(context.Background(), "jellyfin")

	r := chi.NewRouter(); NewSharingRouter(mod, r)

	body := `{"appId":"jellyfin","guestId":"g1","nodeShareLink":"https://tailnet.ts.net/share/abc"}`
	req := httptest.NewRequest("POST", "/sharing/invites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp createInviteResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.ShareID)
	assert.NotEmpty(t, resp.Token)
}

// ---- Shares ----

func TestSharingHTTP_ListShares_Empty(t *testing.T) {
	mod, _, _, _, _ := newSharingModule(t)
	r := chi.NewRouter(); NewSharingRouter(mod, r)

	req := httptest.NewRequest("GET", "/sharing/shares", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSharingHTTP_RevokeShare_Success(t *testing.T) {
	mod, shareStore, _, _, _ := newSharingModule(t)
	shareStore.shares = append(shareStore.shares, &store.Share{
		ID: "s1", AppID: 1, GuestID: "g1", Status: "active",
	})

	r := chi.NewRouter(); NewSharingRouter(mod, r)

	req := httptest.NewRequest("DELETE", "/sharing/shares/s1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Len(t, shareStore.shares, 0)
}

func TestSharingHTTP_RevokeShare_NotFound(t *testing.T) {
	mod, _, _, _, _ := newSharingModule(t)
	r := chi.NewRouter(); NewSharingRouter(mod, r)

	req := httptest.NewRequest("DELETE", "/sharing/shares/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ---- Guests ----

func TestSharingHTTP_ListGuests_Empty(t *testing.T) {
	mod, _, _, _, _ := newSharingModule(t)
	r := chi.NewRouter(); NewSharingRouter(mod, r)

	req := httptest.NewRequest("GET", "/sharing/guests", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSharingHTTP_CreateGuest_MissingName(t *testing.T) {
	mod, _, _, _, _ := newSharingModule(t)
	r := chi.NewRouter(); NewSharingRouter(mod, r)

	body := `{}`
	req := httptest.NewRequest("POST", "/sharing/guests", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSharingHTTP_CreateGuest_Success(t *testing.T) {
	mod, _, guestStore, _, _ := newSharingModule(t)
	r := chi.NewRouter(); NewSharingRouter(mod, r)

	body := `{"name":"Alice"}`
	req := httptest.NewRequest("POST", "/sharing/guests", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Len(t, guestStore.guests, 1)
	assert.Equal(t, "Alice", guestStore.guests[0].Name)
}

// ---- Router registration ----

func TestSharingRouter_RegistersRoutes(t *testing.T) {
	mod, _, _, _, _ := newSharingModule(t)
	r := chi.NewRouter(); NewSharingRouter(mod, r)

	routes := []struct {
		method string
		path   string
	}{
		{"GET", "/sharing/community"},
		{"POST", "/sharing/invites"},
		{"GET", "/sharing/shares"},
		{"DELETE", "/sharing/shares/abc"},
		{"GET", "/sharing/guests"},
		{"POST", "/sharing/guests"},
	}

	for _, route := range routes {
		t.Run(route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code == http.StatusNotFound {
				ct := w.Header().Get("Content-Type")
				assert.Contains(t, ct, "application/json",
					"route %s %s should return JSON even when handler returns 404", route.method, route.path)
			}
		})
	}
}

// ---- Interface contract ----

var _ SharingModule = (*sharingModule)(nil)

var _ = io.EOF
var _ = chi.NewRouter
var _ = sharing.InvitePayload{}

// Suppress unused
var _ = strings.NewReader
