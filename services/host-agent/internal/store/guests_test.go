// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package store

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGuestStore_Create_GetByID(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewGuestStore(db)

	err := store.Create(Guest{
		ID:   "guest-001",
		Name: "Alice",
	})
	require.NoError(t, err)

	guest, err := store.GetByID("guest-001")
	require.NoError(t, err)
	require.NotNil(t, guest)
	assert.Equal(t, "guest-001", guest.ID)
	assert.Equal(t, "Alice", guest.Name)
	assert.False(t, guest.CreatedAt.IsZero())
}

func TestGuestStore_GetByID_NotFound(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewGuestStore(db)

	guest, err := store.GetByID("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, guest)
}

func TestGuestStore_List(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewGuestStore(db)

	require.NoError(t, store.Create(Guest{ID: "guest-001", Name: "Alice"}))
	require.NoError(t, store.Create(Guest{ID: "guest-002", Name: "Bob"}))

	guests, err := store.List()
	require.NoError(t, err)
	assert.Len(t, guests, 2)
	// Ordered by name ASC
	assert.Equal(t, "Alice", guests[0].Name)
	assert.Equal(t, "Bob", guests[1].Name)
}

func TestGuestStore_List_Empty(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewGuestStore(db)

	guests, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, guests)
}

func TestGuestStore_Delete(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewGuestStore(db)

	require.NoError(t, store.Create(Guest{ID: "guest-001", Name: "Alice"}))

	err := store.Delete("guest-001")
	require.NoError(t, err)

	guest, err := store.GetByID("guest-001")
	require.NoError(t, err)
	assert.Nil(t, guest)
}

func TestGuestStore_Delete_NotFound(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewGuestStore(db)

	err := store.Delete("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "guest not found")
}

func TestGuestStore_DuplicateName(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewGuestStore(db)

	require.NoError(t, store.Create(Guest{ID: "guest-001", Name: "Alice"}))

	err := store.Create(Guest{ID: "guest-002", Name: "Alice"})
	require.Error(t, err)
}
