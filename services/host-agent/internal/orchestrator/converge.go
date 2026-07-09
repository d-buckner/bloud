package orchestrator

import (
	"context"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/google/uuid"
)

// TailnetNodeEnsurer abstracts tailnet node container lifecycle.
type TailnetNodeEnsurer interface {
	EnsureRunning(ctx context.Context, appName string) error
	StopAndPurge(ctx context.Context, appName string) error
}

// GatewayEnsurer abstracts gateway container teardown.
type GatewayEnsurer interface {
	StopAndPurge(ctx context.Context) error
}

// ProxyStopper abstracts remote proxy teardown.
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
type ProxyOutpostEnsurer interface {
	EnsureRunning(ctx context.Context, token, tailnetDomain string) error
	Stop(ctx context.Context) error
}

// ConvergeConfig holds the optional convergence dependencies.
// When nil/zero, the convergence phase is a no-op.
type ConvergeConfig struct {
	AppStore         store.AppStoreInterface
	CatalogGraph     catalog.AppGraphInterface
	TailnetStore     store.TailnetStoreInterface
	RemoteAppStore   store.RemoteAppStoreInterface
	TailnetNode      TailnetNodeEnsurer
	Gateway          GatewayEnsurer
	ProxyStopper     ProxyStopper
	ProxyOutpost     ProxyOutpostEnsurer
	TailnetDomain    TailnetDomainDiscoverer
	ForwardDomainSSO ForwardDomainProvisioner
}

// WithConvergeConfig wires the convergence dependencies into the orchestrator.
// Returns the receiver for chaining.
func (o *Orchestrator) WithConvergeConfig(cfg ConvergeConfig) *Orchestrator {
	o.appStore = cfg.AppStore
	o.catalogGraph = cfg.CatalogGraph
	o.tailnetStore = cfg.TailnetStore
	o.remoteAppStore = cfg.RemoteAppStore
	o.tailnetNode = cfg.TailnetNode
	o.gateway = cfg.Gateway
	o.proxyStopper = cfg.ProxyStopper
	o.proxyOutpost = cfg.ProxyOutpost
	o.tailnetDomain = cfg.TailnetDomain
	o.forwardDomainSSO = cfg.ForwardDomainSSO
	return o
}

// applyIntents processes a batch of intents, mutating stores (drain phase).
// pendingClearData accumulates the clearData flags for pending uninstalls.
func (o *Orchestrator) applyIntents(intents []Intent, pendingClearData map[string]bool) {
	for _, intent := range intents {
		o.logger.Info("applying intent", "type", intentTypeName(intent), "id", intent.IntentID())
		switch i := intent.(type) {
		case InstallAppIntent:
			o.applyInstallIntent(i)
		case UninstallAppIntent:
			o.applyUninstallIntent(i, pendingClearData)
		case SetTailnetIntent:
			o.applySetTailnetIntent(i)
		case DeleteTailnetIntent:
			o.applyDeleteTailnetIntent()
		case AddRemoteAppIntent:
			o.applyAddRemoteAppIntent(i)
		case DeleteRemoteAppIntent:
			o.applyDeleteRemoteAppIntent(i)
		case RenameAppIntent:
			o.applyRenameAppIntent(i)
		default:
			o.logger.Info("unhandled intent type in drain phase", "type", intentTypeName(intent))
		}
	}
	o.logger.Info("drain phase complete", "applied", len(intents))
}

// applyInstallIntent resolves dependencies and records apps in the store.
func (o *Orchestrator) applyInstallIntent(intent InstallAppIntent) {
	if o.appStore == nil {
		return
	}
	appName := intent.AppName

	// Skip if already running.
	if existing, _ := o.appStore.GetByCatalogID(appName); existing != nil && existing.Status == "running" {
		o.logger.Info("app already running, skipping install intent", "app", appName)
		return
	}

	if o.catalogGraph == nil {
		return
	}

	plan, err := o.catalogGraph.PlanInstall(appName)
	if err != nil {
		o.logger.Error("failed to plan install", "app", appName, "error", err)
		return
	}
	if !plan.CanInstall {
		o.logger.Error("cannot install app", "app", appName, "blockers", plan.Blockers)
		return
	}

	integrations := buildIntegrationConfig(nil, plan.AutoConfig, plan.Choices)

	// Record dependency providers first.
	for _, provider := range integrations {
		if err := o.recordIntent(provider, nil); err != nil {
			o.logger.Error("failed to record dependency", "app", provider, "error", err)
			return
		}
	}

	// Record the target app with its integrations.
	if err := o.recordIntent(appName, integrations); err != nil {
		o.logger.Error("failed to record app", "app", appName, "error", err)
	}
}

// applyUninstallIntent marks an app as uninstalling and tracks its clearData flag.
func (o *Orchestrator) applyUninstallIntent(intent UninstallAppIntent, pendingClearData map[string]bool) {
	if o.appStore == nil {
		return
	}
	if err := o.appStore.UpdateStatus(intent.AppName, "uninstalling"); err != nil {
		o.logger.Error("failed to mark app as uninstalling", "app", intent.AppName, "error", err)
		return
	}
	pendingClearData[intent.AppName] = intent.ClearData
}

// applySetTailnetIntent deletes any existing connection and creates a new one.
func (o *Orchestrator) applySetTailnetIntent(intent SetTailnetIntent) {
	if o.tailnetStore == nil {
		o.logger.Warn("tailnet store not configured, skipping SetTailnet intent")
		return
	}

	// Delete any existing active connection (MVP: single connection).
	existing, _ := o.tailnetStore.GetActive()
	if existing != nil {
		if err := o.tailnetStore.Delete(existing.ID); err != nil {
			o.logger.Error("failed to delete existing tailnet connection", "error", err)
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

	if err := o.tailnetStore.Create(conn); err != nil {
		o.logger.Error("failed to create tailnet connection", "error", err)
	}
}

// applyDeleteTailnetIntent removes the active tailnet connection from the store.
func (o *Orchestrator) applyDeleteTailnetIntent() {
	if o.tailnetStore == nil {
		o.logger.Warn("tailnet store not configured, skipping DeleteTailnet intent")
		return
	}

	conn, err := o.tailnetStore.GetActive()
	if err != nil {
		o.logger.Error("failed to get active tailnet connection", "error", err)
		return
	}
	if conn == nil {
		return
	}

	if err := o.tailnetStore.Delete(conn.ID); err != nil {
		o.logger.Error("failed to delete tailnet connection", "error", err)
	}
}

// applyAddRemoteAppIntent resolves catalog metadata and creates a remote app in the store.
func (o *Orchestrator) applyAddRemoteAppIntent(intent AddRemoteAppIntent) {
	if o.remoteAppStore == nil {
		o.logger.Warn("remote app store not configured, skipping AddRemoteApp intent")
		return
	}

	catalogApp, err := o.catalog.Get(intent.AppID)
	if err != nil {
		o.logger.Error("failed to resolve catalog app for remote app", "appId", intent.AppID, "error", err)
		return
	}
	if catalogApp == nil {
		o.logger.Error("catalog app not found for remote app", "appId", intent.AppID)
		return
	}

	bypassPaths := catalogApp.SSO.BypassPaths
	if bypassPaths == nil {
		bypassPaths = []string{}
	}

	app := store.RemoteApp{
		ID:          uuid.New().String(),
		HostLabel:   intent.HostLabel,
		AppID:       intent.AppID,
		AppName:     catalogApp.DisplayName,
		SSOStrategy: catalogApp.SSO.Strategy,
		BypassPaths: bypassPaths,
		TailnetAddr: intent.TailnetAddr,
		Status:      "active",
	}

	if err := o.remoteAppStore.Create(app); err != nil {
		o.logger.Error("failed to create remote app", "appId", intent.AppID, "error", err)
	}
}

// applyDeleteRemoteAppIntent removes a remote app from the store.
func (o *Orchestrator) applyDeleteRemoteAppIntent(intent DeleteRemoteAppIntent) {
	if o.remoteAppStore == nil {
		o.logger.Warn("remote app store not configured, skipping DeleteRemoteApp intent")
		return
	}

	if err := o.remoteAppStore.Delete(intent.RemoteAppID); err != nil {
		o.logger.Error("failed to delete remote app", "id", intent.RemoteAppID, "error", err)
	}
}

// applyRenameAppIntent updates an app's display name in the store.
func (o *Orchestrator) applyRenameAppIntent(intent RenameAppIntent) {
	if o.appStore == nil {
		return
	}
	if err := o.appStore.UpdateDisplayName(intent.AppName, intent.DisplayName); err != nil {
		o.logger.Error("failed to rename app", "app", intent.AppName, "error", err)
	}
}

// recordIntent writes an app to the store if it's not already running.
func (o *Orchestrator) recordIntent(appName string, integrations map[string]string) error {
	if o.appStore == nil || o.catalog == nil {
		return nil
	}

	existing, err := o.appStore.GetByCatalogID(appName)
	if err != nil {
		return err
	}
	if existing != nil && existing.Status == "running" {
		return nil
	}

	app, err := o.catalog.Get(appName)
	if err != nil {
		return err
	}
	return o.appStore.Install(app.CatalogID, app.DisplayName, app.Version, integrations, &store.InstallOptions{
		Port:     app.Port,
		IsSystem: app.IsSystem,
	})
}

// convergeFromStores reads all stores and drives the system toward the desired state.
func (o *Orchestrator) convergeFromStores(ctx context.Context, pendingClearData map[string]bool) {
	if o.appStore == nil {
		return
	}
	start := time.Now()

	// Step 1: Sync container state (align DB with reality).
	o.logger.Info("convergence step", "step", "sync-container-state")
	if o.stateSyncer != nil {
		o.stateSyncer.SyncContainerState(ctx)
	}

	apps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Error("failed to load apps for convergence", "error", err)
		return
	}

	// Build map for lookups.
	appMap := make(map[string]*store.InstalledApp, len(apps))
	for _, app := range apps {
		appMap[app.CatalogID] = app
	}

	// Step 2: Handle uninstalls (apps with status "uninstalling").
	o.logger.Info("convergence step", "step", "handle-uninstalls")
	for _, app := range apps {
		if app.Status != "uninstalling" {
			continue
		}
		clearData := pendingClearData[app.CatalogID]
		if err := o.RemoveApp(ctx, app.CatalogID, clearData); err != nil {
			o.logger.Error("failed to remove app", "app", app.CatalogID, "error", err)
		}
		// Uninstall from store (RemoveApp handles container + graph; store is separate).
		if err := o.appStore.Uninstall(app.CatalogID); err != nil {
			o.logger.Error("failed to uninstall app from store", "app", app.CatalogID, "error", err)
		}
		delete(appMap, app.CatalogID)
	}

	// Step 3: Set graph targets to RUNNING so the Orchestrator drives app lifecycle.
	// Nodes and edges are populated here so the Orchestrator enforces dependency ordering.
	o.logger.Info("convergence step", "step", "set-graph-targets")
	appDeps := computeAppDeps(appMap, o.catalog)

	// Ensure all nodes exist before adding edges.
	for appName := range appMap {
		if existing, _ := o.graph.GetNode(appName); existing == nil {
			_ = o.graph.AddNode(appName)
		}
	}

	// Register dependency edges.
	for appName, deps := range appDeps {
		for _, dep := range deps {
			_ = o.graph.AddEdge(appName, dep)
		}
	}

	// Set all targets to RUNNING.
	for appName := range appMap {
		_ = o.graph.SetTargetStatus(appName, graph.StatusRunning)
	}

	// Step 4: Converge tailnet nodes/gateway/proxies.
	o.logger.Info("convergence step", "step", "converge-tailnet")
	o.convergeTailnet(ctx)

	// Step 5: Update catalog graph with current installed list.
	o.logger.Info("convergence step", "step", "update-graph")
	if o.catalogGraph != nil {
		installed, _ := o.appStore.GetInstalledCatalogIDs()
		o.catalogGraph.SetInstalled(installed)
	}

	// Step 6: Regenerate routes.
	o.logger.Info("convergence step", "step", "regenerate-routes")
	if o.routeGenerator != nil {
		if err := o.routeGenerator.RegenerateRoutes(); err != nil {
			o.logger.Warn("failed to regenerate routes", "error", err)
		}
	}

	// Step 7: Provision forward_domain SSO for tailnet access (best-effort).
	if o.provisionTailnetSSO(ctx) {
		o.logger.Info("convergence step", "step", "regenerate-routes-tailnet")
		if o.routeGenerator != nil {
			if err := o.routeGenerator.RegenerateRoutes(); err != nil {
				o.logger.Warn("failed to regenerate routes after tailnet SSO", "error", err)
			}
		}
	}

	duration := time.Since(start)
	o.logger.Info("convergence pass complete", "apps", len(apps), "duration", duration)
}

// convergeTailnet ensures tailnet nodes/gateway/proxies match the tailnet store state.
func (o *Orchestrator) convergeTailnet(ctx context.Context) {
	if o.tailnetStore == nil {
		return
	}

	conn, err := o.tailnetStore.GetActive()
	if err != nil {
		o.logger.Error("failed to get active tailnet for convergence", "error", err)
		return
	}

	apps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Error("failed to list apps for tailnet convergence", "error", err)
		return
	}

	if conn != nil {
		// Active tailnet: ensure tailnet nodes for running non-system apps.
		if o.tailnetNode == nil {
			return
		}
		for _, app := range apps {
			if app.IsSystem || app.Status != "running" {
				continue
			}
			if err := o.tailnetNode.EnsureRunning(ctx, app.CatalogID); err != nil {
				o.logger.Warn("failed to ensure tailnet node", "app", app.CatalogID, "error", err)
				continue
			}
			_ = o.appStore.SetTailnetID(app.CatalogID, conn.ID)
		}
		return
	}

	// No tailnet: purge tailnet nodes, gateway, proxy outpost, and proxies.
	if o.tailnetNode != nil {
		for _, app := range apps {
			if app.IsSystem {
				continue
			}
			if err := o.tailnetNode.StopAndPurge(ctx, app.CatalogID); err != nil {
				o.logger.Warn("failed to purge tailnet node", "app", app.CatalogID, "error", err)
			}
			_ = o.appStore.SetTailnetID(app.CatalogID, "")
		}
	}
	if o.gateway != nil {
		if err := o.gateway.StopAndPurge(ctx); err != nil {
			o.logger.Warn("failed to purge gateway", "error", err)
		}
	}
	if o.proxyOutpost != nil {
		if err := o.proxyOutpost.Stop(ctx); err != nil {
			o.logger.Warn("failed to stop proxy outpost", "error", err)
		}
	}
	if o.proxyStopper != nil {
		o.proxyStopper.StopAll()
	}
}

// provisionTailnetSSO ensures a forward_domain Authentik proxy provider and standalone
// outpost exist for the tailnet MagicDNS domain. Best-effort: logs warnings on failure.
func (o *Orchestrator) provisionTailnetSSO(ctx context.Context) bool {
	if o.tailnetDomain == nil || o.forwardDomainSSO == nil {
		return false
	}

	if o.tailnetStore == nil {
		return false
	}
	conn, err := o.tailnetStore.GetActive()
	if err != nil || conn == nil {
		return false
	}

	o.logger.Info("convergence step", "step", "provision-tailnet-sso")

	domain, err := o.tailnetDomain.GetTailnetDomain(ctx)
	if err != nil {
		o.logger.Warn("failed to discover tailnet domain (gateway not ready?)", "error", err)
		return false
	}

	token, err := o.forwardDomainSSO.EnsureForwardDomainAuth(domain)
	if err != nil {
		o.logger.Warn("failed to provision tailnet forward_domain SSO", "error", err, "domain", domain)
		return false
	}

	if o.proxyOutpost != nil {
		if err := o.proxyOutpost.EnsureRunning(ctx, token, domain); err != nil {
			o.logger.Warn("failed to start proxy outpost", "error", err, "domain", domain)
			return false
		}
	}

	o.logger.Info("tailnet forward_domain SSO provisioned", "domain", domain)
	return true
}

// computeAppDeps builds a map of app name → list of installed dependency names.
func computeAppDeps(apps map[string]*store.InstalledApp, catalogCache catalog.CacheInterface) map[string][]string {
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
	return deps
}

// intentTypeName returns a human-readable name for an intent type.
func intentTypeName(intent Intent) string {
	switch intent.(type) {
	case InstallAppIntent:
		return "InstallApp"
	case UninstallAppIntent:
		return "UninstallApp"
	case RenameAppIntent:
		return "RenameApp"
	case SetTailnetIntent:
		return "SetTailnet"
	case DeleteTailnetIntent:
		return "DeleteTailnet"
	case AddRemoteAppIntent:
		return "AddRemoteApp"
	case DeleteRemoteAppIntent:
		return "DeleteRemoteApp"
	case CreateShareIntent:
		return "CreateShare"
	case RevokeShareIntent:
		return "RevokeShare"
	case ClearAppDataIntent:
		return "ClearAppData"
	case ConvergeIntent:
		return "Converge"
	default:
		return "Unknown"
	}
}
