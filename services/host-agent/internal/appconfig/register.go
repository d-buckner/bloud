// Package appconfig registers all app configurators with the registry.
package appconfig

import (
	"codeberg.org/d-buckner/bloud-v3/apps/authentik"
	"codeberg.org/d-buckner/bloud-v3/apps/jellyfin"
	"codeberg.org/d-buckner/bloud-v3/apps/navidrome"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/config"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/web/static"
)

// RegisterAll registers all available configurators with the registry.
// This should be called during host-agent startup.
func RegisterAll(registry *configurator.Registry, cfg *config.Config) {
	// Register configurators from apps/ directory
	registry.Register(authentik.NewConfigurator(
		cfg.AuthentikPort,
		cfg.AuthentikAdminPassword,
		cfg.AuthentikAdminEmail,
		cfg.AuthentikToken,
		cfg.LDAPBindPassword,
		cfg.DataDir,
		static.AuthentikBrandingCSS,
	).WithBaseURL(cfg.SSOBaseURL))
	registry.Register(navidrome.NewConfigurator(4533, cfg.SSOAuthentikURL, cfg.Secrets))
	registry.Register(jellyfin.NewConfigurator(8096))
}
