package sonarr

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
			wantPort: 8989,
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
	c := NewConfigurator(8989)
	if got := c.Name(); got != "sonarr" {
		t.Errorf("Name() = %q, want %q", got, "sonarr")
	}
}

func TestConfigurator_PreStart(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	c := NewConfigurator(8989)
	state := &configurator.AppState{
		Name:          "sonarr",
		DataPath:      filepath.Join(tmpDir, "sonarr"),
		BloudDataPath: filepath.Join(tmpDir, "bloud"),
	}

	err := c.PreStart(ctx, state)
	if err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}

	expectedDirs := []string{
		filepath.Join(state.DataPath, "config"),
		filepath.Join(state.BloudDataPath, "downloads"),
		filepath.Join(state.BloudDataPath, "media", "shows"),
	}

	for _, dir := range expectedDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("PreStart() did not create directory %s", dir)
		}
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
			configXML:  `<Config><ApiKey>test-api-key-123</ApiKey></Config>`,
			wantAPIKey: "test-api-key-123",
			wantErr:    false,
		},
		{
			name:       "empty API key",
			configXML:  `<Config><ApiKey></ApiKey></Config>`,
			wantAPIKey: "",
			wantErr:    true,
		},
		{
			name:       "missing API key element",
			configXML:  `<Config></Config>`,
			wantAPIKey: "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configDir := filepath.Join(tmpDir, "config")
			os.MkdirAll(configDir, 0755)

			if tt.configXML != "" {
				os.WriteFile(filepath.Join(configDir, "config.xml"), []byte(tt.configXML), 0644)
			}

			c := NewConfigurator(8989)
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

func TestConfigurator_getDownloadClients(t *testing.T) {
	tests := []struct {
		name       string
		response   []downloadClient
		statusCode int
		wantCount  int
		wantErr    bool
	}{
		{
			name: "multiple clients",
			response: []downloadClient{
				{ID: 1, Name: "qBittorrent"},
				{ID: 2, Name: "Transmission"},
			},
			statusCode: http.StatusOK,
			wantCount:  2,
			wantErr:    false,
		},
		{
			name:       "no clients",
			response:   []downloadClient{},
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
				if r.URL.Path != "/api/v3/downloadclient" {
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

			c := &Configurator{Port: 8989, baseURL: server.URL}
			clients, err := c.getDownloadClients(context.Background(), "test-key")

			if (err != nil) != tt.wantErr {
				t.Errorf("getDownloadClients() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if len(clients) != tt.wantCount {
				t.Errorf("getDownloadClients() returned %d clients, want %d", len(clients), tt.wantCount)
			}
		})
	}
}

func TestConfigurator_ensureDownloadClient(t *testing.T) {
	tests := []struct {
		name            string
		integrations    map[string][]string
		existingClients []downloadClient
		wantCreate      bool
	}{
		{
			name:            "no download client integration",
			integrations:    map[string][]string{},
			existingClients: []downloadClient{},
			wantCreate:      false,
		},
		{
			name:            "already configured",
			integrations:    map[string][]string{"downloadClient": {"qbittorrent"}},
			existingClients: []downloadClient{{ID: 1, Name: "Bloud: qBittorrent"}},
			wantCreate:      false,
		},
		{
			name:            "needs creation",
			integrations:    map[string][]string{"downloadClient": {"qbittorrent"}},
			existingClients: []downloadClient{},
			wantCreate:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var createCalled bool

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					json.NewEncoder(w).Encode(tt.existingClients)
				case http.MethodPost:
					createCalled = true
					w.WriteHeader(http.StatusCreated)
				}
			}))
			defer server.Close()

			c := &Configurator{Port: 8989, baseURL: server.URL}
			state := &configurator.AppState{
				Integrations: tt.integrations,
			}

			err := c.ensureDownloadClient(context.Background(), "test-key", state)
			if err != nil {
				t.Fatalf("ensureDownloadClient() error = %v", err)
			}

			if createCalled != tt.wantCreate {
				t.Errorf("createCalled = %v, want %v", createCalled, tt.wantCreate)
			}
		})
	}
}

func TestConfigurator_ensureRootFolder(t *testing.T) {
	tests := []struct {
		name            string
		existingFolders []rootFolder
		wantCreate      bool
	}{
		{
			name:            "already exists",
			existingFolders: []rootFolder{{ID: 1, Path: "/tv"}},
			wantCreate:      false,
		},
		{
			name:            "needs creation",
			existingFolders: []rootFolder{},
			wantCreate:      true,
		},
		{
			name:            "different folder exists",
			existingFolders: []rootFolder{{ID: 1, Path: "/other"}},
			wantCreate:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var createCalled bool
			var createdPath string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					json.NewEncoder(w).Encode(tt.existingFolders)
				case http.MethodPost:
					createCalled = true
					var payload map[string]string
					json.NewDecoder(r.Body).Decode(&payload)
					createdPath = payload["path"]
					w.WriteHeader(http.StatusCreated)
				}
			}))
			defer server.Close()

			c := &Configurator{Port: 8989, baseURL: server.URL}

			err := c.ensureRootFolder(context.Background(), "test-key")
			if err != nil {
				t.Fatalf("ensureRootFolder() error = %v", err)
			}

			if createCalled != tt.wantCreate {
				t.Errorf("createCalled = %v, want %v", createCalled, tt.wantCreate)
			}
			if tt.wantCreate && createdPath != "/tv" {
				t.Errorf("createdPath = %q, want /tv", createdPath)
			}
		})
	}
}

func TestConfigurator_PostStart(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)
	os.WriteFile(filepath.Join(configDir, "config.xml"), []byte(`<Config><ApiKey>test-key</ApiKey></Config>`), 0644)

	var apiCalls []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls = append(apiCalls, r.Method+" "+r.URL.Path)

		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Errorf("expected X-Api-Key: test-key, got %s", r.Header.Get("X-Api-Key"))
		}

		switch r.URL.Path {
		case "/api/v3/downloadclient":
			if r.Method == http.MethodGet {
				json.NewEncoder(w).Encode([]downloadClient{})
			} else {
				w.WriteHeader(http.StatusCreated)
			}
		case "/api/v3/rootfolder":
			if r.Method == http.MethodGet {
				json.NewEncoder(w).Encode([]rootFolder{})
			} else {
				w.WriteHeader(http.StatusCreated)
			}
		}
	}))
	defer server.Close()

	c := &Configurator{Port: 8989, baseURL: server.URL}
	state := &configurator.AppState{
		Name:          "sonarr",
		DataPath:      tmpDir,
		BloudDataPath: t.TempDir(),
		Integrations:  map[string][]string{"downloadClient": {"qbittorrent"}},
	}

	err := c.PostStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	expectedCalls := []string{
		"GET /api/v3/downloadclient",
		"POST /api/v3/downloadclient",
		"GET /api/v3/rootfolder",
		"POST /api/v3/rootfolder",
	}

	if len(apiCalls) != len(expectedCalls) {
		t.Errorf("expected %d API calls, got %d: %v", len(expectedCalls), len(apiCalls), apiCalls)
		return
	}

	for i, expected := range expectedCalls {
		if apiCalls[i] != expected {
			t.Errorf("call %d: expected %q, got %q", i, expected, apiCalls[i])
		}
	}
}

func TestConfigurator_PostStart_missingAPIKey(t *testing.T) {
	tmpDir := t.TempDir()

	c := NewConfigurator(8989)
	state := &configurator.AppState{
		Name:          "sonarr",
		DataPath:      tmpDir,
		BloudDataPath: t.TempDir(),
	}

	err := c.PostStart(context.Background(), state)
	if err == nil {
		t.Error("expected error for missing API key")
	}
	if !strings.Contains(err.Error(), "failed to get API key") {
		t.Errorf("expected 'failed to get API key' error, got: %v", err)
	}
}
