// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package store

import (
	"database/sql"
	"fmt"
	"time"
)

// TailnetConnection represents a configured tailnet connection (Tailscale or Headscale).
type TailnetConnection struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	AuthKey    string    `json:"-"`
	ControlURL string    `json:"control_url"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

// TailnetStore manages tailnet connections in the database.
type TailnetStore struct {
	db *sql.DB
}

// NewTailnetStore creates a new tailnet store.
func NewTailnetStore(db *sql.DB) *TailnetStore {
	return &TailnetStore{db: db}
}

// Create inserts a new tailnet connection.
func (s *TailnetStore) Create(conn TailnetConnection) error {
	_, err := s.db.Exec(`
		INSERT INTO tailnet_connections (id, name, type, auth_key, control_url, status)
		VALUES (?, ?, ?, ?, ?, ?)
	`, conn.ID, conn.Name, conn.Type, conn.AuthKey, conn.ControlURL, conn.Status)
	if err != nil {
		return fmt.Errorf("failed to insert tailnet connection: %w", err)
	}
	return nil
}

// GetByID returns a tailnet connection by ID, or (nil, nil) if not found.
func (s *TailnetStore) GetByID(id string) (*TailnetConnection, error) {
	row := s.db.QueryRow(`
		SELECT id, name, type, auth_key, control_url, status, created_at
		FROM tailnet_connections
		WHERE id = ?
	`, id)

	conn, err := s.scanRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get tailnet connection: %w", err)
	}
	return conn, nil
}

// GetActive returns the first active tailnet connection, or (nil, nil) if none.
func (s *TailnetStore) GetActive() (*TailnetConnection, error) {
	row := s.db.QueryRow(`
		SELECT id, name, type, auth_key, control_url, status, created_at
		FROM tailnet_connections
		WHERE status = 'active'
		LIMIT 1
	`)

	conn, err := s.scanRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get active tailnet connection: %w", err)
	}
	return conn, nil
}

// List returns all tailnet connections.
func (s *TailnetStore) List() ([]*TailnetConnection, error) {
	rows, err := s.db.Query(`
		SELECT id, name, type, auth_key, control_url, status, created_at
		FROM tailnet_connections
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query tailnet connections: %w", err)
	}
	defer rows.Close()

	conns := []*TailnetConnection{}
	for rows.Next() {
		conn, err := s.scanRows(rows)
		if err != nil {
			return nil, err
		}
		conns = append(conns, conn)
	}
	return conns, nil
}

// Delete removes a tailnet connection by ID.
func (s *TailnetStore) Delete(id string) error {
	result, err := s.db.Exec(`DELETE FROM tailnet_connections WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete tailnet connection: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("tailnet connection not found: %s", id)
	}
	return nil
}

func (s *TailnetStore) scanRow(row *sql.Row) (*TailnetConnection, error) {
	var conn TailnetConnection
	var createdAt string

	err := row.Scan(
		&conn.ID,
		&conn.Name,
		&conn.Type,
		&conn.AuthKey,
		&conn.ControlURL,
		&conn.Status,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}

	conn.CreatedAt = parseSQLiteTime(createdAt)
	return &conn, nil
}

func (s *TailnetStore) scanRows(rows *sql.Rows) (*TailnetConnection, error) {
	var conn TailnetConnection
	var createdAt string

	err := rows.Scan(
		&conn.ID,
		&conn.Name,
		&conn.Type,
		&conn.AuthKey,
		&conn.ControlURL,
		&conn.Status,
		&createdAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan tailnet connection: %w", err)
	}

	conn.CreatedAt = parseSQLiteTime(createdAt)
	return &conn, nil
}
