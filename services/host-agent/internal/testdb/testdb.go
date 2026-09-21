// SPDX-License-Identifier: AGPL-3.0-only

// Package testdb provides test database utilities for SQLite
package testdb

import (
	"database/sql"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/db"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/schema"
)

// SetupTestDB returns an in-memory SQLite database migrated to the
// current schema version through the same versioned ledger that
// production (db.InitDB) runs, so tests always exercise the real
// upgrade path against the real DDL. The per-connection pragmas come
// from the same DSN query production uses (db.MemoryDSN), not a
// separate Exec loop.
func SetupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	database, err := sql.Open("sqlite", db.MemoryDSN())
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	// ':memory:' is per-connection: a second pooled connection would
	// be a brand-new empty database. Serialize on one connection.
	database.SetMaxOpenConns(1)

	if err := schema.Migrate(database); err != nil {
		_ = database.Close()
		t.Fatalf("failed to migrate test schema: %v", err)
	}

	t.Cleanup(func() { _ = database.Close() })
	return database
}
