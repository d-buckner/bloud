// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/schema"
	_ "modernc.org/sqlite"
)

// pragmaQuery carries the per-connection SQLite settings in DSN form.
// The modernc driver executes each `_pragma` value at every connection
// open (busy_timeout first), so every connection the pool creates gets
// the same configuration as the first.
//
// This must live in the DSN: `foreign_keys` and `busy_timeout` are
// per-connection settings, so applying them with a one-off db.Exec
// configured exactly one pooled connection. Every other connection
// silently ran with foreign_keys=OFF (ON DELETE cascades stopped
// firing) and busy_timeout=0 (contended writes failed immediately
// with SQLITE_BUSY instead of waiting). That lost the operation-ledger
// phase write on live deploys; see PR 7 in
// docs/plans/tech-debt-repayment.md.
func pragmaQuery() string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	return q.Encode()
}

// dsn builds the SQLite file URI for dbPath. Going through url.URL
// escapes the path so a dataDir containing reserved characters
// round-trips through the driver's URI parser.
func dsn(dbPath string) string {
	u := url.URL{Scheme: "file", Path: dbPath}
	u.RawQuery = pragmaQuery()
	return u.String()
}

// MemoryDSN returns an in-memory DSN carrying the same per-connection
// pragmas as InitDB. Caveat: a `:memory:` database is private to its
// connection; a second pooled connection is a separate empty database,
// so callers using this must cap the pool at one connection (see
// internal/testdb).
func MemoryDSN() string {
	return "file::memory:?" + pragmaQuery()
}

// InitDB initializes the SQLite database connection and upgrades the
// schema to schema.LatestVersion() via the versioned migration ledger.
// The per-connection pragmas travel in the DSN (see pragmaQuery); they
// must not be re-applied with db.Exec, which would re-introduce the
// one-connection-only bug this replaces.
func InitDB(dataDir string) (*sql.DB, error) {
	dbPath := filepath.Join(dataDir, "bloud.db")

	db, err := sql.Open("sqlite", dsn(dbPath))
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Ping opens a real connection, which applies the DSN pragmas and
	// surfaces a bad DSN or unusable file here rather than mid-request.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	if err := schema.Migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return db, nil
}
