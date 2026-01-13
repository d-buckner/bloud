package db

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var schema string

// InitDB initializes the PostgreSQL database connection and runs migrations
func InitDB(databaseURL string) (*sql.DB, error) {
	// Open database connection
	db, err := sql.Open("pgx", databaseURL)
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
	// No migrations needed yet - schema is fresh for PostgreSQL
	// Future migrations can use addColumnIfNotExists pattern below
	return nil
}

// addColumnIfNotExists adds a column to a table if it doesn't already exist
// Uses PostgreSQL's information_schema for column detection
func addColumnIfNotExists(db *sql.DB, table, column, definition string) error {
	var exists bool
	err := db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = $1 AND column_name = $2
		)
	`, table, column).Scan(&exists)
	if err != nil {
		return err
	}

	if !exists {
		_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
		if err != nil {
			return err
		}
	}

	return nil
}
