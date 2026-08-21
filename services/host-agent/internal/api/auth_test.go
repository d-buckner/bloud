// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsLocalRequest_TrustedNets(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		trusted    []string
		want       bool
	}{
		{"loopback", "127.0.0.1:1234", nil, true},
		{"loopback ipv6", "[::1]:1234", nil, true},
		{"nonlocal no trusted", "10.0.2.2:1234", nil, false},
		{"nonlocal trusted cidr", "10.0.2.2:1234", []string{"10.0.2.0/24"}, true},
		{"nonlocal outside cidr", "10.0.2.2:1234", []string{"10.0.3.0/24"}, false},
		{"nonlocal trusted bare ip", "10.0.2.2:1234", []string{"10.0.2.2"}, true},
		{"nonlocal multiple nets", "10.0.2.9:1234", []string{"192.168.0.0/16", "10.0.2.0/24"}, true},
		{"nonlocal invalid net ignored", "10.0.2.2:1234", []string{"not-a-cidr"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remoteAddr
			if got := isLocalRequest(r, tc.trusted); got != tc.want {
				t.Fatalf("isLocalRequest(%q, %v) = %v, want %v", tc.remoteAddr, tc.trusted, got, tc.want)
			}
		})
	}
}
