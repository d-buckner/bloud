// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// ExternalAppKind names the shape of an external app. A launcher plays no
// contract role; a provider plays one (a remote catalog app or a bare
// contract). The two values are the only kinds the entity supports today.
type ExternalAppKind string

const (
	// ExternalAppKindLauncher is a tile that opens a URL and wires to nothing.
	ExternalAppKindLauncher ExternalAppKind = "launcher"
	// ExternalAppKindProvider plays a provider role in the integration graph.
	ExternalAppKindProvider ExternalAppKind = "provider"
)

// ExternalApp is an operator-declared pointer to something Bloud does not run:
// a launcher tile, a remote install of a catalog app, or an off-host provider.
// Credentials are deliberately absent: they live in the secrets manager under
// an external/<id> scope, never in this table.
type ExternalApp struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Source is empty for launchers. A provider names what it satisfies:
	// "app:<catalogID>" or "contract:<name>".
	Source string `json:"source"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Icon   string `json:"icon"`
	// Values is a JSON object of non-secret per-source fields (contract
	// values). It is "{}" for launchers.
	Values    string `json:"values"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ExternalAppStoreInterface is the read/write surface for external apps.
type ExternalAppStoreInterface interface {
	GetAll() ([]*ExternalApp, error)
	Get(id string) (*ExternalApp, error)
	Upsert(app *ExternalApp) error
	Delete(id string) error
	SetOnChange(fn func())
}

// ExternalAppStore manages the external_apps table.
type ExternalAppStore struct {
	db       *sql.DB
	onChange func()
}

// NewExternalAppStore creates a new SQLite-backed external app store.
func NewExternalAppStore(db *sql.DB) *ExternalAppStore {
	return &ExternalAppStore{db: db}
}

// SetOnChange registers a callback fired after every write, so the SSE stream
// can resnapshot the home payload when a launcher is added or removed.
func (s *ExternalAppStore) SetOnChange(fn func()) {
	s.onChange = fn
}

func (s *ExternalAppStore) notify() {
	if s.onChange != nil {
		s.onChange()
	}
}

const externalAppColumns = "id, kind, source, name, url, icon, values_json, created_at, updated_at"

// GetAll returns every external app, ordered by name for a stable grid order.
func (s *ExternalAppStore) GetAll() ([]*ExternalApp, error) {
	rows, err := s.db.Query("SELECT " + externalAppColumns + " FROM external_apps ORDER BY name, id")
	if err != nil {
		return nil, fmt.Errorf("query external apps: %w", err)
	}
	defer func() { _ = rows.Close() }()

	apps := []*ExternalApp{}
	for rows.Next() {
		app, err := scanExternalApp(rows.Scan)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

// Get returns one external app by id, or nil when it does not exist.
func (s *ExternalAppStore) Get(id string) (*ExternalApp, error) {
	row := s.db.QueryRow("SELECT "+externalAppColumns+" FROM external_apps WHERE id = ?", id)
	app, err := scanExternalApp(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return app, nil
}

// Upsert inserts or replaces an external app row, preserving created_at on
// conflict. It is the single write path, so the submit-time record and the
// drain-phase apply are idempotent.
func (s *ExternalAppStore) Upsert(app *ExternalApp) error {
	values := app.Values
	if values == "" {
		values = "{}"
	}
	_, err := s.db.Exec(`
		INSERT INTO external_apps (id, kind, source, name, url, icon, values_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			kind = excluded.kind,
			source = excluded.source,
			name = excluded.name,
			url = excluded.url,
			icon = excluded.icon,
			values_json = excluded.values_json,
			updated_at = datetime('now')
	`, app.ID, app.Kind, app.Source, app.Name, app.URL, app.Icon, values)
	if err != nil {
		return fmt.Errorf("upsert external app: %w", err)
	}
	s.notify()
	return nil
}

// Delete removes one external app. Removing a row that does not exist is not
// an error: the desired end state is reached either way.
func (s *ExternalAppStore) Delete(id string) error {
	if _, err := s.db.Exec("DELETE FROM external_apps WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete external app: %w", err)
	}
	s.notify()
	return nil
}

// scanExternalApp reads one row through the scanner the caller supplies. Both
// *sql.Rows and *sql.Row satisfy the scan func, so the column order lives once.
func scanExternalApp(scan func(dest ...any) error) (*ExternalApp, error) {
	var app ExternalApp
	if err := scan(&app.ID, &app.Kind, &app.Source, &app.Name, &app.URL, &app.Icon, &app.Values, &app.CreatedAt, &app.UpdatedAt); err != nil {
		return nil, err
	}
	return &app, nil
}
