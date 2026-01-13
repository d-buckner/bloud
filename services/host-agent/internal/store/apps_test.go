package store

import (
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppStore_Install(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewAppStore(db)

	mock.ExpectExec(`INSERT INTO apps`).
		WithArgs("radarr", "Radarr", "5.0.0", sql.NullInt64{Int64: 7878, Valid: true}, false, `{"downloadClient":"qbittorrent"}`).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = store.Install("radarr", "Radarr", "5.0.0", map[string]string{
		"downloadClient": "qbittorrent",
	}, &InstallOptions{Port: 7878})
	require.NoError(t, err)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppStore_Install_SystemApp(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewAppStore(db)

	mock.ExpectExec(`INSERT INTO apps`).
		WithArgs("postgres", "PostgreSQL", "16.0", sql.NullInt64{Int64: 5432, Valid: true}, true, `null`).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = store.Install("postgres", "PostgreSQL", "16.0", nil, &InstallOptions{
		Port:     5432,
		IsSystem: true,
	})
	require.NoError(t, err)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppStore_GetByName(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewAppStore(db)

	now := time.Now()
	rows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "version", "status", "port", "is_system", "integration_config", "installed_at", "updated_at",
	}).AddRow(1, "radarr", "Radarr", "5.0.0", "running", 7878, false, `{"downloadClient":"qbittorrent"}`, now, now)

	mock.ExpectQuery(`SELECT .+ FROM apps WHERE name = \$1`).
		WithArgs("radarr").
		WillReturnRows(rows)

	app, err := store.GetByName("radarr")
	require.NoError(t, err)
	require.NotNil(t, app)

	assert.Equal(t, "radarr", app.Name)
	assert.Equal(t, "Radarr", app.DisplayName)
	assert.Equal(t, "5.0.0", app.Version)
	assert.Equal(t, "running", app.Status)
	assert.Equal(t, 7878, app.Port)
	assert.False(t, app.IsSystem)
	assert.Equal(t, "qbittorrent", app.IntegrationConfig["downloadClient"])

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppStore_GetByName_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewAppStore(db)

	mock.ExpectQuery(`SELECT .+ FROM apps WHERE name = \$1`).
		WithArgs("nonexistent").
		WillReturnRows(sqlmock.NewRows([]string{}))

	app, err := store.GetByName("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, app)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppStore_GetInstalledNames(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewAppStore(db)

	rows := sqlmock.NewRows([]string{"name"}).
		AddRow("radarr").
		AddRow("sonarr").
		AddRow("qbittorrent")

	mock.ExpectQuery(`SELECT name FROM apps`).
		WillReturnRows(rows)

	names, err := store.GetInstalledNames()
	require.NoError(t, err)

	assert.Len(t, names, 3)
	assert.Contains(t, names, "radarr")
	assert.Contains(t, names, "sonarr")
	assert.Contains(t, names, "qbittorrent")

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppStore_UpdateStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewAppStore(db)

	mock.ExpectExec(`UPDATE apps SET status = \$1, updated_at = CURRENT_TIMESTAMP WHERE name = \$2`).
		WithArgs("running", "radarr").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = store.UpdateStatus("radarr", "running")
	require.NoError(t, err)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppStore_Uninstall(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewAppStore(db)

	mock.ExpectExec(`DELETE FROM apps WHERE name = \$1`).
		WithArgs("radarr").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = store.Uninstall("radarr")
	require.NoError(t, err)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppStore_IsInstalled(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewAppStore(db)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM apps WHERE name = \$1`).
		WithArgs("radarr").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	installed, err := store.IsInstalled("radarr")
	require.NoError(t, err)
	assert.True(t, installed)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppStore_IsInstalled_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewAppStore(db)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM apps WHERE name = \$1`).
		WithArgs("nonexistent").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	installed, err := store.IsInstalled("nonexistent")
	require.NoError(t, err)
	assert.False(t, installed)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppStore_UpdateIntegrationConfig(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewAppStore(db)

	mock.ExpectExec(`UPDATE apps SET integration_config = \$1, updated_at = CURRENT_TIMESTAMP WHERE name = \$2`).
		WithArgs(`{"downloadClient":"deluge"}`, "radarr").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = store.UpdateIntegrationConfig("radarr", map[string]string{
		"downloadClient": "deluge",
	})
	require.NoError(t, err)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppStore_EnsureSystemApp(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewAppStore(db)

	mock.ExpectExec(`INSERT INTO apps`).
		WithArgs("postgres", "PostgreSQL", 5432).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = store.EnsureSystemApp("postgres", "PostgreSQL", 5432)
	require.NoError(t, err)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppStore_GetAll(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	store := NewAppStore(db)

	now := time.Now()
	rows := sqlmock.NewRows([]string{
		"id", "name", "display_name", "version", "status", "port", "is_system", "integration_config", "installed_at", "updated_at",
	}).
		AddRow(1, "postgres", "PostgreSQL", "16.0", "running", 5432, true, `{}`, now, now).
		AddRow(2, "radarr", "Radarr", "5.0.0", "running", 7878, false, `{"downloadClient":"qbittorrent"}`, now, now)

	mock.ExpectQuery(`SELECT .+ FROM apps ORDER BY name`).
		WillReturnRows(rows)

	apps, err := store.GetAll()
	require.NoError(t, err)
	assert.Len(t, apps, 2)

	assert.Equal(t, "postgres", apps[0].Name)
	assert.True(t, apps[0].IsSystem)

	assert.Equal(t, "radarr", apps[1].Name)
	assert.Equal(t, "qbittorrent", apps[1].IntegrationConfig["downloadClient"])

	require.NoError(t, mock.ExpectationsWereMet())
}
