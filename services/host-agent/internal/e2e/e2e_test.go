//go:build integration

// Package e2e contains integration tests that run against real services
// started by podman-compose (dev/compose.yml).
//
// These tests verify the complete configurator flow end-to-end:
//   - Jellyfin setup wizard completion
//   - Media library creation
//   - LDAP plugin configuration from typed LDAPOutput
//
// Run with:
//
//	go test -tags integration -timeout 300s ./internal/e2e/...
//
// Prerequisites:
//   - Lima VM "bloud-dev" running with compose stack up
//   - host-agent built at /tmp/host-agent
//   - dev/setup.sh has been executed
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
	"testing"
	"time"
)

// Service endpoints — accessible from inside the Lima VM via localhost
// because compose publishes ports to the host.
const (
	jellyfinURL  = "http://localhost:8096"
	authentikURL = "http://localhost:9000"
	postgresPort = "5432"
)

// LDAP expected values — must match buildLDAPOutput in configure.go.
const (
	expectedLDAPPort     = 3389
	expectedLDAPBaseDN   = "dc=ldap,dc=goauthentik,dc=io"
	expectedLDAPBindUser = "cn=ldap-service,ou=users,dc=ldap,dc=goauthentik,dc=io"
)

// expectedLDAPHost returns the LDAP host that the configurator should write.
// In compose this is "authentik-ldap"; in NixOS it's "apps-authentik-ldap".
func expectedLDAPHost() string {
	if h := os.Getenv("BLOUD_LDAP_HOST"); h != "" {
		return h
	}
	return "authentik-ldap"
}

// Jellyfin bootstrap admin — must match constants in apps/jellyfin/configurator.go
const (
	bootstrapUsername = "bloud-bootstrap-admin"
	bootstrapPassword = "bloud-bootstrap-password-change-me"
	ldapPluginID      = "958aad6637844d2ab89aa7b6fab6e25c"
)

func hostAgentBinary() string {
	if p := os.Getenv("HOST_AGENT_BIN"); p != "" {
		return p
	}
	return "/tmp/host-agent"
}

// hostAgentEnv returns the environment variables needed to run host-agent
// configure commands against the compose stack.
func hostAgentEnv() []string {
	dataDir := os.Getenv("BLOUD_DATA_DIR")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".local", "share", "bloud")
	}

	appsDir := os.Getenv("BLOUD_APPS_DIR")
	if appsDir == "" {
		home, _ := os.UserHomeDir()
		appsDir = filepath.Join(home, "bloud", "apps")
	}

	secretsFile := filepath.Join(dataDir, "secrets.json")
	pgPassword := "devpass"
	ldapBindPassword := ""
	if data, err := os.ReadFile(secretsFile); err == nil {
		var secrets map[string]interface{}
		if json.Unmarshal(data, &secrets) == nil {
			if pw, ok := secrets["postgresPassword"].(string); ok {
				pgPassword = pw
			}
			if pw, ok := secrets["ldapBindPassword"].(string); ok {
				ldapBindPassword = pw
			}
		}
	}

	dbURL := fmt.Sprintf("postgres://apps:%s@localhost:%s/bloud?sslmode=disable", pgPassword, postgresPort)

	ldapHost := os.Getenv("BLOUD_LDAP_HOST")
	if ldapHost == "" {
		ldapHost = "authentik-ldap" // compose network hostname
	}

	env := append(os.Environ(),
		"BLOUD_DATA_DIR="+dataDir,
		"BLOUD_APPS_DIR="+appsDir,
		"DATABASE_URL="+dbURL,
		"BLOUD_LDAP_BIND_PASSWORD="+ldapBindPassword,
		"BLOUD_LDAP_HOST="+ldapHost,
	)
	return env
}

// runHostAgent executes a host-agent configure command and returns its output.
func runHostAgent(t *testing.T, args ...string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, hostAgentBinary(), args...)
	cmd.Env = hostAgentEnv()

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("host-agent %s failed: %v\noutput:\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}

// --- Jellyfin Tests ---

func TestJellyfinHealthCheck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Poll /health until it responds 200
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", jellyfinURL+"/health", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}

		select {
		case <-ctx.Done():
			t.Fatal("Jellyfin health check timed out")
		case <-time.After(2 * time.Second):
		}
	}
}

func TestCatalogRefresh(t *testing.T) {
	// Seed the catalog database with app metadata from YAML files.
	// Must run before any configurator that depends on catalog lookups.
	runHostAgent(t, "configure", "catalog-refresh")
}

func TestJellyfinPostStart_CompletesWizardAndConfiguresLDAP(t *testing.T) {
	// Run the Jellyfin poststart configurator
	runHostAgent(t, "configure", "poststart", "jellyfin")

	ctx := context.Background()

	// Verify: Setup wizard completed
	info := getJellyfinPublicInfo(t, ctx)
	if !info.StartupWizardCompleted {
		t.Error("StartupWizardCompleted should be true after poststart")
	}

	// Verify: Libraries created
	token := authenticateJellyfin(t, ctx)
	folders := getVirtualFolders(t, ctx, token)

	foundMovies := false
	foundShows := false
	for _, f := range folders {
		if f.Name == "Movies" {
			foundMovies = true
		}
		if f.Name == "Shows" {
			foundShows = true
		}
	}
	if !foundMovies {
		t.Error("Movies library not found")
	}
	if !foundShows {
		t.Error("Shows library not found")
	}

	// Verify: LDAP plugin configured with typed output values
	ldapConfig := getLDAPPluginConfig(t, ctx, token)

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
}

func TestJellyfinPostStart_IsIdempotent(t *testing.T) {
	// Run poststart a second time — should succeed without errors
	runHostAgent(t, "configure", "poststart", "jellyfin")

	ctx := context.Background()
	info := getJellyfinPublicInfo(t, ctx)
	if !info.StartupWizardCompleted {
		t.Error("StartupWizardCompleted should still be true after second poststart")
	}
}

// --- Helper types and functions ---

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
	LdapServer       string `json:"LdapServer"`
	LdapPort         int    `json:"LdapPort"`
	LdapBaseDn       string `json:"LdapBaseDn"`
	LdapBindUser     string `json:"LdapBindUser"`
	LdapBindPassword string `json:"LdapBindPassword"`
}

func jellyfinAuthHeader(token string) string {
	return fmt.Sprintf(`MediaBrowser Client="Bloud-E2E", Device="Test", DeviceId="e2e-test", Version="1.0.0", Token="%s"`, token)
}

func getJellyfinPublicInfo(t *testing.T, ctx context.Context) jellyfinPublicInfo {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", jellyfinURL+"/System/Info/Public", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
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

func authenticateJellyfin(t *testing.T, ctx context.Context) string {
	t.Helper()

	body := fmt.Sprintf(`{"Username":"%s","Pw":"%s"}`, bootstrapUsername, bootstrapPassword)
	req, err := http.NewRequestWithContext(ctx, "POST", jellyfinURL+"/Users/AuthenticateByName", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Authorization", `MediaBrowser Client="Bloud-E2E", Device="Test", DeviceId="e2e-test", Version="1.0.0"`)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /Users/AuthenticateByName: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("Authentication failed: status %d: %s", resp.StatusCode, respBody)
	}

	var authResp struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		t.Fatal(err)
	}
	return authResp.AccessToken
}

func getVirtualFolders(t *testing.T, ctx context.Context, token string) []virtualFolder {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, "GET", jellyfinURL+"/Library/VirtualFolders", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Emby-Authorization", jellyfinAuthHeader(token))

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

func getLDAPPluginConfig(t *testing.T, ctx context.Context, token string) ldapPluginConfig {
	t.Helper()

	url := fmt.Sprintf("%s/Plugins/%s/Configuration", jellyfinURL, ldapPluginID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Emby-Authorization", jellyfinAuthHeader(token))

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
