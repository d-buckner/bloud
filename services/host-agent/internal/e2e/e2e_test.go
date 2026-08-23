// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// Service endpoints — published by the catalog to localhost inside the VM.
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

// LDAP expected values — must match config.Load defaults and
// apps/jellyfin/configurator.go desiredLDAPConfig.
const (
	expectedLDAPPort     = 3389
	expectedLDAPBaseDN   = "dc=ldap,dc=goauthentik,dc=io"
	expectedLDAPBindUser = "cn=ldap-service,ou=users,dc=ldap,dc=goauthentik,dc=io"
)

// Jellyfin bootstrap admin — must match constants in apps/jellyfin/configurator.go
const (
	bootstrapUsername = "bloud-bootstrap-admin"
	bootstrapPassword = "bloud-bootstrap-password-change-me"
	ldapPluginID      = "958aad6637844d2ab89aa7b6fab6e25c"
)

type secretsFile struct {
	AuthentikBootstrapPassword string `json:"authentikBootstrapPassword"`
	AuthentikBootstrapToken    string `json:"authentikBootstrapToken"`
	LdapBindPassword           string `json:"ldapBindPassword"`
}

// dataDir returns the runtime data directory (secrets.json, api-token, app
// data). The deployer sets BLOUD_DATA_DIR; the standard default is the
// fallback for direct runs.
func dataDir() string {
	if d := os.Getenv("BLOUD_DATA_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "bloud")
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
	if data, err := os.ReadFile(filepath.Join(dataDir(), "authentik", "api-token")); err == nil {
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

// waitHTTP polls url until it returns 200 or the deadline passes.
func waitHTTP(timeout time.Duration, url string) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), "GET", url, nil)
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
	resp, err := http.Get(hostAgentURL + "/api/apps/installed")
	if err != nil {
		t.Fatalf("GET /api/apps/installed: %v", err)
	}
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

// waitAppRunning polls until the app reaches "running" status.
func waitAppRunning(t *testing.T, catalogID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if appStatus(t, catalogID) == "running" {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s to reach running (last status %q)",
		timeout, catalogID, appStatus(t, catalogID))
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
	resp, err := http.Get(hostAgentURL + "/api/apps/installed")
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
	resp, err := http.Post(fmt.Sprintf("%s/api/apps/%s/uninstall", hostAgentURL, catalogID),
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

// postJSON POSTs a JSON body and asserts the expected status code.
func postJSON(t *testing.T, url string, body string, wantStatus int) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s: status %d (want %d): %s", url, resp.StatusCode, wantStatus, data)
	}
}

// --- System apps (auto-installed and converged on boot) ---

// TestSystemAppsConverged verifies the bootstrap contract: system apps are
// auto-installed by the orchestrator and their containers are up, running,
// and carry the managed labels (architecture invariant 10).
func TestSystemAppsConverged(t *testing.T) {
	want := map[string]string{
		"apps-traefik":            "traefik",
		"apps-authentik-postgres": "authentik",
		"apps-authentik-redis":    "authentik",
		"apps-authentik-server":   "authentik",
		"apps-authentik-worker":   "authentik",
		"apps-authentik-ldap":     "authentik",
	}
	for name, app := range want {
		out := runCmd(t, "podman", "inspect", "-f",
			`{{.State.Running}}|{{ index .Config.Labels "io.bloud.managed" }}|{{ index .Config.Labels "io.bloud.app" }}`, name)
		parts := strings.Split(out, "|")
		if len(parts) != 3 {
			t.Fatalf("%s: unexpected inspect output %q", name, out)
		}
		running, managed, gotApp := parts[0], parts[1], parts[2]
		if running != "true" {
			t.Errorf("%s: not running (State.Running=%s)", name, running)
		}
		if managed != "true" {
			t.Errorf("%s: missing io.bloud.managed=true label", name)
		}
		if gotApp != app {
			t.Errorf("%s: io.bloud.app = %q, want %q", name, gotApp, app)
		}
	}
}

func TestAuthentikHealthCheck(t *testing.T) {
	waitHTTPOrFatal(t, 120*time.Second, authentikURL+"/-/health/ready/")
}

// TestAuthentikLDAPOutpostCreated verifies that the LDAP outpost exists after
// convergence — created by the authentik server PostStart as part of the
// normal lifecycle, with no manual configurator invocation.
func TestAuthentikLDAPOutpostCreated(t *testing.T) {
	token := authentikToken(t)

	req, err := http.NewRequestWithContext(context.Background(), "GET",
		authentikURL+"/api/v3/outposts/instances/?search=LDAP", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET outposts: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET outposts: status %d: %s", resp.StatusCode, body)
	}

	var outpostResp struct {
		Results []struct {
			Name string `json:"name"`
			PK   string `json:"pk"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&outpostResp); err != nil {
		t.Fatal(err)
	}
	if len(outpostResp.Results) == 0 {
		t.Fatal("no LDAP outpost found after convergence")
	}
	t.Logf("LDAP outpost present: %s (pk=%s)", outpostResp.Results[0].Name, outpostResp.Results[0].PK)
}

// TestAuthentikAdminLogin verifies real password authentication through the
// Authentik flow executor (not just API health or token access).
func TestAuthentikAdminLogin(t *testing.T) {
	adminPassword := readSecrets(t).AuthentikBootstrapPassword
	if adminPassword == "" {
		t.Fatal("authentikBootstrapPassword not found in secrets.json")
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}

	flowURL := authentikURL + "/api/v3/flows/executor/default-authentication-flow/"

	resp, err := client.Get(flowURL)
	if err != nil {
		t.Fatalf("GET flow executor: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET flow executor: status %d", resp.StatusCode)
	}

	// The product's admin user is "admin", created by the app configurator
	// (scripts/set_admin_password.py) with the bootstrap password. The
	// AUTHENTIK_BOOTSTRAP_PASSWORD env var is not consumed by authentik
	// 2025.10.x, so the built-in "akadmin" user is not the product admin.
	resp, err = client.Post(flowURL, "application/json", strings.NewReader(`{"uid_field":"admin"}`))
	if err != nil {
		t.Fatalf("POST identification: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST identification: status %d", resp.StatusCode)
	}

	resp, err = client.Post(flowURL, "application/json",
		strings.NewReader(fmt.Sprintf(`{"password":%q}`, adminPassword)))
	if err != nil {
		t.Fatalf("POST password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Authentik login as admin failed: status %d: %s", resp.StatusCode, body)
	}

	var flowResp struct {
		Type      string `json:"type"`
		To        string `json:"to"`
		Component string `json:"component"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&flowResp); err != nil {
		t.Fatal(err)
	}
	// A successful final stage redirects to the dashboard. (Older authentik
	// versions answered with type="redirect"; 2025.10.x answers with
	// component="xak-flow-redirect" and to="/".)
	if flowResp.Type != "redirect" && (flowResp.Component != "xak-flow-redirect" || flowResp.To != "/") {
		t.Errorf("expected redirect to dashboard after successful login, got type=%q component=%q to=%q", flowResp.Type, flowResp.Component, flowResp.To)
	}
	t.Logf("Authentik login as admin successful (redirect to %s)", flowResp.To)
}

// TestLDAPAuth_ServiceAccountCanBind is the key behavioral test for the LDAP
// token flow: the outpost accepts connections and the service account binds.
// In the product path the outpost container gets its real token via the
// shared template-var map during graph reconciliation — no container restart
// or env rewriting.
func TestLDAPAuth_ServiceAccountCanBind(t *testing.T) {
	ldapBindPassword := readSecrets(t).LdapBindPassword
	if ldapBindPassword == "" {
		t.Fatal("ldapBindPassword not found in secrets.json")
	}

	out := runCmd(t, "ldapsearch",
		"-x",
		"-H", ldapURL,
		"-D", expectedLDAPBindUser,
		"-w", ldapBindPassword,
		"-b", expectedLDAPBaseDN,
		"-s", "base",
		"(objectClass=*)",
	)
	if !strings.Contains(out, expectedLDAPBaseDN) {
		t.Errorf("LDAP search result does not contain base DN %q:\n%s", expectedLDAPBaseDN, out)
	}
	t.Log("LDAP service account bind successful")
}

// TestLDAPAuth_AuthentikAdminCanBind verifies the LDAP outpost serves real
// user data by binding as the authentik admin user.
func TestLDAPAuth_AuthentikAdminCanBind(t *testing.T) {
	adminPassword := readSecrets(t).AuthentikBootstrapPassword
	if adminPassword == "" {
		t.Fatal("authentikBootstrapPassword not found in secrets.json")
	}

	out := runCmd(t, "ldapsearch",
		"-x",
		"-H", ldapURL,
		"-D", "cn=admin,ou=users,dc=ldap,dc=goauthentik,dc=io",
		"-w", adminPassword,
		"-b", "ou=users,dc=ldap,dc=goauthentik,dc=io",
		"-s", "one",
		"(cn=admin)",
		"cn",
	)
	if !strings.Contains(out, "cn=admin") {
		t.Errorf("LDAP search did not return admin user:\n%s", out)
	}
	t.Log("admin LDAP authentication successful")
}

// --- Live state streaming (install 202 + SSE) ---

type sseFrame struct {
	event string
	data  []byte
}

// readSSEFrames parses an SSE stream into {event, data} frames until the
// stream closes. Comment lines (the ": ping" heartbeat) are skipped.
func readSSEFrames(body io.Reader, frames chan<- sseFrame) {
	defer close(frames)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // snapshot lines can be large
	var event string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			frames <- sseFrame{event: event, data: []byte(strings.TrimPrefix(line, "data: "))}
			event = ""
		case line == "":
			event = ""
		}
	}
}

// TestInstallLiveStateStream verifies the live-state contract:
//
//	 1. The install 202 response carries the app record immediately (the
//	     orchestrator records it at submit time), so the UI can render the
//	     tile without polling.
//	 2. The /api/apps/events SSE stream delivers a snapshot before any node
//	     event, then node/pull updates, ending with the app node RUNNING.
//
// It must run before TestJellyfinInstallViaAPI (source order) so the install
// it drives is the fresh one.
func TestInstallLiveStateStream(t *testing.T) {
	// Open the SSE stream before submitting so no event is missed.
	sseResp, err := http.Get(hostAgentURL + "/api/apps/events")
	if err != nil {
		t.Fatalf("GET /api/apps/events: %v", err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/apps/events: status %d", sseResp.StatusCode)
	}
	frames := make(chan sseFrame, 512)
	go readSSEFrames(sseResp.Body, frames)

	// POST install and inspect the 202 body.
	installResp, err := http.Post(hostAgentURL+"/api/apps/jellyfin/install", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST install: %v", err)
	}
	defer installResp.Body.Close()
	if installResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(installResp.Body)
		t.Fatalf("POST install: status %d: %s", installResp.StatusCode, body)
	}
	var install struct {
		IntentID string        `json:"intentId"`
		App      *installedApp `json:"app"`
	}
	if err := json.NewDecoder(installResp.Body).Decode(&install); err != nil {
		t.Fatalf("decode install response: %v", err)
	}
	if install.App == nil {
		t.Fatal("install 202 response must carry the app record (live-state contract)")
	}
	if install.App.Status != "installing" {
		t.Fatalf("install 202 app record status = %q, want \"installing\"", install.App.Status)
	}

	// Consume frames until the jellyfin node is RUNNING. Fresh VMs may pull
	// the image, so the budget matches TestJellyfinInstallViaAPI.
	sawSnapshot, snapshotBeforeNode, sawNode, sawPull, nodeRunning := false, true, false, false, false
	timeout := time.After(10 * time.Minute)
	for !nodeRunning {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("event stream closed before the app reached RUNNING")
			}
			switch f.event {
			case "snapshot":
				sawSnapshot = true
			case "node":
				// Node events carry the eventbus.NodeInfo projection
				// {app, container, phase}, not the raw graph node.
				var n struct {
					App       string `json:"app"`
					Container string `json:"container"`
					Phase     string `json:"phase"`
				}
				if err := json.Unmarshal(f.data, &n); err == nil && (n.Container == "apps-jellyfin" || n.App == "jellyfin") {
					sawNode = true
					if !sawSnapshot {
						snapshotBeforeNode = false
					}
					if n.Phase == "running" {
						nodeRunning = true
					}
				}
			case "pull":
				var p struct {
					App string `json:"app"`
				}
				if json.Unmarshal(f.data, &p) == nil && p.App == "jellyfin" {
					sawPull = true
				}
			}
		case <-timeout:
			t.Fatalf("timed out waiting for apps-jellyfin RUNNING on the event stream (sawSnapshot=%v sawNode=%v sawPull=%v)", sawSnapshot, sawNode, sawPull)
		}
	}

	if !sawSnapshot {
		t.Error("expected a snapshot event on the stream")
	}
	if !snapshotBeforeNode {
		t.Error("snapshot must be delivered before any node event")
	}
	if !sawNode {
		t.Error("expected node events for apps-jellyfin on the stream")
	}
	t.Logf("live-state stream: snapshot=%v node=%v pull=%v (pull absent if the image was already local)", sawSnapshot, sawNode, sawPull)
}

// --- Jellyfin through the real install path ---

// TestJellyfinInstallViaAPI installs Jellyfin through the host-agent API and
// waits for the orchestrator to converge it to running: intent queue,
// dependency graph, container creation, PreStart (LDAP plugin), PostStart
// (wizard, libraries, LDAP config), route generation.
func TestJellyfinInstallViaAPI(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/jellyfin/install", `{}`, http.StatusAccepted)
	// Fresh VMs may need to pull the Jellyfin image; allow generous time.
	waitAppRunning(t, "jellyfin", 10*time.Minute)
	waitHTTPOrFatal(t, 60*time.Second, jellyfinURL+"/health")
}

// TestJellyfinConfiguredByConfigurator verifies the PostStart outcomes
// behaviorally through the Jellyfin API: wizard completed, libraries created,
// LDAP plugin configured with the typed LDAPOutput values.
func TestJellyfinConfiguredByConfigurator(t *testing.T) {
	waitAppRunning(t, "jellyfin", 2*time.Minute)

	info := getJellyfinSystemInfo(t)
	if !info.StartupWizardCompleted {
		t.Error("StartupWizardCompleted should be true after install")
	}

	token := authenticateJellyfin(t)
	folders := getVirtualFolders(t, token)
	foundMovies, foundShows := false, false
	for _, f := range folders {
		switch f.Name {
		case "Movies":
			foundMovies = true
		case "Shows":
			foundShows = true
		}
	}
	if !foundMovies {
		t.Error("Movies library not found")
	}
	if !foundShows {
		t.Error("Shows library not found")
	}

	ldapConfig := getLDAPPluginConfig(t, token)
	wantHost := expectedLDAPHost()
	if ldapConfig.LdapServer != wantHost {
		t.Errorf("LdapServer = %q, want %q", ldapConfig.LdapServer, wantHost)
	}
	if ldapConfig.LdapPort != expectedLDAPPort {
		t.Errorf("LdapPort = %d, want %d", ldapConfig.LdapPort, expectedLDAPPort)
	}
	if ldapConfig.LdapBaseDn != expectedLDAPBaseDN {
		t.Errorf("LdapBaseDn = %q, want %q", ldapConfig.LdapBaseDn, expectedLDAPBaseDN)
	}
	if ldapConfig.LdapBindUser != expectedLDAPBindUser {
		t.Errorf("LdapBindUser = %q, want %q", ldapConfig.LdapBindUser, expectedLDAPBindUser)
	}
	if ldapConfig.LdapBindPassword == "" {
		t.Error("LdapBindPassword should not be empty")
	}
	// These exact values caught real bugs:
	// - LdapUidAttribute must be "sAMAccountName" (not "uid") for Authentik LDAP
	// - LdapAdminFilter must use memberOf (not memberUid) for admin detection
	if ldapConfig.LdapUidAttribute != "sAMAccountName" {
		t.Errorf("LdapUidAttribute = %q, want %q", ldapConfig.LdapUidAttribute, "sAMAccountName")
	}
	if ldapConfig.LdapUsernameAttribute != "cn" {
		t.Errorf("LdapUsernameAttribute = %q, want %q", ldapConfig.LdapUsernameAttribute, "cn")
	}
	if ldapConfig.LdapSearchFilter != "(objectClass=user)" {
		t.Errorf("LdapSearchFilter = %q, want %q", ldapConfig.LdapSearchFilter, "(objectClass=user)")
	}
	wantAdminFilter := fmt.Sprintf("(memberOf=cn=authentik Admins,ou=groups,%s)", expectedLDAPBaseDN)
	if ldapConfig.LdapAdminFilter != wantAdminFilter {
		t.Errorf("LdapAdminFilter = %q, want %q", ldapConfig.LdapAdminFilter, wantAdminFilter)
	}
}

// TestJellyfinLDAPLogin exercises the full LDAP auth chain:
// Jellyfin → LDAP bind (sAMAccountName lookup) → Authentik LDAP outpost.
func TestJellyfinLDAPLogin(t *testing.T) {
	waitAppRunning(t, "jellyfin", 2*time.Minute)
	jellyfinLDAPLoginAs(t, "admin", readSecrets(t).AuthentikBootstrapPassword)
}

// jellyfinLDAPLoginAs authenticates to Jellyfin over LDAP and asserts the
// admin role is applied via LdapAdminFilter.
func jellyfinLDAPLoginAs(t *testing.T, username, password string) {
	t.Helper()
	if password == "" {
		t.Fatalf("no password available for LDAP login as %s", username)
	}

	body := fmt.Sprintf(`{"Username":%q,"Pw":%q}`, username, password)
	req, err := http.NewRequestWithContext(context.Background(), "POST",
		jellyfinURL+"/Users/AuthenticateByName", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", `MediaBrowser Client="Bloud-E2E", Device="Test", DeviceId="e2e-test", Version="1.0.0"`)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /Users/AuthenticateByName as %s: %v", username, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("Jellyfin LDAP login as %s failed: status %d: %s", username, resp.StatusCode, respBody)
	}

	var authResp struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			Policy struct {
				IsAdministrator bool `json:"IsAdministrator"`
			} `json:"Policy"`
		} `json:"User"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		t.Fatal(err)
	}
	if authResp.AccessToken == "" {
		t.Error("AccessToken should not be empty after LDAP login")
	}
	if !authResp.User.Policy.IsAdministrator {
		t.Errorf("%s should have IsAdministrator=true (LdapAdminFilter may be wrong)", username)
	}
	t.Logf("Jellyfin LDAP login as %s successful", username)
}

// --- Crash recovery through startup convergence ---

// TestCrashRecoveryViaReconcile simulates a container crash, then restarts
// only the host-agent process. Recovery must come from the host-agent's
// startup convergence (graph reconciliation + idempotent PreStart/PostStart),
// not from the supervisor or any direct container manipulation.
func TestCrashRecoveryViaReconcile(t *testing.T) {
	unit := os.Getenv("BLOUD_E2E_HOST_AGENT_UNIT")
	if unit == "" {
		t.Skip("BLOUD_E2E_HOST_AGENT_UNIT not set; skipping crash recovery test")
	}
	waitAppRunning(t, "jellyfin", 2*time.Minute)

	// Fault injection: simulate the Jellyfin container crashing.
	out, err := exec.Command("podman", "stop", "apps-jellyfin").CombinedOutput()
	if err != nil {
		t.Fatalf("podman stop apps-jellyfin (fault injection): %v\n%s", err, out)
	}
	t.Log("fault injected: apps-jellyfin stopped")

	// Restart only the host-agent process. The supervisor brings the process
	// back; the host-agent's startup convergence brings the container back.
	out, err = exec.Command("systemctl", "--user", "restart", unit).CombinedOutput()
	if err != nil {
		t.Fatalf("systemctl --user restart %s: %v\n%s", unit, err, out)
	}
	waitHTTPOrFatal(t, 120*time.Second, hostAgentURL+"/api/health")

	// The host-agent must recover the container through the graph.
	waitAppRunning(t, "jellyfin", 5*time.Minute)
	running := runCmd(t, "podman", "inspect", "-f", `{{.State.Running}}`, "apps-jellyfin")
	if running != "true" {
		t.Fatalf("apps-jellyfin not running after recovery (State.Running=%s)", running)
	}
	waitHTTPOrFatal(t, 60*time.Second, jellyfinURL+"/health")

	// PostStart re-ran during recovery and must have been idempotent:
	// wizard still complete, SSO chain still intact.
	info := getJellyfinSystemInfo(t)
	if !info.StartupWizardCompleted {
		t.Error("StartupWizardCompleted should still be true after recovery")
	}
	jellyfinLDAPLoginAs(t, "admin", readSecrets(t).AuthentikBootstrapPassword)
	t.Log("crash recovery complete: host-agent reconciled the container back")
}

// --- Uninstall through the real path ---

// TestJellyfinUninstallCleanup uninstalls Jellyfin through the API and asserts
// the full cleanup: store entry, container, data directory, and routes.
func TestJellyfinUninstallCleanup(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/jellyfin/uninstall",
		`{"clearData":true}`, http.StatusAccepted)

	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if appStatus(t, "jellyfin") == "" {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if status := appStatus(t, "jellyfin"); status != "" {
		t.Fatalf("jellyfin still listed as installed (status %q)", status)
	}

	// Container must be gone (the orchestrator removes it, not the harness).
	if _, err := exec.Command("podman", "container", "exists", "apps-jellyfin").CombinedOutput(); err == nil {
		t.Fatal("apps-jellyfin container still exists after uninstall")
	}

	// clearData must remove the app's data directory (asserted when the
	// deployer provides the data directory location).
	if os.Getenv("BLOUD_DATA_DIR") != "" {
		dataPath := filepath.Join(dataDir(), "jellyfin")
		if _, err := os.Stat(dataPath); err == nil {
			t.Errorf("data directory %s still exists after clearData uninstall", dataPath)
		}
	}

	// Routes must be regenerated without Jellyfin.
	traefikDir := os.Getenv("BLOUD_TRAEFIK_DYNAMIC_DIR")
	if traefikDir != "" {
		routesPath := filepath.Join(traefikDir, "apps-routes.yml")
		routes, err := os.ReadFile(routesPath)
		if err != nil {
			t.Fatalf("reading %s: %v", routesPath, err)
		}
		if strings.Contains(string(routes), "jellyfin") {
			t.Errorf("apps-routes.yml still references jellyfin after uninstall")
		}
	}
	t.Log("jellyfin fully uninstalled: store, container, data, and routes cleaned up")
}

// --- AFFiNE (native-oidc, own postgres + redis) ---

var affineURL = getEnvDefault("BLOUD_E2E_AFFINE_URL", "http://localhost:3010")

// TestAffineInstallViaAPI installs AFFiNE through the API. The graph spans
// three containers (postgres, redis, server); the server node chains the
// one-shot migration job into its startup command, so first boot runs
// prisma migrations before the HTTP listener opens.
func TestAffineInstallViaAPI(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/affine/install", `{}`, http.StatusAccepted)
	// Fresh VMs pull a large image and run migrations on first boot; allow
	// a generous deadline.
	waitAppRunning(t, "affine", 20*time.Minute)
	waitHTTPOrFatal(t, 60*time.Second, affineURL+"/info")
}

// TestAffineConfiguredByConfigurator verifies the configurator's outcomes
// behaviorally through the app's own API: the server reports its version,
// the first-run owner account exists (the setup endpoint refuses a second
// first-user call), and the OIDC preflight returns an authorization URL
// whose redirect_uri is the app's public callback.
func TestAffineConfiguredByConfigurator(t *testing.T) {
	waitAppRunning(t, "affine", 2*time.Minute)

	info := getAffineInfo(t)
	if info.Compatibility == "" {
		t.Error("affine /info should report a compatibility version")
	}

	// The owner account created in PostStart makes the setup endpoint
	// reject further first-user calls with 403.
	resp, err := http.Post(affineURL+"/api/setup/create-admin-user",
		"application/json",
		strings.NewReader(`{"name":"probe","email":"probe@affine.localhost","password":"probe-password-123"}`))
	if err != nil {
		t.Fatalf("POST /api/setup/create-admin-user: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "First user already created") {
		t.Errorf("setup endpoint = status %d body %q, want 403 containing %q",
			resp.StatusCode, string(body), "First user already created")
	}

	// OIDC preflight: issuer discovery + PKCE are live, and the redirect
	// URI is the app's public callback (same derivation as the route).
	data := affineOAuthPreflight(t)
	if !strings.Contains(data, `"url"`) {
		t.Errorf("preflight response should contain an authorization url: %s", data)
	}
	wantRedirect := url.QueryEscape(expectedAffineCallbackURL())
	if !strings.Contains(data, wantRedirect) {
		t.Errorf("preflight url should carry redirect_uri %q, got: %s", expectedAffineCallbackURL(), data)
	}
}

// TestAffineUninstallCleanup uninstalls AFFiNE through the API and asserts
// the full cleanup: store entry, all three containers, data directory, and
// routes.
func TestAffineUninstallCleanup(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/affine/uninstall",
		`{"clearData":true}`, http.StatusAccepted)

	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if appStatus(t, "affine") == "" {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if status := appStatus(t, "affine"); status != "" {
		t.Fatalf("affine still listed as installed (status %q)", status)
	}

	// All graph nodes must be gone (the orchestrator removes them).
	for _, name := range []string{"apps-affine", "apps-affine-postgres", "apps-affine-redis"} {
		if _, err := exec.Command("podman", "container", "exists", name).CombinedOutput(); err == nil {
			t.Errorf("%s container still exists after uninstall", name)
		}
	}

	if os.Getenv("BLOUD_DATA_DIR") != "" {
		dataPath := filepath.Join(dataDir(), "affine")
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
		if strings.Contains(string(routes), "affine") {
			t.Errorf("apps-routes.yml still references affine after uninstall")
		}
	}
	t.Log("affine fully uninstalled: store, containers, data, and routes cleaned up")
}

// expectedAffineCallbackURL mirrors apps/affine's configurator derivation:
// the Bloud base URL with the app subdomain, plus the app's callback path.
func expectedAffineCallbackURL() string {
	base := os.Getenv("BLOUD_SSO_BASE_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return "http://affine.localhost:8080/oauth/callback"
	}
	parsed.Host = "affine." + parsed.Host
	parsed.Path = "/oauth/callback"
	parsed.RawQuery = ""
	parsed.RawPath = ""
	parsed.User = nil
	return strings.TrimSuffix(parsed.String(), "/")
}

type affineInfo struct {
	Compatibility string `json:"compatibility"`
	Message       string `json:"message"`
	Type          string `json:"type"`
}

func getAffineInfo(t *testing.T) affineInfo {
	t.Helper()
	resp, err := http.Get(affineURL + "/info")
	if err != nil {
		t.Fatalf("GET /info: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /info: status %d: %s", resp.StatusCode, body)
	}
	var info affineInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decoding /info: %v", err)
	}
	return info
}

func affineOAuthPreflight(t *testing.T) string {
	t.Helper()
	resp, err := http.Post(affineURL+"/api/oauth/preflight", "application/json",
		strings.NewReader(`{"provider":"OIDC","client":"web","client_nonce":"bloud-e2e"}`))
	if err != nil {
		t.Fatalf("POST /api/oauth/preflight: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/oauth/preflight: status %d: %s", resp.StatusCode, body)
	}
	return string(body)
}

// --- Jellyfin API helpers ---

type jellyfinPublicInfo struct {
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
}

type virtualFolder struct {
	Name           string   `json:"Name"`
	Locations      []string `json:"Locations"`
	CollectionType string   `json:"CollectionType"`
}

type ldapPluginConfig struct {
	LdapServer            string `json:"LdapServer"`
	LdapPort              int    `json:"LdapPort"`
	LdapBaseDn            string `json:"LdapBaseDn"`
	LdapBindUser          string `json:"LdapBindUser"`
	LdapBindPassword      string `json:"LdapBindPassword"`
	LdapUidAttribute      string `json:"LdapUidAttribute"`
	LdapUsernameAttribute string `json:"LdapUsernameAttribute"`
	LdapSearchFilter      string `json:"LdapSearchFilter"`
	LdapAdminFilter       string `json:"LdapAdminFilter"`
}

func jellyfinAuthHeader(token string) string {
	return fmt.Sprintf(`MediaBrowser Client="Bloud-E2E", Device="Test", DeviceId="e2e-test", Version="1.0.0", Token="%s"`, token)
}

func getJellyfinSystemInfo(t *testing.T) jellyfinPublicInfo {
	t.Helper()
	resp, err := http.Get(jellyfinURL + "/System/Info/Public")
	if err != nil {
		t.Fatalf("GET /System/Info/Public: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /System/Info/Public: status %d: %s", resp.StatusCode, body)
	}
	var info jellyfinPublicInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	return info
}

// authenticateJellyfin logs in as the managed bootstrap admin and returns an
// access token.
func authenticateJellyfin(t *testing.T) string {
	t.Helper()
	body := fmt.Sprintf(`{"Username":%q,"Pw":%q}`, bootstrapUsername, bootstrapPassword)
	req, err := http.NewRequestWithContext(context.Background(), "POST",
		jellyfinURL+"/Users/AuthenticateByName", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", `MediaBrowser Client="Bloud-E2E", Device="Test", DeviceId="e2e-test", Version="1.0.0"`)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /Users/AuthenticateByName: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("Jellyfin authentication failed: status %d: %s", resp.StatusCode, respBody)
	}
	var authResp struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		t.Fatal(err)
	}
	if authResp.AccessToken == "" {
		t.Fatal("empty access token from Jellyfin authentication")
	}
	return authResp.AccessToken
}

func getVirtualFolders(t *testing.T, token string) []virtualFolder {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), "GET", jellyfinURL+"/Library/VirtualFolders", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", jellyfinAuthHeader(token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /Library/VirtualFolders: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /Library/VirtualFolders: status %d: %s", resp.StatusCode, body)
	}
	var folders []virtualFolder
	if err := json.NewDecoder(resp.Body).Decode(&folders); err != nil {
		t.Fatal(err)
	}
	return folders
}

func getLDAPPluginConfig(t *testing.T, token string) ldapPluginConfig {
	t.Helper()
	url := fmt.Sprintf("%s/Plugins/%s/Configuration", jellyfinURL, ldapPluginID)
	req, err := http.NewRequestWithContext(context.Background(), "GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", jellyfinAuthHeader(token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET LDAP plugin config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET LDAP plugin config: status %d: %s", resp.StatusCode, body)
	}
	var config ldapPluginConfig
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		t.Fatal(err)
	}
	return config
}
