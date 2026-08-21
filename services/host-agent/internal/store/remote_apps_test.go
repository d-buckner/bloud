// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package store

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoteAppStore_Create_List(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewRemoteAppStore(db)

	err := store.Create(RemoteApp{
		ID:                 "ra-001",
		HostLabel:          "alice-server",
		AppID:              "jellyfin",
		AppName:            "Jellyfin",
		SSOStrategy:        "native-oidc",
		BypassPaths:        []string{"/health", "/metrics"},
		TailnetAddr: "alice-jellyfin.tail1234.ts.net",
		Status:             "pending_credential",
	})
	require.NoError(t, err)

	apps, err := store.List()
	require.NoError(t, err)
	assert.Len(t, apps, 1)
	assert.Equal(t, "ra-001", apps[0].ID)
	assert.Equal(t, "alice-server", apps[0].HostLabel)
	assert.Equal(t, "jellyfin", apps[0].AppID)
	assert.Equal(t, "Jellyfin", apps[0].AppName)
	assert.Equal(t, "native-oidc", apps[0].SSOStrategy)
	assert.Equal(t, []string{"/health", "/metrics"}, apps[0].BypassPaths)
	assert.Equal(t, "alice-jellyfin.tail1234.ts.net", apps[0].TailnetAddr)
	assert.Equal(t, "pending_credential", apps[0].Status)
	assert.False(t, apps[0].CreatedAt.IsZero())
}

func TestRemoteAppStore_SetCredential_GetByID(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewRemoteAppStore(db)

	require.NoError(t, store.Create(RemoteApp{
		ID:                 "ra-001",
		HostLabel:          "alice-server",
		AppID:              "jellyfin",
		AppName:            "Jellyfin",
		SSOStrategy:        "native-oidc",
		BypassPaths:        []string{},
		TailnetAddr: "alice-jellyfin.tail1234.ts.net",
		Status:             "pending_credential",
	}))

	cred := []byte("encrypted-api-key-data")
	err := store.SetCredential("ra-001", cred)
	require.NoError(t, err)

	app, err := store.GetByID("ra-001")
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.Equal(t, cred, app.EncryptedCred)
	assert.Equal(t, "active", app.Status)
}

func TestRemoteAppStore_SetCredential_NotFound(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewRemoteAppStore(db)

	err := store.SetCredential("nonexistent", []byte("data"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remote app not found")
}

func TestRemoteAppStore_SetStatus(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewRemoteAppStore(db)

	require.NoError(t, store.Create(RemoteApp{
		ID:                 "ra-001",
		HostLabel:          "alice-server",
		AppID:              "jellyfin",
		AppName:            "Jellyfin",
		SSOStrategy:        "native-oidc",
		BypassPaths:        []string{},
		TailnetAddr: "alice-jellyfin.tail1234.ts.net",
		Status:             "pending_credential",
	}))

	err := store.SetStatus("ra-001", "error")
	require.NoError(t, err)

	app, err := store.GetByID("ra-001")
	require.NoError(t, err)
	assert.Equal(t, "error", app.Status)
}

func TestRemoteAppStore_Delete(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewRemoteAppStore(db)

	require.NoError(t, store.Create(RemoteApp{
		ID:                 "ra-001",
		HostLabel:          "alice-server",
		AppID:              "jellyfin",
		AppName:            "Jellyfin",
		SSOStrategy:        "native-oidc",
		BypassPaths:        []string{},
		TailnetAddr: "alice-jellyfin.tail1234.ts.net",
		Status:             "pending_credential",
	}))

	err := store.Delete("ra-001")
	require.NoError(t, err)

	app, err := store.GetByID("ra-001")
	require.NoError(t, err)
	assert.Nil(t, app)
}

func TestRemoteAppStore_Delete_NotFound(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewRemoteAppStore(db)

	err := store.Delete("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remote app not found")
}

func TestRemoteAppStore_GetByID_NotFound(t *testing.T) {
	db := testdb.SetupTestDB(t)
	store := NewRemoteAppStore(db)

	app, err := store.GetByID("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, app)
}
