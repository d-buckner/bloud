// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var vaultwardenURL = getEnvDefault("BLOUD_E2E_VAULTWARDEN_URL", "http://localhost:8222")

// Vaultwarden integration values. They must match apps/vaultwarden: the
// callback path Vaultwarden derives from its DOMAIN, and the access token
// lifetime and extra scope the manifest asks the host-agent to give the
// identity provider.
const (
	vaultwardenCallbackPath   = "/identity/connect/oidc-signin"
	vaultwardenExtraScope     = "offline_access"
	vaultwardenTokenValidity  = "minutes=60"
	vaultwardenTokenExpirySec = 3600
)

// TestVaultwardenInstallViaAPI installs Vaultwarden through the API. It is a
// single container on its own SQLite database, so first boot is quick.
func TestVaultwardenInstallViaAPI(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/vaultwarden/install", `{}`, http.StatusAccepted)
	waitAppRunning(t, "vaultwarden", 10*time.Minute)
	waitHTTPOrFatal(t, 60*time.Second, vaultwardenURL+"/alive")
}

// TestVaultwardenConfiguredByConfigurator verifies the configurator's outcomes
// behaviorally: the generated env file reached the running app (SSO is
// available), the app hands the browser to the issuer with the registered
// callback and the extra scope, and the host-agent gave the identity provider
// the scope and token lifetime the manifest declares.
func TestVaultwardenConfiguredByConfigurator(t *testing.T) {
	waitAppRunning(t, "vaultwarden", 2*time.Minute)
	b := newSSOBrowser(t)

	token := vaultwardenPrevalidate(t, b)

	// The app fetches the issuer's discovery document to build the redirect,
	// so a redirect proves the issuer is reachable from inside the container.
	location := vaultwardenAuthorize(t, b, token, "state", "challenge")
	authorize, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parsing authorize redirect %q: %v", location, err)
	}
	// Authentik's authorize endpoint is global (from the discovery document),
	// not per-application.
	if authorize.Path != "/application/o/authorize/" {
		t.Errorf("redirect must point at the issuer's authorize endpoint, got %q", location)
	}
	q := authorize.Query()
	if got, want := q.Get("redirect_uri"), vaultwardenPublicURL()+vaultwardenCallbackPath; got != want {
		t.Errorf("redirect_uri = %q, want %q (the callback Bloud registers)", got, want)
	}
	if !strings.Contains(" "+q.Get("scope")+" ", " "+vaultwardenExtraScope+" ") {
		t.Errorf("scope %q must include %s", q.Get("scope"), vaultwardenExtraScope)
	}
	if strings.Count(" "+q.Get("scope")+" ", " openid ") != 1 {
		t.Errorf("scope %q must carry openid exactly once", q.Get("scope"))
	}

	provider := authentikProvider(t, "Vaultwarden OAuth2 Provider")
	if provider.AccessTokenValidity != vaultwardenTokenValidity {
		t.Errorf("provider access_token_validity = %q, want %q", provider.AccessTokenValidity, vaultwardenTokenValidity)
	}
	if scopes := authentikScopeNames(t, provider.PropertyMappings); !containsString(scopes, vaultwardenExtraScope) {
		t.Errorf("provider scopes %v must include %s", scopes, vaultwardenExtraScope)
	}

	// The file carries the OIDC client secret: it must not be readable by
	// other users. (Only checkable when the test shares the data dir.)
	if os.Getenv("BLOUD_DATA_DIR") != "" {
		path := filepath.Join(dataDir(), "vaultwarden", "config", "vaultwarden.env")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode = %v, want no group/other access", path, info.Mode().Perm())
		}
	}
}

// TestVaultwardenSSOLoginCreatesAccount runs a complete sign-in the way the web
// vault does: prevalidate, authorize, Authentik's login flow, the callback, and
// the code exchange. It asserts what only a real login proves:
//   - the callback Bloud registered with the provider is the one the flow uses;
//   - a first sign-in creates the account even though local self-registration
//     is closed (SSO signup is its own switch);
//   - the session is refreshable and lives as long as the manifest declares.
func TestVaultwardenSSOLoginCreatesAccount(t *testing.T) {
	waitAppRunning(t, "vaultwarden", 2*time.Minute)
	// A throwaway identity provider user, so the test depends on no existing
	// account's password and leaves nothing behind in Authentik.
	username, password := createAuthentikUser(t)
	b := newSSOBrowser(t)

	verifier := randomString(t, 48)
	challenge := base64.RawURLEncoding.EncodeToString(sha256Sum(verifier))
	ssoToken := vaultwardenPrevalidate(t, b)
	issuerAuthorize := vaultwardenAuthorize(t, b, ssoToken, "bloud-e2e-state", challenge)

	// The provider sends an unauthenticated browser to its login flow.
	resp, _ := b.get(issuerAuthorize)
	next := resp.Header.Get("Location")
	if resp.StatusCode/100 != 3 || !strings.Contains(next, "/flows/-/default/authentication/") {
		t.Fatalf("issuer authorize = %d Location %q, want a redirect into the login flow", resp.StatusCode, next)
	}
	nextURL, err := url.Parse(next)
	if err != nil {
		t.Fatal(err)
	}
	issuer, _ := url.Parse(issuerAuthorize)
	callbackURL := authentikFlowLogin(t, b, issuer, nextURL.Query().Get("next"), username, password)

	// The provider hands the browser back to the callback Bloud registered.
	wantCallback := vaultwardenPublicURL() + vaultwardenCallbackPath
	if !strings.HasPrefix(callbackURL, wantCallback+"?") || !strings.Contains(callbackURL, "code=") {
		t.Fatalf("provider redirected to %q, want %s?code=...", callbackURL, wantCallback)
	}

	// Vaultwarden validates the state and hands the code to the web vault's
	// connector page.
	resp, _ = b.get(callbackURL)
	connector := resp.Header.Get("Location")
	if resp.StatusCode/100 != 3 || !strings.Contains(connector, "/sso-connector.html") {
		t.Fatalf("oidc-signin = %d Location %q, want a redirect to sso-connector.html", resp.StatusCode, connector)
	}
	connectorURL, _ := url.Parse(connector)
	code := connectorURL.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in %q", connector)
	}

	// The exchange is where the account is created (or refused).
	tokens := vaultwardenExchange(t, b, code, verifier)
	if tokens.RefreshToken == "" {
		t.Error("no refresh token: offline_access did not reach the app")
	}
	// The lifetime counts down from when the provider issued the token, so it
	// is a moment short of the declared 60 minutes, and nowhere near the 5
	// minute default.
	if tokens.ExpiresIn > vaultwardenTokenExpirySec || tokens.ExpiresIn < vaultwardenTokenExpirySec-60 {
		t.Errorf("expires_in = %d, want about %d (the lifetime the manifest declares)", tokens.ExpiresIn, vaultwardenTokenExpirySec)
	}
	profile := vaultwardenProfile(t, b, tokens.AccessToken)
	if want := username + "@bloud.test"; profile.Email != want {
		t.Errorf("the created account's email = %q, want the identity provider user's %q", profile.Email, want)
	}
	t.Logf("SSO sign-in created a Vaultwarden account for %s", profile.Email)

	// With SSO wired, local self-registration is closed for everyone.
	resp, body := b.post(vaultwardenPublicURL()+"/identity/accounts/register/send-verification-email",
		"application/json", `{"email":"registration-probe@bloud.test","name":"probe"}`)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "Registration not allowed") {
		t.Errorf("local registration = %d %s, want 400 \"Registration not allowed\"", resp.StatusCode, body)
	}

	// The identity provider is the only way in: the master-password login grant
	// is refused (SSO_ONLY). The SSO exchange above and token refresh are not
	// affected, so existing sessions keep working.
	passwordGrant := url.Values{
		"grant_type":       {"password"},
		"username":         {profile.Email},
		"password":         {"not-checked"},
		"scope":            {"api offline_access"},
		"client_id":        {"web"},
		"deviceType":       {"9"},
		"deviceIdentifier": {"11111111-2222-3333-4444-555555555556"},
		"deviceName":       {"bloud-e2e"},
	}
	resp, body = b.post(vaultwardenPublicURL()+"/identity/connect/token", "application/x-www-form-urlencoded", passwordGrant.Encode())
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "SSO sign-in is required") {
		t.Errorf("master-password login = %d %s, want 400 \"SSO sign-in is required\"", resp.StatusCode, body)
	}
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tokens.RefreshToken}, "client_id": {"web"}}
	resp, body = b.post(vaultwardenPublicURL()+"/identity/connect/token", "application/x-www-form-urlencoded", refresh.Encode())
	if resp.StatusCode != http.StatusOK {
		t.Errorf("token refresh = %d %s, want 200: SSO_ONLY must not end existing sessions", resp.StatusCode, body)
	}
}

// TestVaultwardenUninstallCleanup uninstalls Vaultwarden through the API and
// asserts the cleanup the platform performs: store entry, container, data
// directory, and routes. (Removing the app's Authentik provider and application
// is not part of uninstall for any native-oidc app today, so it is not asserted.)
func TestVaultwardenUninstallCleanup(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/vaultwarden/uninstall", `{"clearData":true}`, http.StatusAccepted)

	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if appStatus(t, "vaultwarden") == "" {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if status := appStatus(t, "vaultwarden"); status != "" {
		t.Fatalf("vaultwarden still listed as installed (status %q)", status)
	}

	if _, err := exec.Command("podman", "container", "exists", "apps-vaultwarden").CombinedOutput(); err == nil {
		t.Error("apps-vaultwarden container still exists after uninstall")
	}
	if os.Getenv("BLOUD_DATA_DIR") != "" {
		dataPath := filepath.Join(dataDir(), "vaultwarden")
		if _, err := os.Stat(dataPath); err == nil {
			t.Errorf("data directory %s still exists after clearData uninstall", dataPath)
		}
	}
	if traefikDir := os.Getenv("BLOUD_TRAEFIK_DYNAMIC_DIR"); traefikDir != "" {
		routes, err := os.ReadFile(filepath.Join(traefikDir, "apps-routes.yml"))
		if err != nil {
			t.Fatalf("reading apps-routes.yml: %v", err)
		}
		if strings.Contains(string(routes), "vaultwarden") {
			t.Errorf("apps-routes.yml still references vaultwarden after uninstall")
		}
	}
}

// --- SSO browser -------------------------------------------------------

// ssoBrowser behaves like a browser for the parts of the sign-in flow that
// matter here: it keeps cookies per host, hands every redirect back to the test
// instead of following it (so each hop is asserted), and resolves *.localhost
// to loopback as browsers do (Bloud serves apps on <app>.localhost:8080).
type ssoBrowser struct {
	t      *testing.T
	client *http.Client
}

func newSSOBrowser(t *testing.T) *ssoBrowser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &ssoBrowser{t: t, client: &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err == nil && (host == "localhost" || strings.HasSuffix(host, ".localhost")) {
					addr = net.JoinHostPort("127.0.0.1", port)
				}
				return dialer.DialContext(ctx, network, addr)
			},
		},
	}}
}

func (b *ssoBrowser) do(req *http.Request) (*http.Response, []byte) {
	b.t.Helper()
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func (b *ssoBrowser) get(rawURL string) (*http.Response, []byte) {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	return b.do(req)
}

func (b *ssoBrowser) getAuth(rawURL, bearer string) (*http.Response, []byte) {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	return b.do(req)
}

func (b *ssoBrowser) post(rawURL, contentType, body string) (*http.Response, []byte) {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	return b.do(req)
}

// --- Vaultwarden steps ----------------------------------------------------

// vaultwardenPublicURL is the URL a browser uses to reach Vaultwarden (its
// DOMAIN), derived from the primary Bloud base URL the same way the
// configurator does.
func vaultwardenPublicURL() string {
	return bloudSubdomainURL("vaultwarden")
}

// bloudSubdomainURL returns the app-subdomain URL on the primary Bloud base
// URL, e.g. "http://vaultwarden.localhost:8080".
func bloudSubdomainURL(app string) string {
	base := os.Getenv("BLOUD_SSO_BASE_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return "http://" + app + ".localhost:8080"
	}
	parsed.Host = app + "." + parsed.Host
	parsed.Path, parsed.RawPath, parsed.RawQuery, parsed.Fragment = "", "", "", ""
	parsed.User = nil
	return strings.TrimSuffix(parsed.String(), "/")
}

// vaultwardenPrevalidate is the first call the clients make for an SSO login:
// a token when SSO is available, an error when the config did not reach the app.
func vaultwardenPrevalidate(t *testing.T, b *ssoBrowser) string {
	t.Helper()
	resp, body := b.get(vaultwardenPublicURL() + "/identity/sso/prevalidate")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /identity/sso/prevalidate = %d %s: SSO is not enabled in the running app", resp.StatusCode, body)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		t.Fatalf("prevalidate returned no token: %s", body)
	}
	return out.Token
}

// vaultwardenAuthorize starts the authorization request as the web vault does
// and returns the issuer URL Vaultwarden redirects the browser to.
func vaultwardenAuthorize(t *testing.T, b *ssoBrowser, ssoToken, state, challenge string) string {
	t.Helper()
	q := url.Values{
		"client_id":             {"web"},
		"redirect_uri":          {vaultwardenPublicURL() + "/sso-connector.html"},
		"response_type":         {"code"},
		"scope":                 {"api offline_access"},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"response_mode":         {"query"},
		"domain_hint":           {"bloud"},
		"ssoToken":              {ssoToken},
	}
	resp, body := b.get(vaultwardenPublicURL() + "/identity/connect/authorize?" + q.Encode())
	location := resp.Header.Get("Location")
	if resp.StatusCode/100 != 3 || location == "" {
		t.Fatalf("GET /identity/connect/authorize = %d %s, want a redirect to the issuer "+
			"(a 400 means discovery against the issuer failed from inside the container)", resp.StatusCode, body)
	}
	return location
}

type vaultwardenTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// vaultwardenExchange trades the authorization code for tokens, which is where
// Vaultwarden creates the account on a first sign-in.
func vaultwardenExchange(t *testing.T, b *ssoBrowser, code, verifier string) vaultwardenTokens {
	t.Helper()
	form := url.Values{
		"grant_type":       {"authorization_code"},
		"code":             {code},
		"code_verifier":    {verifier},
		"redirect_uri":     {vaultwardenPublicURL() + "/sso-connector.html"},
		"client_id":        {"web"},
		"scope":            {"api offline_access"},
		"deviceType":       {"9"},
		"deviceIdentifier": {"11111111-2222-3333-4444-555555555555"},
		"deviceName":       {"bloud-e2e"},
	}
	resp, body := b.post(vaultwardenPublicURL()+"/identity/connect/token", "application/x-www-form-urlencoded", form.Encode())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /identity/connect/token = %d %s", resp.StatusCode, body)
	}
	var tokens vaultwardenTokens
	if err := json.Unmarshal(body, &tokens); err != nil || tokens.AccessToken == "" {
		t.Fatalf("no access token in %s", body)
	}
	return tokens
}

func vaultwardenProfile(t *testing.T, b *ssoBrowser, accessToken string) struct{ Email string } {
	t.Helper()
	resp, body := b.getAuth(vaultwardenPublicURL()+"/api/accounts/profile", accessToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/accounts/profile = %d %s", resp.StatusCode, body)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	email, _ := raw["email"].(string)
	if email == "" {
		email, _ = raw["Email"].(string)
	}
	return struct{ Email string }{Email: email}
}

// --- Authentik steps ------------------------------------------------------

// authentikFlowLogin signs in through Authentik's flow executor on the issuer's
// own host (the session cookie is per host, and the issuer is not the direct
// Authentik port) and returns the URL the provider redirects the browser to
// once authorized.
func authentikFlowLogin(t *testing.T, b *ssoBrowser, issuer *url.URL, next, username, password string) string {
	t.Helper()
	origin := issuer.Scheme + "://" + issuer.Host
	executor := origin + "/api/v3/flows/executor/default-authentication-flow/?query=" +
		url.QueryEscape(url.Values{"next": {next}}.Encode())

	stage := authentikStage(t, b, "GET", executor, "")
	if stage.Component != "ak-stage-identification" {
		t.Fatalf("login flow starts at %q, want ak-stage-identification", stage.Component)
	}
	stage = authentikStage(t, b, "POST", executor, fmt.Sprintf(`{"component":"ak-stage-identification","uid_field":%q}`, username))
	if stage.Component != "ak-stage-password" {
		t.Fatalf("after identification the flow is at %q, want ak-stage-password", stage.Component)
	}
	stage = authentikStage(t, b, "POST", executor, fmt.Sprintf(`{"component":"ak-stage-password","password":%q}`, password))
	if stage.Component != "xak-flow-redirect" || stage.To == "" {
		t.Fatalf("after the password the flow is at %q (%+v), want a redirect", stage.Component, stage)
	}

	authorized := stage.To
	if strings.HasPrefix(authorized, "/") {
		authorized = origin + authorized
	}
	resp, body := b.get(authorized)
	location := resp.Header.Get("Location")
	if resp.StatusCode/100 != 3 || location == "" {
		t.Fatalf("authorize after login = %d %s, want a redirect back to the app", resp.StatusCode, body)
	}
	return location
}

type authentikStageResponse struct {
	Component string `json:"component"`
	To        string `json:"to"`
}

// authentikStage runs one flow executor request. A successful stage POST is
// answered with a redirect to the executor's GET, which carries the next
// challenge, so redirects to the executor are followed here.
func authentikStage(t *testing.T, b *ssoBrowser, method, executor, body string) authentikStageResponse {
	t.Helper()
	var resp *http.Response
	var data []byte
	req, err := http.NewRequest(method, executor, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, data = b.do(req)
	for hop := 0; hop < 4 && resp.StatusCode/100 == 3; hop++ {
		next, err := resp.Location()
		if err != nil {
			t.Fatalf("flow redirect without a Location: %v", err)
		}
		resp, data = b.get(next.String())
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("flow executor %s = %d %s", method, resp.StatusCode, data)
	}
	var stage authentikStageResponse
	if err := json.Unmarshal(data, &stage); err != nil {
		t.Fatalf("decoding flow challenge: %v (%s)", err, data)
	}
	return stage
}

type authentikProviderInfo struct {
	Name                string   `json:"name"`
	AccessTokenValidity string   `json:"access_token_validity"`
	PropertyMappings    []string `json:"property_mappings"`
}

func authentikAPI(t *testing.T, path string, out any) {
	t.Helper()
	authentikRequest(t, http.MethodGet, path, nil, out, http.StatusOK)
}

// authentikRequest calls the Authentik API with the host-agent's token and
// requires wantStatus. A non-nil out receives the decoded JSON response.
func authentikRequest(t *testing.T, method, path string, body, out any, wantStatus int) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequest(method, authentikURL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+authentikToken(t))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s = %d (want %d) %s", method, path, resp.StatusCode, wantStatus, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decoding %s %s: %v", method, path, err)
		}
	}
}

// createAuthentikUser creates a throwaway user with a known password and
// registers its deletion. The email is on a reserved test domain; Bloud's
// verified-email scope mapping reports it as verified, which Vaultwarden
// requires before it creates an account.
func createAuthentikUser(t *testing.T) (username, password string) {
	t.Helper()
	username = "vw-e2e-" + strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(randomString(t, 6)))
	password = randomString(t, 18)
	var created struct {
		PK int `json:"pk"`
	}
	authentikRequest(t, http.MethodPost, "/api/v3/core/users/", map[string]any{
		"username":  username,
		"name":      "Vaultwarden e2e",
		"email":     username + "@bloud.test",
		"is_active": true,
		"path":      "users",
	}, &created, http.StatusCreated)
	t.Cleanup(func() {
		authentikRequest(t, http.MethodDelete, fmt.Sprintf("/api/v3/core/users/%d/", created.PK), nil, nil, http.StatusNoContent)
	})
	authentikRequest(t, http.MethodPost, fmt.Sprintf("/api/v3/core/users/%d/set_password/", created.PK),
		map[string]string{"password": password}, nil, http.StatusNoContent)
	return username, password
}

func authentikProviderOrNil(t *testing.T, name string) *authentikProviderInfo {
	t.Helper()
	var list struct {
		Results []authentikProviderInfo `json:"results"`
	}
	authentikAPI(t, "/api/v3/providers/oauth2/?search="+url.QueryEscape(name), &list)
	for i := range list.Results {
		if list.Results[i].Name == name {
			return &list.Results[i]
		}
	}
	return nil
}

func authentikProvider(t *testing.T, name string) *authentikProviderInfo {
	t.Helper()
	p := authentikProviderOrNil(t, name)
	if p == nil {
		t.Fatalf("Authentik provider %q not found", name)
	}
	return p
}

// authentikScopeNames resolves scope mapping UUIDs to their scope names.
func authentikScopeNames(t *testing.T, mappingIDs []string) []string {
	t.Helper()
	var list struct {
		Results []struct {
			PK        string `json:"pk"`
			ScopeName string `json:"scope_name"`
		} `json:"results"`
	}
	authentikAPI(t, "/api/v3/propertymappings/provider/scope/?page_size=100", &list)
	byID := map[string]string{}
	for _, m := range list.Results {
		byID[m.PK] = m.ScopeName
	}
	var names []string
	for _, id := range mappingIDs {
		if name, ok := byID[id]; ok {
			names = append(names, name)
		}
	}
	return names
}

// --- small helpers ----------------------------------------------------------

func randomString(t *testing.T, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func sha256Sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
