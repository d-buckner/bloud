// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/api"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/appconfig"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/config"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/db"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/system"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

func main() {
	// Check for subcommands
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "configure":
			os.Exit(runConfigure(os.Args[2:]))
		case "init-secrets":
			os.Exit(runInitSecrets(os.Args[2:]))
		}
	}

	// Default: run the server
	runServer()
}

func runServer() {
	// Setup structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	logger.Info("starting Bloud host agent")

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}
	logger.Info("loaded configuration",
		"port", cfg.Port,
		"data_dir", cfg.DataDir,
		"apps_dir", cfg.AppsDir,
	)

	// Ensure data directory exists for SQLite
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		logger.Error("failed to create data directory", "error", err)
		os.Exit(1)
	}

	// Initialize SQLite database (instant: no postgres dependency)
	database, err := db.InitDB(cfg.DataDir)
	if err != nil {
		logger.Error("failed to initialize database", "error", err)
		os.Exit(1)
	}
	defer func() { _ = database.Close() }()
	logger.Info("database initialized successfully")

	// Create PodmanRuntime for system app configurators
	client, err := podman.NewClient()
	if err != nil {
		logger.Error("failed to create podman client", "error", err)
		os.Exit(1)
	}
	runtime := containerruntime.NewPodmanRuntime(client)

	// Host state: the effective set of hostnames (built-ins + admin custom
	// hosts from the database, with legacy env fallbacks). Shared between the
	// configurators, the orchestrator, and the API so UI host changes apply
	// without a restart.
	hostSet, hostStore := resolveHostSet(database, cfg, logger)
	hosts := hostset.NewState(hostSet)

	// templateVars is shared by reference with the authentik configurator, so the
	// LDAP token its PostStart writes is visible to the orchestrator.
	templateVars := buildTemplateVars(cfg)

	// Configurator registry: system configurators are registered eagerly;
	// app configurators self-register factories (apps/<name>/registration.go)
	// and are instantiated lazily on first lookup.
	//
	// restartContainer forces a running container to stop and start again, so
	// its process re-execs and re-reads on-disk config. Configurators use this
	// where the app's own in-app restart is unreliable under a container init
	// (Home Assistant). Stop grace lets the app shut down cleanly before SIGKILL.
	restartContainer := func(ctx context.Context, name string) error {
		if err := client.StopContainer(ctx, name, 30); err != nil {
			return err
		}
		return client.StartContainer(ctx, name)
	}
	registry := configurator.NewRegistry(logger, appconfig.AppDeps(cfg, logger, hosts, restartContainer, client.ExecWithEnv))
	appconfig.RegisterSystem(cfg, runtime, templateVars)

	// Event bus: shared between the API (SSE streams) and background consumers.
	eventsBus := eventbus.New()

	// Create HTTP server (orchestrator created + started inside)
	server := api.NewServer(database, api.ServerConfig{
		RefreshAuthentikToken: func() string { return cfg.ReadAuthentikToken(logger) },
		AppsDir:               cfg.AppsDir,
		DataDir:               cfg.DataDir,
		TraefikDynamicDir:     cfg.TraefikDynamicDir,
		BaseDomain:            cfg.BaseDomain,
		TraefikPort:           cfg.TraefikPort,
		Port:                  cfg.Port,
		SSOHostSecret:         cfg.SSOHostSecret,
		SSOBaseURL:            cfg.SSOBaseURL,
		SSOAuthentikURL:       cfg.SSOAuthentikURL,
		SSOIssuerURL:          cfg.SSOIssuerURL,
		AuthentikToken:        cfg.AuthentikToken,
		AuthentikPort:         cfg.AuthentikPort,
		TSAuthKey:             cfg.TSAuthKey,
		HostLabel:             cfg.HostLabel,
		TrustedLocalNets:      cfg.TrustedLocalNets,
		APIToken:              cfg.APIToken,
		Hosts:                 hosts,
		EventsBus:             eventsBus,
		HostStore:             hostStore,
		LDAPOutput:            cfg.LDAPOutput(),
		Registry:              registry,
		TemplateVars:          templateVars,
	}, logger)

	// Open the listener before convergence. Until the orchestrator reports
	// ready the server answers with a static loading page (and 503 for /api),
	// so a browser hitting Traefik during bootstrap sees the page instead of
	// Traefik's 502. waitForSystemConvergence still gates the API surface.
	go func() {
		if err := server.Start(); err != nil {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	waitForSystemConvergence(server, logger)

	// Setup graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start background system stats collector
	system.StartStatsCollector(ctx)

	// Start background purge of expired sessions (SQLite has no TTL)
	store.StartSessionPurger(ctx, store.NewSessionStore(database), logger)

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Info("shutdown signal received")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}

	logger.Info("server stopped gracefully")
}

// resolveHostSet computes the effective host set from stored admin hosts and the
// legacy env fallbacks, and returns it with the host store the caller wires into
// the API. Resolution failures degrade to the built-in set rather than aborting
// boot; the instance must still come up reachable on localhost.
func resolveHostSet(database *sql.DB, cfg *config.Config, logger *slog.Logger) (hostset.HostSet, *store.HostStore) {
	hostStore := store.NewHostStore(database)
	var storedHosts []hostset.StoredHost
	if stored, err := hostStore.List(); err != nil {
		logger.Warn("failed to load stored hosts, using defaults", "error", err)
	} else {
		for _, h := range stored {
			storedHosts = append(storedHosts, hostset.StoredHost{Hostname: h.Hostname, Primary: h.Primary})
		}
	}
	hostSet, err := hostset.Resolve(hostset.Input{
		Stored:     storedHosts,
		BaseDomain: cfg.BaseDomain,
		SSOBaseURL: cfg.SSOBaseURL,
	})
	if err != nil {
		logger.Warn("failed to resolve host set, using defaults", "error", err)
		hostSet = hostset.New(hostset.BuiltinHosts, hostset.DefaultPrimary)
	}
	logger.Info("host set resolved", "hosts", hostSet.Hosts(), "primary", hostSet.Primary())
	return hostSet, hostStore
}

// buildTemplateVars renders the shared template-variable map passed to the
// orchestrator and the authentik configurator. authentikLdapToken is empty here
// and written at runtime by apps-authentik-server's PostStart, so the same map
// instance must be shared by both consumers.
func buildTemplateVars(cfg *config.Config) map[string]string {
	return map[string]string{
		"postgresPassword":        cfg.PostgresPassword,
		"authentikSecretKey":      cfg.Secrets.GetAuthentikSecretKey(),
		"authentikBootstrapToken": cfg.Secrets.GetAuthentikBootstrapToken(),
		"authentikAdminPassword":  cfg.AuthentikAdminPassword,
		"authentikAdminEmail":     cfg.AuthentikAdminEmail,
		"authentikLdapToken":      "",
	}
}

// waitForSystemConvergence blocks until the orchestrator reports ready and the
// system apps pass their health check, then initialises auth. It aborts the
// process on a failed health check or a 10-minute timeout. The listener is
// already open here, but bootstrapGate keeps the API unavailable until this
// returns: the API must not serve before the system apps it depends on are
// running.
func waitForSystemConvergence(server *api.Server, logger *slog.Logger) {
	logger.Info("waiting for system apps to converge")
	readyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	select {
	case <-server.OrchestratorReady():
		if err := server.CheckSystemHealth(); err != nil {
			logger.Error("system app failed during startup", "error", err)
			os.Exit(1)
		}
		logger.Info("system apps converged successfully")
		server.InitAuth()
	case <-readyCtx.Done():
		logger.Error("system startup timed out")
		os.Exit(1)
	}
}
