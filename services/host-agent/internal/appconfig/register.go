// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package appconfig registers all available configurators with the registry.
package appconfig

import (
	"fmt"
	"log/slog"

	"codeberg.org/d-buckner/bloud/apps/affine"
	"codeberg.org/d-buckner/bloud/apps/appflowy"
	"codeberg.org/d-buckner/bloud/apps/authentik"
	"codeberg.org/d-buckner/bloud/apps/immich"
	"codeberg.org/d-buckner/bloud/apps/jellyfin"
	"codeberg.org/d-buckner/bloud/apps/navidrome"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/config"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/web/static"
)

// RegisterAll registers all available configurators with the registry.
// This should be called during host-agent startup. hosts is the live host-set
// state (may be nil in tests/CLI mode); configurators read the current base
// URL through it so UI host changes take effect without restarts.
func RegisterAll(
	registry *configurator.Registry,
	cfg *config.Config,
	runtime containerruntime.Runtime,
	catalogApps map[string]*catalog.App,
	logger *slog.Logger,
	templateVars map[string]string,
	hosts *hostset.State,
) {
	// primaryBaseURLFn resolves the current primary host's base URL, falling
	// back to the static env value when no host state is configured.
	primaryBaseURLFn := func() string {
		if hosts != nil {
			return hosts.Get().PrimaryBaseURL()
		}
		return cfg.SSOBaseURL
	}

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
		).WithBaseURLFn(primaryBaseURLFn))
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
		).WithBaseURLFn(primaryBaseURLFn))
	}

	// User app configurators (always registered)
	registry.Register(appflowy.NewConfigurator(8480, logger))
	registry.Register(affine.NewConfigurator(3010, primaryBaseURLFn, cfg.Secrets, logger))
	// Navidrome's user sync calls the Authentik API from the host itself, so it
	// always uses the local Traefik URL regardless of the public host set.
	registry.Register(navidrome.NewConfigurator(4533, fmt.Sprintf("http://localhost:%d", cfg.TraefikPort), cfg.Secrets, logger))
	registry.Register(jellyfin.NewConfigurator(8096, logger))
	registry.Register(immich.NewConfigurator(2283, cfg.Secrets, logger))
}
