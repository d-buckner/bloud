package jellyfin

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
			wantPort: 8096,
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
	c := NewConfigurator(8096)
	if got := c.Name(); got != "jellyfin" {
		t.Errorf("Name() = %q, want %q", got, "jellyfin")
	}
}

func TestConfigurator_PreStart(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	c := NewConfigurator(8096)
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
		filepath.Join(state.BloudDataPath, "movies"),
		filepath.Join(state.BloudDataPath, "tv"),
	}

	for _, dir := range expectedDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("PreStart() did not create directory %s", dir)
		}
	}
}

func TestConfigurator_isWizardCompleted(t *testing.T) {
	tests := []struct {
		name       string
		response   map[string]any
		statusCode int
		want       bool
		wantErr    bool
	}{
		{
			name:       "wizard completed",
			response:   map[string]any{"StartupWizardCompleted": true},
			statusCode: http.StatusOK,
			want:       true,
			wantErr:    false,
		},
		{
			name:       "wizard not completed",
			response:   map[string]any{"StartupWizardCompleted": false},
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
				if r.URL.Path != "/System/Info/Public" {
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

			c := &Configurator{Port: 8096, baseURL: server.URL}
			got, err := c.isWizardCompleted(context.Background())

			if (err != nil) != tt.wantErr {
				t.Errorf("isWizardCompleted() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("isWizardCompleted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigurator_completeSetupWizard(t *testing.T) {
	var calls []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)

		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type: application/json, got %s", r.Header.Get("Content-Type"))
		}

		// Verify payloads for each step
		switch r.URL.Path {
		case "/Startup/Configuration":
			var payload map[string]string
			json.NewDecoder(r.Body).Decode(&payload)
			if payload["UICulture"] != "en-US" {
				t.Errorf("expected UICulture=en-US, got %s", payload["UICulture"])
			}
		case "/Startup/User":
			var payload map[string]string
			json.NewDecoder(r.Body).Decode(&payload)
			if payload["Name"] != "admin" {
				t.Errorf("expected Name=admin, got %s", payload["Name"])
			}
			if payload["Password"] != "admin123" {
				t.Errorf("expected Password=admin123, got %s", payload["Password"])
			}
		case "/Startup/RemoteAccess":
			var payload map[string]bool
			json.NewDecoder(r.Body).Decode(&payload)
			if !payload["EnableRemoteAccess"] {
				t.Error("expected EnableRemoteAccess=true")
			}
		case "/Startup/Complete":
			// No payload expected
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := &Configurator{Port: 8096, baseURL: server.URL}
	err := c.completeSetupWizard(context.Background())

	if err != nil {
		t.Fatalf("completeSetupWizard() error = %v", err)
	}

	// Verify all steps were called in order
	expectedCalls := []string{
		"POST /Startup/Configuration",
		"POST /Startup/User",
		"POST /Startup/RemoteAccess",
		"POST /Startup/Complete",
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

func TestConfigurator_completeSetupWizard_stepFailure(t *testing.T) {
	tests := []struct {
		name      string
		failAt    string
		wantError string
	}{
		{
			name:      "configuration step fails",
			failAt:    "/Startup/Configuration",
			wantError: "failed to set configuration",
		},
		{
			name:      "user step fails",
			failAt:    "/Startup/User",
			wantError: "failed to create admin user",
		},
		{
			name:      "remote access step fails",
			failAt:    "/Startup/RemoteAccess",
			wantError: "failed to enable remote access",
		},
		{
			name:      "complete step fails",
			failAt:    "/Startup/Complete",
			wantError: "failed to complete wizard",
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
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			c := &Configurator{Port: 8096, baseURL: server.URL}
			err := c.completeSetupWizard(context.Background())

			if err == nil {
				t.Error("expected error, got nil")
				return
			}
			if !contains(err.Error(), tt.wantError) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantError)
			}
		})
	}
}

func TestConfigurator_PostStart(t *testing.T) {
	tests := []struct {
		name            string
		wizardCompleted bool
		wantWizardCalls bool
	}{
		{
			name:            "wizard already completed",
			wizardCompleted: true,
			wantWizardCalls: false,
		},
		{
			name:            "wizard not completed",
			wizardCompleted: false,
			wantWizardCalls: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wizardCalls int

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/System/Info/Public":
					json.NewEncoder(w).Encode(map[string]bool{
						"StartupWizardCompleted": tt.wizardCompleted,
					})
				default:
					// Count wizard setup calls
					wizardCalls++
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			c := &Configurator{Port: 8096, baseURL: server.URL}
			state := &configurator.AppState{
				Name:          "jellyfin",
				DataPath:      t.TempDir(),
				BloudDataPath: t.TempDir(),
			}

			err := c.PostStart(context.Background(), state)
			if err != nil {
				t.Fatalf("PostStart() error = %v", err)
			}

			if tt.wantWizardCalls && wizardCalls == 0 {
				t.Error("expected wizard setup calls, got none")
			}
			if !tt.wantWizardCalls && wizardCalls > 0 {
				t.Errorf("expected no wizard setup calls, got %d", wizardCalls)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	if start+len(substr) > len(s) {
		return false
	}
	if s[start:start+len(substr)] == substr {
		return true
	}
	return containsAt(s, substr, start+1)
}
