package prowlarr

import (
	"context"
	"encoding/json"
	"fmt"
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
			wantPort: 9696,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewConfigurator(tt.port)
			if c.Port != tt.wantPort {
				t.Errorf("NewConfigurator(%d).Port = %d, want %d", tt.port, c.Port, tt.wantPort)
			}
		})
	}
}

func TestConfigurator_Name(t *testing.T) {
	c := NewConfigurator(9696)
	if got := c.Name(); got != "prowlarr" {
		t.Errorf("Name() = %q, want %q", got, "prowlarr")
	}
}

func TestConfigurator_PreStart(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	c := NewConfigurator(9696)
	state := &configurator.AppState{
		Name:          "prowlarr",
		DataPath:      filepath.Join(tmpDir, "prowlarr"),
		BloudDataPath: filepath.Join(tmpDir, "bloud"),
	}

	err := c.PreStart(ctx, state)
	if err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}

	configDir := filepath.Join(state.DataPath, "config")
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		t.Errorf("PreStart() did not create directory %s", configDir)
	}

	// Verify external auth config was created
	configPath := filepath.Join(configDir, "config.xml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("PreStart() did not create config.xml: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "<AuthenticationMethod>External</AuthenticationMethod>") {
		t.Error("PreStart() did not set AuthenticationMethod to External")
	}
	if !strings.Contains(content, "<AuthenticationRequired>Enabled</AuthenticationRequired>") {
		t.Error("PreStart() did not set AuthenticationRequired to Enabled")
	}
}

func TestConfigurator_configureExternalAuth(t *testing.T) {
	tests := []struct {
		name          string
		existingXML   string
		wantExternal  bool
		wantRequired  bool
	}{
		{
			name:         "no existing config",
			existingXML:  "",
			wantExternal: true,
			wantRequired: true,
		},
		{
			name:         "already configured correctly",
			existingXML:  `<Config><AuthenticationMethod>External</AuthenticationMethod><AuthenticationRequired>Enabled</AuthenticationRequired></Config>`,
			wantExternal: true,
			wantRequired: true,
		},
		{
			name:         "different auth method",
			existingXML:  `<Config><AuthenticationMethod>Forms</AuthenticationMethod></Config>`,
			wantExternal: true,
			wantRequired: true,
		},
		{
			name:         "missing auth method",
			existingXML:  `<Config><ApiKey>test-key</ApiKey></Config>`,
			wantExternal: true,
			wantRequired: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configDir := filepath.Join(tmpDir, "config")
			os.MkdirAll(configDir, 0755)

			if tt.existingXML != "" {
				os.WriteFile(filepath.Join(configDir, "config.xml"), []byte(tt.existingXML), 0644)
			}

			c := NewConfigurator(9696)
			err := c.configureExternalAuth(tmpDir)
			if err != nil {
				t.Fatalf("configureExternalAuth() error = %v", err)
			}

			data, err := os.ReadFile(filepath.Join(configDir, "config.xml"))
			if err != nil {
				t.Fatalf("failed to read config.xml: %v", err)
			}

			content := string(data)
			hasExternal := strings.Contains(content, "<AuthenticationMethod>External</AuthenticationMethod>")
			hasRequired := strings.Contains(content, "<AuthenticationRequired>Enabled</AuthenticationRequired>")

			if hasExternal != tt.wantExternal {
				t.Errorf("AuthenticationMethod=External: got %v, want %v", hasExternal, tt.wantExternal)
			}
			if hasRequired != tt.wantRequired {
				t.Errorf("AuthenticationRequired=Enabled: got %v, want %v", hasRequired, tt.wantRequired)
			}
		})
	}
}

func TestConfigurator_getAPIKey(t *testing.T) {
	tests := []struct {
		name       string
		configXML  string
		wantAPIKey string
		wantErr    bool
	}{
		{
			name:       "valid config",
			configXML:  `<Config><ApiKey>prowlarr-api-key</ApiKey></Config>`,
			wantAPIKey: "prowlarr-api-key",
			wantErr:    false,
		},
		{
			name:       "empty API key",
			configXML:  `<Config><ApiKey></ApiKey></Config>`,
			wantAPIKey: "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configDir := filepath.Join(tmpDir, "config")
			os.MkdirAll(configDir, 0755)
			os.WriteFile(filepath.Join(configDir, "config.xml"), []byte(tt.configXML), 0644)

			c := NewConfigurator(9696)
			apiKey, err := c.getAPIKey(tmpDir)

			if (err != nil) != tt.wantErr {
				t.Errorf("getAPIKey() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if apiKey != tt.wantAPIKey {
				t.Errorf("getAPIKey() = %q, want %q", apiKey, tt.wantAPIKey)
			}
		})
	}
}

func TestConfigurator_getArrAPIKey(t *testing.T) {
	tmpDir := t.TempDir()

	// Create radarr config
	radarrConfigDir := filepath.Join(tmpDir, "radarr", "config")
	os.MkdirAll(radarrConfigDir, 0755)
	os.WriteFile(filepath.Join(radarrConfigDir, "config.xml"), []byte(`<Config><ApiKey>radarr-key</ApiKey></Config>`), 0644)

	c := NewConfigurator(9696)

	t.Run("app exists", func(t *testing.T) {
		apiKey, err := c.getArrAPIKey(tmpDir, "radarr")
		if err != nil {
			t.Fatalf("getArrAPIKey() error = %v", err)
		}
		if apiKey != "radarr-key" {
			t.Errorf("getArrAPIKey() = %q, want radarr-key", apiKey)
		}
	})

	t.Run("app not installed", func(t *testing.T) {
		_, err := c.getArrAPIKey(tmpDir, "sonarr")
		if err == nil {
			t.Error("expected error for missing app")
		}
	})
}

func TestConfigurator_getSyncCategories(t *testing.T) {
	c := NewConfigurator(9696)

	tests := []struct {
		appName   string
		wantCount int
	}{
		{"radarr", 8}, // Movie categories
		{"sonarr", 8}, // TV categories
		{"unknown", 0},
	}

	for _, tt := range tests {
		t.Run(tt.appName, func(t *testing.T) {
			categories := c.getSyncCategories(tt.appName)
			if len(categories) != tt.wantCount {
				t.Errorf("getSyncCategories(%q) returned %d categories, want %d", tt.appName, len(categories), tt.wantCount)
			}
		})
	}
}

func TestConfigurator_getApplications(t *testing.T) {
	tests := []struct {
		name       string
		response   []application
		statusCode int
		wantCount  int
		wantErr    bool
	}{
		{
			name: "multiple apps",
			response: []application{
				{ID: 1, Name: "Bloud: Radarr"},
				{ID: 2, Name: "Bloud: Sonarr"},
			},
			statusCode: http.StatusOK,
			wantCount:  2,
			wantErr:    false,
		},
		{
			name:       "no apps",
			response:   []application{},
			statusCode: http.StatusOK,
			wantCount:  0,
			wantErr:    false,
		},
		{
			name:       "server error",
			response:   nil,
			statusCode: http.StatusInternalServerError,
			wantCount:  0,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/applications" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Header.Get("X-Api-Key") != "test-key" {
					t.Error("expected X-Api-Key header")
				}

				w.WriteHeader(tt.statusCode)
				if tt.response != nil {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			c := &Configurator{Port: 9696, baseURL: server.URL}
			apps, err := c.getApplications(context.Background(), "test-key")

			if (err != nil) != tt.wantErr {
				t.Errorf("getApplications() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if len(apps) != tt.wantCount {
				t.Errorf("getApplications() returned %d apps, want %d", len(apps), tt.wantCount)
			}
		})
	}
}

func TestConfigurator_createApplication(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "created successfully",
			statusCode: http.StatusCreated,
			wantErr:    false,
		},
		{
			name:       "ok response",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedPayload map[string]any

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				json.NewDecoder(r.Body).Decode(&receivedPayload)
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			c := &Configurator{Port: 9696, baseURL: server.URL}
			app := map[string]any{
				"name":           "Bloud: Radarr",
				"implementation": "Radarr",
			}

			err := c.createApplication(context.Background(), "test-key", app)

			if (err != nil) != tt.wantErr {
				t.Errorf("createApplication() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && receivedPayload["name"] != "Bloud: Radarr" {
				t.Errorf("expected name=Bloud: Radarr, got %v", receivedPayload["name"])
			}
		})
	}
}

func TestConfigurator_getProwlarrURL(t *testing.T) {
	t.Run("with baseURL override", func(t *testing.T) {
		c := &Configurator{Port: 9696, baseURL: "http://test-server:9696"}
		if got := c.getProwlarrURL(); got != "http://test-server:9696" {
			t.Errorf("getProwlarrURL() = %q, want %q", got, "http://test-server:9696")
		}
	})

	t.Run("without baseURL uses container DNS", func(t *testing.T) {
		c := &Configurator{Port: 9696}
		expected := "http://prowlarr:9696"
		if got := c.getProwlarrURL(); got != expected {
			t.Errorf("getProwlarrURL() = %q, want %q", got, expected)
		}
	})
}

func TestConfigurator_ensureApplication_payload(t *testing.T) {
	tests := []struct {
		name           string
		appName        string
		appDisplayName string
		appPort        int
		apiKey         string
		wantBaseURL    string
	}{
		{
			name:           "radarr",
			appName:        "radarr",
			appDisplayName: "Radarr",
			appPort:        7878,
			apiKey:         "radarr-api-key",
			wantBaseURL:    "http://radarr:7878",
		},
		{
			name:           "sonarr",
			appName:        "sonarr",
			appDisplayName: "Sonarr",
			appPort:        8989,
			apiKey:         "sonarr-api-key",
			wantBaseURL:    "http://sonarr:8989",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			// Setup app config
			appConfigDir := filepath.Join(tmpDir, tt.appName, "config")
			os.MkdirAll(appConfigDir, 0755)
			os.WriteFile(filepath.Join(appConfigDir, "config.xml"), []byte(fmt.Sprintf(`<Config><ApiKey>%s</ApiKey></Config>`, tt.apiKey)), 0644)

			var receivedPayload map[string]any

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					json.NewEncoder(w).Encode([]application{})
				case http.MethodPost:
					json.NewDecoder(r.Body).Decode(&receivedPayload)
					w.WriteHeader(http.StatusCreated)
				}
			}))
			defer server.Close()

			c := &Configurator{Port: 9696, baseURL: server.URL}
			state := &configurator.AppState{
				BloudDataPath: tmpDir,
			}

			err := c.ensureApplication(context.Background(), "prowlarr-key", state, tt.appName, tt.appDisplayName, tt.appPort)
			if err != nil {
				t.Fatalf("ensureApplication() error = %v", err)
			}

			// Verify payload structure
			expectedName := fmt.Sprintf("Bloud: %s", tt.appDisplayName)
			if receivedPayload["name"] != expectedName {
				t.Errorf("expected name=%q, got %v", expectedName, receivedPayload["name"])
			}
			if receivedPayload["implementation"] != tt.appDisplayName {
				t.Errorf("expected implementation=%q, got %v", tt.appDisplayName, receivedPayload["implementation"])
			}
			expectedContract := fmt.Sprintf("%sSettings", tt.appDisplayName)
			if receivedPayload["configContract"] != expectedContract {
				t.Errorf("expected configContract=%q, got %v", expectedContract, receivedPayload["configContract"])
			}
			if receivedPayload["syncLevel"] != "fullSync" {
				t.Errorf("expected syncLevel='fullSync', got %v", receivedPayload["syncLevel"])
			}

			// Verify tags field is present (required by API)
			if _, ok := receivedPayload["tags"]; !ok {
				t.Error("expected tags field to be present")
			}

			// Verify fields array
			fields, ok := receivedPayload["fields"].([]any)
			if !ok {
				t.Fatalf("expected fields to be an array, got %T", receivedPayload["fields"])
			}

			// Find and verify each field
			fieldMap := make(map[string]any)
			for _, f := range fields {
				field := f.(map[string]any)
				fieldMap[field["name"].(string)] = field["value"]
			}

			// Verify prowlarrUrl uses test server URL (simulating container DNS in tests)
			if fieldMap["prowlarrUrl"] != server.URL {
				t.Errorf("expected prowlarrUrl=%q, got %v", server.URL, fieldMap["prowlarrUrl"])
			}

			// Verify baseUrl uses container DNS format
			if fieldMap["baseUrl"] != tt.wantBaseURL {
				t.Errorf("expected baseUrl=%q, got %v", tt.wantBaseURL, fieldMap["baseUrl"])
			}

			// Verify apiKey is present
			if fieldMap["apiKey"] != tt.apiKey {
				t.Errorf("expected apiKey=%q, got %v", tt.apiKey, fieldMap["apiKey"])
			}
		})
	}
}

func TestConfigurator_ensureApplication(t *testing.T) {
	tests := []struct {
		name            string
		existingApps    []application
		appExists       bool
		wantCreate      bool
	}{
		{
			name:         "already configured",
			existingApps: []application{{ID: 1, Name: "Bloud: Radarr"}},
			appExists:    true,
			wantCreate:   false,
		},
		{
			name:         "needs creation",
			existingApps: []application{},
			appExists:    true,
			wantCreate:   true,
		},
		{
			name:         "app not installed",
			existingApps: []application{},
			appExists:    false,
			wantCreate:   false, // Can't create if app doesn't exist
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var createCalled bool
			tmpDir := t.TempDir()

			// Setup radarr config if it should exist
			if tt.appExists {
				radarrConfigDir := filepath.Join(tmpDir, "radarr", "config")
				os.MkdirAll(radarrConfigDir, 0755)
				os.WriteFile(filepath.Join(radarrConfigDir, "config.xml"), []byte(`<Config><ApiKey>radarr-key</ApiKey></Config>`), 0644)
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					json.NewEncoder(w).Encode(tt.existingApps)
				case http.MethodPost:
					createCalled = true
					w.WriteHeader(http.StatusCreated)
				}
			}))
			defer server.Close()

			c := &Configurator{Port: 9696, baseURL: server.URL}
			state := &configurator.AppState{
				BloudDataPath: tmpDir,
			}

			err := c.ensureApplication(context.Background(), "prowlarr-key", state, "radarr", "Radarr", 7878)

			if !tt.appExists {
				// Should error when app not installed
				if err == nil {
					t.Error("expected error when app not installed")
				}
				return
			}

			if err != nil {
				t.Fatalf("ensureApplication() error = %v", err)
			}

			if createCalled != tt.wantCreate {
				t.Errorf("createCalled = %v, want %v", createCalled, tt.wantCreate)
			}
		})
	}
}

func TestConfigurator_PostStart(t *testing.T) {
	tmpDir := t.TempDir()

	// Setup prowlarr config
	prowlarrConfigDir := filepath.Join(tmpDir, "prowlarr", "config")
	os.MkdirAll(prowlarrConfigDir, 0755)
	os.WriteFile(filepath.Join(prowlarrConfigDir, "config.xml"), []byte(`<Config><ApiKey>prowlarr-key</ApiKey></Config>`), 0644)

	// Setup radarr config
	radarrConfigDir := filepath.Join(tmpDir, "radarr", "config")
	os.MkdirAll(radarrConfigDir, 0755)
	os.WriteFile(filepath.Join(radarrConfigDir, "config.xml"), []byte(`<Config><ApiKey>radarr-key</ApiKey></Config>`), 0644)

	// Setup sonarr config
	sonarrConfigDir := filepath.Join(tmpDir, "sonarr", "config")
	os.MkdirAll(sonarrConfigDir, 0755)
	os.WriteFile(filepath.Join(sonarrConfigDir, "config.xml"), []byte(`<Config><ApiKey>sonarr-key</ApiKey></Config>`), 0644)

	var createdApps []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "prowlarr-key" {
			t.Errorf("expected X-Api-Key: prowlarr-key, got %s", r.Header.Get("X-Api-Key"))
		}

		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]application{})
		case http.MethodPost:
			var payload map[string]any
			json.NewDecoder(r.Body).Decode(&payload)
			createdApps = append(createdApps, payload["name"].(string))
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer server.Close()

	c := &Configurator{Port: 9696, baseURL: server.URL}
	state := &configurator.AppState{
		Name:          "prowlarr",
		DataPath:      filepath.Join(tmpDir, "prowlarr"),
		BloudDataPath: tmpDir,
	}

	err := c.PostStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	// Should have created both Radarr and Sonarr applications
	if len(createdApps) != 2 {
		t.Errorf("expected 2 apps created, got %d: %v", len(createdApps), createdApps)
	}

	hasRadarr := false
	hasSonarr := false
	for _, app := range createdApps {
		if app == "Bloud: Radarr" {
			hasRadarr = true
		}
		if app == "Bloud: Sonarr" {
			hasSonarr = true
		}
	}

	if !hasRadarr {
		t.Error("Radarr application was not created")
	}
	if !hasSonarr {
		t.Error("Sonarr application was not created")
	}
}

func TestConfigurator_PostStart_noArrs(t *testing.T) {
	tmpDir := t.TempDir()

	// Setup prowlarr config only - no radarr/sonarr
	prowlarrConfigDir := filepath.Join(tmpDir, "prowlarr", "config")
	os.MkdirAll(prowlarrConfigDir, 0755)
	os.WriteFile(filepath.Join(prowlarrConfigDir, "config.xml"), []byte(`<Config><ApiKey>prowlarr-key</ApiKey></Config>`), 0644)

	var apiCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalled = true
		json.NewEncoder(w).Encode([]application{})
	}))
	defer server.Close()

	c := &Configurator{Port: 9696, baseURL: server.URL}
	state := &configurator.AppState{
		Name:          "prowlarr",
		DataPath:      filepath.Join(tmpDir, "prowlarr"),
		BloudDataPath: tmpDir,
	}

	// Should not error even if Radarr/Sonarr are not installed
	err := c.PostStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	// API should not have been called since no apps to configure
	if apiCalled {
		t.Error("API should not be called when no *arr apps exist")
	}
}
