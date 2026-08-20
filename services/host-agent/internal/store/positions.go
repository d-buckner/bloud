// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package store

import (
	"database/sql"
	"fmt"
)

// Position represents a single grid element's position for a user.
type Position struct {
	ElementID   string `json:"id"`
	ElementType string `json:"type"`
	X           *int   `json:"x"`
	Y           *int   `json:"y"`
	W           int    `json:"w"`
	H           int    `json:"h"`
}

// PositionStore manages per-user grid positions in user_app_positions.
type PositionStore struct {
	db *sql.DB
}

// NewPositionStore creates a new PositionStore.
func NewPositionStore(db *sql.DB) *PositionStore {
	return &PositionStore{db: db}
}

// GetForUser returns all stored positions for a user.
func (s *PositionStore) GetForUser(username string) ([]Position, error) {
	rows, err := s.db.Query(`
		SELECT element_id, element_type, x, y, w, h
		FROM user_app_positions
		WHERE username = ?
	`, username)
	if err != nil {
		return nil, fmt.Errorf("query positions: %w", err)
	}
	defer rows.Close()

	var positions []Position
	for rows.Next() {
		var p Position
		var x, y sql.NullInt64
		if err := rows.Scan(&p.ElementID, &p.ElementType, &x, &y, &p.W, &p.H); err != nil {
			return nil, fmt.Errorf("scan position: %w", err)
		}
		if x.Valid {
			v := int(x.Int64)
			p.X = &v
		}
		if y.Valid {
			v := int(y.Int64)
			p.Y = &v
		}
		positions = append(positions, p)
	}
	return positions, nil
}

// SetForUser replaces all positions for a user in a single transaction.
func (s *PositionStore) SetForUser(username string, positions []Position) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM user_app_positions WHERE username = ?", username); err != nil {
		return fmt.Errorf("delete positions: %w", err)
	}

	for _, p := range positions {
		if _, err := tx.Exec(`
			INSERT INTO user_app_positions (username, element_id, element_type, x, y, w, h)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, username, p.ElementID, p.ElementType, p.X, p.Y, p.W, p.H); err != nil {
			return fmt.Errorf("insert position %s: %w", p.ElementID, err)
		}
	}

	return tx.Commit()
}
