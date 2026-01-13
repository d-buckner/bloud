package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/appconfig"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/config"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/db"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// runConfigure handles the "configure" subcommand
// Usage:
//
//	bloud-agent configure prestart <app-name>
//	bloud-agent configure poststart <app-name>
//	bloud-agent configure reconcile
func runConfigure(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: bloud-agent configure <prestart|poststart|reconcile> [app-name]")
		return 1
	}

	action := args[0]
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg := config.Load()

	// Initialize database (read-only for configure commands)
	database, err := db.InitDB(cfg.DataDir)
	if err != nil {
		logger.Error("failed to initialize database", "error", err)
		return 1
	}
	defer database.Close()

	// Create app store
	appStore := store.NewAppStore(database)

	// Create and populate registry
	registry := configurator.NewRegistry(logger)
	appconfig.RegisterAll(registry, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	switch action {
	case "prestart":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: bloud-agent configure prestart <app-name>")
			return 1
		}
		return runPreStart(ctx, args[1], registry, appStore, cfg.DataDir, cfg, logger)

	case "poststart":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: bloud-agent configure poststart <app-name>")
			return 1
		}
		return runPostStart(ctx, args[1], registry, appStore, cfg.DataDir, logger)

	case "reconcile":
		return runReconcile(ctx, registry, appStore, cfg.DataDir, logger)

	default:
		fmt.Fprintf(os.Stderr, "Unknown action: %s\n", action)
		fmt.Fprintln(os.Stderr, "Usage: bloud-agent configure <prestart|poststart|reconcile> [app-name]")
		return 1
	}
}

func runPreStart(ctx context.Context, appName string, registry *configurator.Registry, appStore *store.AppStore, dataDir string, appCfg *config.Config, logger *slog.Logger) int {
	logger.Info("running prestart", "app", appName)

	// Framework-level SSO wait: check if this app has SSO configured in the database.
	// If so, wait for Authentik's OpenID endpoint before the app starts.
	// This ensures the OAuth2 provider/application has been created by the blueprint.
	// We check the database (source of truth for configured integrations) rather than
	// probing Authentik, because Authentik may not have processed the blueprint yet.
	app, err := appStore.GetByName(appName)
	if err != nil {
		logger.Warn("failed to get app from database", "app", appName, "error", err)
	}
	if app != nil {
		if _, hasSSO := app.IntegrationConfig["sso"]; hasSSO {
			logger.Info("waiting for SSO to be ready", "app", appName)
			timeout := 180 * time.Second
			if err := configurator.WaitForSSOReady(ctx, appName, appCfg.AuthentikPort, timeout); err != nil {
				logger.Error("SSO not ready", "app", appName, "error", err)
				return 1
			}
			logger.Info("SSO is ready", "app", appName)
		}
	}

	cfg := registry.Get(appName)
	if cfg == nil {
		// No configurator for this app - that's OK, just succeed
		logger.Debug("no configurator registered", "app", appName)
		return 0
	}

	state, err := buildAppState(appName, appStore, dataDir)
	if err != nil {
		logger.Error("failed to build app state", "app", appName, "error", err)
		return 1
	}

	if err := cfg.PreStart(ctx, state); err != nil {
		logger.Error("prestart failed", "app", appName, "error", err)
		return 1
	}

	logger.Info("prestart completed", "app", appName)
	return 0
}

func runPostStart(ctx context.Context, appName string, registry *configurator.Registry, appStore *store.AppStore, dataDir string, logger *slog.Logger) int {
	logger.Info("running poststart", "app", appName)

	cfg := registry.Get(appName)
	if cfg == nil {
		// No configurator for this app - that's OK, just succeed
		logger.Debug("no configurator registered", "app", appName)
		return 0
	}

	// HealthCheck first
	logger.Debug("waiting for app to be healthy", "app", appName)
	if err := cfg.HealthCheck(ctx); err != nil {
		logger.Error("healthcheck failed", "app", appName, "error", err)
		return 1
	}

	state, err := buildAppState(appName, appStore, dataDir)
	if err != nil {
		logger.Error("failed to build app state", "app", appName, "error", err)
		return 1
	}

	if err := cfg.PostStart(ctx, state); err != nil {
		logger.Error("poststart failed", "app", appName, "error", err)
		return 1
	}

	logger.Info("poststart completed", "app", appName)
	return 0
}

func runReconcile(ctx context.Context, registry *configurator.Registry, appStore *store.AppStore, dataDir string, logger *slog.Logger) int {
	logger.Info("running full reconciliation")

	reconciler := orchestrator.NewReconciler(
		registry,
		appStore,
		dataDir,
		logger,
		orchestrator.DefaultReconcileConfig(),
	)

	if err := reconciler.Reconcile(ctx); err != nil {
		logger.Error("reconciliation failed", "error", err)
		return 1
	}

	logger.Info("reconciliation completed")
	return 0
}

func buildAppState(appName string, appStore *store.AppStore, dataDir string) (*configurator.AppState, error) {
	app, err := appStore.GetByName(appName)
	if err != nil {
		return nil, fmt.Errorf("failed to get app: %w", err)
	}

	// App might not be in database yet (fresh install)
	integrations := make(map[string][]string)
	port := 0

	if app != nil {
		for name, source := range app.IntegrationConfig {
			integrations[name] = []string{source}
		}
		port = app.Port
	}

	return &configurator.AppState{
		Name:          appName,
		DataPath:      filepath.Join(dataDir, appName),
		BloudDataPath: dataDir,
		Port:          port,
		Integrations:  integrations,
		Options:       make(map[string]any),
	}, nil
}
