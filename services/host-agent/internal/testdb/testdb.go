// Package testdb provides test database utilities for SQLite
package testdb

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// Schema is the SQLite schema for tests
const Schema = `
CREATE TABLE IF NOT EXISTS apps (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    version TEXT DEFAULT '',
    status TEXT NOT NULL DEFAULT 'stopped',
    port INTEGER,
    is_system INTEGER NOT NULL DEFAULT 0,
    integration_config TEXT DEFAULT '{}',
    installed_at TEXT DEFAULT (datetime('now')),
    updated_at TEXT DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_apps_status ON apps(status);

CREATE TABLE IF NOT EXISTS user_preferences (
    username TEXT PRIMARY KEY,
    layout TEXT DEFAULT '[]',
    created_at TEXT DEFAULT (datetime('now'))
);
`

// SetupTestDB returns an in-memory SQLite database for testing
func SetupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	// Set pragmas
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			t.Fatalf("failed to set pragma: %v", err)
		}
	}

	// Create schema
	if _, err := db.Exec(Schema); err != nil {
		db.Close()
		t.Fatalf("failed to create schema: %v", err)
	}

	t.Cleanup(func() { db.Close() })
	return db
}
