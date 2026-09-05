// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package appconfig wires system-infrastructure configurators with the
// registry and links in the app catalog's self-registering configurators.
//
// User-app configurators are NOT registered here: each app package in the
// apps/ module registers a factory from its own init() (see
// apps/<name>/registration.go), and the registry instantiates them lazily on
// first lookup. This file only imports the app packages for their side
// effects and wires the system configurators (Traefik, Authentik server),
// which are always needed and runtime-dependent.
package appconfig

import (
	"log/slog"

	"codeberg.org/d-buckner/bloud/apps/authentik"

	// User-app configurators: blank imports run each app's init(), which
	// registers its factory with the configurator registry. Adding an app
	// means adding one blank import here — and nothing else in host-agent.
	_ "codeberg.org/d-buckner/bloud/apps/affine"
	_ "codeberg.org/d-buckner/bloud/apps/homeassistant"
	_ "codeberg.org/d-buckner/bloud/apps/immich"
	_ "codeberg.org/d-buckner/bloud/apps/jellyfin"
	_ "codeberg.org/d-buckner/bloud/apps/navidrome"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/config"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/web/static"
)

// RegisterSystem registers the system configurators (Traefik, Authentik
// server) with the registry. This should be called during host-agent startup.
// hosts is the live host-set state (may be nil in tests/CLI mode);
// configurators read the current base URL through it so UI host changes take
// effect without restarts.
func RegisterSystem(
	registry *configurator.Registry,
	cfg *config.Config,
	runtime containerruntime.Runtime,
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
}

// AppDeps builds the dependency set passed to app configurator factories.
// hosts may be nil (tests/CLI mode), in which case the static SSO base URL
// is used.
func AppDeps(cfg *config.Config, logger *slog.Logger, hosts *hostset.State) configurator.Deps {
	primaryBaseURL := func() string {
		if hosts != nil {
			return hosts.Get().PrimaryBaseURL()
		}
		return cfg.SSOBaseURL
	}
	return configurator.Deps{
		Logger:         logger,
		Secrets:        cfg.Secrets,
		PrimaryBaseURL: primaryBaseURL,
		TraefikPort:    cfg.TraefikPort,
	}
}
