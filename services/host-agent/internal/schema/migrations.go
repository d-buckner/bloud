// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package schema

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// Migration is one ordered, idempotent, error-checked schema change.
// The ledger records each applied version in schema_migrations, so an
// upgrade runs only the entries newer than the database's applied
// version. A failing migration aborts with a wrapped error and its
// version is NOT stamped, so the next boot retries it — the agent
// refuses to run on half-migrated durable state.
type Migration struct {
	Version int
	Name    string
	Up      func(tx *sql.Tx) error
}

// Migrations is the ordered upgrade ledger. Entry 1 is the full
// baseline DDL (schema.sql); later entries bring pre-ledger databases
// up to that baseline. Idempotency convention: every "legacy" entry
// is conditional (PRAGMA-guarded) so it is a verified no-op on
// databases where schema.sql already created the final shape, and a
// real change on older databases. When schema.sql gains a structural
// change, add the equivalent conditional entry here and let
// LatestVersion advance with it.
var Migrations = []Migration{
	{1, "baseline: full schema DDL", func(tx *sql.Tx) error {
		_, err := tx.Exec(SQL)
		return err
	}},
	{2, "legacy: apps.tailnet_id", ensureColumn("apps", "tailnet_id", "TEXT DEFAULT ''")},
	{3, "legacy: shares.node_share_link", ensureColumn("shares", "node_share_link", "TEXT NOT NULL DEFAULT ''")},
	{4, "legacy: shares.guest_label renamed to guest_id", renameColumn("shares", "guest_label", "guest_id")},
	{5, "legacy: apps.last_error", ensureColumn("apps", "last_error", "TEXT NOT NULL DEFAULT ''")},
	{6, "fix: user_app_positions shape fork", fixUserAppPositionsShape},
	{7, "operations: durable lifecycle operation state", func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS operations (
			app_name   TEXT PRIMARY KEY,
			id         TEXT NOT NULL,
			type       TEXT NOT NULL,
			phase      TEXT NOT NULL,
			status     TEXT NOT NULL,
			retryable  INTEGER NOT NULL DEFAULT 1,
			cause      TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`)
		return err
	}},
}

// LatestVersion is the schema version that schema.sql represents:
// the highest version in the ledger.
func LatestVersion() int {
	v := 0
	for _, m := range Migrations {
		if m.Version > v {
			v = m.Version
		}
	}
	return v
}

// Migrate upgrades db to LatestVersion. Databases with no ledger
// start at version 0 and walk every entry; each entry is a verified
// no-op if already satisfied. Safe to call on every startup.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	applied_at TEXT NOT NULL DEFAULT (datetime('now'))
)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	var applied int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&applied); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	for _, m := range Migrations {
		if m.Version <= applied {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return fmt.Errorf("migration %d (%s) failed: %w", m.Version, m.Name, err)
		}
	}
	return nil
}

// applyMigration runs one entry and stamps its version in the same
// transaction: either the change and its ledger row both land, or
// neither does.
func applyMigration(db *sql.DB, m Migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := m.Up(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, name) VALUES (?, ?)`, m.Version, m.Name); err != nil {
		return fmt.Errorf("stamp version: %w", err)
	}
	return tx.Commit()
}

// queryable is satisfied by both *sql.DB and *sql.Tx, so the schema
// probes work inside a migration and against a plain connection.
type queryable interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// ensureColumn returns a migration that adds a column only when the
// table lacks it (checked, not error-suppressed like the old
// duplicate-column tolerance).
func ensureColumn(table, column, definition string) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		ok, err := tableExists(tx, table)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("table %s not found", table)
		}
		has, err := columnExists(tx, table, column)
		if err != nil {
			return err
		}
		if has {
			return nil
		}
		_, err = tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
		return err
	}
}

// renameColumn returns a migration that renames oldName to newName when
// the old column is present and the new one is not.
func renameColumn(table, oldName, newName string) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		hasOld, err := columnExists(tx, table, oldName)
		if err != nil {
			return err
		}
		if !hasOld {
			return nil // already renamed (or table rebuilt in final shape)
		}
		hasNew, err := columnExists(tx, table, newName)
		if err != nil {
			return err
		}
		if hasNew {
			return fmt.Errorf("%s.%s and %s.%s both present; ambiguous state", table, oldName, table, newName)
		}
		_, err = tx.Exec(fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", table, oldName, newName))
		return err
	}
}

// gridPositionsDDL must stay identical to the user_app_positions
// definition in schema.sql.
const gridPositionsDDL = `CREATE TABLE user_app_positions (
	username     TEXT    NOT NULL REFERENCES user_preferences(username) ON DELETE CASCADE,
	element_id   TEXT    NOT NULL,
	element_type TEXT    NOT NULL,
	x            INTEGER,
	y            INTEGER,
	w            INTEGER NOT NULL DEFAULT 1,
	h            INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (username, element_id)
)`

// fixUserAppPositionsShape repairs the shape fork: an early version of
// runMigrations created user_app_positions as
// (user_id, app_id, position INTEGER), while the grid redesign ships
// (username, element_id, element_type, x, y, w, h). On databases that
// ran the early migration, CREATE TABLE IF NOT EXISTS was a silent
// no-op and every PositionStore query failed against the dead shape.
//
// Repair: drop the dead table, create the grid shape, and rebuild
// positions from the recoverable source (user_preferences.layout JSON)
// when empty. Data repair is best-effort per user — one user's
// malformed layout row must not block the boot of the whole
// appliance; the layout is re-derivable in the UI.
func fixUserAppPositionsShape(tx *sql.Tx) error {
	exists, err := tableExists(tx, "user_app_positions")
	if err != nil {
		return err
	}
	if exists {
		dead, err := columnExists(tx, "user_app_positions", "user_id")
		if err != nil {
			return err
		}
		if !dead {
			return nil // already the grid shape
		}
		if _, err := tx.Exec(`DROP TABLE user_app_positions`); err != nil {
			return fmt.Errorf("drop dead user_app_positions: %w", err)
		}
	}

	if _, err := tx.Exec(gridPositionsDDL); err != nil {
		return fmt.Errorf("create grid user_app_positions: %w", err)
	}

	prefs, err := tableExists(tx, "user_preferences")
	if err != nil {
		return err
	}
	if !prefs {
		return nil
	}
	return rebuildPositionsFromLayout(tx)
}

// layoutElement mirrors the dashboard layout JSON. Both the legacy
// (col/row/colspan/rowspan) and grid (x/y/w/h) spellings are read;
// the conversion matches what the pre-ledger
// migrateLayoutToPositions helper did.
type layoutElement struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	X       *int   `json:"x"`
	Y       *int   `json:"y"`
	W       int    `json:"w"`
	H       int    `json:"h"`
	Col     int    `json:"col"`
	Row     int    `json:"row"`
	Colspan int    `json:"colspan"`
	Rowspan int    `json:"rowspan"`
}

func rebuildPositionsFromLayout(tx *sql.Tx) error {
	rows, err := tx.Query("SELECT username, layout FROM user_preferences WHERE layout IS NOT NULL AND layout != '' AND layout != '[]'")
	if err != nil {
		return fmt.Errorf("read user layouts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type pending struct {
		username string
		el       layoutElement
	}
	var inserts []pending
	for rows.Next() {
		var username, layoutJSON string
		if err := rows.Scan(&username, &layoutJSON); err != nil {
			return fmt.Errorf("scan user layout: %w", err)
		}
		var elements []layoutElement
		if err := json.Unmarshal([]byte(layoutJSON), &elements); err != nil {
			continue // best-effort: skip malformed layout rows
		}
		for _, el := range elements {
			if el.ID == "" || el.Type == "" {
				continue
			}
			inserts = append(inserts, pending{username: username, el: el})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate user layouts: %w", err)
	}

	for _, p := range inserts {
		el := p.el
		x, y, w, h := el.X, el.Y, el.W, el.H
		if el.Col > 0 || el.Row > 0 {
			xv := el.Col - 1
			yv := el.Row - 1
			x, y = &xv, &yv
			w, h = el.Colspan, el.Rowspan
		}
		if w < 1 {
			w = 1
		}
		if h < 1 {
			h = 1
		}
		// INSERT OR IGNORE keeps this idempotent against a partially
		// rebuilt grid; only SQL-level failures (not constraint hits)
		// abort here.
		if _, err := tx.Exec(`INSERT OR IGNORE INTO user_app_positions (username, element_id, element_type, x, y, w, h) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.username, el.ID, el.Type, x, y, w, h); err != nil {
			return fmt.Errorf("rebuild position %s/%s: %w", p.username, el.ID, err)
		}
	}
	return nil
}

func tableExists(db queryable, table string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check table %s: %w", table, err)
	}
	return n > 0, nil
}

func columnExists(db queryable, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, fmt.Errorf("table_info %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("scan table_info %s: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
