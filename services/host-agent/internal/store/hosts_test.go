// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package store

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostStore_Replace_List(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewHostStore(db)

	err := s.Replace(
		[]string{"localhost", "bloud.local", "example.com", "other.example.com"},
		"example.com",
	)
	require.NoError(t, err)

	hosts, err := s.List()
	require.NoError(t, err)
	require.Len(t, hosts, 2) // built-ins are never stored

	byName := map[string]Host{}
	for _, h := range hosts {
		byName[h.Hostname] = h
	}
	assert.Equal(t, true, byName["example.com"].Primary)
	assert.Equal(t, false, byName["other.example.com"].Primary)
}

func TestHostStore_ReplaceSwapsExisting(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewHostStore(db)

	require.NoError(t, s.Replace([]string{"old.example.com"}, "old.example.com"))
	require.NoError(t, s.Replace([]string{"new.example.com"}, ""))

	hosts, err := s.List()
	require.NoError(t, err)
	require.Len(t, hosts, 1)
	assert.Equal(t, "new.example.com", hosts[0].Hostname)
	assert.False(t, hosts[0].Primary)
}

func TestHostStore_ReplaceDedupesAndSkipsBuiltins(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewHostStore(db)

	err := s.Replace([]string{"localhost", "example.com", "example.com", "EXAMPLE.com"}, "example.com")
	require.NoError(t, err)

	hosts, err := s.List()
	require.NoError(t, err)
	require.Len(t, hosts, 1)
	assert.Equal(t, "example.com", hosts[0].Hostname)
	assert.True(t, hosts[0].Primary)
}

func TestHostStore_ReplaceRejectsInvalid(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewHostStore(db)

	require.NoError(t, s.Replace([]string{"good.example.com"}, ""))
	err := s.Replace([]string{"bad host"}, "")
	assert.Error(t, err)

	// Failed replace must not have clobbered existing rows.
	hosts, err := s.List()
	require.NoError(t, err)
	require.Len(t, hosts, 1)
	assert.Equal(t, "good.example.com", hosts[0].Hostname)
}
