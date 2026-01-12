package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// InitDB initializes the SQLite database and runs migrations
func InitDB(dataDir string) (*sql.DB, error) {
	// Ensure the state directory exists
	stateDir := filepath.Join(dataDir, "state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	// Open database connection
	dbPath := filepath.Join(stateDir, "bloud.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Run schema initialization
	if err := runSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Run migrations for existing databases
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return db, nil
}

// runSchema executes the embedded schema SQL
func runSchema(db *sql.DB) error {
	_, err := db.Exec(schema)
	if err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}
	return nil
}

// runMigrations applies schema changes to existing databases
func runMigrations(db *sql.DB) error {
	// Migration: Add is_system column to apps table if it doesn't exist
	if err := addColumnIfNotExists(db, "apps", "is_system", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("failed to add is_system column: %w", err)
	}

	return nil
}

// addColumnIfNotExists adds a column to a table if it doesn't already exist
func addColumnIfNotExists(db *sql.DB, table, column, definition string) error {
	// Check if column exists by querying table info
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()

	var exists bool
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			exists = true
			break
		}
	}

	if !exists {
		_, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
		if err != nil {
			return err
		}
	}

	return nil
}
