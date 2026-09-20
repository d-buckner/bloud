// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import "testing"

// The API credential is now required for admin calls from a trusted position,
// so every generated curl invocation must carry it. It must be quoted, or
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

// The e2e runners deploy host-agent into their own runtime dir
// (--runtime-dir / BLOUD_E2E_RUNTIME_DIR, default /var/tmp/bloud-e2e-runtime),
// which is NOT the native backend's default dir. Reading the token through the
// backend's DataDirs silently looked in the wrong place and made every browser
// e2e leg fail with "host-agent API credential unavailable".
func TestLifecycleAPITokenPathUsesTheDeployedRuntimeDir(t *testing.T) {
	r := &lifecycle{cfg: lifecycleConfig{remoteDir: "/var/tmp/bloud-e2e-runtime"}}
	if got, want := r.apiTokenPath(), "/var/tmp/bloud-e2e-runtime/data/host-agent-api-token"; got != want {
		t.Errorf("apiTokenPath() = %q, want %q", got, want)
	}

	// A different runtime dir must produce a different path: the token always
	// comes from the deployment the runner made, never from a backend default.
	other := &lifecycle{cfg: lifecycleConfig{remoteDir: "/var/tmp/bloud-qemu-runtime"}}
	if other.apiTokenPath() == r.apiTokenPath() {
		t.Error("apiTokenPath() ignored the configured runtime dir")
	}
}
