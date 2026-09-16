// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// VirtualFolder represents a Jellyfin library
type VirtualFolder struct {
	Name           string   `json:"Name"`
	Locations      []string `json:"Locations"`
	CollectionType string   `json:"CollectionType"`
	ItemId         string   `json:"ItemId"`
}

// configureLibraries sets up the default media libraries

// configureLibraries sets up the default media libraries
func (c *Configurator) configureLibraries(ctx context.Context) error {
	// Authenticate first
	adminPassword, err := c.resolveAdminPassword()
	if err != nil {
		return err
	}
	token, err := c.authenticate(ctx, bootstrapUsername, adminPassword)
	if err != nil {
		return fmt.Errorf("authenticating: %w", err)
	}

	// Get existing libraries
	existingLibraries, err := c.getVirtualFolders(ctx, token)
	if err != nil {
		return fmt.Errorf("getting libraries: %w", err)
	}

	// Create a map of existing library names
	existingNames := make(map[string]bool)
	for _, lib := range existingLibraries {
		existingNames[lib.Name] = true
	}

	// Define libraries to create
	libraries := []struct {
		name           string
		collectionType string
		path           string
	}{
		{"Movies", "movies", "/movies"},
		{"Shows", "shows", "/shows"},
	}

	for _, lib := range libraries {
		if existingNames[lib.name] {
			c.logger.Info("media library already exists", "library", lib.name)
			continue
		}

		c.logger.Info("creating media library", "library", lib.name, "path", lib.path)
		if err := c.addVirtualFolder(ctx, token, lib.name, lib.collectionType, lib.path); err != nil {
			return fmt.Errorf("creating library %s: %w", lib.name, err)
		}
	}

	return nil
}

// getVirtualFolders returns all configured libraries

// getVirtualFolders returns all configured libraries
func (c *Configurator) getVirtualFolders(ctx context.Context, token string) ([]VirtualFolder, error) {
	url := c.getBaseURL() + "/Library/VirtualFolders"

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

	var folders []VirtualFolder
	if err := json.NewDecoder(resp.Body).Decode(&folders); err != nil {
		return nil, err
	}

	return folders, nil
}

// addVirtualFolder creates a new library

// addVirtualFolder creates a new library
func (c *Configurator) addVirtualFolder(ctx context.Context, token, name, collectionType, path string) error {
	// The API uses query parameters for the folder metadata
	reqURL := fmt.Sprintf("%s/Library/VirtualFolders?name=%s&collectionType=%s&paths=%s&refreshLibrary=false",
		c.getBaseURL(), url.QueryEscape(name), url.QueryEscape(collectionType), url.QueryEscape(path))

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, nil)
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

// LDAPConfig represents the LDAP plugin configuration
