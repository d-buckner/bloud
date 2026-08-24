// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"context"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
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
		case SetHostsIntent:
			o.applySetHostsIntent(i)
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
	// nodes on its own — without this reset, retrying a failed/degraded app
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

// applySetHostsIntent persists the new host set, swaps the runtime URL state,
// and resets SSO-dependent nodes so the convergence pass that follows this
// drain re-runs their full lifecycle: PreStart rewrites app configs with the
// new URLs (recreating changed containers), ensureSSO re-provisions the
// Authentik providers with the new redirect URIs, and PostStart re-applies
// outpost/launch configuration.
func (o *Orchestrator) applySetHostsIntent(intent SetHostsIntent) {
	hosts := make([]string, 0, len(intent.Hosts))
	seen := map[string]bool{}
	for _, h := range intent.Hosts {
		h = hostset.Normalize(h)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		hosts = append(hosts, h)
	}
	primary := hostset.Normalize(intent.Primary)
	if primary == "" || !seen[primary] {
		primary = hostset.DefaultPrimary
		if !seen[primary] {
			hosts = append(hosts, primary)
		}
	}
	if len(hosts) == 0 {
		return
	}

	hs := hostset.New(hosts, primary)

	// No-op guard: skip all side effects when nothing actually changed.
	if o.hosts != nil && hostSetsEqual(o.hosts.Get(), hs) {
		o.logger.Info("host set unchanged, skipping SetHosts side effects", "hosts", hs.Hosts())
		return
	}

	o.logger.Info("applying host set", "hosts", hs.Hosts(), "primary", hs.Primary())

	// 1. Persist custom hosts (built-ins are implicit, never stored).
	if o.hostStore != nil {
		storedPrimary := ""
		if !hostset.BuiltinSet()[hs.Primary()] {
			storedPrimary = hs.Primary()
		}
		if err := o.hostStore.Replace(hosts, storedPrimary); err != nil {
			o.logger.Error("failed to persist host set", "error", err)
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

// hostSetsEqual reports whether two host sets contain the same hosts with the
// same primary, ignoring order.
func hostSetsEqual(a, b hostset.HostSet) bool {
	if a.Primary() != b.Primary() {
		return false
	}
	if len(a.Hosts()) != len(b.Hosts()) {
		return false
	}
	set := map[string]bool{}
	for _, h := range a.Hosts() {
		set[h] = true
	}
	for _, h := range b.Hosts() {
		if !set[h] {
			return false
		}
	}
	return true
}

// resetSSONodes resets every RUNNING node that depends on the host set back to
// INITIALIZING: all containers of installed native-oidc / forward-auth apps
// (their configs and SSO providers bake in host URLs) plus the Authentik
// server container (its PostStart sets the embedded outpost's browser URL).
func (o *Orchestrator) resetSSONodes() {
	if o.graph == nil || o.appStore == nil {
		return
	}

	reset := func(nodeID string) {
		node, err := o.graph.GetNode(nodeID)
		if err != nil || node == nil || node.ActualStatus != graph.StatusRunning {
			return
		}
		o.logger.Info("resetting SSO-dependent node for host change", "node", nodeID)
		_ = o.graph.SetActualStatus(nodeID, graph.StatusInitializing, "")
	}

	// Authentik server: PostStart re-applies the embedded outpost host URL.
	if _, err := o.appStore.GetByCatalogID("authentik"); err == nil {
		reset("apps-authentik-server")
	}

	apps, err := o.appStore.GetAll()
	if err != nil {
		return
	}
	for _, app := range apps {
		if app.IsSystem || o.catalog == nil {
			continue
		}
		catalogApp, err := o.catalog.Get(app.CatalogID)
		if err != nil || catalogApp == nil {
			continue
		}
		switch catalogApp.SSO.Strategy {
		case "native-oidc", "forward-auth":
		default:
			continue
		}
		if len(catalogApp.Containers) > 0 {
			for _, def := range catalogApp.Containers {
				reset(def.Name)
			}
			continue
		}
		reset(app.CatalogID)
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

	// Step 6: Run reconcile pass — drives per-app lifecycle phases and regenerates routes.
	o.logger.Info("convergence step", "step", "reconcile")
	o.recordActivity("converge_step", "reconcile")
	if err := o.Reconcile(ctx); err != nil {
		o.logger.Warn("reconcile failed", "error", err)
	}

	// Step 7: Provision forward_domain SSO for tailnet access (best-effort).
	if o.provisionTailnetSSO(ctx) {
		o.logger.Info("convergence step", "step", "regenerate-routes-tailnet")
		if err := o.RegenerateRoutes(); err != nil {
			o.logger.Warn("failed to regenerate routes after tailnet SSO", "error", err)
		}
	}

	duration := time.Since(start)
	o.logger.Info("convergence pass complete", "apps", len(apps), "duration", duration.String())
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
		return
	}

	// No tailnet: purge tailnet nodes, gateway, proxy outpost, and proxies.
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

// populateGraphNodes creates graph nodes and edges for all installed apps.
// Apps with multiple containers (containers: list in metadata) are expanded into
// one node per container. Single-container apps retain a single node per app.
// Within-app dependsOn edges are wired from each container's DependsOn list.
// Inter-app edges connect from each app's primary container to the provider's
// primary container.
func (o *Orchestrator) populateGraphNodes(appMap map[string]*store.InstalledApp) {
	// Pass 1: create nodes.
	for appName := range appMap {
		var defs []catalog.ContainerDef
		var hasCatalogContainers bool
		if o.catalog != nil {
			if catalogApp, err := o.catalog.Get(appName); err == nil && catalogApp != nil {
				if len(catalogApp.Containers) > 0 {
					defs = catalogApp.ContainerDefs()
					hasCatalogContainers = true
				}
			}
		}

		if !hasCatalogContainers {
			// Legacy or no-container app: one node with the catalog ID.
			if existing, _ := o.graph.GetNode(appName); existing == nil {
				_ = o.graph.AddNode(appName)
			}
			continue
		}

		// Multi-container app: create one node per container def.
		for _, def := range defs {
			if existing, _ := o.graph.GetNode(def.Name); existing == nil {
				_ = o.graph.AddNode(def.Name)
			}
			o.registerContainerOwner(def.Name, appName)
		}
	}

	// Pass 2: wire within-app dependsOn edges for multi-container apps.
	for appName := range appMap {
		if o.catalog == nil {
			continue
		}
		catalogApp, err := o.catalog.Get(appName)
		if err != nil || catalogApp == nil {
			continue
		}
		if len(catalogApp.Containers) == 0 {
			continue
		}
		defs := catalogApp.ContainerDefs()
		for _, def := range defs {
			for _, dep := range def.DependsOn {
				_ = o.graph.AddEdge(def.Name, dep)
			}
		}
	}

	// Pass 3: wire inter-app dependency edges.
	appDeps := computeAppDeps(appMap, o.catalog)
	for appName, deps := range appDeps {
		fromNode := o.primaryContainerNode(appName)
		for _, dep := range deps {
			toNode := o.primaryContainerNode(dep)
			_ = o.graph.AddEdge(fromNode, toNode)
		}
	}

	// Pass 4: set all targets to RUNNING.
	for appName := range appMap {
		if o.catalog != nil {
			if catalogApp, err := o.catalog.Get(appName); err == nil && catalogApp != nil {
				if defs := catalogApp.ContainerDefs(); len(defs) > 0 {
					for _, def := range defs {
						_ = o.graph.SetTargetStatus(def.Name, graph.StatusRunning)
					}
					continue
				}
			}
		}
		_ = o.graph.SetTargetStatus(appName, graph.StatusRunning)
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
	case SetHostsIntent:
		return "SetHosts"
	default:
		return "Unknown"
	}
}
