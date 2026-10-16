// SPDX-License-Identifier: AGPL-3.0-only

package timezone

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse_AcceptsRealZones(t *testing.T) {
	for _, name := range []string{
		"UTC",
		"Etc/UTC",
		"Europe/Berlin",
		"America/New_York",
		"Asia/Kolkata",
		"Pacific/Auckland",
		// The marks validName allows exist because real names use them.
		"Etc/GMT+2",
		"America/Port-au-Prince",
		"US/Eastern",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Parse(name)
			require.NoError(t, err)
			assert.Equal(t, name, got)
		})
	}
}

// Surrounding space is trimmed, because the value arrives from a form field.
func TestParse_TrimsSpace(t *testing.T) {
	got, err := Parse("  Europe/Berlin ")
	require.NoError(t, err)
	assert.Equal(t, "Europe/Berlin", got)
}

// Every rejection is one the API turns into a 400 that names the problem, so
// each case is a message an operator can act on.
func TestParse_Rejects(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"space only", "   "},
		{"unknown zone", "Mars/Olympus"},
		{"offset instead of a zone", "+02:00"},
		{"space inside", "Europe/Berlin MEZ"},
		// The shape check runs before the lookup, and these are the forms that
		// would otherwise reach the host's zone directory.
		{"absolute path", "/usr/share/zoneinfo/Europe/Berlin"},
		{"parent traversal", "../../etc/passwd"},
		{"leading slash", "/Europe/Berlin"},
		{"trailing slash", "Europe/Berlin/"},
		{"empty segment", "Europe//Berlin"},
		{"dot segment", "Europe/../etc"},
		{"too long", strings.Repeat("a", maxNameLen+1)},
		// Local is the one name LoadLocation answers that names no zone: it
		// means the container's own clock, which is not what the operator chose.
		{"Local", "Local"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(c.input)
			assert.Error(t, err, "%q must be rejected", c.input)
		})
	}
}

// An empty value is its own error, because the caller decides whether "no zone"
// is a rejection or a request to clear the setting.
func TestParse_EmptyIsDistinguishable(t *testing.T) {
	_, err := Parse("")
	assert.ErrorIs(t, err, ErrEmpty)
}

func TestState(t *testing.T) {
	s := NewState("Etc/UTC")
	assert.Equal(t, "Etc/UTC", s.Get())

	s.Set("Europe/Berlin")
	assert.Equal(t, "Europe/Berlin", s.Get())

	// An empty zone is a value: it means Bloud states none.
	s.Set("")
	assert.Equal(t, "", s.Get())
}

// A nil state reads as "no zone" rather than panicking, which is what lets a
// hand-built orchestrator run without a clock at all.
func TestState_NilIsSafe(t *testing.T) {
	var s *State
	assert.Equal(t, "", s.Get())
	assert.NotPanics(t, func() { s.Set("Europe/Berlin") })
}
