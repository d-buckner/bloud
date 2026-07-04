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
