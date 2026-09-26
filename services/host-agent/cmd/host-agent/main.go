// SPDX-License-Identifier: AGPL-3.0-only

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
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/config"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/db"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/system"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/wire"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// systemConvergenceTimeout bounds how long the control plane waits for the
// first convergence pass before giving up on startup.
//
// It is derived from the per-node PostStart budget rather than picked
// independently, because the two are coupled: the budget is
// appclient.MaxWaitBudget, so a gate set at or below it would let a single
// node spending its full budget trip the startup timeout and take the whole
// control plane down. That is a worse failure than the per-node ERROR the
// budget exists to produce. The margin is for the other levels that have to
// converge in the same window.
const systemConvergenceTimeout = appclient.MaxWaitBudget + 5*time.Minute

func main() {
	// Check for subcommands
	if len(os.Args) > 1 {
		switch os.Args[1] {
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

	// One store, handed to both the orchestrator and the authentik
	// configurator, so the LDAP token PostStart records is visible to the
	// orchestrator without either of them holding a mutable map.
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

	// The stores the orchestrator and the API share. They are built once, here,
	// and the same pointers go to both: the catalog refresh endpoint has to
	// refresh the cache the orchestrator reads, and app-status writes have to
	// fire the change hook the SSE stream listens to.
	appStore := store.NewAppStore(database)
	catalogCache := catalog.NewMemoryCache()
	if err := catalogCache.Refresh(catalog.NewLoader(cfg.AppsDir)); err != nil {
		logger.Error("failed to load the app catalog", "apps_dir", cfg.AppsDir, "error", err)
		os.Exit(1)
	}
	tailnetStore := store.NewTailnetStore(database)
	remoteAppStore := store.NewRemoteAppStore(database)

	serverCfg := api.ServerConfig{
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
		Secrets:               cfg.Secrets,
		AppStore:              appStore,
		CatalogCache:          catalogCache,
		TailnetStore:          tailnetStore,
		RemoteAppStore:        remoteAppStore,
	}

	// The auth ref exists before the orchestrator because the orchestrator's
	// host-change hook re-ensures the dashboard OAuth app. The hook crosses
	// into the builder as a plain func(), so wire never imports the API
	// package.
	authClient := api.NewAuthentikClient(cfg.AuthentikPort, cfg.AuthentikToken, cfg.BaseDomain)
	authRef := api.NewAuthRef(authClient, store.NewSessionStore(database), serverCfg, logger)
	serverCfg.Authentik = authClient
	serverCfg.AuthRef = authRef

	// One builder owns the orchestrator wiring. Nothing else constructs one.
	out, err := wire.Build(wire.Input{
		Logger:            logger,
		DB:                database,
		AppStore:          appStore,
		CatalogCache:      catalogCache,
		Registry:          registry,
		ContainerRuntime:  runtime,
		EventsBus:         eventsBus,
		Authentik:         authClient,
		TailnetStore:      tailnetStore,
		HostStore:         hostStore,
		Hosts:             hosts,
		AppsDir:           cfg.AppsDir,
		DataDir:           cfg.DataDir,
		TraefikDynamicDir: cfg.TraefikDynamicDir,
		TraefikPort:       cfg.TraefikPort,
		TSAuthKey:         cfg.TSAuthKey,
		LDAPOutput:        cfg.LDAPOutput(),
		TemplateVars:      templateVars,
		SSOBaseURL:        cfg.SSOBaseURL,
		SSOHostSecret:     cfg.SSOHostSecret,
		SSOAuthentikURL:   cfg.SSOAuthentikURL,
		SSOIssuerURL:      cfg.SSOIssuerURL,
		Secrets:           cfg.Secrets,
		OnHostsChanged:    authRef.Ensure,
	})
	if err != nil {
		logger.Error("failed to build the orchestrator", "error", err)
		os.Exit(1)
	}
	serverCfg.Orchestrator = out.Orchestrator

	// The intent loop runs under its own cancellable context so the shutdown
	// path stops it deliberately instead of letting it outlive the process.
	orchCtx, stopOrchestrator := context.WithCancel(context.Background())
	defer stopOrchestrator()
	go out.Orchestrator.Start(orchCtx)

	server := api.NewServer(database, serverCfg, logger)

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

// buildTemplateVars builds the template-variable store shared by the
// orchestrator and the authentik configurator. The LDAP outpost token is not
// in the static set: it is issued at runtime and recorded through the store's
// named setter, which is what keeps that one mutable value guarded.
func buildTemplateVars(cfg *config.Config) *configurator.TemplateVars {
	return configurator.NewTemplateVars(map[string]string{
		"postgresPassword":        cfg.PostgresPassword,
		"authentikSecretKey":      cfg.Secrets.GetAuthentikSecretKey(),
		"authentikBootstrapToken": cfg.Secrets.GetAuthentikBootstrapToken(),
		"authentikAdminPassword":  cfg.AuthentikAdminPassword,
		"authentikAdminEmail":     cfg.AuthentikAdminEmail,
	})
}

// waitForSystemConvergence blocks until the orchestrator reports ready and the
// system apps pass their health check, then initialises auth. It aborts the
// process on a failed health check or a systemConvergenceTimeout. The
// listener is already open here, but bootstrapGate keeps the API unavailable
// until this returns: the API must not serve before the system apps it depends
// on are running.
func waitForSystemConvergence(server *api.Server, logger *slog.Logger) {
	logger.Info("waiting for system apps to converge")
	readyCtx, cancel := context.WithTimeout(context.Background(), systemConvergenceTimeout)
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
