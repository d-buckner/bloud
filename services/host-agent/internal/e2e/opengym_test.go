// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var openGymURL = getEnvDefault("BLOUD_E2E_OPENGYM_URL", "http://localhost:8090")

func openGymPublicURL() string {
	return bloudSubdomainURL("opengym")
}

// TestOpenGymInstallViaAPI installs openGym through the API.
//
// The deadline covers a 0.48 GB pull for two images and, after it, the front end's
// PostStart proving the composed path. That proof is the slow part on a cold install:
// nginx re-resolves the API name on a timer, so the first request can wait one interval
// out.
func TestOpenGymInstallViaAPI(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/opengym/install", `{}`, http.StatusAccepted)
	waitAppRunning(t, "opengym", 5*time.Minute)
	waitHTTPOrFatal(t, 3*time.Minute, openGymURL+"/")
	waitRoutedApp(t, openGymPublicURL()+"/", 2*time.Minute)
}

// TestOpenGymServesItsAPIOnBothOrigins is the assertion this app needs most.
//
// openGym is two containers and the only interesting failure is the wire between them:
// the front end resolves the API name per request through nginx's `resolver`, and the
// address podman puts its network DNS on is not the address the image's own default
// assumes. When that goes wrong nothing refuses a connection. The static app serves,
// both containers report healthy, and every `/api` request hangs.
//
// So the test asks the question a browser asks, from both sides of the ingress: the
// published container port, and the routed public origin. The second half also covers
// Traefik, because a request that reaches the front end and never reaches the API looks
// identical from inside the network.
func TestOpenGymServesItsAPIOnBothOrigins(t *testing.T) {
	waitAppRunning(t, "opengym", 2*time.Minute)

	for _, base := range []string{openGymURL, openGymPublicURL()} {
		health := openGymHealth(t, base)
		if !health.OK {
			t.Errorf("%s%s did not report ok: %+v", base, "/api/health", health)
		}
		// The half a TCP probe cannot see: a data directory the app cannot write is a
		// server that answers and saves nothing.
		if !health.Writable {
			t.Errorf("%s%s reports the store is not writable; the mount is wrong or full", base, "/api/health")
		}
	}
}

// TestOpenGymTurnsOnTheSignInsBloudMeansToOffer reads the app's own config document.
//
// The app has no admin API without a profile, and reading a file the configurator wrote
// would only prove the file was written. This is the value arriving: `password_login`
// is what the login page reads to decide whether to render a form at all, and on a
// plain-http deployment reached by hostname that form is the only way in, because
// WebAuthn refuses to run there.
func TestOpenGymTurnsOnTheSignInsBloudMeansToOffer(t *testing.T) {
	waitAppRunning(t, "opengym", 2*time.Minute)

	resp, err := http.Get(openGymURL + "/api/config")
	if err != nil {
		t.Fatalf("GET /api/config: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/config: status %d, want 200: %s", resp.StatusCode, body)
	}

	var cfg struct {
		Config struct {
			PasswordLogin *bool `json:"password_login"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("decoding /api/config: %v\n%s", err, body)
	}
	if cfg.Config.PasswordLogin == nil || !*cfg.Config.PasswordLogin {
		t.Fatalf("the app does not offer password sign-in although PASSWORD_LOGIN=1 is declared: %s", body)
	}
}

// TestOpenGymAddressFileNamesThisInstall checks the values a passkey is bound to.
//
// A passkey registered under one hostname never authenticates under another, so an
// install whose RP_ID and ORIGIN disagree with the address people actually type is an
// install that fails every sign-in for a reason nothing in the app can show. The file
// is read on the host at container create and is deliberately not mounted, so this is
// the only place its contents are visible.
func TestOpenGymAddressFileNamesThisInstall(t *testing.T) {
	waitAppRunning(t, "opengym", 2*time.Minute)

	path := filepath.Join(appDataDir("opengym"), "config", "env")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s mode = %o, want 600", path, perm)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}

	wantOrigin := openGymPublicURL()
	if values["ORIGIN"] != wantOrigin {
		t.Errorf("ORIGIN = %q, want the routed origin %q", values["ORIGIN"], wantOrigin)
	}
	parsed, err := url.Parse(wantOrigin)
	if err != nil {
		t.Fatalf("parsing the routed origin %q: %v", wantOrigin, err)
	}
	// No scheme, no port: a credential is bound to a hostname and nothing else.
	if values["RP_ID"] != parsed.Hostname() {
		t.Errorf("RP_ID = %q, want the routed hostname %q", values["RP_ID"], parsed.Hostname())
	}
}

// TestOpenGymFirstProfileBecomesTheAdmin creates the first profile the way a person
// does, then checks the role the app assigned it.
//
// This is the whole of what Bloud promises about accounts: it does not create one, it
// makes sure the first one that appears can administer the instance. `admin: true` in
// the app's own answer is that promise observed from outside, and the user count moving
// off zero is the proof the write reached the mounted data directory rather than a
// container-local path that a recreate would delete.
func TestOpenGymFirstProfileBecomesTheAdmin(t *testing.T) {
	waitAppRunning(t, "opengym", 2*time.Minute)

	const name = "Bloud Integration"
	body := `{"name":"` + name + `","password":"bloud integration passphrase"}`
	req, err := http.NewRequest(http.MethodPost, openGymURL+"/api/register/password",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("building the register request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The app compares this against ORIGIN and refuses a mismatch, so a request with
	// no origin at all is not the request a browser makes.
	req.Header.Set("Origin", openGymPublicURL())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/register/password: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/register/password: status %d, want 200: %s", resp.StatusCode, raw)
	}

	var out struct {
		User struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Admin bool   `json:"admin"`
		} `json:"user"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding the register response: %v\n%s", err, raw)
	}
	if out.User.ID == "" {
		t.Fatalf("the app created no profile: %s", raw)
	}
	if !out.User.Admin {
		t.Errorf("the first profile is not an admin; FIRST_USER_ADMIN did not reach the app: %s", raw)
	}

	if health := openGymHealth(t, openGymURL); health.Users < 1 {
		t.Errorf("the app reports %d users after a profile was created; the data directory is not the mounted one", health.Users)
	}
}

// --- helpers ---

// openGymHealth reads the app's public health document.
func openGymHealth(t *testing.T, base string) openGymHealthBody {
	t.Helper()
	resp, err := http.Get(base + "/api/health")
	if err != nil {
		t.Fatalf("GET %s/api/health: %v", base, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s/api/health: status %d, want 200: %s", base, resp.StatusCode, body)
	}
	var health openGymHealthBody
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("decoding the health document from %s: %v\n%s", base, err, body)
	}
	return health
}

type openGymHealthBody struct {
	OK       bool `json:"ok"`
	Users    int  `json:"users"`
	Writable bool `json:"writable"`
}
