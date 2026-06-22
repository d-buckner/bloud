package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	integrationdomain "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/integration"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// ReconfigDispatcher is notified when an app's optional dependency transitions to
// healthy. The implementation decides whether to restart the app (prestart config
// change) or re-run PostStart only (poststart config change).
type ReconfigDispatcher interface {
	DispatchReconfig(ctx context.Context, appName string, installedApps map[string]*store.InstalledApp)
}

// ReconcileConfig holds configuration for the reconciliation loop
type ReconcileConfig struct {
	// HealthCheckTimeout is the max time to wait for an app to become healthy
	HealthCheckTimeout time.Duration

	// LDAPOutput is the LDAP provider endpoint to pass to apps with LDAP SSO strategy.
	// Nil when no LDAP provider is configured.
	LDAPOutput *configurator.LDAPOutput
}

// DefaultReconcileConfig returns default reconciliation configuration
func DefaultReconcileConfig() ReconcileConfig {
	return ReconcileConfig{
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
	registry     configurator.RegistryInterface
	appStore     store.AppStoreInterface
	catalogCache catalog.CacheInterface
	dataDir      string
	logger       *slog.Logger
	config       ReconcileConfig

	mu          sync.Mutex
	prevHealthy map[string]bool
	dispatcher  ReconfigDispatcher
}

// SetReconfigDispatcher registers a dispatcher to be called when an app's optional
// dependency transitions to healthy. Safe to call at any time.
func (r *Reconciler) SetReconfigDispatcher(d ReconfigDispatcher) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatcher = d
}

// NewReconciler creates a new reconciler
func NewReconciler(
	registry configurator.RegistryInterface,
	appStore store.AppStoreInterface,
	catalogCache catalog.CacheInterface,
	dataDir string,
	logger *slog.Logger,
	config ReconcileConfig,
) *Reconciler {
	return &Reconciler{
		registry:     registry,
		appStore:     appStore,
		catalogCache: catalogCache,
		dataDir:      dataDir,
		logger:       logger,
		config:       config,
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
	currentlyHealthy := make(map[string]bool)

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

		state, err := r.buildAppState(app, appMap)
		if err != nil {
			r.logger.Warn("failed to build app state", "app", app.Name, "error", err)
			errors = append(errors, fmt.Sprintf("%s: app state failed: %v", app.Name, err))
			continue
		}
		if sc, ok := cfg.(configurator.PreStartConfigurator); ok {
			changed, err := sc.PreStartConfig(ctx, state)
			if err != nil {
				r.logger.Warn("PreStartConfig failed", "app", app.Name, "error", err)
				errors = append(errors, fmt.Sprintf("%s: PreStartConfig failed: %v", app.Name, err))
			} else if changed {
				r.logger.Info("prestart config changed", "app", app.Name)
			}
		} else {
			if err := cfg.PreStart(ctx, state); err != nil {
				r.logger.Warn("PreStart failed", "app", app.Name, "error", err)
				errors = append(errors, fmt.Sprintf("%s: PreStart failed: %v", app.Name, err))
			}
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
			currentlyHealthy[app.Name] = true

			// Run PostStart
			state, err := r.buildAppState(app, appMap)
			if err != nil {
				r.logger.Warn("failed to build app state", "app", app.Name, "error", err)
				errors = append(errors, fmt.Sprintf("%s: app state failed: %v", app.Name, err))
				continue
			}
			if dc, ok := cfg.(configurator.PostStartConfigurator); ok {
				if err := dc.PostStartConfig(ctx, state); err != nil {
					r.logger.Warn("PostStartConfig failed", "app", app.Name, "error", err)
					errors = append(errors, fmt.Sprintf("%s: PostStartConfig failed: %v", app.Name, err))
					continue
				}
			} else {
				if err := cfg.PostStart(ctx, state); err != nil {
					r.logger.Warn("PostStart failed", "app", app.Name, "error", err)
					errors = append(errors, fmt.Sprintf("%s: PostStart failed: %v", app.Name, err))
					continue
				}
			}

			reconciled = append(reconciled, app.Name)
		}
	}

	// Detect newly-healthy apps and dispatch reconfig for any parent apps that
	// have a non-required integration on them and were already healthy last cycle.
	r.mu.Lock()
	prev := r.prevHealthy
	r.prevHealthy = currentlyHealthy
	dispatcher := r.dispatcher
	r.mu.Unlock()

	if dispatcher != nil {
		r.dispatchOptionalDepTransitions(ctx, currentlyHealthy, prev, appMap, dispatcher)
	}

	duration := time.Since(startTime)
	r.logger.Info("reconciliation complete",
		"duration", duration,
		"reconciled", len(reconciled),
		"errors", len(errors),
	)

	return nil
}

// dispatchOptionalDepTransitions finds apps that transitioned from not-healthy to healthy
// this cycle, locates installed parent apps that declare a non-required integration on
// each, and dispatches a reconfig for parents that were already healthy last cycle.
// Parents that are themselves new this cycle don't need dispatch — the level-ordered
// PostStart already ran after the dep became healthy.
func (r *Reconciler) dispatchOptionalDepTransitions(
	ctx context.Context,
	currentlyHealthy, prevHealthy map[string]bool,
	installedApps map[string]*store.InstalledApp,
	dispatcher ReconfigDispatcher,
) {
	if r.catalogCache == nil {
		return
	}

	for newApp := range currentlyHealthy {
		if prevHealthy[newApp] {
			continue // not a transition
		}

		// newApp just became healthy — find parents with optional integration on it
		for parentName := range installedApps {
			if parentName == newApp {
				continue
			}
			if !prevHealthy[parentName] {
				continue // parent is also new; its PostStart handled registration directly
			}

			catalogApp, err := r.catalogCache.Get(parentName)
			if err != nil || catalogApp == nil {
				continue
			}

			for _, integration := range catalogApp.Integrations {
				if integration.Required {
					continue
				}
				for _, compatible := range integration.Compatible {
					if compatible.App == newApp {
						r.logger.Info("dispatching reconfig: optional dep became healthy",
							"parent", parentName, "dep", newApp)
						dispatcher.DispatchReconfig(ctx, parentName, installedApps)
						break
					}
				}
			}
		}
	}
}

// computeLevels computes execution levels for apps.
// Level 0 contains apps with no dependencies (leaf nodes).
// Level N contains apps whose dependencies are all in levels < N.
// Both required integrations (from DB) and non-required integrations whose
// compatible app is currently installed contribute to ordering.
// Returns a slice of levels, each containing app names.
func (r *Reconciler) computeLevels(apps map[string]*store.InstalledApp) [][]string {
	// Build dependency graph: app -> apps it depends on
	deps := make(map[string][]string)
	for name, app := range apps {
		// Required integrations stored in DB
		for _, source := range app.IntegrationConfig {
			if _, installed := apps[source]; installed {
				deps[name] = append(deps[name], source)
			}
		}

		// Non-required (optional) integrations from catalog — soft ordering constraints.
		// If the compatible app is installed, run the parent app after it so PostStart
		// can configure the integration immediately rather than on the next reconcile.
		if r.catalogCache == nil {
			continue
		}
		catalogApp, err := r.catalogCache.Get(name)
		if err != nil || catalogApp == nil {
			continue
		}
		for _, integration := range catalogApp.Integrations {
			if integration.Required {
				continue // already handled via IntegrationConfig
			}
			for _, compatible := range integration.Compatible {
				if _, installed := apps[compatible.App]; installed {
					deps[name] = append(deps[name], compatible.App)
				}
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

// buildAppState creates an AppState from a database app record.
// installedApps is used to resolve optional integrations — pass the full
// installed app map from the current reconcile cycle (or nil to skip).
func (r *Reconciler) buildAppState(app *store.InstalledApp, installedApps map[string]*store.InstalledApp) (*configurator.AppState, error) {
	var catalogApp *catalog.App
	if r.catalogCache != nil {
		var err error
		catalogApp, err = r.catalogCache.Get(app.Name)
		if err != nil {
			return nil, fmt.Errorf("load catalog app: %w", err)
		}
	}

	_, err := resolveIntegrationBindings(app, installedApps, catalogApp)
	if err != nil {
		return nil, fmt.Errorf("resolve integrations: %w", err)
	}

	ssoEnabled := shouldConfigureSSO(catalogApp)

	state := &configurator.AppState{
		DataPath:      filepath.Join(r.dataDir, app.Name),
		BloudDataPath: r.dataDir,
		SSOEnabled:    ssoEnabled,
	}

	if ssoEnabled && catalogApp != nil && catalogApp.SSO.Strategy == "ldap" && r.config.LDAPOutput != nil {
		state.LDAP = r.config.LDAPOutput
	}

	return state, nil
}

func resolveIntegrationBindings(
	app *store.InstalledApp,
	installedApps map[string]*store.InstalledApp,
	catalogApp *catalog.App,
) (integrationdomain.Bindings, error) {
	input := integrationdomain.ResolutionInput{
		Requirements:   make(map[integrationdomain.Type]integrationdomain.Requirement),
		BoundProviders: make(map[integrationdomain.Type]integrationdomain.AppID),
		Installed:      make(map[integrationdomain.AppID]struct{}),
	}

	for integrationType, provider := range app.IntegrationConfig {
		input.BoundProviders[integrationdomain.Type(integrationType)] = integrationdomain.AppID(provider)
	}
	for appName := range installedApps {
		input.Installed[integrationdomain.AppID(appName)] = struct{}{}
	}
	if catalogApp != nil {
		for integrationType, requirement := range catalogApp.Integrations {
			compatible := make([]integrationdomain.AppID, 0, len(requirement.Compatible))
			for _, provider := range requirement.Compatible {
				compatible = append(compatible, integrationdomain.AppID(provider.App))
			}
			input.Requirements[integrationdomain.Type(integrationType)] = integrationdomain.Requirement{
				Required:   requirement.Required,
				Compatible: compatible,
			}
		}
	}

	return integrationdomain.Resolve(input)
}

func shouldConfigureSSO(catalogApp *catalog.App) bool {
	return catalogApp != nil && catalogApp.SSO.Strategy != "" && catalogApp.SSO.Strategy != "none"
}
