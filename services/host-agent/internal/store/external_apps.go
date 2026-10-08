// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// "app:<catalogID>" or "contract:<name>". Build and read it with
	// ExternalAppSourceForApp / ParseExternalAppSource rather than by
	// hand-concatenating, so the two halves cannot drift.
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

// ExternalAppSourceKind is the discriminator half of an external provider's
// source string: which way round the reference points.
type ExternalAppSourceKind string

const (
	// ExternalAppSourceKindApp names a catalog app: the external record is a
	// remote install of that app and satisfies every contract the app's
	// metadata `provides:`.
	ExternalAppSourceKindApp ExternalAppSourceKind = "app"
	// ExternalAppSourceKindContract names one contract directly, for an
	// off-host provider that has no catalog app behind it.
	ExternalAppSourceKindContract ExternalAppSourceKind = "contract"
)

// ExternalAppSourceForApp renders the source value for a remote install of the
// named catalog app.
func ExternalAppSourceForApp(catalogID string) string {
	return string(ExternalAppSourceKindApp) + ":" + catalogID
}

// ExternalAppSourceForContract renders the source value for a bare contract
// provider: an off-host thing that fills one named contract with no catalog
// app behind it.
//
// This is the runtime counterpart of a consumer's `compatible:
// [{source: setting}]` declaration. The consumer names the role; the record
// names the contract it fills, and the resolver matches them by that name.
func ExternalAppSourceForContract(contract string) string {
	return string(ExternalAppSourceKindContract) + ":" + contract
}

// ParseExternalAppSource splits a source value into its kind and reference.
// An empty source (a launcher) and any string without a non-empty reference on
// both sides report ok=false, so a malformed stored value reads as "not a
// provider" rather than as a provider with an empty name.
func ParseExternalAppSource(source string) (ExternalAppSourceKind, string, bool) {
	kind, ref, found := strings.Cut(strings.TrimSpace(source), ":")
	if !found {
		return "", "", false
	}
	switch ExternalAppSourceKind(kind) {
	case ExternalAppSourceKindApp, ExternalAppSourceKindContract:
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return "", "", false
		}
		return ExternalAppSourceKind(kind), ref, true
	default:
		return "", "", false
	}
}

// Value reads one operator-supplied, non-secret value out of the record's
// values JSON for one contract. A missing contract, a missing key, or a body
// that does not parse all read as empty: the caller cannot tell those apart and
// does not need to, because an absent value is what an unset field looks like
// everywhere else in a binding too.
func (a *ExternalApp) Value(contract, key string) string {
	if a == nil || a.Values == "" {
		return ""
	}
	var parsed map[string]map[string]string
	if err := json.Unmarshal([]byte(a.Values), &parsed); err != nil {
		return ""
	}
	return parsed[contract][key]
}

// ExternalSecretScope is the secrets-manager scope an external app's
// credentials live under. It is deliberately not the catalog app's own scope:
// a remote AFFiNE's password must not be readable by, or overwrite, the
// credentials a local AFFiNE install publishes for itself.
func ExternalSecretScope(id string) string {
	return "external/" + id
}

// ExternalAppStoreInterface is the read/write surface for external apps.
type ExternalAppStoreInterface interface {
	GetAll() ([]*ExternalApp, error)
	Get(id string) (*ExternalApp, error)
	FindBySource(source string) (*ExternalApp, error)
	FindAllBySource(source string) ([]*ExternalApp, error)
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

// FindBySource returns the first external app whose source matches exactly.
// The API keeps one record per source (a catalog ID is either run locally or
// pointed at externally, never both), so a first match is a unique match for
// every caller; ordering by id makes that deterministic.
func (s *ExternalAppStore) FindBySource(source string) (*ExternalApp, error) {
	if source == "" {
		return nil, nil
	}
	row := s.db.QueryRow("SELECT "+externalAppColumns+" FROM external_apps WHERE source = ? ORDER BY id", source)
	app, err := scanExternalApp(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return app, nil
}

// FindAllBySource returns every record carrying one exact source value.
//
// Where FindBySource assumes at most one record per source, this is the read
// for a contract that any number of off-host providers can fill: every AI
// upstream is a `contract:inference` record, and the Settings surface lists
// them all. Ordering by id keeps the list stable across saves so a row does not
// move under the operator.
func (s *ExternalAppStore) FindAllBySource(source string) ([]*ExternalApp, error) {
	if source == "" {
		return nil, nil
	}
	rows, err := s.db.Query("SELECT "+externalAppColumns+" FROM external_apps WHERE source = ? ORDER BY id", source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*ExternalApp
	for rows.Next() {
		app, err := scanExternalApp(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
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
