package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/api"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/appconfig"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/config"
	containerruntime "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/db"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/podman"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/system"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/systemd"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
	_ "github.com/jackc/pgx/v5/stdlib"
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
	cfg := config.Load()
	logger.Info("loaded configuration",
		"runtime", cfg.RuntimeMode,
		"systemd_scope", cfg.SystemdScope,
		"quadlet_dir", cfg.QuadletDir,
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

	templateVars := map[string]string{
		"postgresPassword": cfg.PostgresPassword,
	}

	// In portable mode, ensure system infrastructure containers (postgres, redis)
	// are running before apps need them.
	if cfg.RuntimeMode == "portable" {
		if err := bootstrapInfra(cfg, templateVars, logger); err != nil {
			logger.Error("failed to bootstrap infrastructure", "error", err)
			os.Exit(1)
		}
	}

	// In portable mode, register system apps so dependency resolution works.
	if cfg.RuntimeMode == "portable" {
		registerSystemApps(database, cfg, logger)
	}

	// Create configurator registry
	registry := configurator.NewRegistry(logger)
	appconfig.RegisterAll(registry, cfg)

	// Create HTTP server
	server := api.NewServer(database, api.ServerConfig{
		RuntimeMode:       cfg.RuntimeMode,
		SystemdScope:      cfg.SystemdScope,
		QuadletDir:        cfg.QuadletDir,
		AppsDir:           cfg.AppsDir,
		DataDir:           cfg.DataDir,
		TraefikDynamicDir: cfg.TraefikDynamicDir,
		BaseDomain:        cfg.BaseDomain,
		Port:              cfg.Port,
		SSOHostSecret:     cfg.SSOHostSecret,
		SSOBaseURL:        cfg.SSOBaseURL,
		SSOAuthentikURL:   cfg.SSOAuthentikURL,
		AuthentikToken:    cfg.AuthentikToken,
		AuthentikPort:     cfg.AuthentikPort,
		TSAuthKey:         cfg.TSAuthKey,
		HostLabel:         cfg.HostLabel,
		RedisAddr:         cfg.RedisAddr,
		LDAPOutput:        cfg.LDAPOutput(),
		Registry:          registry,
		TemplateVars:      templateVars,
	}, logger)

	// Setup graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start background system stats collector
	system.StartStatsCollector(ctx)

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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}

	logger.Info("server stopped gracefully")
}

// bootstrapInfra ensures system infrastructure containers (postgres, redis) are
// running before the database connection is attempted. It loads the app catalog
// from YAML, filters for system apps with container specs, and uses the same
// container runtime as the normal orchestrator flow.
func bootstrapInfra(cfg *config.Config, templateVars map[string]string, logger *slog.Logger) error {
	ctx := context.Background()

	client, err := podman.NewClient()
	if err != nil {
		return fmt.Errorf("podman client: %w", err)
	}

	userScope := cfg.SystemdScope != "system"
	wantedBy := "default.target"
	if !userScope {
		wantedBy = "multi-user.target"
	}
	runtime := containerruntime.NewQuadletRuntime(
		client,
		systemd.NewManager(userScope),
		cfg.QuadletDir,
		wantedBy,
	)

	apps, err := catalog.NewLoader(cfg.AppsDir).LoadAll()
	if err != nil {
		return fmt.Errorf("load catalog: %w", err)
	}

	for _, app := range apps {
		if !app.IsSystem || app.Container == nil {
			continue
		}
		logger.Info("bootstrapping system container", "app", app.CatalogID)

		spec, err := orchestrator.PortableContainerSpec(app, cfg.DataDir, templateVars)
		if err != nil {
			return fmt.Errorf("build spec for %s: %w", app.CatalogID, err)
		}
		if err := runtime.EnsureNetwork(ctx, spec.Network); err != nil {
			return fmt.Errorf("ensure network for %s: %w", app.CatalogID, err)
		}
		for _, mount := range spec.Mounts {
			if err := os.MkdirAll(mount.Source, 0755); err != nil {
				return fmt.Errorf("create mount %s for %s: %w", mount.Source, app.CatalogID, err)
			}
		}
		if _, err := runtime.Ensure(ctx, spec); err != nil {
			return fmt.Errorf("ensure container %s: %w", app.CatalogID, err)
		}
	}

	logger.Info("waiting for postgres to accept connections")
	if err := waitForPostgres(cfg.PostgresURL(), 30*time.Second); err != nil {
		return err
	}
	logger.Info("postgres is ready")

	// Bootstrap Traefik reverse proxy.
	// Non-fatal: the system can work without Traefik for API-only access.
	if err := bootstrapTraefik(cfg, runtime, logger); err != nil {
		logger.Error("failed to bootstrap traefik", "error", err)
	}

	// Bootstrap Authentik SSO stack (server, worker, LDAP outpost).
	// Non-fatal: the host-agent can serve apps without Authentik, but LDAP login won't work.
	if err := bootstrapAuthentik(cfg, runtime, logger); err != nil {
		logger.Error("failed to bootstrap authentik (LDAP login will not work)", "error", err)
	}

	return nil
}

// registerSystemApps records all system apps as installed+running in the app store.
// This ensures the orchestrator knows about apps bootstrapped outside its normal
// Install flow (e.g. postgres, redis, authentik) so that dependency resolution
// works when installing user apps like Jellyfin.
func registerSystemApps(database *sql.DB, cfg *config.Config, logger *slog.Logger) {
	apps, err := catalog.NewLoader(cfg.AppsDir).LoadAll()
	if err != nil {
		logger.Warn("failed to load catalog for system app registration", "error", err)
		return
	}

	appStore := store.NewAppStore(database)
	for _, app := range apps {
		if !app.IsSystem {
			continue
		}
		if err := appStore.Install(app.CatalogID, app.DisplayName, app.Version, nil, &store.InstallOptions{
			Port:     app.Port,
			IsSystem: true,
		}); err != nil {
			logger.Warn("failed to register system app", "app", app.CatalogID, "error", err)
			continue
		}
		if err := appStore.UpdateStatus(app.CatalogID, "running"); err != nil {
			logger.Warn("failed to update system app status", "app", app.CatalogID, "error", err)
		}
	}
	logger.Info("registered system apps")
}

// waitForPostgres polls until postgres accepts SQL connections or the timeout expires.
func waitForPostgres(databaseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := sql.Open("pgx", databaseURL)
		if err != nil {
			lastErr = err
			time.Sleep(time.Second)
			continue
		}
		err = conn.Ping()
		conn.Close()
		if err == nil {
			return nil
		}
		lastErr = err
		slog.Warn("postgres not ready", "error", err)
		time.Sleep(time.Second)
	}
	return fmt.Errorf("postgres did not become ready within %s: %w", timeout, lastErr)
}
