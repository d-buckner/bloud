// Package testdb provides test database utilities for PostgreSQL
package testdb

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var (
	testDB     *sql.DB
	setupOnce  sync.Once
	setupErr   error
	schemaOnce sync.Once
)

// Schema is the PostgreSQL schema for tests
const Schema = `
CREATE TABLE IF NOT EXISTS apps (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    version TEXT,
    status TEXT NOT NULL DEFAULT 'stopped',
    port INTEGER,
    is_system BOOLEAN NOT NULL DEFAULT FALSE,
    integration_config TEXT,
    installed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS rebuild_history (
    id SERIAL PRIMARY KEY,
    trigger TEXT NOT NULL,
    app_name TEXT,
    status TEXT NOT NULL,
    log_path TEXT,
    started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS catalog_cache (
    name TEXT PRIMARY KEY,
    yaml_content TEXT NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_apps_status ON apps(status);
CREATE INDEX IF NOT EXISTS idx_rebuild_status ON rebuild_history(status);
CREATE INDEX IF NOT EXISTS idx_rebuild_app ON rebuild_history(app_name);
`

// getTestDatabaseURL returns the database URL for testing
// Uses TEST_DATABASE_URL env var, or a default that connects to local postgres
func getTestDatabaseURL() string {
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		return url
	}
	// Default matches postgres module defaults (apps/testpass123)
	// Uses bloud_test database to isolate from production data
	return "postgres://apps:testpass123@localhost:5432/bloud_test?sslmode=disable"
}

// SetupTestDB returns a database connection for testing
// If no PostgreSQL is available, it skips the test
func SetupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	setupOnce.Do(func() {
		dbURL := getTestDatabaseURL()
		testDB, setupErr = sql.Open("pgx", dbURL)
		if setupErr != nil {
			return
		}

		// Test the connection
		if err := testDB.Ping(); err != nil {
			setupErr = err
			testDB.Close()
			testDB = nil
			return
		}
	})

	if setupErr != nil {
		t.Skipf("Skipping test: PostgreSQL not available (%v). Set TEST_DATABASE_URL or run ./bloud start to enable database tests.", setupErr)
	}

	if testDB == nil {
		t.Skip("Skipping test: PostgreSQL not available")
	}

	// Create schema (idempotent)
	schemaOnce.Do(func() {
		if _, err := testDB.Exec(Schema); err != nil {
			t.Logf("Warning: failed to create schema: %v", err)
		}
	})

	// Clean up existing data for test isolation
	cleanupTables := []string{"apps", "rebuild_history", "catalog_cache"}
	for _, table := range cleanupTables {
		_, _ = testDB.Exec(fmt.Sprintf("DELETE FROM %s", table))
	}

	return testDB
}
