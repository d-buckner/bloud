package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/api"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/appconfig"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/config"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/db"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/system"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
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
		"port", cfg.Port,
		"data_dir", cfg.DataDir,
		"apps_dir", cfg.AppsDir,
		"flake_path", cfg.FlakePath,
		"nixos_path", cfg.NixosPath,
	)

	// Initialize database
	database, err := db.InitDB(cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to initialize database", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	logger.Info("database initialized successfully")

	// Create configurator registry
	registry := configurator.NewRegistry(logger)
	appconfig.RegisterAll(registry, cfg)

	// Create HTTP server
	server := api.NewServer(database, api.ServerConfig{
		AppsDir:         cfg.AppsDir,
		ConfigDir:       cfg.NixConfigDir,
		DataDir:         cfg.DataDir,
		FlakePath:       cfg.FlakePath,
		FlakeTarget:     cfg.FlakeTarget,
		NixosPath:       cfg.NixosPath,
		Port:            cfg.Port,
		SSOHostSecret:   cfg.SSOHostSecret,
		SSOBaseURL:      cfg.SSOBaseURL,
		SSOAuthentikURL: cfg.SSOAuthentikURL,
		AuthentikToken:  cfg.AuthentikToken,
		RedisAddr:       cfg.RedisAddr,
		Registry:        registry,
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

