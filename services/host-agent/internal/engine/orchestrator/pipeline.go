// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"reflect"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
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
	if existing, _ := o.appStore.GetByCatalogID(appName); existing != nil && existing.Status == store.AppStatusRunning {
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

	o.logger.Info("install plan resolved", "app", appName, "required", len(plan.RequiredProviders), "wired", len(plan.AutoConfig))

	// Record required providers first: installing the consumer installs them
	// with it, and the graph must order them before the consumer. The wired
	// (already installed) providers need no record.
	for _, provider := range plan.RequiredProviders {
		o.logger.Info("recording required provider", "app", appName, "provider", provider.Source)
		if err := o.recordIntent(provider.Source, nil); err != nil {
			o.logger.Error("failed to record required provider", "app", appName, "provider", provider.Source, "error", err)
			return
		}
	}

	// Record the target app. It carries no recorded integration config: the
	// wiring is the declared compatible set, not a value chosen at install.
	if err := o.recordIntent(appName, nil); err != nil {
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
	if err := o.appStore.UpdateStatus(intent.AppName, store.AppStatusUninstalling); err != nil {
		o.logger.Error("failed to mark app as uninstalling", "app", intent.AppName, "error", err)
		return
	}
	o.recordOpStart(intent.AppName, store.OpTypeUninstall, store.OpPhaseTopology)
	pendingClearData[intent.AppName] = intent.ClearData
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
	if existing != nil && existing.Status == store.AppStatusRunning {
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
	o.reconcileCatalogUpdates(ctx, appMap)
	o.populateGraphNodes(appMap)

	// Step 4: Update catalog graph with current installed list.
	o.logger.Info("convergence step", "step", "update-graph")
	o.recordActivity("converge_step", "update-graph")
	if o.catalogGraph != nil {
		installed, _ := o.appStore.GetInstalledCatalogIDs()
		o.catalogGraph.SetInstalled(installed)
	}

	// Step 5: Run reconcile pass, which drives per-app lifecycle phases and regenerates routes.
	o.logger.Info("convergence step", "step", "reconcile")
	o.recordActivity("converge_step", "reconcile")
	if err := o.Reconcile(ctx); err != nil {
		o.logger.Warn("reconcile failed", "error", err)
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
	removedAny := false
	for _, app := range apps {
		if app.Status != store.AppStatusUninstalling {
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
		removedAny = true
	}
	// Routes are a function of the installed set, not of any configurator's
	// PostStart. Regenerate them here, the moment the set is final, so a
	// removed app stops being routed before Reconcile's resync work runs later
	// this pass. Reconcile's own SyncRoutes is then a no-op: the bytes already
	// match.
	if removedAny {
		if err := o.SyncRoutes(); err != nil {
			o.logger.Warn("failed to sync routes after uninstall", "error", err)
		}
	}
}

// intentTypeName returns a human-readable name for an intent type, derived
// from the type's own name rather than a parallel switch. The sealed Intent
// set is enumerated exactly once (in applyIntents); deriving the log name here
// means a new intent type can no longer drift from its drain arm: the name
// follows the type for free, and every concrete type ends in "Intent" by the
// convention in intent.go.
func intentTypeName(intent Intent) string {
	if intent == nil {
		return "Unknown"
	}
	name := reflect.TypeOf(intent).Name()
	if name == "" {
		return "Unknown"
	}
	return strings.TrimSuffix(name, "Intent")
}
