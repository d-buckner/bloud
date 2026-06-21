package orchestrator

import (
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
)

// buildIntegrationConfig builds the integration configuration map from user choices,
// auto-config tasks, and integration choices. This is a pure function for testability.
//
// Priority (highest to lowest):
// 1. Auto-configured integrations - required for app functionality, always applied
// 2. User choices - explicit user selections for optional integrations
// 3. Required choice defaults - fallback when no other value exists
//
// Note: AutoConfig overwrites user choices because auto-config represents
// configurations that are required for the app to function correctly.
func buildIntegrationConfig(
	userChoices map[string]string,
	autoConfig []catalog.ConfigTask,
	choices []catalog.IntegrationChoice,
) map[string]string {
	config := make(map[string]string)

	// Copy user choices first
	if userChoices != nil {
		for k, v := range userChoices {
			config[k] = v
		}
	}

	// Auto-config overwrites user choices - these are required for functionality
	for _, auto := range autoConfig {
		config[auto.Integration] = auto.Source
	}

	// For required integrations with no value yet, use recommended default
	for _, choice := range choices {
		if _, hasChoice := config[choice.Integration]; hasChoice {
			continue // Already have a value
		}
		if !choice.Required {
			continue // Not required, skip
		}
		if choice.Recommended != "" {
			config[choice.Integration] = choice.Recommended
		}
	}

	return config
}

// shouldCleanupAuthentik determines if Authentik SSO cleanup should be performed.
// Returns true if the app has a valid SSO strategy that requires Authentik cleanup.
func shouldCleanupAuthentik(catalogApp *catalog.App) bool {
	if catalogApp == nil {
		return false
	}
	strategy := catalogApp.SSO.Strategy
	return strategy != "" && strategy != "none"
}


