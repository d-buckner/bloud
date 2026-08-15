package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/go-chi/chi/v5"
)

func newAuthModule(t *testing.T, cfg *AuthConfig) (*authModule, *FakeAuthentikClient, *fakeSessionStore) {
	t.Helper()
	client := NewFakeAuthentikClient()
	sessStore := newFakeSessionStore()
	prefsStore := NewFakePreferencesStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mod := NewAuthModule(client, newAuthConfigRef(cfg), prefsStore, sessStore, logger)
	return mod, client, sessStore
}

// ---- Unit tests (no HTTP) ----

func TestAuthModule_Login_NoConfig(t *testing.T) {
	mod, _, _ := newAuthModule(t, nil)
	handler := mod.LoginHandler()
	assert.NotNil(t, handler)
}

func TestAuthModule_Callback_NoConfig(t *testing.T) {
	mod, _, _ := newAuthModule(t, nil)
	handler := mod.CallbackHandler()
	assert.NotNil(t, handler)
}

func TestAuthModule_Logout_WithAuthentikConfig(t *testing.T) {
	cfg := &AuthConfig{
		OIDCConfig: &authentik.OIDCConfig{
			ProviderID: 1,
		},
	}
	mod, _, _ := newAuthModule(t, cfg)
	handler := mod.LogoutHandler()
	assert.NotNil(t, handler)
}

func TestAuthModule_GetCurrentUserHandler_WithConfig(t *testing.T) {
	cfg := &AuthConfig{}
	mod, _, _ := newAuthModule(t, cfg)
	handler := mod.GetCurrentUserHandler()
	assert.NotNil(t, handler)
}

func TestAuthModule_Logout_ClearsSession(t *testing.T) {
	cfg := &AuthConfig{}
	mod, _, sessStore := newAuthModule(t, cfg)

	sessStore.Create("user1", "alice", store.RoleMember)

	handler := mod.LogoutHandler()

	req := httptest.NewRequest("POST", "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "bloud_session", Value: "fake-session-alice"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusFound, w.Code)

	s, _ := sessStore.Get("fake-session-alice")
	assert.Nil(t, s, "session should have been deleted")
}

func TestAuthModule_Logout_ReducesToAuthentik(t *testing.T) {
	cfg := &AuthConfig{
		OIDCConfig: &authentik.OIDCConfig{
			ProviderID: 1,
		},
	}
	mod, _, _ := newAuthModule(t, cfg)

	handler := mod.LogoutHandler()

	req := httptest.NewRequest("POST", "/auth/logout", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusFound, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "/if/flow/default-invalidation-flow/")
}

// ---- HTTP handler tests ----

func TestAuthHTTP_GetCurrentUser_NoSession(t *testing.T) {
	cfg := &AuthConfig{}
	mod, _, _ := newAuthModule(t, cfg)
	r := chi.NewRouter(); NewAuthRouter(mod, r)

	req := httptest.NewRequest("GET", "/auth/me", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAuthHTTP_GetCurrentUser_NoSessionStore(t *testing.T) {
	cfg := &AuthConfig{}
	client := NewFakeAuthentikClient()
	prefsStore := NewFakePreferencesStore()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	authMod := &authModule{
		authentikClient: client,
		authConfig:      newAuthConfigRef(cfg),
		prefsStore:      prefsStore,
		sessionStore:    nil,
		logger:          logger,
	}
	r := chi.NewRouter(); NewAuthRouter(authMod, r)

	req := httptest.NewRequest("GET", "/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "bloud_session", Value: "some-session"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAuthHTTP_GetCurrentUser_ValidSession(t *testing.T) {
	cfg := &AuthConfig{}
	mod, _, sessStore := newAuthModule(t, cfg)

	sessStore.Create("user1", "bob", store.RoleAdmin)

	r := chi.NewRouter(); NewAuthRouter(mod, r)

	req := httptest.NewRequest("GET", "/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "bloud_session", Value: "fake-session-bob"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "bob", resp["username"])
	assert.Equal(t, "admin", resp["role"])
}

func TestAuthHTTP_GetCurrentUser_InvalidSession(t *testing.T) {
	cfg := &AuthConfig{}
	mod, _, _ := newAuthModule(t, cfg)
	r := chi.NewRouter(); NewAuthRouter(mod, r)

	req := httptest.NewRequest("GET", "/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "bloud_session", Value: "nonexistent-session"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAuthHTTP_Login_NoConfig(t *testing.T) {
	mod, _, _ := newAuthModule(t, nil)
	r := chi.NewRouter(); NewAuthRouter(mod, r)

	req := httptest.NewRequest("GET", "/auth/login", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAuthHTTP_Logout_ClearsCookie(t *testing.T) {
	cfg := &AuthConfig{}
	mod, _, _ := newAuthModule(t, cfg)
	r := chi.NewRouter(); NewAuthRouter(mod, r)

	req := httptest.NewRequest("POST", "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "bloud_session", Value: "some-session-id"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusFound, w.Code)

	cookies := w.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "bloud_session" {
			sessionCookie = c
			break
		}
	}
	require.NotNil(t, sessionCookie, "session cookie should be set")
	assert.Equal(t, "", sessionCookie.Value, "session cookie value should be empty")
	assert.True(t, sessionCookie.MaxAge < 0, "session cookie should have negative MaxAge")
}

func TestAuthHTTP_Logout_DefaultRedirect(t *testing.T) {
	cfg := &AuthConfig{}
	mod, _, _ := newAuthModule(t, cfg)
	r := chi.NewRouter(); NewAuthRouter(mod, r)

	req := httptest.NewRequest("POST", "/auth/logout", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/", w.Header().Get("Location"))
}

func TestAuthHTTP_Callback_MissingState(t *testing.T) {
	cfg := &AuthConfig{
		OIDCConfig: &authentik.OIDCConfig{
			ProviderID: 1,
			ClientID:   "test-client",
		},
	}
	mod, _, _ := newAuthModule(t, cfg)
	r := chi.NewRouter(); NewAuthRouter(mod, r)

	req := httptest.NewRequest("GET", "/auth/callback?code=abc123&state=xyz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAuthHTTP_Callback_StateMismatch(t *testing.T) {
	cfg := &AuthConfig{
		OIDCConfig: &authentik.OIDCConfig{
			ProviderID: 1,
			ClientID:   "test-client",
		},
	}
	mod, _, _ := newAuthModule(t, cfg)
	r := chi.NewRouter(); NewAuthRouter(mod, r)

	req := httptest.NewRequest("GET", "/auth/callback?code=abc123&state=wrong-state", nil)
	req.AddCookie(&http.Cookie{Name: "bloud_oauth_state", Value: "expected-state"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAuthHTTP_Callback_NoCode(t *testing.T) {
	cfg := &AuthConfig{
		OIDCConfig: &authentik.OIDCConfig{
			ProviderID: 1,
			ClientID:   "test-client",
		},
	}
	mod, _, _ := newAuthModule(t, cfg)
	r := chi.NewRouter(); NewAuthRouter(mod, r)

	req := httptest.NewRequest("GET", "/auth/callback?state=correct-state", nil)
	req.AddCookie(&http.Cookie{Name: "bloud_oauth_state", Value: "correct-state"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAuthHTTP_Callback_WithOAuthError(t *testing.T) {
	cfg := &AuthConfig{
		OIDCConfig: &authentik.OIDCConfig{
			ProviderID: 1,
			ClientID:   "test-client",
		},
	}
	mod, _, _ := newAuthModule(t, cfg)
	r := chi.NewRouter(); NewAuthRouter(mod, r)

	req := httptest.NewRequest("GET", "/auth/callback?error=access_denied&error_description=User+denied+access&state=s", nil)
	req.AddCookie(&http.Cookie{Name: "bloud_oauth_state", Value: "s"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "Authentication failed")
}

func TestAuthHTTP_Callback_FullFlow(t *testing.T) {
	cfg := &AuthConfig{
		OIDCConfig: &authentik.OIDCConfig{
			ProviderID:   1,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
		},
	}
	mod, client, sessStore := newAuthModule(t, cfg)
	r := chi.NewRouter(); NewAuthRouter(mod, r)

	state := "test-state-123"
	req := httptest.NewRequest("GET", "/auth/callback?code=auth-code-xyz&state="+state, nil)
	req.AddCookie(&http.Cookie{Name: "bloud_oauth_state", Value: state})
	req.Host = "localhost:8080"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusFound, w.Code)

	assert.True(t, client.exchangeCodeCalled, "ExchangeCode should have been called")
	assert.True(t, client.getUserInfoCalled, "GetUserInfo should have been called")

	sess, err := sessStore.Get("fake-session-testuser")
	require.NoError(t, err)
	require.NotNil(t, sess, "session should have been created")
	assert.Equal(t, "testuser", sess.Username)
	assert.Equal(t, store.RoleAdmin, sess.Role)

	cookies := w.Result().Cookies()
	var stateCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "bloud_oauth_state" {
			stateCookie = c
			break
		}
	}
	require.NotNil(t, stateCookie)
	assert.Equal(t, "", stateCookie.Value)
	assert.True(t, stateCookie.MaxAge < 0)

	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "bloud_session" {
			sessionCookie = c
			break
		}
	}
	require.NotNil(t, sessionCookie)
	assert.NotEmpty(t, sessionCookie.Value)
}

// ---- authConfigRef shared holder ----

func TestAuthConfigRef_GetReturnsSetValue(t *testing.T) {
	cfg := &AuthConfig{}
	ref := newAuthConfigRef(cfg)
	assert.Same(t, cfg, ref.Get())

	updated := &AuthConfig{OIDCConfig: &authentik.OIDCConfig{ClientID: "new-id"}}
	ref.Set(updated)
	assert.Same(t, updated, ref.Get())
}

func TestAuthConfigRef_GetNilWhenUnset(t *testing.T) {
	ref := newAuthConfigRef(nil)
	assert.Nil(t, ref.Get())

	var nilRef *authConfigRef
	assert.Nil(t, nilRef.Get())
}

func TestAuthConfigRef_EnsureRunsFactory(t *testing.T) {
	ref := newAuthConfigRef(nil)

	var calls int
	cfg := &AuthConfig{OIDCConfig: &authentik.OIDCConfig{ClientID: "bootstrapped"}}
	ref.SetEnsure(func() *AuthConfig {
		calls++
		return cfg
	})

	ref.Ensure()
	assert.Equal(t, 1, calls)
	assert.Same(t, cfg, ref.Get())
}

func TestAuthConfigRef_EnsureFactoryReturningNilKeepsDisabled(t *testing.T) {
	ref := newAuthConfigRef(nil)
	ref.SetEnsure(func() *AuthConfig {
		return nil
	})

	ref.Ensure()
	assert.Nil(t, ref.Get())
}

func TestAuthConfigRef_EnsureWithoutFactoryIsNoop(t *testing.T) {
	ref := newAuthConfigRef(&AuthConfig{})
	ref.Ensure()
	assert.NotNil(t, ref.Get())
}

func TestServer_InitAuth_ReEnablesAuth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ref := newAuthConfigRef(nil)

	cfg := &AuthConfig{OIDCConfig: &authentik.OIDCConfig{ClientID: "bootstrapped"}}
	ref.SetEnsure(func() *AuthConfig { return cfg })

	server := &Server{authConfig: ref, logger: logger}
	server.InitAuth()
	assert.Same(t, cfg, ref.Get())
}

func TestServer_InitAuth_NilRefIsNoop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := &Server{authConfig: nil, logger: logger}
	server.InitAuth()
}

// ---- Router registration ----

func TestAuthRouter_RegistersRoutes(t *testing.T) {
	cfg := &AuthConfig{}
	mod, _, _ := newAuthModule(t, cfg)
	r := chi.NewRouter(); NewAuthRouter(mod, r)

	routes := []struct {
		method string
		path   string
	}{
		{"GET", "/auth/me"},
		{"GET", "/auth/login"},
		{"GET", "/auth/callback"},
		{"POST", "/auth/logout"},
	}

	for _, route := range routes {
		req := httptest.NewRequest(route.method, route.path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.NotEqual(t, http.StatusNotFound, w.Code,
			"route %s %s should not return 404", route.method, route.path)
	}
}

// ---- Interface contract ----

var _ AuthentikClientInterface = (*FakeAuthentikClient)(nil)
var _ = io.EOF
var _ store.PreferencesStoreInterface = (*FakePreferencesStore)(nil)
var _ = time.Now
var _ = chi.NewRouter
