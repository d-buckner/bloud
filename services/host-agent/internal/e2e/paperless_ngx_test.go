// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var paperlessNgxURL = getEnvDefault("BLOUD_E2E_PAPERLESS_NGX_URL", "http://localhost:8000")

// Paperless-ngx integration values. They must match apps/paperless-ngx: the
// provider id allauth registers, the admin username the container creates
// from the generated config file, and the group Bloud declares for SSO
// accounts.
const (
	paperlessNgxAdminUser        = "bloud-admin"
	paperlessNgxProviderLogin    = "/accounts/oidc/bloud/login/"
	paperlessNgxProviderCallback = "/accounts/oidc/bloud/login/callback/"
	paperlessNgxBaselineGroup    = "bloud-users"
)

// paperlessNgxNodes are the app's graph nodes, one container each.
var paperlessNgxNodes = []string{
	"apps-paperless-ngx",
	"apps-paperless-ngx-postgres",
	"apps-paperless-ngx-redis",
	"apps-paperless-ngx-gotenberg",
	"apps-paperless-ngx-tika",
}

// TestPaperlessNgxInstallViaAPI installs Paperless-ngx through the API. The graph
// spans five containers (webserver, postgres, redis, gotenberg, tika); first
// boot runs Django migrations before the webserver listener opens.
func TestPaperlessNgxInstallViaAPI(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/paperless-ngx/install", `{}`, http.StatusAccepted)
	// A fresh VM pulls five images (about 5 GB unpacked) and migrates on first
	// boot; allow a generous deadline.
	waitAppRunning(t, "paperless-ngx", 30*time.Minute)
	waitHTTPOrFatal(t, 60*time.Second, paperlessNgxURL+"/accounts/login/")
}

// TestPaperlessNgxConfiguredByConfigurator verifies the configurator's outcomes
// behaviorally: the generated config file reaches the running app (the sign-in
// page advertises the provider), the provider hands the browser to the issuer
// with the registered callback, the internal admin account authenticates, and
// an uploaded document travels the whole consume pipeline (redis -> celery ->
// parser -> search index).
func TestPaperlessNgxConfiguredByConfigurator(t *testing.T) {
	waitAppRunning(t, "paperless-ngx", 2*time.Minute)

	signIn := paperlessNgxGet(t, "/accounts/login/", "")
	if !strings.Contains(signIn, paperlessNgxProviderLogin) {
		t.Errorf("sign-in page must advertise %s: the generated provider settings did not reach the app", paperlessNgxProviderLogin)
	}

	// allauth fetches the issuer's discovery document to build the
	// authorization URL, so a redirect carrying a callback Bloud registered
	// proves the issuer is reachable from inside the container and the client
	// resolves. allauth derives the redirect URI from the host it is asked on,
	// and Bloud registers one per base URL plus the direct-port debug URL, so
	// the expectation is the callback on the URL this test probes.
	location := paperlessNgxProviderRedirect(t)
	// Authentik's authorize endpoint is global (discovered from the
	// application's discovery document), not per-application.
	if !strings.Contains(location, "/application/o/authorize/") {
		t.Errorf("provider redirect must point at the issuer's authorize endpoint, got %q", location)
	}
	wantCallback := paperlessNgxURL + paperlessNgxProviderCallback
	if !strings.Contains(location, url.QueryEscape(wantCallback)) {
		t.Errorf("provider redirect must carry redirect_uri %q, got %q", wantCallback, location)
	}

	password := readSecrets(t).AppSecrets["paperless-ngx"].AdminPassword
	if password == "" {
		t.Fatal("no admin password for paperless-ngx in secrets.json")
	}
	token := paperlessNgxToken(t, password)
	paperlessNgxIngestsUploadedDocument(t, token)
}

// TestPaperlessNgxBaselineGroupGrantsWebAppAccess asserts what a signed-in SSO
// account can do, which is the part membership alone does not prove:
// Paperless-ngx grants a new user no permissions and gates every REST endpoint
// on model permissions, so the web app's own first calls (/api/ui_settings/,
// /api/saved_views/) answer 403 for an account without them, and the dashboard
// renders but never works. Bloud declares the group social signups join; this
// places a user in that group through the app's API and checks the API accepts
// the session.
func TestPaperlessNgxBaselineGroupGrantsWebAppAccess(t *testing.T) {
	waitAppRunning(t, "paperless-ngx", 2*time.Minute)

	adminToken := paperlessNgxToken(t, readSecrets(t).AppSecrets["paperless-ngx"].AdminPassword)
	groupID := paperlessNgxBaselineGroupID(t, adminToken)
	memberToken := paperlessNgxCreateMemberInGroup(t, adminToken, groupID)

	for _, path := range []string{"/api/ui_settings/", "/api/saved_views/"} {
		paperlessNgxGet(t, path, memberToken)
	}
	t.Log("a member of the baseline group can use the API")
}

// paperlessNgxBaselineGroupID returns the id of the group SSO accounts join and
// fails if it is missing or carries no permissions: a group that exists but
// grants nothing satisfies membership and still answers 403.
func paperlessNgxBaselineGroupID(t *testing.T, adminToken string) int {
	t.Helper()
	body := paperlessNgxGet(t, "/api/groups/?name="+url.QueryEscape(paperlessNgxBaselineGroup), adminToken)
	var list struct {
		Results []struct {
			ID          int      `json:"id"`
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decoding the group list: %v (%s)", err, body)
	}
	for _, g := range list.Results {
		if g.Name != paperlessNgxBaselineGroup {
			continue
		}
		if len(g.Permissions) == 0 {
			t.Fatalf("group %s grants no permissions: an SSO session would answer 403", paperlessNgxBaselineGroup)
		}
		return g.ID
	}
	t.Fatalf("group %s does not exist: SSO signups would land with no permissions", paperlessNgxBaselineGroup)
	return 0
}

// paperlessNgxCreateMemberInGroup creates an account in the baseline group, which
// is the state PAPERLESS_SOCIAL_ACCOUNT_DEFAULT_GROUPS produces at signup, and
// returns its API token.
func paperlessNgxCreateMemberInGroup(t *testing.T, adminToken string, groupID int) string {
	t.Helper()
	const (
		username = "bloud-member"
		password = "bloud-member-password-1"
	)
	body := fmt.Sprintf(`{"username":%q,"password":%q,"groups":[%d]}`, username, password, groupID)
	payload := paperlessNgxPostJSON(t, "/api/users/", adminToken, body)

	var created struct {
		ID     int   `json:"id"`
		Groups []int `json:"groups"`
	}
	if err := json.Unmarshal(payload, &created); err != nil {
		t.Fatalf("decoding the created user: %v (%s)", err, payload)
	}
	if !slices.Contains(created.Groups, groupID) {
		t.Fatalf("the created user is not in group %d: %s", groupID, payload)
	}
	return paperlessNgxTokenFor(t, username, password)
}

// paperlessNgxPostJSON posts a JSON body to the app with an API token and fails
// the test on anything but 201.
func paperlessNgxPostJSON(t *testing.T, path, token, body string) []byte {
	t.Helper()
	req, err := http.NewRequest("POST", paperlessNgxURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("building request for %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Token "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s: status %d: %s", path, resp.StatusCode, truncateBody(payload))
	}
	return payload
}

// TestPaperlessNgxUninstallCleanup uninstalls Paperless-ngx through the API and
// asserts the full cleanup: store entry, all five containers, data directory,
// and routes.
func TestPaperlessNgxUninstallCleanup(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/paperless-ngx/uninstall",
		`{"clearData":true}`, http.StatusAccepted)

	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if appStatus(t, "paperless-ngx") == "" {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if status := appStatus(t, "paperless-ngx"); status != "" {
		t.Fatalf("paperless-ngx still listed as installed (status %q)", status)
	}

	for _, name := range paperlessNgxNodes {
		if _, err := exec.Command("podman", "container", "exists", name).CombinedOutput(); err == nil {
			t.Errorf("%s container still exists after uninstall", name)
		}
	}

	if os.Getenv("BLOUD_DATA_DIR") != "" {
		dataPath := filepath.Join(dataDir(), "paperless-ngx")
		if _, err := os.Stat(dataPath); err == nil {
			t.Errorf("data directory %s still exists after clearData uninstall", dataPath)
		}
	}

	traefikDir := os.Getenv("BLOUD_TRAEFIK_DYNAMIC_DIR")
	if traefikDir != "" {
		routesPath := filepath.Join(traefikDir, "apps-routes.yml")
		routes, err := os.ReadFile(routesPath)
		if err != nil {
			t.Fatalf("reading %s: %v", routesPath, err)
		}
		if strings.Contains(string(routes), "paperless-ngx") {
			t.Errorf("apps-routes.yml still references paperless-ngx after uninstall")
		}
	}
	t.Log("paperless-ngx fully uninstalled: store, containers, data, and routes cleaned up")
}

// paperlessNgxProviderRedirect starts the authorization-code flow the way the
// browser does: fetch the provider login page (allauth's confirmation page,
// which carries the CSRF token), then post the form and read the redirect.
func paperlessNgxProviderRedirect(t *testing.T) string {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	confirm, err := client.Get(paperlessNgxURL + paperlessNgxProviderLogin)
	if err != nil {
		t.Fatalf("GET %s: %v", paperlessNgxProviderLogin, err)
	}
	page, _ := io.ReadAll(confirm.Body)
	confirm.Body.Close()
	if confirm.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", paperlessNgxProviderLogin, confirm.StatusCode, page)
	}

	csrf := csrfTokenRe.FindSubmatch(page)
	if csrf == nil {
		t.Fatalf("no CSRF token in the provider confirmation page: %s", truncateBody(page))
	}

	resp, err := client.PostForm(paperlessNgxURL+paperlessNgxProviderLogin,
		url.Values{"csrfmiddlewaretoken": {string(csrf[1])}})
	if err != nil {
		t.Fatalf("POST %s: %v", paperlessNgxProviderLogin, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("POST %s: status %d, want a redirect to the issuer: %s",
			paperlessNgxProviderLogin, resp.StatusCode, truncateBody(body))
	}
	return resp.Header.Get("Location")
}

// paperlessNgxToken authenticates as the internal admin account. It doubles as
// the check that the container created that account from the generated config
// file: a missing user or password answers 401.
func paperlessNgxToken(t *testing.T, password string) string {
	t.Helper()
	return paperlessNgxTokenFor(t, paperlessNgxAdminUser, password)
}

// paperlessNgxTokenFor exchanges an account's credentials for an API token.
func paperlessNgxTokenFor(t *testing.T, username, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	resp, err := http.Post(paperlessNgxURL+"/api/token/", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/token/: %v", err)
	}
	payload, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/token/ for %s: status %d: %s", username, resp.StatusCode, payload)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("decoding token response: %v (%s)", err, payload)
	}
	if out.Token == "" {
		t.Fatalf("token response carries no token: %s", payload)
	}
	return out.Token
}

// paperlessNgxIngestsUploadedDocument uploads a plain-text document through the
// REST API and waits until it is searchable with its text extracted. That path
// covers the broker (redis), the Celery worker, the document parser, and the
// search index, which is what makes the app useful rather than merely running.
func paperlessNgxIngestsUploadedDocument(t *testing.T, token string) {
	t.Helper()
	const marker = "bloud-integration-probe"

	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	part, err := writer.CreateFormFile("document", "bloud-probe.txt")
	if err != nil {
		t.Fatalf("building upload: %v", err)
	}
	if _, err := fmt.Fprintf(part, "Bloud integration probe %s\n", marker); err != nil {
		t.Fatalf("building upload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("building upload: %v", err)
	}

	req, err := http.NewRequest("POST", paperlessNgxURL+"/api/documents/post_document/", &upload)
	if err != nil {
		t.Fatalf("building upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Token "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/documents/post_document/: %v", err)
	}
	payload, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/documents/post_document/: status %d: %s", resp.StatusCode, payload)
	}

	deadline := time.Now().Add(3 * time.Minute)
	var last string
	for time.Now().Before(deadline) {
		for _, id := range paperlessNgxDocumentIDs(t, token) {
			content := paperlessNgxGet(t, fmt.Sprintf("/api/documents/%s/", id), token)
			var doc struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(content), &doc); err != nil {
				t.Fatalf("decoding document %s: %v (%s)", id, err, content)
			}
			if strings.Contains(doc.Content, marker) {
				return
			}
			last = fmt.Sprintf("document %s content %q", id, doc.Content)
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("uploaded document was not consumed with its text extracted within the deadline (last: %s)", last)
}

// paperlessNgxDocumentIDs lists the ids of the documents the API reports.
func paperlessNgxDocumentIDs(t *testing.T, token string) []string {
	t.Helper()
	body := paperlessNgxGet(t, "/api/documents/?page_size=5", token)
	var list struct {
		Results []struct {
			ID int `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decoding document list: %v (%s)", err, body)
	}
	ids := make([]string, 0, len(list.Results))
	for _, doc := range list.Results {
		ids = append(ids, fmt.Sprint(doc.ID))
	}
	return ids
}

// paperlessNgxGet fetches a path from the app, optionally with an API token, and
// fails the test on anything but 200.
func paperlessNgxGet(t *testing.T, path, token string) string {
	t.Helper()
	req, err := http.NewRequest("GET", paperlessNgxURL+path, nil)
	if err != nil {
		t.Fatalf("building request for %s: %v", path, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Token "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, resp.StatusCode, truncateBody(body))
	}
	return string(body)
}

var csrfTokenRe = regexp.MustCompile(`name="csrfmiddlewaretoken"\s+value="([^"]+)"`)

// truncateBody keeps failure output readable.
func truncateBody(body []byte) string {
	const limit = 200
	if len(body) > limit {
		return string(body[:limit]) + "..."
	}
	return string(body)
}
