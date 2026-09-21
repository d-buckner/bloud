// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package configurator

import "testing"

func TestAppExternalURL(t *testing.T) {
	tests := []struct {
		name string
		base string
		app  string
		want string
	}{
		{"localhost keeps the port", "http://localhost:8080", "paperless-ngx", "http://paperless-ngx.localhost:8080"},
		{"ip host", "http://192.168.1.5:8080", "affine", "http://affine.192.168.1.5:8080"},
		{"real domain keeps the scheme", "https://bloud.example.com", "immich", "https://immich.bloud.example.com"},
		{"trailing slash trimmed", "http://localhost:8080/", "affine", "http://affine.localhost:8080"},
		{"path and query dropped", "http://localhost:8080/admin?x=1", "affine", "http://affine.localhost:8080"},
		{"empty base falls back", "", "affine", "http://affine.localhost:8080"},
		{"invalid base falls back", "://nonsense", "affine", "http://affine.localhost:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AppExternalURL(func() string { return tt.base }, tt.app); got != tt.want {
				t.Errorf("AppExternalURL(%q, %q) = %q, want %q", tt.base, tt.app, got, tt.want)
			}
		})
	}
}

func TestAppExternalURL_NilBaseURL(t *testing.T) {
	if got := AppExternalURL(nil, "affine"); got != "http://affine.localhost:8080" {
		t.Errorf("AppExternalURL(nil, affine) = %q, want the dev fallback", got)
	}
}
