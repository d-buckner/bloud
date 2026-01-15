package sso

import (
	"context"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
)

// BlueprintGeneratorInterface defines the interface for generating Authentik blueprints.
// This interface enables mocking for testing.
type BlueprintGeneratorInterface interface {
	// GenerateForApp generates an Authentik blueprint for an app with SSO
	GenerateForApp(app *catalog.App) error

	// DeleteBlueprint removes the blueprint file for an app
	DeleteBlueprint(appName string) error

	// GetSSOEnvVars returns the environment variables needed for an app's SSO config
	GetSSOEnvVars(app *catalog.App) map[string]string

	// GenerateOutpostBlueprint creates or updates the outpost blueprint with all forward-auth providers
	GenerateOutpostBlueprint(providers []ForwardAuthProvider) error

	// GenerateLDAPOutpostBlueprint creates the LDAP provider, service account, and outpost
	// This is called when any app with LDAP strategy is installed
	GenerateLDAPOutpostBlueprint(apps []LDAPApp, ldapBindPassword string) error

	// GetLDAPBindPassword returns the LDAP bind password for apps to use
	GetLDAPBindPassword() string

	// GetLDAPOutpostToken queries Authentik API to get the auto-generated LDAP outpost token
	// This should be called after the blueprint is applied and the outpost is created
	GetLDAPOutpostToken(ctx context.Context, authentikURL, apiToken string) (string, error)
}

// Compile-time assertion
var _ BlueprintGeneratorInterface = (*BlueprintGenerator)(nil)
