package store

import (
	"testing"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShareStore_Create_GetByID(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewShareStore(db)

	err := store.Create(Share{
		ID:          "share-001",
		AppID:       "jellyfin",
		SSOStrategy: "native-oidc",
		GuestLabel:  "Alice",
		Status:      "active",
	})
	require.NoError(t, err)

	share, err := store.GetByID("share-001")
	require.NoError(t, err)
	require.NotNil(t, share)
	assert.Equal(t, "share-001", share.ID)
	assert.Equal(t, "jellyfin", share.AppID)
	assert.Equal(t, "native-oidc", share.SSOStrategy)
	assert.Equal(t, "Alice", share.GuestLabel)
	assert.Equal(t, "active", share.Status)
	assert.False(t, share.CreatedAt.IsZero())
	assert.Nil(t, share.RevokedAt)
}

func TestShareStore_GetByID_NotFound(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewShareStore(db)

	share, err := store.GetByID("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, share)
}

func TestShareStore_Revoke(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewShareStore(db)

	require.NoError(t, store.Create(Share{
		ID:          "share-001",
		AppID:       "jellyfin",
		SSOStrategy: "native-oidc",
		GuestLabel:  "Alice",
		Status:      "active",
	}))

	err := store.Revoke("share-001")
	require.NoError(t, err)

	share, err := store.GetByID("share-001")
	require.NoError(t, err)
	require.NotNil(t, share)
	assert.Equal(t, "revoked", share.Status)
	assert.NotNil(t, share.RevokedAt)
}

func TestShareStore_Revoke_NotFound(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewShareStore(db)

	err := store.Revoke("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "share not found")
}

func TestShareStore_List(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewShareStore(db)

	require.NoError(t, store.Create(Share{
		ID:          "share-001",
		AppID:       "jellyfin",
		SSOStrategy: "native-oidc",
		GuestLabel:  "Alice",
		Status:      "active",
	}))
	require.NoError(t, store.Create(Share{
		ID:          "share-002",
		AppID:       "navidrome",
		SSOStrategy: "native-oidc",
		GuestLabel:  "Bob",
		Status:      "active",
	}))

	shares, err := store.List()
	require.NoError(t, err)
	assert.Len(t, shares, 2)
}

func TestShareStore_DuplicateID(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewShareStore(db)

	require.NoError(t, store.Create(Share{
		ID:          "share-001",
		AppID:       "jellyfin",
		SSOStrategy: "native-oidc",
		GuestLabel:  "Alice",
		Status:      "active",
	}))

	err := store.Create(Share{
		ID:          "share-001",
		AppID:       "navidrome",
		SSOStrategy: "native-oidc",
		GuestLabel:  "Bob",
		Status:      "active",
	})
	require.Error(t, err)
}
