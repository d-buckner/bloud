// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var penpotURL = getEnvDefault("BLOUD_E2E_PENPOT_URL", "http://localhost:9002")

// The seven containers apps/penpot declares. Named here rather than derived
// from the catalog so the uninstall assertion can fail: a test that read the
// same list the planner used could never notice a node the orchestrator
// orphaned.
var penpotContainers = []string{
	"apps-penpot-postgres",
	"apps-penpot-valkey",
	"apps-penpot-backend",
	"apps-penpot-exporter",
	"apps-penpot-mcp",
	"apps-penpot-admin-console",
	"apps-penpot-frontend",
}

func penpotPublicURL() string {
	return bloudSubdomainURL("penpot")
}

// TestPenpotInstallViaAPI installs Penpot through the API.
//
// The deadline is the largest in the suite, and the reason is the shape of the
// install rather than any one slow part: seven containers, about 1.85 GB of
// images, a JVM backend that opens its listener only after its own migrations,
// and a frontend whose nginx renders its runtime config from the environment
// file at start. The health checks' own retries cover the slow boots; this
// covers the whole convergence.
func TestPenpotInstallViaAPI(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/penpot/install", `{}`, http.StatusAccepted)
	waitAppRunning(t, "penpot", 25*time.Minute)
	waitHTTPOrFatal(t, 2*time.Minute, penpotURL+"/js/config.js")
	waitRoutedApp(t, penpotPublicURL()+"/", 3*time.Minute)
}

// TestPenpotServesTheConfigurationBloudGenerated is the browser-visible half of
// the wiring. Penpot's SPA reads its flags, its public URI, and the identity
// provider's display name from this file, which the image's entrypoint renders
// from the environment at container start. So what an anonymous visitor can
// fetch is exactly what Bloud configured: if the file had not reached the
// container, or the image had ignored it, the defaults would come back instead.
func TestPenpotServesTheConfigurationBloudGenerated(t *testing.T) {
	waitAppRunning(t, "penpot", 2*time.Minute)

	for _, base := range []string{penpotURL, penpotPublicURL()} {
		body := httpGet(t, base+"/js/config.js")
		for _, want := range []string{
			"enable-login-with-oidc",
			"enable-mcp",
			"enable-admin-console",
			"Bloud SSO",
			`var penpotPublicURI = "` + penpotPublicURL() + `"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s/js/config.js does not contain %q; got:\n%s", base, want, body)
			}
		}
	}
}

// TestPenpotBackendReachedItsIdentityProvider is the assertion that earns this
// app's place in the suite. Penpot initializes the OIDC provider once, at
// startup, and when that fails it logs a warning and boots anyway with no
// provider: the login page still renders, the button is still there, and every
// sign-in fails. A node at RUNNING proves nothing about it.
//
// The two log lines are Penpot's own (app.auth.oidc, the provider init):
// success says "provider initialized", failure says "unable to initialize auth
// provider". The failure Penpot can actually hit in a Bloud install is
// discovery against a private issuer being refused by its SSRF guard, which is
// what PENPOT_SSRF_ALLOWED_HOSTS exists to prevent.
func TestPenpotBackendReachedItsIdentityProvider(t *testing.T) {
	waitAppRunning(t, "penpot", 2*time.Minute)

	logs := runCmd(t, "podman", "logs", "apps-penpot-backend")
	if strings.Contains(logs, "unable to initialize auth provider") {
		t.Fatal("Penpot could not reach its OIDC issuer at startup; the login page would " +
			"offer a provider that cannot authenticate anyone")
	}
	if !strings.Contains(logs, "provider initialized") {
		t.Fatal("the backend never reported an initialized auth provider; SSO is not configured")
	}
}

// TestPenpotEnvFileStaysOnTheHost checks the delivery channel: the file is read
// on the host when the container is created and is deliberately not mounted
// into it, so it must exist with the mode that keeps the OIDC client secret out
// of every other reader's reach, and with the same client credential Bloud
// registered at the provider.
func TestPenpotEnvFileStaysOnTheHost(t *testing.T) {
	waitAppRunning(t, "penpot", 2*time.Minute)

	path := filepath.Join(appDataDir("penpot"), "config", "penpot.env")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s mode = %o, want 600 (it holds an OIDC client secret)", path, perm)
	}

	values := envFile(t, path)
	if values["PENPOT_OIDC_CLIENT_ID"] == "" {
		t.Error("PENPOT_OIDC_CLIENT_ID is empty; the login page would offer a provider with no client")
	}
	if values["PENPOT_OIDC_CLIENT_SECRET"] == "" {
		t.Error("PENPOT_OIDC_CLIENT_SECRET is empty")
	}
	if values["PENPOT_OIDC_BASE_URI"] == "" {
		t.Error("PENPOT_OIDC_BASE_URI is empty; Penpot cannot discover the provider")
	}
	if want := penpotPublicURL() + "/api/auth/oidc/callback"; values["PENPOT_OIDC_REDIRECT_URI"] != want {
		t.Errorf("PENPOT_OIDC_REDIRECT_URI = %q, want %q", values["PENPOT_OIDC_REDIRECT_URI"], want)
	}
	if want := penpotPublicURL(); values["PENPOT_PUBLIC_URI"] != want {
		t.Errorf("PENPOT_PUBLIC_URI = %q, want %q", values["PENPOT_PUBLIC_URI"], want)
	}
	// The issuer host has to be in the allowlist or discovery is refused before
	// it starts: Penpot's SSRF guard rejects private and loopback addresses, and
	// a localhost deployment's issuer is loopback by design.
	if !strings.Contains(values["PENPOT_SSRF_ALLOWED_HOSTS"], "sso.localhost") {
		t.Errorf("PENPOT_SSRF_ALLOWED_HOSTS = %q, want it to name the issuer host",
			values["PENPOT_SSRF_ALLOWED_HOSTS"])
	}
	if values["PENPOT_SECRET_KEY"] == "" {
		t.Error("PENPOT_SECRET_KEY is empty; every restart would sign out every session")
	}
}

// TestPenpotMcpStaysUnpublishedUntilTheOperatorEnablesIt pins the contract's
// empty-means-not-ready rule on the real path. Penpot mints its MCP key itself,
// shown once, and only after a signed-in person turns the integration on in
// their profile. Bloud cannot invent that value, so a fresh install has nothing
// to publish, and a consumer must see nothing rather than a key that
// authenticates against nothing.
func TestPenpotMcpStaysUnpublishedUntilTheOperatorEnablesIt(t *testing.T) {
	waitAppRunning(t, "penpot", 2*time.Minute)

	published := readSecrets(t).AppSecrets["penpot"].Published
	if token := published["httpToken"]; token != "" {
		t.Errorf("penpot published an MCP token (%d chars) before anyone enabled MCP in Penpot", len(token))
	}
	values := readSecrets(t).AppSecrets["penpot"].PublishedValues
	if path := values["mcp"]["path"]; path != "" {
		t.Errorf("penpot published an MCP path %q with no token behind it; consumers must see one not-ready state, not half of one", path)
	}
}

// TestPenpotUninstallCleanup uninstalls Penpot through the API and asserts the
// full cleanup: store entry, all seven containers, data directory, routes, and
// the identity-provider objects Bloud created for it on install.
func TestPenpotUninstallCleanup(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/penpot/uninstall",
		`{"clearData":true}`, http.StatusAccepted)

	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if appStatus(t, "penpot") == "" {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if status := appStatus(t, "penpot"); status != "" {
		t.Fatalf("penpot still listed as installed (status %q)", status)
	}

	for _, name := range penpotContainers {
		if _, err := exec.Command("podman", "container", "exists", name).CombinedOutput(); err == nil {
			t.Errorf("%s container still exists after uninstall", name)
		}
	}

	if os.Getenv("BLOUD_DATA_DIR") != "" {
		if _, err := os.Stat(appDataDir("penpot")); err == nil {
			t.Errorf("data directory %s still exists after clearData uninstall", appDataDir("penpot"))
		}
	}

	traefikDir := os.Getenv("BLOUD_TRAEFIK_DYNAMIC_DIR")
	if traefikDir != "" {
		routesPath := filepath.Join(traefikDir, "apps-routes.yml")
		routes, err := os.ReadFile(routesPath)
		if err != nil {
			t.Fatalf("reading %s: %v", routesPath, err)
		}
		if strings.Contains(string(routes), "penpot") {
			t.Errorf("apps-routes.yml still references penpot after uninstall")
		}
	}

	if authentikApplicationExists(t, "penpot") {
		t.Errorf("Authentik application %q still exists after uninstall", "penpot")
	}
	if p := authentikProviderOrNil(t, "Penpot OAuth2 Provider"); p != nil {
		t.Errorf("Authentik OAuth2 provider %q still exists after uninstall", p.Name)
	}
	t.Log("penpot fully uninstalled: store, containers, data, routes, and SSO cleaned up")
}

// envFile parses a generated dotenv file into its key/value pairs.
func envFile(t *testing.T, path string) map[string]string {
	t.Helper()
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
	return values
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", url, resp.StatusCode, body)
	}
	return string(body)
}
