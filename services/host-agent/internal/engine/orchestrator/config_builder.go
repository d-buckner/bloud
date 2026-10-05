// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
)

// shouldCleanupAuthentik determines if Authentik SSO cleanup should be performed.
// Returns true if the app has a valid SSO strategy that requires Authentik cleanup.
func shouldCleanupAuthentik(catalogApp *catalog.App) bool {
	if catalogApp == nil {
		return false
	}
	strategy := catalogApp.SSO.Strategy
	return strategy != "" && strategy != "none"
}
