package sso

import "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"

// BlueprintGeneratorInterface defines the interface for generating Authentik blueprints.
// This interface enables mocking for testing.
type BlueprintGeneratorInterface interface {
	// GenerateForApp generates an Authentik blueprint for an app with SSO
	GenerateForApp(app *catalog.App) error

	// DeleteBlueprint removes the blueprint file for an app
	DeleteBlueprint(appName string) error

	// GetSSOEnvVars returns the environment variables needed for an app's SSO config
	GetSSOEnvVars(app *catalog.App) map[string]string
}

// Compile-time assertion
var _ BlueprintGeneratorInterface = (*BlueprintGenerator)(nil)
