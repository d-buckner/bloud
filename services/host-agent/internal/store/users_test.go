// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
)

func TestPreferencesStore_FirstUserIsEmptyBeforeSetup(t *testing.T) {
	s := NewPreferencesStore(testdb.SetupTestDB(t))

	username, err := s.FirstUser()
	require.NoError(t, err)
	assert.Empty(t, username, "no row means setup has not run, not an error")
}

// FirstUser is the principal per-user app state is created under (Radicale's
// synced calendar collections). Setup writes the operator's row before any
// other user can exist, so the earliest row is the operator, and a later login
// by a member must not displace it.
func TestPreferencesStore_FirstUserIsTheFirstRow(t *testing.T) {
	s := NewPreferencesStore(testdb.SetupTestDB(t))

	require.NoError(t, s.EnsureUser("alice"))
	require.NoError(t, s.EnsureUser("bob"))

	username, err := s.FirstUser()
	require.NoError(t, err)
	assert.Equal(t, "alice", username)
}
