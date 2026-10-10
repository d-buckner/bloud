// SPDX-License-Identifier: AGPL-3.0-only

package seerr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// The remote sign-in exchange is the only place Bloud trades a login for a
// credential, and every branch of it is a different thing an operator can be told.
// Each test below is one of those sentences: finish your setup first, that is not
// a Seerr, that account cannot see the key, or here is your key.
//
// The fake models the parts of Seerr's auth surface the exchange depends on,
// taken from the pinned v3.4.1 source: /auth/jellyfin and /auth/local both issue a
// session cookie, /settings/main answers an admin with apiKey and a non-admin
// without it, and /settings/public answers `initialized`.

// fakeRemote is a Seerr instance that was not booted by Bloud.
type fakeRemote struct {
	server *httptest.Server
	mu     sync.Mutex
	// initialized is what /settings/public reports; nil models the field being
	// absent, which is what an address that is not a Seerr looks like.
	initialized *bool
	// jellyfinUsers and localUsers are the accounts the two sign-in routes
	// accept, keyed by the identifier each route reads.
	jellyfinUsers map[string]string
	localUsers    map[string]string
	// admins decides who can see apiKey in /settings/main.
	admins map[string]bool
	apiKey string
	// csrf, when set, is the token the instance issues and demands, modeling an
	// operator who turned settings.network.csrfProtection on.
	csrf     string
	sessions map[string]string
	requests []recordedRequest
}

func newFakeRemote(t *testing.T) *fakeRemote {
	t.Helper()
	f := &fakeRemote{
		jellyfinUsers: map[string]string{"daniel": "jelly-pw"},
		localUsers:    map[string]string{},
		admins:        map[string]bool{"daniel": true},
		apiKey:        "remote-api-key",
		sessions:      map[string]string{},
	}
	initialized := true
	f.initialized = &initialized
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeRemote) url() string { return f.server.URL }

func (f *fakeRemote) setInitialized(v *bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initialized = v
}

func (f *fakeRemote) record(r *http.Request) recordedRequest {
	body, _ := io.ReadAll(r.Body)
	req := recordedRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	return req
}

func (f *fakeRemote) all() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func (f *fakeRemote) paths() []string {
	var out []string
	for _, r := range f.all() {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func (f *fakeRemote) serveHTTP(w http.ResponseWriter, r *http.Request) {
	recorded := f.record(r)
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	csrf := f.csrf
	f.mu.Unlock()

	if csrf != "" {
		// The token is issued on any response and demanded on every unsafe
		// method (server/index.ts mounts csurf globally when enabled).
		http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: csrf, Path: "/"})
		if r.Method != http.MethodGet && r.Header.Get("X-CSRF-TOKEN") != csrf {
			w.WriteHeader(http.StatusForbidden)
			writeJSON(w, map[string]any{"error": "invalid csrf token"})
			return
		}
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/settings/public":
		f.mu.Lock()
		initialized := f.initialized
		f.mu.Unlock()
		if initialized == nil {
			writeJSON(w, map[string]any{})
			return
		}
		writeJSON(w, map[string]any{"initialized": *initialized})

	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/jellyfin":
		f.serveJellyfinSignIn(w, recorded.Body)

	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/local":
		f.serveLocalSignIn(w, recorded.Body)

	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/settings/main":
		f.serveMainSettings(w, r)

	default:
		// Everything else, including /settings/main/regenerate: an exchange that
		// reaches any of it is a bug this default makes loud.
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"error": "not found", "seen": r.URL.Path})
	}
}

// serveJellyfinSignIn answers the already-configured form of the Jellyfin
// sign-in. A hostname in the body is the wizard's shape, and a configured
// instance refuses it (server/routes/auth.ts), so the fake refuses it too: that is
// how a test catches the exchange starting to send a field it must not.
func (f *fakeRemote) serveJellyfinSignIn(w http.ResponseWriter, body []byte) {
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	if _, named := in["hostname"]; named {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"error": alreadyConfiguredError})
		return
	}
	username, _ := in["username"].(string)
	password, _ := in["password"].(string)
	f.mu.Lock()
	want, ok := f.jellyfinUsers[username]
	f.mu.Unlock()
	if !ok || want != password {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, map[string]any{"error": "invalid credentials"})
		return
	}
	f.issueSession(w, username)
	writeJSON(w, map[string]any{"id": 2, "username": username, "email": username + "@example.com"})
}

func (f *fakeRemote) serveLocalSignIn(w http.ResponseWriter, body []byte) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	_ = json.Unmarshal(body, &in)
	f.mu.Lock()
	want, ok := f.localUsers[in.Email]
	f.mu.Unlock()
	if !ok || want != in.Password {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, map[string]any{"error": "invalid credentials"})
		return
	}
	f.issueSession(w, in.Email)
	writeJSON(w, map[string]any{"id": 3, "username": in.Email, "email": in.Email})
}

// serveMainSettings answers as the route does: apiKey for an admin, and the same
// object with apiKey omitted for anyone else (filteredMainSettings).
func (f *fakeRemote) serveMainSettings(w http.ResponseWriter, r *http.Request) {
	user := f.sessionUser(r)
	if user == "" {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]any{"error": "no session"})
		return
	}
	f.mu.Lock()
	admin := f.admins[user]
	key := f.apiKey
	f.mu.Unlock()
	body := map[string]any{"librarySyncEnabled": true}
	if admin {
		body["apiKey"] = key
	}
	writeJSON(w, body)
}

func (f *fakeRemote) issueSession(w http.ResponseWriter, user string) {
	id := "sid-" + user
	f.mu.Lock()
	f.sessions[id] = user
	f.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "connect.sid", Value: id, Path: "/"})
}

func (f *fakeRemote) sessionUser(r *http.Request) string {
	cookie, err := r.Cookie("connect.sid")
	if err != nil {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[cookie.Value]
}

func exchangeRequest(login configurator.Login, hasLogin bool) configurator.ExchangeRequest {
	req := configurator.ExchangeRequest{
		Endpoint: "",
		Inputs:   map[string]string{},
	}
	if hasLogin {
		req.Login = &login
	}
	return req
}

func runExchange(t *testing.T, f *fakeRemote, req configurator.ExchangeRequest) (configurator.ExchangeResult, error) {
	t.Helper()
	req.Endpoint = f.url()
	if req.Logger == nil {
		req.Logger = quietLogger()
	}
	return credentialExchange{}.Exchange(context.Background(), req)
}

func typed(username, password string) configurator.ExchangeRequest {
	return configurator.ExchangeRequest{Inputs: map[string]string{
		configurator.ExchangeInputUsername: username,
		configurator.ExchangeInputPassword: password,
	}}
}

func TestExchange_SignsInWithJellyfinCredentialsAndPublishesTheKey(t *testing.T) {
	f := newFakeRemote(t)

	got, err := runExchange(t, f, typed("daniel", "jelly-pw"))
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if got.Secrets[contractRequestManager] != "remote-api-key" {
		t.Errorf("Secrets[%s] = %q, want the key the instance reported", contractRequestManager, got.Secrets[contractRequestManager])
	}
	// defaultUser is the account arr-mcp attributes requests to, and the account
	// just signed in as is the only one this exchange has evidence about.
	if got.Values[contractRequestManager][valueDefaultUser] != "daniel@example.com" {
		t.Errorf("defaultUser = %q, want the signed-in account", got.Values[contractRequestManager][valueDefaultUser])
	}
	for _, call := range f.paths() {
		if strings.Contains(call, "regenerate") {
			t.Errorf("the exchange rotated a credential it should only read: %v", f.paths())
		}
	}
}

func TestExchange_NeverSendsAHostnameToTheSignIn(t *testing.T) {
	f := newFakeRemote(t)
	if _, err := runExchange(t, f, typed("daniel", "jelly-pw")); err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	for _, r := range f.all() {
		if r.Path != "/api/v1/auth/jellyfin" {
			continue
		}
		if strings.Contains(string(r.Body), "hostname") {
			t.Errorf("the sign-in body carried a hostname, which a configured instance refuses: %s", r.Body)
		}
	}
}

func TestExchange_FallsBackToALocalAccount(t *testing.T) {
	f := newFakeRemote(t)
	f.mu.Lock()
	// No Jellyfin account by this name; a local Seerr account instead.
	f.localUsers["owner@example.com"] = "local-pw"
	f.admins["owner@example.com"] = true
	f.mu.Unlock()

	got, err := runExchange(t, f, typed("owner@example.com", "local-pw"))
	if err != nil {
		t.Fatalf("Exchange() error = %v, want the local route to carry the sign-in: %v", err, f.paths())
	}
	if got.Secrets[contractRequestManager] != "remote-api-key" {
		t.Errorf("Secrets = %v, want the key read after the local sign-in", got.Secrets)
	}
}

func TestExchange_RefusesAnInstanceStillInItsOwnWizard(t *testing.T) {
	f := newFakeRemote(t)
	off := false
	f.setInitialized(&off)

	_, err := runExchange(t, f, typed("daniel", "jelly-pw"))
	if err == nil {
		t.Fatal("Exchange() error = nil, want the setup-wizard refusal")
	}
	if !strings.Contains(err.Error(), "first-run wizard") {
		t.Errorf("error = %q, want it to send the operator to the instance's own setup", err)
	}
	for _, call := range f.paths() {
		if strings.Contains(call, "auth/") {
			t.Errorf("the exchange tried to sign in to an uninitialized instance: %v", f.paths())
		}
	}
}

func TestExchange_SaysWhenTheAddressIsNotASeerr(t *testing.T) {
	f := newFakeRemote(t)
	f.setInitialized(nil)

	_, err := runExchange(t, f, typed("daniel", "jelly-pw"))
	if err == nil {
		t.Fatal("Exchange() error = nil, want \"not a Seerr\"")
	}
	if !strings.Contains(err.Error(), "not a Seerr") {
		t.Errorf("error = %q, want it to name the wrong-app case rather than \"not initialized\"", err)
	}
}

func TestExchange_SaysWhenTheAccountIsNotAnAdmin(t *testing.T) {
	f := newFakeRemote(t)
	f.mu.Lock()
	delete(f.admins, "daniel")
	f.mu.Unlock()

	_, err := runExchange(t, f, typed("daniel", "jelly-pw"))
	if err == nil {
		t.Fatal("Exchange() error = nil, want the not-an-admin refusal")
	}
	if !strings.Contains(err.Error(), "not an admin") {
		t.Errorf("error = %q, want it to say the sign-in worked but the role is wrong", err)
	}
}

func TestExchange_ReportsBothSignInsWhenNeitherWorks(t *testing.T) {
	f := newFakeRemote(t)

	_, err := runExchange(t, f, typed("daniel", "wrong"))
	if err == nil {
		t.Fatal("Exchange() error = nil, want the rejected-credentials refusal")
	}
	for _, want := range []string{"jellyfin", "local account"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name both routes that refused", err)
		}
	}
}

func TestExchange_UsesTheLoginBloudHoldsWhenTheInputsAreEmpty(t *testing.T) {
	f := newFakeRemote(t)

	got, err := runExchange(t, f, exchangeRequest(configurator.Login{Username: "daniel", Password: "jelly-pw"}, true))
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if got.Secrets[contractRequestManager] == "" {
		t.Error("Secrets empty, want the key read with Bloud's own login")
	}
}

func TestExchange_TypedInputsWinOverTheStoredLogin(t *testing.T) {
	f := newFakeRemote(t)
	f.mu.Lock()
	f.localUsers["someone@example.com"] = "other-pw"
	f.admins["someone@example.com"] = true
	f.mu.Unlock()

	req := typed("someone@example.com", "other-pw")
	login := configurator.Login{Username: "daniel", Password: "jelly-pw"}
	req.Login = &login

	got, err := runExchange(t, f, req)
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if got.Values[contractRequestManager][valueDefaultUser] != "someone@example.com" {
		t.Errorf("defaultUser = %q, want the account the operator typed", got.Values[contractRequestManager][valueDefaultUser])
	}
}

func TestExchange_RefusesHalfASignIn(t *testing.T) {
	f := newFakeRemote(t)

	_, err := runExchange(t, f, configurator.ExchangeRequest{Inputs: map[string]string{
		configurator.ExchangeInputUsername: "daniel",
	}})
	if err == nil {
		t.Fatal("Exchange() error = nil, want a refusal: half a sign-in is not a credential")
	}
}

// TestExchange_WorksWithCSRFProtectionOn is the regression for a hardened
// instance: with csrfProtection enabled every unsafe request needs the token the
// instance issued, and a 403 there would read to the operator as "wrong password".
func TestExchange_WorksWithCSRFProtectionOn(t *testing.T) {
	f := newFakeRemote(t)
	f.mu.Lock()
	f.csrf = "token-from-the-cookie-jar"
	f.mu.Unlock()

	got, err := runExchange(t, f, typed("daniel", "jelly-pw"))
	if err != nil {
		t.Fatalf("Exchange() error = %v, want the token echoed back: %v", err, f.paths())
	}
	if got.Secrets[contractRequestManager] == "" {
		t.Error("Secrets empty with csrfProtection on")
	}
}

func TestExchange_DeclarationsNameTheContractsTheyTrade(t *testing.T) {
	exchange := credentialExchange{}
	if exchange.Contract() != "requestManager" {
		t.Errorf("Contract() = %q, want the contract whose secret the exchange replaces", exchange.Contract())
	}
	if exchange.LoginContract() != "mediaServer" {
		t.Errorf("LoginContract() = %q, want mediaServer, the role whose login a Seerr admin is", exchange.LoginContract())
	}
	keys := map[string]bool{}
	for _, input := range exchange.Inputs() {
		keys[input.Key] = true
	}
	if !keys[configurator.ExchangeInputUsername] || !keys[configurator.ExchangeInputPassword] {
		t.Errorf("Inputs() = %v, want a username and a password", keys)
	}
	for _, input := range exchange.Inputs() {
		if input.Key == configurator.ExchangeInputPassword && !input.Secret {
			t.Error("the password input must be marked secret so the form renders a password box")
		}
	}
}
