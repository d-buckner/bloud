package prowlarr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
