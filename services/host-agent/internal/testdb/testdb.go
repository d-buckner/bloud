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

// Schema is the production schema, shared from the schema package so
// tests always exercise the same DDL as the real database.
var Schema = schema.SQL

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
