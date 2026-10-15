// SPDX-License-Identifier: AGPL-3.0-only

package slug

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestVectors contains shared test vectors that must also pass in the
// TypeScript implementation (web/src/lib/utils/appUrl.ts slugify function).
func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Basic
		{"hello", "hello"},
		{"Hello World", "hello-world"},

		// Runs of special chars collapse to a single dash
		{"Alice's Server", "alice-s-server"},
		{"foo---bar", "foo-bar"},
		{"  leading spaces  ", "leading-spaces"},

		// Leading/trailing non-alphanumeric trimmed
		{"---trim---", "trim"},
		{"...dots...", "dots"},

		// Numbers preserved
		{"server42", "server42"},
		{"123abc", "123abc"},

		// Unicode collapses to dashes (matching JS /[^a-z0-9]+/g)
		{"café", "caf"},
		{"naïve", "na-ve"},
		{"Ünïcödé", "n-c-d"},

		// Empty and single-char
		{"", ""},
		{"a", "a"},
		{"-", ""},

		// Realistic labels
		{"Johan's server", "johan-s-server"},
		{"Bob's NAS", "bob-s-nas"},
		{"My Home Lab (2024)", "my-home-lab-2024"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, Slugify(tt.input))
		})
	}
}

// TestIsPathSegment pins the rule for a value that becomes a location rather
// than a label. Slugify rewrites anything into something safe; this answers a
// question about a string somebody chose, and the answer has to be no for the
// one input that would escape the tree it was meant to live in.
func TestIsPathSegment(t *testing.T) {
	accept := []string{
		"Movies", "Shows", "movies", "shows",
		"family", "my-calendar", "calendar_2", "v2.1",
		"Movies2", "a",
	}
	for _, s := range accept {
		assert.True(t, IsPathSegment(s), "%q should be a usable path segment", s)
	}

	reject := map[string]string{
		"":            "nothing to name",
		".":           "the current directory",
		"..":          "the parent directory",
		"../admin":    "a traversal out of the tree",
		"a/b":         "two segments, not one",
		"/Movies":     "a leading separator",
		"My Movies":   "a space needs percent-encoding",
		"Movies ":     "a trailing space",
		"Movies?x=1":  "a query separator",
		"Movies#frag": "a fragment separator",
		"a%2Fb":       "an encoded separator is still a separator to a decoder",
		"Movies&":     "an ampersand",
		"Movies\n":    "a control character",
		"caf\u00e9":   "a non-ASCII letter is not path-safe by this rule",
	}
	for s, why := range reject {
		assert.False(t, IsPathSegment(s), "%q should be refused: %s", s, why)
	}
}
