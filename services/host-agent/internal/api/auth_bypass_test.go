// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/netutil"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Auth-boundary tests (PR 4).
//
// These exist because the previous suite could not see the auth boundary at
// all: every router-level request forced RemoteAddr to loopback
// (serverRequest, api_test.go), which auto-authenticated as admin and made
// middleware regressions invisible. The tests below deliberately do NOT
// authenticate by position alone.

// newAuthTestServer builds a server whose auth-relevant config the caller
// controls (bind port, trusted nets, API token). setupTestServer keeps its
// zero-auth defaults for the older tests.
func newAuthTestServer(t *testing.T, mutate func(*ServerConfig)) *Server {
	t.Helper()
	tmpDir := t.TempDir()

	testAppDir := filepath.Join(tmpDir, "test-app")
	require.NoError(t, os.MkdirAll(testAppDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(testAppDir, "metadata.yaml"), []byte(`name: test-app
displayName: Test App
description: A test application
category: testing
version: 1.0.0
`), 0644))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, initTestDB(db))

	cfg := ServerConfig{
		AppsDir:           tmpDir,
		DataDir:           tmpDir,
		TraefikDynamicDir: tmpDir,
		Port:              3000,
	}
	if mutate != nil {
		mutate(&cfg)
	}

	fCatalog := NewFakeCatalogCache()
	require.NoError(t, fCatalog.Refresh(catalog.NewLoader(tmpDir)))

	router, _ := NewRouter(db, cfg, logger, func(o *routerOptions) {
		o.catalog = fCatalog
		o.appStore = NewFakeAppStore()
		o.remoteAppStore = NewFakeRemoteAppStore()
	})
	return &Server{cfg: cfg, router: router, db: db, logger: logger}
}

// do issues a request against the real router with an explicit source address
// and headers, bypassing the loopback default that serverRequest applies.
func do(t *testing.T, s *Server, method, path, remoteAddr string, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// newSession creates a real session row and returns its cookie.
func newSession(t *testing.T, s *Server, username string, role store.Role) *http.Cookie {
	t.Helper()
	sess, err := store.NewSessionStore(s.db).Create(username, username, role)
	require.NoError(t, err)
	return &http.Cookie{Name: sessionCookieName, Value: sess.ID}
}

// A public source with no credential: only genuinely public routes may answer.
func TestRouter_UnauthenticatedRouteClassification(t *testing.T) {
	server := newAuthTestServer(t, nil)
	const public = "203.0.113.9:5555"

	cases := []struct {
		method, path string
		want         int
		why          string
	}{
		{http.MethodGet, "/health", http.StatusOK, "liveness probe"},
		{http.MethodGet, "/api/health", http.StatusOK, "liveness probe"},
		{http.MethodGet, "/api/setup/status", http.StatusOK, "first-run wizard reads this BEFORE any login exists"},
		// Reachable without a credential: the handler self-limits to the no-user
		// state (409 once set up) and Authentik is absent in tests → 503.
		{http.MethodPost, "/api/setup/create-user", http.StatusServiceUnavailable, "first-run admin creation must be reachable before any login exists"},
		{http.MethodGet, "/api/auth/me", http.StatusUnauthorized, "identity endpoint: no cookie, no identity"},
		{http.MethodGet, "/api/apps", http.StatusUnauthorized, "catalog is authenticated"},
		{http.MethodGet, "/api/apps/installed", http.StatusUnauthorized, "app inventory is authenticated"},
		{http.MethodPost, "/api/apps/test-app/install", http.StatusUnauthorized, "mutations are authenticated"},
		{http.MethodPost, "/api/apps/test-app/uninstall", http.StatusUnauthorized, "mutations are authenticated"},
		{http.MethodPost, "/api/apps/refresh-catalog", http.StatusUnauthorized, "admin surface"},
		{http.MethodGet, "/api/settings/hosts", http.StatusUnauthorized, "admin surface"},
		{http.MethodGet, "/api/admin/users", http.StatusUnauthorized, "admin surface"},
		{http.MethodGet, "/api/sharing/shares", http.StatusUnauthorized, "admin surface"},
		{http.MethodGet, "/api/user/home", http.StatusUnauthorized, "authenticated"},
		{http.MethodGet, "/api/apps/events", http.StatusUnauthorized, "SSE stream is authenticated"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := do(t, server, tc.method, tc.path, public, nil)
			require.Equal(t, tc.want, w.Code, "%s (%s)", tc.path, tc.why)
		})
	}
}

// The forwarding headers chi's RealIP trusts are client-controlled. Spoofing
// them must not move a public caller into the trusted position.
func TestRouter_SpoofedForwardingHeadersGrantNoAccess(t *testing.T) {
	server := newAuthTestServer(t, func(c *ServerConfig) { c.APIToken = "s3cret-token" })
	const public = "203.0.113.9:5555"

	for _, h := range []string{"True-Client-IP", "X-Real-IP", "X-Forwarded-For", "X-Forwarded-Host"} {
		t.Run(h, func(t *testing.T) {
			for _, path := range []string{"/api/apps/installed", "/api/settings/hosts"} {
				w := do(t, server, http.MethodGet, path, public, map[string]string{
					h:                   "127.0.0.1",
					"Authorization":     "Bearer s3cret-token",
					"X-Forwarded-Proto": "https",
				})
				require.Equal(t, http.StatusUnauthorized, w.Code,
					"%s: a spoofed %s header must not authenticate a public caller", path, h)
			}
		})
	}
}

// Position alone is not a credential: the defect this PR exists to fix.
func TestRouter_LoopbackPositionIsNotEnough(t *testing.T) {
	server := newAuthTestServer(t, func(c *ServerConfig) { c.APIToken = "s3cret-token" })

	for _, tc := range []struct{ name, addr string }{
		{"loopback", "127.0.0.1:44444"},
		{"loopback ipv6", "[::1]:44444"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, server, http.MethodGet, "/api/apps/installed", tc.addr, nil)
			require.Equal(t, http.StatusUnauthorized, w.Code,
				"loopback without the API token must not be admin")
		})
	}
}

// The ordering trap: every browser request arriving through Traefik comes from
// Traefik's loopback connection. If the position check short-circuits before
// the session check, the whole dashboard is locked out (or worse, promoted to
// _cli admin).
func TestRouter_SessionSurvivesLoopbackPosition(t *testing.T) {
	server := newAuthTestServer(t, func(c *ServerConfig) { c.APIToken = "s3cret-token" })

	t.Run("member session is honoured, not promoted", func(t *testing.T) {
		cookie := newSession(t, server, "alice", store.RoleMember)
		w := do(t, server, http.MethodGet, "/api/apps/installed", "127.0.0.1:44444", nil, cookie)
		require.Equal(t, http.StatusOK, w.Code, "a member session from loopback still authenticates")

		// Admin surface must be forbidden for a member, proving the session's
		// role was used rather than the loopback admin shortcut.
		w = do(t, server, http.MethodGet, "/api/settings/hosts", "127.0.0.1:44444", nil, cookie)
		require.Equal(t, http.StatusForbidden, w.Code,
			"loopback must not upgrade a member session to admin")
	})

	t.Run("admin session keeps admin", func(t *testing.T) {
		cookie := newSession(t, server, "root", store.RoleAdmin)
		w := do(t, server, http.MethodGet, "/api/settings/hosts", "127.0.0.1:44444", nil, cookie)
		require.Equal(t, http.StatusOK, w.Code)
	})
}

// ── OAuth redirect URIs (PR 4) ───────────────────────────────────────────────
//
// The login handler used to build the redirect URI from the request's Host /
// X-Forwarded-Host and *register it in Authentik* when unknown. An
// unauthenticated caller could therefore add arbitrary redirect URIs to the
// OAuth client that backs every SSO'd app. Redirect URIs must come from the
// admin-controlled host set.

func newHostAwareAuthModule(t *testing.T, hosts *hostset.State) (*authModule, *FakeAuthentikClient) {
	t.Helper()
	client := NewFakeAuthentikClient()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mod := NewAuthModule(client, newAuthRef(&AuthConfig{OIDCConfig: client.oidcConfig}),
		NewFakePreferencesStore(), newFakeSessionStore(), logger, 0, hosts)
	return mod, client
}

func loginRedirectURI(t *testing.T, w *httptest.ResponseRecorder, resp *http.Response) string {
	t.Helper()
	loc := w.Header().Get("Location")
	require.NotEmpty(t, loc, "login must redirect")
	u, err := url.Parse(loc)
	require.NoError(t, err)
	return u.Query().Get("redirect_uri")
}

func TestAuthModule_LoginNeverRegistersRequestSuppliedHost(t *testing.T) {
	hs := hostset.New([]string{"localhost", "bloud.local"}, "bloud.local")
	mod, client := newHostAwareAuthModule(t, hostset.NewState(hs))

	req := httptest.NewRequest(http.MethodGet, "http://evil.example/auth/login", nil)
	req.Host = "evil.example"
	req.Header.Set("X-Forwarded-Host", "evil.example")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	mod.LoginHandler()(w, req)

	require.Empty(t, client.redirectURIs[1],
		"an unauthenticated caller must not be able to register a redirect URI in Authentik")
	require.Equal(t, "http://bloud.local/auth/callback", loginRedirectURI(t, w, nil),
		"an unknown host must fall back to the configured primary host")
}

func TestAuthModule_LoginUsesConfiguredHost(t *testing.T) {
	hs := hostset.New([]string{"localhost", "bloud.local"}, "bloud.local")
	mod, _ := newHostAwareAuthModule(t, hostset.NewState(hs))

	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/auth/login", nil)
	req.Host = "localhost:8080"
	w := httptest.NewRecorder()
	mod.LoginHandler()(w, req)

	require.Equal(t, "http://localhost:8080/auth/callback", loginRedirectURI(t, w, nil),
		"a configured host keeps its own base URL (localhost keeps port 8080)")
}

func TestAuthModule_LogoutDoesNotRedirectToRequestSuppliedHost(t *testing.T) {
	hs := hostset.New([]string{"localhost", "bloud.local"}, "bloud.local")
	mod, _ := newHostAwareAuthModule(t, hostset.NewState(hs))

	req := httptest.NewRequest(http.MethodPost, "http://evil.example/auth/logout", nil)
	req.Host = "evil.example"
	req.Header.Set("X-Forwarded-Host", "evil.example")
	w := httptest.NewRecorder()
	mod.LogoutHandler()(w, req)

	loc := w.Header().Get("Location")
	require.NotContains(t, loc, "evil.example", "logout must not bounce through a request-supplied host")
}

// Reaching the UI by IP must keep working. initAuthHelper registers every base
// URL in hostset.AllBaseURLs with the identity provider, including the host's
// detected local IPs, so the login may use whichever one the browser used.
// Falling back to the primary host instead breaks login, because the OAuth state
// cookie is host-scoped: the callback would arrive on a different host than the
// one that set it, and the state check would fail with "missing state cookie".
func TestAuthModule_IPAccessKeepsTheRequestedBaseURL(t *testing.T) {
	ips := netutil.DetectLocalIPs()
	if len(ips) == 0 {
		t.Skip("no non-loopback IPv4 on this host")
	}
	mod, _ := newHostAwareAuthModule(t, hostset.NewState(hostset.New([]string{"localhost"}, "localhost")))

	ip := ips[0]
	req := httptest.NewRequest(http.MethodGet, "http://"+ip+":8080/auth/login", nil)
	req.Host = ip + ":8080"
	w := httptest.NewRecorder()
	mod.LoginHandler()(w, req)

	require.Equal(t, "http://"+ip+":8080/auth/callback", loginRedirectURI(t, w, nil),
		"an IP published in the host set's base URLs must keep its own redirect URI")
}
