// Package schema holds the single source of truth for the host-agent
// SQLite schema. Both the production database (db.InitDB) and the test
// database (testdb) apply the same embedded schema.sql, so the two can
// never drift apart.
package schema

import (
	"database/sql"
	_ "embed"
)

//go:embed schema.sql
var SQL string

// Run executes the schema against db. All statements are idempotent
// (CREATE TABLE/INDEX IF NOT EXISTS), so it is safe to call on every
// startup.
func Run(db *sql.DB) error {
	_, err := db.Exec(SQL)
	return err
}
