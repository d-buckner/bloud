// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// SettingPublicURL is the settings key holding the address this Bloud is
// reachable at, as a bare origin (https://bloud.example.com:8443).
const SettingPublicURL = "public_url"

// SettingsStore holds instance-level scalar settings as key/value rows.
//
// This is where an admin-editable single value goes, so one setting does not
// have to earn its own table. The keys are declared next to the code that owns
// them (see schema.SettingPublicURL) rather than here, because what a key means
// is that code's business.
type SettingsStoreInterface interface {
	// Get returns the value stored under key, or "" when the key is unset.
	Get(key string) (string, error)
	// Set stores value under key, replacing any previous value. An empty
	// string clears the key, which returns resolution to whatever the
	// fallback source is.
	Set(key, value string) error
}

// Compile-time assertion that SettingsStore implements SettingsStoreInterface
var _ SettingsStoreInterface = (*SettingsStore)(nil)

// SettingsStore manages instance settings in the database.
type SettingsStore struct {
	db *sql.DB
}

// NewSettingsStore creates a new settings store.
func NewSettingsStore(db *sql.DB) *SettingsStore {
	return &SettingsStore{db: db}
}

// Get returns the value stored under key, or "" when the key is unset.
func (s *SettingsStore) Get(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to read setting %q: %w", key, err)
	}
	return value, nil
}

// Set stores value under key, replacing any previous value. An empty string
// deletes the row rather than storing an empty value, so "unset" has exactly
// one representation.
func (s *SettingsStore) Set(key, value string) error {
	if value == "" {
		if _, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key); err != nil {
			return fmt.Errorf("failed to clear setting %q: %w", key, err)
		}
		return nil
	}
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = datetime('now')`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("failed to store setting %q: %w", key, err)
	}
	return nil
}
