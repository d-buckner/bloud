// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// AuthResponse represents the authentication response
type AuthResponse struct {
	AccessToken string `json:"AccessToken"`
	User        struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	} `json:"User"`
}

// authenticate logs in to Jellyfin and returns an access token

// authenticate logs in to Jellyfin and returns an access token
func (c *Configurator) authenticate(ctx context.Context, username, password string) (string, error) {
	url := c.getBaseURL() + "/Users/AuthenticateByName"

	body := map[string]string{
		"Username": username,
		"Pw":       password,
	}

	bodyBytes, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// Jellyfin 10.11+ only parses Client/Device from the Authorization header,
	// not X-Emby-Authorization. Using the wrong header causes request.App=null crashes.
	req.Header.Set("Authorization", `MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0"`)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("authentication failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var authResp AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return "", err
	}

	return authResp.AccessToken, nil
}

// getPluginConfiguration fetches a plugin's configuration

// getPluginConfiguration fetches a plugin's configuration
func (c *Configurator) getPluginConfiguration(ctx context.Context, token, pluginID string) ([]byte, error) {
	url := fmt.Sprintf("%s/Plugins/%s/Configuration", c.getBaseURL(), pluginID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return io.ReadAll(resp.Body)
}

// setPluginConfiguration updates a plugin's configuration

// setPluginConfiguration updates a plugin's configuration
func (c *Configurator) setPluginConfiguration(ctx context.Context, token, pluginID string, config []byte) error {
	url := fmt.Sprintf("%s/Plugins/%s/Configuration", c.getBaseURL(), pluginID)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(config))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// User represents a Jellyfin user

// User represents a Jellyfin user
type User struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

// getUsers fetches all users from Jellyfin

// getUsers fetches all users from Jellyfin
func (c *Configurator) getUsers(ctx context.Context, token string) ([]User, error) {
	url := c.getBaseURL() + "/Users"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var users []User
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}

	return users, nil
}

// deleteUser deletes a user by ID

// deleteUser deletes a user by ID
func (c *Configurator) deleteUser(ctx context.Context, token, userID string) error {
	url := fmt.Sprintf("%s/Users/%s", c.getBaseURL(), userID)

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"`, token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// deleteBootstrapAdmin removes the bootstrap admin user

// deleteBootstrapAdmin removes the bootstrap admin user
func (c *Configurator) deleteBootstrapAdmin(ctx context.Context, token string) error {
	users, err := c.getUsers(ctx, token)
	if err != nil {
		return fmt.Errorf("getting users: %w", err)
	}

	for _, user := range users {
		if user.Name == bootstrapUsername {
			c.logger.Info("deleting bootstrap admin user")
			if err := c.deleteUser(ctx, token, user.ID); err != nil {
				return fmt.Errorf("deleting user: %w", err)
			}
			c.logger.Info("bootstrap admin deleted")
			return nil
		}
	}

	c.logger.Info("bootstrap admin not found (may already be deleted)")
	return nil
}
