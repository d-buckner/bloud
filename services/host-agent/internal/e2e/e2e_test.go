// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

// Package e2e contains integration tests that run against a host-agent
// deployment exercising the real product path: the catalog planner, the
// intent queue, and the orchestrator's dependency-graph reconciliation.
//
// The test binary runs inside the VM next to the deployed host-agent. It
// drives every mutation through the host-agent HTTP API (install, uninstall);
// the only direct container interactions are read-only inspection and one
// fault injection (podman stop) that simulates a container crash. Recovery
// from that crash is performed by the host-agent's startup convergence, not
// by the test harness.
//
// Contract under test:
//   - System apps (traefik, authentik) auto-install and converge on boot
//   - Authentik LDAP infrastructure is created by the server PostStart during
//     convergence; the LDAP outpost gets its real token via the shared
//     template-var flow (no manual restart, no env file rewriting)
//   - Installing Jellyfin through the API runs the full graph: containers,
//     PreStart (LDAP plugin), PostStart (wizard, libraries, LDAP config)
//   - A crashed container is recovered when the host-agent restarts
//   - Uninstalling through the API removes containers, data, and routes
//
// Run with:
//
//	go test -tags integration -c -o bloud-integration.test ./internal/e2e/...
//	./bloud-integration.test -test.v
//
// Prerequisites (provided by `./bloud validate --tier integration`):
//   - host-agent deployed and running, API on localhost:3000
//   - BLOUD_DATA_DIR points at the runtime data dir (secrets.json, api-token)
//   - BLOUD_E2E_HOST_AGENT_UNIT names the unit supervising the host-agent
//     process (crash-recovery test)
//   - BLOUD_TRAEFIK_DYNAMIC_DIR points at the Traefik dynamic config dir
//   - ldapsearch available (ldap-utils)
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/dirs"
)

// Service endpoints: published by the catalog to localhost inside the VM.
var (
	hostAgentURL = getEnvDefault("BLOUD_E2E_HOST_AGENT_URL", "http://localhost:3000")
	jellyfinURL  = getEnvDefault("BLOUD_E2E_JELLYFIN_URL", "http://localhost:8096")
	authentikURL = getEnvDefault("BLOUD_E2E_AUTHENTIK_URL", "http://localhost:9001")
	ldapURL      = getEnvDefault("BLOUD_E2E_LDAP_URL", "ldap://localhost:3389")
)

// expectedLDAPHost is the LDAP host the Jellyfin configurator must be given.
// It matches config.Load's BLOUD_LDAP_HOST default (the catalog container
// name), overridable for exotic deployments.
func expectedLDAPHost() string {
	if h := os.Getenv("BLOUD_E2E_LDAP_HOST"); h != "" {
		return h
	}
	return "apps-authentik-ldap"
}

// LDAP expected values: must match config.Load defaults and
// apps/jellyfin/configurator.go desiredLDAPConfig.

const (
	expectedLDAPPort     = 3389
	expectedLDAPBaseDN   = "dc=ldap,dc=goauthentik,dc=io"
	expectedLDAPBindUser = "cn=ldap-service,ou=users,dc=ldap,dc=goauthentik,dc=io"
)

// Jellyfin managed bootstrap admin: the username matches apps/jellyfin; the
// password is generated per-deployment and read from secrets.json (never hardcoded).

const (
	bootstrapUsername = "bloud-bootstrap-admin"
	ldapPluginID      = "958aad6637844d2ab89aa7b6fab6e25c"
)

type secretsFile struct {
	AuthentikBootstrapPassword string `json:"authentikBootstrapPassword"`
	AuthentikBootstrapToken    string `json:"authentikBootstrapToken"`
	LdapBindPassword           string `json:"ldapBindPassword"`
	AppSecrets                 map[string]struct {
		AdminPassword string `json:"adminPassword"`
	} `json:"appSecrets"`
}

// dataDir returns the runtime data root (secrets.json, bloud.db, the
// generated proxy config). App state is not directly in here: it sits one
// level down, so use appDataDir for that. The deployer sets BLOUD_DATA_DIR;
// the standard default is the fallback for direct runs.
func dataDir() string {
	if d := os.Getenv("BLOUD_DATA_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "bloud")
}

// appDataDir returns one app's private tree in the runtime under test. It
// resolves through the same dirs helper the orchestrator uses to render
// {{appDataDir}} and to fill AppState.DataPath, so the harness cannot drift
// from the on-disk layout.
//
// This matters more than a normal helper. Most of these paths feed an
// os.Stat inside a "must be gone after uninstall" assertion: point it at a
// directory that never existed and the assertion passes forever. Routing
// through dirs is what keeps that check able to fail.
func appDataDir(app string) string {
	return dirs.AppDataDir(dataDir(), app)
}

func readSecrets(t *testing.T) secretsFile {
	t.Helper()
	path := filepath.Join(dataDir(), "secrets.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var s secretsFile
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return s
}

// authentikToken returns the host-agent's long-lived Authentik API token,
// using the same priority as config.getAuthentikToken: the api-token file
// written by the Authentik server PostStart (always valid), then the
// one-shot bootstrap token from secrets.json (first boot only).
func authentikToken(t *testing.T) string {
	t.Helper()
	if data, err := os.ReadFile(filepath.Join(appDataDir("authentik"), "api-token")); err == nil {
		if token := strings.TrimSpace(string(data)); token != "" {
			return token
		}
	}
	if token := readSecrets(t).AuthentikBootstrapToken; token != "" {
		return token
	}
	t.Fatal("no Authentik API token available (api-token file or bootstrap token)")
	return ""
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// runCmd executes a local command and returns its combined output.
func runCmd(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\noutput:\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// --- Authenticated host-agent client ---

// Admin API calls require a credential even from a trusted position (loopback /
// BLOUD_TRUSTED_LOCAL_NETS): position is a scope, not a credential. The
// credential is the runtime's API token, written by the secrets manager next to
// secrets.json; these tests run inside the runtime, so they read it directly.
type tokenTransport struct {
	base  http.RoundTripper
	token string
}

func (t tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.URL.String(), hostAgentURL) {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(req)
}

var agentClientOnce = sync.OnceValues(func() (*http.Client, error) {
	token, err := readRuntimeAPIToken()
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: tokenTransport{base: http.DefaultTransport, token: token}}, nil
})

// agentClient returns a client that authenticates to the host-agent.
func agentClient(t *testing.T) *http.Client {
	t.Helper()
	c, err := agentClientOnce()
	if err != nil {
		t.Fatalf("host-agent API credential unavailable: %v", err)
	}
	return c
}

// readRuntimeAPIToken reads the host-agent API token from the runtime data dir
// (mirrors cli/executor.DataDirs.APITokenPath; the filename is pinned by
// internal/secrets.TestAPITokenFileNameIsStable).
func readRuntimeAPIToken() (string, error) {
	path := filepath.Join(dataDir(), "host-agent-api-token")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return token, nil
}

func agentGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := agentClient(t).Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func agentPost(t *testing.T, url, contentType string, body io.Reader) *http.Response {
	t.Helper()
	resp, err := agentClient(t).Post(url, contentType, body)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// waitHTTP polls url until it returns 200 or the deadline passes.
func waitHTTP(timeout time.Duration, url string) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out after %s waiting for %s", timeout, url)
}

func waitHTTPOrFatal(t *testing.T, timeout time.Duration, url string) {
	t.Helper()
	if err := waitHTTP(timeout, url); err != nil {
		t.Fatalf("host not ready: %v", err)
	}
}

// installedApp is one entry of GET /api/apps/installed.

type installedApp struct {
	CatalogID string `json:"catalog_id"`
	Status    string `json:"status"`
	IsSystem  bool   `json:"is_system"`
}

func getInstalledApps(t *testing.T) []installedApp {
	t.Helper()
	resp := agentGet(t, hostAgentURL+"/api/apps/installed")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /api/apps/installed: status %d: %s", resp.StatusCode, body)
	}
	var payload struct {
		Apps []installedApp `json:"apps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload.Apps
}

func appStatus(t *testing.T, catalogID string) string {
	t.Helper()
	for _, app := range getInstalledApps(t) {
		if app.CatalogID == catalogID {
			return app.Status
		}
	}
	return ""
}

// appUsesLDAP reports whether the catalog app authenticates over LDAP. The
// metadata is the single source of truth, so the readiness barrier below
// follows the app's declared strategy rather than a hardcoded list of app
// names.
func appUsesLDAP(t *testing.T, catalogID string) bool {
	t.Helper()
	resp := agentGet(t, fmt.Sprintf("%s/api/apps/%s/metadata", hostAgentURL, catalogID))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var app struct {
		SSO struct {
			Strategy string `json:"strategy"`
		} `json:"sso"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&app); err != nil {
		return false
	}
	return app.SSO.Strategy == "ldap"
}

// waitAppRunning polls until the app reaches "running" status. For an app that
// authenticates over LDAP it then waits for the outpost to accept a bind:
// every app install re-provisions Authentik and can restart the outpost, and
// the app's own containers can be up while the directory is still coming back.
// That window is a readiness gap, not an app failure, so the framework absorbs
// it here rather than turning it into a 401 or 500 in whichever test runs next.
func waitAppRunning(t *testing.T, catalogID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if appStatus(t, catalogID) == "running" {
			if appUsesLDAP(t, catalogID) {
				waitLDAPReady(t, 3*time.Minute)
			}
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s to reach running (last status %q)",
		timeout, catalogID, appStatus(t, catalogID))
}

// fallbackDashboardMarker is the title of the page host-agent serves for every
// unmatched path when no frontend build sits next to it. The integration tier
// deploys no frontend on purpose, so this page is exactly what a request finds
// when the ingress has not been pointed at the app yet.
const fallbackDashboardMarker = "Bloud Dev Dashboard"

// waitRoutedApp polls a path on the app's routed (public) URL until the app
// itself answers. It is the second half of the readiness barrier that
// waitAppRunning starts, and it covers a gap the store cannot see.
//
// waitAppRunning proves the orchestrator marked the node RUNNING. It does not
// prove the ingress serves the app: the orchestrator regenerates the Traefik
// dynamic config immediately before that promotion, and Traefik applies the
// new file a moment later. A request inside that window is routed by the
// previous config and never reaches the app, while the store already reads
// "running" and reports nothing missing.
//
// The probe tells the two apart by who answered, not by status: the fallback
// page arrives with a 200, so a status check passes on the wrong responder.
// A body that is not that page means the app answered.
//
// Tests that dial the container's own port do not need this. That is why an
// install test can pass against :8222 while the next test's first call on the
// routed URL lands on host-agent instead of the app.
func waitRoutedApp(t *testing.T, probeURL string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := "no response"
	for time.Now().Before(deadline) {
		resp, err := http.Get(probeURL)
		if err != nil {
			last = err.Error()
		} else {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			last = fmt.Sprintf("%d %s %.80s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
			if resp.StatusCode == http.StatusOK && !strings.Contains(string(body), fallbackDashboardMarker) {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out after %s: %s never served the app (last response: %s)", timeout, probeURL, last)
}

// resetUserApps uninstalls every installed user app through the API so the
// suite always starts from a clean slate, regardless of prior state.
func resetUserApps() error {
	apps, err := fetchInstalled()
	if err != nil {
		return err
	}
	for _, app := range apps {
		if err := postUninstall(app.CatalogID); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		apps, err := fetchInstalled()
		if err != nil {
			return err
		}
		remaining := 0
		for _, app := range apps {
			if !app.IsSystem {
				remaining++
			}
		}
		if remaining == 0 {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("timed out waiting for uninstall reset to complete")
}

func TestMain(m *testing.M) {
	if err := waitHTTP(120*time.Second, hostAgentURL+"/api/health"); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: host-agent API not reachable:", err)
		os.Exit(1)
	}
	// Clean slate before the suite: uninstall whatever user apps a previous
	// run (or dev session) left behind.
	if err := resetUserApps(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: reset failed:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func fetchInstalled() ([]installedApp, error) {
	// Called from TestMain too, so it cannot depend on *testing.T.
	client, err := agentClientOnce()
	if err != nil {
		return nil, err
	}
	resp, err := client.Get(hostAgentURL + "/api/apps/installed")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /api/apps/installed: status %d", resp.StatusCode)
	}
	var payload struct {
		Apps []installedApp `json:"apps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Apps, nil
}

func postUninstall(catalogID string) error {
	client, err := agentClientOnce()
	if err != nil {
		return err
	}
	resp, err := client.Post(fmt.Sprintf("%s/api/apps/%s/uninstall", hostAgentURL, catalogID),
		"application/json", strings.NewReader(`{"clearData":true}`))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("uninstall %s: status %d: %s", catalogID, resp.StatusCode, body)
	}
	return nil
}

// ensureAppUninstalled uninstalls catalogID when the runtime still has it, and
// waits for the record to disappear. A test that asserts on a *fresh* install
// calls this first: Go runs tests in file order, so "nothing installed this app
// yet" is a precondition the test must establish, not one it may inherit from
// whichever file happened to run before it.
func ensureAppUninstalled(t *testing.T, catalogID string) {
	t.Helper()
	installed := func() (bool, error) {
		apps, err := fetchInstalled()
		if err != nil {
			return false, err
		}
		for _, app := range apps {
			if app.CatalogID == catalogID {
				return true, nil
			}
		}
		return false, nil
	}

	present, err := installed()
	if err != nil {
		t.Fatalf("reading installed apps: %v", err)
	}
	if !present {
		return
	}
	if err := postUninstall(catalogID); err != nil {
		t.Fatalf("uninstalling %s: %v", catalogID, err)
	}

	deadline := time.Now().Add(4 * time.Minute)
	for {
		present, err := installed()
		if err == nil && !present {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s is still installed after %s (last error: %v)", catalogID, 4*time.Minute, err)
		}
		time.Sleep(3 * time.Second)
	}
}

// postJSON POSTs a JSON body and asserts the expected status code.
func postJSON(t *testing.T, url string, body string, wantStatus int) {
	t.Helper()
	resp := agentPost(t, url, "application/json", strings.NewReader(body))
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s: status %d (want %d): %s", url, resp.StatusCode, wantStatus, data)
	}
}
