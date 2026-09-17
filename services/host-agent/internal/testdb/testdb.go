// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package testdb provides test database utilities for SQLite
package testdb

import (
	"database/sql"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/schema"
	_ "modernc.org/sqlite"
)

// SetupTestDB returns an in-memory SQLite database migrated to the
// current schema version through the same versioned ledger that
// production (db.InitDB) runs, so tests always exercise the real
// upgrade path against the real DDL.
func SetupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	// ':memory:' is per-connection: a second pooled connection would
	// be a brand-new empty database. Serialize on one connection.
	db.SetMaxOpenConns(1)

	// Set pragmas
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			t.Fatalf("failed to set pragma: %v", err)
		}
	}

	if err := schema.Migrate(db); err != nil {
		_ = db.Close()
		t.Fatalf("failed to migrate test schema: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })
	return db
}
