// Package appconfig registers all app configurators with the registry.
package appconfig

import (
	"fmt"
	"path/filepath"

	adguardhome "codeberg.org/d-buckner/bloud-v3/apps/adguard-home"
	"codeberg.org/d-buckner/bloud-v3/apps/authentik"
	"codeberg.org/d-buckner/bloud-v3/apps/jellyfin"
	"codeberg.org/d-buckner/bloud-v3/apps/miniflux"
	"codeberg.org/d-buckner/bloud-v3/apps/qbittorrent"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/config"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/web/static"
)

// RegisterAll registers all available configurators with the registry.
// This should be called during host-agent startup.
func RegisterAll(registry *configurator.Registry, cfg *config.Config) {
	traefikDynamicDir := filepath.Join(cfg.DataDir, "traefik", "dynamic")

	// Register configurators from apps/ directory
	registry.Register(adguardhome.NewConfigurator(3080))
	registry.Register(authentik.NewConfigurator(
		cfg.AuthentikPort,
		cfg.AuthentikAdminPassword,
		cfg.AuthentikAdminEmail,
		cfg.AuthentikToken,
		cfg.LDAPBindPassword,
		cfg.DataDir,
		static.AuthentikBrandingCSS,
	))
	registry.Register(miniflux.NewConfigurator(8085, traefikDynamicDir))
	registry.Register(qbittorrent.NewConfigurator(8086))
	registry.Register(jellyfin.NewConfigurator(8096, fmt.Sprintf("http://localhost:%d", cfg.AuthentikPort), cfg.AuthentikToken))
}
