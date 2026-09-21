// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"encoding/json"
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

var affineURL = getEnvDefault("BLOUD_E2E_AFFINE_URL", "http://localhost:3010")

// TestAffineInstallViaAPI installs AFFiNE through the API. The graph spans
// three containers (postgres, redis, server); the server node chains the
// one-shot migration job into its startup command, so first boot runs
// prisma migrations before the HTTP listener opens.

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
