package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Guest represents a contact in the host's guest book
type Guest struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// GuestStore manages guests in the database
type GuestStore struct {
	db *sql.DB
}

// NewGuestStore creates a new guest store
func NewGuestStore(db *sql.DB) *GuestStore {
	return &GuestStore{db: db}
}

// Create inserts a new guest
func (s *GuestStore) Create(guest Guest) error {
	_, err := s.db.Exec(`
		INSERT INTO guests (id, name) VALUES (?, ?)
	`, guest.ID, guest.Name)
	if err != nil {
		return fmt.Errorf("failed to insert guest: %w", err)
	}
	return nil
}

// GetByID returns a guest by ID, or (nil, nil) if not found
func (s *GuestStore) GetByID(id string) (*Guest, error) {
	var guest Guest
	var createdAt string

	err := s.db.QueryRow(`
		SELECT id, name, created_at FROM guests WHERE id = ?
	`, id).Scan(&guest.ID, &guest.Name, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get guest: %w", err)
	}

	guest.CreatedAt = parseSQLiteTime(createdAt)
	return &guest, nil
}

// List returns all guests ordered by name
func (s *GuestStore) List() ([]*Guest, error) {
	rows, err := s.db.Query(`
		SELECT id, name, created_at FROM guests ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query guests: %w", err)
	}
	defer rows.Close()

	guests := []*Guest{}
	for rows.Next() {
		var guest Guest
		var createdAt string
		if err := rows.Scan(&guest.ID, &guest.Name, &createdAt); err != nil {
			return nil, fmt.Errorf("failed to scan guest: %w", err)
		}
		guest.CreatedAt = parseSQLiteTime(createdAt)
		guests = append(guests, &guest)
	}
	return guests, nil
}

// Delete removes a guest by ID
func (s *GuestStore) Delete(id string) error {
	result, err := s.db.Exec(`DELETE FROM guests WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete guest: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("guest not found: %s", id)
	}
	return nil
}
