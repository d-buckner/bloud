package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// InstalledApp represents an app installed on this host
type InstalledApp struct {
	ID                int               `json:"id"`
	Name              string            `json:"name"`
	DisplayName       string            `json:"display_name"`
	Version           string            `json:"version"`
	Status            string            `json:"status"`
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

// GetAll returns all installed apps
func (s *AppStore) GetAll() ([]*InstalledApp, error) {
	rows, err := s.db.Query(`
		SELECT id, name, display_name, version, status, port, is_system, integration_config, installed_at, updated_at
		FROM apps
		ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query apps: %w", err)
	}
	defer rows.Close()

	apps := []*InstalledApp{}
	for rows.Next() {
		app, err := s.scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}

	return apps, nil
}

// GetByName returns an installed app by name
func (s *AppStore) GetByName(name string) (*InstalledApp, error) {
	row := s.db.QueryRow(`
		SELECT id, name, display_name, version, status, port, is_system, integration_config, installed_at, updated_at
		FROM apps
		WHERE name = ?
	`, name)

	app, err := s.scanAppRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get app: %w", err)
	}

	return app, nil
}

// GetInstalledNames returns just the names of installed apps
func (s *AppStore) GetInstalledNames() ([]string, error) {
	rows, err := s.db.Query("SELECT name FROM apps ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("failed to query app names: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan name: %w", err)
		}
		names = append(names, name)
	}

	return names, nil
}

// InstallOptions contains optional fields for app installation
type InstallOptions struct {
	Port     int
	IsSystem bool
}

// Install records a new app installation (or re-install)
func (s *AppStore) Install(name, displayName, version string, integrationConfig map[string]string, opts *InstallOptions) error {
	configJSON, err := json.Marshal(integrationConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal integration config: %w", err)
	}

	var port sql.NullInt64
	var isSystem int
	if opts != nil {
		if opts.Port > 0 {
			port = sql.NullInt64{Int64: int64(opts.Port), Valid: true}
		}
		if opts.IsSystem {
			isSystem = 1
		}
	}

	_, err = s.db.Exec(`
		INSERT INTO apps (name, display_name, version, status, port, is_system, integration_config)
		VALUES (?, ?, ?, 'installing', ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			display_name = excluded.display_name,
			version = excluded.version,
			status = 'installing',
			port = excluded.port,
			is_system = excluded.is_system,
			integration_config = excluded.integration_config,
			updated_at = CURRENT_TIMESTAMP
	`, name, displayName, version, port, isSystem, string(configJSON))
	if err != nil {
		return fmt.Errorf("failed to insert app: %w", err)
	}

	s.notify()
	return nil
}

// UpdateStatus updates the status of an installed app
func (s *AppStore) UpdateStatus(name, status string) error {
	result, err := s.db.Exec(`
		UPDATE apps SET status = ?, updated_at = CURRENT_TIMESTAMP
		WHERE name = ?
	`, status, name)
	if err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("app not found: %s", name)
	}

	s.notify()
	return nil
}

// EnsureSystemApp ensures a system app (managed by NixOS, not user-installed) is registered
// System apps are marked with is_system=true and their status is set to "running"
// This is idempotent - it creates or updates the app entry
func (s *AppStore) EnsureSystemApp(name, displayName string, port int) error {
	_, err := s.db.Exec(`
		INSERT INTO apps (name, display_name, version, status, port, is_system, integration_config)
		VALUES (?, ?, '', 'running', ?, 1, '{}')
		ON CONFLICT(name) DO UPDATE SET
			display_name = excluded.display_name,
			status = 'running',
			port = excluded.port,
			is_system = 1,
			updated_at = CURRENT_TIMESTAMP
	`, name, displayName, port)
	if err != nil {
		return fmt.Errorf("failed to ensure system app: %w", err)
	}

	s.notify()
	return nil
}

// UpdateIntegrationConfig updates the integration config for an app
func (s *AppStore) UpdateIntegrationConfig(name string, config map[string]string) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	result, err := s.db.Exec(`
		UPDATE apps SET integration_config = ?, updated_at = CURRENT_TIMESTAMP
		WHERE name = ?
	`, string(configJSON), name)
	if err != nil {
		return fmt.Errorf("failed to update integration config: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("app not found: %s", name)
	}

	return nil
}

// Uninstall removes an app from the database
func (s *AppStore) Uninstall(name string) error {
	result, err := s.db.Exec("DELETE FROM apps WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("failed to delete app: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("app not found: %s", name)
	}

	s.notify()
	return nil
}

// IsInstalled checks if an app is installed
func (s *AppStore) IsInstalled(name string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM apps WHERE name = ?", name).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check if installed: %w", err)
	}
	return count > 0, nil
}

func (s *AppStore) scanApp(rows *sql.Rows) (*InstalledApp, error) {
	var app InstalledApp
	var port sql.NullInt64
	var isSystem int
	var configJSON sql.NullString

	err := rows.Scan(
		&app.ID,
		&app.Name,
		&app.DisplayName,
		&app.Version,
		&app.Status,
		&port,
		&isSystem,
		&configJSON,
		&app.InstalledAt,
		&app.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan app: %w", err)
	}

	if port.Valid {
		app.Port = int(port.Int64)
	}
	app.IsSystem = isSystem == 1

	if configJSON.Valid && configJSON.String != "" {
		if err := json.Unmarshal([]byte(configJSON.String), &app.IntegrationConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal integration config: %w", err)
		}
	}

	return &app, nil
}

func (s *AppStore) scanAppRow(row *sql.Row) (*InstalledApp, error) {
	var app InstalledApp
	var port sql.NullInt64
	var isSystem int
	var configJSON sql.NullString

	err := row.Scan(
		&app.ID,
		&app.Name,
		&app.DisplayName,
		&app.Version,
		&app.Status,
		&port,
		&isSystem,
		&configJSON,
		&app.InstalledAt,
		&app.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if port.Valid {
		app.Port = int(port.Int64)
	}
	app.IsSystem = isSystem == 1

	if configJSON.Valid && configJSON.String != "" {
		if err := json.Unmarshal([]byte(configJSON.String), &app.IntegrationConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal integration config: %w", err)
		}
	}

	return &app, nil
}
