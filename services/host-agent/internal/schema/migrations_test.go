// SPDX-License-Identifier: AGPL-3.0-only

package schema

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// openRawDB returns an empty in-memory SQLite DB with production
// pragmas but NO schema applied: the pre-migration starting point.
func openRawDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			t.Fatalf("pragma %s: %v", pragma, err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// setupMigrated returns a fresh DB that has walked the full ledger.
func setupMigrated(t *testing.T) *sql.DB {
	t.Helper()
	db := openRawDB(t)
	if err := Migrate(db); err != nil {
		_ = db.Close()
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

// dropLedgerVersionsAbove rewinds the ledger so migrations with
// version > v will run again on the next Migrate (used to re-exercise
// a specific entry against an already-migrated database).
func dropLedgerVersionsAbove(t *testing.T, db *sql.DB, v int) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version > ?`, v); err != nil {
		t.Fatalf("rewind ledger to %d: %v", v, err)
	}
}

func ledgerRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	return n
}

func ledgerMaxVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("max ledger version: %v", err)
	}
	return n
}

func TestMigrate_FreshDatabaseWalksFullLedger(t *testing.T) {
	db := setupMigrated(t)

	if got := ledgerRowCount(t, db); got != LatestVersion() {
		t.Errorf("ledger rows = %d, want %d (every version applied once)", got, LatestVersion())
	}

	// Second run must be a verified no-op: no duplicate stamps, no error.
	if err := Migrate(db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if got := ledgerRowCount(t, db); got != LatestVersion() {
		t.Errorf("ledger rows after rerun = %d, want %d (no duplicate stamps)", got, LatestVersion())
	}

	// The fresh baseline must already be the grid shape (fork repair is
	// a no-op here, not a drop-and-rebuild).
	if has, _ := columnExists(db, "user_app_positions", "user_id"); has {
		t.Error("fresh baseline must not contain the dead user_id column")
	}
	for _, want := range []string{"username", "element_id", "element_type"} {
		if has, _ := columnExists(db, "user_app_positions", want); !has {
			t.Errorf("fresh baseline missing grid column %s", want)
		}
	}
}

func TestMigrate_LegacyColumnsAdded(t *testing.T) {
	db := openRawDB(t)

	// Pre-ledger apps table missing the column migration 5 adds.
	if _, err := db.Exec(`CREATE TABLE apps (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		catalog_id TEXT NOT NULL UNIQUE,
		display_name TEXT NOT NULL,
		version TEXT DEFAULT '',
		status TEXT NOT NULL DEFAULT 'stopped',
		port INTEGER,
		is_system INTEGER NOT NULL DEFAULT 0,
		integration_config TEXT DEFAULT '{}',
		installed_at TEXT,
		updated_at TEXT
	)`); err != nil {
		t.Fatalf("create legacy apps: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	has, err := columnExists(db, "apps", "last_error")
	if err != nil {
		t.Fatalf("columnExists apps.last_error: %v", err)
	}
	if !has {
		t.Error("apps.last_error missing after migration")
	}
}

func TestMigrate_FixesUserAppPositionsShapeFork(t *testing.T) {
	db := openRawDB(t)
	seedDeadShapeFork(t, db)

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	assertGridColumns(t, db)
	assertRebuiltPosition(t, db)
}

// seedDeadShapeFork lays down the dead early-v6 positions table plus one row
// that must not survive, and a recoverable legacy col/row layout JSON in
// user_preferences for the repair to read.
func seedDeadShapeFork(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE user_preferences (
		username TEXT PRIMARY KEY,
		layout TEXT DEFAULT '[]',
		created_at TEXT DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatalf("create user_preferences: %v", err)
	}
	// The dead early-v6 shape plus a leftover dead row that must not survive.
	if _, err := db.Exec(`CREATE TABLE user_app_positions (
		user_id TEXT NOT NULL,
		app_id TEXT NOT NULL,
		position INTEGER NOT NULL,
		PRIMARY KEY (user_id, app_id)
	)`); err != nil {
		t.Fatalf("create dead-shape positions: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user_app_positions (user_id, app_id, position) VALUES ('alice', 'ghost', 5)`); err != nil {
		t.Fatalf("seed dead row: %v", err)
	}
	// Recoverable source: legacy col/row layout JSON.
	layout := `[{"type":"app","id":"jellyfin","col":2,"row":3,"colspan":2,"rowspan":2}]`
	if _, err := db.Exec(`INSERT INTO user_preferences (username, layout) VALUES ('alice', ?)`, layout); err != nil {
		t.Fatalf("seed layout: %v", err)
	}
}

// assertGridColumns checks the dead-shape column is gone and every column the
// final grid shape declares is present.
func assertGridColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	if has, _ := columnExists(db, "user_app_positions", "user_id"); has {
		t.Fatal("dead-shape column user_id survived the fork repair")
	}
	for _, want := range []string{"username", "element_id", "element_type", "x", "y", "w", "h"} {
		has, err := columnExists(db, "user_app_positions", want)
		if err != nil {
			t.Fatalf("columnExists %s: %v", want, err)
		}
		if !has {
			t.Errorf("grid column %s missing after fork repair", want)
		}
	}
}

// assertRebuiltPosition reads the single row the repair should have produced
// from the legacy layout and checks both its identity and its geometry: the
// legacy col/row pair is 1-based, the grid is 0-based.
func assertRebuiltPosition(t *testing.T, db *sql.DB) {
	t.Helper()
	var (
		username, elementID, elementType string
		x, y, w, h                       int
	)
	err := db.QueryRow(`SELECT username, element_id, element_type, x, y, w, h FROM user_app_positions`).
		Scan(&username, &elementID, &elementType, &x, &y, &w, &h)
	if err != nil {
		t.Fatalf("read rebuilt position: %v", err)
	}
	if username != "alice" || elementID != "jellyfin" || elementType != "app" {
		t.Errorf("rebuilt identity = %s/%s/%s, want alice/jellyfin/app", username, elementID, elementType)
	}
	if x != 1 || y != 2 || w != 2 || h != 2 {
		t.Errorf("rebuilt geometry = x:%d y:%d w:%d h:%d, want 1/2/2/2 (col-1, row-1, colspan, rowspan)", x, y, w, h)
	}
}

func TestMigrate_GridShapeUntouchedByForkRepair(t *testing.T) {
	// A database already in the final grid shape must pass the re-run
	// fork repair with its positions intact.
	db := setupMigrated(t)

	if _, err := db.Exec(`INSERT INTO user_preferences (username, layout) VALUES ('bob', '[]')`); err != nil {
		t.Fatalf("seed pref: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user_app_positions (username, element_id, element_type, x, y, w, h)
		VALUES ('bob', 'navidrome', 'app', 3, 4, 1, 1)`); err != nil {
		t.Fatalf("seed grid row: %v", err)
	}

	// Rewind the ledger so only the fork-repair entry (version 6) re-runs.
	dropLedgerVersionsAbove(t, db, 5)
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate with fork repair pending: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_app_positions WHERE username='bob' AND element_id='navidrome'`).Scan(&count); err != nil {
		t.Fatalf("count bob: %v", err)
	}
	if count != 1 {
		t.Errorf("existing grid row lost across fork repair (count=%d, want 1)", count)
	}
}

func TestMigrate_FailureIsVisibleAndNotStamped(t *testing.T) {
	orig := Migrations
	defer func() { Migrations = orig }()

	failingVersion := LatestVersion() + 1
	Migrations = append(Migrations[:len(Migrations):len(Migrations)], Migration{
		Version: failingVersion,
		Name:    "injected failure",
		Up:      func(*sql.Tx) error { return errors.New("boom") },
	})

	db := openRawDB(t)

	err := Migrate(db)
	if err == nil {
		t.Fatal("expected error from failing migration")
	}
	if !strings.Contains(err.Error(), "injected failure") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q should name the migration and wrap its cause", err)
	}

	if got := ledgerMaxVersion(t, db); got != orig[len(orig)-1].Version {
		t.Errorf("failed migration was stamped (max=%d, want %d)", got, orig[len(orig)-1].Version)
	}
}

func TestLatestVersion_MatchesLedgerTop(t *testing.T) {
	if LatestVersion() != len(Migrations) {
		t.Errorf("LatestVersion() = %d, want len(Migrations) = %d (ledger must stay gapless from 1)",
			LatestVersion(), len(Migrations))
	}
}
