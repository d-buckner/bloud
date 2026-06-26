package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// InitDB initializes the SQLite database connection and runs schema
func InitDB(dataDir string) (*sql.DB, error) {
	dbPath := filepath.Join(dataDir, "bloud.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Set SQLite pragmas for performance and correctness
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to set pragma %q: %w", pragma, err)
		}
	}

	// Run schema initialization
	if err := runSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Run migrations for existing databases
	runMigrations(db)

	return db, nil
}

// runMigrations applies incremental schema changes for existing databases.
func runMigrations(db *sql.DB) {
	// v1: add tailnet_id to apps (ALTER TABLE ADD COLUMN fails if column already exists, ignore error)
	db.Exec("ALTER TABLE apps ADD COLUMN tailnet_id TEXT DEFAULT ''")
	// v2: add node_share_link to shares
	db.Exec("ALTER TABLE shares ADD COLUMN node_share_link TEXT NOT NULL DEFAULT ''")
	// v3: add guests table
	db.Exec("CREATE TABLE IF NOT EXISTS guests (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, created_at TEXT DEFAULT (datetime('now')))")
	// v4: rename guest_label → guest_id in shares
	db.Exec("ALTER TABLE shares RENAME COLUMN guest_label TO guest_id")
}

// runSchema executes the embedded schema SQL
func runSchema(db *sql.DB) error {
	_, err := db.Exec(schema)
	if err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}
	return nil
}
