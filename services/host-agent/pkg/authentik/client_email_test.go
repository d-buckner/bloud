// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package authentik

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserEmailDomain(t *testing.T) {
	tests := []struct {
		baseDomain string
		want       string
	}{
		{"localhost", "localhost.local"},
		{"", "localhost.local"},
		{"bloud.example", "bloud.example"},
		{"myhouse.lan", "myhouse.lan"},
	}
	for _, tt := range tests {
		if got := UserEmailDomain(tt.baseDomain); got != tt.want {
			t.Errorf("UserEmailDomain(%q) = %q, want %q", tt.baseDomain, got, tt.want)
		}
	}
}

// TestCreateUserSetsValidEmail verifies managed users get a TLD-bearing
// identity email: SSO apps (e.g. AFFiNE) validate the claim with an RFC
// email regex and reject single-label domains like "localhost".
func TestCreateUserSetsValidEmail(t *testing.T) {
	var capturedEmail string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/core/users/" && r.Method == http.MethodPost {
			var payload map[string]any
			json.NewDecoder(r.Body).Decode(&payload)
			capturedEmail, _ = payload["email"].(string)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"pk": 42}`))
			return
		}
		// Password set + group add succeed silently.
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-token").WithUserEmailDomain("localhost")
	id, err := client.CreateUser("e2etest", "password")
	if err != nil {
		t.Fatalf("CreateUser() error: %v", err)
	}
	if id != 42 {
		t.Errorf("CreateUser() id = %d, want 42", id)
	}
	if !strings.HasSuffix(capturedEmail, "@localhost.local") {
		t.Errorf("CreateUser() email = %q, want a TLD-bearing address", capturedEmail)
	}
	if !strings.Contains(capturedEmail, "@localhost") {
		t.Errorf("CreateUser() email = %q, must not be the bare 'localhost' domain", capturedEmail)
	}
}
