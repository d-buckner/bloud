// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package appconfig wires system-infrastructure configurators with the
// registry and links in the app catalog's self-registering configurators.
//
// User-app configurators are NOT registered here: the apps module owns that
// list (see apps/registry.go). Calling apps.RegisterAll() links every user-app
// factory, which each app package registers from its own init() (see
// apps/<name>/registration.go); the registry instantiates them lazily on first
// lookup. This file wires only the system configurators (Traefik, Authentik
// server), which are always needed and runtime-dependent.
//
// Adding an app therefore touches apps/registry.go only: never this file.
package appconfig

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/apps"
	"codeberg.org/d-buckner/bloud/apps/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/config"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appasset"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/web/static"
)

// RegisterSystem registers the system configurators (Traefik, Authentik server)
// as lazy factories, the same way the user app catalog registers its own, and
// links the user-app factories.
//
// A factory is only instantiated on first lookup (see
// configurator.RegisterFactory), so the Traefik configurator, which needs a
// container runtime, is not built in a mode that has none, and both system
// configurators receive the same Deps bundle the user apps do.
func RegisterSystem(
	cfg *config.Config,
	runtime containerruntime.Runtime,
	templateVars map[string]string,
) {
	// Link every user-app configurator factory before wiring the system ones.
	// Idempotent: the registration itself already ran in each app's init().
	apps.RegisterAll()

	configurator.RegisterFactory("apps-traefik", func(configurator.Deps) (configurator.NodeLifecycle, error) {
		if runtime == nil {
			return nil, fmt.Errorf("traefik configurator requires a container runtime")
		}
		return NewTraefikConfigurator(
			runtime,
			cfg.TraefikPort,
			cfg.Port,
			cfg.AuthentikPort,
			cfg.DataDir,
		), nil
	})

	configurator.RegisterFactory("apps-authentik-server", func(deps configurator.Deps) (configurator.NodeLifecycle, error) {
		return authentik.NewServerConfigurator(deps, authentik.Params{
			Port:              cfg.AuthentikPort,
			BootstrapPassword: cfg.AuthentikAdminPassword,
			BootstrapEmail:    cfg.AuthentikAdminEmail,
			TokenKey:          cfg.AuthentikToken,
			LDAPBindPassword:  cfg.LDAPBindPassword,
			BrandingCSS:       static.AuthentikBrandingCSS,
			AppsDir:           cfg.AppsDir,
			TemplateVars:      templateVars,
		}), nil
	})
}

// AppDeps builds the dependency set passed to app configurator factories.
// hosts may be nil (tests/CLI mode), in which case the static SSO base URL
// is used.
// restartContainer (may be nil in CLI/tests) is the host-runtime callback
// configurators use to force a running container to re-exec and reload
// on-disk config; it is plumbed straight into Deps.
// exec (may be nil in CLI/tests) is the host-runtime callback configurators use
// to run a command inside a container and read its output; it is plumbed
// straight into Deps.Exec.
func AppDeps(cfg *config.Config, logger *slog.Logger, hosts *hostset.State, restartContainer func(ctx context.Context, name string) error, exec configurator.ExecFunc) configurator.Deps {
	primaryBaseURL := func() string {
		if hosts != nil {
			return hosts.Get().PrimaryBaseURL()
		}
		return cfg.SSOBaseURL
	}
	// One process-shared transport for every app HTTP client: connection
	// pooling across reconciliation cycles, one dial timeout, one keep-alive
	// policy for the whole runtime.
	transport := appclient.DefaultTransport()
	return configurator.Deps{
		Logger:           logger,
		Secrets:          cfg.Secrets,
		PrimaryBaseURL:   primaryBaseURL,
		TraefikPort:      cfg.TraefikPort,
		RestartContainer: restartContainer,
		Exec:             exec,
		HTTP: configurator.ClientFactory{
			Transport: transport,
			Retry:     appclient.DefaultRetry,
			Logger:    logger,
		},
		Assets: appasset.Installer{
			CacheDir: filepath.Join(cfg.DataDir, "asset-cache"),
			Retry:    appclient.DefaultRetry,
		},
	}
}
