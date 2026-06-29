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
    catalog_id TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    version TEXT DEFAULT '',
    status TEXT NOT NULL DEFAULT 'stopped',
    port INTEGER,
    is_system INTEGER NOT NULL DEFAULT 0,
    tailnet_id TEXT DEFAULT '',
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

CREATE TABLE IF NOT EXISTS guests (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    created_at TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS shares (
    id              TEXT PRIMARY KEY,
    app_id          INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    sso_strategy    TEXT NOT NULL DEFAULT 'native-oidc',
    guest_id        TEXT NOT NULL REFERENCES guests(id) ON DELETE CASCADE,
    node_share_link TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'active',
    created_at      TEXT DEFAULT (datetime('now')),
    revoked_at      TEXT
);

CREATE TABLE IF NOT EXISTS tailnet_connections (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    type        TEXT NOT NULL,
    auth_key    TEXT NOT NULL,
    control_url TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS remote_apps (
    id                   TEXT PRIMARY KEY,
    host_label           TEXT NOT NULL,
    app_id               TEXT NOT NULL,
    app_name             TEXT NOT NULL,
    sso_strategy         TEXT NOT NULL,
    bypass_paths         TEXT NOT NULL DEFAULT '[]',
    sidecar_tailnet_addr TEXT NOT NULL,
    encrypted_cred       BLOB,
    status               TEXT NOT NULL DEFAULT 'pending_credential',
    created_at           TEXT DEFAULT (datetime('now'))
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
