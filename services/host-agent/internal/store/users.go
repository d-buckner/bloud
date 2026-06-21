package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// User represents a Bloud user
type User struct {
	Username string `json:"username"`
}

// GridElement represents an element (app or widget) in the layout grid
type GridElement struct {
	Type    string `json:"type"`    // "app" or "widget"
	ID      string `json:"id"`      // app name or widget id
	Col     int    `json:"col"`     // 1-based column position
	Row     int    `json:"row"`     // 1-based row position
	Colspan int    `json:"colspan"` // number of columns to span
	Rowspan int    `json:"rowspan"` // number of rows to span
}

// PreferencesStore manages user preferences in the database
type PreferencesStore struct {
	db *sql.DB
}

// NewPreferencesStore creates a new preferences store
func NewPreferencesStore(db *sql.DB) *PreferencesStore {
	return &PreferencesStore{db: db}
}

// HasUsers checks if any users exist (fast - stops at first row)
func (s *PreferencesStore) HasUsers() (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM user_preferences").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check users: %w", err)
	}
	return count > 0, nil
}

// EnsureUser creates a user preferences row if it doesn't already exist
func (s *PreferencesStore) EnsureUser(username string) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO user_preferences (username) VALUES (?)",
		username,
	)
	if err != nil {
		return fmt.Errorf("failed to ensure user: %w", err)
	}
	return nil
}

// GetLayout returns the user's layout as an array of grid elements
func (s *PreferencesStore) GetLayout(username string) ([]GridElement, error) {
	var layoutJSON []byte
	err := s.db.QueryRow(
		"SELECT COALESCE(layout, '[]') FROM user_preferences WHERE username = ?",
		username,
	).Scan(&layoutJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get layout: %w", err)
	}

	var elements []GridElement
	if err := json.Unmarshal(layoutJSON, &elements); err != nil {
		return nil, fmt.Errorf("failed to parse layout: %w", err)
	}
	return elements, nil
}

// SetLayout updates the user's layout
func (s *PreferencesStore) SetLayout(username string, elements []GridElement) error {
	layoutJSON, err := json.Marshal(elements)
	if err != nil {
		return fmt.Errorf("failed to marshal layout: %w", err)
	}

	_, err = s.db.Exec(
		"UPDATE user_preferences SET layout = ? WHERE username = ?",
		layoutJSON, username,
	)
	if err != nil {
		return fmt.Errorf("failed to update layout: %w", err)
	}
	return nil
}
