package jellyfin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

func TestNewConfigurator(t *testing.T) {
	tests := []struct {
		name     string
		port     int
		wantPort int
	}{
		{
			name:     "custom port",
			port:     9000,
			wantPort: 9000,
		},
		{
			name:     "default port when zero",
			port:     0,
			wantPort: 8096,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewConfigurator(tt.port, "http://localhost:9001", "test-token")
			if c.Port != tt.wantPort {
				t.Errorf("NewConfigurator(%d).Port = %d, want %d", tt.port, c.Port, tt.wantPort)
			}
		})
	}
}

func TestConfigurator_Name(t *testing.T) {
	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	if got := c.Name(); got != "jellyfin" {
		t.Errorf("Name() = %q, want %q", got, "jellyfin")
	}
}

func TestConfigurator_PreStart(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	state := &configurator.AppState{
		Name:          "jellyfin",
		DataPath:      filepath.Join(tmpDir, "jellyfin"),
		BloudDataPath: filepath.Join(tmpDir, "bloud"),
	}

	err := c.PreStart(ctx, state)
	if err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}

	// Verify directories were created
	expectedDirs := []string{
		filepath.Join(state.DataPath, "config"),
		filepath.Join(state.DataPath, "cache"),
		filepath.Join(state.BloudDataPath, "media", "movies"),
		filepath.Join(state.BloudDataPath, "media", "shows"),
	}

	for _, dir := range expectedDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("PreStart() did not create directory %s", dir)
		}
	}

	// Verify network.xml was created with reverse proxy settings
	networkPath := filepath.Join(state.DataPath, "config", "network.xml")
	content, err := os.ReadFile(networkPath)
	if err != nil {
		t.Fatalf("PreStart() did not create network.xml: %v", err)
	}

	contentStr := string(content)

	// Check EnablePublishedServerUriByRequest is enabled
	if !strings.Contains(contentStr, "<EnablePublishedServerUriByRequest>true</EnablePublishedServerUriByRequest>") {
		t.Error("network.xml should have EnablePublishedServerUriByRequest=true")
	}

	// Check KnownProxies includes localhost
	if !strings.Contains(contentStr, "<string>127.0.0.1</string>") {
		t.Error("network.xml should have 127.0.0.1 in KnownProxies")
	}
}

func TestConfigurator_ConfigureNetwork_CreatesNewFile(t *testing.T) {
	tmpDir := t.TempDir()
	dataPath := filepath.Join(tmpDir, "jellyfin")

	// Create config directory
	if err := os.MkdirAll(filepath.Join(dataPath, "config"), 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	err := c.configureNetwork(dataPath)
	if err != nil {
		t.Fatalf("configureNetwork() error = %v", err)
	}

	// Verify file was created
	networkPath := filepath.Join(dataPath, "config", "network.xml")
	content, err := os.ReadFile(networkPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	contentStr := string(content)

	// Verify key settings
	checks := []struct {
		name     string
		contains string
	}{
		{"EnablePublishedServerUriByRequest", "<EnablePublishedServerUriByRequest>true</EnablePublishedServerUriByRequest>"},
		{"KnownProxies localhost", "<string>127.0.0.1</string>"},
		{"KnownProxies IPv6", "<string>::1</string>"},
		{"EnableRemoteAccess", "<EnableRemoteAccess>true</EnableRemoteAccess>"},
	}

	for _, check := range checks {
		if !strings.Contains(contentStr, check.contains) {
			t.Errorf("network.xml missing %s: expected %q", check.name, check.contains)
		}
	}
}

func TestConfigurator_ConfigureNetwork_SkipsIfAlreadyConfigured(t *testing.T) {
	tmpDir := t.TempDir()
	dataPath := filepath.Join(tmpDir, "jellyfin")
	configDir := filepath.Join(dataPath, "config")

	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	// Create existing network.xml with EnablePublishedServerUriByRequest=true
	existing := `<?xml version="1.0" encoding="UTF-8"?>
<NetworkConfiguration>
  <EnablePublishedServerUriByRequest>true</EnablePublishedServerUriByRequest>
  <CustomSetting>should-be-preserved</CustomSetting>
</NetworkConfiguration>`
	networkPath := filepath.Join(configDir, "network.xml")
	if err := os.WriteFile(networkPath, []byte(existing), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	err := c.configureNetwork(dataPath)
	if err != nil {
		t.Fatalf("configureNetwork() error = %v", err)
	}

	// Verify file wasn't modified (custom setting preserved)
	content, err := os.ReadFile(networkPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !strings.Contains(string(content), "should-be-preserved") {
		t.Error("configureNetwork() should not modify already-configured file")
	}
}

func TestConfigurator_ConfigureNetwork_UpdatesExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	dataPath := filepath.Join(tmpDir, "jellyfin")
	configDir := filepath.Join(dataPath, "config")

	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	// Create existing network.xml with EnablePublishedServerUriByRequest=false
	existing := `<?xml version="1.0" encoding="UTF-8"?>
<NetworkConfiguration>
  <EnablePublishedServerUriByRequest>false</EnablePublishedServerUriByRequest>
  <SomeOtherSetting>value</SomeOtherSetting>
</NetworkConfiguration>`
	networkPath := filepath.Join(configDir, "network.xml")
	if err := os.WriteFile(networkPath, []byte(existing), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	err := c.configureNetwork(dataPath)
	if err != nil {
		t.Fatalf("configureNetwork() error = %v", err)
	}

	// Verify setting was updated
	content, err := os.ReadFile(networkPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	contentStr := string(content)

	if !strings.Contains(contentStr, "<EnablePublishedServerUriByRequest>true</EnablePublishedServerUriByRequest>") {
		t.Error("configureNetwork() should update EnablePublishedServerUriByRequest to true")
	}

	// Verify KnownProxies was added
	if !strings.Contains(contentStr, "<string>127.0.0.1</string>") {
		t.Error("configureNetwork() should add KnownProxies")
	}
}

func TestConfigurator_GetSystemInfo(t *testing.T) {
	tests := []struct {
		name       string
		response   SystemInfo
		statusCode int
		wantErr    bool
	}{
		{
			name: "wizard completed",
			response: SystemInfo{
				StartupWizardCompleted: true,
				ServerName:             "Test Server",
				Version:                "10.8.0",
			},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name: "wizard not completed",
			response: SystemInfo{
				StartupWizardCompleted: false,
			},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "server error",
			response:   SystemInfo{},
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/System/Info/Public" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method != http.MethodGet {
					t.Errorf("expected GET, got %s", r.Method)
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			c := NewConfigurator(8096, "http://localhost:9001", "test-token")
			c.baseURL = server.URL

			got, err := c.getSystemInfo(context.Background())

			if (err != nil) != tt.wantErr {
				t.Errorf("getSystemInfo() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got.StartupWizardCompleted != tt.response.StartupWizardCompleted {
				t.Errorf("StartupWizardCompleted = %v, want %v", got.StartupWizardCompleted, tt.response.StartupWizardCompleted)
			}
		})
	}
}

func TestConfigurator_CompleteStartupWizard(t *testing.T) {
	var calls []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)

		switch r.URL.Path {
		case "/Startup/Configuration":
			if r.Method == http.MethodGet {
				// waitForStartupWizardReady checks this endpoint
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{}`))
				return
			}
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			var payload map[string]string
			json.NewDecoder(r.Body).Decode(&payload)
			if payload["UICulture"] != "en-US" {
				t.Errorf("expected UICulture=en-US, got %s", payload["UICulture"])
			}
			w.WriteHeader(http.StatusNoContent)

		case "/Startup/User":
			if r.Method == http.MethodGet {
				// setStartupUser waits for initial user to be available
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{}`))
				return
			}
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			var payload map[string]string
			json.NewDecoder(r.Body).Decode(&payload)
			if payload["Name"] != bootstrapUsername {
				t.Errorf("expected Name=%s, got %s", bootstrapUsername, payload["Name"])
			}
			w.WriteHeader(http.StatusNoContent)

		case "/Startup/RemoteAccess":
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)

		case "/Startup/Complete":
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	c.baseURL = server.URL

	err := c.completeStartupWizard(context.Background())
	if err != nil {
		t.Fatalf("completeStartupWizard() error = %v", err)
	}

	// Verify key steps were called (GET calls for readiness checks may vary)
	requiredCalls := []string{
		"GET /Startup/Configuration",  // waitForStartupWizardReady
		"POST /Startup/Configuration", // setStartupConfiguration
		"GET /Startup/User",           // setStartupUser waits for user
		"POST /Startup/User",          // setStartupUser updates user
		"POST /Startup/RemoteAccess",  // setRemoteAccess
		"POST /Startup/Complete",      // completeWizard
	}

	for _, required := range requiredCalls {
		found := false
		for _, call := range calls {
			if call == required {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected call %q not found in %v", required, calls)
		}
	}
}

func TestConfigurator_Authenticate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Users/AuthenticateByName" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Verify X-Emby-Authorization header
		authHeader := r.Header.Get("X-Emby-Authorization")
		if !strings.Contains(authHeader, "MediaBrowser") {
			t.Error("Missing MediaBrowser in Authorization header")
		}

		resp := AuthResponse{
			AccessToken: "test-token-123",
		}
		resp.User.ID = "user-id"
		resp.User.Name = bootstrapUsername
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	c.baseURL = server.URL

	token, err := c.authenticate(context.Background(), bootstrapUsername, bootstrapPassword)
	if err != nil {
		t.Fatalf("authenticate() error = %v", err)
	}

	if token != "test-token-123" {
		t.Errorf("Expected token 'test-token-123', got '%s'", token)
	}
}

func TestConfigurator_GetPluginConfiguration(t *testing.T) {
	expectedConfig := LDAPConfig{
		LdapServer:          "test-server",
		LdapPort:            389,
		LdapBindUser:        "cn=admin",
		CreateUsersFromLdap: true,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/Plugins/" + ldapPluginID + "/Configuration"
		if r.URL.Path != expectedPath {
			t.Errorf("unexpected path: %s, expected %s", r.URL.Path, expectedPath)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Verify token in header
		authHeader := r.Header.Get("X-Emby-Authorization")
		if !strings.Contains(authHeader, "test-token") {
			t.Error("Missing token in Authorization header")
		}

		json.NewEncoder(w).Encode(expectedConfig)
	}))
	defer server.Close()

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	c.baseURL = server.URL

	configBytes, err := c.getPluginConfiguration(context.Background(), "test-token", ldapPluginID)
	if err != nil {
		t.Fatalf("getPluginConfiguration() error = %v", err)
	}

	var config LDAPConfig
	if err := json.Unmarshal(configBytes, &config); err != nil {
		t.Fatalf("Failed to unmarshal config: %v", err)
	}

	if config.LdapServer != "test-server" {
		t.Errorf("Expected LdapServer 'test-server', got '%s'", config.LdapServer)
	}
}

func TestConfigurator_SetPluginConfiguration(t *testing.T) {
	var receivedConfig LDAPConfig

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}

		json.NewDecoder(r.Body).Decode(&receivedConfig)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	c.baseURL = server.URL

	config := LDAPConfig{
		LdapServer:          "new-server",
		LdapPort:            636,
		CreateUsersFromLdap: true,
	}

	configBytes, _ := json.Marshal(config)
	err := c.setPluginConfiguration(context.Background(), "test-token", ldapPluginID, configBytes)
	if err != nil {
		t.Fatalf("setPluginConfiguration() error = %v", err)
	}

	if receivedConfig.LdapServer != "new-server" {
		t.Errorf("Expected LdapServer 'new-server', got '%s'", receivedConfig.LdapServer)
	}
}

func TestConfigurator_GetUsers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Users" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		users := []User{
			{ID: "user-1", Name: "admin"},
			{ID: "user-2", Name: bootstrapUsername},
		}
		json.NewEncoder(w).Encode(users)
	}))
	defer server.Close()

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	c.baseURL = server.URL

	users, err := c.getUsers(context.Background(), "test-token")
	if err != nil {
		t.Fatalf("getUsers() error = %v", err)
	}

	if len(users) != 2 {
		t.Fatalf("Expected 2 users, got %d", len(users))
	}

	if users[1].Name != bootstrapUsername {
		t.Errorf("Expected user name '%s', got '%s'", bootstrapUsername, users[1].Name)
	}
}

func TestConfigurator_DeleteUser(t *testing.T) {
	deletedUserID := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("Expected DELETE, got %s", r.Method)
		}

		// Extract user ID from path
		parts := strings.Split(r.URL.Path, "/")
		deletedUserID = parts[len(parts)-1]

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	c.baseURL = server.URL

	err := c.deleteUser(context.Background(), "test-token", "user-123")
	if err != nil {
		t.Fatalf("deleteUser() error = %v", err)
	}

	if deletedUserID != "user-123" {
		t.Errorf("Expected deleted user ID 'user-123', got '%s'", deletedUserID)
	}
}

func TestConfigurator_DeleteBootstrapAdmin(t *testing.T) {
	deleteCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users" && r.Method == "GET":
			users := []User{
				{ID: "keep-me", Name: "admin"},
				{ID: "delete-me", Name: bootstrapUsername},
			}
			json.NewEncoder(w).Encode(users)

		case strings.HasPrefix(r.URL.Path, "/Users/") && r.Method == "DELETE":
			userID := strings.TrimPrefix(r.URL.Path, "/Users/")
			if userID != "delete-me" {
				t.Errorf("Expected to delete 'delete-me', got '%s'", userID)
			}
			deleteCalled = true
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	c.baseURL = server.URL

	err := c.deleteBootstrapAdmin(context.Background(), "test-token")
	if err != nil {
		t.Fatalf("deleteBootstrapAdmin() error = %v", err)
	}

	if !deleteCalled {
		t.Error("Delete was not called for bootstrap admin")
	}
}

func TestConfigurator_DeleteBootstrapAdmin_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return users without bootstrap admin
		users := []User{
			{ID: "user-1", Name: "admin"},
			{ID: "user-2", Name: "other-user"},
		}
		json.NewEncoder(w).Encode(users)
	}))
	defer server.Close()

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	c.baseURL = server.URL

	// Should not error even if bootstrap admin is not found
	err := c.deleteBootstrapAdmin(context.Background(), "test-token")
	if err != nil {
		t.Fatalf("deleteBootstrapAdmin() should not error when user not found: %v", err)
	}
}

func TestConfigurator_PostStart_WizardAlreadyComplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info/Public":
			resp := SystemInfo{StartupWizardCompleted: true}
			json.NewEncoder(w).Encode(resp)

		case "/Users/AuthenticateByName":
			// configureLibraries needs to authenticate
			resp := AuthResponse{AccessToken: "test-token"}
			json.NewEncoder(w).Encode(resp)

		case "/Library/VirtualFolders":
			if r.Method == http.MethodGet {
				// Return empty list - no libraries yet
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode([]VirtualFolder{})
			} else if r.Method == http.MethodPost {
				// Library creation
				w.WriteHeader(http.StatusNoContent)
			}

		default:
			t.Errorf("Unexpected endpoint called: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	c.baseURL = server.URL

	state := &configurator.AppState{
		Name:         "jellyfin",
		Integrations: map[string][]string{}, // No SSO
	}

	err := c.PostStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}
}

func TestConfigurator_ConfigureLDAP_AlreadyConfigured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/AuthenticateByName":
			resp := AuthResponse{AccessToken: "test-token"}
			json.NewEncoder(w).Encode(resp)

		case "/Plugins/" + ldapPluginID + "/Configuration":
			if r.Method == "GET" {
				// Return already configured LDAP
				config := LDAPConfig{
					LdapServer:   defaultLDAPHost,
					LdapBindUser: "cn=already-configured",
				}
				json.NewEncoder(w).Encode(config)
			} else {
				// POST should not be called
				t.Error("SetPluginConfiguration should not be called when already configured")
				w.WriteHeader(http.StatusBadRequest)
			}

		default:
			t.Errorf("Unexpected endpoint: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	c.baseURL = server.URL

	err := c.configureLDAP(context.Background())
	if err != nil {
		t.Fatalf("configureLDAP() error = %v", err)
	}
}

func TestConfigurator_ConfigureLDAP_PluginNotInstalled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/AuthenticateByName":
			resp := AuthResponse{AccessToken: "test-token"}
			json.NewEncoder(w).Encode(resp)

		case "/Plugins/" + ldapPluginID + "/Configuration":
			// Plugin not found
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("Unexpected endpoint: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := NewConfigurator(8096, "http://localhost:9001", "test-token")
	c.baseURL = server.URL

	// Should not error when plugin is not installed
	err := c.configureLDAP(context.Background())
	if err != nil {
		t.Fatalf("configureLDAP() should not error when plugin not installed: %v", err)
	}
}
