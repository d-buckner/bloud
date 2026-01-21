package store

import (
	"database/sql"
	"fmt"
	"time"
)

// User represents a Bloud user (credentials stored in Authentik)
type User struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
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
