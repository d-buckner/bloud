package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	authentikClient "codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

const (
	// Bootstrap admin credentials - used to complete setup, then deleted after LDAP is configured
	bootstrapUsername = "bloud-bootstrap-admin"
	bootstrapPassword = "bloud-bootstrap-password-change-me"

	// LDAP plugin GUID - this is the standard ID for the Jellyfin LDAP-Auth plugin
	// Note: Jellyfin uses GUIDs without dashes in the API
	ldapPluginID = "958aad6637844d2ab89aa7b6fab6e25c"

	// Default LDAP configuration for Authentik
	// Use container name since Jellyfin and LDAP outpost are on the same network
	defaultLDAPHost     = "apps-authentik-ldap"
	defaultLDAPPort     = 3389
	defaultLDAPBaseDN   = "dc=ldap,dc=goauthentik,dc=io"
	defaultLDAPBindUser = "cn=ldap-service,ou=users,dc=ldap,dc=goauthentik,dc=io"
)

// Configurator handles Jellyfin configuration
type Configurator struct {
	Port           int
	baseURL        string // Override for testing; if empty, uses localhost:Port
	ldapHost       string
	ldapPort       int
	authentikURL   string // URL for Authentik API
	authentikToken string // API token for Authentik
}

// NewConfigurator creates a new Jellyfin configurator
func NewConfigurator(port int, authentikURL, authentikToken string) *Configurator {
	if port == 0 {
		port = 8096
	}
	return &Configurator{
		Port:           port,
		ldapHost:       defaultLDAPHost,
		ldapPort:       defaultLDAPPort,
		authentikURL:   authentikURL,
		authentikToken: authentikToken,
	}
}

// getBaseURL returns the base URL for API calls
func (c *Configurator) getBaseURL() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return fmt.Sprintf("http://localhost:%d", c.Port)
}

func (c *Configurator) Name() string {
	return "jellyfin"
}

// PreStart ensures directories exist.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	dirs := []string{
		filepath.Join(state.DataPath, "config"),
		filepath.Join(state.DataPath, "cache"),
		filepath.Join(state.BloudDataPath, "media", "movies"),
		filepath.Join(state.BloudDataPath, "media", "shows"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return nil
}

// HealthCheck waits for Jellyfin's API to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
	url := c.getBaseURL() + "/health"
	return configurator.WaitForHTTP(ctx, url, 60*time.Second)
}

// PostStart completes the Jellyfin setup wizard and configures LDAP
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// 1. Check if setup wizard is complete
	info, err := c.getSystemInfo(ctx)
	if err != nil {
		return fmt.Errorf("failed to get system info: %w", err)
	}

	if !info.StartupWizardCompleted {
		log.Println("Jellyfin: Completing setup wizard...")
		if err := c.completeStartupWizard(ctx); err != nil {
			return fmt.Errorf("failed to complete startup wizard: %w", err)
		}
		log.Println("Jellyfin: Setup wizard completed")
	}

	// 2. Configure media libraries
	if err := c.configureLibraries(ctx); err != nil {
		return fmt.Errorf("failed to configure libraries: %w", err)
	}

	// 3. Configure LDAP if SSO integration is enabled
	if _, hasSSO := state.Integrations["sso"]; hasSSO {
		if err := c.configureLDAP(ctx); err != nil {
			return fmt.Errorf("failed to configure LDAP: %w", err)
		}
	}

	return nil
}

// SystemInfo represents the /System/Info response
type SystemInfo struct {
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	ID                     string `json:"Id"`
}

// getSystemInfo fetches the system info from Jellyfin
func (c *Configurator) getSystemInfo(ctx context.Context) (*SystemInfo, error) {
	url := c.getBaseURL() + "/System/Info/Public"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var info SystemInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}

	return &info, nil
}

// waitForStartupWizardReady waits for Jellyfin's startup wizard API to be ready
// Even after /health returns OK, Jellyfin may still return 503 with HTML during initialization
func (c *Configurator) waitForStartupWizardReady(ctx context.Context) error {
	url := c.getBaseURL() + "/Startup/Configuration"

	for i := 0; i < 60; i++ { // Wait up to 60 seconds
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}

		// Check if we got a JSON response (API ready) vs HTML (still initializing)
		contentType := resp.Header.Get("Content-Type")
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK && (contentType == "application/json" || contentType == "application/json; charset=utf-8") {
			log.Println("Jellyfin: Startup wizard API is ready")
			return nil
		}

		log.Printf("Jellyfin: Waiting for startup wizard API (status=%d, content-type=%s)", resp.StatusCode, contentType)
		time.Sleep(time.Second)
	}

	return fmt.Errorf("startup wizard API not ready after 60 seconds")
}

// completeStartupWizard completes the Jellyfin initial setup wizard
func (c *Configurator) completeStartupWizard(ctx context.Context) error {
	// Wait for the startup wizard API to be ready
	// Jellyfin returns 503 with HTML while initializing, even if /health returns OK
	if err := c.waitForStartupWizardReady(ctx); err != nil {
		return fmt.Errorf("waiting for startup wizard: %w", err)
	}

	// Step 1: Set initial configuration
	if err := c.setStartupConfiguration(ctx); err != nil {
		return fmt.Errorf("setting startup configuration: %w", err)
	}

	// Step 2: Create the bootstrap admin user
	if err := c.setStartupUser(ctx, bootstrapUsername, bootstrapPassword); err != nil {
		return fmt.Errorf("creating startup user: %w", err)
	}

	// Step 3: Configure remote access
	if err := c.setRemoteAccess(ctx); err != nil {
		return fmt.Errorf("setting remote access: %w", err)
	}

	// Step 4: Mark wizard as complete
	if err := c.completeWizard(ctx); err != nil {
		return fmt.Errorf("completing wizard: %w", err)
	}

	return nil
}

// setStartupConfiguration sets the initial configuration
func (c *Configurator) setStartupConfiguration(ctx context.Context) error {
	url := c.getBaseURL() + "/Startup/Configuration"

	config := map[string]interface{}{
		"UICulture":                 "en-US",
		"MetadataCountryCode":       "US",
		"PreferredMetadataLanguage": "en",
	}

	body, _ := json.Marshal(config)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// setStartupUser creates the first admin user
// Jellyfin auto-creates an initial user internally, but it may take a moment to be ready.
// We first wait for GET /Startup/User to succeed, then POST to update it.
func (c *Configurator) setStartupUser(ctx context.Context, username, password string) error {
	url := c.getBaseURL() + "/Startup/User"

	// Wait for the initial user to be available (Jellyfin creates it asynchronously)
	var lastErr error
	for i := 0; i < 10; i++ {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			// Initial user is ready, proceed with update
			break
		}

		lastErr = fmt.Errorf("GET /Startup/User returned %d", resp.StatusCode)
		time.Sleep(500 * time.Millisecond)
	}

	if lastErr != nil {
		log.Printf("Jellyfin: Warning - initial user not ready after retries: %v", lastErr)
	}

	// Now update the user with our credentials
	user := map[string]string{
		"Name":     username,
		"Password": password,
	}

	body, _ := json.Marshal(user)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// setRemoteAccess configures remote access settings
func (c *Configurator) setRemoteAccess(ctx context.Context) error {
	url := c.getBaseURL() + "/Startup/RemoteAccess"

	config := map[string]bool{
		"EnableRemoteAccess":         true,
		"EnableAutomaticPortMapping": false,
	}

	body, _ := json.Marshal(config)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// completeWizard marks the startup wizard as complete
func (c *Configurator) completeWizard(ctx context.Context) error {
	url := c.getBaseURL() + "/Startup/Complete"

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// VirtualFolder represents a Jellyfin library
type VirtualFolder struct {
	Name           string   `json:"Name"`
	Locations      []string `json:"Locations"`
	CollectionType string   `json:"CollectionType"`
	ItemId         string   `json:"ItemId"`
}

// configureLibraries sets up the default media libraries
func (c *Configurator) configureLibraries(ctx context.Context) error {
	// Authenticate first
	token, err := c.authenticate(ctx, bootstrapUsername, bootstrapPassword)
	if err != nil {
		return fmt.Errorf("authenticating: %w", err)
	}

	// Get existing libraries
	existingLibraries, err := c.getVirtualFolders(ctx, token)
	if err != nil {
		return fmt.Errorf("getting libraries: %w", err)
	}

	// Create a map of existing library names
	existingNames := make(map[string]bool)
	for _, lib := range existingLibraries {
		existingNames[lib.Name] = true
	}

	// Define libraries to create
	libraries := []struct {
		name           string
		collectionType string
		path           string
	}{
		{"Movies", "movies", "/movies"},
		{"Shows", "shows", "/shows"},
	}

	for _, lib := range libraries {
		if existingNames[lib.name] {
			log.Printf("Jellyfin: Library '%s' already exists", lib.name)
			continue
		}

		log.Printf("Jellyfin: Creating library '%s' at %s", lib.name, lib.path)
		if err := c.addVirtualFolder(ctx, token, lib.name, lib.collectionType, lib.path); err != nil {
			return fmt.Errorf("creating library %s: %w", lib.name, err)
		}
	}

	return nil
}

// getVirtualFolders returns all configured libraries
func (c *Configurator) getVirtualFolders(ctx context.Context, token string) ([]VirtualFolder, error) {
	url := c.getBaseURL() + "/Library/VirtualFolders"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Emby-Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var folders []VirtualFolder
	if err := json.NewDecoder(resp.Body).Decode(&folders); err != nil {
		return nil, err
	}

	return folders, nil
}

// addVirtualFolder creates a new library
func (c *Configurator) addVirtualFolder(ctx context.Context, token, name, collectionType, path string) error {
	// The API uses query parameters for the folder metadata
	reqURL := fmt.Sprintf("%s/Library/VirtualFolders?name=%s&collectionType=%s&paths=%s&refreshLibrary=false",
		c.getBaseURL(), url.QueryEscape(name), url.QueryEscape(collectionType), url.QueryEscape(path))

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Emby-Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// LDAPConfig represents the LDAP plugin configuration
type LDAPConfig struct {
	LdapServer                     string   `json:"LdapServer"`
	LdapPort                       int      `json:"LdapPort"`
	UseSsl                         bool     `json:"UseSsl"`
	UseStartTls                    bool     `json:"UseStartTls"`
	SkipSslVerify                  bool     `json:"SkipSslVerify"`
	LdapBindUser                   string   `json:"LdapBindUser"`
	LdapBindPassword               string   `json:"LdapBindPassword"`
	LdapBaseDn                     string   `json:"LdapBaseDn"`
	LdapSearchFilter               string   `json:"LdapSearchFilter"`
	LdapAdminBaseDn                string   `json:"LdapAdminBaseDn"`
	LdapAdminFilter                string   `json:"LdapAdminFilter"`
	EnableLdapAdminFilterMemberUid bool     `json:"EnableLdapAdminFilterMemberUid"`
	LdapSearchAttributes           string   `json:"LdapSearchAttributes"`
	LdapClientCertPath             string   `json:"LdapClientCertPath"`
	LdapClientKeyPath              string   `json:"LdapClientKeyPath"`
	LdapRootCaPath                 string   `json:"LdapRootCaPath"`
	CreateUsersFromLdap            bool     `json:"CreateUsersFromLdap"`
	AllowPassChange                bool     `json:"AllowPassChange"`
	LdapUidAttribute               string   `json:"LdapUidAttribute"`
	LdapUsernameAttribute          string   `json:"LdapUsernameAttribute"`
	LdapPasswordAttribute          string   `json:"LdapPasswordAttribute"`
	EnableLdapProfileImageSync     bool     `json:"EnableLdapProfileImageSync"`
	RemoveImagesNotInLdap          bool     `json:"RemoveImagesNotInLdap"`
	LdapProfileImageAttribute      string   `json:"LdapProfileImageAttribute"`
	EnableAllFolders               bool     `json:"EnableAllFolders"`
	EnabledFolders                 []string `json:"EnabledFolders"`
	PasswordResetUrl               string   `json:"PasswordResetUrl"`
}

// configureLDAP configures the LDAP plugin to use Authentik
func (c *Configurator) configureLDAP(ctx context.Context) error {
	// First, authenticate to get an access token
	token, err := c.authenticate(ctx, bootstrapUsername, bootstrapPassword)
	if err != nil {
		return fmt.Errorf("authenticating: %w", err)
	}

	// Get current LDAP config to check if already configured
	currentConfig, err := c.getPluginConfiguration(ctx, token, ldapPluginID)
	if err != nil {
		log.Printf("Jellyfin: Could not get LDAP config (plugin may not be installed): %v", err)
		return nil // Plugin not installed, skip LDAP configuration
	}

	var config LDAPConfig
	if err := json.Unmarshal(currentConfig, &config); err != nil {
		return fmt.Errorf("parsing LDAP config: %w", err)
	}

	// Check if already configured for our LDAP server (idempotency)
	if config.LdapServer == c.ldapHost && config.LdapBindUser != "" {
		log.Println("Jellyfin: LDAP already configured")
		return nil
	}

	// Query Authentik for the actual LDAP service token key
	akClient := authentikClient.NewClient(c.authentikURL, c.authentikToken)
	ldapBindPassword, err := akClient.GetLDAPServiceTokenKey()
	if err != nil {
		return fmt.Errorf("getting LDAP service token key from Authentik: %w", err)
	}

	// Configure LDAP for Authentik
	log.Println("Jellyfin: Configuring LDAP...")
	newConfig := LDAPConfig{
		LdapServer:            c.ldapHost,
		LdapPort:              c.ldapPort,
		UseSsl:                false, // Using plain LDAP for local dev
		UseStartTls:           false,
		SkipSslVerify:         true,
		LdapBindUser:          defaultLDAPBindUser,
		LdapBindPassword:      ldapBindPassword,
		LdapBaseDn:            defaultLDAPBaseDN,
		LdapSearchFilter:      "(objectClass=user)",
		LdapAdminBaseDn:       "",
		LdapAdminFilter:       "(memberOf=cn=jellyfin-admins,ou=groups,dc=ldap,dc=goauthentik,dc=io)",
		LdapSearchAttributes:  "uid, cn, mail, displayName",
		LdapUidAttribute:      "uid",
		LdapUsernameAttribute: "cn",
		LdapPasswordAttribute: "userPassword",
		CreateUsersFromLdap:   true,
		AllowPassChange:       false,
		EnableAllFolders:      true,
		EnabledFolders:        []string{},
	}

	configBytes, _ := json.Marshal(newConfig)
	if err := c.setPluginConfiguration(ctx, token, ldapPluginID, configBytes); err != nil {
		return fmt.Errorf("setting LDAP config: %w", err)
	}

	log.Println("Jellyfin: LDAP configured successfully")

	// Delete bootstrap admin after LDAP is configured
	if err := c.deleteBootstrapAdmin(ctx, token); err != nil {
		log.Printf("Jellyfin: Warning - failed to delete bootstrap admin: %v", err)
		// Don't fail - LDAP is working, bootstrap admin can be cleaned up later
	}

	return nil
}

// AuthResponse represents the authentication response
type AuthResponse struct {
	AccessToken string `json:"AccessToken"`
	User        struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	} `json:"User"`
}

// authenticate logs in to Jellyfin and returns an access token
func (c *Configurator) authenticate(ctx context.Context, username, password string) (string, error) {
	url := c.getBaseURL() + "/Users/AuthenticateByName"

	body := map[string]string{
		"Username": username,
		"Pw":       password,
	}

	bodyBytes, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// Jellyfin requires this header for authentication
	req.Header.Set("X-Emby-Authorization", `MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0"`)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("authentication failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var authResp AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return "", err
	}

	return authResp.AccessToken, nil
}

// getPluginConfiguration fetches a plugin's configuration
func (c *Configurator) getPluginConfiguration(ctx context.Context, token, pluginID string) ([]byte, error) {
	url := fmt.Sprintf("%s/Plugins/%s/Configuration", c.getBaseURL(), pluginID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Emby-Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return io.ReadAll(resp.Body)
}

// setPluginConfiguration updates a plugin's configuration
func (c *Configurator) setPluginConfiguration(ctx context.Context, token, pluginID string, config []byte) error {
	url := fmt.Sprintf("%s/Plugins/%s/Configuration", c.getBaseURL(), pluginID)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(config))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// User represents a Jellyfin user
type User struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

// getUsers fetches all users from Jellyfin
func (c *Configurator) getUsers(ctx context.Context, token string) ([]User, error) {
	url := c.getBaseURL() + "/Users"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Emby-Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var users []User
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}

	return users, nil
}

// deleteUser deletes a user by ID
func (c *Configurator) deleteUser(ctx context.Context, token, userID string) error {
	url := fmt.Sprintf("%s/Users/%s", c.getBaseURL(), userID)

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Emby-Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// deleteBootstrapAdmin removes the bootstrap admin user
func (c *Configurator) deleteBootstrapAdmin(ctx context.Context, token string) error {
	users, err := c.getUsers(ctx, token)
	if err != nil {
		return fmt.Errorf("getting users: %w", err)
	}

	for _, user := range users {
		if user.Name == bootstrapUsername {
			log.Println("Jellyfin: Deleting bootstrap admin user...")
			if err := c.deleteUser(ctx, token, user.ID); err != nil {
				return fmt.Errorf("deleting user: %w", err)
			}
			log.Println("Jellyfin: Bootstrap admin deleted")
			return nil
		}
	}

	log.Println("Jellyfin: Bootstrap admin not found (may already be deleted)")
	return nil
}
