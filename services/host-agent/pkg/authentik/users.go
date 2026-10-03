// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

func (c *Client) addUserToGroup(ctx context.Context, userID int, groupName string) error {
	// Find the group
	groupID, err := c.findGroupID(ctx, groupName)
	if err != nil {
		return err
	}

	// Add user to group using the group's add_user endpoint.
	// 204 = success, 200 = already in group (idempotent)
	if err := c.cl.POST("/api/v3/core/groups/"+groupID+"/add_user/").
		JSON(map[string]int{"pk": userID}).
		OK(http.StatusNoContent, http.StatusOK).
		Exec(ctx); err != nil {
		return fmt.Errorf("adding user to group: %w", err)
	}

	return nil
}

// CreateUser creates a new user in Authentik and sets their password.
// The user gets a derived identity email (username@<domain>): SSO apps
// (e.g. AFFiNE) require a valid RFC-style email to create app accounts.
func (c *Client) CreateUser(ctx context.Context, username, password string) (int, error) {
	// Create the user
	payload := map[string]interface{}{
		"username":  username,
		"name":      username,
		"email":     c.ManagedUserEmail(username),
		"path":      "users",
		"is_active": true,
	}
	var result struct {
		PK int `json:"pk"`
	}
	if err := c.cl.POST("/api/v3/core/users/").JSON(payload).OK(http.StatusCreated).DoInto(ctx, &result); err != nil {
		return 0, fmt.Errorf("creating user: %w", err)
	}

	// Set the user's password
	if err := c.setUserPassword(ctx, result.PK, password); err != nil {
		return 0, fmt.Errorf("setting password: %w", err)
	}

	return result.PK, nil
}

// setUserPassword sets a user's password via the Authentik API
func (c *Client) setUserPassword(ctx context.Context, userID int, password string) error {
	// 204 No Content = success
	if err := c.cl.POST(fmt.Sprintf("/api/v3/core/users/%d/set_password/", userID)).
		JSON(map[string]string{"password": password}).
		OK(http.StatusNoContent, http.StatusOK).
		Exec(ctx); err != nil {
		return fmt.Errorf("setting password: %w", err)
	}

	return nil
}

// SetUserEmail sets the user's identity email via the Authentik API.
func (c *Client) SetUserEmail(ctx context.Context, userID int, email string) error {
	return c.cl.PATCH(fmt.Sprintf("/api/v3/core/users/%d/", userID)).
		JSON(map[string]string{"email": email}).
		OK(http.StatusOK).
		Exec(ctx)
}

// SetUserPassword sets a user's password via the Authentik API (public wrapper)
func (c *Client) SetUserPassword(ctx context.Context, userID int, password string) error {
	return c.setUserPassword(ctx, userID, password)
}

// AddUserToGroup adds a user to a group by name (public wrapper around internal method)
func (c *Client) AddUserToGroup(ctx context.Context, userID int, groupName string) error {
	return c.addUserToGroup(ctx, userID, groupName)
}

// RemoveUserFromGroup removes a user from a group by name
func (c *Client) RemoveUserFromGroup(ctx context.Context, userID int, groupName string) error {
	groupID, err := c.findGroupID(ctx, groupName)
	if err != nil {
		return err
	}

	if err := c.cl.POST("/api/v3/core/groups/"+groupID+"/remove_user/").
		JSON(map[string]int{"pk": userID}).
		OK(http.StatusNoContent, http.StatusOK).
		Exec(ctx); err != nil {
		return fmt.Errorf("removing user from group: %w", err)
	}

	return nil
}

// FindUserID finds a user ID by username (public wrapper)
func (c *Client) FindUserID(ctx context.Context, username string) (int, error) {
	return c.findUserID(ctx, username)
}

// ManagedUserInfo represents a user returned by ListUsers
type ManagedUserInfo struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	IsAdmin  bool   `json:"is_admin"`
	IsActive bool   `json:"is_active"`
}

// ListUsers fetches internal (non-service) users from Authentik and determines their roles
func (c *Client) ListUsers(ctx context.Context) ([]ManagedUserInfo, error) {
	// Fetch users of type "internal"
	var rawResult struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := c.cl.GET("/api/v3/core/users/").
		Query("type", "internal").
		Query("page_size", "200").
		OK(http.StatusOK).
		DoInto(ctx, &rawResult); err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}

	// Get the admin group members to cross-reference
	adminGroupMembers, err := c.getAdminGroupMembers(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting admin group members: %w", err)
	}

	var users []ManagedUserInfo
	for _, raw := range rawResult.Results {
		var user struct {
			PK       int    `json:"pk"`
			Username string `json:"username"`
			Name     string `json:"name"`
			Email    string `json:"email"`
			IsActive bool   `json:"is_active"`
			Type     string `json:"type"`
		}
		if err := json.Unmarshal(raw, &user); err != nil {
			continue
		}

		// Skip service accounts
		if user.Type == "service_account" {
			continue
		}

		// Skip the akadmin user (Authentik's built-in admin)
		if user.Username == "akadmin" {
			continue
		}

		users = append(users, ManagedUserInfo{
			ID:       user.PK,
			Username: user.Username,
			Name:     user.Name,
			Email:    user.Email,
			IsAdmin:  adminGroupMembers[user.PK],
			IsActive: user.IsActive,
		})
	}

	return users, nil
}

// getAdminGroupMembers returns a set of user IDs that are in the "authentik Admins" group
func (c *Client) getAdminGroupMembers(ctx context.Context) (map[int]bool, error) {
	groupID, err := c.findGroupID(ctx, "authentik Admins")
	if err != nil {
		return nil, err
	}

	var group struct {
		Users []int `json:"users"`
	}
	if err := c.cl.GET("/api/v3/core/groups/"+groupID+"/").
		OK(http.StatusOK).
		DoInto(ctx, &group); err != nil {
		return nil, fmt.Errorf("fetching group: %w", err)
	}

	members := make(map[int]bool)
	for _, uid := range group.Users {
		members[uid] = true
	}
	return members, nil
}

func (c *Client) DeleteUser(ctx context.Context, username string) error {
	// Find the user ID first
	userID, err := c.findUserID(ctx, username)
	if err != nil {
		return fmt.Errorf("finding user: %w", err)
	}
	if userID == 0 {
		return nil // User doesn't exist, nothing to delete
	}

	// 204 No Content = success, 404 = already deleted
	return c.cl.DELETE(fmt.Sprintf("/api/v3/core/users/%d/", userID)).
		OK(http.StatusNoContent, http.StatusNotFound).
		Exec(ctx)
}
