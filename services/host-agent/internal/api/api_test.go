package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"log/slog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// setupTestServer creates a test server with in-memory database and test catalog
func setupTestServer(t *testing.T) (*Server, string) {
	// Create temp directory for test apps
	tmpDir := t.TempDir()

	// Create test app directory with metadata.yaml
	testAppDir := filepath.Join(tmpDir, "test-app")
	if err := os.MkdirAll(testAppDir, 0755); err != nil {
		t.Fatalf("failed to create test app directory: %v", err)
	}

	// Create test app YAML
	testAppYAML := `name: test-app
displayName: Test App
description: A test application
category: testing
version: 1.0.0
dependencies: []
resources:
  minRam: 128
  minDisk: 1
  gpu: false
sso:
  enabled: false
  protocol: ""
  blueprint: ""
defaultConfig: {}
healthCheck:
  path: /health
  interval: 30
  timeout: 5
docs:
  homepage: https://example.com
  source: https://github.com/example/test-app
tags:
  - test
`

	appFile := filepath.Join(testAppDir, "metadata.yaml")
	if err := os.WriteFile(appFile, []byte(testAppYAML), 0644); err != nil {
		t.Fatalf("failed to write test app file: %v", err)
	}

	// Create file-based database in temp dir (automatically cleaned up by test)
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite", dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	// Enable WAL mode for better concurrent access handling
	db.Exec("PRAGMA journal_mode=WAL")

	// Create schema
	schema := `
		CREATE TABLE IF NOT EXISTS apps (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL,
			version TEXT,
			status TEXT NOT NULL DEFAULT 'stopped',
			port INTEGER,
			is_system INTEGER NOT NULL DEFAULT 0,
			integration_config TEXT,
			installed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS catalog_cache (
			name TEXT PRIMARY KEY,
			yaml_content TEXT NOT NULL,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	// Create logger that doesn't output during tests
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError, // Only show errors during tests
	}))

	// Create server
	nixConfigDir := filepath.Join(tmpDir, "nix")
	server := NewServer(db, ServerConfig{
		AppsDir:      tmpDir,
		ConfigDir: nixConfigDir,
		Port:         8080,
	}, logger)

	return server, tmpDir
}

func TestAPI_Health(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)

	assert.Equal(t, "ok", response["status"])
}

func TestAPI_ListApps(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/apps", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)

	apps, ok := response["apps"].([]interface{})
	require.True(t, ok, "response should contain apps array")
	require.Len(t, apps, 1, "should have exactly 1 app")

	// Check first app
	app := apps[0].(map[string]interface{})
	assert.Equal(t, "test-app", app["name"])
	assert.Equal(t, "Test App", app["displayName"])
}

func TestAPI_ListInstalledApps_Empty(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/apps/installed", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var apps []interface{}
	err := json.NewDecoder(w.Body).Decode(&apps)
	require.NoError(t, err)

	assert.Empty(t, apps, "should have 0 installed apps")
}

func TestAPI_SystemStatus(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/system/status", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var stats map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&stats)
	require.NoError(t, err)

	// Check that required fields exist
	assert.Contains(t, stats, "cpu", "response should contain cpu field")
	assert.Contains(t, stats, "memory", "response should contain memory field")
	assert.Contains(t, stats, "disk", "response should contain disk field")
}

func TestAPI_RefreshCatalog(t *testing.T) {
	server, appsDir := setupTestServer(t)

	// Add another app to catalog
	newAppDir := filepath.Join(appsDir, "new-app")
	require.NoError(t, os.MkdirAll(newAppDir, 0755))

	newAppYAML := `name: new-app
displayName: New App
description: A newly added app
category: testing
version: 2.0.0
dependencies: []
resources:
  minRam: 256
  minDisk: 2
  gpu: false
sso:
  enabled: false
  protocol: ""
  blueprint: ""
defaultConfig: {}
healthCheck:
  path: /
  interval: 30
  timeout: 5
docs:
  homepage: https://example.com
  source: https://github.com/example/new-app
tags:
  - new
`
	newAppFile := filepath.Join(newAppDir, "metadata.yaml")
	err := os.WriteFile(newAppFile, []byte(newAppYAML), 0644)
	require.NoError(t, err, "should be able to write new app file")

	// Refresh catalog
	req := httptest.NewRequest("POST", "/api/apps/refresh-catalog", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify new app is in catalog
	req = httptest.NewRequest("GET", "/api/apps", nil)
	w = httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	var response map[string]interface{}
	err = json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)

	apps, ok := response["apps"].([]interface{})
	require.True(t, ok, "response should contain apps array")
	assert.Len(t, apps, 2, "should have 2 apps after refresh")
}

// setupTestServerWithGraph creates a server with apps that have integrations
func setupTestServerWithGraph(t *testing.T) *Server {
	tmpDir := t.TempDir()

	// Create apps with integrations (each in its own directory)
	apps := map[string]string{
		"qbittorrent": `name: qbittorrent
displayName: qBittorrent
description: Torrent download client
category: downloads
image: qbittorrent:latest
integrations: {}
`,
		"radarr": `name: radarr
displayName: Radarr
description: Movie collection manager
category: media
image: radarr:latest
integrations:
  downloadClient:
    required: true
    multi: false
    compatible:
      - app: qbittorrent
        default: true
`,
		"jellyseerr": `name: jellyseerr
displayName: Jellyseerr
description: Request management and media discovery tool
category: media
image: jellyseerr:latest
integrations:
  pvr:
    required: true
    multi: true
    compatible:
      - app: radarr
        category: movies
`,
	}

	for appName, content := range apps {
		appDir := filepath.Join(tmpDir, appName)
		require.NoError(t, os.MkdirAll(appDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(appDir, "metadata.yaml"), []byte(content), 0644))
	}

	// Use file-based db in temp dir (automatically cleaned up by test)
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite", dbPath+"?_busy_timeout=5000")
	require.NoError(t, err)
	// Enable WAL mode for better concurrent access handling
	db.Exec("PRAGMA journal_mode=WAL")

	schema := `
		CREATE TABLE IF NOT EXISTS apps (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL,
			version TEXT,
			status TEXT NOT NULL DEFAULT 'stopped',
			port INTEGER,
			is_system INTEGER NOT NULL DEFAULT 0,
			integration_config TEXT,
			installed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS catalog_cache (
			name TEXT PRIMARY KEY,
			yaml_content TEXT NOT NULL,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
	`
	_, err = db.Exec(schema)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	nixConfigDir := filepath.Join(tmpDir, "nix")

	return NewServer(db, ServerConfig{
		AppsDir:      tmpDir,
		ConfigDir: nixConfigDir,
		Port:         8080,
	}, logger)
}

func TestAPI_PlanInstall(t *testing.T) {
	server := setupTestServerWithGraph(t)

	req := httptest.NewRequest("GET", "/api/apps/radarr/plan-install", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var plan map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&plan)
	require.NoError(t, err)

	assert.Equal(t, "radarr", plan["app"])
	assert.Equal(t, true, plan["canInstall"])

	// Should have a choice for downloadClient (nothing installed yet)
	choices := plan["choices"].([]interface{})
	assert.Len(t, choices, 1)

	choice := choices[0].(map[string]interface{})
	assert.Equal(t, "downloadClient", choice["integration"])
	assert.Equal(t, "qbittorrent", choice["recommended"])
}

func TestAPI_PlanInstall_WithDependencyInstalled(t *testing.T) {
	server := setupTestServerWithGraph(t)

	// Mark qbittorrent as installed
	_, err := server.db.Exec(`INSERT INTO apps (name, display_name, status) VALUES ('qbittorrent', 'qBittorrent', 'running')`)
	require.NoError(t, err)

	// Refresh to sync installed state
	server.syncInstalledState()

	req := httptest.NewRequest("GET", "/api/apps/radarr/plan-install", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var plan map[string]interface{}
	err = json.NewDecoder(w.Body).Decode(&plan)
	require.NoError(t, err)

	// Should have no choices (qbittorrent auto-selected)
	choices := plan["choices"].([]interface{})
	assert.Len(t, choices, 0)

	// Should have auto-config for qbittorrent
	autoConfig := plan["autoConfig"].([]interface{})
	assert.Len(t, autoConfig, 1)
	assert.Equal(t, "qbittorrent", autoConfig[0].(map[string]interface{})["source"])
}

func TestAPI_PlanRemove_Blocked(t *testing.T) {
	server := setupTestServerWithGraph(t)

	// Install qbittorrent and radarr
	_, err := server.db.Exec(`INSERT INTO apps (name, display_name, status) VALUES ('qbittorrent', 'qBittorrent', 'running')`)
	require.NoError(t, err)
	_, err = server.db.Exec(`INSERT INTO apps (name, display_name, status) VALUES ('radarr', 'Radarr', 'running')`)
	require.NoError(t, err)

	server.syncInstalledState()

	// Try to remove qbittorrent (radarr depends on it)
	req := httptest.NewRequest("GET", "/api/apps/qbittorrent/plan-remove", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var plan map[string]interface{}
	err = json.NewDecoder(w.Body).Decode(&plan)
	require.NoError(t, err)

	assert.Equal(t, false, plan["canRemove"])
	blockers := plan["blockers"].([]interface{})
	assert.Len(t, blockers, 1)
}

func TestAPI_PlanInstall_NotFound(t *testing.T) {
	server := setupTestServerWithGraph(t)

	req := httptest.NewRequest("GET", "/api/apps/nonexistent/plan-install", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPI_PlanRemove_Allowed(t *testing.T) {
	server := setupTestServerWithGraph(t)

	// Install only radarr (no dependents)
	_, err := server.db.Exec(`INSERT INTO apps (name, display_name, status) VALUES ('radarr', 'Radarr', 'running')`)
	require.NoError(t, err)

	server.syncInstalledState()

	// Try to remove radarr (no dependents, should be allowed)
	req := httptest.NewRequest("GET", "/api/apps/radarr/plan-remove", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var plan map[string]interface{}
	err = json.NewDecoder(w.Body).Decode(&plan)
	require.NoError(t, err)

	assert.Equal(t, true, plan["canRemove"])
}

func TestAPI_PlanRemove_NotFound(t *testing.T) {
	server := setupTestServerWithGraph(t)

	req := httptest.NewRequest("GET", "/api/apps/nonexistent/plan-remove", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPI_AppMetadata(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/apps/test-app/metadata", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var app map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&app)
	require.NoError(t, err)

	assert.Equal(t, "test-app", app["name"])
	assert.Equal(t, "Test App", app["displayName"])
	assert.Equal(t, "A test application", app["description"])
}

func TestAPI_AppMetadata_NotFound(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/apps/nonexistent/metadata", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPI_AppIcon(t *testing.T) {
	server, appsDir := setupTestServer(t)

	// Create an icon file
	iconPath := filepath.Join(appsDir, "test-app", "icon.png")
	iconData := []byte{0x89, 0x50, 0x4E, 0x47} // PNG magic bytes
	require.NoError(t, os.WriteFile(iconPath, iconData, 0644))

	req := httptest.NewRequest("GET", "/api/apps/test-app/icon", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Cache-Control"), "max-age")
}

func TestAPI_AppIcon_NotFound(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/apps/test-app/icon", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPI_Install_NoOrchestrator(t *testing.T) {
	server, _ := setupTestServer(t)
	server.orchestrator = nil // Ensure no orchestrator

	req := httptest.NewRequest("POST", "/api/apps/test-app/install", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAPI_Uninstall_NoOrchestrator(t *testing.T) {
	server, _ := setupTestServer(t)
	server.orchestrator = nil

	req := httptest.NewRequest("POST", "/api/apps/test-app/uninstall", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAPI_Rollback_NoNixOrchestrator(t *testing.T) {
	server, _ := setupTestServer(t)
	server.orchestrator = nil

	req := httptest.NewRequest("POST", "/api/system/rollback", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestAPI_ClearData_NotFound(t *testing.T) {
	server, _ := setupTestServer(t)

	req := httptest.NewRequest("POST", "/api/apps/nonexistent/clear-data", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPI_ClearData_OrphanedData(t *testing.T) {
	server, appsDir := setupTestServer(t)
	server.orchestrator = nil // No orchestrator needed for orphaned data cleanup

	// Create orphaned data directory
	dataDir := filepath.Join(appsDir, "data")
	appDataDir := filepath.Join(dataDir, "test-app")
	require.NoError(t, os.MkdirAll(appDataDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(appDataDir, "data.txt"), []byte("test"), 0644))

	server.dataDir = dataDir

	req := httptest.NewRequest("POST", "/api/apps/test-app/clear-data", nil)
	w := httptest.NewRecorder()

	server.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.Equal(t, "data cleared", response["status"])
}

// AppEventHub tests

func TestAppEventHub_Subscribe(t *testing.T) {
	server, _ := setupTestServer(t)

	ch := server.appHub.Subscribe()
	assert.NotNil(t, ch)
	assert.Equal(t, 1, server.appHub.SubscriberCount())

	server.appHub.Unsubscribe(ch)
	assert.Equal(t, 0, server.appHub.SubscriberCount())
}

func TestAppEventHub_MultipleSubscribers(t *testing.T) {
	server, _ := setupTestServer(t)

	ch1 := server.appHub.Subscribe()
	ch2 := server.appHub.Subscribe()
	ch3 := server.appHub.Subscribe()

	assert.Equal(t, 3, server.appHub.SubscriberCount())

	server.appHub.Unsubscribe(ch1)
	assert.Equal(t, 2, server.appHub.SubscriberCount())

	server.appHub.Unsubscribe(ch2)
	server.appHub.Unsubscribe(ch3)
	assert.Equal(t, 0, server.appHub.SubscriberCount())
}

func TestAppEventHub_Broadcast(t *testing.T) {
	server, _ := setupTestServer(t)

	// Add an app to the database (version must be non-null)
	_, err := server.db.Exec(`INSERT INTO apps (name, display_name, version, status) VALUES ('broadcast-app', 'Broadcast App', '1.0.0', 'running')`)
	require.NoError(t, err)

	ch := server.appHub.Subscribe()
	defer server.appHub.Unsubscribe(ch)

	// Broadcast
	server.appHub.Broadcast()

	// Should receive the app list
	select {
	case apps := <-ch:
		assert.Len(t, apps, 1)
		assert.Equal(t, "broadcast-app", apps[0].Name)
	default:
		t.Fatal("expected to receive broadcast")
	}
}

func TestAppEventHub_BroadcastChannelFull(t *testing.T) {
	server, _ := setupTestServer(t)

	ch := server.appHub.Subscribe()
	defer server.appHub.Unsubscribe(ch)

	// Fill the channel (buffer size is 10)
	for i := 0; i < 15; i++ {
		server.appHub.Broadcast()
	}

	// Should not panic - channel full is handled gracefully
	assert.Equal(t, 1, server.appHub.SubscriberCount())
}

// Utility function tests

func TestRespondJSON(t *testing.T) {
	w := httptest.NewRecorder()

	respondJSON(w, http.StatusCreated, map[string]string{"key": "value"})

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.Equal(t, "value", response["key"])
}

func TestRespondError(t *testing.T) {
	w := httptest.NewRecorder()

	respondError(w, http.StatusBadRequest, "something went wrong")

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response map[string]string
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.Equal(t, "something went wrong", response["error"])
}
