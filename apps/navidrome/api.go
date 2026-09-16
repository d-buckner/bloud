// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package navidrome

import (
	"context"
	"fmt"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// navidromeUser is a Navidrome user record.
type navidromeUser struct {
	ID       string `json:"id"`
	UserName string `json:"userName"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	IsAdmin  bool   `json:"isAdmin"`
}

// authentikUser is an Authentik user record.
type authentikUser struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

// navidromeAPI is the typed surface over Navidrome's own HTTP API. Navidrome
// uses a non-standard auth header (X-ND-Authorization), so the token is
// attached explicitly per call.
type navidromeAPI struct {
	cl *appclient.Client
}

// newNavidromeAPI builds the own-API client against a base-URL resolver.
func newNavidromeAPI(f configurator.ClientFactory, baseURLFn func() string) *navidromeAPI {
	return &navidromeAPI{cl: f.New(appclient.Spec{Name: "navidrome", BaseURLFn: baseURLFn})}
}

// createAdmin calls /auth/createAdmin (only works when no users exist) and
// returns the admin token.
func (a *navidromeAPI) createAdmin(ctx context.Context, username, password string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	err := a.cl.POST("/auth/createAdmin").
		Anonymous().
		JSON(map[string]string{"username": username, "password": password}).
		OK(http.StatusOK).
		DoInto(ctx, &out)
	if err != nil {
		return "", err
	}
	return out.Token, nil
}

// login exchanges credentials for a session token.
func (a *navidromeAPI) login(ctx context.Context, username, password string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	err := a.cl.POST("/auth/login").
		Anonymous().
		JSON(map[string]string{"username": username, "password": password}).
		OK(http.StatusOK).
		DoInto(ctx, &out)
	if err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("empty token")
	}
	return out.Token, nil
}

// listUsers returns all users from Navidrome.
func (a *navidromeAPI) listUsers(ctx context.Context, token string) ([]navidromeUser, error) {
	var users []navidromeUser
	err := a.cl.GET("/api/user").
		Header("X-ND-Authorization", "Bearer "+token).
		Query("_end", "500").Query("_start", "0").
		Query("_order", "ASC").Query("_sort", "id").
		OK(http.StatusOK).
		DoInto(ctx, &users)
	return users, err
}

// createUser creates a user in Navidrome. The password is a required field
// but unused under forward-auth.
func (a *navidromeAPI) createUser(ctx context.Context, token, username, name, email string) error {
	_, err := a.cl.POST("/api/user").
		Header("X-ND-Authorization", "Bearer "+token).
		JSON(map[string]any{
			"userName": username,
			"name":     name,
			"email":    email,
			"isAdmin":  false,
			"password": "placeholder",
		}).
		OK(http.StatusOK).
		Do(ctx)
	return err
}

// authentikAPI is the typed surface over the Authentik REST API (used to read
// the users to sync into Navidrome).
type authentikAPI struct {
	cl *appclient.Client
}

// newAuthentikAPI builds the Authentik client against a base-URL resolver.
func newAuthentikAPI(f configurator.ClientFactory, baseURLFn func() string) *authentikAPI {
	return &authentikAPI{cl: f.New(appclient.Spec{Name: "navidrome-authentik", BaseURLFn: baseURLFn})}
}

// listActiveUsers returns internal active users from Authentik.
func (a *authentikAPI) listActiveUsers(ctx context.Context, token string) ([]authentikUser, error) {
	var out struct {
		Results []authentikUser `json:"results"`
	}
	err := a.cl.GET("/api/v3/core/users/").
		Header("Authorization", "Bearer "+token).
		Header("Accept", "application/json").
		Query("type", "internal").Query("is_active", "true").Query("page_size", "100").
		OK(http.StatusOK).
		DoInto(ctx, &out)
	return out.Results, err
}