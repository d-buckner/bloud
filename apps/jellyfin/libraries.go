// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package jellyfin

import (
	"context"
	"fmt"
)

// defaultLibraries are the media libraries Bloud provisions on every install.
var defaultLibraries = []struct {
	name           string
	collectionType string
	path           string
}{
	{"Movies", "movies", "/movies"},
	{"Shows", "shows", "/shows"},
}

// configureLibraries sets up the default media libraries, creating only the
// ones that don't already exist.
func (c *Configurator) configureLibraries(ctx context.Context) error {
	adminPassword, err := c.resolveAdminPassword()
	if err != nil {
		return err
	}
	token, err := c.api.authenticate(ctx, bootstrapUsername, adminPassword)
	if err != nil {
		return fmt.Errorf("authenticating: %w", err)
	}

	existingLibraries, err := c.api.getVirtualFolders(ctx, token)
	if err != nil {
		return fmt.Errorf("getting libraries: %w", err)
	}

	existingNames := make(map[string]bool, len(existingLibraries))
	for _, lib := range existingLibraries {
		existingNames[lib.Name] = true
	}

	for _, lib := range defaultLibraries {
		if existingNames[lib.name] {
			c.logger.Info("media library already exists", "library", lib.name)
			continue
		}
		c.logger.Info("creating media library", "library", lib.name, "path", lib.path)
		if err := c.api.addVirtualFolder(ctx, token, lib.name, lib.collectionType, lib.path); err != nil {
			return fmt.Errorf("creating library %s: %w", lib.name, err)
		}
	}

	return nil
}
