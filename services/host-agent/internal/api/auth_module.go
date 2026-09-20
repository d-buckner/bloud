// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"github.com/go-chi/chi/v5"
)

// ---------------------------------------------------------------------------
// Shared auth primitives: cookie names, the thread-safe AuthConfig reference,
// and request-derivation helpers. This is the auth module's shared half, read
// by the Server and the settings module too, not just authModule below.
// ---------------------------------------------------------------------------
const (
	sessionCookieName = "bloud_session"
	stateCookieName   = "bloud_oauth_state"
	stateCookieMaxAge = 10 * 60 // 10 minutes
)

type contextKey string

const userContextKey contextKey = "user"

// AuthConfig holds OIDC configuration for authentication.
// OIDCConfig contains path templates only (no host). Full URLs are derived
// from the incoming request's Host header so OAuth works via any hostname/IP.
type AuthConfig struct {
	OIDCConfig *authentik.OIDCConfig
}

// authConfigRef is a shared, thread-safe reference to the AuthConfig.
// It is shared between the auth and settings modules (and the Server) so a
// post-convergence re-initialization is visible to all consumers without
// mutating module internals. The atomic pointer guards against concurrent
// reads while InitAuth swaps in a fresh config.
type authConfigRef struct {
	p      atomic.Pointer[AuthConfig]
	ensure func() *AuthConfig // re-init factory; nil for static refs (tests)
}

// newAuthConfigRef creates a reference wrapping the given config.
func newAuthConfigRef(cfg *AuthConfig) *authConfigRef {
	ref := &authConfigRef{}
	ref.p.Store(cfg)
	return ref
}

// Get returns the current auth config, or nil if auth is not initialized.
func (r *authConfigRef) Get() *AuthConfig {
	if r == nil {
		return nil
	}
	return r.p.Load()
}

// Set swaps in a fresh auth config. Safe to call concurrently with Get.
func (r *authConfigRef) Set(cfg *AuthConfig) {
	r.p.Store(cfg)
}

// SetEnsure attaches the re-init factory used by Ensure. It captures the
// dependencies (Authentik client, session store, server config) in the scope
// where they are available, so the Server can re-initialize without holding
// them itself.
func (r *authConfigRef) SetEnsure(fn func() *AuthConfig) {
	r.ensure = fn
}

// Ensure re-runs the init factory and swaps in a fresh config if it succeeds.
// Safe to call after system convergence (EnsureBloudOAuthApp is idempotent).
func (r *authConfigRef) Ensure() {
	if r == nil || r.ensure == nil {
		return
	}
	if cfg := r.ensure(); cfg != nil {
		r.Set(cfg)
	}
}

// isLocalRequest reports whether the request's source address is a trusted
// position: loopback, or an address in trustedNets (e.g. a dev VM's slirp NAT
// gateway, where host-forwarded connections arrive from a non-loopback source).
//
// This is a *scope*, not an authorization decision. It says where the API token
// is accepted, never that the caller is trusted. The address is the real TCP
// peer address: host-agent deliberately does not run middleware.RealIP, because
// letting a header rewrite RemoteAddr made this check forgeable.
func isLocalRequest(r *http.Request, trustedNets []string) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, netStr := range trustedNets {
		if _, cidr, err := net.ParseCIDR(netStr); err == nil {
			if cidr.Contains(ip) {
				return true
			}
			continue
		}
		if net.ParseIP(netStr).Equal(ip) {
			return true
		}
	}
	return false
}

// getUserFromContext retrieves the user from the request context.
func getUserFromContext(ctx context.Context) *store.User {
	user, ok := ctx.Value(userContextKey).(*store.User)
	if !ok {
		return nil
	}
	return user
}

// requestHost returns the request's Host header.
//
// Deliberately not X-Forwarded-Host: that header is client-controlled
// (Traefik forwards it verbatim with forwardedHeaders.insecure), and the value
// used to become an OAuth redirect URI registered in the identity provider.
func requestHost(r *http.Request) string {
	return r.Host
}

// isDirectAgentRequest reports whether r bypassed Traefik and hit
// host-agent's own bind port directly, where OIDC login can't work.
func isDirectAgentRequest(r *http.Request, selfPort int) bool {
	if selfPort <= 0 {
		return false
	}
	_, portStr, err := net.SplitHostPort(requestHost(r))
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portStr)
	return err == nil && port == selfPort
}

// oauthBaseURL returns the base URL used for OAuth redirects: the base URL the
// browser used, but only when it is one of the URLs already registered with the
// identity provider: hostset.AllBaseURLs, i.e. the configured hosts plus the
// host's detected local IPs, which is exactly what initAuthHelper registers
// (EnsureBloudOAuthApp). Anything else falls back to the primary host, so neither
// the request nor a spoofed X-Forwarded-Host can introduce a redirect target.
//
// Matching the registered set rather than only the hostname list is what keeps
// IP access working: the box's IPs are published as base URLs but are not
// hostnames in the set, and bouncing an IP visitor to the primary host would
// break login, because the OAuth state cookie is host-scoped: the callback would
// arrive on a different host than the one that set it.
//
// Returns "" when no host set is configured; callers must then refuse rather than
// fall back to the request's Host header.
func (m *authModule) oauthBaseURL(r *http.Request) string {
	if m.hosts == nil {
		return ""
	}
	hs := m.hosts.Get()
	if len(hs.Hosts()) == 0 {
		return ""
	}

	if host := hostOnly(r.Host); host != "" {
		for _, base := range hs.AllBaseURLs() {
			u, err := url.Parse(base)
			if err != nil {
				continue
			}
			if strings.EqualFold(u.Hostname(), host) {
				return strings.TrimSuffix(base, "/")
			}
		}
	}
	return hs.PrimaryBaseURL()
}

// hostOnly returns the hostname of a Host header, dropping any port.
func hostOnly(hostHeader string) string {
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		return h
	}
	return hostHeader
}

// generateState creates a cryptographically secure random state parameter.
func generateState() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// sessionStoreInterface abstracts session persistence, allowing in-memory
// fakes for unit tests without needing a live session store.
type sessionStoreInterface interface {
	Create(userID, username string, role store.Role) (*store.Session, error)
	Get(sessionID string) (*store.Session, error)
	Delete(sessionID string) error
}

// AuthentikClientInterface abstracts the Authentik OIDC client so we can
// mock it in tests.
type AuthentikClientInterface interface {
	IsAvailable(ctx context.Context) bool
	EnsureBloudOAuthApp(ctx context.Context, baseURLs []string, clientSecret string) (*authentik.OIDCConfig, error)
	ExchangeCode(ctx context.Context, code, redirectURI, clientID, clientSecret string) (*authentik.TokenResponse, error)
	GetUserInfo(ctx context.Context, accessToken string) (*authentik.UserInfo, error)
}

// AuthModule encapsulates all authentication operations (login, callback,
// logout, current user). It is a deep module: the implementation hides OIDC
// flow, cookie management, and session persistence.

// FakeAuthentikClient is a fake Authentik client for testing.
type FakeAuthentikClient struct {
	available            bool
	redirectURIs         map[int][]string
	oauthAppBaseURLs     [][]string
	oauthAppClientSecret string
	oidcConfig           *authentik.OIDCConfig
	exchangeCodeCalled   bool
	exchangeCodeResp     *authentik.TokenResponse
	getUserInfoCalled    bool
	userInfo             *authentik.UserInfo
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

func (f *FakeAuthentikClient) IsAvailable(ctx context.Context) bool { return f.available }

func (f *FakeAuthentikClient) EnsureBloudOAuthApp(ctx context.Context, baseURLs []string, clientSecret string) (*authentik.OIDCConfig, error) {
	f.oauthAppBaseURLs = append(f.oauthAppBaseURLs, baseURLs)
	f.oauthAppClientSecret = clientSecret
	return f.oidcConfig, nil
}

func (f *FakeAuthentikClient) ExchangeCode(ctx context.Context, code, redirectURI, clientID, clientSecret string) (*authentik.TokenResponse, error) {
	f.exchangeCodeCalled = true
	return f.exchangeCodeResp, nil
}

func (f *FakeAuthentikClient) GetUserInfo(ctx context.Context, accessToken string) (*authentik.UserInfo, error) {
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

func (f *fakeSessionStore) Create(userID string, username string, role store.Role) (*store.Session, error) {
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

func (f *fakeSessionStore) Get(sessionID string) (*store.Session, error) {
	s, ok := f.sessions[sessionID]
	if !ok {
		return nil, nil
	}
	return s, nil
}

func (f *fakeSessionStore) Delete(sessionID string) error {
	delete(f.sessions, sessionID)
	return nil
}

type authModule struct {
	authentikClient AuthentikClientInterface
	authConfig      *authConfigRef
	prefsStore      store.PreferencesStoreInterface
	sessionStore    sessionStoreInterface
	logger          *slog.Logger
	selfPort        int
	// hosts is the live host set (built-ins + admin custom hosts). OAuth
	// redirect and logout URLs are derived from it, never from the request:
	// LoginHandler registers those URLs in the identity provider, so honouring
	// a request header let any caller add redirect URIs to the OAuth client.
	//
	// There is deliberately no second URL source here (e.g. BLOUD_SSO_BASE_URL):
	// hostset.Resolve already seeds that env var into the set as a per-host URL
	// override, and duplicating it would give one URL two owners.
	hosts *hostset.State
}

// NewAuthModule creates a new AuthModule. selfPort is host-agent's own bind
// port (0 disables the direct-access check, e.g. in tests). hosts supplies the
// OAuth base URL; nil (or an empty host set) means auth is unconfigured, and the
// login/callback handlers refuse to build a URL rather than falling back to the
// request's Host header.
func NewAuthModule(
	client AuthentikClientInterface,
	cfg *authConfigRef,
	prefsStore store.PreferencesStoreInterface,
	sessStore sessionStoreInterface,
	logger *slog.Logger,
	selfPort int,
	hosts *hostset.State,
) *authModule {
	return &authModule{
		authentikClient: client,
		authConfig:      cfg,
		prefsStore:      prefsStore,
		sessionStore:    sessStore,
		logger:          logger,
		selfPort:        selfPort,
		hosts:           hosts,
	}
}

// ---- Login ----

// getAuthConfig returns the current auth config, or nil if not initialized.
func (m *authModule) getAuthConfig() *AuthConfig {
	if m.authConfig == nil {
		return nil
	}
	return m.authConfig.Get()
}

// LoginHandler initiates the OIDC login flow.
func (m *authModule) LoginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isDirectAgentRequest(r, m.selfPort) {
			http.Error(w, "Login isn't available on this port. Use the app's Traefik URL (e.g. http://localhost:8080) instead.", http.StatusBadRequest)
			return
		}

		cfg := m.getAuthConfig()
		if cfg == nil || cfg.OIDCConfig == nil {
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

		baseURL := m.oauthBaseURL(r)
		if baseURL == "" {
			m.logger.Error("cannot build OAuth redirect URI: no host set or SSO base URL configured")
			http.Error(w, "Authentication not configured", http.StatusServiceUnavailable)
			return
		}
		// Redirect URIs are registered for every configured host up front
		// (initAuthHelper → EnsureBloudOAuthApp). Nothing is registered from a
		// request: an unauthenticated caller must not be able to widen the OAuth
		// client's redirect-URI allowlist, which is what the previous lazy
		// AddRedirectURI call allowed via a spoofed Host/X-Forwarded-Host.
		redirectURI := baseURL + "/auth/callback"

		authURL, err := url.Parse(baseURL + cfg.OIDCConfig.AuthURL)
		if err != nil {
			m.logger.Error("failed to parse auth URL", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		q := authURL.Query()
		q.Set("client_id", cfg.OIDCConfig.ClientID)
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
		cfg := m.getAuthConfig()
		if cfg == nil || cfg.OIDCConfig == nil {
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

		baseURL := m.oauthBaseURL(r)
		if baseURL == "" {
			m.logger.Error("cannot build OAuth redirect URI: no host set or SSO base URL configured")
			http.Error(w, "Authentication not configured", http.StatusServiceUnavailable)
			return
		}
		redirectURI := baseURL + "/auth/callback"

		tokenResp, err := m.authentikClient.ExchangeCode(
			r.Context(),
			code,
			redirectURI,
			cfg.OIDCConfig.ClientID,
			cfg.OIDCConfig.ClientSecret,
		)
		if err != nil {
			m.logger.Error("failed to exchange code", "error", err)
			http.Error(w, "Failed to authenticate", http.StatusInternalServerError)
			return
		}

		userInfo, err := m.authentikClient.GetUserInfo(r.Context(), tokenResp.AccessToken)
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

		session, err := m.sessionStore.Create(username, username, role)
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
			if err := m.sessionStore.Delete(cookie.Value); err != nil {
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
		cfg := m.getAuthConfig()
		if cfg != nil && cfg.OIDCConfig != nil && m.oauthBaseURL(r) != "" {
			baseURL := m.oauthBaseURL(r)
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

		session, err := m.sessionStore.Get(cookie.Value)
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

// NewAuthRouter registers all auth-related routes on the given router.
func NewAuthRouter(mod *authModule, r chi.Router) {
	r.Get("/auth/me", mod.GetCurrentUserHandler())
	r.Get("/auth/login", mod.LoginHandler())
	r.Get("/auth/callback", mod.CallbackHandler())
	r.Post("/auth/logout", mod.LogoutHandler())
}
