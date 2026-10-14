// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
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

	// The half of the wiring a config file cannot prove. get-profile is the SPA's
	// first call and it reaches every part of the install: nginx, the JVM, its
	// database, and (because enable-admin-console is on) the admin console, since
	// the RPC ends in with-nitrate-licence. When that last call fails Penpot
	// answers 503 {:type :nitrate-unavailable} and the browser renders
	// "temporarily unavailable" with no login page, on an install whose every
	// container reports healthy. httpGet prints the body on a non-200, which is
	// where Penpot names the failing dependency.
	httpGet(t, penpotPublicURL()+"/api/main/methods/get-profile")
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
			"offer a provider that cannot authenticate anyone.\nBackend's own words:\n" +
			logContext(logs, "unable to initialize auth provider") +
			"\nWhat the container can actually see:\n" +
			oidcDiagnosis(envFile(t, filepath.Join(appDataDir("penpot"), "config", "penpot.env"))["PENPOT_OIDC_BASE_URI"]))
	}
	if !strings.Contains(logs, "provider initialized") {
		t.Fatal("the backend never reported an initialized auth provider; SSO is not configured.\n" +
			"Last lines of the backend log:\n" + tailLines(logs, 25))
	}
}

// logContext returns the log lines that carry the marker plus their neighbours,
// because Penpot writes the reason for a failed provider init next to the
// warning, not in it. A failure that only quotes the warning is a failure the
// reader has to go reproduce by hand.
// softRun runs a diagnostic. Unlike runCmd it never fails the test: a probe that
// could not run is itself an answer, and the point of these is to be printed
// next to a failure that already happened.
func softRun(name string, args ...string) string {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return strings.TrimSpace(fmt.Sprintf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out))
	}
	return strings.TrimSpace(string(out))
}

// oidcDiagnosis answers the question Penpot's log cannot. "unable to discover
// OIDC configuration" wraps at least three different faults (the SSRF guard
// refusing the address, the issuer name not resolving inside the container, the
// proxy answering something other than 200) and they need three different
// fixes, so the test reports what the container sees instead of guessing: the
// two variables that decide the guard, whether the name resolves, and the status
// line of the actual discovery request made from inside the container.
func oidcDiagnosis(baseURI string) string {
	var b strings.Builder

	env := softRun("podman", "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", "apps-penpot-backend")
	for _, line := range strings.Split(env, "\n") {
		if strings.HasPrefix(line, "PENPOT_SSRF_ALLOWED_HOSTS=") || strings.HasPrefix(line, "PENPOT_OIDC_BASE_URI=") {
			b.WriteString("  container env: " + line + "\n")
		}
	}

	parsed, err := url.Parse(baseURI)
	if err != nil || parsed.Hostname() == "" {
		return b.String() + "  could not parse PENPOT_OIDC_BASE_URI: " + baseURI + "\n"
	}

	host, port := parsed.Hostname(), parsed.Port()
	if port == "" {
		port = "80"
	}
	b.WriteString("  resolves to: " + softRun("podman", "exec", "apps-penpot-backend", "getent", "hosts", host) + "\n")

	// The image has no curl and no wget; bash is installed (it is the image's own
	// entrypoint) and its /dev/tcp pseudo-file makes the request the container
	// would make, from inside the container.
	path := strings.TrimSuffix(parsed.Path, "/") + "/.well-known/openid-configuration"
	script := fmt.Sprintf(
		"exec 3<>/dev/tcp/%s/%s || { echo 'connect failed'; exit 0; }; "+
			"printf 'GET %s HTTP/1.0\r\nHost: %s\r\n\r\n' >&3; head -n 1 <&3", host, port, path, host)
	b.WriteString("  " + baseURI + ".well-known/openid-configuration from inside: " +
		softRun("podman", "exec", "apps-penpot-backend", "bash", "-c", script) + "\n")
	return b.String()
}

func logContext(logs, marker string) string {
	lines := strings.Split(logs, "\n")
	var out []string
	for i, line := range lines {
		if !strings.Contains(line, marker) {
			continue
		}
		from, to := i-2, i+6
		if from < 0 {
			from = 0
		}
		if to > len(lines) {
			to = len(lines)
		}
		out = append(out, lines[from:to]...)
	}
	if len(out) == 0 {
		return tailLines(logs, 25)
	}
	return strings.Join(out, "\n")
}

// tailLines returns the last n lines of a log.
func tailLines(logs string, n int) string {
	lines := strings.Split(strings.TrimRight(logs, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
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
	// There is no PENPOT_OIDC_REDIRECT_URI to check: Penpot has no such config
	// key. backend/src/app/auth/oidc.clj builds the redirect URI as
	// PENPOT_PUBLIC_URI with the path replaced by /api/auth/oidc/callback, so
	// the assertion that matters is the one below on PENPOT_PUBLIC_URI, and the
	// callback path is the one metadata.yaml declares to Authentik.
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

// TestPenpotAdminConsoleStaysUp pins the failure that is invisible from the
// container list. The admin console is a dependency of the profile RPC, not an
// optional page: when it is down every Penpot request, the login page's first
// one included, answers 503 {:type :nitrate-unavailable}, and the container can
// sit in that state while the graph calls the install converged. It also exits
// rather than degrading when its own base path is wrong, so "running" is the
// assertion, and the narrow env file is what keeps it running.
func TestPenpotAdminConsoleStaysUp(t *testing.T) {
	waitAppRunning(t, "penpot", 2*time.Minute)

	state := strings.TrimSpace(runCmd(t, "podman", "inspect", "-f", "{{.State.Status}}", "apps-penpot-admin-console"))
	if state != "running" {
		t.Fatalf("apps-penpot-admin-console is %q; Penpot answers 503 for everything while it is down.\n"+
			"Its own words:\n%s", state, tailLines(runCmd(t, "podman", "logs", "apps-penpot-admin-console"), 20))
	}

	values := envFile(t, filepath.Join(appDataDir("penpot"), "config", "penpot-admin-console.env"))
	if v, ok := values["PENPOT_ADMIN_CONSOLE_URI"]; ok {
		t.Errorf("the admin console's env file carries PENPOT_ADMIN_CONSOLE_URI=%q; the image reads that as its own base path and exits", v)
	}
	if values["PENPOT_SECRET_KEY"] == "" {
		t.Error("the admin console has no signing key to derive the backend's shared secret from")
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
