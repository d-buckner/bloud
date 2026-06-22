package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// RemoteApp represents an app on a remote host that this host can access
type RemoteApp struct {
	ID                 string   `json:"id"`
	HostLabel          string   `json:"host_label"`
	AppID              string   `json:"app_id"`
	AppName            string   `json:"app_name"`
	SSOStrategy        string   `json:"sso_strategy"`
	BypassPaths        []string `json:"bypass_paths"`
	SidecarTailnetAddr string   `json:"sidecar_tailnet_addr"`
	EncryptedCred      []byte   `json:"-"`
	Status             string   `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
}

// RemoteAppStore manages remote apps in the database
type RemoteAppStore struct {
	db *sql.DB
}

// NewRemoteAppStore creates a new remote app store
func NewRemoteAppStore(db *sql.DB) *RemoteAppStore {
	return &RemoteAppStore{db: db}
}

// Create inserts a new remote app
func (s *RemoteAppStore) Create(app RemoteApp) error {
	bypassJSON, err := json.Marshal(app.BypassPaths)
	if err != nil {
		return fmt.Errorf("failed to marshal bypass_paths: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO remote_apps (id, host_label, app_id, app_name, sso_strategy, bypass_paths, sidecar_tailnet_addr, encrypted_cred, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, app.ID, app.HostLabel, app.AppID, app.AppName, app.SSOStrategy, string(bypassJSON), app.SidecarTailnetAddr, app.EncryptedCred, app.Status)
	if err != nil {
		return fmt.Errorf("failed to insert remote app: %w", err)
	}
	return nil
}

// GetByID returns a remote app by ID, or (nil, nil) if not found
func (s *RemoteAppStore) GetByID(id string) (*RemoteApp, error) {
	row := s.db.QueryRow(`
		SELECT id, host_label, app_id, app_name, sso_strategy, bypass_paths, sidecar_tailnet_addr, encrypted_cred, status, created_at
		FROM remote_apps
		WHERE id = ?
	`, id)

	app, err := s.scanRemoteAppRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get remote app: %w", err)
	}
	return app, nil
}

// List returns all remote apps
func (s *RemoteAppStore) List() ([]*RemoteApp, error) {
	rows, err := s.db.Query(`
		SELECT id, host_label, app_id, app_name, sso_strategy, bypass_paths, sidecar_tailnet_addr, encrypted_cred, status, created_at
		FROM remote_apps
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query remote apps: %w", err)
	}
	defer rows.Close()

	apps := []*RemoteApp{}
	for rows.Next() {
		app, err := s.scanRemoteApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, nil
}

// SetCredential updates the encrypted credential and sets status to 'active'
func (s *RemoteAppStore) SetCredential(id string, encryptedCred []byte) error {
	result, err := s.db.Exec(`
		UPDATE remote_apps SET encrypted_cred = ?, status = 'active'
		WHERE id = ?
	`, encryptedCred, id)
	if err != nil {
		return fmt.Errorf("failed to set credential: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("remote app not found: %s", id)
	}
	return nil
}

// SetStatus updates the status of a remote app
func (s *RemoteAppStore) SetStatus(id, status string) error {
	result, err := s.db.Exec(`
		UPDATE remote_apps SET status = ?
		WHERE id = ?
	`, status, id)
	if err != nil {
		return fmt.Errorf("failed to set status: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("remote app not found: %s", id)
	}
	return nil
}

// Delete removes a remote app from the database
func (s *RemoteAppStore) Delete(id string) error {
	result, err := s.db.Exec("DELETE FROM remote_apps WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete remote app: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("remote app not found: %s", id)
	}
	return nil
}

func (s *RemoteAppStore) scanRemoteApp(rows *sql.Rows) (*RemoteApp, error) {
	var app RemoteApp
	var bypassJSON string
	var encryptedCred []byte
	var createdAt string

	err := rows.Scan(
		&app.ID,
		&app.HostLabel,
		&app.AppID,
		&app.AppName,
		&app.SSOStrategy,
		&bypassJSON,
		&app.SidecarTailnetAddr,
		&encryptedCred,
		&app.Status,
		&createdAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan remote app: %w", err)
	}

	app.CreatedAt = parseSQLiteTime(createdAt)
	app.EncryptedCred = encryptedCred

	if err := json.Unmarshal([]byte(bypassJSON), &app.BypassPaths); err != nil {
		return nil, fmt.Errorf("failed to unmarshal bypass_paths: %w", err)
	}

	return &app, nil
}

func (s *RemoteAppStore) scanRemoteAppRow(row *sql.Row) (*RemoteApp, error) {
	var app RemoteApp
	var bypassJSON string
	var encryptedCred []byte
	var createdAt string

	err := row.Scan(
		&app.ID,
		&app.HostLabel,
		&app.AppID,
		&app.AppName,
		&app.SSOStrategy,
		&bypassJSON,
		&app.SidecarTailnetAddr,
		&encryptedCred,
		&app.Status,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}

	app.CreatedAt = parseSQLiteTime(createdAt)
	app.EncryptedCred = encryptedCred

	if err := json.Unmarshal([]byte(bypassJSON), &app.BypassPaths); err != nil {
		return nil, fmt.Errorf("failed to unmarshal bypass_paths: %w", err)
	}

	return &app, nil
}
