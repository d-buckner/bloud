package traefikgen

import "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"

// GeneratorInterface defines the interface for generating Traefik configuration.
// This interface enables mocking for testing.
type GeneratorInterface interface {
	// Generate creates Traefik routes for the given installed apps
	Generate(apps []*catalog.App) error

	// SetAuthentikEnabled updates the Authentik status for SSO middleware generation
	SetAuthentikEnabled(enabled bool)

	// Preview generates a preview of what the config will look like
	Preview(apps []*catalog.App) string
}

// Compile-time assertion
var _ GeneratorInterface = (*Generator)(nil)
