// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettingsStore_GetMissingKeyIsEmpty(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSettingsStore(db)

	value, err := s.Get(SettingPublicURL)
	require.NoError(t, err)
	assert.Equal(t, "", value, "an unset key reads as empty rather than erroring")
}

func TestSettingsStore_SetAndGet(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSettingsStore(db)

	require.NoError(t, s.Set(SettingPublicURL, "https://bloud.example.com:8443"))

	value, err := s.Get(SettingPublicURL)
	require.NoError(t, err)
	assert.Equal(t, "https://bloud.example.com:8443", value)
}

// Set replaces rather than appends: the setting is one value, so a second write
// must not leave two rows or a stale copy behind.
func TestSettingsStore_SetReplaces(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSettingsStore(db)

	require.NoError(t, s.Set(SettingPublicURL, "https://first.example.com"))
	require.NoError(t, s.Set(SettingPublicURL, "https://second.example.com"))

	value, err := s.Get(SettingPublicURL)
	require.NoError(t, err)
	assert.Equal(t, "https://second.example.com", value)

	var count int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM settings WHERE key = ?`, SettingPublicURL,
	).Scan(&count))
	assert.Equal(t, 1, count)
}

// Clearing writes no row at all, so "unset" has exactly one representation
// rather than an empty string that downstream code has to distinguish from a
// genuinely absent key.
func TestSettingsStore_EmptyValueClears(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSettingsStore(db)

	require.NoError(t, s.Set(SettingPublicURL, "https://bloud.example.com"))
	require.NoError(t, s.Set(SettingPublicURL, ""))

	value, err := s.Get(SettingPublicURL)
	require.NoError(t, err)
	assert.Equal(t, "", value)

	var count int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM settings WHERE key = ?`, SettingPublicURL,
	).Scan(&count))
	assert.Equal(t, 0, count)
}

// Keys are independent: clearing one must not touch another.
func TestSettingsStore_KeysAreIndependent(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSettingsStore(db)

	require.NoError(t, s.Set("public_url", "https://a.example.com"))
	require.NoError(t, s.Set("other_key", "keep me"))
	require.NoError(t, s.Set("public_url", ""))

	other, err := s.Get("other_key")
	require.NoError(t, err)
	assert.Equal(t, "keep me", other)
}
