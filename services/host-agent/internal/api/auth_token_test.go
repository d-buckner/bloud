// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestAuthMiddleware_BearerToken drives the local-auth contract directly:
// the bearer token is the credential, and loopback/trusted-net origin is a
// scope on it. This is the behavior that replaced the old
// "loopback grants admin with no credential" bypass.
func TestAuthMiddleware_BearerToken(t *testing.T) {
	const token = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// newRouter builds a router with only the auth middleware and an echo
	// handler that reports the authenticated user, so tests can assert the
	// middleware promoted (or refused) the request.
	newRouter := func(apiToken string, trusted []string) *chi.Mux {
		r := chi.NewRouter()
		// nil is safe: bearer requests never touch the store, and the
		// fall-through cases have no cookie (rejected before a store read).
		r.Use(authMiddlewareFn(nil, logger, trusted, apiToken))
		r.Get("/api/apps", func(w http.ResponseWriter, req *http.Request) {
			u := getUserFromContext(req.Context())
			if u == nil {
				respondError(w, http.StatusUnauthorized, "no user")
				return
			}
			// A real admin claim proves promotion happened.
			if u.IsAdmin() && u.Username == "_cli" {
				respondJSON(w, http.StatusOK, map[string]string{"ok": "admin"})
				return
			}
			respondJSON(w, http.StatusOK, map[string]string{"ok": string(u.Role)})
		})
		return r
	}

	reqWith := func(remote, bearer string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
		req.RemoteAddr = remote
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		return req
	}

	t.Run("valid token from loopback grants admin", func(t *testing.T) {
		w := httptest.NewRecorder()
		newRouter(token, nil).ServeHTTP(w, reqWith("127.0.0.1:1234", token))
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "admin")
	})

	t.Run("valid token from trusted net grants admin", func(t *testing.T) {
		w := httptest.NewRecorder()
		newRouter(token, []string{"10.0.2.0/24"}).ServeHTTP(w, reqWith("10.0.2.2:5555", token))
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "admin")
	})

	t.Run("valid token from off-network is rejected", func(t *testing.T) {
		// The whole point of the scope: a stolen token presented from the
		// LAN/WAN must not authenticate.
		w := httptest.NewRecorder()
		newRouter(token, []string{"10.0.2.0/24"}).ServeHTTP(w, reqWith("203.0.113.9:1234", token))
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.NotContains(t, w.Body.String(), "admin")
	})

	t.Run("wrong token is rejected with 401", func(t *testing.T) {
		w := httptest.NewRecorder()
		newRouter(token, nil).ServeHTTP(w, reqWith("127.0.0.1:1234", "totally-wrong-token-xxxxx"))
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.NotContains(t, w.Body.String(), "admin")
	})

	t.Run("no bearer + no session is rejected", func(t *testing.T) {
		// Local browser with neither a CLI token nor a session cookie must
		// not get admin. This is the regression this PR exists to fix.
		w := httptest.NewRecorder()
		newRouter(token, nil).ServeHTTP(w, reqWith("127.0.0.1:1234", ""))
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("empty configured token disables local bypass", func(t *testing.T) {
		// If the server was built without a token, a bearer request must not
		// authenticate even from loopback — fail closed.
		w := httptest.NewRecorder()
		newRouter("", nil).ServeHTTP(w, reqWith("127.0.0.1:1234", "anything"))
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("non-bearer auth scheme falls through to session path", func(t *testing.T) {
		// Basic auth is not our scheme; absence of a bearer means the normal
		// (session-cookie) path, which is unauthenticated here.
		req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
		w := httptest.NewRecorder()
		newRouter(token, nil).ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestBearerToken_Extraction(t *testing.T) {
	cases := []struct {
		name        string
		header      string
		wantVal     string
		wantPresent bool
	}{
		{"present", "Bearer abc123", "abc123", true},
		{"case-insensitive scheme", "bearer abc123", "abc123", true},
		{"trims trailing space", "Bearer abc123  ", "abc123", true},
		{"absent", "", "", false},
		{"wrong scheme", "Token abc123", "", false},
		{"bare prefix only", "Bearer ", "", false},
		{"too short", "Bear", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			got, present := bearerToken(req)
			assert.Equal(t, tc.wantPresent, present)
			assert.Equal(t, tc.wantVal, got)
		})
	}
}

func TestSubtleTokenEqual(t *testing.T) {
	require.False(t, subtleTokenEqual("short", "different-length-token"))
	require.False(t, subtleTokenEqual("a", "b"))
	require.True(t, subtleTokenEqual("match", "match"))
}
