package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// ReconcileConfig holds configuration for the reconciliation loop
type ReconcileConfig struct {
	// WatchdogInterval is how often the watchdog runs reconciliation (default: 5 minutes)
	WatchdogInterval time.Duration

	// HealthCheckTimeout is the max time to wait for an app to become healthy
	HealthCheckTimeout time.Duration
}

// DefaultReconcileConfig returns default reconciliation configuration
func DefaultReconcileConfig() ReconcileConfig {
	return ReconcileConfig{
		WatchdogInterval:   5 * time.Minute,
		HealthCheckTimeout: 60 * time.Second,
	}
}

// Reconciler handles the three-phase reconciliation loop:
// 1. PreStart - ensure config files and directories exist
// 2. HealthCheck - wait for apps to be ready
// 3. PostStart - configure via APIs
//
// Apps are processed in level order (leaf nodes first):
// - Level 0: Apps with no dependencies (e.g., qBittorrent)
// - Level 1: Apps that depend only on Level 0 (e.g., Radarr, Sonarr)
// - Level 2: Apps that depend on Level 1 (e.g., Jellyseerr)
// Apps within the same level can be configured in parallel.
type Reconciler struct {
	registry configurator.RegistryInterface
	appStore store.AppStoreInterface
	dataDir  string
	logger   *slog.Logger
	config   ReconcileConfig
	stopCh   chan struct{}
}

// NewReconciler creates a new reconciler
func NewReconciler(
	registry configurator.RegistryInterface,
	appStore store.AppStoreInterface,
	dataDir string,
	logger *slog.Logger,
	config ReconcileConfig,
) *Reconciler {
	return &Reconciler{
		registry: registry,
		appStore: appStore,
		dataDir:  dataDir,
		logger:   logger,
		config:   config,
	}
}

// Reconcile runs the full reconciliation cycle for all installed apps.
// This is idempotent and safe to call repeatedly.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	r.logger.Info("starting reconciliation")
	startTime := time.Now()

	// Get all installed apps
	apps, err := r.appStore.GetAll()
	if err != nil {
		return fmt.Errorf("failed to get installed apps: %w", err)
	}

	// Build app map for quick lookup
	appMap := make(map[string]*store.InstalledApp)
	for _, app := range apps {
		if app.Status != "uninstalling" {
			appMap[app.Name] = app
		}
	}

	// Compute execution levels (leaf nodes first)
	levels := r.computeLevels(appMap)

	var reconciled []string
	var errors []string

	// Phase 1: PreStart for all apps (can run in any order)
	r.logger.Debug("phase 1: running PreStart for all apps")
	for _, app := range apps {
		if app.Status == "uninstalling" {
			continue
		}

		cfg := r.registry.Get(app.Name)
		if cfg == nil {
			continue
		}

		state := r.buildAppState(app)
		if err := cfg.PreStart(ctx, state); err != nil {
			r.logger.Warn("PreStart failed", "app", app.Name, "error", err)
			errors = append(errors, fmt.Sprintf("%s: PreStart failed: %v", app.Name, err))
		}
	}

	// Phase 2 & 3: HealthCheck + PostStart in level order
	r.logger.Debug("phase 2-3: running HealthCheck + PostStart in level order", "levels", len(levels))
	for levelNum, levelApps := range levels {
		r.logger.Debug("processing level", "level", levelNum, "apps", levelApps)

		// TODO: Run apps within the same level in parallel
		for _, appName := range levelApps {
			app := appMap[appName]
			if app == nil {
				continue
			}

			cfg := r.registry.Get(app.Name)
			if cfg == nil {
				continue
			}

			// Wait for app to be healthy
			healthCtx, cancel := context.WithTimeout(ctx, r.config.HealthCheckTimeout)
			if err := cfg.HealthCheck(healthCtx); err != nil {
				cancel()
				r.logger.Warn("HealthCheck failed, skipping PostStart", "app", app.Name, "error", err)
				errors = append(errors, fmt.Sprintf("%s: HealthCheck failed: %v", app.Name, err))
				continue
			}
			cancel()

			// Run PostStart
			state := r.buildAppState(app)
			if err := cfg.PostStart(ctx, state); err != nil {
				r.logger.Warn("PostStart failed", "app", app.Name, "error", err)
				errors = append(errors, fmt.Sprintf("%s: PostStart failed: %v", app.Name, err))
				continue
			}

			reconciled = append(reconciled, app.Name)
		}
	}

	duration := time.Since(startTime)
	r.logger.Info("reconciliation complete",
		"duration", duration,
		"reconciled", len(reconciled),
		"errors", len(errors),
	)

	return nil
}

// computeLevels computes execution levels for apps.
// Level 0 contains apps with no dependencies (leaf nodes).
// Level N contains apps whose dependencies are all in levels < N.
// Returns a slice of levels, each containing app names.
func (r *Reconciler) computeLevels(apps map[string]*store.InstalledApp) [][]string {
	// Build dependency graph: app -> apps it depends on
	deps := make(map[string][]string)
	for name, app := range apps {
		for _, source := range app.IntegrationConfig {
			// Only count dependencies on other installed apps
			if _, installed := apps[source]; installed {
				deps[name] = append(deps[name], source)
			}
		}
	}

	// Compute level for each app
	levels := make(map[string]int)
	var computeLevel func(name string) int
	computeLevel = func(name string) int {
		if level, ok := levels[name]; ok {
			return level
		}

		appDeps := deps[name]
		if len(appDeps) == 0 {
			levels[name] = 0
			return 0
		}

		maxDepLevel := 0
		for _, dep := range appDeps {
			depLevel := computeLevel(dep)
			if depLevel >= maxDepLevel {
				maxDepLevel = depLevel + 1
			}
		}
		levels[name] = maxDepLevel
		return maxDepLevel
	}

	// Compute all levels
	for name := range apps {
		computeLevel(name)
	}

	// Group apps by level
	maxLevel := 0
	for _, level := range levels {
		if level > maxLevel {
			maxLevel = level
		}
	}

	result := make([][]string, maxLevel+1)
	for name, level := range levels {
		result[level] = append(result[level], name)
	}

	return result
}

// buildAppState creates an AppState from a database app record
func (r *Reconciler) buildAppState(app *store.InstalledApp) *configurator.AppState {
	// Parse integrations from database
	integrations := make(map[string][]string)
	for name, source := range app.IntegrationConfig {
		integrations[name] = []string{source}
	}

	return &configurator.AppState{
		Name:          app.Name,
		DataPath:      filepath.Join(r.dataDir, app.Name),
		BloudDataPath: r.dataDir,
		Port:          app.Port,
		Integrations:  integrations,
		Options:       make(map[string]any), // TODO: Load from catalog or config
	}
}

// StartWatchdog starts the reconciliation watchdog that runs every 5 minutes.
// Returns immediately; reconciliation runs in a background goroutine.
func (r *Reconciler) StartWatchdog(ctx context.Context) {
	r.stopCh = make(chan struct{})

	go func() {
		ticker := time.NewTicker(r.config.WatchdogInterval)
		defer ticker.Stop()

		r.logger.Info("reconciliation watchdog started", "interval", r.config.WatchdogInterval)

		// Run initial reconciliation
		if err := r.Reconcile(ctx); err != nil {
			r.logger.Error("initial reconciliation failed", "error", err)
		}

		for {
			select {
			case <-ctx.Done():
				r.logger.Info("reconciliation watchdog stopped (context cancelled)")
				return
			case <-r.stopCh:
				r.logger.Info("reconciliation watchdog stopped")
				return
			case <-ticker.C:
				if err := r.Reconcile(ctx); err != nil {
					r.logger.Error("watchdog reconciliation failed", "error", err)
				}
			}
		}
	}()
}

// StopWatchdog stops the reconciliation watchdog
func (r *Reconciler) StopWatchdog() {
	if r.stopCh != nil {
		close(r.stopCh)
	}
}
