package traefikgen

import "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"

// RemoteAppRoute describes a remote (shared) app that should be proxied through Traefik.
type RemoteAppRoute struct {
	ID         string // unique slug for router/service naming (e.g. "jellyfin-johan")
	TailnetURL string // full URL to the remote sidecar (e.g. "https://ts-jellyfin.tail1275sa.ts.net")
}

// GeneratorInterface defines the interface for generating Traefik configuration.
// This interface enables mocking for testing.
type GeneratorInterface interface {
	// Generate creates Traefik routes for the given installed apps
	Generate(apps []*catalog.App) error

	// GenerateAll creates Traefik routes for local apps and remote (shared) apps
	GenerateAll(apps []*catalog.App, remoteApps []RemoteAppRoute) error

	// SetAuthentikEnabled updates the Authentik status for SSO middleware generation
	SetAuthentikEnabled(enabled bool)

	// Preview generates a preview of what the config will look like
	Preview(apps []*catalog.App) string
}

// Compile-time assertion
var _ GeneratorInterface = (*Generator)(nil)
