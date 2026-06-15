//go:build integration

// Package e2e contains integration tests that run against real services
// started by podman-compose (dev/compose.yml).
//
// These tests verify the complete configurator flow end-to-end:
//   - Authentik LDAP infrastructure creation
//   - LDAP outpost token extraction and outpost restart
//   - Jellyfin setup wizard completion
//   - Media library creation
//   - LDAP plugin configuration from typed LDAPOutput
//   - Actual LDAP authentication (behavioral verification)
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
// Compose containers use deterministic names matching production (apps-authentik-ldap).
func expectedLDAPHost() string {
	if h := os.Getenv("BLOUD_LDAP_HOST"); h != "" {
		return h
	}
	return "apps-authentik-ldap"
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

// readSecrets reads secrets.json and returns the parsed map.
func readSecrets() map[string]interface{} {
	dataDir := os.Getenv("BLOUD_DATA_DIR")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".local", "share", "bloud")
	}
	secretsFile := filepath.Join(dataDir, "secrets.json")
	data, err := os.ReadFile(secretsFile)
	if err != nil {
		return nil
	}
	var secrets map[string]interface{}
	json.Unmarshal(data, &secrets)
	return secrets
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

	secrets := readSecrets()
	pgPassword := "devpass"
	ldapBindPassword := ""
	authentikBootstrapToken := ""
	authentikBootstrapPassword := ""
	if secrets != nil {
		if pw, ok := secrets["postgresPassword"].(string); ok {
			pgPassword = pw
		}
		if pw, ok := secrets["ldapBindPassword"].(string); ok {
			ldapBindPassword = pw
		}
		if tok, ok := secrets["authentikBootstrapToken"].(string); ok {
			authentikBootstrapToken = tok
		}
		if pw, ok := secrets["authentikBootstrapPassword"].(string); ok {
			authentikBootstrapPassword = pw
		}
	}

	dbURL := fmt.Sprintf("postgres://apps:%s@localhost:%s/bloud?sslmode=disable", pgPassword, postgresPort)

	ldapHost := os.Getenv("BLOUD_LDAP_HOST")
	if ldapHost == "" {
		ldapHost = "apps-authentik-ldap" // deterministic container name matching production
	}

	env := append(os.Environ(),
		"BLOUD_DATA_DIR="+dataDir,
		"BLOUD_APPS_DIR="+appsDir,
		"DATABASE_URL="+dbURL,
		"BLOUD_LDAP_BIND_PASSWORD="+ldapBindPassword,
		"BLOUD_LDAP_HOST="+ldapHost,
		"BLOUD_AUTHENTIK_PORT=9000",
	)

	// Pass Authentik credentials if available from secrets.json.
	// These are needed by the Authentik configurator.
	if authentikBootstrapToken != "" {
		env = append(env, "BLOUD_AUTHENTIK_TOKEN="+authentikBootstrapToken)
	}
	if authentikBootstrapPassword != "" {
		env = append(env, "BLOUD_AUTHENTIK_ADMIN_PASSWORD="+authentikBootstrapPassword)
	}

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

// --- Authentik Tests ---

func TestAuthentikHealthCheck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	for {
		req, err := http.NewRequestWithContext(ctx, "GET", authentikURL+"/-/health/ready/", nil)
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
			t.Fatal("Authentik health check timed out")
		case <-time.After(3 * time.Second):
		}
	}
}

func TestAuthentikPostStart_CreatesLDAPInfrastructure(t *testing.T) {
	// Run the Authentik poststart configurator — this creates:
	// - API service account + token
	// - LDAP provider, application, outpost, service account
	runHostAgent(t, "configure", "poststart", "authentik")

	// Verify: LDAP outpost exists via API
	secrets := readSecrets()
	token, ok := secrets["authentikBootstrapToken"].(string)
	if !ok || token == "" {
		t.Fatal("authentikBootstrapToken not found in secrets.json")
	}

	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, "GET", authentikURL+"/api/v3/outposts/instances/?search=LDAP", nil)
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
		t.Fatal("No LDAP outpost found after poststart")
	}
	t.Logf("LDAP outpost created: %s (pk=%s)", outpostResp.Results[0].Name, outpostResp.Results[0].PK)
}

func TestLDAPOutpost_RestartWithToken(t *testing.T) {
	// Extract the LDAP outpost token from Authentik and restart the
	// outpost container with the real token so it can connect.
	secrets := readSecrets()
	token, ok := secrets["authentikBootstrapToken"].(string)
	if !ok || token == "" {
		t.Fatal("authentikBootstrapToken not found in secrets.json")
	}

	ctx := context.Background()

	// Find the outpost to get its PK
	req, _ := http.NewRequestWithContext(ctx, "GET", authentikURL+"/api/v3/outposts/instances/?search=LDAP", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET outposts: %v", err)
	}
	defer resp.Body.Close()

	var outpostResp struct {
		Results []struct {
			PK string `json:"pk"`
		} `json:"results"`
	}
	json.NewDecoder(resp.Body).Decode(&outpostResp)
	if len(outpostResp.Results) == 0 {
		t.Fatal("No LDAP outpost found")
	}
	outpostPK := outpostResp.Results[0].PK

	// Get the outpost token using the view_key endpoint
	tokenID := fmt.Sprintf("ak-outpost-%s-api", outpostPK)
	viewKeyURL := fmt.Sprintf("%s/api/v3/core/tokens/%s/view_key/", authentikURL, tokenID)
	req2, _ := http.NewRequestWithContext(ctx, "GET", viewKeyURL, nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("GET token view_key: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("GET token view_key: status %d: %s", resp2.StatusCode, body)
	}

	var tokenResp struct {
		Key string `json:"key"`
	}
	json.NewDecoder(resp2.Body).Decode(&tokenResp)
	if tokenResp.Key == "" {
		t.Fatal("LDAP outpost token is empty")
	}
	t.Logf("Extracted LDAP outpost token: %s...", tokenResp.Key[:8])

	// Stop the LDAP outpost, update its token, and restart it
	stopCmd := exec.Command("podman", "stop", "apps-authentik-ldap")
	if out, err := stopCmd.CombinedOutput(); err != nil {
		t.Logf("podman stop output: %s", out)
		// Don't fatal — container might not be running
	}

	rmCmd := exec.Command("podman", "rm", "-f", "apps-authentik-ldap")
	if out, err := rmCmd.CombinedOutput(); err != nil {
		t.Logf("podman rm output: %s", out)
	}

	// Restart via compose with the real token
	dataDir := os.Getenv("BLOUD_DATA_DIR")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".local", "share", "bloud")
	}

	// Update the compose .env with the real token
	appsDir := os.Getenv("BLOUD_APPS_DIR")
	if appsDir == "" {
		home, _ := os.UserHomeDir()
		appsDir = filepath.Join(home, "bloud", "apps")
	}
	// The compose dir is two levels up from the apps dir
	composeDir := filepath.Join(filepath.Dir(appsDir), "dev")
	envFile := filepath.Join(composeDir, ".env")
	envData, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("reading .env: %v", err)
	}

	// Replace the placeholder token with the real one
	newEnv := strings.Replace(string(envData), "AUTHENTIK_LDAP_TOKEN=placeholder", "AUTHENTIK_LDAP_TOKEN="+tokenResp.Key, 1)
	// Also handle case where token was previously set
	lines := strings.Split(newEnv, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "AUTHENTIK_LDAP_TOKEN=") {
			lines[i] = "AUTHENTIK_LDAP_TOKEN=" + tokenResp.Key
		}
	}
	newEnv = strings.Join(lines, "\n")
	if err := os.WriteFile(envFile, []byte(newEnv), 0600); err != nil {
		t.Fatalf("writing .env: %v", err)
	}

	// Restart just the LDAP outpost via compose
	composeUp := exec.Command("podman-compose", "up", "-d", "authentik-ldap")
	composeUp.Dir = composeDir
	if out, err := composeUp.CombinedOutput(); err != nil {
		t.Fatalf("podman-compose up authentik-ldap failed: %v\n%s", err, out)
	}

	// Wait for the LDAP outpost to connect (check logs for successful config fetch)
	t.Log("Waiting for LDAP outpost to connect...")
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		logsCmd := exec.Command("podman", "logs", "apps-authentik-ldap")
		logsOut, _ := logsCmd.CombinedOutput()
		if strings.Contains(string(logsOut), "Starting LDAP server") {
			t.Log("LDAP outpost connected and started")
			return
		}
		time.Sleep(3 * time.Second)
	}

	// If we get here, print the last logs for debugging
	logsCmd := exec.Command("podman", "logs", "--tail", "20", "apps-authentik-ldap")
	logsOut, _ := logsCmd.CombinedOutput()
	t.Fatalf("LDAP outpost did not start within 60s. Last logs:\n%s", logsOut)
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

// --- LDAP Behavioral Verification ---

func TestLDAPAuth_ServiceAccountCanBind(t *testing.T) {
	// This is the key behavioral test: verify that the LDAP outpost
	// actually accepts connections and the service account can bind.
	// This proves the full chain works, not just that config was written.
	secrets := readSecrets()
	ldapBindPassword, ok := secrets["ldapBindPassword"].(string)
	if !ok || ldapBindPassword == "" {
		t.Fatal("ldapBindPassword not found in secrets.json")
	}

	// Use ldapsearch to perform an actual LDAP bind
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ldapsearch",
		"-x",
		"-H", "ldap://localhost:3389",
		"-D", expectedLDAPBindUser,
		"-w", ldapBindPassword,
		"-b", expectedLDAPBaseDN,
		"-s", "base",
		"(objectClass=*)",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("LDAP bind failed: %v\noutput:\n%s", err, string(out))
	}

	// Verify we got a result (the base DN entry)
	if !strings.Contains(string(out), expectedLDAPBaseDN) {
		t.Errorf("LDAP search result does not contain base DN %q:\n%s", expectedLDAPBaseDN, out)
	}
	t.Logf("LDAP bind successful, got base DN entry")
}

func TestLDAPAuth_AuthentikAdminCanBind(t *testing.T) {
	// Verify the Authentik admin user (akadmin) can authenticate via LDAP.
	// This proves LDAP is serving real user data, not just the base DN.
	secrets := readSecrets()
	adminPassword, ok := secrets["authentikBootstrapPassword"].(string)
	if !ok || adminPassword == "" {
		t.Fatal("authentikBootstrapPassword not found in secrets.json")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Bind as akadmin — the DN follows Authentik's LDAP schema
	cmd := exec.CommandContext(ctx, "ldapsearch",
		"-x",
		"-H", "ldap://localhost:3389",
		"-D", "cn=akadmin,ou=users,dc=ldap,dc=goauthentik,dc=io",
		"-w", adminPassword,
		"-b", "ou=users,dc=ldap,dc=goauthentik,dc=io",
		"-s", "one",
		"(cn=akadmin)",
		"cn",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("LDAP bind as akadmin failed: %v\noutput:\n%s", err, string(out))
	}

	if !strings.Contains(string(out), "akadmin") {
		t.Errorf("LDAP search did not return akadmin user:\n%s", out)
	}
	t.Logf("akadmin LDAP authentication successful")
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
