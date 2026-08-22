// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/schema"
	_ "modernc.org/sqlite"
)

// InitDB initializes the SQLite database connection and runs schema
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
			db.Close()
			return nil, fmt.Errorf("failed to set pragma %q: %w", pragma, err)
		}
	}

	if err := schema.Run(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	runMigrations(db)

	return db, nil
}

// runMigrations applies incremental schema changes for existing databases.
func runMigrations(db *sql.DB) {
	// v1: add tailnet_id to apps
	db.Exec("ALTER TABLE apps ADD COLUMN tailnet_id TEXT DEFAULT ''")
	// v2: add node_share_link to shares
	db.Exec("ALTER TABLE shares ADD COLUMN node_share_link TEXT NOT NULL DEFAULT ''")
	// v3: add guests table
	db.Exec("CREATE TABLE IF NOT EXISTS guests (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, created_at TEXT DEFAULT (datetime('now')))")
	// v4: rename guest_label → guest_id in shares
	db.Exec("ALTER TABLE shares RENAME COLUMN guest_label TO guest_id")
	// v5: add lifecycle graph tables
	db.Exec("CREATE TABLE IF NOT EXISTS graph_nodes (id TEXT PRIMARY KEY, target_status TEXT NOT NULL DEFAULT 'INITIALIZING', actual_status TEXT NOT NULL DEFAULT 'INITIALIZING', error TEXT NOT NULL DEFAULT '')")
	db.Exec("CREATE TABLE IF NOT EXISTS graph_edges (dependent_id TEXT NOT NULL, dependency_id TEXT NOT NULL, PRIMARY KEY (dependent_id, dependency_id))")
	// v6: add user_app_positions table and migrate existing layout JSON
	db.Exec(`CREATE TABLE IF NOT EXISTS user_app_positions (
		username     TEXT    NOT NULL REFERENCES user_preferences(username) ON DELETE CASCADE,
		element_id   TEXT    NOT NULL,
		element_type TEXT    NOT NULL,
		x            INTEGER,
		y            INTEGER,
		w            INTEGER NOT NULL DEFAULT 1,
		h            INTEGER NOT NULL DEFAULT 1,
		PRIMARY KEY (username, element_id)
	)`)
	// v7: add last_error to apps
	db.Exec("ALTER TABLE apps ADD COLUMN last_error TEXT NOT NULL DEFAULT ''")
	migrateLayoutToPositions(db)
}

// migrateLayoutToPositions reads existing layout JSON from user_preferences and
// populates user_app_positions. Runs only when the positions table is empty.
func migrateLayoutToPositions(db *sql.DB) {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM user_app_positions").Scan(&count); err != nil || count > 0 {
		return
	}

	rows, err := db.Query("SELECT username, layout FROM user_preferences WHERE layout IS NOT NULL AND layout != '' AND layout != '[]'")
	if err != nil {
		return
	}
	defer rows.Close()

	type rawEl struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		X       *int   `json:"x"`
		Y       *int   `json:"y"`
		W       int    `json:"w"`
		H       int    `json:"h"`
		Col     int    `json:"col"`
		Row     int    `json:"row"`
		Colspan int    `json:"colspan"`
		Rowspan int    `json:"rowspan"`
	}

	for rows.Next() {
		var username, layoutJSON string
		if err := rows.Scan(&username, &layoutJSON); err != nil {
			continue
		}
		var elements []rawEl
		if err := json.Unmarshal([]byte(layoutJSON), &elements); err != nil {
			continue
		}
		for _, el := range elements {
			if el.ID == "" || el.Type == "" {
				continue
			}
			x, y, w, h := el.X, el.Y, el.W, el.H
			if el.Col > 0 || el.Row > 0 {
				xv := el.Col - 1
				yv := el.Row - 1
				x, y = &xv, &yv
				w, h = el.Colspan, el.Rowspan
			}
			if w < 1 {
				w = 1
			}
			if h < 1 {
				h = 1
			}
			db.Exec(`INSERT OR IGNORE INTO user_app_positions (username, element_id, element_type, x, y, w, h) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				username, el.ID, el.Type, x, y, w, h)
		}
	}
}
