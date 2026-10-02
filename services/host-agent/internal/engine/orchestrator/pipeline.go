// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/google/uuid"
)

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
		case SetPublicURLIntent:
			o.applySetPublicURLIntent(i)
		case SetInferenceIntent:
			o.applySetInferenceIntent(i)
		case ReconcileIntent:
			// The timer pass asks for no change of its own, so there is no
			// user request to record. The one thing it does drive is the
			// retry of nodes whose failure was marked retryable: without a
			// consumer, that flag is decoration and an app that failed once
			// stays down until a human submits an install.
			o.retryErroredNodes()
		default:
			o.logger.Warn("unhandled intent type in drain phase", "type", intentTypeName(intent))
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

	// An explicit install intent is the user's "retry": reset any of the
	// app's nodes stuck in the terminal ERROR state so the convergence pass
	// re-runs their full lifecycle. collectWorkForLevel never retries ERROR
	// nodes on its own: without this reset, retrying a failed/degraded app
	// would leave it stuck at "installing" forever.
	o.resetErroredNodes(appName)

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
	o.logger.Info("install plan resolved", "app", appName, "deps", len(integrations), "integrations", integrations)

	// Record dependency providers first.
	for _, provider := range integrations {
		o.logger.Info("recording dependency provider", "app", appName, "provider", provider)
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

// resetErroredNodes resets the app's graph nodes from the terminal ERROR
// state back to INITIALIZING. Called when an install intent is applied, the
// explicit reset that makes "Retry install" recover failed and degraded apps.
func (o *Orchestrator) resetErroredNodes(appName string) {
	if o.catalog == nil {
		return
	}
	app, err := o.catalog.Get(appName)
	if err != nil || app == nil {
		return
	}
	nodeIDs := make([]string, 0, len(app.Containers)+1)
	if len(app.Containers) > 0 {
		for _, def := range app.Containers {
			nodeIDs = append(nodeIDs, def.Name)
		}
	} else {
		nodeIDs = append(nodeIDs, appName)
	}
	for _, id := range nodeIDs {
		node, err := o.graph.GetNode(id)
		if err != nil || node == nil || node.ActualStatus != graph.StatusError {
			continue
		}
		o.logger.Info("resetting errored node for install retry", "node", id, "error", node.Error)
		_ = o.graph.SetActualStatus(id, graph.StatusInitializing, "")
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
	o.recordOpStart(intent.AppName, store.OpTypeUninstall, store.OpPhaseTopology)
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

// applySetPublicURLIntent persists the new public address, swaps the runtime
// URL state, and resets SSO-dependent nodes so the convergence pass that
// follows this drain re-runs their full lifecycle: PreStart rewrites app
// configs with the new URLs (recreating changed containers), ensureSSO
// re-provisions the Authentik providers with the new redirect URIs, and
// PostStart re-applies outpost/launch configuration.
func (o *Orchestrator) applySetPublicURLIntent(intent SetPublicURLIntent) {
	public, err := hostset.ParsePublicURL(intent.URL)
	if err != nil {
		o.logger.Error("rejected public url", "url", intent.URL, "error", err)
		return
	}

	// The served port is re-applied here rather than inherited, because the
	// set is built from scratch. A set that lost it would render the detected
	// LAN URLs on port 80 while the entrypoint serves something else, and the
	// redirect URIs would be re-registered against a port nothing answers on.
	hs := hostset.New(public).WithServedPort(o.config.TraefikPort)

	// No-op guard: skip the side effects when the derived address did not
	// change. The base URL is compared rather than the raw string, so a save
	// that only reformatted the same origin (trailing slash, an explicit
	// default port, mixed case) does not restart every SSO app for nothing.
	if o.hosts != nil && o.hosts.Get().PrimaryBaseURL() == hs.PrimaryBaseURL() {
		o.logger.Info("public url unchanged, skipping side effects", "url", hs.PrimaryBaseURL())
		return
	}

	o.logger.Info("applying public url", "url", hs.PrimaryBaseURL())

	// 1. Persist the normalized origin, so what the store holds and what the
	//    live set derives cannot diverge.
	if o.settings != nil {
		if err := o.settings.Set(store.SettingPublicURL, hs.PrimaryBaseURL()); err != nil {
			o.logger.Error("failed to persist public url", "error", err)
			return
		}
	}

	// 2. Swap the runtime URL state before the convergence pass so SSO
	//    provisioning and app config generation see the new URLs.
	if o.hosts != nil {
		o.hosts.Set(hs)
	}

	// 3. Reset SSO-dependent nodes so they re-run their full lifecycle.
	o.resetSSONodes()

	// 4. Notify the API layer (dashboard OAuth app re-ensure, etc.).
	if o.onHostsChanged != nil {
		o.onHostsChanged()
	}
}

// resetSSONodes resets every RUNNING node that depends on the host set back to
// INITIALIZING: all containers of installed native-oidc / forward-auth apps
// (their configs and SSO providers bake in host URLs) plus the Authentik
// server container (its PostStart sets the embedded outpost's browser URL).
func (o *Orchestrator) resetSSONodes() {
	if o.graph == nil || o.appStore == nil {
		return
	}
	for _, nodeID := range o.ssoDependentNodes() {
		node, err := o.graph.GetNode(nodeID)
		if err != nil || node == nil || node.ActualStatus != graph.StatusRunning {
			continue
		}
		o.logger.Info("resetting SSO-dependent node for host change", "node", nodeID)
		_ = o.graph.SetActualStatus(nodeID, graph.StatusInitializing, "")
	}
}

// ssoDependentNodes lists the nodes whose config or provider bakes in the host
// set: the Authentik server container, plus every container of each installed
// app that joined the identity provider.
func (o *Orchestrator) ssoDependentNodes() []string {
	var nodes []string
	// Authentik server: PostStart re-applies the embedded outpost host URL.
	if _, err := o.appStore.GetByCatalogID("authentik"); err == nil {
		nodes = append(nodes, "apps-authentik-server")
	}
	apps, err := o.appStore.GetAll()
	if err != nil {
		return nodes
	}
	for _, app := range apps {
		nodes = append(nodes, o.ssoAppNodes(app)...)
	}
	return nodes
}

// ssoAppNodes returns the container nodes of one installed app when its SSO
// strategy makes it host-dependent, or the app's own node when it declares no
// containers of its own.
func (o *Orchestrator) ssoAppNodes(app *store.InstalledApp) []string {
	if app.IsSystem || o.catalog == nil {
		return nil
	}
	catalogApp, err := o.catalog.Get(app.CatalogID)
	if err != nil || catalogApp == nil {
		return nil
	}
	switch catalogApp.SSO.Strategy {
	case "native-oidc", "forward-auth":
	default:
		return nil
	}
	if len(catalogApp.Containers) == 0 {
		return []string{app.CatalogID}
	}
	nodes := make([]string, 0, len(catalogApp.Containers))
	for _, def := range catalogApp.Containers {
		nodes = append(nodes, def.Name)
	}
	return nodes
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
		o.logger.Info("skipping record: app already running", "app", appName)
		return nil
	}

	app, err := o.catalog.Get(appName)
	if err != nil {
		return err
	}
	o.logger.Info("recording app in store", "app", appName, "integrations", len(integrations))
	return o.appStore.Install(app.CatalogID, app.DisplayName, app.Version, integrations, &store.InstallOptions{
		Port:     app.Port,
		IsSystem: app.IsSystem,
	})
}

// ensureSystemAppsInstalled records all system apps in the store if not already present.
// On first boot this inserts them; on subsequent boots it's a no-op.
func (o *Orchestrator) ensureSystemAppsInstalled() {
	if o.appStore == nil || o.catalog == nil {
		return
	}
	allApps, err := o.catalog.GetAll()
	if err != nil {
		o.logger.Warn("failed to load catalog for system app install", "error", err)
		return
	}
	for _, app := range allApps {
		if !app.IsSystem {
			continue
		}
		existing, _ := o.appStore.GetByCatalogID(app.CatalogID)
		if existing != nil {
			continue
		}
		o.logger.Info("auto-installing system app", "app", app.CatalogID)
		if err := o.appStore.Install(app.CatalogID, app.DisplayName, app.Version, nil, &store.InstallOptions{
			Port:     app.Port,
			IsSystem: true,
		}); err != nil {
			o.logger.Warn("failed to auto-install system app", "app", app.CatalogID, "error", err)
		}
	}
}

// convergeFromStores reads all stores and drives the system toward the desired state.
func (o *Orchestrator) convergeFromStores(ctx context.Context, pendingClearData map[string]bool) {
	if o.appStore == nil {
		return
	}
	start := time.Now()

	// Step 0: Ensure system apps are installed in the store.
	o.ensureSystemAppsInstalled()

	// Step 1: Sync container state (align DB with reality).
	o.logger.Info("convergence step", "step", "sync-container-state")
	o.recordActivity("converge_step", "sync-container-state")
	o.SyncContainerState(ctx)

	apps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Error("failed to load apps for convergence", "error", err)
		return
	}
	o.logger.Info("loaded apps for convergence", "total", len(apps))

	// Build map for lookups.
	appMap := make(map[string]*store.InstalledApp, len(apps))
	for _, app := range apps {
		appMap[app.CatalogID] = app
	}

	// Step 2: Handle uninstalls (apps with status "uninstalling").
	o.logger.Info("convergence step", "step", "handle-uninstalls")
	o.recordActivity("converge_step", "handle-uninstalls")
	o.convergeUninstalls(ctx, apps, appMap, pendingClearData)

	// Step 3: Set graph targets to RUNNING so the Orchestrator drives app lifecycle.
	// Nodes and edges are populated here so the Orchestrator enforces dependency ordering.
	o.logger.Info("convergence step", "step", "set-graph-targets")
	o.recordActivity("converge_step", "set-graph-targets")
	o.populateGraphNodes(appMap)

	// Step 4: Converge tailnet nodes/gateway/proxies.
	o.logger.Info("convergence step", "step", "converge-tailnet")
	o.recordActivity("converge_step", "converge-tailnet")
	o.convergeTailnet(ctx)

	// Step 5: Update catalog graph with current installed list.
	o.logger.Info("convergence step", "step", "update-graph")
	o.recordActivity("converge_step", "update-graph")
	if o.catalogGraph != nil {
		installed, _ := o.appStore.GetInstalledCatalogIDs()
		o.catalogGraph.SetInstalled(installed)
	}

	// Step 6: Run reconcile pass, which drives per-app lifecycle phases and regenerates routes.
	o.logger.Info("convergence step", "step", "reconcile")
	o.recordActivity("converge_step", "reconcile")
	if err := o.Reconcile(ctx); err != nil {
		o.logger.Warn("reconcile failed", "error", err)
	}

	// Step 7: Provision forward_domain SSO for tailnet access (best-effort).
	if o.provisionTailnetSSO(ctx) {
		o.logger.Info("convergence step", "step", "sync-routes-tailnet")
		if err := o.SyncRoutes(); err != nil {
			o.logger.Warn("failed to sync routes after tailnet SSO", "error", err)
		}
	}

	duration := time.Since(start)
	o.logger.Info("convergence pass complete", "apps", len(apps), "duration", duration.String())
}

// convergeUninstalls tears down every app the store marks "uninstalling":
// containers and graph nodes first, then the store row, then the entry in the
// map the rest of the pass reads, so a removed app is never re-added as a
// target. Each step logs and continues: a half-removed app is repaired on the
// next pass rather than abandoning the rest of the list.
func (o *Orchestrator) convergeUninstalls(ctx context.Context, apps []*store.InstalledApp, appMap map[string]*store.InstalledApp, pendingClearData map[string]bool) {
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
}

// convergeTailnet ensures tailnet nodes, gateway, proxies, and the proxy
// outpost match the tailnet store state: an active connection brings a node up
// for every running app, and no connection takes the whole tailnet surface
// down.
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
		o.ensureTailnetNodes(ctx, conn, apps)
		return
	}
	o.purgeTailnet(ctx, apps)
}

// ensureTailnetNodes brings a tailnet node up for each running non-system app
// and records which connection it now rides.
func (o *Orchestrator) ensureTailnetNodes(ctx context.Context, conn *store.TailnetConnection, apps []*store.InstalledApp) {
	o.logger.Info("tailnet active, ensuring nodes for running apps", "conn_id", conn.ID, "app_count", len(apps))
	if o.tailnetNode == nil {
		return
	}
	for _, app := range apps {
		if app.IsSystem || app.Status != "running" {
			continue
		}
		o.logger.Info("ensuring tailnet node", "app", app.CatalogID)
		if err := o.tailnetNode.EnsureRunning(ctx, app.CatalogID); err != nil {
			o.logger.Warn("failed to ensure tailnet node", "app", app.CatalogID, "error", err)
			continue
		}
		_ = o.appStore.SetTailnetID(app.CatalogID, conn.ID)
	}
}

// purgeTailnet takes the whole tailnet surface down: the per-app nodes, the
// SOCKS gateway, the proxy outpost, and the remote-proxy pool. Each piece logs
// and carries on, so one stuck container does not leave the others up.
func (o *Orchestrator) purgeTailnet(ctx context.Context, apps []*store.InstalledApp) {
	o.logger.Info("no active tailnet, purging nodes and gateway", "app_count", len(apps))
	if o.tailnetNode != nil {
		for _, app := range apps {
			if app.IsSystem {
				continue
			}
			o.logger.Info("purging tailnet node", "app", app.CatalogID)
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
	if o.remoteProxy != nil {
		o.remoteProxy.StopAll()
	}
}

// provisionTailnetSSO ensures a forward_domain Authentik proxy provider and standalone
// outpost exist for the tailnet MagicDNS domain. Best-effort: logs warnings on failure.
func (o *Orchestrator) provisionTailnetSSO(ctx context.Context) bool {
	if o.gateway == nil || o.forwardDomainSSO == nil {
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

	domain, err := o.gateway.GetTailnetDomain(ctx)
	if err != nil {
		o.logger.Warn("failed to discover tailnet domain (gateway not ready?)", "error", err)
		return false
	}

	token, err := o.forwardDomainSSO.EnsureForwardDomainAuth(ctx, domain)
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

// populateGraphNodes creates graph nodes and edges for all installed apps.
// Apps with multiple containers (containers: list in metadata) are expanded into
// one node per container. Single-container apps retain a single node per app.
// Within-app dependsOn edges are wired from each container's DependsOn list.
// Inter-app edges connect from each app's primary container to the provider's
// primary container.
func (o *Orchestrator) populateGraphNodes(appMap map[string]*store.InstalledApp) {
	o.createGraphNodes(appMap)
	o.wireInAppEdges(appMap)
	o.wireInterAppEdges(appMap)
	o.setGraphTargets(appMap)
}

// containerDefsFor returns the catalog container defs for an installed app,
// and whether the app declares any containers (vs. legacy single-node).
func (o *Orchestrator) containerDefsFor(appName string) ([]catalog.ContainerDef, bool) {
	if o.catalog == nil {
		return nil, false
	}
	catalogApp, err := o.catalog.Get(appName)
	if err != nil || catalogApp == nil || len(catalogApp.Containers) == 0 {
		return nil, false
	}
	return catalogApp.ContainerDefs(), true
}

// Pass 1: create one node per container def for multi-container apps, and
// one with the catalog ID for legacy/no-container apps.
func (o *Orchestrator) createGraphNodes(appMap map[string]*store.InstalledApp) {
	for appName := range appMap {
		defs, hasCatalogContainers := o.containerDefsFor(appName)
		if !hasCatalogContainers {
			if existing, _ := o.graph.GetNode(appName); existing == nil {
				_ = o.graph.AddNode(appName)
			}
			continue
		}
		for _, def := range defs {
			if existing, _ := o.graph.GetNode(def.Name); existing == nil {
				_ = o.graph.AddNode(def.Name)
			}
			o.registerContainerOwner(def.Name, appName)
		}
	}
}

// Pass 2: wire within-app dependsOn edges for multi-container apps.
func (o *Orchestrator) wireInAppEdges(appMap map[string]*store.InstalledApp) {
	for appName := range appMap {
		defs, ok := o.containerDefsFor(appName)
		if !ok {
			continue
		}
		for _, def := range defs {
			for _, dep := range def.DependsOn {
				_ = o.graph.AddEdge(def.Name, dep)
			}
		}
	}
}

// Pass 3: wire inter-app dependency edges through each app's primary node.
func (o *Orchestrator) wireInterAppEdges(appMap map[string]*store.InstalledApp) {
	appDeps := computeAppDeps(appMap, o.catalog)
	for appName, deps := range appDeps {
		fromNode := o.primaryContainerNode(appName)
		for _, dep := range deps {
			toNode := o.primaryContainerNode(dep)
			_ = o.graph.AddEdge(fromNode, toNode)
		}
	}
}

// Pass 4: set every created node's target to RUNNING.
func (o *Orchestrator) setGraphTargets(appMap map[string]*store.InstalledApp) {
	for appName := range appMap {
		defs, ok := o.containerDefsFor(appName)
		if !ok {
			_ = o.graph.SetTargetStatus(appName, graph.StatusRunning)
			continue
		}
		for _, def := range defs {
			_ = o.graph.SetTargetStatus(def.Name, graph.StatusRunning)
		}
	}
}

// primaryContainerNode returns the graph node ID that represents the "entry point"
// for an app in inter-app dependency edges. For multi-container apps, returns the
// last container's name (the main service container). For single-container apps,
// returns the catalog ID itself.
func (o *Orchestrator) primaryContainerNode(appName string) string {
	if o.catalog == nil {
		return appName
	}
	catalogApp, err := o.catalog.Get(appName)
	if err != nil || catalogApp == nil {
		return appName
	}
	if len(catalogApp.Containers) == 0 {
		return appName
	}
	defs := catalogApp.ContainerDefs()
	return defs[len(defs)-1].Name
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
				// An instance provider has no container and therefore no
				// node to order. Filtering on the source rather than relying
				// on `apps[""]` missing keeps the invariant explicit: the
				// graph never gains a phantom node for a setting.
				if compatible.Source == catalog.InstanceProviderSource {
					continue
				}
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
	case ClearAppDataIntent:
		return "ClearAppData"
	case SetPublicURLIntent:
		return "SetPublicURL"
	case SetInferenceIntent:
		return "SetInference"
	case ReconcileIntent:
		return "Reconcile"
	default:
		return "Unknown"
	}
}
