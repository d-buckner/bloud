// Package appconfig registers all app configurators with the registry.
package appconfig

import (
	"log/slog"

	"codeberg.org/d-buckner/bloud/apps/authentik"
	"codeberg.org/d-buckner/bloud/apps/jellyfin"
	"codeberg.org/d-buckner/bloud/apps/navidrome"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/config"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/web/static"
)

// RegisterAll registers all available configurators with the registry.
// This should be called during host-agent startup.
func RegisterAll(
	registry *configurator.Registry,
	cfg *config.Config,
	runtime containerruntime.Runtime,
	catalogApps map[string]*catalog.App,
	logger *slog.Logger,
	templateVars map[string]string,
) {
	// System configurators (only registered when runtime is available, i.e. server mode)
	if runtime != nil {
		registry.Register(NewTraefikConfigurator(
			runtime,
			cfg.TraefikPort,
			cfg.Port,
			cfg.AuthentikPort,
			cfg.DataDir,
		))

		registry.Register(authentik.NewServerConfigurator(
			cfg.AuthentikPort,
			cfg.AuthentikAdminPassword,
			cfg.AuthentikAdminEmail,
			cfg.AuthentikToken,
			cfg.LDAPBindPassword,
			static.AuthentikBrandingCSS,
			cfg.AppsDir,
			templateVars,
		).WithBaseURL(cfg.SSOBaseURL))
	} else {
		// CLI mode: authentik server configurator without runtime-dependent fields.
		registry.Register(authentik.NewServerConfigurator(
			cfg.AuthentikPort,
			cfg.AuthentikAdminPassword,
			cfg.AuthentikAdminEmail,
			cfg.AuthentikToken,
			cfg.LDAPBindPassword,
			static.AuthentikBrandingCSS,
			cfg.AppsDir,
			nil, // no templateVars in CLI mode
		).WithBaseURL(cfg.SSOBaseURL))
	}

	// User app configurators (always registered)
	registry.Register(navidrome.NewConfigurator(4533, cfg.SSOAuthentikURL, cfg.Secrets, logger))
	registry.Register(jellyfin.NewConfigurator(8096, logger))
}
