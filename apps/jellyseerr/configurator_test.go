package jellyseerr

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
			wantPort: 5055,
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
	c := NewConfigurator(5055)
	if got := c.Name(); got != "jellyseerr" {
		t.Errorf("Name() = %q, want %q", got, "jellyseerr")
	}
}

func TestConfigurator_PreStart(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	c := NewConfigurator(5055)
	state := &configurator.AppState{
		Name:          "jellyseerr",
		DataPath:      filepath.Join(tmpDir, "jellyseerr"),
		BloudDataPath: filepath.Join(tmpDir, "bloud"),
	}

	err := c.PreStart(ctx, state)
	if err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}

	// Verify config directory was created
	configDir := filepath.Join(state.DataPath, "config")
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		t.Errorf("PreStart() did not create directory %s", configDir)
	}
}

func TestConfigurator_isInitialized(t *testing.T) {
	tests := []struct {
		name       string
		response   map[string]any
		statusCode int
		want       bool
		wantErr    bool
	}{
		{
			name:       "initialized",
			response:   map[string]any{"initialized": true},
			statusCode: http.StatusOK,
			want:       true,
			wantErr:    false,
		},
		{
			name:       "not initialized",
			response:   map[string]any{"initialized": false},
			statusCode: http.StatusOK,
			want:       false,
			wantErr:    false,
		},
		{
			name:       "server error",
			response:   nil,
			statusCode: http.StatusInternalServerError,
			want:       false,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/settings/public" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method != http.MethodGet {
					t.Errorf("expected GET, got %s", r.Method)
				}

				w.WriteHeader(tt.statusCode)
				if tt.response != nil {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			c := &Configurator{Port: 5055, baseURL: server.URL}
			got, err := c.isInitialized(context.Background())

			if (err != nil) != tt.wantErr {
				t.Errorf("isInitialized() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("isInitialized() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigurator_completeSetupWizard(t *testing.T) {
	var calls []string
	var authPayload map[string]string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)

		switch r.URL.Path {
		case "/api/v1/auth/jellyfin":
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			json.NewDecoder(r.Body).Decode(&authPayload)
			// Set a session cookie
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "test-session"})
			w.WriteHeader(http.StatusOK)

		case "/api/v1/settings/jellyfin/library":
			if r.Method != http.MethodGet {
				t.Errorf("expected GET, got %s", r.Method)
			}
			// Verify cookie is passed
			if _, err := r.Cookie("session"); err != nil {
				t.Error("expected session cookie")
			}

			if r.URL.Query().Get("sync") == "true" {
				// Return mock libraries
				libraries := []Library{
					{ID: "lib1", Name: "Movies"},
					{ID: "lib2", Name: "TV Shows"},
				}
				json.NewEncoder(w).Encode(libraries)
			} else if r.URL.Query().Get("enable") != "" {
				// Verify library IDs are passed
				enableIDs := r.URL.Query().Get("enable")
				if enableIDs != "lib1,lib2" {
					t.Errorf("expected enable=lib1,lib2, got %s", enableIDs)
				}
				w.WriteHeader(http.StatusOK)
			}

		case "/api/v1/settings/initialize":
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			// Verify cookie is passed
			if _, err := r.Cookie("session"); err != nil {
				t.Error("expected session cookie")
			}
			w.WriteHeader(http.StatusOK)

		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := &Configurator{
		Port:        5055,
		baseURL:     server.URL,
		jellyfinURL: "http://jellyfin.test:8096",
	}
	err := c.completeSetupWizard(context.Background())

	if err != nil {
		t.Fatalf("completeSetupWizard() error = %v", err)
	}

	// Verify auth payload
	if authPayload["username"] != "admin" {
		t.Errorf("expected username=admin, got %s", authPayload["username"])
	}
	if authPayload["password"] != "admin123" {
		t.Errorf("expected password=admin123, got %s", authPayload["password"])
	}
	if authPayload["hostname"] != "http://jellyfin.test:8096" {
		t.Errorf("expected hostname=http://jellyfin.test:8096, got %s", authPayload["hostname"])
	}
	if authPayload["email"] != "admin@bloud.local" {
		t.Errorf("expected email=admin@bloud.local, got %s", authPayload["email"])
	}

	// Verify call order
	expectedCalls := []string{
		"POST /api/v1/auth/jellyfin",
		"GET /api/v1/settings/jellyfin/library", // sync
		"GET /api/v1/settings/jellyfin/library", // enable
		"POST /api/v1/settings/initialize",
	}

	if len(calls) != len(expectedCalls) {
		t.Errorf("expected %d calls, got %d: %v", len(expectedCalls), len(calls), calls)
		return
	}

	for i, expected := range expectedCalls {
		if calls[i] != expected {
			t.Errorf("call %d: expected %q, got %q", i, expected, calls[i])
		}
	}
}

func TestConfigurator_completeSetupWizard_noLibraries(t *testing.T) {
	var calls []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)

		switch r.URL.Path {
		case "/api/v1/auth/jellyfin":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "test-session"})
			w.WriteHeader(http.StatusOK)

		case "/api/v1/settings/jellyfin/library":
			// Return empty libraries
			json.NewEncoder(w).Encode([]Library{})

		case "/api/v1/settings/initialize":
			w.WriteHeader(http.StatusOK)

		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := &Configurator{Port: 5055, baseURL: server.URL, jellyfinURL: "http://test:8096"}
	err := c.completeSetupWizard(context.Background())

	if err != nil {
		t.Fatalf("completeSetupWizard() error = %v", err)
	}

	// Should skip enable step when no libraries
	for _, call := range calls {
		if strings.Contains(call, "enable=") {
			t.Error("should not call enable when no libraries")
		}
	}
}

func TestConfigurator_completeSetupWizard_stepFailure(t *testing.T) {
	tests := []struct {
		name      string
		failAt    string
		wantError string
	}{
		{
			name:      "auth step fails",
			failAt:    "/api/v1/auth/jellyfin",
			wantError: "failed to authenticate with Jellyfin",
		},
		{
			name:      "sync libraries step fails",
			failAt:    "/api/v1/settings/jellyfin/library",
			wantError: "failed to sync libraries",
		},
		{
			name:      "initialize step fails",
			failAt:    "/api/v1/settings/initialize",
			wantError: "failed to complete initialization",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tt.failAt {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte("server error"))
					return
				}

				switch r.URL.Path {
				case "/api/v1/auth/jellyfin":
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "test"})
					w.WriteHeader(http.StatusOK)
				case "/api/v1/settings/jellyfin/library":
					json.NewEncoder(w).Encode([]Library{})
				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			c := &Configurator{Port: 5055, baseURL: server.URL, jellyfinURL: "http://test:8096"}
			err := c.completeSetupWizard(context.Background())

			if err == nil {
				t.Error("expected error, got nil")
				return
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantError)
			}
		})
	}
}

func TestConfigurator_PostStart(t *testing.T) {
	tests := []struct {
		name            string
		initialized     bool
		wantSetupCalls  bool
	}{
		{
			name:           "already initialized",
			initialized:    true,
			wantSetupCalls: false,
		},
		{
			name:           "not initialized",
			initialized:    false,
			wantSetupCalls: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var setupCalls int

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/settings/public":
					json.NewEncoder(w).Encode(map[string]bool{"initialized": tt.initialized})
				case "/api/v1/auth/jellyfin":
					setupCalls++
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "test"})
					w.WriteHeader(http.StatusOK)
				case "/api/v1/settings/jellyfin/library":
					setupCalls++
					json.NewEncoder(w).Encode([]Library{})
				case "/api/v1/settings/initialize":
					setupCalls++
					w.WriteHeader(http.StatusOK)
				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			c := &Configurator{Port: 5055, baseURL: server.URL, jellyfinURL: "http://test:8096"}
			state := &configurator.AppState{
				Name:          "jellyseerr",
				DataPath:      t.TempDir(),
				BloudDataPath: t.TempDir(),
			}

			err := c.PostStart(context.Background(), state)
			if err != nil {
				t.Fatalf("PostStart() error = %v", err)
			}

			if tt.wantSetupCalls && setupCalls == 0 {
				t.Error("expected setup calls, got none")
			}
			if !tt.wantSetupCalls && setupCalls > 0 {
				t.Errorf("expected no setup calls, got %d", setupCalls)
			}
		})
	}
}

func TestConfigurator_getLibraries(t *testing.T) {
	tests := []struct {
		name       string
		response   []Library
		statusCode int
		wantCount  int
		wantErr    bool
	}{
		{
			name: "multiple libraries",
			response: []Library{
				{ID: "1", Name: "Movies"},
				{ID: "2", Name: "TV Shows"},
				{ID: "3", Name: "Music"},
			},
			statusCode: http.StatusOK,
			wantCount:  3,
			wantErr:    false,
		},
		{
			name:       "no libraries",
			response:   []Library{},
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
				if r.URL.Query().Get("sync") != "true" {
					t.Error("expected sync=true query param")
				}
				w.WriteHeader(tt.statusCode)
				if tt.response != nil {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			c := &Configurator{Port: 5055, baseURL: server.URL}
			cookies := []*http.Cookie{{Name: "session", Value: "test"}}

			libraries, err := c.getLibraries(context.Background(), server.URL, cookies)

			if (err != nil) != tt.wantErr {
				t.Errorf("getLibraries() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if len(libraries) != tt.wantCount {
				t.Errorf("getLibraries() returned %d libraries, want %d", len(libraries), tt.wantCount)
			}
		})
	}
}

func TestConfigurator_enableLibraries(t *testing.T) {
	tests := []struct {
		name       string
		libraries  []Library
		wantIDs    string
		statusCode int
		wantErr    bool
	}{
		{
			name: "multiple libraries",
			libraries: []Library{
				{ID: "abc", Name: "Movies"},
				{ID: "def", Name: "TV"},
			},
			wantIDs:    "abc,def",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "single library",
			libraries:  []Library{{ID: "xyz", Name: "Movies"}},
			wantIDs:    "xyz",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "server error",
			libraries:  []Library{{ID: "1", Name: "Test"}},
			wantIDs:    "1",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				enableIDs := r.URL.Query().Get("enable")
				if enableIDs != tt.wantIDs {
					t.Errorf("expected enable=%s, got %s", tt.wantIDs, enableIDs)
				}
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			c := &Configurator{Port: 5055, baseURL: server.URL}
			cookies := []*http.Cookie{{Name: "session", Value: "test"}}

			err := c.enableLibraries(context.Background(), server.URL, cookies, tt.libraries)

			if (err != nil) != tt.wantErr {
				t.Errorf("enableLibraries() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
