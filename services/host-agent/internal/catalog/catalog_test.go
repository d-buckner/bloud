package catalog

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// setupTestDB creates a temporary SQLite database for testing
func setupTestDB(t *testing.T) *sql.DB {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	// Create catalog_cache table
	schema := `
		CREATE TABLE IF NOT EXISTS catalog_cache (
			name TEXT PRIMARY KEY,
			yaml_content TEXT NOT NULL,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	return db
}

// setupTestCatalog creates a temporary apps directory with test apps
func setupTestCatalog(t *testing.T) string {
	tmpDir := t.TempDir()

	// Create test app directory with metadata.yaml
	appDir := filepath.Join(tmpDir, "test-app")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatalf("failed to create app directory: %v", err)
	}

	// Create test app YAML
	testAppYAML := `name: test-app
displayName: Test App
description: A test application
category: testing
icon: https://example.com/icon.png
version: 1.0.0
dependencies:
  - authentik
resources:
  minRam: 128
  minDisk: 1
  gpu: false
sso:
  enabled: true
  protocol: oauth2
  blueprint: auto
defaultConfig:
  port: 9000
healthCheck:
  path: /health
  interval: 30
  timeout: 5
docs:
  homepage: https://example.com
  source: https://github.com/example/test-app
tags:
  - test
  - example
`

	appFile := filepath.Join(appDir, "metadata.yaml")
	if err := os.WriteFile(appFile, []byte(testAppYAML), 0644); err != nil {
		t.Fatalf("failed to write test app file: %v", err)
	}

	return tmpDir
}

func TestLoader_LoadAll(t *testing.T) {
	catalogDir := setupTestCatalog(t)
	loader := NewLoader(catalogDir)

	apps, err := loader.LoadAll()
	require.NoError(t, err, "LoadAll should not return error")
	require.Len(t, apps, 1, "should load exactly 1 app")

	testApp, exists := apps["test-app"]
	require.True(t, exists, "test-app should be in loaded apps")

	// Verify app fields
	assert.Equal(t, "Test App", testApp.DisplayName)
	assert.Equal(t, "testing", testApp.Category)
	assert.Equal(t, "1.0.0", testApp.Version)
	assert.Equal(t, []string{"authentik"}, testApp.Dependencies)
}

func TestCache_RefreshAndGetAll(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	catalogDir := setupTestCatalog(t)
	loader := NewLoader(catalogDir)
	cache := NewCache(db)

	// Test refresh
	err := cache.Refresh(loader)
	require.NoError(t, err, "Refresh should not return error")

	// Test GetAll
	apps, err := cache.GetAll()
	require.NoError(t, err, "GetAll should not return error")
	require.Len(t, apps, 1, "should have exactly 1 app in cache")
	assert.Equal(t, "test-app", apps[0].Name)
}

func TestCache_Get(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	catalogDir := setupTestCatalog(t)
	loader := NewLoader(catalogDir)
	cache := NewCache(db)

	// Refresh cache first
	err := cache.Refresh(loader)
	require.NoError(t, err)

	// Test Get existing app
	app, err := cache.Get("test-app")
	require.NoError(t, err, "Get should return existing app without error")
	assert.Equal(t, "test-app", app.Name)

	// Test Get non-existent app
	_, err = cache.Get("non-existent")
	assert.Error(t, err, "Get should return error for non-existent app")
}

func TestCache_MultipleRefresh(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	catalogDir := setupTestCatalog(t)
	loader := NewLoader(catalogDir)
	cache := NewCache(db)

	// First refresh
	err := cache.Refresh(loader)
	require.NoError(t, err, "first refresh should succeed")

	// Second refresh (should clear and reload)
	err = cache.Refresh(loader)
	require.NoError(t, err, "second refresh should succeed")

	// Verify still only 1 app
	apps, err := cache.GetAll()
	require.NoError(t, err)
	assert.Len(t, apps, 1, "should still have exactly 1 app after multiple refreshes")
}

func TestLoader_ValidateApp(t *testing.T) {
	loader := NewLoader("")

	tests := []struct {
		name    string
		app     *App
		wantErr bool
	}{
		{
			name: "valid app",
			app: &App{
				Name:        "valid",
				DisplayName: "Valid App",
				Description: "A valid app",
				Category:    "test",
			},
			wantErr: false,
		},
		{
			name: "missing name",
			app: &App{
				DisplayName: "Missing Name",
				Description: "Missing name field",
				Category:    "test",
			},
			wantErr: true,
		},
		{
			name: "missing displayName",
			app: &App{
				Name:        "test",
				Description: "Missing displayName",
				Category:    "test",
			},
			wantErr: true,
		},
		{
			name: "missing category",
			app: &App{
				Name:        "test",
				DisplayName: "Test",
				Description: "Missing category",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loader.validateApp(tt.app)
			if tt.wantErr {
				assert.Error(t, err, "validateApp should return error")
			} else {
				assert.NoError(t, err, "validateApp should not return error")
			}
		})
	}
}

// setupTestGraphCatalog creates a catalog with AppDefinition format
func setupTestGraphCatalog(t *testing.T) string {
	tmpDir := t.TempDir()

	apps := map[string]string{
		"qbittorrent": `name: qbittorrent
image: lscr.io/linuxserver/qbittorrent:latest
integrations: {}
`,
		"radarr": `name: radarr
image: lscr.io/linuxserver/radarr:latest
integrations:
  downloadClient:
    required: true
    multi: false
    compatible:
      - app: qbittorrent
        default: true
      - app: deluge
`,
		"jellyseerr": `name: jellyseerr
image: fallenbagel/jellyseerr:latest
integrations:
  pvr:
    required: true
    multi: true
    compatible:
      - app: radarr
        category: movies
      - app: sonarr
        category: tv
`,
	}

	for appName, content := range apps {
		appDir := filepath.Join(tmpDir, appName)
		if err := os.MkdirAll(appDir, 0755); err != nil {
			t.Fatalf("failed to create %s directory: %v", appName, err)
		}
		path := filepath.Join(appDir, "metadata.yaml")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", appName, err)
		}
	}

	return tmpDir
}

func TestLoader_LoadGraph(t *testing.T) {
	catalogDir := setupTestGraphCatalog(t)
	loader := NewLoader(catalogDir)

	graph, err := loader.LoadGraph()
	require.NoError(t, err, "LoadGraph should not return error")

	// Verify apps loaded
	assert.Len(t, graph.Apps, 3, "should load 3 apps")
	assert.Contains(t, graph.Apps, "qbittorrent")
	assert.Contains(t, graph.Apps, "radarr")
	assert.Contains(t, graph.Apps, "jellyseerr")

	// Verify radarr has correct integrations
	radarr := graph.Apps["radarr"]
	require.NotNil(t, radarr.Integrations["downloadClient"])
	assert.True(t, radarr.Integrations["downloadClient"].Required)
	assert.Len(t, radarr.Integrations["downloadClient"].Compatible, 2)

	// Verify dependents index built correctly
	// qbittorrent should have radarr as dependent
	graph.SetInstalled([]string{"radarr"})
	deps := graph.FindDependents("qbittorrent")
	assert.Len(t, deps, 1)
	assert.Equal(t, "radarr", deps[0].Target)
}

func TestLoader_LoadGraph_WithRealCatalog(t *testing.T) {
	// Test with actual apps directory (integration test)
	// Try multiple possible paths
	possiblePaths := []string{
		"../../../../apps",        // from internal/catalog
		"../../apps",              // from deeper test runs
		"apps",                    // from project root
	}

	var appsDir string
	for _, p := range possiblePaths {
		// Check if path exists and has at least one app directory with metadata.yaml
		if entries, err := os.ReadDir(p); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					if _, err := os.Stat(filepath.Join(p, entry.Name(), "metadata.yaml")); err == nil {
						appsDir = p
						break
					}
				}
			}
		}
		if appsDir != "" {
			break
		}
	}

	if appsDir == "" {
		t.Skip("apps directory not found, skipping integration test")
	}

	loader := NewLoader(appsDir)
	graph, err := loader.LoadGraph()
	require.NoError(t, err, "LoadGraph should work with real apps directory")

	// Should have loaded our app definitions (currently 6 implemented apps)
	assert.Greater(t, len(graph.Apps), 0, "should load at least one app")

	// Verify we loaded some of the expected implemented apps
	expectedApps := []string{"postgres", "traefik", "miniflux", "authentik"}
	foundCount := 0
	for _, name := range expectedApps {
		if _, ok := graph.Apps[name]; ok {
			foundCount++
		}
	}
	assert.Greater(t, foundCount, 0, "should find at least one expected app")

	// Test miniflux -> postgres integration if both exist
	if miniflux, ok := graph.Apps["miniflux"]; ok {
		if db, hasDB := miniflux.Integrations["database"]; hasDB {
			assert.True(t, db.Required, "miniflux database should be required")
			var foundPostgres bool
			for _, compat := range db.Compatible {
				if compat.App == "postgres" {
					foundPostgres = true
					assert.True(t, compat.Default, "postgres should be default")
				}
			}
			assert.True(t, foundPostgres, "postgres should be in compatible list")
		}
	}
}
