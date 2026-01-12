package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)

	// Create apps table
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
	`
	_, err = db.Exec(schema)
	require.NoError(t, err)

	return db
}

func TestAppStore_Install(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store := NewAppStore(db)

	err := store.Install("radarr", "Radarr", "5.0.0", map[string]string{
		"downloadClient": "qbittorrent",
	}, &InstallOptions{Port: 7878})
	require.NoError(t, err)

	// Verify it's there
	app, err := store.GetByName("radarr")
	require.NoError(t, err)
	require.NotNil(t, app)

	assert.Equal(t, "radarr", app.Name)
	assert.Equal(t, "Radarr", app.DisplayName)
	assert.Equal(t, "5.0.0", app.Version)
	assert.Equal(t, "installing", app.Status)
	assert.Equal(t, 7878, app.Port)
	assert.False(t, app.IsSystem)
	assert.Equal(t, "qbittorrent", app.IntegrationConfig["downloadClient"])
}

func TestAppStore_GetInstalledNames(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store := NewAppStore(db)

	// Install some apps
	require.NoError(t, store.Install("radarr", "Radarr", "1.0", nil, nil))
	require.NoError(t, store.Install("sonarr", "Sonarr", "1.0", nil, nil))
	require.NoError(t, store.Install("qbittorrent", "qBittorrent", "1.0", nil, nil))

	names, err := store.GetInstalledNames()
	require.NoError(t, err)

	assert.Len(t, names, 3)
	assert.Contains(t, names, "radarr")
	assert.Contains(t, names, "sonarr")
	assert.Contains(t, names, "qbittorrent")
}

func TestAppStore_UpdateStatus(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store := NewAppStore(db)

	require.NoError(t, store.Install("radarr", "Radarr", "1.0", nil, nil))

	// Update status
	err := store.UpdateStatus("radarr", "running")
	require.NoError(t, err)

	app, err := store.GetByName("radarr")
	require.NoError(t, err)
	assert.Equal(t, "running", app.Status)
}

func TestAppStore_Uninstall(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store := NewAppStore(db)

	require.NoError(t, store.Install("radarr", "Radarr", "1.0", nil, nil))

	// Verify it exists
	installed, err := store.IsInstalled("radarr")
	require.NoError(t, err)
	assert.True(t, installed)

	// Uninstall
	err = store.Uninstall("radarr")
	require.NoError(t, err)

	// Verify it's gone
	installed, err = store.IsInstalled("radarr")
	require.NoError(t, err)
	assert.False(t, installed)
}

func TestAppStore_GetByName_NotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store := NewAppStore(db)

	app, err := store.GetByName("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, app)
}

func TestAppStore_UpdateIntegrationConfig(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store := NewAppStore(db)

	require.NoError(t, store.Install("radarr", "Radarr", "1.0", map[string]string{
		"downloadClient": "qbittorrent",
	}, nil))

	// Update to use deluge instead
	err := store.UpdateIntegrationConfig("radarr", map[string]string{
		"downloadClient": "deluge",
	})
	require.NoError(t, err)

	app, err := store.GetByName("radarr")
	require.NoError(t, err)
	assert.Equal(t, "deluge", app.IntegrationConfig["downloadClient"])
}

func TestAppStore_Install_SystemApp(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store := NewAppStore(db)

	err := store.Install("postgres", "PostgreSQL", "16.0", nil, &InstallOptions{
		Port:     5432,
		IsSystem: true,
	})
	require.NoError(t, err)

	app, err := store.GetByName("postgres")
	require.NoError(t, err)
	require.NotNil(t, app)

	assert.Equal(t, "postgres", app.Name)
	assert.Equal(t, 5432, app.Port)
	assert.True(t, app.IsSystem)
}
