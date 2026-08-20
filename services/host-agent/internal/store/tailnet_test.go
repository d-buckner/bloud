// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package store

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTailnetStore_Create_GetByID(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewTailnetStore(db)

	err := store.Create(TailnetConnection{
		ID:         "tn-001",
		Name:       "My Tailnet",
		Type:       "tailscale",
		AuthKey:    "tskey-auth-abc123",
		ControlURL: "",
		Status:     "active",
	})
	require.NoError(t, err)

	conn, err := store.GetByID("tn-001")
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.Equal(t, "tn-001", conn.ID)
	assert.Equal(t, "My Tailnet", conn.Name)
	assert.Equal(t, "tailscale", conn.Type)
	assert.Equal(t, "tskey-auth-abc123", conn.AuthKey)
	assert.Equal(t, "", conn.ControlURL)
	assert.Equal(t, "active", conn.Status)
	assert.False(t, conn.CreatedAt.IsZero())
}

func TestTailnetStore_GetByID_NotFound(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewTailnetStore(db)

	conn, err := store.GetByID("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, conn)
}

func TestTailnetStore_GetActive_Empty(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewTailnetStore(db)

	conn, err := store.GetActive()
	require.NoError(t, err)
	assert.Nil(t, conn)
}

func TestTailnetStore_GetActive_WithData(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewTailnetStore(db)

	require.NoError(t, store.Create(TailnetConnection{
		ID:      "tn-001",
		Name:    "My Tailnet",
		Type:    "tailscale",
		AuthKey: "tskey-auth-abc123",
		Status:  "active",
	}))

	conn, err := store.GetActive()
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.Equal(t, "tn-001", conn.ID)
	assert.Equal(t, "tskey-auth-abc123", conn.AuthKey)
}

func TestTailnetStore_List(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewTailnetStore(db)

	require.NoError(t, store.Create(TailnetConnection{
		ID:      "tn-001",
		Name:    "Tailscale",
		Type:    "tailscale",
		AuthKey: "tskey-1",
		Status:  "active",
	}))
	require.NoError(t, store.Create(TailnetConnection{
		ID:         "tn-002",
		Name:       "Headscale",
		Type:       "headscale",
		AuthKey:    "hskey-1",
		ControlURL: "https://hs.example.com",
		Status:     "active",
	}))

	conns, err := store.List()
	require.NoError(t, err)
	assert.Len(t, conns, 2)
}

func TestTailnetStore_Delete(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewTailnetStore(db)

	require.NoError(t, store.Create(TailnetConnection{
		ID:      "tn-001",
		Name:    "Tailscale",
		Type:    "tailscale",
		AuthKey: "tskey-1",
		Status:  "active",
	}))

	err := store.Delete("tn-001")
	require.NoError(t, err)

	conn, err := store.GetByID("tn-001")
	require.NoError(t, err)
	assert.Nil(t, conn)
}

func TestTailnetStore_Delete_NotFound(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewTailnetStore(db)

	err := store.Delete("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tailnet connection not found")
}

func TestTailnetStore_Headscale_WithControlURL(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewTailnetStore(db)

	require.NoError(t, store.Create(TailnetConnection{
		ID:         "tn-001",
		Name:       "My Headscale",
		Type:       "headscale",
		AuthKey:    "hskey-auth-xyz",
		ControlURL: "https://hs.example.com",
		Status:     "active",
	}))

	conn, err := store.GetByID("tn-001")
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.Equal(t, "headscale", conn.Type)
	assert.Equal(t, "https://hs.example.com", conn.ControlURL)
}
