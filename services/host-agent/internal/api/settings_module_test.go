// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

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

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/go-chi/chi/v5"
)

func newSettingsModule(t *testing.T, authConfig *AuthConfig) *settingsModule {
	t.Helper()
	tailnetStore := &FakeTailnetStore{}
	prefsStore := NewFakePreferencesStore()
	var sessionStore store.SessionStoreInterface = nil // nil for tests — session invalidation tested via direct fakes
	authClient := NewFakeSettingsAuthentikClient()
	orch := newFakeOrchestrator()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	if authConfig == nil {
		authConfig = &AuthConfig{}
	}

	return &settingsModule{
		tailnetStore:    tailnetStore,
		prefsStore:      prefsStore,
		sessionStore:    sessionStore,
		authentikClient: authClient,
		orch:            orch,
		authConfig:      newAuthConfigRef(authConfig),
		logger:          logger,
	}
}

// FakeTailnetStore is an in-memory tailnet store for testing.
type FakeTailnetStore struct {
	active *store.TailnetConnection
}

func (f *FakeTailnetStore) Create(conn store.TailnetConnection) error {
	f.active = &conn
	return nil
}

func (f *FakeTailnetStore) GetByID(id string) (*store.TailnetConnection, error) {
	if f.active != nil && f.active.ID == id {
		return f.active, nil
	}
	return nil, nil
}

func (f *FakeTailnetStore) GetActive() (*store.TailnetConnection, error) {
	return f.active, nil
}

func (f *FakeTailnetStore) List() ([]*store.TailnetConnection, error) {
	if f.active == nil {
		return nil, nil
	}
	return []*store.TailnetConnection{f.active}, nil
}

func (f *FakeTailnetStore) Delete(id string) error {
	if f.active != nil && f.active.ID == id {
		f.active = nil
	}
	return nil
}

var _ store.TailnetStoreInterface = (*FakeTailnetStore)(nil)

// ---- Tailnet tests ----

func TestSettingsHTTP_GetTailnet_None(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	req := httptest.NewRequest("GET", "/settings/tailnet", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Nil(t, resp)
}

func TestSettingsHTTP_GetTailnet_WithData(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.tailnetStore.(*FakeTailnetStore).active = &store.TailnetConnection{
		ID:      "ts-1",
		Name:    "My Tailscale",
		Type:    "tailscale",
		AuthKey: "tskey-auth-xyz",
		Status:  "active",
	}

	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	req := httptest.NewRequest("GET", "/settings/tailnet", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp tailnetResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "ts-1", resp.ID)
	assert.Equal(t, "My Tailscale", resp.Name)
	assert.True(t, resp.HasAuthKey)
}

func TestSettingsHTTP_SetTailnet_InvalidType(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"name":"test","type":"vpn","authKey":"key"}`
	req := httptest.NewRequest("POST", "/settings/tailnet", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSettingsHTTP_SetTailnet_MissingAuthKey(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"name":"test","type":"tailscale"}`
	req := httptest.NewRequest("POST", "/settings/tailnet", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSettingsHTTP_SetTailnet_HeadscaleMissingControlURL(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"name":"test","type":"headscale","authKey":"key"}`
	req := httptest.NewRequest("POST", "/settings/tailnet", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSettingsHTTP_SetTailnet_NoOrchestrator(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.orch = nil
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"name":"test","type":"tailscale","authKey":"key"}`
	req := httptest.NewRequest("POST", "/settings/tailnet", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestSettingsHTTP_SetTailnet_Valid(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"name":"My TS","type":"tailscale","authKey":"tskey-auth-xyz"}`
	req := httptest.NewRequest("POST", "/settings/tailnet", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.NotEmpty(t, resp["intentId"])
}

func TestSettingsHTTP_DeleteTailnet_None(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	req := httptest.NewRequest("DELETE", "/settings/tailnet", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestSettingsHTTP_DeleteTailnet_Valid(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.tailnetStore.(*FakeTailnetStore).active = &store.TailnetConnection{
		ID:      "ts-1",
		Name:    "My TS",
		Type:    "tailscale",
		AuthKey: "key",
		Status:  "active",
	}

	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	req := httptest.NewRequest("DELETE", "/settings/tailnet", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
}

// ---- Setup wizard tests ----

func TestSettingsHTTP_SetupStatus_NoUsers(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	req := httptest.NewRequest("GET", "/setup/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp SetupStatusResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.True(t, resp.SetupRequired)
}

func TestSettingsHTTP_SetupStatus_AuthReadyReflectsSharedRef(t *testing.T) {
	// A shared ref (as wired by NewRouter) starts disabled and flips to ready
	// once Server.InitAuth runs post-convergence.
	ref := newAuthConfigRef(nil)
	mod := newSettingsModule(t, &AuthConfig{})
	mod.authConfig = ref
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	getAuthReady := func() bool {
		req := httptest.NewRequest("GET", "/setup/status", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var resp SetupStatusResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		return resp.AuthReady
	}

	assert.False(t, getAuthReady())

	ref.SetEnsure(func() *AuthConfig {
		return &AuthConfig{OIDCConfig: &authentik.OIDCConfig{ClientID: "bootstrapped"}}
	})
	ref.Ensure()

	assert.True(t, getAuthReady())
}

func TestSettingsHTTP_SetupStatus_WithUsers(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.prefsStore.(*FakePreferencesStore).EnsureUser("alice")
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	req := httptest.NewRequest("GET", "/setup/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp SetupStatusResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.False(t, resp.SetupRequired)
}

func TestSettingsHTTP_CreateFirstUser_AlreadySetup(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.prefsStore.(*FakePreferencesStore).EnsureUser("alice")
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"username":"bob","password":"password123"}`
	req := httptest.NewRequest("POST", "/setup/create-user", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestSettingsHTTP_CreateFirstUser_NoAuthentik(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.authentikClient = nil
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"username":"bob","password":"password123"}`
	req := httptest.NewRequest("POST", "/setup/create-user", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestSettingsHTTP_CreateFirstUser_InvalidUsername(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"username":"ab","password":"password123"}`
	req := httptest.NewRequest("POST", "/setup/create-user", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSettingsHTTP_CreateFirstUser_ShortPassword(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"username":"bob","password":"short"}`
	req := httptest.NewRequest("POST", "/setup/create-user", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSettingsHTTP_CreateFirstUser_Success(t *testing.T) {
	mod := newSettingsModule(t, nil)
	fake := mod.authentikClient.(*FakeSettingsAuthentikClient)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"username":"admin","password":"securepass123"}`
	req := httptest.NewRequest("POST", "/setup/create-user", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp CreateUserResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.True(t, resp.Success)
	// The user gets a valid identity email (TLD-bearing): SSO apps like
	// AFFiNE reject email addresses without a TLD.
	assert.Equal(t, "admin@localhost.local", fake.users["admin"].Email)
}

func TestSettingsHTTP_CreateFirstUser_AdoptsExistingUser(t *testing.T) {
	mod := newSettingsModule(t, nil)
	fake := mod.authentikClient.(*FakeSettingsAuthentikClient)
	// Simulate a fresh install: the bootstrap already created "admin" in
	// Authentik, so creating it again fails with a duplicate error.
	adminID := fake.userIDCounter
	// The bootstrap admin carries the legacy invalid email (no TLD).
	fake.users["admin"] = &authentik.ManagedUserInfo{ID: adminID, Username: "admin", Email: "admin@localhost"}
	fake.userIDCounter++
	fake.failCreateUsername = "admin"
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"username":"admin","password":"password"}`
	req := httptest.NewRequest("POST", "/setup/create-user", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp CreateUserResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.True(t, resp.Success)
	// The existing user was adopted: password updated and local record created.
	assert.Equal(t, "password", fake.lastSetPasswords[adminID])
	// Adoption also repairs the legacy invalid email so SSO apps can
	// create accounts for the user.
	assert.Equal(t, "admin@localhost.local", fake.users["admin"].Email)
	hasUsers, err := mod.prefsStore.HasUsers()
	require.NoError(t, err)
	assert.True(t, hasUsers)
}

// ---- User management tests ----

func TestSettingsHTTP_ListUsers_NoAuthentik(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.authentikClient = nil
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	req := httptest.NewRequest("GET", "/admin/users", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestSettingsHTTP_ListUsers_Empty(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	req := httptest.NewRequest("GET", "/admin/users", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSettingsHTTP_ListUsers_WithUsers(t *testing.T) {
	mod := newSettingsModule(t, nil)
	client := mod.authentikClient.(*FakeSettingsAuthentikClient)
	client.users["alice"] = &authentik.ManagedUserInfo{ID: 1, Username: "alice", IsAdmin: true}

	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	req := httptest.NewRequest("GET", "/admin/users", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSettingsHTTP_CreateManagedUser_NoAuthentik(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.authentikClient = nil
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"username":"bob","password":"password123"}`
	req := httptest.NewRequest("POST", "/admin/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestSettingsHTTP_CreateManagedUser_MissingFields(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"username":"bob"}`
	req := httptest.NewRequest("POST", "/admin/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSettingsHTTP_CreateManagedUser_InvalidRole(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"username":"bob","password":"password123","role":"superadmin"}`
	req := httptest.NewRequest("POST", "/admin/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSettingsHTTP_CreateManagedUser_DefaultRole(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"username":"bob","password":"password123"}`
	req := httptest.NewRequest("POST", "/admin/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestSettingsHTTP_DeleteManagedUser_SelfDelete(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	ctx := context.WithValue(context.Background(), userContextKey, &store.User{
		Username: "alice",
		Role:     store.RoleAdmin,
	})
	req := httptest.NewRequest("DELETE", "/admin/users/alice", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSettingsHTTP_DeleteManagedUser_NoAuthentik(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.authentikClient = nil
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	req := httptest.NewRequest("DELETE", "/admin/users/alice", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestSettingsHTTP_DeleteManagedUser_Success(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	req := httptest.NewRequest("DELETE", "/admin/users/alice", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSettingsHTTP_SetUserRole_NoAuthentik(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.authentikClient = nil
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"role":"admin"}`
	req := httptest.NewRequest("PUT", "/admin/users/alice/role", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestSettingsHTTP_SetUserRole_InvalidRole(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"role":"superadmin"}`
	req := httptest.NewRequest("PUT", "/admin/users/alice/role", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSettingsHTTP_SetUserRole_NotFound(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	body := `{"role":"admin"}`
	req := httptest.NewRequest("PUT", "/admin/users/nonexistent/role", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// FindUserID returns an error for unknown users, which maps to 500
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestSettingsHTTP_SetUserRole_Success(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	mod.authentikClient.(*FakeSettingsAuthentikClient).users["bob"] = &authentik.ManagedUserInfo{
		ID: 1, Username: "bob", IsAdmin: false,
	}

	body := `{"role":"admin"}`
	req := httptest.NewRequest("PUT", "/admin/users/bob/role", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// ---- Router registration ----

func TestSettingsRouter_RegistersRoutes(t *testing.T) {
	mod := newSettingsModule(t, nil)
	r := chi.NewRouter(); NewSettingsRouter(mod, r)

	routes := []struct {
		method string
		path   string
	}{
		{"GET", "/settings/tailnet"},
		{"POST", "/settings/tailnet"},
		{"DELETE", "/settings/tailnet"},
		{"GET", "/setup/status"},
		{"POST", "/setup/create-user"},
		{"GET", "/admin/users"},
		{"POST", "/admin/users"},
		{"DELETE", "/admin/users/alice"},
		{"PUT", "/admin/users/bob/role"},
	}

	for _, route := range routes {
		t.Run(route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			// The handler may return 4xx/5xx legitimately; we only check it doesn't return
			// chi's own 404 (no route match). A handler-level 404 will still return JSON.
			if w.Code == http.StatusNotFound {
				ct := w.Header().Get("Content-Type")
				assert.Contains(t, ct, "application/json",
					"route %s %s should return JSON even when handler returns 404", route.method, route.path)
			}
		})
	}
}

// ---- Interface contract ----


var _ = io.EOF
var _ = orchestrator.NewSetTailnetIntent
var _ = chi.NewRouter
var _ store.PreferencesStoreInterface = (*FakePreferencesStore)(nil)

// Suppress unused
var _ = strings.NewReader
