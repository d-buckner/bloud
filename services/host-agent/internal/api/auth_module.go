// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

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

// AuthRef is a shared, thread-safe reference to the AuthConfig.
// It is shared between the auth and settings modules (and the Server) so a
// post-convergence re-initialization is visible to all consumers without
// mutating module internals. The atomic pointer guards against concurrent
// reads while InitAuth swaps in a fresh config.
type AuthRef struct {
	p      atomic.Pointer[AuthConfig]
	ensure func() *AuthConfig // re-init factory; nil for static refs (tests)
}

// newAuthRef creates a reference wrapping the given config.
func newAuthRef(cfg *AuthConfig) *AuthRef {
	ref := &AuthRef{}
	ref.p.Store(cfg)
	return ref
}

// Get returns the current auth config, or nil if auth is not initialized.
func (r *AuthRef) Get() *AuthConfig {
	if r == nil {
		return nil
	}
	return r.p.Load()
}

// Set swaps in a fresh auth config. Safe to call concurrently with Get.
func (r *AuthRef) Set(cfg *AuthConfig) {
	r.p.Store(cfg)
}

// SetEnsure attaches the re-init factory used by Ensure. It captures the
// dependencies (Authentik client, session store, server config) in the scope
// where they are available, so the Server can re-initialize without holding
// them itself.
func (r *AuthRef) SetEnsure(fn func() *AuthConfig) {
	r.ensure = fn
}

// Ensure re-runs the init factory and swaps in a fresh config if it succeeds.
// Safe to call after system convergence (EnsureBloudOAuthApp is idempotent).
func (r *AuthRef) Ensure() {
	if r == nil || r.ensure == nil {
		return
	}
	if cfg := r.ensure(); cfg != nil {
		r.Set(cfg)
	}
}

// NewAuthentikClient builds the internal identity-provider client from the
// settings the router uses. It returns nil when the instance is not
// configured for Authentik: that is how "no SSO" is represented, rather
// than an error.
func NewAuthentikClient(authentikPort int, authentikToken, baseDomain string) *authentik.Client {
	if authentikToken == "" || authentikPort <= 0 {
		return nil
	}
	internalURL := fmt.Sprintf("http://localhost:%d", authentikPort)
	return authentik.NewClient(internalURL, authentikToken).WithUserEmailDomain(baseDomain)
}

// NewAuthRef builds the live handle on the dashboard's OIDC config, with the
// re-init factory attached and the initial load performed.
//
// It is exported because the handle is now built outside the router. main.go
// constructs it, hands ref.Ensure to the orchestrator builder as the
// host-change hook, and passes the same ref to the server. Leaving the router
// to conjure it internally is what kept the orchestrator's construction
// inside the HTTP layer.
func NewAuthRef(
	client *authentik.Client,
	sessionStore store.SessionStoreInterface,
	cfg ServerConfig,
	logger *slog.Logger,
) *AuthRef {
	ref := newAuthRef(nil)
	ref.SetEnsure(func() *AuthConfig {
		return initAuthHelper(context.Background(), client, sessionStore, cfg, logger)
	})
	ref.Set(initAuthHelper(context.Background(), client, sessionStore, cfg, logger))
	return ref
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

type authModule struct {
	authentikClient AuthentikClientInterface
	authConfig      *AuthRef
	prefsStore      store.PreferencesStoreInterface
	sessionStore    sessionStoreInterface
	logger          *slog.Logger
	selfPort        int
	// hosts is the live host set (built-ins + admin custom hosts). OAuth
	// redirect and logout URLs are derived from it, never from the request:
	// LoginHandler registers those URLs in the identity provider, so honoring
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
	cfg *AuthRef,
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

// NewAuthRouter registers all auth-related routes on the given router.
func NewAuthRouter(mod *authModule, r chi.Router) {
	r.Get("/auth/me", mod.GetCurrentUserHandler())
	r.Get("/auth/login", mod.LoginHandler())
	r.Get("/auth/callback", mod.CallbackHandler())
	r.Post("/auth/logout", mod.LogoutHandler())
}
