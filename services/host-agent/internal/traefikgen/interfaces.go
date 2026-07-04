package traefikgen

import "codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"

// RemoteAppRoute describes a remote (shared) app that should be proxied through Traefik.
type RemoteAppRoute struct {
	ID       string // unique slug for router/service naming (e.g. "jellyfin-johan")
	ProxyURL string // localhost reverse proxy URL (e.g. "http://localhost:10100")
}

// GeneratorInterface defines the interface for generating Traefik configuration.
// This interface enables mocking for testing.
type GeneratorInterface interface {
	// Generate creates Traefik routes for the given installed apps
	Generate(apps []*catalog.App) error

	// GenerateAll creates Traefik routes for local apps, remote (shared) apps,
	// and tailnet-specific routes when a tailnet domain is active.
	GenerateAll(apps []*catalog.App, remoteApps []RemoteAppRoute, tailnetDomain string) error

	// SetAuthentikEnabled updates the Authentik status for SSO middleware generation
	SetAuthentikEnabled(enabled bool)

	// Preview generates a preview of what the config will look like
	Preview(apps []*catalog.App) string
}

// Compile-time assertion
var _ GeneratorInterface = (*Generator)(nil)
