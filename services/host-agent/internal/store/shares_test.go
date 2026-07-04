package store

import (
	"database/sql"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedShareParents inserts an app and a guest so FK constraints are satisfied.
// Returns the integer app ID.
func seedShareParents(t *testing.T, db *sql.DB, catalogID, guestID string) int {
	t.Helper()
	appStore := NewAppStore(db)
	require.NoError(t, appStore.Install(catalogID, "Test App", "1.0.0", nil, nil))
	app, err := appStore.GetByCatalogID(catalogID)
	require.NoError(t, err)

	guestStore := NewGuestStore(db)
	require.NoError(t, guestStore.Create(Guest{ID: guestID, Name: "Guest " + guestID}))

	return app.ID
}

func TestShareStore_Create_GetByID(t *testing.T) {
	db := testdb.SetupTestDB(t)
	appID := seedShareParents(t, db, "jellyfin", "guest-001")
	store := NewShareStore(db)

	err := store.Create(Share{
		ID:          "share-001",
		AppID:       appID,
		SSOStrategy: "native-oidc",
		GuestID:     "guest-001",
		Status:      "active",
	})
	require.NoError(t, err)

	share, err := store.GetByID("share-001")
	require.NoError(t, err)
	require.NotNil(t, share)
	assert.Equal(t, "share-001", share.ID)
	assert.Equal(t, appID, share.AppID)
	assert.Equal(t, "native-oidc", share.SSOStrategy)
	assert.Equal(t, "guest-001", share.GuestID)
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
	appID := seedShareParents(t, db, "jellyfin", "guest-001")
	store := NewShareStore(db)

	require.NoError(t, store.Create(Share{
		ID:          "share-001",
		AppID:       appID,
		SSOStrategy: "native-oidc",
		GuestID:     "guest-001",
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
	appID1 := seedShareParents(t, db, "jellyfin", "guest-001")
	// Second app + guest for second share
	appStore := NewAppStore(db)
	require.NoError(t, appStore.Install("navidrome", "Navidrome", "1.0.0", nil, nil))
	app2, err := appStore.GetByCatalogID("navidrome")
	require.NoError(t, err)
	guestStore := NewGuestStore(db)
	require.NoError(t, guestStore.Create(Guest{ID: "guest-002", Name: "Guest 002"}))

	store := NewShareStore(db)

	require.NoError(t, store.Create(Share{
		ID:          "share-001",
		AppID:       appID1,
		SSOStrategy: "native-oidc",
		GuestID:     "guest-001",
		Status:      "active",
	}))
	require.NoError(t, store.Create(Share{
		ID:          "share-002",
		AppID:       app2.ID,
		SSOStrategy: "native-oidc",
		GuestID:     "guest-002",
		Status:      "active",
	}))

	shares, err := store.List()
	require.NoError(t, err)
	assert.Len(t, shares, 2)
}

func TestShareStore_DuplicateID(t *testing.T) {
	db := testdb.SetupTestDB(t)
	appID := seedShareParents(t, db, "jellyfin", "guest-001")
	// Second guest for the duplicate attempt
	guestStore := NewGuestStore(db)
	require.NoError(t, guestStore.Create(Guest{ID: "guest-002", Name: "Guest 002"}))

	store := NewShareStore(db)

	require.NoError(t, store.Create(Share{
		ID:          "share-001",
		AppID:       appID,
		SSOStrategy: "native-oidc",
		GuestID:     "guest-001",
		Status:      "active",
	}))

	err := store.Create(Share{
		ID:          "share-001",
		AppID:       appID,
		SSOStrategy: "native-oidc",
		GuestID:     "guest-002",
		Status:      "active",
	})
	require.Error(t, err)
}
