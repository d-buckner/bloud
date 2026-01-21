package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/nixgen"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/secrets"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/sso"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/traefikgen"
)

// Orchestrator coordinates app installation via NixOS
type Orchestrator struct {
	graph           catalog.AppGraphInterface
	catalogCache    catalog.CacheInterface
	appStore        store.AppStoreInterface
	generator       nixgen.GeneratorInterface
	traefikGen      traefikgen.GeneratorInterface
	blueprintGen    sso.BlueprintGeneratorInterface
	authentikClient authentik.ClientInterface
	rebuilder       nixgen.RebuilderInterface
	dataDir         string
	logger          *slog.Logger
	queue           *OperationQueue
}

// Config holds Orchestrator configuration
type Config struct {
	Graph             catalog.AppGraphInterface
	CatalogCache      catalog.CacheInterface
	AppStore          store.AppStoreInterface
	Logger            *slog.Logger
	ConfigPath        string // Path to generated apps.nix
	TraefikConfigPath string // Path to generated apps-routes.yml
	NixosPath         string // Path to nixos/ directory
	FlakePath         string // Path to flake
	Hostname          string // NixOS hostname
	DataDir           string // Path to bloud data directory
	// SSO configuration
	SSOHostSecret    string // Master secret for deriving client secrets
	SSOBaseURL       string // Base URL for callbacks (e.g., "http://localhost:8080")
	SSOAuthentikURL  string // Authentik URL for discovery (e.g., "http://localhost:8080")
	SSOBlueprintsDir string // Directory to write blueprints to
	AuthentikToken   string // Authentik API token for SSO cleanup
	LDAPBindPassword string // LDAP bind password for service accounts
	Secrets          *secrets.Manager // Secrets manager for persisting derived secrets

	// Optional: inject dependencies for testing (if nil, defaults will be created)
	Generator       nixgen.GeneratorInterface
	Rebuilder       nixgen.RebuilderInterface
	TraefikGen      traefikgen.GeneratorInterface
	BlueprintGen    sso.BlueprintGeneratorInterface
	AuthentikClient authentik.ClientInterface
}

// New creates a Nix-based orchestrator
func New(cfg Config) *Orchestrator {
	// Use injected dependencies if provided, otherwise create defaults

	// Generator
	var generator nixgen.GeneratorInterface = cfg.Generator
	if generator == nil {
		generator = nixgen.NewGenerator(cfg.ConfigPath, cfg.NixosPath)
	}

	// Rebuilder
	var rebuilder nixgen.RebuilderInterface = cfg.Rebuilder
	if rebuilder == nil {
		rebuilder = nixgen.NewRebuilder(cfg.FlakePath, cfg.Hostname, cfg.Logger)
	}

	// Traefik generator
	var traefikGen traefikgen.GeneratorInterface = cfg.TraefikGen
	if traefikGen == nil {
		traefikGen = traefikgen.NewGenerator(cfg.TraefikConfigPath)
	}

	// Blueprint generator (if SSO config is provided)
	var blueprintGen sso.BlueprintGeneratorInterface = cfg.BlueprintGen
	if blueprintGen == nil && cfg.SSOBlueprintsDir != "" {
		blueprintGen = sso.NewBlueprintGenerator(
			cfg.SSOHostSecret,
			cfg.LDAPBindPassword,
			cfg.SSOBaseURL,
			cfg.SSOAuthentikURL,
			cfg.SSOBlueprintsDir,
			cfg.Secrets,
		)
	}

	// Authentik client (if token is provided)
	var authentikClient authentik.ClientInterface = cfg.AuthentikClient
	if authentikClient == nil && cfg.AuthentikToken != "" && cfg.SSOBaseURL != "" {
		authentikClient = authentik.NewClient(cfg.SSOBaseURL, cfg.AuthentikToken)
	}

	o := &Orchestrator{
		graph:           cfg.Graph,
		catalogCache:    cfg.CatalogCache,
		appStore:        cfg.AppStore,
		generator:       generator,
		traefikGen:      traefikGen,
		blueprintGen:    blueprintGen,
		authentikClient: authentikClient,
		rebuilder:       rebuilder,
		dataDir:         cfg.DataDir,
		logger:          cfg.Logger,
	}

	// Create and start the operation queue
	o.queue = NewOperationQueue(o, DefaultQueueConfig(), cfg.Logger)
	o.queue.Start()

	return o
}

// Stop gracefully shuts down the orchestrator, including the operation queue.
func (o *Orchestrator) Stop() {
	if o.queue != nil {
		o.queue.Stop()
	}
}

// EnqueueInstall adds an install request to the queue and waits for the result.
// This is the primary entry point for install operations from HTTP handlers.
func (o *Orchestrator) EnqueueInstall(ctx context.Context, req InstallRequest) (InstallResponse, error) {
	return o.queue.EnqueueInstall(ctx, req)
}

// EnqueueUninstall adds an uninstall request to the queue and waits for the result.
// This is the primary entry point for uninstall operations from HTTP handlers.
func (o *Orchestrator) EnqueueUninstall(ctx context.Context, req UninstallRequest) (UninstallResponse, error) {
	return o.queue.EnqueueUninstall(ctx, req)
}

// InstallResult describes the outcome of a Nix-based installation
type InstallResult struct {
	App            string   `json:"app"`
	Success        bool     `json:"success"`
	Error          string   `json:"error,omitempty"`
	AppsInstalled  []string `json:"appsInstalled,omitempty"`
	Configured     []string `json:"configured,omitempty"`
	ConfigErrors   []string `json:"configErrors,omitempty"`
	RebuildOutput  string   `json:"rebuildOutput,omitempty"`
	GenerationInfo string   `json:"generationInfo,omitempty"`
}

// Install installs an app using NixOS transactions
func (o *Orchestrator) Install(ctx context.Context, req InstallRequest) (InstallResponse, error) {
	result := &InstallResult{App: req.App}

	o.logger.Info("starting Nix installation", "app", req.App)

	// 1. Build install plan
	plan, err := o.graph.PlanInstall(req.App)
	if err != nil {
		result.Error = fmt.Sprintf("failed to plan install: %v", err)
		return result, nil
	}

	if !plan.CanInstall {
		result.Error = fmt.Sprintf("cannot install: %v", plan.Blockers)
		return result, nil
	}

	// 2. Build transaction with all apps to install
	tx, err := o.buildInstallTransaction(req, plan)
	if err != nil {
		result.Error = fmt.Sprintf("failed to build transaction: %v", err)
		return result, nil
	}

	// 3. Show preview
	preview := o.generator.Preview(tx)
	o.logger.Debug("Nix config preview", "config", preview)

	// 4. Record intent in database (before Nix rebuild)
	if err := o.recordInstallIntent(req, plan); err != nil {
		result.Error = fmt.Sprintf("failed to record intent: %v", err)
		return result, nil
	}

	// 5. Generate SSO blueprints for apps with native-oidc strategy
	if err := o.generateSSOBlueprints(tx); err != nil {
		o.logger.Warn("failed to generate SSO blueprints", "error", err)
		// Non-fatal - apps will work, just without SSO
	}

	// 6. Generate Nix config
	if err := o.generator.Apply(tx); err != nil {
		result.Error = fmt.Sprintf("failed to generate Nix config: %v", err)
		return result, nil
	}

	// 7. Trigger nixos-rebuild switch (atomic transaction)
	o.logger.Info("triggering nixos-rebuild switch")
	rebuildResult, err := o.rebuilder.Switch(ctx)
	if err != nil {
		result.Error = fmt.Sprintf("nixos-rebuild failed: %v", err)
		return result, nil
	}

	result.RebuildOutput = rebuildResult.Output

	if !rebuildResult.Success {
		result.Error = rebuildResult.ErrorMessage
		o.appStore.UpdateStatus(req.App, "failed")
		return result, nil
	}

	// 8. Reload systemd and restart all apps via bloud-apps.target
	// This properly handles daemon-reload and restarts services in dependency order
	if err := o.rebuilder.ReloadAndRestartApps(ctx); err != nil {
		o.logger.Warn("failed to reload and restart apps", "error", err)
		// Don't fail the install - apps may still come up via systemd dependencies
	}

	// 9. Update database status to 'starting' and begin health checks
	for appName := range tx.Apps {
		if err := o.appStore.UpdateStatus(appName, "starting"); err != nil {
			o.logger.Warn("failed to update app status", "app", appName, "error", err)
		}
		result.AppsInstalled = append(result.AppsInstalled, appName)

		// Start health check polling in background
		go o.waitForHealthy(appName)
	}

	// 10. Update graph state
	installedNames, _ := o.appStore.GetInstalledNames()
	o.graph.SetInstalled(installedNames)

	// 11. Regenerate Traefik routes for all installed apps
	if err := o.regenerateTraefikRoutes(); err != nil {
		o.logger.Warn("failed to regenerate Traefik routes", "error", err)
		// Non-fatal - apps may still work, just not via iframe embedding
	}

	result.Success = true
	result.GenerationInfo = fmt.Sprintf("Rebuild completed in %v", rebuildResult.Duration)

	o.logger.Info("installation complete",
		"app", req.App,
		"apps_installed", result.AppsInstalled,
		"configured", len(result.Configured),
	)

	return result, nil
}

// buildInstallTransaction creates a Nix transaction for installation
func (o *Orchestrator) buildInstallTransaction(req InstallRequest, plan *catalog.InstallPlan) (*nixgen.Transaction, error) {
	// Load current state
	current, err := o.generator.LoadCurrent()
	if err != nil {
		return nil, fmt.Errorf("failed to load current state: %w", err)
	}

	tx := &nixgen.Transaction{
		Apps:   make(map[string]nixgen.AppConfig),
		Global: current.Global, // Preserve existing global config
	}

	// Copy existing apps
	for name, app := range current.Apps {
		tx.Apps[name] = app
	}

	// Add the main app
	integrationConfig := make(map[string]string)
	if req.Choices != nil {
		for k, v := range req.Choices {
			integrationConfig[k] = v
		}
	}

	// Add auto-configured integrations
	for _, auto := range plan.AutoConfig {
		integrationConfig[auto.Integration] = auto.Source
	}

	// For required integrations with choices but no user selection, use defaults
	for _, choice := range plan.Choices {
		if _, hasChoice := integrationConfig[choice.Integration]; hasChoice {
			continue // User already made a choice
		}
		if !choice.Required {
			continue // Not required, skip
		}

		// Use the recommended option (first default in compatible list)
		if choice.Recommended != "" {
			integrationConfig[choice.Integration] = choice.Recommended
			o.logger.Info("auto-selected integration", "app", req.App, "integration", choice.Integration, "source", choice.Recommended)
		}
	}

	tx.Apps[req.App] = nixgen.AppConfig{
		Name:         req.App,
		Enabled:      true,
		Integrations: integrationConfig,
	}

	// Check if app needs LDAP outpost (enable if any app has LDAP strategy)
	if mainApp, err := o.catalogCache.Get(req.App); err == nil && mainApp != nil {
		if mainApp.SSO.Strategy == "ldap" {
			tx.Global.AuthentikLDAPEnable = true
			o.logger.Info("enabling LDAP outpost for app", "app", req.App)
		}
	}

	// For each required integration choice, ensure that app is also enabled
	for _, source := range integrationConfig {
		if _, exists := tx.Apps[source]; !exists {
			// This app needs to be installed too
			tx.Apps[source] = nixgen.AppConfig{
				Name:    source,
				Enabled: true,
			}
		}
	}

	return tx, nil
}

// recordInstallIntent records the installation in the database
func (o *Orchestrator) recordInstallIntent(req InstallRequest, plan *catalog.InstallPlan) error {
	integrationConfig := make(map[string]string)
	if req.Choices != nil {
		for k, v := range req.Choices {
			integrationConfig[k] = v
		}
	}

	for _, auto := range plan.AutoConfig {
		integrationConfig[auto.Integration] = auto.Source
	}

	// For required integrations with choices but no user selection, use defaults
	// (mirrors the logic in buildInstallTransaction)
	for _, choice := range plan.Choices {
		if _, hasChoice := integrationConfig[choice.Integration]; hasChoice {
			continue // User already made a choice
		}
		if !choice.Required {
			continue // Not required, skip
		}
		if choice.Recommended != "" {
			integrationConfig[choice.Integration] = choice.Recommended
		}
	}

	// Record the main app with port, isSystem, and displayName from catalog
	mainApp, _ := o.catalogCache.Get(req.App)
	opts := &store.InstallOptions{}
	displayName := req.App // fallback to internal name
	if mainApp != nil {
		opts.Port = mainApp.Port
		opts.IsSystem = mainApp.IsSystem
		displayName = mainApp.DisplayName
	}
	if err := o.appStore.Install(req.App, displayName, "", integrationConfig, opts); err != nil {
		return err
	}

	// Record dependencies that will be installed
	for _, source := range integrationConfig {
		// Check if already installed
		existing, err := o.appStore.GetByName(source)
		if err != nil {
			return err // Real error
		}
		if existing == nil {
			// Not installed yet, record it with port/isSystem/displayName from catalog
			depApp, _ := o.catalogCache.Get(source)
			depOpts := &store.InstallOptions{}
			depDisplayName := source // fallback to internal name
			if depApp != nil {
				depOpts.Port = depApp.Port
				depOpts.IsSystem = depApp.IsSystem
				depDisplayName = depApp.DisplayName
			}
			if err := o.appStore.Install(source, depDisplayName, "", nil, depOpts); err != nil {
				return err
			}
		}
	}

	return nil
}

// Rollback reverts to the previous NixOS generation
func (o *Orchestrator) Rollback(ctx context.Context) (*nixgen.RebuildResult, error) {
	o.logger.Info("starting NixOS rollback")

	// Call nixos-rebuild switch --rollback
	result, err := o.rebuilder.Rollback(ctx)
	if err != nil {
		return nil, fmt.Errorf("rollback failed: %w", err)
	}

	if !result.Success {
		return result, fmt.Errorf("rollback unsuccessful: %s", result.ErrorMessage)
	}

	// Sync database state from current Nix config
	// This ensures DB reflects what's actually running after rollback
	current, err := o.generator.LoadCurrent()
	if err != nil {
		o.logger.Warn("failed to load current state after rollback", "error", err)
	} else {
		// Update installed apps in database to match Nix state
		installedNames := []string{}
		for name, app := range current.Apps {
			if app.Enabled {
				installedNames = append(installedNames, name)
			}
		}
		o.graph.SetInstalled(installedNames)
	}

	o.logger.Info("rollback complete")
	return result, nil
}

// RebuildStream triggers a nixos-rebuild switch with streaming output
func (o *Orchestrator) RebuildStream(ctx context.Context, events chan<- nixgen.RebuildEvent) {
	o.logger.Info("starting streaming NixOS rebuild")
	o.rebuilder.SwitchStream(ctx, events)
}

// Uninstall removes an app using NixOS transactions
func (o *Orchestrator) Uninstall(ctx context.Context, req UninstallRequest) (UninstallResponse, error) {
	appName := req.App
	result := &UninstallResult{App: appName}

	o.logger.Info("starting Nix uninstallation", "app", appName, "clearData", req.ClearData)

	// Get app metadata early for SSO cleanup
	catalogApp, _ := o.catalogCache.Get(appName)

	// 1. Always check dependencies first, regardless of Nix config state
	plan, err := o.graph.PlanRemove(appName)
	if err != nil {
		result.Error = fmt.Sprintf("failed to plan removal: %v", err)
		return result, nil
	}

	if !plan.CanRemove {
		result.Error = fmt.Sprintf("cannot remove: %v", plan.Blockers)
		return result, nil
	}

	// Set status to uninstalling (will broadcast via AppStore.onChange)
	o.appStore.UpdateStatus(appName, "uninstalling")

	// Load current Nix state to check if app is actually in config
	current, err := o.generator.LoadCurrent()
	if err != nil {
		result.Error = fmt.Sprintf("failed to load current state: %v", err)
		return result, nil
	}

	// Check if app is in the Nix config
	appInConfig := false
	if app, exists := current.Apps[appName]; exists && app.Enabled {
		appInConfig = true
	}

	// If app is in config, do the full Nix uninstall flow
	if appInConfig {
		// 2. Track dependent apps that will be unconfigured
		if len(plan.WillUnconfigure) > 0 {
			result.Unconfigured = plan.WillUnconfigure
		}

		// 3. Build transaction with app disabled
		tx := &nixgen.Transaction{
			Apps: make(map[string]nixgen.AppConfig),
		}

		for name, app := range current.Apps {
			if name == appName {
				app.Enabled = false
			}
			tx.Apps[name] = app
		}

		// 4. Generate Nix config
		if err := o.generator.Apply(tx); err != nil {
			result.Error = fmt.Sprintf("failed to generate Nix config: %v", err)
			return result, nil
		}

		// 5. Trigger nixos-rebuild switch
		o.logger.Info("triggering nixos-rebuild switch for uninstall")
		rebuildResult, err := o.rebuilder.Switch(ctx)
		if err != nil {
			result.Error = fmt.Sprintf("nixos-rebuild failed: %v", err)
			return result, nil
		}

		if !rebuildResult.Success {
			result.Error = rebuildResult.ErrorMessage
			return result, nil
		}

		// 6. Stop the user service
		o.logger.Info("stopping user service", "app", appName)
		if err := o.rebuilder.StopUserService(ctx, appName); err != nil {
			o.logger.Warn("failed to stop user service", "app", appName, "error", err)
		}
	} else {
		// App not in Nix config - it's orphaned, just clean up
		o.logger.Info("app not in Nix config, cleaning up orphaned entry", "app", appName)

		// Try to stop the service anyway (it might be running from old config)
		if err := o.rebuilder.StopUserService(ctx, appName); err != nil {
			o.logger.Debug("service not running or already stopped", "app", appName)
		}
	}

	// Always remove from database
	if err := o.appStore.Uninstall(appName); err != nil {
		result.Error = fmt.Sprintf("failed to remove from database: %v", err)
		return result, nil
	}

	// Update graph state
	installedNames, _ := o.appStore.GetInstalledNames()
	o.graph.SetInstalled(installedNames)

	// Regenerate Traefik routes (removes the uninstalled app)
	if err := o.regenerateTraefikRoutes(); err != nil {
		o.logger.Warn("failed to regenerate Traefik routes", "error", err)
		// Non-fatal - just means old routes may persist
	}

	// Always cleanup SSO (Authentik app/provider + blueprint file)
	o.cleanupSSO(appName, catalogApp)

	// Conditionally cleanup data and database
	if req.ClearData {
		o.cleanupAppData(appName)
	}

	result.Success = true
	o.logger.Info("uninstallation complete", "app", appName)

	return result, nil
}

// cleanupSSO removes the app's SSO configuration from Authentik and deletes the blueprint file
func (o *Orchestrator) cleanupSSO(appName string, catalogApp *catalog.App) {
	// Delete blueprint file (always, even if app has no SSO - no harm in trying)
	if o.blueprintGen != nil {
		if err := o.blueprintGen.DeleteBlueprint(appName); err != nil {
			o.logger.Warn("failed to delete blueprint file", "app", appName, "error", err)
		} else {
			o.logger.Debug("deleted blueprint file", "app", appName)
		}
	}

	// If app has SSO configured, delete from Authentik via API
	if catalogApp == nil || catalogApp.SSO.Strategy == "" || catalogApp.SSO.Strategy == "none" {
		return
	}

	if o.authentikClient == nil {
		o.logger.Warn("authentik client not configured, skipping SSO cleanup", "app", appName)
		return
	}

	o.logger.Info("cleaning up Authentik SSO", "app", appName, "strategy", catalogApp.SSO.Strategy)

	if err := o.authentikClient.DeleteAppSSO(appName, catalogApp.DisplayName, catalogApp.SSO.Strategy); err != nil {
		o.logger.Warn("failed to cleanup Authentik SSO", "app", appName, "error", err)
		// Non-fatal - continue with uninstall
	} else {
		o.logger.Info("cleaned up Authentik SSO", "app", appName)
	}
}

// cleanupAppData removes the app's data directory and database
func (o *Orchestrator) cleanupAppData(appName string) {
	// Delete app data directory
	if o.dataDir != "" {
		appDataDir := filepath.Join(o.dataDir, appName)
		o.logger.Info("removing app data directory", "app", appName, "path", appDataDir)
		if err := os.RemoveAll(appDataDir); err != nil {
			o.logger.Warn("failed to remove app data directory", "app", appName, "error", err)
		}
	}

	// Drop app's database if it uses shared postgres
	if err := o.dropAppDatabase(appName); err != nil {
		o.logger.Warn("failed to drop app database", "app", appName, "error", err)
	}
}

// dropAppDatabase drops the database for an app if it uses shared postgres
func (o *Orchestrator) dropAppDatabase(appName string) error {
	// Apps that use shared postgres and their database names
	// TODO: Move this to catalog metadata
	appDatabases := map[string]string{
		"actual-budget": "actual_budget",
		"miniflux":      "miniflux",
	}

	dbName, ok := appDatabases[appName]
	if !ok {
		return nil // App doesn't use shared postgres
	}

	o.logger.Info("dropping app database", "app", appName, "database", dbName)

	// Use podman exec to run psql command
	cmd := exec.Command("podman", "exec", "apps-postgres", "psql", "-U", "apps", "-c",
		fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to drop database %s: %w (output: %s)", dbName, err, string(output))
	}

	return nil
}

// waitForHealthy polls an app's health endpoint until it responds or times out
func (o *Orchestrator) waitForHealthy(appName string) {
	o.logger.Info("starting health check", "app", appName)

	// Get app info from catalog for health check config
	app, err := o.catalogCache.Get(appName)
	if err != nil || app == nil {
		o.logger.Warn("failed to get app from catalog", "app", appName, "error", err)
		// No health check info, assume healthy after a short delay
		time.Sleep(5 * time.Second)
		o.appStore.UpdateStatus(appName, "running")
		return
	}

	// If no health check configured, assume healthy after short delay
	if app.HealthCheck.Path == "" {
		o.logger.Debug("no health check configured, assuming healthy", "app", appName)
		time.Sleep(3 * time.Second)
		o.appStore.UpdateStatus(appName, "running")
		return
	}

	// Get port from appPorts map or default
	port := o.getAppPort(appName)
	if port == 0 {
		o.logger.Warn("no port configured for app", "app", appName)
		o.appStore.UpdateStatus(appName, "running")
		return
	}

	// Configure polling parameters
	interval := time.Duration(app.HealthCheck.Interval) * time.Second
	if interval == 0 {
		interval = 2 * time.Second
	}
	timeout := time.Duration(app.HealthCheck.Timeout) * time.Second
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	url := fmt.Sprintf("http://localhost:%d%s", port, app.HealthCheck.Path)
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}

	o.logger.Info("polling health check", "app", appName, "url", url, "timeout", timeout, "interval", interval)

	attempts := 0
	var lastErr error
	var lastStatus int
	for time.Now().Before(deadline) {
		attempts++
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			lastStatus = 0
			o.logger.Debug("health check attempt failed",
				"app", appName,
				"attempt", attempts,
				"error", err)
		} else {
			lastStatus = resp.StatusCode
			lastErr = nil
			resp.Body.Close()
			// Accept 2xx, 3xx, 401, and 403 as "healthy"
			// 401/403 means the service is responding but requires authentication
			if (resp.StatusCode >= 200 && resp.StatusCode < 400) || resp.StatusCode == 401 || resp.StatusCode == 403 {
				o.logger.Info("health check passed", "app", appName, "status", resp.StatusCode, "attempts", attempts)
				o.appStore.UpdateStatus(appName, "running")
				// Ensure forward-auth providers are in the embedded outpost
				// (async call to avoid blocking health check completion)
				go o.ensureForwardAuthOutpostAssociation()
				return
			}
			o.logger.Debug("health check got non-success status",
				"app", appName,
				"attempt", attempts,
				"status", resp.StatusCode)
		}
		time.Sleep(interval)
	}

	o.logger.Warn("health check timed out",
		"app", appName,
		"attempts", attempts,
		"lastError", lastErr,
		"lastStatus", lastStatus,
		"url", url)
	o.appStore.UpdateStatus(appName, "error")
}

// RecheckFailedApps checks health of apps with "failed" or "error" status
// Called on server startup to recover from stale failure states
func (o *Orchestrator) RecheckFailedApps() {
	apps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Warn("failed to get apps for health recheck", "error", err)
		return
	}

	for _, app := range apps {
		if app.Status == "failed" || app.Status == "error" {
			o.logger.Info("rechecking failed app", "app", app.Name, "status", app.Status)
			go o.waitForHealthy(app.Name)
		}
	}
}

// getAppPort returns the port for an app from the catalog
func (o *Orchestrator) getAppPort(appName string) int {
	app, err := o.catalogCache.Get(appName)
	if err != nil || app == nil || app.Port == 0 {
		return 0
	}
	return app.Port
}

// regenerateTraefikRoutes generates Traefik routes for all installed apps
func (o *Orchestrator) regenerateTraefikRoutes() error {
	// Get list of installed app names
	installedNames, err := o.appStore.GetInstalledNames()
	if err != nil {
		return fmt.Errorf("failed to get installed apps: %w", err)
	}

	// Check if Authentik is installed (for SSO middlewares)
	authentikEnabled := false
	for _, name := range installedNames {
		if name == "authentik" {
			authentikEnabled = true
			break
		}
	}
	o.traefikGen.SetAuthentikEnabled(authentikEnabled)

	// Get full app metadata from catalog
	var installedApps []*catalog.App
	for _, name := range installedNames {
		app, err := o.catalogCache.Get(name)
		if err != nil || app == nil {
			o.logger.Warn("failed to get app from catalog", "app", name, "error", err)
			continue
		}
		installedApps = append(installedApps, app)
	}

	// Generate Traefik routes
	if err := o.traefikGen.Generate(installedApps); err != nil {
		return fmt.Errorf("failed to generate Traefik routes: %w", err)
	}

	o.logger.Info("regenerated Traefik routes", "apps", len(installedApps))
	return nil
}

// RegenerateRoutes implements AppOrchestrator interface
func (o *Orchestrator) RegenerateRoutes() error {
	return o.regenerateTraefikRoutes()
}

// generateSSOBlueprints generates Authentik blueprints for apps with SSO
func (o *Orchestrator) generateSSOBlueprints(tx *nixgen.Transaction) error {
	if o.blueprintGen == nil {
		return nil // SSO not configured
	}

	// Track forward-auth providers for outpost blueprint
	var forwardAuthProviders []sso.ForwardAuthProvider
	// Track LDAP apps for LDAP outpost blueprint
	var ldapApps []sso.LDAPApp

	for appName, appConfig := range tx.Apps {
		if !appConfig.Enabled {
			continue
		}

		// Get full app metadata from catalog
		app, err := o.catalogCache.Get(appName)
		if err != nil || app == nil {
			o.logger.Debug("app not in catalog, skipping SSO", "app", appName)
			continue
		}

		// Generate blueprint if app has SSO configured
		if app.SSO.Strategy == "native-oidc" || app.SSO.Strategy == "forward-auth" || app.SSO.Strategy == "ldap" {
			o.logger.Info("generating SSO blueprint", "app", appName, "strategy", app.SSO.Strategy)
			if err := o.blueprintGen.GenerateForApp(app); err != nil {
				return fmt.Errorf("failed to generate blueprint for %s: %w", appName, err)
			}

			// Track forward-auth providers for outpost association
			if app.SSO.Strategy == "forward-auth" {
				forwardAuthProviders = append(forwardAuthProviders, sso.ForwardAuthProvider{
					DisplayName: app.DisplayName,
				})
			}

			// Track LDAP apps for LDAP outpost blueprint
			if app.SSO.Strategy == "ldap" {
				ldapApps = append(ldapApps, sso.LDAPApp{
					Name:        app.Name,
					DisplayName: app.DisplayName,
				})
			}
		}
	}

	// Generate the outpost blueprint with all forward-auth providers.
	// This adds the providers to the embedded outpost via blueprint (no API call needed).
	if err := o.blueprintGen.GenerateOutpostBlueprint(forwardAuthProviders); err != nil {
		return fmt.Errorf("failed to generate outpost blueprint: %w", err)
	}

	// Create LDAP infrastructure via API if there are LDAP apps.
	// This is done via API (not blueprint) to ensure the resources exist BEFORE
	// the LDAP container tries to start. Blueprint timing is unreliable.
	if len(ldapApps) > 0 && o.authentikClient != nil && o.authentikClient.IsAvailable() {
		ldapBindPassword := o.blueprintGen.GetLDAPBindPassword()
		o.logger.Info("creating LDAP infrastructure via API", "apps", len(ldapApps))
		if err := o.authentikClient.EnsureLDAPInfrastructure(ldapBindPassword); err != nil {
			// Log warning but don't fail - LDAP container's prestart will retry
			o.logger.Warn("failed to create LDAP infrastructure via API", "error", err)
		}
	}

	return nil
}

// ensureForwardAuthOutpostAssociation ensures forward-auth providers are added to the embedded outpost
// This is called after Authentik has had time to process blueprints
func (o *Orchestrator) ensureForwardAuthOutpostAssociation() {
	if o.authentikClient == nil {
		return
	}

	// Get all running apps
	apps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Warn("failed to get apps for outpost association", "error", err)
		return
	}

	for _, dbApp := range apps {
		if dbApp.Status != "running" {
			continue
		}

		// Get catalog metadata
		app, err := o.catalogCache.Get(dbApp.Name)
		if err != nil || app == nil {
			continue
		}

		// Only process forward-auth apps
		if app.SSO.Strategy != "forward-auth" {
			continue
		}

		providerName := fmt.Sprintf("%s Proxy Provider", app.DisplayName)
		if err := o.authentikClient.AddProviderToEmbeddedOutpost(providerName); err != nil {
			o.logger.Warn("failed to add provider to embedded outpost",
				"app", app.Name, "provider", providerName, "error", err)
		} else {
			o.logger.Info("ensured provider in embedded outpost",
				"app", app.Name, "provider", providerName)
		}
	}
}

// systemAppInfo contains metadata for NixOS-managed system apps
type systemAppInfo struct {
	name        string
	displayName string
	port        int
	serviceName string
}

// systemApps defines the NixOS-managed system apps
// These apps have their own service names (not podman-{appName}.service)
var systemApps = []systemAppInfo{
	{"authentik", "Authentik", 9001, "podman-apps-authentik-server.service"},
	{"traefik", "Traefik", 8080, "podman-traefik.service"},
	{"postgres", "PostgreSQL", 5432, "podman-apps-postgres.service"},
	{"redis", "Redis", 6379, "podman-apps-redis.service"},
}

// getSystemdServiceName returns the systemd service name for an app
func getSystemdServiceName(appName string) string {
	for _, app := range systemApps {
		if app.name == appName {
			return app.serviceName
		}
	}
	// Regular apps use podman-{appName}.service
	return fmt.Sprintf("podman-%s.service", appName)
}

// checkSystemdServiceActive checks if a systemd user service is active
func (o *Orchestrator) checkSystemdServiceActive(appName string) bool {
	serviceName := getSystemdServiceName(appName)
	cmd := exec.Command("systemctl", "--user", "is-active", serviceName)
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "active"
}

// ensureSystemAppsRegistered ensures NixOS-managed system apps are in the database
// This is needed so that authentikEnabled detection works (it checks the app database)
// NOTE: In future, this should be handled via systemd dependencies
func (o *Orchestrator) ensureSystemAppsRegistered() {
	for _, app := range systemApps {
		// Check if the systemd service is active
		cmd := exec.Command("systemctl", "--user", "is-active", app.serviceName)
		output, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(output)) != "active" {
			continue // Service not running, don't register
		}

		// Register the system app
		if err := o.appStore.EnsureSystemApp(app.name, app.displayName, app.port); err != nil {
			o.logger.Warn("failed to register system app", "app", app.name, "error", err)
		} else {
			o.logger.Info("registered system app", "app", app.name)
		}
	}
}

// ReconcileState synchronizes database state with actual system state
// Called on server startup to recover from crashes or stale states
func (o *Orchestrator) ReconcileState() {
	o.logger.Info("reconciling app states with system")

	// Ensure system apps (Authentik, Traefik, etc.) are registered first
	// This enables proper authentikEnabled detection for SSO middleware
	o.ensureSystemAppsRegistered()

	apps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Warn("failed to get apps for reconciliation", "error", err)
		return
	}

	o.logger.Info("reconciling apps", "count", len(apps))

	for _, app := range apps {
		serviceName := getSystemdServiceName(app.Name)
		o.logger.Debug("reconciling app",
			"app", app.Name,
			"status", app.Status,
			"isSystem", app.IsSystem,
			"serviceName", serviceName)

		switch app.Status {
		case "installing":
			// Server crashed mid-install - mark as error
			o.logger.Warn("found app stuck in installing state", "app", app.Name,
				"updated_at", app.UpdatedAt)
			o.appStore.UpdateStatus(app.Name, "error")

		case "starting":
			// Server crashed during health check - restart health check
			o.logger.Info("restarting health check for app", "app", app.Name)
			go o.waitForHealthy(app.Name)

		case "running":
			// Verify service is actually running
			serviceActive := o.checkSystemdServiceActive(app.Name)
			o.logger.Debug("checking running app service",
				"app", app.Name,
				"serviceName", serviceName,
				"serviceActive", serviceActive)
			if !serviceActive {
				o.logger.Warn("app marked running but service not active",
					"app", app.Name,
					"serviceName", serviceName)
				// Try to restart health check - service might be starting
				o.appStore.UpdateStatus(app.Name, "starting")
				go o.waitForHealthy(app.Name)
			} else {
				o.logger.Debug("app service is active, keeping running status", "app", app.Name)
			}

		case "uninstalling":
			// Server crashed mid-uninstall - mark as error
			o.logger.Warn("found app stuck in uninstalling state", "app", app.Name)
			o.appStore.UpdateStatus(app.Name, "error")

		case "error", "failed":
			o.logger.Debug("app in error/failed state, will be checked by watchdog", "app", app.Name)
		}
	}

	// Regenerate Traefik routes now that system apps are registered
	// This ensures authentikEnabled=true and forward-auth middleware is generated
	if err := o.RegenerateRoutes(); err != nil {
		o.logger.Warn("failed to regenerate routes during reconciliation", "error", err)
	}

	// Ensure forward-auth providers are in the embedded outpost
	o.ensureForwardAuthOutpostAssociation()

	o.logger.Info("state reconciliation complete")
}

// StateWatchdogConfig configures the state watchdog
type StateWatchdogConfig struct {
	CheckInterval     time.Duration // How often to check (default: 30s)
	InstallingTimeout time.Duration // Max time in "installing" state (default: 10m)
	StartingTimeout   time.Duration // Max time in "starting" state (default: 5m)
}

// DefaultWatchdogConfig returns default watchdog configuration
func DefaultWatchdogConfig() StateWatchdogConfig {
	return StateWatchdogConfig{
		CheckInterval:     30 * time.Second,
		InstallingTimeout: 10 * time.Minute,
		StartingTimeout:   5 * time.Minute,
	}
}

// StartStateWatchdog starts a background goroutine that monitors for stuck states
// Returns a channel that can be closed to stop the watchdog
func (o *Orchestrator) StartStateWatchdog(cfg StateWatchdogConfig) chan struct{} {
	stop := make(chan struct{})

	go func() {
		ticker := time.NewTicker(cfg.CheckInterval)
		defer ticker.Stop()

		o.logger.Info("state watchdog started",
			"interval", cfg.CheckInterval,
			"installingTimeout", cfg.InstallingTimeout,
			"startingTimeout", cfg.StartingTimeout)

		for {
			select {
			case <-stop:
				o.logger.Info("state watchdog stopped")
				return
			case <-ticker.C:
				o.checkForStuckStates(cfg)
			}
		}
	}()

	return stop
}

// checkForStuckStates checks for apps that have been in transitional states too long
func (o *Orchestrator) checkForStuckStates(cfg StateWatchdogConfig) {
	apps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Warn("watchdog: failed to get apps", "error", err)
		return
	}

	now := time.Now()

	for _, app := range apps {
		stuckDuration := now.Sub(app.UpdatedAt)

		switch app.Status {
		case "installing":
			if stuckDuration > cfg.InstallingTimeout {
				o.logger.Warn("watchdog: app stuck in installing state",
					"app", app.Name,
					"duration", stuckDuration,
					"timeout", cfg.InstallingTimeout)
				o.appStore.UpdateStatus(app.Name, "error")
			}

		case "starting":
			if stuckDuration > cfg.StartingTimeout {
				o.logger.Warn("watchdog: app stuck in starting state",
					"app", app.Name,
					"duration", stuckDuration,
					"timeout", cfg.StartingTimeout)
				o.appStore.UpdateStatus(app.Name, "error")
			}

		case "uninstalling":
			if stuckDuration > cfg.InstallingTimeout {
				o.logger.Warn("watchdog: app stuck in uninstalling state",
					"app", app.Name,
					"duration", stuckDuration)
				o.appStore.UpdateStatus(app.Name, "error")
			}

		case "running":
			// Periodically verify running apps are still healthy via health check
			// Skip health checks for system apps - they're NixOS-managed
			if !app.IsSystem && !o.checkHealthOnce(app.Name) {
				o.logger.Warn("watchdog: running app health check failed",
					"app", app.Name)
				o.appStore.UpdateStatus(app.Name, "error")
			}

		case "error", "failed":
			// Check if errored apps have recovered - health check is sufficient proof
			healthOk := app.IsSystem || o.checkHealthOnce(app.Name)
			o.logger.Debug("watchdog: checking error/failed app recovery",
				"app", app.Name,
				"healthOk", healthOk,
				"isSystem", app.IsSystem)
			if healthOk {
				o.logger.Info("watchdog: app recovered",
					"app", app.Name)
				o.appStore.UpdateStatus(app.Name, "running")
			}
		}
	}
}

// checkHealthOnce performs a single health check for an app
// Returns true if healthy, false otherwise
func (o *Orchestrator) checkHealthOnce(appName string) bool {
	app, err := o.catalogCache.Get(appName)
	if err != nil || app == nil {
		// Can't get app info, assume healthy
		o.logger.Debug("checkHealthOnce: no catalog entry, assuming healthy", "app", appName)
		return true
	}

	// No health check configured, assume healthy
	if app.HealthCheck.Path == "" {
		o.logger.Debug("checkHealthOnce: no health check path configured, assuming healthy", "app", appName)
		return true
	}

	port := o.getAppPort(appName)
	if port == 0 {
		// No port, assume healthy
		o.logger.Debug("checkHealthOnce: no port configured, assuming healthy", "app", appName)
		return true
	}

	url := fmt.Sprintf("http://localhost:%d%s", port, app.HealthCheck.Path)
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		o.logger.Debug("checkHealthOnce: request failed", "app", appName, "url", url, "error", err)
		return false
	}
	defer resp.Body.Close()

	// Accept 2xx, 3xx, and auth errors (401/403) as healthy
	// Auth errors mean the service is running but requires authentication
	healthy := resp.StatusCode < 500
	if !healthy {
		o.logger.Debug("checkHealthOnce: unhealthy status", "app", appName, "url", url, "status", resp.StatusCode)
	}
	return healthy
}
