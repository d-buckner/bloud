// SPDX-License-Identifier: AGPL-3.0-only

package jellyfin

import (
	"context"
	"fmt"
)

// completeStartupWizard drives Jellyfin's initial setup wizard end to end:
// wait for the wizard API to be reachable, then post configuration, create the
// bootstrap admin, enable remote access, and mark it complete. Each step is a
// declared call on the typed client; this function is the orchestration.
func (c *Configurator) completeStartupWizard(ctx context.Context) error {
	// Wait for the wizard API itself: Jellyfin returns 503 with HTML while
	// initializing even if /health is OK, and returns 401 when already complete.
	if err := c.api.waitForStartupWizardReady(ctx); err != nil {
		return fmt.Errorf("waiting for startup wizard: %w", err)
	}

	if err := c.api.setStartupConfiguration(ctx); err != nil {
		return fmt.Errorf("setting startup configuration: %w", err)
	}

	adminPassword, err := c.resolveAdminPassword()
	if err != nil {
		return err
	}
	if err := c.api.setStartupUser(ctx, bootstrapUsername, adminPassword); err != nil {
		return fmt.Errorf("creating startup user: %w", err)
	}

	if err := c.api.setRemoteAccess(ctx); err != nil {
		return fmt.Errorf("setting remote access: %w", err)
	}

	if err := c.api.completeWizard(ctx); err != nil {
		return fmt.Errorf("completing wizard: %w", err)
	}

	return nil
}
