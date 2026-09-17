// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package db

import (
	"database/sql"
	"fmt"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/schema"
	_ "modernc.org/sqlite"
)

// InitDB initializes the SQLite database connection and upgrades the
// schema to schema.LatestVersion() via the versioned migration ledger.
func InitDB(dataDir string) (*sql.DB, error) {
	dbPath := filepath.Join(dataDir, "bloud.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("failed to set pragma %q: %w", pragma, err)
		}
	}

	if err := schema.Migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return db, nil
}

