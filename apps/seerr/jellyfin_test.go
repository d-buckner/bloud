// SPDX-License-Identifier: AGPL-3.0-only

package seerr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The onboarding body takes a host, a port and a TLS flag rather than a URL,
// so the split has to be right for both a container on Bloud's network and a
// server the operator pointed at from off-host.
func TestJellyfinHostPort(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		host    string
		port    int
		useSSL  bool
		wantErr bool
	}{
		{"container on the app network", "http://apps-jellyfin:8096", "apps-jellyfin", 8096, false, false},
		{"remote https with an explicit port", "https://jellyfin.example.com:8443", "jellyfin.example.com", 8443, true, false},
		{"remote https defaults to 443", "https://media.example.com", "media.example.com", 443, true, false},
		{"remote http defaults to 80", "http://media.example.com", "media.example.com", 80, false, false},
		{"empty address", "", "", 0, false, true},
		{"no host", "http://:8096", "", 0, false, true},
		{"unsupported scheme", "ftp://media.example.com", "", 0, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, port, useSSL, err := jellyfinHostPort(tc.baseURL)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.host, host)
			assert.Equal(t, tc.port, port)
			assert.Equal(t, tc.useSSL, useSSL)
		})
	}
}
