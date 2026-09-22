// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"strings"
	"testing"
)

// withEnv must override an inherited key (not append a duplicate, which a
// child process would resolve ambiguously) while leaving other keys untouched.
func TestWithEnvOverridesAndAdds(t *testing.T) {
	got := withEnv([]string{"PATH=/bin", "GOOS=darwin"}, "GOOS=linux", "GOTOOLCHAIN=auto")

	seen := map[string]int{}
	for _, e := range got {
		key, value, _ := strings.Cut(e, "=")
		seen[key]++
		switch key {
		case "PATH":
			if value != "/bin" {
				t.Fatalf("PATH = %q, want /bin", value)
			}
		case "GOOS":
			if value != "linux" {
				t.Fatalf("GOOS = %q, want linux", value)
			}
		case "GOTOOLCHAIN":
			if value != "auto" {
				t.Fatalf("GOTOOLCHAIN = %q, want auto", value)
			}
		}
	}

	for _, key := range []string{"PATH", "GOOS", "GOTOOLCHAIN"} {
		if seen[key] != 1 {
			t.Fatalf("%s appears %d times, want 1: %v", key, seen[key], got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("env = %v, want 3 entries", got)
	}
}
