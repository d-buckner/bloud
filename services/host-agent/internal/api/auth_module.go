package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"github.com/go-chi/chi/v5"
)

// sessionStoreInterface abstracts session persistence, allowing in-memory
// fakes for unit tests without needing a live Redis.
type sessionStoreInterface interface {
	Create(ctx context.Context, userID string, username string, role store.Role) (*store.Session, error)
	Get(ctx context.Context, sessionID string) (*store.Session, error)
	Delete(ctx context.Context, sessionID string) error
}

// AuthentikClientInterface abstracts the Authentik OIDC client so we can
// mock it in tests.
type AuthentikClientInterface interface {
	IsAvailable() bool
	AddRedirectURI(providerID int, uri string) error
	EnsureBloudOAuthApp(baseURLs []string, clientSecret string) (*authentik.OIDCConfig, error)
	ExchangeCode(code, redirectURI, clientID, clientSecret string) (*authentik.TokenResponse, error)
	GetUserInfo(accessToken string) (*authentik.UserInfo, error)
}

// AuthModule encapsulates all authentication operations (login, callback,
// logout, current user). It is a deep module: the interface is stable; the
// implementation hides OIDC flow, cookie management, and session persistence.
type AuthModule interface {
	// LoginHandler initiates the OIDC login flow.
	LoginHandler() http.HandlerFunc
	// CallbackHandler handles the OAuth2 callback.
	CallbackHandler() http.HandlerFunc
	// LogoutHandler clears the session and redirects to Authentik.
	LogoutHandler() http.HandlerFunc
	// GetCurrentUserHandler returns the current authenticated user.
	GetCurrentUserHandler() http.HandlerFunc
}

// FakeAuthentikClient is a fake Authentik client for testing.
type FakeAuthentikClient struct {
	available          bool
	redirectURIs       map[int][]string
	oauthAppBaseURLs   [][]string
	oauthAppClientID   string
	oauthAppClientSecret string
	oidcConfig         *authentik.OIDCConfig
	exchangeCodeCalled bool
	exchangeCodeResp   *authentik.TokenResponse
	getUserInfoCalled  bool
	userInfo           *authentik.UserInfo
}

// NewFakeAuthentikClient creates a fake Authentik client.
func NewFakeAuthentikClient() *FakeAuthentikClient {
	return &FakeAuthentikClient{
		available:    true,
		redirectURIs: make(map[int][]string),
		oidcConfig: &authentik.OIDCConfig{
			ClientID:     "fake-client-id",
			ClientSecret: "fake-client-secret",
			ProviderID:   1,
		},
		exchangeCodeResp: &authentik.TokenResponse{
			AccessToken: "fake-access-token",
		},
		userInfo: &authentik.UserInfo{
			PreferredUsername: "testuser",
			Groups:            []string{"authentik Admins"},
		},
	}
}

func (f *FakeAuthentikClient) IsAvailable() bool { return f.available }

func (f *FakeAuthentikClient) AddRedirectURI(providerID int, uri string) error {
	f.redirectURIs[providerID] = append(f.redirectURIs[providerID], uri)
	return nil
}

func (f *FakeAuthentikClient) EnsureBloudOAuthApp(baseURLs []string, clientSecret string) (*authentik.OIDCConfig, error) {
	f.oauthAppBaseURLs = append(f.oauthAppBaseURLs, baseURLs)
	f.oauthAppClientSecret = clientSecret
	return f.oidcConfig, nil
}

func (f *FakeAuthentikClient) ExchangeCode(code, redirectURI, clientID, clientSecret string) (*authentik.TokenResponse, error) {
	f.exchangeCodeCalled = true
	return f.exchangeCodeResp, nil
}

func (f *FakeAuthentikClient) GetUserInfo(accessToken string) (*authentik.UserInfo, error) {
	f.getUserInfoCalled = true
	return f.userInfo, nil
}

// fakeSessionStore is an in-memory session store for testing.
type fakeSessionStore struct {
	sessions map[string]*store.Session
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{sessions: make(map[string]*store.Session)}
}

func (f *fakeSessionStore) Create(_ context.Context, userID string, username string, role store.Role) (*store.Session, error) {
	s := &store.Session{
		ID:        "fake-session-" + username,
		UserID:    userID,
		Username:  username,
		Role:      role,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}
	f.sessions[s.ID] = s
	return s, nil
}

func (f *fakeSessionStore) Get(_ context.Context, sessionID string) (*store.Session, error) {
	s, ok := f.sessions[sessionID]
	if !ok {
		return nil, nil
	}
	return s, nil
}

func (f *fakeSessionStore) Delete(_ context.Context, sessionID string) error {
	delete(f.sessions, sessionID)
	return nil
}

type authModule struct {
	authentikClient   AuthentikClientInterface
	authConfig        *AuthConfig
	prefsStore        store.PreferencesStoreInterface
	sessionStore      sessionStoreInterface
	logger            *slog.Logger
	knownRedirectURIs sync.Map // tracks redirect URIs already registered in Authentik
}

// NewAuthModule creates a new AuthModule.
func NewAuthModule(
	client AuthentikClientInterface,
	cfg *AuthConfig,
	prefsStore store.PreferencesStoreInterface,
	sessStore sessionStoreInterface,
	logger *slog.Logger,
) AuthModule {
	return &authModule{
		authentikClient: client,
		authConfig:      cfg,
		prefsStore:      prefsStore,
		sessionStore:    sessStore,
		logger:          logger,
	}
}

// ---- Login ----

// LoginHandler initiates the OIDC login flow.
func (m *authModule) LoginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Try lazy initialization if auth isn't configured yet.
		if m.authConfig == nil || m.authConfig.OIDCConfig == nil {
			m.logger.Error("auth not configured")
			http.Error(w, "Authentication not configured", http.StatusServiceUnavailable)
			return
		}

		state, err := generateState()
		if err != nil {
			m.logger.Error("failed to generate state", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     stateCookieName,
			Value:    state,
			Path:     "/",
			MaxAge:   stateCookieMaxAge,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		baseURL := requestBaseURL(r)
		redirectURI := baseURL + "/auth/callback"

		// Lazily register this redirect URI in Authentik if we haven't seen this host before.
		if _, known := m.knownRedirectURIs.Load(redirectURI); !known {
			if m.authentikClient != nil && m.authConfig.OIDCConfig.ProviderID > 0 {
				if err := m.authentikClient.AddRedirectURI(m.authConfig.OIDCConfig.ProviderID, redirectURI); err != nil {
					m.logger.Warn("failed to register redirect URI lazily", "uri", redirectURI, "error", err)
				} else {
					m.logger.Info("lazily registered redirect URI", "uri", redirectURI)
				}
				m.knownRedirectURIs.Store(redirectURI, true)
			}
		}

		authURL, err := url.Parse(baseURL + m.authConfig.OIDCConfig.AuthURL)
		if err != nil {
			m.logger.Error("failed to parse auth URL", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		q := authURL.Query()
		q.Set("client_id", m.authConfig.OIDCConfig.ClientID)
		q.Set("redirect_uri", redirectURI)
		q.Set("response_type", "code")
		q.Set("scope", "openid profile email")
		q.Set("state", state)
		authURL.RawQuery = q.Encode()

		http.Redirect(w, r, authURL.String(), http.StatusFound)
	}
}

// ---- Callback ----

// CallbackHandler handles the OAuth2 callback.
func (m *authModule) CallbackHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.authConfig == nil || m.authConfig.OIDCConfig == nil {
			m.logger.Error("auth not configured")
			http.Error(w, "Authentication not configured", http.StatusServiceUnavailable)
			return
		}

		// Verify state parameter
		stateCookie, err := r.Cookie(stateCookieName)
		if err != nil {
			m.logger.Warn("missing state cookie")
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		state := r.URL.Query().Get("state")
		if state == "" || state != stateCookie.Value {
			m.logger.Warn("state mismatch", "expected", stateCookie.Value, "got", state)
			http.Error(w, "Invalid state", http.StatusBadRequest)
			return
		}

		// Clear state cookie
		http.SetCookie(w, &http.Cookie{
			Name:     stateCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
		})

		// Check for error response
		if errParam := r.URL.Query().Get("error"); errParam != "" {
			errDesc := r.URL.Query().Get("error_description")
			m.logger.Warn("OAuth error", "error", errParam, "description", errDesc)
			http.Error(w, "Authentication failed: "+errDesc, http.StatusUnauthorized)
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			m.logger.Warn("missing authorization code")
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			return
		}

		redirectURI := requestBaseURL(r) + "/auth/callback"

		tokenResp, err := m.authentikClient.ExchangeCode(
			code,
			redirectURI,
			m.authConfig.OIDCConfig.ClientID,
			m.authConfig.OIDCConfig.ClientSecret,
		)
		if err != nil {
			m.logger.Error("failed to exchange code", "error", err)
			http.Error(w, "Failed to authenticate", http.StatusInternalServerError)
			return
		}

		userInfo, err := m.authentikClient.GetUserInfo(tokenResp.AccessToken)
		if err != nil {
			m.logger.Error("failed to get user info", "error", err)
			http.Error(w, "Failed to get user info", http.StatusInternalServerError)
			return
		}

		username := userInfo.PreferredUsername
		if err := m.prefsStore.EnsureUser(username); err != nil {
			m.logger.Error("failed to ensure local user", "error", err)
			http.Error(w, "Failed to create user", http.StatusInternalServerError)
			return
		}

		// Determine role from Authentik groups
		role := store.RoleMember
		for _, group := range userInfo.Groups {
			if group == "authentik Admins" {
				role = store.RoleAdmin
				break
			}
		}

		ctx := r.Context()
		session, err := m.sessionStore.Create(ctx, username, username, role)
		if err != nil {
			m.logger.Error("failed to create session", "error", err)
			http.Error(w, "Failed to create session", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    session.ID,
			Path:     "/",
			Expires:  session.ExpiresAt,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		m.logger.Info("user logged in", "username", username)

		// Redirect to home
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// ---- Logout ----

// LogoutHandler clears the session and redirects to Authentik for SSO invalidation.
func (m *authModule) LogoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get session from cookie and delete from store if available
		cookie, err := r.Cookie(sessionCookieName)
		if err == nil && cookie.Value != "" && m.sessionStore != nil {
			ctx := r.Context()
			if err := m.sessionStore.Delete(ctx, cookie.Value); err != nil {
				m.logger.Warn("failed to delete session", "error", err)
			}
		}

		// Clear session cookie
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
		})

		// Redirect to Authentik's native invalidation flow to end the SSO session.
		if m.authConfig != nil && m.authConfig.OIDCConfig != nil {
			baseURL := requestBaseURL(r)
			logoutURL := baseURL + "/if/flow/default-invalidation-flow/?redirect=" + url.QueryEscape(baseURL+"/")
			http.Redirect(w, r, logoutURL, http.StatusFound)
			return
		}

		// Fallback when auth is not configured
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// ---- Current User ----

// GetCurrentUserHandler returns the current authenticated user.
func (m *authModule) GetCurrentUserHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Not authenticated",
			})
			return
		}

		if m.sessionStore == nil {
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Not authenticated",
			})
			return
		}

		ctx := r.Context()
		session, err := m.sessionStore.Get(ctx, cookie.Value)
		if err != nil || session == nil {
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Not authenticated",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"id":       session.UserID,
			"username": session.Username,
			"role":     session.Role,
		})
	}
}

// NewAuthRouter returns a chi.Router with all auth-related routes.
func NewAuthRouter(mod *authModule) *chi.Mux {
	r := chi.NewRouter()

	r.Route("/api", func(api chi.Router) {
		api.Get("/auth/me", mod.GetCurrentUserHandler())
	})

	r.Get("/auth/login", mod.LoginHandler())
	r.Get("/auth/callback", mod.CallbackHandler())
	r.Post("/auth/logout", mod.LogoutHandler())

	return r
}
