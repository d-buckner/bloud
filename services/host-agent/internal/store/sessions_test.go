// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package store

import (
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionStore_Create_Get(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSessionStore(db)

	created, err := s.Create("user-1", "alice", RoleMember)
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	assert.Equal(t, "user-1", created.UserID)
	assert.Equal(t, "alice", created.Username)
	assert.Equal(t, RoleMember, created.Role)
	assert.False(t, created.CreatedAt.IsZero())
	assert.True(t, created.ExpiresAt.After(created.CreatedAt))

	got, err := s.Get(created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, created.UserID, got.UserID)
	assert.Equal(t, created.Username, got.Username)
	assert.Equal(t, created.Role, got.Role)
	assert.WithinDuration(t, created.ExpiresAt, got.ExpiresAt, time.Second)
}

func TestSessionStore_Get_NotFound(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSessionStore(db)

	got, err := s.Get("does-not-exist")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestSessionStore_Create_StoresUTCTimestamps(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSessionStore(db)

	created, err := s.Create("user-1", "alice", RoleAdmin)
	require.NoError(t, err)

	var storedCreated, storedExpires string
	err = db.QueryRow(
		"SELECT created_at, expires_at FROM sessions WHERE id = ?", created.ID,
	).Scan(&storedCreated, &storedExpires)
	require.NoError(t, err)

	for _, stored := range []string{storedCreated, storedExpires} {
		// RFC3339 in UTC must carry the "Z" designator, which keeps
		// string comparison correct for expiry checks in SQL.
		assert.True(t, len(stored) > 1 && stored[len(stored)-1] == 'Z',
			"expected UTC timestamp, got %q", stored)
		parsed, err := time.Parse(time.RFC3339, stored)
		require.NoError(t, err)
		assert.Equal(t, time.UTC, parsed.Location(), "parsed location should be UTC")
	}
}

func TestSessionStore_Delete(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSessionStore(db)

	created, err := s.Create("user-1", "alice", RoleMember)
	require.NoError(t, err)

	require.NoError(t, s.Delete(created.ID))

	got, err := s.Get(created.ID)
	require.NoError(t, err)
	assert.Nil(t, got)

	// Deleting again is a no-op, not an error.
	require.NoError(t, s.Delete(created.ID))
}

func TestSessionStore_DeleteByUserID(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSessionStore(db)

	a1, err := s.Create("user-1", "alice", RoleMember)
	require.NoError(t, err)
	a2, err := s.Create("user-1", "alice", RoleMember)
	require.NoError(t, err)
	b, err := s.Create("user-2", "bob", RoleMember)
	require.NoError(t, err)

	require.NoError(t, s.DeleteByUserID("user-1"))

	assert.Nil(t, mustGet(t, s, a1.ID))
	assert.Nil(t, mustGet(t, s, a2.ID))
	assert.NotNil(t, mustGet(t, s, b.ID))
}

func TestSessionStore_DeleteByUsername(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSessionStore(db)

	a, err := s.Create("user-1", "alice", RoleMember)
	require.NoError(t, err)
	b, err := s.Create("user-2", "bob", RoleMember)
	require.NoError(t, err)

	require.NoError(t, s.DeleteByUsername("alice"))

	assert.Nil(t, mustGet(t, s, a.ID))
	assert.NotNil(t, mustGet(t, s, b.ID))
}

func TestSessionStore_Refresh(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSessionStore(db)
	s.ttl = 1 * time.Hour

	created, err := s.Create("user-1", "alice", RoleMember)
	require.NoError(t, err)

	// Backdate the expiry so a refresh must extend it.
	oldExpiry := time.Now().Add(-10 * time.Minute)
	_, err = db.Exec("UPDATE sessions SET expires_at = ? WHERE id = ?",
		formatSessionTime(oldExpiry), created.ID)
	require.NoError(t, err)

	require.NoError(t, s.Refresh(created.ID))

	got, err := s.Get(created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.ExpiresAt.After(oldExpiry),
		"refresh should extend the expiry")
	assert.WithinDuration(t, time.Now().Add(1*time.Hour), got.ExpiresAt, 5*time.Second)
}

func TestSessionStore_Refresh_NotFound(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSessionStore(db)

	err := s.Refresh("does-not-exist")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session not found")
}

func TestSessionStore_PurgeExpired(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewSessionStore(db)

	expired, err := s.Create("user-1", "alice", RoleMember)
	require.NoError(t, err)
	valid, err := s.Create("user-2", "bob", RoleMember)
	require.NoError(t, err)

	// Expire one session in the past.
	_, err = db.Exec("UPDATE sessions SET expires_at = ? WHERE id = ?",
		formatSessionTime(time.Now().Add(-1*time.Hour)), expired.ID)
	require.NoError(t, err)

	// Only the backdated session has expired.
	n, err := s.PurgeExpired()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "only the backdated session should be purged")

	assert.Nil(t, mustGet(t, s, expired.ID))
	assert.NotNil(t, mustGet(t, s, valid.ID))

	// Second purge removes nothing.
	n, err = s.PurgeExpired()
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

func TestSessionStore_SchemaHasIndexes(t *testing.T) {
	db := testdb.SetupTestDB(t)

	var names []string
	rows, err := db.Query(
		"SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'sessions' ORDER BY name",
	)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}

	for _, want := range []string{"idx_sessions_user_id", "idx_sessions_username", "idx_sessions_expires_at"} {
		assert.Contains(t, names, want, "missing index %q", want)
	}
}

func mustGet(t *testing.T, s *SessionStore, id string) *Session {
	t.Helper()
	got, err := s.Get(id)
	require.NoError(t, err)
	return got
}
