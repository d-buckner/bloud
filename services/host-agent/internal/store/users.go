package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// User represents a Bloud user (credentials stored in Authentik)
type User struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
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

// UserStore manages users in the database
type UserStore struct {
	db *sql.DB
}

// NewUserStore creates a new user store
func NewUserStore(db *sql.DB) *UserStore {
	return &UserStore{db: db}
}

// HasUsers checks if any users exist (fast - stops at first row)
func (s *UserStore) HasUsers() (bool, error) {
	var exists bool
	err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM users)").Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check users: %w", err)
	}
	return exists, nil
}

// Create adds a new user to the database
func (s *UserStore) Create(username string) error {
	_, err := s.db.Exec(
		"INSERT INTO users (username) VALUES ($1)",
		username,
	)
	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

// GetByUsername returns a user by username
func (s *UserStore) GetByUsername(username string) (*User, error) {
	var user User
	err := s.db.QueryRow(
		"SELECT id, username, created_at FROM users WHERE username = $1",
		username,
	).Scan(&user.ID, &user.Username, &user.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	return &user, nil
}

// GetLayout returns the user's layout as an array of grid elements
func (s *UserStore) GetLayout(userID string) ([]GridElement, error) {
	var layoutJSON []byte
	err := s.db.QueryRow(
		"SELECT COALESCE(layout, '[]') FROM users WHERE id = $1",
		userID,
	).Scan(&layoutJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get layout: %w", err)
	}

	// Try to parse as array first (new format)
	var elements []GridElement
	if err := json.Unmarshal(layoutJSON, &elements); err == nil {
		return elements, nil
	}

	// Fall back to old format {elements: [...], widgetConfigs: {...}}
	var oldFormat struct {
		Elements []GridElement `json:"elements"`
	}
	if err := json.Unmarshal(layoutJSON, &oldFormat); err != nil {
		return nil, fmt.Errorf("failed to parse layout: %w", err)
	}
	return oldFormat.Elements, nil
}

// SetLayout updates the user's layout
func (s *UserStore) SetLayout(userID string, elements []GridElement) error {
	layoutJSON, err := json.Marshal(elements)
	if err != nil {
		return fmt.Errorf("failed to marshal layout: %w", err)
	}

	_, err = s.db.Exec(
		"UPDATE users SET layout = $1 WHERE id = $2",
		layoutJSON, userID,
	)
	if err != nil {
		return fmt.Errorf("failed to update layout: %w", err)
	}
	return nil
}
