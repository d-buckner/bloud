// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFirstRunModule builds the settings module the way a real first boot has
// it: a live host set still on the default localhost primary, and an
// orchestrator recording whatever the handler submits.
func newFirstRunModule(t *testing.T, hosts []string, primary string) *settingsModule {
	t.Helper()
	mod := newSettingsModule(t, nil)
	mod.hostState = hostset.NewState(hostset.New(hosts, primary))
	return mod
}

func postCreateFirstUser(t *testing.T, mod *settingsModule, host string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	NewSetupRouter(mod, r)

	body := `{"username":"admin","password":"securepass123"}`
	req := httptest.NewRequest("POST", "/setup/create-user", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if host != "" {
		req.Host = host
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeCreateUser(t *testing.T, w *httptest.ResponseRecorder) CreateUserResponse {
	t.Helper()
	var resp CreateUserResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	return resp
}

func setHostsIntent(t *testing.T, mod *settingsModule) (orchestrator.SetHostsIntent, bool) {
	t.Helper()
	i := mod.orch.(*FakeOrchestrator).LastIntent()
	if i == nil {
		return orchestrator.SetHostsIntent{}, false
	}
	sh, ok := i.(orchestrator.SetHostsIntent)
	return sh, ok
}

// The whole point: an install set up through a real domain adopts that domain,
// so the first login round-trips on the address the admin is standing on
// instead of bouncing to a localhost nobody outside the box can reach.
func TestCreateFirstUser_AdoptsOriginHost(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")

	w := postCreateFirstUser(t, mod, "bloud.example.com")
	require.Equal(t, http.StatusOK, w.Code)
	resp := decodeCreateUser(t, w)
	require.True(t, resp.Success)
	assert.Equal(t, "bloud.example.com", resp.PrimaryHost)

	intent, ok := setHostsIntent(t, mod)
	require.True(t, ok, "expected a SetHostsIntent")
	assert.Equal(t, "bloud.example.com", intent.Primary)
	assert.ElementsMatch(t, []string{"localhost", "bloud.local", "bloud.example.com"}, intent.Hosts)
}

// Built-in hosts survive the adoption. Dropping them would take localhost
// access away from the operator who is still sitting on the machine.
func TestCreateFirstUser_AdoptionKeepsBuiltinHosts(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")

	w := postCreateFirstUser(t, mod, "bloud.example.com")
	require.Equal(t, http.StatusOK, w.Code)

	intent, ok := setHostsIntent(t, mod)
	require.True(t, ok)
	for _, builtin := range hostset.BuiltinHosts {
		assert.Contains(t, intent.Hosts, builtin,
			"adopting a custom host must not remove the built-in host %q", builtin)
	}
}

// Already on the right address: nothing to change, and no intent churn means no
// gratuitous SSO re-provisioning.
func TestCreateFirstUser_NoAdoptionWhenAlreadyPrimary(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local", "bloud.example.com"}, "bloud.example.com")

	w := postCreateFirstUser(t, mod, "bloud.example.com")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, decodeCreateUser(t, w).PrimaryHost)
	assert.Equal(t, 0, mod.orch.(*FakeOrchestrator).IntentCount())
}

// localhost is the default primary, so a local first run adopts nothing.
func TestCreateFirstUser_NoAdoptionForLocalhostOrigin(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")

	w := postCreateFirstUser(t, mod, "localhost:8080")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, decodeCreateUser(t, w).PrimaryHost)
	assert.Equal(t, 0, mod.orch.(*FakeOrchestrator).IntentCount())
}

// A port must not end up inside the hostname.
func TestCreateFirstUser_AdoptionStripsPortFromHost(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")

	w := postCreateFirstUser(t, mod, "bloud.example.com:8443")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "bloud.example.com", decodeCreateUser(t, w).PrimaryHost)

	intent, ok := setHostsIntent(t, mod)
	require.True(t, ok)
	assert.Equal(t, "bloud.example.com", intent.Primary)
}

// An unusable Host header is not adopted; the install keeps its current host
// rather than being pinned to garbage. (An absent Host is not testable here:
// HTTP/1.1 requires it, and httptest supplies one.)
func TestCreateFirstUser_IgnoresUnusableHost(t *testing.T) {
	for _, host := range []string{"not a hostname", "bad host!", "..", "a b"} {
		t.Run("host="+host, func(t *testing.T) {
			mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")
			w := postCreateFirstUser(t, mod, host)
			require.Equal(t, http.StatusOK, w.Code)
			assert.Empty(t, decodeCreateUser(t, w).PrimaryHost)
			assert.Equal(t, 0, mod.orch.(*FakeOrchestrator).IntentCount())
		})
	}
}

// No orchestrator means nowhere to route the change. Setup must still succeed:
// the account is the important outcome, and a missing host adoption is not a
// reason to fail first-run.
func TestCreateFirstUser_SucceedsWithoutOrchestrator(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")
	mod.orch = nil

	w := postCreateFirstUser(t, mod, "bloud.example.com")
	require.Equal(t, http.StatusOK, w.Code)
	resp := decodeCreateUser(t, w)
	assert.True(t, resp.Success)
	assert.Empty(t, resp.PrimaryHost)
}

// No live host set is the same story as no orchestrator: create the account,
// adopt nothing, do not panic.
func TestCreateFirstUser_SucceedsWithoutHostState(t *testing.T) {
	mod := newSettingsModule(t, nil)

	w := postCreateFirstUser(t, mod, "bloud.example.com")
	require.Equal(t, http.StatusOK, w.Code)
	resp := decodeCreateUser(t, w)
	assert.True(t, resp.Success)
	assert.Empty(t, resp.PrimaryHost)
}

// The security boundary. Once a user exists the handler 409s before the
// adoption point, so no anonymous caller can ever move the primary host.
func TestCreateFirstUser_NoAdoptionAfterSetupComplete(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")
	require.NoError(t, mod.prefsStore.EnsureUser("someone"))

	w := postCreateFirstUser(t, mod, "evil.example")
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, 0, mod.orch.(*FakeOrchestrator).IntentCount(),
		"a completed install must not accept a host change from the setup route")
	assert.Equal(t, "localhost", mod.hostState.Get().Primary())
}

// The adoption must not be reachable from the login path. The auth module has
// no orchestrator at all, which is the structural form of the invariant: the
// handler that registers OAuth URLs cannot submit a host change, so a
// spoofed Host on an unauthenticated login cannot move the primary host or
// widen the redirect-URI allowlist.
func TestAuthLoginNeverMovesThePrimaryHost(t *testing.T) {
	hostState := hostset.NewState(hostset.New([]string{"localhost", "bloud.local"}, "localhost"))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	authMod := &authModule{
		authentikClient: NewFakeAuthentikClient(),
		authConfig:      newAuthRef(&AuthConfig{}),
		prefsStore:      NewFakePreferencesStore(),
		logger:          logger,
		hosts:           hostState,
	}

	r := chi.NewRouter()
	r.Get("/auth/login", authMod.LoginHandler())
	req := httptest.NewRequest("GET", "/auth/login", nil)
	req.Host = "evil.example"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, "localhost", hostState.Get().Primary(),
		"an unauthenticated login must never move the primary host")
	if loc := w.Header().Get("Location"); loc != "" {
		assert.NotContains(t, loc, "evil.example",
			"a spoofed Host must not appear in the OAuth redirect target")
	}
}

func TestAdoptFirstRunHostLoggerUnusedWithoutHostState(t *testing.T) {
	// Guard the nil-hostState path directly so a future refactor that moves
	// the check does not silently drop it.
	mod := &settingsModule{logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
	req := httptest.NewRequest("POST", "/setup/create-user", nil)
	req.Host = "bloud.example.com"
	assert.Equal(t, "", mod.adoptFirstRunHost(req))
}

// A TLS-terminating proxy leaves the socket plain http, so X-Forwarded-Proto
// is the only signal that the operator reached this install over https. The
// adopted scheme has to follow it or the redirect URI is registered on a
// scheme the browser is not on.
func TestCreateFirstUser_AdoptsHTTPSSchemeFromForwardedProto(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")

	r := chi.NewRouter()
	NewSetupRouter(mod, r)
	req := httptest.NewRequest("POST", "/setup/create-user", strings.NewReader(`{"username":"admin","password":"securepass123"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Host = "bloud.example.com"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	intent, ok := setHostsIntent(t, mod)
	require.True(t, ok)
	assert.Equal(t, "bloud.example.com", intent.Primary)
	assert.Equal(t, "https", intent.Schemes["bloud.example.com"])
}

// Multiple proxy hops: the first entry is the client-facing scheme.
func TestCreateFirstUser_AdoptsSchemeFromFirstForwardedHop(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")

	r := chi.NewRouter()
	NewSetupRouter(mod, r)
	req := httptest.NewRequest("POST", "/setup/create-user", strings.NewReader(`{"username":"admin","password":"securepass123"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https, http")
	req.Host = "bloud.example.com"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	intent, ok := setHostsIntent(t, mod)
	require.True(t, ok)
	assert.Equal(t, "https", intent.Schemes["bloud.example.com"])
}

// TLS on the socket is definitive and outranks any header.
func TestCreateFirstUser_AdoptsHTTPSSchemeFromTLS(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")

	r := chi.NewRouter()
	NewSetupRouter(mod, r)
	req := httptest.NewRequest("POST", "/setup/create-user", strings.NewReader(`{"username":"admin","password":"securepass123"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "bloud.example.com"
	req.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	intent, ok := setHostsIntent(t, mod)
	require.True(t, ok)
	assert.Equal(t, "https", intent.Schemes["bloud.example.com"])
}

// No TLS and no forwarded header: plain http, which is the default mapping
// and needs no override.
func TestCreateFirstUser_DefaultsToHTTPScheme(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")

	w := postCreateFirstUser(t, mod, "bloud.example.com")
	require.Equal(t, http.StatusOK, w.Code)
	intent, ok := setHostsIntent(t, mod)
	require.True(t, ok)
	assert.Equal(t, "http", intent.Schemes["bloud.example.com"])
}

// A garbage forwarded scheme must not become https by accident; it falls
// back to http rather than pinning the install to a scheme it does not have.
func TestCreateFirstUser_GarbageForwardedSchemeFallsBackToHTTP(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")

	r := chi.NewRouter()
	NewSetupRouter(mod, r)
	req := httptest.NewRequest("POST", "/setup/create-user", strings.NewReader(`{"username":"admin","password":"securepass123"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "ftp")
	req.Host = "bloud.example.com"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	intent, ok := setHostsIntent(t, mod)
	require.True(t, ok)
	assert.Equal(t, "http", intent.Schemes["bloud.example.com"])
}
