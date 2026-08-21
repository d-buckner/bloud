// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package store

import (
	"database/sql"
	"fmt"
)

// Role represents a user's permission level
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

// User represents a Bloud user
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

// DeleteUser removes a user's preferences row (cascades to user_app_positions)
func (s *PreferencesStore) DeleteUser(username string) error {
	_, err := s.db.Exec("DELETE FROM user_preferences WHERE username = ?", username)
	if err != nil {
		return fmt.Errorf("failed to delete user preferences: %w", err)
	}
	return nil
}
