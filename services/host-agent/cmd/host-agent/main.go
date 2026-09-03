// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"context"
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
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/mdns"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/netutil"
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
		case "front-proxy":
			os.Exit(runFrontProxy())
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
	cfg := config.Load()
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

	// Initialize SQLite database (instant — no postgres dependency)
	database, err := db.InitDB(cfg.DataDir)
	if err != nil {
		logger.Error("failed to initialize database", "error", err)
		os.Exit(1)
	}
	defer database.Close()
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
	hosts := hostset.NewState(hostSet)
	logger.Info("host set resolved", "hosts", hostSet.Hosts(), "primary", hostSet.Primary())

	// templateVars is the shared mutable map passed to the orchestrator and
	// the authentik server configurator. PostStart writes authentikLdapToken
	// at runtime so it is available when the LDAP container spec is resolved.
	templateVars := map[string]string{
		"postgresPassword":        cfg.PostgresPassword,
		"authentikSecretKey":      cfg.Secrets.GetAuthentikSecretKey(),
		"authentikBootstrapToken": cfg.Secrets.GetAuthentikBootstrapToken(),
		"authentikAdminPassword":  cfg.AuthentikAdminPassword,
		"authentikAdminEmail":     cfg.AuthentikAdminEmail,
		"authentikLdapToken":      "", // written by apps-authentik-server PostStart
	}

	// Configurator registry: system configurators are registered eagerly;
	// app configurators self-register factories (apps/<name>/registration.go)
	// and are instantiated lazily on first lookup.
	registry := configurator.NewRegistry(logger, appconfig.AppDeps(cfg, logger, hosts))
	appconfig.RegisterSystem(registry, cfg, runtime, logger, templateVars, hosts)

	// Event bus: shared between the API (SSE streams) and background
	// consumers (the mDNS publisher reconciles on app changes).
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
		Hosts:                 hosts,
		EventsBus:             eventsBus,
		HostStore:             hostStore,
		LDAPOutput:            cfg.LDAPOutput(),
		Registry:              registry,
		TemplateVars:          templateVars,
	}, logger)

	// Block until system apps are healthy (first convergence pass).
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

	// Setup graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start background system stats collector
	system.StartStatsCollector(ctx)

	// Start background purge of expired sessions (SQLite has no TTL)
	store.StartSessionPurger(ctx, store.NewSessionStore(database), logger)

	// Advertise the .local hostnames (bloud.local + one subdomain per
	// installed app) over mDNS so LAN devices can reach the instance
	// without DNS configuration.
	mdnsAppStore := store.NewAppStore(database)
	mdns.Start(ctx, mdns.Options{
		Logger: logger,
		Hosts:  hosts,
		Apps: func() []string {
			apps, err := mdnsAppStore.GetAll()
			if err != nil {
				return nil
			}
			var ids []string
			for _, a := range apps {
				// Mirror traefikgen's routable filter: subdomains exist only
				// for non-system apps with a port.
				if a.IsSystem || a.Port <= 0 {
					continue
				}
				ids = append(ids, a.CatalogID)
			}
			return ids
		},
		IP:     netutil.GetPrimaryIP,
		Events: eventsBus,
	})

	// Start server in a goroutine
	go func() {
		if err := server.Start(); err != nil {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

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
