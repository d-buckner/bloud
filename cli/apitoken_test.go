// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import "testing"

// The API credential is now required for admin calls from a trusted position,
// so every generated curl invocation must carry it — and it must be quoted, or
// a token containing shell metacharacters would break the command (or worse).
func TestAuthHeaderQuotesTheToken(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{"plain", "abc123", `-H 'Authorization: Bearer abc123'`},
		{"shell metacharacters", "a;rm -rf /", `-H 'Authorization: Bearer a;rm -rf /'`},
		{"embedded quote", "a'b", `-H 'Authorization: Bearer a'"'"'b'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := authHeader(tc.token); got != tc.want {
				t.Errorf("authHeader(%q) = %s, want %s", tc.token, got, tc.want)
			}
		})
	}
}
