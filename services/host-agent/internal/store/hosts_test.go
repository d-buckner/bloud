// SPDX-License-Identifier: AGPL-3.0-only

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
		[]Host{{Hostname: "localhost"}, {Hostname: "bloud.local"}, {Hostname: "example.com"}, {Hostname: "other.example.com"}},
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

// The scheme round-trips. It is the whole reason the column exists: a host
// behind a TLS-terminating proxy has to come back as https on every boot,
// because nothing Bloud observes at its own socket says so.
func TestHostStore_SchemeRoundTrips(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewHostStore(db)

	require.NoError(t, s.Replace([]Host{
		{Hostname: "bloud.example.com", Scheme: "https"},
		{Hostname: "plain.example.com", Scheme: "http"},
		{Hostname: "unset.example.com"},
	}, "bloud.example.com"))

	hosts, err := s.List()
	require.NoError(t, err)
	byName := map[string]Host{}
	for _, h := range hosts {
		byName[h.Hostname] = h
	}
	assert.Equal(t, "https", byName["bloud.example.com"].Scheme)
	assert.Equal(t, "http", byName["plain.example.com"].Scheme)
	assert.Equal(t, "", byName["unset.example.com"].Scheme)
	assert.True(t, byName["bloud.example.com"].Primary)
}

// A scheme that is not http or https is rejected rather than stored, so a
// typo cannot come back later as a redirect URI the provider refuses.
func TestHostStore_RejectsInvalidScheme(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewHostStore(db)

	require.NoError(t, s.Replace([]Host{{Hostname: "good.example.com", Scheme: "https"}}, ""))

	for _, bad := range []string{"ftp", "htps", "//", "HTTPS; drop"} {
		err := s.Replace([]Host{{Hostname: "other.example.com", Scheme: bad}}, "")
		assert.Error(t, err, "scheme %q must be rejected", bad)
	}

	// The rejected Replace must not have clobbered the good row.
	hosts, err := s.List()
	require.NoError(t, err)
	require.Len(t, hosts, 1)
	assert.Equal(t, "good.example.com", hosts[0].Hostname)
	assert.Equal(t, "https", hosts[0].Scheme)
}

// Built-in hosts never take a stored scheme, even if one is passed: their
// base URLs are fixed by convention and no CA issues for those names.
func TestHostStore_BuiltinsNeverStoredWithScheme(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewHostStore(db)

	require.NoError(t, s.Replace([]Host{
		{Hostname: "localhost", Scheme: "https"},
		{Hostname: "bloud.local", Scheme: "https"},
		{Hostname: "real.example.com", Scheme: "https"},
	}, "real.example.com"))

	hosts, err := s.List()
	require.NoError(t, err)
	require.Len(t, hosts, 1)
	assert.Equal(t, "real.example.com", hosts[0].Hostname)
}

func TestHostStore_ReplaceSwapsExisting(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewHostStore(db)

	require.NoError(t, s.Replace([]Host{{Hostname: "old.example.com", Scheme: "https"}}, "old.example.com"))
	require.NoError(t, s.Replace([]Host{{Hostname: "new.example.com"}}, ""))

	hosts, err := s.List()
	require.NoError(t, err)
	require.Len(t, hosts, 1)
	assert.Equal(t, "new.example.com", hosts[0].Hostname)
	assert.False(t, hosts[0].Primary)
	// The previous scheme is gone, not inherited by the new host.
	assert.Empty(t, hosts[0].Scheme)
}

func TestHostStore_ReplaceDedupesAndSkipsBuiltins(t *testing.T) {
	db := testdb.SetupTestDB(t)
	s := NewHostStore(db)

	err := s.Replace([]Host{
		{Hostname: "localhost"},
		{Hostname: "example.com"},
		{Hostname: "example.com"},
		{Hostname: "EXAMPLE.com"},
	}, "example.com")
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

	require.NoError(t, s.Replace([]Host{{Hostname: "good.example.com"}}, ""))
	err := s.Replace([]Host{{Hostname: "bad host"}}, "")
	assert.Error(t, err)

	// Failed replace must not have clobbered existing rows.
	hosts, err := s.List()
	require.NoError(t, err)
	require.Len(t, hosts, 1)
	assert.Equal(t, "good.example.com", hosts[0].Hostname)
}
