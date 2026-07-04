package reconciler

import (
	"context"
	"fmt"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/google/uuid"
)

// AppLifecycleManager abstracts the container lifecycle operations needed by the
// convergence loop. The portable orchestrator implements this interface.
// Each sub-step method corresponds to a phase of the app startup lifecycle:
// PreStart → EnsureContainer → HealthCheck → PostStart → SSO provisioning.
type AppLifecycleManager interface {
	PreStartApp(ctx context.Context, appName string) error
	EnsureContainer(ctx context.Context, appName string) error
	HealthCheckApp(ctx context.Context, appName string) error
	PostStartApp(ctx context.Context, appName string) error
	ProvisionSSO(ctx context.Context, appName string) error
	RemoveApp(ctx context.Context, appName string, clearData bool) error
	SyncContainerState(ctx context.Context)
	RegenerateRoutes() error
}

// TailnetNodeEnsurer abstracts tailnet node container lifecycle for the reconciler.
type TailnetNodeEnsurer interface {
	EnsureRunning(ctx context.Context, appName string) error
	StopAndPurge(ctx context.Context, appName string) error
}

// GatewayEnsurer abstracts gateway container teardown for the reconciler.
// Gateway startup is handled by RegenerateRoutes() — no need to duplicate here.
type GatewayEnsurer interface {
	StopAndPurge(ctx context.Context) error
}

// ProxyStopper abstracts remote proxy teardown for the reconciler.
type ProxyStopper interface {
	StopAll()
}

// TailnetDomainDiscoverer discovers the tailnet MagicDNS domain from the running gateway.
type TailnetDomainDiscoverer interface {
	GetTailnetDomain(ctx context.Context) (string, error)
}

// ForwardDomainProvisioner provisions a forward_domain SSO provider for a tailnet domain.
// Returns the outpost API token needed to start the standalone proxy outpost container.
type ForwardDomainProvisioner interface {
	EnsureForwardDomainAuth(cookieDomain string) (token string, err error)
}

// ProxyOutpostEnsurer abstracts the standalone proxy outpost container lifecycle.
// The proxy outpost handles forward-auth for tailnet access, running separately from
// the embedded outpost so it can redirect to the tailnet URL for login.
type ProxyOutpostEnsurer interface {
	EnsureRunning(ctx context.Context, token, tailnetDomain string) error
	Stop(ctx context.Context) error
}

// Config holds the dependencies the reconciler needs for convergence.
// When nil, the reconciler runs in stub mode (Phase 2 behavior).
type Config struct {
	Lifecycle        AppLifecycleManager
	AppStore         store.AppStoreInterface
	CatalogCache     catalog.CacheInterface
	Graph            catalog.AppGraphInterface
	TailnetStore     store.TailnetStoreInterface
	RemoteAppStore   store.RemoteAppStoreInterface
	TailnetNode      TailnetNodeEnsurer
	Gateway          GatewayEnsurer
	ProxyStopper     ProxyStopper
	ProxyOutpost     ProxyOutpostEnsurer     // optional: manages standalone proxy outpost for tailnet auth
	TailnetDomain    TailnetDomainDiscoverer  // optional: discovers tailnet MagicDNS domain
	ForwardDomainSSO ForwardDomainProvisioner // optional: provisions forward_domain SSO (nil when Authentik not installed)
}

// applyIntents processes a batch of intents, mutating stores (drain phase).
// Returns a map of app name → clearData for pending uninstalls.
func (r *Reconciler) applyIntents(intents []Intent, pendingClearData map[string]bool) {
	for _, intent := range intents {
		r.logger.Info("applying intent", "type", intentTypeName(intent), "id", intent.IntentID())
		switch i := intent.(type) {
		case InstallAppIntent:
			r.applyInstallIntent(i)
		case UninstallAppIntent:
			r.applyUninstallIntent(i, pendingClearData)
		case SetTailnetIntent:
			r.applySetTailnetIntent(i)
		case DeleteTailnetIntent:
			r.applyDeleteTailnetIntent()
		case AddRemoteAppIntent:
			r.applyAddRemoteAppIntent(i)
		case DeleteRemoteAppIntent:
			r.applyDeleteRemoteAppIntent(i)
		case RenameAppIntent:
			r.applyRenameAppIntent(i)
		default:
			r.logger.Info("unhandled intent type in drain phase", "type", intentTypeName(intent))
		}
	}
	r.logger.Info("drain phase complete", "applied", len(intents))
	r.activity.Record("drain_complete", fmt.Sprintf("%d intents", len(intents)))
}

// applyInstallIntent resolves dependencies and records apps in the store.
func (r *Reconciler) applyInstallIntent(intent InstallAppIntent) {
	cfg := r.config
	appName := intent.AppName

	// Skip if already running.
	if existing, _ := cfg.AppStore.GetByCatalogID(appName); existing != nil && existing.Status == "running" {
		r.logger.Info("app already running, skipping install intent", "app", appName)
		return
	}

	plan, err := cfg.Graph.PlanInstall(appName)
	if err != nil {
		r.logger.Error("failed to plan install", "app", appName, "error", err)
		return
	}
	if !plan.CanInstall {
		r.logger.Error("cannot install app", "app", appName, "blockers", plan.Blockers)
		return
	}

	integrations := buildIntegrationConfig(nil, plan.AutoConfig, plan.Choices)

	// Record dependency providers first.
	for _, provider := range integrations {
		if err := r.recordIntent(provider, nil); err != nil {
			r.logger.Error("failed to record dependency", "app", provider, "error", err)
			return
		}
	}

	// Record the target app with its integrations.
	if err := r.recordIntent(appName, integrations); err != nil {
		r.logger.Error("failed to record app", "app", appName, "error", err)
	}
}

// applyUninstallIntent marks an app as uninstalling and tracks its clearData flag.
func (r *Reconciler) applyUninstallIntent(intent UninstallAppIntent, pendingClearData map[string]bool) {
	cfg := r.config
	if err := cfg.AppStore.UpdateStatus(intent.AppName, "uninstalling"); err != nil {
		r.logger.Error("failed to mark app as uninstalling", "app", intent.AppName, "error", err)
		return
	}
	pendingClearData[intent.AppName] = intent.ClearData
}

// applySetTailnetIntent deletes any existing connection and creates a new one.
func (r *Reconciler) applySetTailnetIntent(intent SetTailnetIntent) {
	cfg := r.config
	if cfg.TailnetStore == nil {
		r.logger.Warn("tailnet store not configured, skipping SetTailnet intent")
		return
	}

	// Delete any existing active connection (MVP: single connection).
	existing, _ := cfg.TailnetStore.GetActive()
	if existing != nil {
		if err := cfg.TailnetStore.Delete(existing.ID); err != nil {
			r.logger.Error("failed to delete existing tailnet connection", "error", err)
			return
		}
	}

	conn := store.TailnetConnection{
		ID:         uuid.New().String(),
		Name:       intent.Name,
		Type:       intent.Type,
		AuthKey:    intent.AuthKey,
		ControlURL: intent.ControlURL,
		Status:     "active",
	}

	if err := cfg.TailnetStore.Create(conn); err != nil {
		r.logger.Error("failed to create tailnet connection", "error", err)
	}
}

// applyDeleteTailnetIntent removes the active tailnet connection from the store.
func (r *Reconciler) applyDeleteTailnetIntent() {
	cfg := r.config
	if cfg.TailnetStore == nil {
		r.logger.Warn("tailnet store not configured, skipping DeleteTailnet intent")
		return
	}

	conn, err := cfg.TailnetStore.GetActive()
	if err != nil {
		r.logger.Error("failed to get active tailnet connection", "error", err)
		return
	}
	if conn == nil {
		return
	}

	if err := cfg.TailnetStore.Delete(conn.ID); err != nil {
		r.logger.Error("failed to delete tailnet connection", "error", err)
	}
}

// applyAddRemoteAppIntent resolves catalog metadata and creates a remote app in the store.
func (r *Reconciler) applyAddRemoteAppIntent(intent AddRemoteAppIntent) {
	cfg := r.config
	if cfg.RemoteAppStore == nil {
		r.logger.Warn("remote app store not configured, skipping AddRemoteApp intent")
		return
	}

	catalogApp, err := cfg.CatalogCache.Get(intent.AppID)
	if err != nil {
		r.logger.Error("failed to resolve catalog app for remote app", "appId", intent.AppID, "error", err)
		return
	}

	bypassPaths := catalogApp.SSO.BypassPaths
	if bypassPaths == nil {
		bypassPaths = []string{}
	}

	app := store.RemoteApp{
		ID:                 uuid.New().String(),
		HostLabel:          intent.HostLabel,
		AppID:              intent.AppID,
		AppName:            catalogApp.DisplayName,
		SSOStrategy:        catalogApp.SSO.Strategy,
		BypassPaths:        bypassPaths,
		TailnetAddr:        intent.TailnetAddr,
		Status:             "active",
	}

	if err := cfg.RemoteAppStore.Create(app); err != nil {
		r.logger.Error("failed to create remote app", "appId", intent.AppID, "error", err)
	}
}

// applyDeleteRemoteAppIntent removes a remote app from the store.
func (r *Reconciler) applyDeleteRemoteAppIntent(intent DeleteRemoteAppIntent) {
	cfg := r.config
	if cfg.RemoteAppStore == nil {
		r.logger.Warn("remote app store not configured, skipping DeleteRemoteApp intent")
		return
	}

	if err := cfg.RemoteAppStore.Delete(intent.RemoteAppID); err != nil {
		r.logger.Error("failed to delete remote app", "id", intent.RemoteAppID, "error", err)
	}
}

// applyRenameAppIntent updates an app's display name in the store.
func (r *Reconciler) applyRenameAppIntent(intent RenameAppIntent) {
	cfg := r.config
	if err := cfg.AppStore.UpdateDisplayName(intent.AppName, intent.DisplayName); err != nil {
		r.logger.Error("failed to rename app", "app", intent.AppName, "error", err)
	}
}

// recordIntent writes an app to the store if it's not already running.
// Mirrors orchestrator.recordIntent but uses reconciler's config.
func (r *Reconciler) recordIntent(appName string, integrations map[string]string) error {
	cfg := r.config

	existing, err := cfg.AppStore.GetByCatalogID(appName)
	if err != nil {
		return err
	}
	if existing != nil && existing.Status == "running" {
		return nil
	}

	app, err := cfg.CatalogCache.Get(appName)
	if err != nil {
		return err
	}
	return cfg.AppStore.Install(app.CatalogID, app.DisplayName, app.Version, integrations, &store.InstallOptions{
		Port:     app.Port,
		IsSystem: app.IsSystem,
	})
}

// convergeFromStores reads all stores and drives the system toward the desired state.
func (r *Reconciler) convergeFromStores(ctx context.Context, pendingClearData map[string]bool) {
	cfg := r.config
	start := time.Now()

	r.activity.Record("converge_start", "")
	r.clearAppPhases()

	// Step 1: Sync container state (align DB with reality).
	r.logger.Info("convergence step", "step", "sync-container-state")
	r.activity.Record("converge_step", "sync-container-state")
	cfg.Lifecycle.SyncContainerState(ctx)

	apps, err := cfg.AppStore.GetAll()
	if err != nil {
		r.logger.Error("failed to load apps for convergence", "error", err)
		return
	}

	// Build map for lookups.
	appMap := make(map[string]*store.InstalledApp, len(apps))
	for _, app := range apps {
		appMap[app.CatalogID] = app
	}

	// Step 2: Handle uninstalls (apps with status "uninstalling").
	uninstallCount := 0
	for _, app := range apps {
		if app.Status == "uninstalling" {
			uninstallCount++
		}
	}
	r.logger.Info("convergence step", "step", "handle-uninstalls", "count", uninstallCount)
	r.activity.Record("converge_step", fmt.Sprintf("handle-uninstalls (%d)", uninstallCount))
	for _, app := range apps {
		if app.Status != "uninstalling" {
			continue
		}
		clearData := pendingClearData[app.CatalogID]
		if err := cfg.Lifecycle.RemoveApp(ctx, app.CatalogID, clearData); err != nil {
			r.logger.Error("failed to remove app", "app", app.CatalogID, "error", err)
		}
		delete(appMap, app.CatalogID)
	}

	// Step 3: Compute execution levels for remaining apps.
	r.logger.Info("convergence step", "step", "compute-levels")
	r.activity.Record("converge_step", "compute-levels")
	levels := computeLevels(appMap, cfg.CatalogCache)

	// Step 4: Ensure apps in level order (sub-step phases per app).
	r.logger.Info("convergence step", "step", "ensure-apps", "levels", len(levels))
	r.activity.Record("converge_step", fmt.Sprintf("ensure-apps (%d levels)", len(levels)))
	for _, levelApps := range levels {
		for _, appName := range levelApps {
			app := appMap[appName]
			if app == nil {
				continue
			}
			// Skip apps that are already running.
			if app.Status == "running" {
				continue
			}
			// Skip apps without container specs (system apps managed by bootstrap).
			if catalogApp, err := cfg.CatalogCache.Get(appName); err != nil || catalogApp.Container == nil {
				continue
			}
			r.ensureAppPhased(ctx, cfg, appName)
		}
	}

	// Step 5: Converge tailnet nodes/gateway/proxies.
	r.logger.Info("convergence step", "step", "converge-tailnet")
	r.activity.Record("converge_step", "converge-tailnet")
	r.convergeTailnet(ctx)

	// Step 6: Update graph with current installed list.
	r.logger.Info("convergence step", "step", "update-graph")
	r.activity.Record("converge_step", "update-graph")
	installed, _ := cfg.AppStore.GetInstalledCatalogIDs()
	cfg.Graph.SetInstalled(installed)

	// Step 7: Regenerate routes.
	r.logger.Info("convergence step", "step", "regenerate-routes")
	r.activity.Record("converge_step", "regenerate-routes")
	if err := cfg.Lifecycle.RegenerateRoutes(); err != nil {
		r.logger.Warn("failed to regenerate routes", "error", err)
	}

	// Step 8: Provision forward_domain SSO for tailnet access (best-effort).
	// If successful, regenerate routes again — the gateway may not have been
	// connected to tailscale when step 7 ran (race on first convergence), but
	// provisionTailnetSSO exercises the gateway connection, so by this point
	// the tailnet domain is available for route generation.
	if r.provisionTailnetSSO(ctx) {
		r.logger.Info("convergence step", "step", "regenerate-routes-tailnet")
		if err := cfg.Lifecycle.RegenerateRoutes(); err != nil {
			r.logger.Warn("failed to regenerate routes after tailnet SSO", "error", err)
		}
	}

	duration := time.Since(start)
	r.logger.Info("convergence pass complete", "apps", len(apps), "duration", duration)
	r.activity.Record("converge_complete", fmt.Sprintf("%d apps, %s", len(apps), duration.Round(time.Millisecond)))
	r.activity.Checkpoint()
}

// ensureAppPhased runs the app lifecycle sub-steps in sequence, recording
// activity and phase status at each boundary.
func (r *Reconciler) ensureAppPhased(ctx context.Context, cfg *Config, appName string) {
	type phase struct {
		name  string
		fn    func() error
		fatal bool
	}

	phases := []phase{
		{"pre-start", func() error { return cfg.Lifecycle.PreStartApp(ctx, appName) }, true},
		{"ensure-container", func() error { return cfg.Lifecycle.EnsureContainer(ctx, appName) }, true},
		{"health-check", func() error { return cfg.Lifecycle.HealthCheckApp(ctx, appName) }, true},
		{"post-start", func() error { return cfg.Lifecycle.PostStartApp(ctx, appName) }, true},
		{"sso", func() error { return cfg.Lifecycle.ProvisionSSO(ctx, appName) }, false},
	}

	failed := false
	for _, p := range phases {
		r.setAppPhase(appName, p.name, "active")
		r.activity.Record("app_phase", fmt.Sprintf("%s:%s", appName, p.name))
		if err := p.fn(); err != nil {
			if p.fatal {
				r.setAppPhase(appName, p.name, "error")
				_ = cfg.AppStore.UpdateStatus(appName, "error")
				r.logger.Error("app phase failed", "app", appName, "phase", p.name, "error", err)
				failed = true
				break
			}
			r.setAppPhase(appName, p.name, "warning")
			r.logger.Warn("app phase failed (non-fatal)", "app", appName, "phase", p.name, "error", err)
			continue
		}
		r.setAppPhase(appName, p.name, "done")
	}
	if !failed {
		_ = cfg.AppStore.UpdateStatus(appName, "running")
	}
}

// convergeTailnet ensures tailnet nodes/gateway/proxies match the tailnet store state.
// With an active tailnet: ensure tailnet nodes for running non-system apps.
// Without a tailnet: purge all tailnet nodes, stop gateway, stop proxies.
// All operations are idempotent — safe to run every convergence pass.
func (r *Reconciler) convergeTailnet(ctx context.Context) {
	cfg := r.config
	if cfg.TailnetStore == nil {
		return
	}

	conn, err := cfg.TailnetStore.GetActive()
	if err != nil {
		r.logger.Error("failed to get active tailnet for convergence", "error", err)
		return
	}

	apps, err := cfg.AppStore.GetAll()
	if err != nil {
		r.logger.Error("failed to list apps for tailnet convergence", "error", err)
		return
	}

	if conn != nil {
		// Active tailnet: ensure tailnet nodes for running non-system apps.
		if cfg.TailnetNode == nil {
			return
		}
		for _, app := range apps {
			if app.IsSystem || app.Status != "running" {
				continue
			}
			if err := cfg.TailnetNode.EnsureRunning(ctx, app.CatalogID); err != nil {
				r.logger.Warn("failed to ensure tailnet node", "app", app.CatalogID, "error", err)
				continue
			}
			_ = cfg.AppStore.SetTailnetID(app.CatalogID, conn.ID)
		}
		return
	}

	// No tailnet: purge tailnet nodes, gateway, proxy outpost, and proxies.
	if cfg.TailnetNode != nil {
		for _, app := range apps {
			if app.IsSystem {
				continue
			}
			if err := cfg.TailnetNode.StopAndPurge(ctx, app.CatalogID); err != nil {
				r.logger.Warn("failed to purge tailnet node", "app", app.CatalogID, "error", err)
			}
			_ = cfg.AppStore.SetTailnetID(app.CatalogID, "")
		}
	}
	if cfg.Gateway != nil {
		if err := cfg.Gateway.StopAndPurge(ctx); err != nil {
			r.logger.Warn("failed to purge gateway", "error", err)
		}
	}
	if cfg.ProxyOutpost != nil {
		if err := cfg.ProxyOutpost.Stop(ctx); err != nil {
			r.logger.Warn("failed to stop proxy outpost", "error", err)
		}
	}
	if cfg.ProxyStopper != nil {
		cfg.ProxyStopper.StopAll()
	}
}

// provisionTailnetSSO ensures a forward_domain Authentik proxy provider and standalone
// outpost exist for the tailnet MagicDNS domain. The standalone outpost runs separately
// from the embedded outpost so it can use AUTHENTIK_HOST_BROWSER pointing to the tailnet
// URL, while the embedded outpost continues serving local auth on localhost.
// Best-effort: logs warnings and returns on failure (retried next pass).
func (r *Reconciler) provisionTailnetSSO(ctx context.Context) bool {
	cfg := r.config
	if cfg.TailnetDomain == nil || cfg.ForwardDomainSSO == nil {
		return false
	}

	// Only provision when there's an active tailnet.
	if cfg.TailnetStore == nil {
		return false
	}
	conn, err := cfg.TailnetStore.GetActive()
	if err != nil || conn == nil {
		return false
	}

	r.logger.Info("convergence step", "step", "provision-tailnet-sso")
	r.activity.Record("converge_step", "provision-tailnet-sso")

	domain, err := cfg.TailnetDomain.GetTailnetDomain(ctx)
	if err != nil {
		r.logger.Warn("failed to discover tailnet domain (gateway not ready?)", "error", err)
		return false
	}

	token, err := cfg.ForwardDomainSSO.EnsureForwardDomainAuth(domain)
	if err != nil {
		r.logger.Warn("failed to provision tailnet forward_domain SSO", "error", err, "domain", domain)
		return false
	}

	// Start the standalone proxy outpost container with the tailnet domain.
	if cfg.ProxyOutpost != nil {
		if err := cfg.ProxyOutpost.EnsureRunning(ctx, token, domain); err != nil {
			r.logger.Warn("failed to start proxy outpost", "error", err, "domain", domain)
			return false
		}
	}

	r.logger.Info("tailnet forward_domain SSO provisioned", "domain", domain)
	return true
}

// buildIntegrationConfig builds the integration configuration map from user choices,
// auto-config tasks, and integration choices. Pure function (reimplemented from
// orchestrator/config_builder.go to avoid circular imports).
func buildIntegrationConfig(
	userChoices map[string]string,
	autoConfig []catalog.ConfigTask,
	choices []catalog.IntegrationChoice,
) map[string]string {
	config := make(map[string]string)

	if userChoices != nil {
		for k, v := range userChoices {
			config[k] = v
		}
	}

	for _, auto := range autoConfig {
		config[auto.Integration] = auto.Source
	}

	for _, choice := range choices {
		if _, hasChoice := config[choice.Integration]; hasChoice {
			continue
		}
		if !choice.Required {
			continue
		}
		if choice.Recommended != "" {
			config[choice.Integration] = choice.Recommended
		}
	}

	return config
}

// computeLevels computes execution levels for apps. Level 0 contains apps with no
// dependencies (leaf nodes). Level N contains apps whose dependencies are all in
// levels < N. Reimplemented from orchestrator/reconcile.go to avoid circular imports.
func computeLevels(apps map[string]*store.InstalledApp, catalogCache catalog.CacheInterface) [][]string {
	// Build dependency graph: app → apps it depends on.
	deps := make(map[string][]string)
	for name, app := range apps {
		for _, source := range app.IntegrationConfig {
			if _, installed := apps[source]; installed {
				deps[name] = append(deps[name], source)
			}
		}

		if catalogCache == nil {
			continue
		}
		catalogApp, err := catalogCache.Get(name)
		if err != nil || catalogApp == nil {
			continue
		}
		for _, integration := range catalogApp.Integrations {
			if integration.Required {
				continue
			}
			for _, compatible := range integration.Compatible {
				if _, installed := apps[compatible.App]; installed {
					deps[name] = append(deps[name], compatible.App)
				}
			}
		}
	}

	// Compute level for each app.
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

	for name := range apps {
		computeLevel(name)
	}

	// Group apps by level.
	maxLevel := 0
	for _, level := range levels {
		if level > maxLevel {
			maxLevel = level
		}
	}

	if len(levels) == 0 {
		return nil
	}

	result := make([][]string, maxLevel+1)
	for name, level := range levels {
		result[level] = append(result[level], name)
	}

	return result
}

// Compile-time assertion that portable orchestrator must implement this interface.
// This is verified by the importing package (orchestrator) — not here, since we
// can't import orchestrator from reconciler without creating a circular dependency.
//
// The assertion lives in orchestrator_portable.go or a dedicated file in the
// orchestrator package:
//   var _ reconciler.AppLifecycleManager = (*PortableOrchestrator)(nil)

// We also need a compile-time check for the fakes in tests — done in fakes_test.go.
