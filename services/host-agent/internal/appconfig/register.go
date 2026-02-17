// Package appconfig registers all app configurators with the registry.
package appconfig

import (
	"fmt"
	"path/filepath"

	actualbudget "codeberg.org/d-buckner/bloud-v3/apps/actual-budget"
	adguardhome "codeberg.org/d-buckner/bloud-v3/apps/adguard-home"
	"codeberg.org/d-buckner/bloud-v3/apps/affine"
	"codeberg.org/d-buckner/bloud-v3/apps/authentik"
	"codeberg.org/d-buckner/bloud-v3/apps/jellyfin"
	"codeberg.org/d-buckner/bloud-v3/apps/jellyseerr"
	"codeberg.org/d-buckner/bloud-v3/apps/miniflux"
	"codeberg.org/d-buckner/bloud-v3/apps/prowlarr"
	"codeberg.org/d-buckner/bloud-v3/apps/qbittorrent"
	"codeberg.org/d-buckner/bloud-v3/apps/radarr"
	"codeberg.org/d-buckner/bloud-v3/apps/sonarr"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/config"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// RegisterAll registers all available configurators with the registry.
// This should be called during host-agent startup.
func RegisterAll(registry *configurator.Registry, cfg *config.Config) {
	traefikDynamicDir := filepath.Join(cfg.DataDir, "traefik", "dynamic")

	// Register configurators from apps/ directory
	registry.Register(actualbudget.NewConfigurator(5006))
	registry.Register(adguardhome.NewConfigurator(3080))
	registry.Register(affine.NewConfigurator(3010, cfg.DataDir))
	registry.Register(authentik.NewConfigurator(
		cfg.AuthentikPort,
		cfg.AuthentikAdminPassword,
		cfg.AuthentikAdminEmail,
		cfg.AuthentikToken,
		cfg.LDAPBindPassword,
		cfg.DataDir,
	))
	registry.Register(miniflux.NewConfigurator(8085, traefikDynamicDir))
	registry.Register(qbittorrent.NewConfigurator(8086))
	registry.Register(radarr.NewConfigurator(7878))
	registry.Register(sonarr.NewConfigurator(8989))
	registry.Register(prowlarr.NewConfigurator(9696))
	registry.Register(jellyfin.NewConfigurator(8096, fmt.Sprintf("http://localhost:%d", cfg.AuthentikPort), cfg.AuthentikToken))
	registry.Register(jellyseerr.NewConfigurator(5055))
}
