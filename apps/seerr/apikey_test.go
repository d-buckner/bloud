// SPDX-License-Identifier: AGPL-3.0-only

package seerr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// TestDeriveAPIKey pins the login-and-read flow: the Jellyfin login sets a
// session cookie, and the settings read presents that cookie back to return
// main.apiKey.
func TestDeriveAPIKey(t *testing.T) {
	var sawCookie bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == apiRoot+"/auth/jellyfin":
			w.Header().Add("Set-Cookie", "connect.sid=session-token; Path=/")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == apiRoot+"/settings/main":
			if strings.Contains(r.Header.Get("Cookie"), "connect.sid") {
				sawCookie = true
			}
			_, _ = w.Write([]byte(`{"apiKey":"derived-api-key"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	key, err := DeriveAPIKey(context.Background(), configurator.ClientFactory{}, func() string { return srv.URL }, "daniel", "pw")
	if err != nil {
		t.Fatalf("DeriveAPIKey() error = %v", err)
	}
	if key != "derived-api-key" {
		t.Errorf("DeriveAPIKey() = %q, want %q", key, "derived-api-key")
	}
	if !sawCookie {
		t.Error("the settings read did not present the login session cookie")
	}
}

// TestDeriveAPIKeyRejectsBadLogin pins that a Jellyfin login Seerr refuses is an
// error, not a silent empty key.
func TestDeriveAPIKeyRejectsBadLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == apiRoot+"/auth/jellyfin" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := DeriveAPIKey(context.Background(), configurator.ClientFactory{}, func() string { return srv.URL }, "daniel", "wrong"); err == nil {
		t.Fatal("DeriveAPIKey() succeeded with a refused login, want error")
	}
}

// TestDeriveAPIKeyRejectsMissingKey pins that settings without main.apiKey is an
// error, not an empty credential that reads as "not ready".
func TestDeriveAPIKeyRejectsMissingKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == apiRoot+"/auth/jellyfin":
			w.Header().Add("Set-Cookie", "connect.sid=session-token; Path=/")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == apiRoot+"/settings/main":
			_, _ = w.Write([]byte(`{"apiKey":""}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	if _, err := DeriveAPIKey(context.Background(), configurator.ClientFactory{}, func() string { return srv.URL }, "daniel", "pw"); err == nil {
		t.Fatal("DeriveAPIKey() succeeded with no apiKey in settings, want error")
	}
}
