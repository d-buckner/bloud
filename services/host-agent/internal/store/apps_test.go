// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package store

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppStore_Install(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewAppStore(db)

	err := store.Install("radarr", "Radarr", "5.0.0", map[string]string{
		"downloadClient": "qbittorrent",
	}, &InstallOptions{Port: 7878})
	require.NoError(t, err)

	app, err := store.GetByCatalogID("radarr")
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.Equal(t, "radarr", app.CatalogID)
	assert.Equal(t, "Radarr", app.DisplayName)
	assert.Equal(t, "5.0.0", app.Version)
	assert.Equal(t, "installing", app.Status)
	assert.Equal(t, 7878, app.Port)
	assert.False(t, app.IsSystem)
	assert.Equal(t, "qbittorrent", app.IntegrationConfig["downloadClient"])
}

func TestAppStore_Install_SystemApp(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewAppStore(db)

	err := store.Install("postgres", "PostgreSQL", "16.0", nil, &InstallOptions{
		Port:     5432,
		IsSystem: true,
	})
	require.NoError(t, err)

	app, err := store.GetByCatalogID("postgres")
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.True(t, app.IsSystem)
	assert.Equal(t, 5432, app.Port)
}

func TestAppStore_SetLastError(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewAppStore(db)

	err := store.Install("radarr", "Radarr", "5.0.0", nil, &InstallOptions{Port: 7878})
	require.NoError(t, err)

	// New installs start without an error.
	app, err := store.GetByCatalogID("radarr")
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.Empty(t, app.LastError)

	// Record a failure, then read it back via both accessors.
	require.NoError(t, store.SetLastError("radarr", "image pull timed out"))
	app, err = store.GetByCatalogID("radarr")
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.Equal(t, "image pull timed out", app.LastError)

	apps, err := store.GetAll()
	require.NoError(t, err)
	require.Len(t, apps, 1)
	assert.Equal(t, "image pull timed out", apps[0].LastError)

	// Reinstall clears a previous error.
	require.NoError(t, store.Install("radarr", "Radarr", "5.0.1", nil, &InstallOptions{Port: 7878}))
	app, err = store.GetByCatalogID("radarr")
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.Empty(t, app.LastError)

	// Clearing the error with an empty string works too.
	require.NoError(t, store.SetLastError("radarr", "boom"))
	require.NoError(t, store.SetLastError("radarr", ""))
	app, err = store.GetByCatalogID("radarr")
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.Empty(t, app.LastError)

	// Unknown app is an error.
	err = store.SetLastError("nope", "x")
	require.Error(t, err)
}

func TestAppStore_GetByCatalogID(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewAppStore(db)

	// Install an app first
	err := store.Install("radarr", "Radarr", "5.0.0", map[string]string{
		"downloadClient": "qbittorrent",
	}, &InstallOptions{Port: 7878})
	require.NoError(t, err)
	require.NoError(t, store.UpdateStatus("radarr", "running"))

	app, err := store.GetByCatalogID("radarr")
	require.NoError(t, err)
	require.NotNil(t, app)

	assert.Equal(t, "radarr", app.CatalogID)
	assert.Equal(t, "Radarr", app.DisplayName)
	assert.Equal(t, "5.0.0", app.Version)
	assert.Equal(t, "running", app.Status)
	assert.Equal(t, 7878, app.Port)
	assert.False(t, app.IsSystem)
	assert.Equal(t, "qbittorrent", app.IntegrationConfig["downloadClient"])
}

func TestAppStore_GetByCatalogID_NotFound(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewAppStore(db)

	app, err := store.GetByCatalogID("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, app)
}

func TestAppStore_GetInstalledCatalogIDs(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewAppStore(db)

	require.NoError(t, store.Install("radarr", "Radarr", "", nil, nil))
	require.NoError(t, store.Install("sonarr", "Sonarr", "", nil, nil))
	require.NoError(t, store.Install("qbittorrent", "qBittorrent", "", nil, nil))

	ids, err := store.GetInstalledCatalogIDs()
	require.NoError(t, err)

	assert.Len(t, ids, 3)
	assert.Contains(t, ids, "radarr")
	assert.Contains(t, ids, "sonarr")
	assert.Contains(t, ids, "qbittorrent")
}

func TestAppStore_UpdateStatus(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewAppStore(db)

	require.NoError(t, store.Install("radarr", "Radarr", "", nil, nil))
	require.NoError(t, store.UpdateStatus("radarr", "running"))

	app, err := store.GetByCatalogID("radarr")
	require.NoError(t, err)
	assert.Equal(t, "running", app.Status)
}

func TestAppStore_Uninstall(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewAppStore(db)

	require.NoError(t, store.Install("radarr", "Radarr", "", nil, nil))
	require.NoError(t, store.Uninstall("radarr"))

	app, err := store.GetByCatalogID("radarr")
	require.NoError(t, err)
	assert.Nil(t, app)
}

func TestAppStore_IsInstalled(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewAppStore(db)

	require.NoError(t, store.Install("radarr", "Radarr", "", nil, nil))

	installed, err := store.IsInstalled("radarr")
	require.NoError(t, err)
	assert.True(t, installed)
}

func TestAppStore_IsInstalled_NotFound(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewAppStore(db)

	installed, err := store.IsInstalled("nonexistent")
	require.NoError(t, err)
	assert.False(t, installed)
}

func TestAppStore_UpdateIntegrationConfig(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewAppStore(db)

	require.NoError(t, store.Install("radarr", "Radarr", "", map[string]string{
		"downloadClient": "qbittorrent",
	}, nil))

	require.NoError(t, store.UpdateIntegrationConfig("radarr", map[string]string{
		"downloadClient": "deluge",
	}))

	app, err := store.GetByCatalogID("radarr")
	require.NoError(t, err)
	assert.Equal(t, "deluge", app.IntegrationConfig["downloadClient"])
}

func TestAppStore_EnsureSystemApp(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewAppStore(db)

	require.NoError(t, store.EnsureSystemApp("postgres", "PostgreSQL", 5432))

	app, err := store.GetByCatalogID("postgres")
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.Equal(t, "running", app.Status)
	assert.True(t, app.IsSystem)
	assert.Equal(t, 5432, app.Port)
}

func TestAppStore_GetAll(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewAppStore(db)

	require.NoError(t, store.EnsureSystemApp("postgres", "PostgreSQL", 5432))
	require.NoError(t, store.Install("radarr", "Radarr", "5.0.0", map[string]string{
		"downloadClient": "qbittorrent",
	}, &InstallOptions{Port: 7878}))

	apps, err := store.GetAll()
	require.NoError(t, err)
	assert.Len(t, apps, 2)

	assert.Equal(t, "postgres", apps[0].CatalogID)
	assert.True(t, apps[0].IsSystem)

	assert.Equal(t, "radarr", apps[1].CatalogID)
	assert.Equal(t, "qbittorrent", apps[1].IntegrationConfig["downloadClient"])
}
