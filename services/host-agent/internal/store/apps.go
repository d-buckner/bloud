// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// InstalledApp represents an app installed on this host
type InstalledApp struct {
	ID          int       `json:"id"`
	CatalogID   string    `json:"catalog_id"`
	DisplayName string    `json:"display_name"`
	Version     string    `json:"version"`
	Status      AppStatus `json:"status"`
	LastError   string    `json:"last_error,omitempty"`
	// Operation is the current-or-last lifecycle drive for this app
	// (docs/plans/archive/operation-state-design.md). Read-side join; writes
	// belong to the orchestrator's operation recorder only.
	Operation         *Operation        `json:"operation,omitempty"`
	Port              int               `json:"port,omitempty"`
	IsSystem          bool              `json:"is_system"`
	IntegrationConfig map[string]string `json:"integration_config,omitempty"`
	InstalledAt       time.Time         `json:"installed_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// AppStore manages installed apps in the database
type AppStore struct {
	db       *sql.DB
	onChange func() // Called when app state changes
}

// NewAppStore creates a new app store
func NewAppStore(db *sql.DB) *AppStore {
	return &AppStore{db: db}
}

// SetOnChange sets a callback that fires when app state changes
func (s *AppStore) SetOnChange(fn func()) {
	s.onChange = fn
}

// notify calls the onChange callback if set
func (s *AppStore) notify() {
	if s.onChange != nil {
		s.onChange()
	}
}

func (s *AppStore) GetAll() ([]*InstalledApp, error) {
	rows, err := s.db.Query(`
		SELECT id, catalog_id, display_name, version, status, last_error, port, is_system, integration_config, installed_at, updated_at
		FROM apps
		ORDER BY catalog_id
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query apps: %w", err)
	}
	defer func() { _ = rows.Close() }()

	apps := []*InstalledApp{}
	for rows.Next() {
		app, err := s.scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to query apps: %w", err)
	}

	if err := s.attachOperations(apps); err != nil {
		return nil, err
	}
	return apps, nil
}

// GetByCatalogID returns an installed app by catalog ID
func (s *AppStore) GetByCatalogID(catalogID string) (*InstalledApp, error) {
	row := s.db.QueryRow(`
		SELECT id, catalog_id, display_name, version, status, last_error, port, is_system, integration_config, installed_at, updated_at
		FROM apps
		WHERE catalog_id = ?`, catalogID)

	app, err := s.scanAppRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get app: %w", err)
	}

	if err := s.attachOperations([]*InstalledApp{app}); err != nil {
		return nil, err
	}
	return app, nil
}

// GetInstalledCatalogIDs returns just the catalog IDs of installed apps
func (s *AppStore) GetInstalledCatalogIDs() ([]string, error) {
	rows, err := s.db.Query("SELECT catalog_id FROM apps ORDER BY catalog_id")
	if err != nil {
		return nil, fmt.Errorf("failed to query app catalog IDs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan catalog_id: %w", err)
		}
		ids = append(ids, id)
	}

	return ids, nil
}

// InstallOptions contains optional fields for app installation
type InstallOptions struct {
	Port     int
	IsSystem bool
}

// Install records a new app installation (or re-install)
func (s *AppStore) Install(catalogID, displayName, version string, integrationConfig map[string]string, opts *InstallOptions) error {
	configJSON, err := json.Marshal(integrationConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal integration config: %w", err)
	}

	var port sql.NullInt64
	var isSystem bool
	if opts != nil {
		if opts.Port > 0 {
			port = sql.NullInt64{Int64: int64(opts.Port), Valid: true}
		}
		isSystem = opts.IsSystem
	}

	_, err = s.db.Exec(`
		INSERT INTO apps (catalog_id, display_name, version, status, port, is_system, integration_config)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(catalog_id) DO UPDATE SET
			display_name = excluded.display_name,
			version = excluded.version,
			status = CASE WHEN apps.status = ? THEN ? ELSE ? END,
			last_error = '',
			port = excluded.port,
			is_system = excluded.is_system,
			integration_config = excluded.integration_config,
			updated_at = datetime('now')
	`, catalogID, displayName, version, AppStatusInstalling, port, isSystem, string(configJSON),
		AppStatusRunning, AppStatusRunning, AppStatusInstalling)
	if err != nil {
		return fmt.Errorf("failed to insert app: %w", err)
	}

	s.notify()
	return nil
}

// SetLastError records (or clears, with an empty string) the most recent
// lifecycle failure reason for an app. Only the orchestrator writes this.
func (s *AppStore) SetLastError(catalogID, lastError string) error {
	result, err := s.db.Exec(`
		UPDATE apps SET last_error = ?, updated_at = datetime('now')
		WHERE catalog_id = ?
	`, lastError, catalogID)
	if err != nil {
		return fmt.Errorf("failed to set app last_error: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("app not found: %s", catalogID)
	}

	s.notify()
	return nil
}

// UpdateStatus updates the status of an installed app
func (s *AppStore) UpdateStatus(catalogID string, status AppStatus) error {
	result, err := s.db.Exec(`
		UPDATE apps SET status = ?, updated_at = datetime('now')
		WHERE catalog_id = ?
	`, status, catalogID)
	if err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("app not found: %s", catalogID)
	}

	s.notify()
	return nil
}

// EnsureSystemApp ensures a system app (managed by the host agent, not user-installed) is registered
// System apps are marked with is_system=true and their status is set to "running"
// This is idempotent - it creates or updates the app entry
func (s *AppStore) EnsureSystemApp(catalogID, displayName string, port int) error {
	_, err := s.db.Exec(`
		INSERT INTO apps (catalog_id, display_name, version, status, port, is_system, integration_config)
		VALUES (?, ?, '', 'running', ?, 1, '{}')
		ON CONFLICT(catalog_id) DO UPDATE SET
			display_name = excluded.display_name,
			status = 'running',
			port = excluded.port,
			is_system = 1,
			updated_at = datetime('now')
	`, catalogID, displayName, port)
	if err != nil {
		return fmt.Errorf("failed to ensure system app: %w", err)
	}

	s.notify()
	return nil
}

// UpdateDisplayName updates the display name of an installed app
func (s *AppStore) UpdateDisplayName(catalogID, displayName string) error {
	result, err := s.db.Exec(`
		UPDATE apps SET display_name = ?, updated_at = datetime('now')
		WHERE catalog_id = ?
	`, displayName, catalogID)
	if err != nil {
		return fmt.Errorf("failed to update display name: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("app not found: %s", catalogID)
	}

	s.notify()
	return nil
}

// GetSSOStrategy returns the SSO strategy the app last converged to, or the
// empty string when none has been recorded yet.
func (s *AppStore) GetSSOStrategy(catalogID string) (string, error) {
	var strategy string
	err := s.db.QueryRow(`SELECT sso_strategy FROM apps WHERE catalog_id = ?`, catalogID).Scan(&strategy)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get sso strategy: %w", err)
	}
	return strategy, nil
}

// SetSSOStrategy records the SSO strategy the app just converged to.
func (s *AppStore) SetSSOStrategy(catalogID, strategy string) error {
	_, err := s.db.Exec(`
		UPDATE apps SET sso_strategy = ?, updated_at = datetime('now')
		WHERE catalog_id = ?
	`, strategy, catalogID)
	if err != nil {
		return fmt.Errorf("failed to set sso strategy: %w", err)
	}
	return nil
}

// UpdateIntegrationConfig updates the integration config for an app
func (s *AppStore) UpdateIntegrationConfig(catalogID string, config map[string]string) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	result, err := s.db.Exec(`
		UPDATE apps SET integration_config = ?, updated_at = datetime('now')
		WHERE catalog_id = ?
	`, string(configJSON), catalogID)
	if err != nil {
		return fmt.Errorf("failed to update integration config: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("app not found: %s", catalogID)
	}

	return nil
}

// Uninstall removes an app from the database
func (s *AppStore) Uninstall(catalogID string) error {
	result, err := s.db.Exec("DELETE FROM apps WHERE catalog_id = ?", catalogID)
	if err != nil {
		return fmt.Errorf("failed to delete app: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("app not found: %s", catalogID)
	}

	s.notify()
	return nil
}

// IsInstalled checks if an app is installed
func (s *AppStore) IsInstalled(catalogID string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM apps WHERE catalog_id = ?", catalogID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check if installed: %w", err)
	}
	return count > 0, nil
}

// parseSQLiteTime parses datetime strings produced by SQLite's datetime() function.
func parseSQLiteTime(s string) time.Time {
	for _, format := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	} {
		if t, err := time.Parse(format, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (s *AppStore) scanApp(rows *sql.Rows) (*InstalledApp, error) {
	app, err := s.scanAppColumns(rows.Scan)
	if err != nil {
		return nil, fmt.Errorf("failed to scan app: %w", err)
	}
	return app, nil
}

func (s *AppStore) scanAppRow(row *sql.Row) (*InstalledApp, error) {
	return s.scanAppColumns(row.Scan)
}

// scanAppColumns scans one apps row through the provided scanner and
// folds it into an InstalledApp. Both *sql.Rows and *sql.Row satisfy the
// scan func, so the column order and post-processing live exactly once:
// a schema change touches one Scan list, not two near-identical copies.
//
// The recorded operation is not part of this scan. It used to arrive on a
// LEFT JOIN against `operations`, which was sound only while that table
// held one row per app. It now holds one row per node, so a join would
// multiply app rows and pick a survivor at random; attachOperations
// folds the per-node rows into one projection by rule instead.
func (s *AppStore) scanAppColumns(scan func(dest ...any) error) (*InstalledApp, error) {
	var app InstalledApp
	var port sql.NullInt64
	var configJSON sql.NullString
	var installedAt, updatedAt string

	err := scan(
		&app.ID,
		&app.CatalogID,
		&app.DisplayName,
		&app.Version,
		&app.Status,
		&app.LastError,
		&port,
		&app.IsSystem,
		&configJSON,
		&installedAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}

	app.InstalledAt = parseSQLiteTime(installedAt)
	app.UpdatedAt = parseSQLiteTime(updatedAt)

	if port.Valid {
		app.Port = int(port.Int64)
	}

	if configJSON.Valid && configJSON.String != "" {
		if err := json.Unmarshal([]byte(configJSON.String), &app.IntegrationConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal integration config: %w", err)
		}
	}

	return &app, nil
}

// attachOperations folds each app's per-node operation rows into the one
// Operation the app reports, using the ranking OperationStore.Get
// documents. Apps with no recorded operation keep a nil Operation, which
// is how a reader tells "never driven" from "driven and finished".
//
// One query for the whole set, not one per app: GetAll runs on every
// dashboard read and every status poll.
func (s *AppStore) attachOperations(apps []*InstalledApp) error {
	wanted := make(map[string]*InstalledApp, len(apps))
	for _, app := range apps {
		if app != nil {
			wanted[app.CatalogID] = app
		}
	}
	if len(wanted) == 0 {
		return nil
	}

	rows, err := s.db.Query(`
		SELECT app_name, node, id, type, phase, status, retryable, cause, started_at, updated_at
		FROM operations`)
	if err != nil {
		return fmt.Errorf("failed to query operations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	best := map[string]*Operation{}
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return fmt.Errorf("failed to scan operation: %w", err)
		}
		if cur, ok := best[op.AppName]; !ok || ranksAbove(op, cur) {
			best[op.AppName] = op
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to query operations: %w", err)
	}

	for name, op := range best {
		if app, ok := wanted[name]; ok {
			app.Operation = op
		}
	}
	return nil
}
