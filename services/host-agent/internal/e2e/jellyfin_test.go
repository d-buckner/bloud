// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

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

// TestJellyfinLDAPLogin exercises the full LDAP auth chain:
// Jellyfin → LDAP bind (sAMAccountName lookup) → Authentik LDAP outpost.
func TestJellyfinLDAPLogin(t *testing.T) {
	waitAppRunning(t, "jellyfin", 2*time.Minute)
	jellyfinLDAPLoginAs(t, "admin", readSecrets(t).AuthentikBootstrapPassword)
}

// jellyfinLDAPLoginAs authenticates to Jellyfin over LDAP and asserts the
// admin role is applied via LdapAdminFilter.

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

// authenticateJellyfin logs in as the managed bootstrap admin and returns an
// access token.
func authenticateJellyfin(t *testing.T) string {
	t.Helper()
	pw := readSecrets(t).AppSecrets["jellyfin"].AdminPassword
	if pw == "" {
		t.Fatal("no Jellyfin admin password in secrets.json (appSecrets.jellyfin.adminPassword)")
	}
	body := fmt.Sprintf(`{"Username":%q,"Pw":%q}`, bootstrapUsername, pw)
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
