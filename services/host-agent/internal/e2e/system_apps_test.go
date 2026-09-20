// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"
)

// TestSystemAppsConverged verifies the bootstrap contract: system apps are
// auto-installed by the orchestrator and their containers are up, running,
// and carry the managed labels (architecture invariant 12).
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
// convergence: created by the authentik server PostStart as part of the
// normal lifecycle, with no manual configurator invocation.

// TestAuthentikLDAPOutpostCreated verifies that the LDAP outpost exists after
// convergence: created by the authentik server PostStart as part of the
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
// shared template-var map during graph reconciliation; no container restart
// or env rewriting.
