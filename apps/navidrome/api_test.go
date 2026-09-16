// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package navidrome

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestNavidromeLoginAndCreateUser(t *testing.T) {
	var createdHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login":
			_, _ = w.Write([]byte("{\"token\":\"navitok\"}"))
		case "/api/user":
			if r.Method == http.MethodPost {
				createdHeader = r.Header.Get("X-ND-Authorization")
				w.WriteHeader(http.StatusOK)
				return
			}
			_, _ = w.Write([]byte("[{\"userName\":\"alice\"}]"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	c.baseURL = srv.URL

	tok, err := c.navi.login(context.Background(), "bloud-admin", "pw")
	require.NoError(t, err)
	assert.Equal(t, "navitok", tok)

	require.NoError(t, c.navi.createUser(context.Background(), tok, "bob", "Bob", "bob@x.io"))
	assert.Equal(t, "Bearer navitok", createdHeader, "own API uses X-ND-Authorization")
}

func TestAuthentikListActiveUsers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer aktok", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("{\"results\":[{\"username\":\"carol\",\"name\":\"Carol\",\"email\":\"c@x.io\"}]}"))
	}))
	t.Cleanup(srv.Close)
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	c.authentikURL = srv.URL
	users, err := c.ak.listActiveUsers(context.Background(), "aktok")
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "carol", users[0].Username)
}