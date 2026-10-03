// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Role represents a user's permission level
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

type User struct {
	Username string `json:"username"`
	Role     Role   `json:"role"`
}

// IsAdmin returns true if the user has admin privileges
func (u *User) IsAdmin() bool {
	return u.Role == RoleAdmin
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

// FirstUser returns the login name of the first user preferences row, which is
// the operator who completed first-run setup: setup creates that row before any
// other user can exist, and rowid tracks creation order. Empty when setup has
// not run.
func (s *PreferencesStore) FirstUser() (string, error) {
	var username string
	err := s.db.QueryRow(
		"SELECT username FROM user_preferences ORDER BY rowid ASC LIMIT 1",
	).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to read the first user: %w", err)
	}
	return username, nil
}

// DeleteUser removes a user's preferences row (cascades to user_app_positions)
func (s *PreferencesStore) DeleteUser(username string) error {
	_, err := s.db.Exec("DELETE FROM user_preferences WHERE username = ?", username)
	if err != nil {
		return fmt.Errorf("failed to delete user preferences: %w", err)
	}
	return nil
}
