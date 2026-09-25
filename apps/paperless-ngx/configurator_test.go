// SPDX-License-Identifier: AGPL-3.0-only

package paperlessngx

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// staticBaseURL adapts a fixed URL to the configurator's func() string base
// URL interface (tests do not mutate hosts at runtime).
func staticBaseURL(u string) func() string {
	return func() string { return u }
}

type fakeSecrets struct {
	password string
}

func (f *fakeSecrets) GenerateAppAdminPassword(_ string) (string, error) {
	return f.password, nil
}

func (f *fakeSecrets) GetAppSecret(_, _ string) string { return "" }

func (f *fakeSecrets) SetAppSecret(string, string, string) error { return nil }

// fakeApp stands in for the app's HTTP surface: the sign-in page, the signup
// form (open only while no user exists, as Paperless-ngx's account adapter
// decides), the provider flow, and the token endpoint. It records what the
// configurator asked of it and only issues a token for credentials it actually
// holds, so the tests observe the bootstrap end to end rather than a mock echo.
type fakeApp struct {
	mu sync.Mutex

	users map[string]string // username -> password
	csrf  string

	signupPosts    []url.Values
	csrfCookieSeen bool
	tokenPosts     int
	providerPosts  int
	// Cookies observed on each flow: Django rejects a form post that carries a
	// session but no matching CSRF token, so the flows must not share a jar.
	tokenCookies    []string
	providerCookies []string

	// The group API: what the app holds, what the configurator wrote, and the
	// credential each call presented.
	groups      map[string]*group
	nextGroupID int
	groupWrites []string
	groupTokens []string

	// Knobs for the failure cases.
	signupClosed     bool
	signupRejected   bool
	providerHidden   bool
	providerError    bool
	signupPageStatus int
	groupWriteStatus int
}

// fakeAPIToken is the token the fake issues and demands, so a call that
// forgets to present the admin's credential fails instead of passing.
const fakeAPIToken = "api-token"

func newFakeApp() *fakeApp {
	return &fakeApp{users: map[string]string{}, csrf: "csrf-token-value", groups: map[string]*group{}}
}

// ServeHTTP routes the fake's small surface. Each handler holds one flow, so
// the shapes stay readable and the complexity gate stays satisfied.
func (f *fakeApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case r.URL.Path == signInPath:
		f.serveSignIn(w)
	case r.URL.Path == signupPath && r.Method == http.MethodGet:
		f.serveSignupForm(w)
	case r.URL.Path == signupPath && r.Method == http.MethodPost:
		f.serveSignupPost(w, r)
	case r.URL.Path == providerLoginPath:
		f.serveProviderLogin(w, r)
	case r.URL.Path == "/api/token/":
		f.serveToken(w, r)
	case r.URL.Path == "/api/groups/" && r.Method == http.MethodGet:
		f.serveGroupList(w, r)
	case r.URL.Path == "/api/groups/" && r.Method == http.MethodPost:
		f.serveGroupCreate(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/groups/") && r.Method == http.MethodPatch:
		f.serveGroupPatch(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serveSignIn renders the sign-in page: the SSO button is a form whose action
// is the provider login URL, exactly as allauth renders it.
func (f *fakeApp) serveSignIn(w http.ResponseWriter) {
	// Django issues the CSRF cookie with any page that renders a form.
	http.SetCookie(w, &http.Cookie{Name: "csrftoken", Value: f.csrf, Path: "/"})
	if f.providerHidden {
		_, _ = fmt.Fprintf(w, `<form method="post"><input type="hidden" name="csrfmiddlewaretoken" value="%s">`+
			`<input name="login"><button>Sign in</button></form>`, f.csrf)
		return
	}
	_, _ = fmt.Fprintf(w, `<form id="social-login" method="post" action="%s?process=">`+
		`<input type="hidden" name="csrfmiddlewaretoken" value="%s">`+
		`<button type="submit">%s</button></form>`, providerLoginPath, f.csrf, providerName)
}

// serveSignupForm renders the signup page while no user exists ("Sign Up
// Closed" once one does, as the app answers).
func (f *fakeApp) serveSignupForm(w http.ResponseWriter) {
	if f.signupPageStatus != 0 {
		w.WriteHeader(f.signupPageStatus)
		return
	}
	if f.signupClosed {
		_, _ = w.Write([]byte(`<h1>Sign Up Closed</h1>`))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "csrftoken", Value: f.csrf, Path: "/"})
	_, _ = fmt.Fprintf(w, `<form method="post"><input type="hidden" name="csrfmiddlewaretoken" value="%s">`+
		`<input name="password1"><input name="password2"></form>`, f.csrf)
}

// serveSignupPost creates the account the way the app does: reject unless the
// form token matches its cookie, then log the new account in.
func (f *fakeApp) serveSignupPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.signupPosts = append(f.signupPosts, r.PostForm)
	if _, err := r.Cookie("csrftoken"); err == nil {
		f.csrfCookieSeen = true
	}
	if f.signupRejected || r.PostFormValue("csrfmiddlewaretoken") != f.csrf {
		_, _ = w.Write([]byte(`<p>errorlist</p>`))
		return
	}
	f.users[r.PostFormValue("username")] = r.PostFormValue("password1")
	http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "signup-session", Path: "/"})
	w.WriteHeader(http.StatusFound)
}

// serveProviderLogin answers the SSO button's submission with the redirect the
// app sends: to the issuer's authorize endpoint.
func (f *fakeApp) serveProviderLogin(w http.ResponseWriter, r *http.Request) {
	f.providerPosts++
	for _, c := range r.Cookies() {
		f.providerCookies = append(f.providerCookies, c.Name)
	}
	if f.providerError {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if r.PostFormValue("csrfmiddlewaretoken") != f.csrf {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.Header().Set("Location", "http://sso.localhost:8080/application/o/authorize/")
	w.WriteHeader(http.StatusFound)
}

// serveToken answers credentials with a token only when the account exists.
func (f *fakeApp) serveToken(w http.ResponseWriter, r *http.Request) {
	f.tokenPosts++
	for _, c := range r.Cookies() {
		f.tokenCookies = append(f.tokenCookies, c.Name)
	}
	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&creds)
	if password, ok := f.users[creds.Username]; ok && password == creds.Password {
		writeJSON(w, http.StatusOK, map[string]string{"token": fakeAPIToken})
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"non_field_errors":["Unable to log in with provided credentials."]}`))
}

// serveGroupList answers the group list, filtered by name the way the app's
// filter set does.
func (f *fakeApp) serveGroupList(w http.ResponseWriter, r *http.Request) {
	if !f.requireToken(w, r) {
		return
	}
	name := r.URL.Query().Get("name")
	results := []group{}
	for _, g := range f.groups {
		if g.Name == name {
			results = append(results, *g)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(results), "results": results})
}

// serveGroupCreate stores the group the configurator declares.
func (f *fakeApp) serveGroupCreate(w http.ResponseWriter, r *http.Request) {
	if !f.requireToken(w, r) {
		return
	}
	if f.groupWriteStatus != 0 {
		w.WriteHeader(f.groupWriteStatus)
		return
	}
	var body groupWrite
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.nextGroupID++
	g := &group{ID: f.nextGroupID, Name: body.Name, Permissions: body.Permissions}
	f.groups[body.Name] = g
	f.groupWrites = append(f.groupWrites, "POST "+body.Name)
	writeJSON(w, http.StatusCreated, g)
}

// serveGroupPatch replaces a group's permissions, as the app does.
func (f *fakeApp) serveGroupPatch(w http.ResponseWriter, r *http.Request) {
	if !f.requireToken(w, r) {
		return
	}
	if f.groupWriteStatus != 0 {
		w.WriteHeader(f.groupWriteStatus)
		return
	}
	id, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/groups/"), "/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	for _, g := range f.groups {
		if g.ID != id {
			continue
		}
		var body groupPermissionsWrite
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		g.Permissions = body.Permissions
		f.groupWrites = append(f.groupWrites, "PATCH "+g.Name)
		writeJSON(w, http.StatusOK, g)
		return
	}
	http.NotFound(w, r)
}

// requireToken enforces the app's authentication on the group API and records
// what each call presented, so a call without the admin's token fails here.
func (f *fakeApp) requireToken(w http.ResponseWriter, r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	f.groupTokens = append(f.groupTokens, auth)
	if auth != "Token "+fakeAPIToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"detail": "Authentication credentials were not provided."})
		return false
	}
	return true
}

// writeJSON answers with a JSON body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// testConfigurator builds a configurator whose API clients point at a fake app
// server, with the wait backoff collapsed so reconciliation probes resolve
// immediately. It returns the configurator and the app data dir the caller
// passes in AppState.
func testConfigurator(t *testing.T, app http.Handler) (*Configurator, string) {
	t.Helper()
	c := NewConfigurator(0, configurator.Deps{
		Secrets:        &fakeSecrets{password: "test-password"},
		PrimaryBaseURL: staticBaseURL("http://localhost:8080"),
		Logger:         quietLogger(),
	})
	if app != nil {
		server := httptest.NewServer(app)
		t.Cleanup(server.Close)
		c.baseURL = server.URL
	}
	c.api.cl.WithSleeper(func(time.Duration) {})
	c.api.forms.WithSleeper(func(time.Duration) {})
	c.api.signupForm.WithSleeper(func(time.Duration) {})
	return c, t.TempDir()
}

func appState(dataPath string, oidc *configurator.OIDCOutput) *configurator.AppState {
	return &configurator.AppState{DataPath: dataPath, SSOEnabled: oidc != nil, OIDC: oidc}
}

func testOIDC() *configurator.OIDCOutput {
	return &configurator.OIDCOutput{
		ClientID:     "paperless-client",
		ClientSecret: "secret-value",
		IssuerURL:    "http://sso.localhost:8080/application/o/paperless/",
		RedirectURI:  "http://paperless-ngx.localhost:8080" + callbackPath,
	}
}

func TestAppExternalURL_DerivesSubdomain(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8080":            "http://paperless-ngx.localhost:8080",
		"http://192.168.1.5:8080":          "http://paperless-ngx.192.168.1.5:8080",
		"https://bloud.example.com":        "https://paperless-ngx.bloud.example.com",
		"":                                 "http://paperless-ngx.localhost:8080",
		"://nonsense":                      "http://paperless-ngx.localhost:8080",
		"http://localhost:8080/":           "http://paperless-ngx.localhost:8080",
		"http://localhost:8080/dashboard/": "http://paperless-ngx.localhost:8080",
	}
	for base, want := range cases {
		c := NewConfigurator(0, configurator.Deps{PrimaryBaseURL: staticBaseURL(base), Logger: quietLogger()})
		assert.Equal(t, want, c.appExternalURL(), "base URL %q", base)
	}
}

func TestPreStart_WritesConfigAndReportsChange(t *testing.T) {
	c, dataPath := testConfigurator(t, nil)
	state := appState(dataPath, testOIDC())

	changed, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.True(t, changed.RestartNeeded, "first write must report a config change")

	path := filepath.Join(dataPath, "config", confFileName)
	info, err := os.Stat(path)
	require.NoError(t, err)
	// The webserver reads this file as the image's unprivileged user, which
	// rootless podman maps to a subuid: whoever owns the file on the host is
	// not that user, so it must be readable by others.
	assert.NotZero(t, info.Mode().Perm()&0o004, "config must be readable by others, got %v", info.Mode().Perm())

	conf := readConf(t, path)
	assert.Equal(t, "http://paperless-ngx.localhost:8080", conf["PAPERLESS_URL"])
	assert.NotEmpty(t, conf["PAPERLESS_SECRET_KEY"])

	// Idempotent: an identical second run must not report a change (the
	// orchestrator would otherwise recreate the container every cycle).
	changed, err = c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, changed.RestartNeeded, "identical config must not trigger a recreate")
}

func TestPreStart_WithoutSecretsProviderStillWritesConfig(t *testing.T) {
	// The config file carries only what the app itself reads; the admin
	// account is created over the app's API in PostStart, so a degraded
	// context (no secrets provider) can still configure the file.
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	dataPath := t.TempDir()

	changed, err := c.PreStart(context.Background(), appState(dataPath, testOIDC()))
	require.NoError(t, err)
	assert.True(t, changed.RestartNeeded)

	conf := readConf(t, filepath.Join(dataPath, "config", confFileName))
	assert.NotEmpty(t, conf["PAPERLESS_SECRET_KEY"])
	assert.NotContains(t, conf, "PAPERLESS_ADMIN_PASSWORD")
}

func TestPreStart_PreservesSecretKeyAcrossRuns(t *testing.T) {
	c, dataPath := testConfigurator(t, nil)

	_, err := c.PreStart(context.Background(), appState(dataPath, testOIDC()))
	require.NoError(t, err)
	path := filepath.Join(dataPath, "config", confFileName)
	first := readConf(t, path)["PAPERLESS_SECRET_KEY"]
	require.NotEmpty(t, first)

	// A rotated OIDC client must land, but the secret key must survive: it
	// signs sessions and API tokens, so a new value would sign every user out.
	rotated := testOIDC()
	rotated.ClientSecret = "rotated-secret"
	changed, err := c.PreStart(context.Background(), appState(dataPath, rotated))
	require.NoError(t, err)
	assert.True(t, changed.RestartNeeded, "rotated client secret must trigger a container recreate")

	conf := readConf(t, path)
	assert.Equal(t, first, conf["PAPERLESS_SECRET_KEY"])
	assert.Contains(t, conf["PAPERLESS_SOCIALACCOUNT_PROVIDERS"], "rotated-secret")
}

func TestRenderConf_RedirectProtocolFollowsPublicURL(t *testing.T) {
	// allauth builds the OIDC redirect URI from this value, and it must equal
	// the URL Bloud registers with the identity provider, so it is derived from
	// the app's public URL rather than left at allauth's https default.
	cases := map[string]string{
		"http://paperless-ngx.localhost:8080": "http",
		"https://paperless-ngx.example.com":   "https",
		"://unparseable":                      "http",
	}
	for publicURL, want := range cases {
		content, err := renderConf(publicSettings{publicURL: publicURL, secretKey: "k"})
		require.NoError(t, err)
		assert.Equal(t, want, parseConf(content)["PAPERLESS_ACCOUNT_DEFAULT_HTTP_PROTOCOL"], "public URL %q", publicURL)
	}
}

func TestRenderConf_OIDCProviderContract(t *testing.T) {
	content, err := renderConf(publicSettings{
		publicURL: "http://paperless-ngx.localhost:8080",
		secretKey: "secret-key",
		oidc:      testOIDC(),
	})
	require.NoError(t, err)
	conf := parseConf(content)

	assert.Equal(t, oidcApps, conf["PAPERLESS_APPS"])
	assert.Equal(t, "true", conf["PAPERLESS_SOCIAL_AUTO_SIGNUP"])
	assert.Equal(t, "true", conf["PAPERLESS_SOCIALACCOUNT_ALLOW_SIGNUPS"])
	// An SSO account lands in the group Bloud declares, and a member of the
	// instance's admin group becomes a superuser: without either, a signed-in
	// user's first API call answers 403.
	assert.Equal(t, baselineGroup, conf["PAPERLESS_SOCIAL_ACCOUNT_DEFAULT_GROUPS"])
	assert.Equal(t, adminGroupClaim, conf["PAPERLESS_SOCIAL_ACCOUNT_SYNC_SUPERUSER_GROUP"])
	// With SSO wired, the app's own password form is disabled and the sign-in
	// redirects to the issuer, so the provider is the user-facing way in.
	assert.Equal(t, "true", conf["PAPERLESS_DISABLE_REGULAR_LOGIN"])
	assert.Equal(t, "true", conf["PAPERLESS_REDIRECT_LOGIN_TO_SSO"])
	// Logging out must end the session at the issuer rather than landing on
	// the app's own sign-in page.
	assert.Equal(t, "http://sso.localhost:8080/application/o/paperless/end-session/", conf["PAPERLESS_LOGOUT_REDIRECT_URL"])

	var providers struct {
		OpenIDConnect struct {
			Apps []struct {
				ProviderID string `json:"provider_id"`
				ClientID   string `json:"client_id"`
				Secret     string `json:"secret"`
				Settings   struct {
					ServerURL string `json:"server_url"`
					FetchUser bool   `json:"fetch_userinfo"`
				} `json:"settings"`
			} `json:"APPS"`
		} `json:"openid_connect"`
	}
	require.NoError(t, json.Unmarshal([]byte(conf["PAPERLESS_SOCIALACCOUNT_PROVIDERS"]), &providers), "providers must be JSON")
	require.Len(t, providers.OpenIDConnect.Apps, 1)
	app := providers.OpenIDConnect.Apps[0]
	assert.Equal(t, providerID, app.ProviderID)
	assert.Equal(t, "paperless-client", app.ClientID)
	assert.Equal(t, "secret-value", app.Secret)
	// allauth fetches server_url itself, so it must be the discovery document
	// rather than the bare issuer.
	assert.Equal(t, "http://sso.localhost:8080/application/o/paperless/.well-known/openid-configuration", app.Settings.ServerURL)
	assert.True(t, app.Settings.FetchUser)
}

func TestRenderConf_WithoutOIDC_OmitsProviderSettings(t *testing.T) {
	content, err := renderConf(publicSettings{
		publicURL: "http://paperless-ngx.localhost:8080",
		secretKey: "secret-key",
	})
	require.NoError(t, err)
	conf := parseConf(content)

	assert.Equal(t, "http://paperless-ngx.localhost:8080", conf["PAPERLESS_URL"])
	assert.NotEmpty(t, conf["PAPERLESS_SECRET_KEY"])
	for _, key := range []string{
		"PAPERLESS_APPS",
		"PAPERLESS_SOCIALACCOUNT_PROVIDERS",
		"PAPERLESS_LOGOUT_REDIRECT_URL",
		"PAPERLESS_SOCIAL_ACCOUNT_DEFAULT_GROUPS",
		"PAPERLESS_SOCIAL_ACCOUNT_SYNC_SUPERUSER_GROUP",
		// An install without a provider keeps its local password login: it
		// would otherwise have no user-facing way in at all.
		"PAPERLESS_DISABLE_REGULAR_LOGIN",
		"PAPERLESS_REDIRECT_LOGIN_TO_SSO",
	} {
		_, ok := conf[key]
		assert.False(t, ok, "%s must be absent without SSO", key)
	}
}

func TestRenderConf_IsDeterministic(t *testing.T) {
	settings := publicSettings{
		publicURL: "http://paperless-ngx.localhost:8080",
		secretKey: "secret-key",
		oidc:      testOIDC(),
	}
	first, err := renderConf(settings)
	require.NoError(t, err)
	second, err := renderConf(settings)
	require.NoError(t, err)
	assert.Equal(t, first, second, "unchanged settings must render identical bytes")
}

func TestRenderConf_RejectsUnquotableValue(t *testing.T) {
	_, err := renderConf(publicSettings{publicURL: "http://paperless-ngx.localhost:8080/it's", secretKey: "k"})
	require.Error(t, err, "a quote would silently truncate the value into the app's settings")
}

func TestMetadata_MatchesRegistration(t *testing.T) {
	raw, err := os.ReadFile("metadata.yaml")
	require.NoError(t, err)

	// First occurrence wins: the file's top-level keys come before the nested
	// sso/containers blocks.
	metadata := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if _, seen := metadata[key]; !seen {
			metadata[key] = value
		}
	}

	// The catalog id and the container name the registry and the orchestrator
	// key on: a rename in one place would install an app with no configurator.
	assert.Equal(t, appName, metadata["name"])
	assert.Contains(t, string(raw), "- name: apps-"+appName+"\n")
	// The redirect URI the host-agent registers with the identity provider
	// must be the path allauth actually redirects to.
	assert.Equal(t, "native-oidc", metadata["strategy"])
	assert.Equal(t, callbackPath, metadata["callbackPath"])
}

func TestPostStart_FreshInstallCreatesAdminAndVerifiesProvider(t *testing.T) {
	app := newFakeApp()
	c, dataPath := testConfigurator(t, app)

	require.NoError(t, c.PostStart(context.Background(), appState(dataPath, testOIDC())))

	app.mu.Lock()
	defer app.mu.Unlock()
	require.Len(t, app.signupPosts, 1, "a fresh install must be bootstrapped through the signup form")
	form := app.signupPosts[0]
	assert.Equal(t, adminUser, form.Get("username"))
	assert.Equal(t, adminEmail, form.Get("email"))
	assert.Equal(t, "test-password", form.Get("password1"))
	assert.Equal(t, form.Get("password1"), form.Get("password2"))
	assert.Equal(t, app.csrf, form.Get("csrfmiddlewaretoken"), "the form token must be the one the page issued")
	assert.True(t, app.csrfCookieSeen, "the token must travel with the cookie it was issued against")
	assert.Equal(t, 1, app.tokenPosts, "the created account must be verified through the API")
	assert.Equal(t, 1, app.providerPosts, "the OIDC flow must be probed")
	// The signup leaves a session behind; neither the provider form nor the API
	// token request may carry it, or Django rejects both with a CSRF error.
	assert.NotContains(t, app.providerCookies, "sessionid", "the provider form must not inherit the signup session")
	assert.Empty(t, app.tokenCookies, "the token endpoint must be called without cookies")
}

func TestPostStart_EstablishedInstanceSkipsSignup(t *testing.T) {
	app := newFakeApp()
	app.signupClosed = true
	app.users[adminUser] = "test-password"
	c, dataPath := testConfigurator(t, app)

	require.NoError(t, c.PostStart(context.Background(), appState(dataPath, testOIDC())))

	app.mu.Lock()
	defer app.mu.Unlock()
	assert.Empty(t, app.signupPosts, "an instance that already has users must not be re-signed-up")
	assert.Equal(t, 1, app.tokenPosts)
}

func TestPostStart_FailsWhenSignupIsRejected(t *testing.T) {
	app := newFakeApp()
	app.signupRejected = true
	c, dataPath := testConfigurator(t, app)

	err := c.PostStart(context.Background(), appState(dataPath, testOIDC()))
	require.Error(t, err, "without an account the sign-in page has no SSO entry point")
	assert.Contains(t, err.Error(), "creating the internal admin account")
}

func TestPostStart_FailsWhenProviderNotAdvertised(t *testing.T) {
	app := newFakeApp()
	app.providerHidden = true
	c, dataPath := testConfigurator(t, app)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := c.PostStart(ctx, appState(dataPath, testOIDC()))
	require.Error(t, err, "a silently unconfigured provider must surface as an error")
	assert.Contains(t, err.Error(), "sign-in page")
}

func TestPostStart_FailsWhenProviderProbeErrors(t *testing.T) {
	app := newFakeApp()
	app.providerError = true
	c, dataPath := testConfigurator(t, app)

	err := c.PostStart(context.Background(), appState(dataPath, testOIDC()))
	require.Error(t, err, "a provider that cannot start the flow must surface as an error")
	assert.Contains(t, err.Error(), "authorization redirect")
}

func TestPostStart_DeclaresBaselineGroupOnFreshInstall(t *testing.T) {
	app := newFakeApp()
	c, dataPath := testConfigurator(t, app)

	require.NoError(t, c.PostStart(context.Background(), appState(dataPath, testOIDC())))

	app.mu.Lock()
	defer app.mu.Unlock()
	assert.Equal(t, []string{"POST " + baselineGroup}, app.groupWrites, "a fresh install must declare the group")
	created := app.groups[baselineGroup]
	require.NotNil(t, created, "the group an SSO account joins must exist before the first signup")
	// UISettings is the permission whose absence broke the dashboard: the web
	// app's own first request is GET /api/ui_settings/.
	assert.Contains(t, created.Permissions, "view_uisettings")
	assert.Contains(t, created.Permissions, "view_document")
	for _, auth := range app.groupTokens {
		assert.Equal(t, "Token "+fakeAPIToken, auth, "declaring the group must present the admin's token")
	}
	assert.Equal(t, 1, app.tokenPosts, "the admin token must be reused, not re-fetched")
}

func TestPostStart_LeavesConvergedBaselineGroupAlone(t *testing.T) {
	app := newFakeApp()
	app.groups[baselineGroup] = &group{ID: 1, Name: baselineGroup, Permissions: baselinePermissions()}
	c, dataPath := testConfigurator(t, app)

	require.NoError(t, c.PostStart(context.Background(), appState(dataPath, testOIDC())))

	app.mu.Lock()
	defer app.mu.Unlock()
	assert.Empty(t, app.groupWrites, "a converged group must not be rewritten")
}

func TestPostStart_RepairsDriftedBaselineGroup(t *testing.T) {
	app := newFakeApp()
	app.groups[baselineGroup] = &group{ID: 7, Name: baselineGroup, Permissions: []string{"view_document"}}
	c, dataPath := testConfigurator(t, app)

	require.NoError(t, c.PostStart(context.Background(), appState(dataPath, testOIDC())))

	app.mu.Lock()
	defer app.mu.Unlock()
	assert.Equal(t, []string{"PATCH " + baselineGroup}, app.groupWrites)
	assert.Contains(t, app.groups[baselineGroup].Permissions, "view_uisettings")
}

func TestPostStart_FailsWhenTheBaselineGroupIsRejected(t *testing.T) {
	// The group is what makes an SSO session usable, so a rejected declaration
	// must fail the reconciliation rather than leave users answering 403.
	app := newFakeApp()
	app.groupWriteStatus = http.StatusBadRequest
	c, dataPath := testConfigurator(t, app)

	err := c.PostStart(context.Background(), appState(dataPath, testOIDC()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "baseline group")
}

func TestSamePermissions_ComparesAsASet(t *testing.T) {
	// The app returns codenames sorted, and the declared list is ordered by
	// model: differing order is not drift, or every reconciliation would
	// rewrite the group. A duplicate in the declared list must not read as
	// drift either.
	assert.True(t, samePermissions([]string{"view_tag", "add_tag"}, []string{"add_tag", "view_tag"}))
	assert.True(t, samePermissions([]string{"view_tag"}, []string{"view_tag", "view_tag"}))
	assert.False(t, samePermissions([]string{"view_tag"}, []string{"view_tag", "add_tag"}))
	assert.False(t, samePermissions([]string{"view_tag", "add_tag"}, []string{"view_tag"}))
}

func TestPostStart_WithoutSSOSkipsProviderChecks(t *testing.T) {
	app := newFakeApp()
	app.providerHidden = true
	c, dataPath := testConfigurator(t, app)

	require.NoError(t, c.PostStart(context.Background(), appState(dataPath, nil)))

	app.mu.Lock()
	defer app.mu.Unlock()
	assert.Zero(t, app.providerPosts, "no provider probe without an OIDC output")
	assert.Empty(t, app.groupWrites, "no SSO accounts means no group to declare")
	assert.Len(t, app.signupPosts, 1, "the admin bootstrap runs regardless of SSO")
}

// readConf reads a generated config file from disk into a key/value map.
func readConf(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return parseConf(string(raw))
}

// parseConf parses the generated file the way python-dotenv reads our
// single-quoted values, so the tests see what the app would.
func parseConf(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), "'")
	}
	return out
}
