// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// reconcileCatalogUpdates applies catalog changes to installed apps on every
// convergence pass. It prunes containers the catalog no longer declares, resets
// nodes whose rendered spec no longer matches the running container, and resets
// the primary node when an app's SSO strategy changed. The diff is against the
// live container set and the stored sso_strategy, never a persisted snapshot.
func (o *Orchestrator) reconcileCatalogUpdates(ctx context.Context, appMap map[string]*store.InstalledApp) {
	if o.config.Runtime.Containers == nil || o.catalog == nil {
		return
	}

	containers, err := o.config.Runtime.Containers.ListContainers(ctx)
	if err != nil {
		o.logger.Warn("catalog update: failed to list containers", "error", err)
		return
	}

	byName := make(map[string]containerruntime.ContainerInfo, len(containers))
	for _, c := range containers {
		if c.Name == "" {
			continue
		}
		byName[c.Name] = c
	}

	for appID := range appMap {
		o.reconcileAppCatalogUpdate(ctx, appID, byName)
	}
}

func (o *Orchestrator) reconcileAppCatalogUpdate(ctx context.Context, appID string, byName map[string]containerruntime.ContainerInfo) {
	catalogApp, err := o.catalog.Get(appID)
	if err != nil || catalogApp == nil {
		// Catalog-missing: surface, don't act. The app stays installed and
		// running; nothing here re-drives or prunes it.
		return
	}
	defs := catalogApp.ContainerDefs()

	// Case B: prune containers this app owns but the catalog no longer declares.
	// A container is only a candidate when it was once a declared lifecycle
	// node: a labeled container that never had a graph node is somebody else's,
	// and pruning it here would be guessing.
	for name, info := range byName {
		if info.Labels[containerruntime.AppLabel] != appID {
			continue
		}
		if hasContainerDef(defs, name) {
			continue
		}
		if node, _ := o.graph.GetNode(name); node == nil {
			continue
		}
		o.pruneContainer(ctx, appID, name)
	}

	// Case A: reset RUNNING nodes whose rendered spec changed.
	for _, def := range defs {
		o.resetSpecChangedNode(appID, def, byName)
	}

	// Case C: reset the primary node when the SSO strategy changed.
	o.resetForSSOChange(appID, catalogApp)
}

func hasContainerDef(defs []catalog.ContainerDef, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

// pruneContainer removes one container the catalog no longer declares, plus its
// graph node. It mirrors removeMultiContainerApp for a subset. The app's data
// directory is left alone: the app still exists and a dropped sidecar may share
// apps/<app>/.
func (o *Orchestrator) pruneContainer(ctx context.Context, appID, name string) {
	o.logger.Info("catalog update: pruning removed container", "app", appID, "container", name)

	if r, ok := o.registry.Get(name).(configurator.Remover); ok {
		state := o.buildAppState(appID)
		if err := r.Remove(ctx, state, false); err != nil {
			o.logger.Warn("catalog update: configurator remove failed", "container", name, "error", err)
		}
	}

	if o.config.Runtime.Containers != nil {
		if err := o.config.Runtime.Containers.Remove(ctx, name); err != nil {
			o.logger.Warn("catalog update: failed to remove container", "container", name, "error", err)
			// Keep the node: it is the retry signal for the next pass.
			return
		}
	}

	if err := o.graph.DeleteNode(name); err != nil {
		o.logger.Warn("catalog update: failed to delete graph node", "container", name, "error", err)
	}
	delete(o.containerOwner, name)
}

// resetSpecChangedNode resets a RUNNING container node whose rendered spec no
// longer matches the revision the running container was created from. The reset
// re-runs the full lifecycle (PreStart, Ensure, PostStart), which is what
// applies the new spec. A missing container, or a node not at RUNNING, is left
// to SyncContainerState's drift repair or the in-flight drive.
func (o *Orchestrator) resetSpecChangedNode(appID string, def catalog.ContainerDef, byName map[string]containerruntime.ContainerInfo) {
	node, err := o.graph.GetNode(def.Name)
	if err != nil || node == nil || node.ActualStatus != graph.StatusRunning {
		return
	}

	spec, err := o.computeContainerSpec(&def, appID)
	if err != nil {
		o.logger.Warn("catalog update: failed to render spec", "app", appID, "container", def.Name, "error", err)
		return
	}
	revision, err := spec.Revision()
	if err != nil {
		o.logger.Warn("catalog update: failed to compute revision", "app", appID, "container", def.Name, "error", err)
		return
	}
	info, ok := byName[def.Name]
	if !ok {
		return
	}
	stored := info.Labels[containerruntime.SpecRevisionLabel]
	// A missing revision means the container was not created by Ensure (or
	// predates the label); there is nothing to compare, so leave it to its
	// next natural recreate.
	if stored == "" || stored == revision {
		return
	}

	o.logger.Info("catalog update: spec changed, re-driving lifecycle",
		"app", appID, "container", def.Name)
	_ = o.graph.SetActualStatus(def.Name, graph.StatusInitializing, "")
}

// resetForSSOChange resets the app's primary node when the stored SSO strategy
// differs from the current catalog. The reset makes the full lifecycle re-run
// ensureSSO (provision the new strategy) and reconcileSSOStrategy (deprovision
// the old one).
func (o *Orchestrator) resetForSSOChange(appID string, catalogApp *catalog.App) {
	if o.appStore == nil {
		return
	}
	stored, err := o.appStore.GetSSOStrategy(appID)
	if err != nil {
		o.logger.Warn("catalog update: failed to read stored sso strategy", "app", appID, "error", err)
		return
	}
	if stored == "" || stored == catalogApp.SSO.Strategy {
		return
	}

	nodeID := o.primaryContainerNode(appID)
	node, err := o.graph.GetNode(nodeID)
	if err != nil || node == nil || node.ActualStatus != graph.StatusRunning {
		return
	}

	o.logger.Info("catalog update: SSO strategy changed, re-driving lifecycle",
		"app", appID, "from", stored, "to", catalogApp.SSO.Strategy)
	_ = o.graph.SetActualStatus(nodeID, graph.StatusInitializing, "")
}

// reconcileSSOStrategy runs in the full lifecycle after ensureSSO has
// provisioned the current strategy. It deprovisions the previous strategy (when
// there was one) and records the new one, so the next pass sees no change.
// Gated on the primary node, the same gate ensureSSO uses, so a multi-container
// app deprovisions exactly once.
//
// Provider names embed the display name, so deprovisioning uses the catalog's
// display name, which is the name the provider was created with: a rename
// through RenameAppIntent changes only the store's name, never the catalog's.
func (o *Orchestrator) reconcileSSOStrategy(ctx context.Context, id string) {
	appID := o.ownerApp(id)
	if o.appStore == nil || o.catalog == nil {
		return
	}
	if o.primaryContainerNode(appID) != id {
		return
	}
	catalogApp, err := o.catalog.Get(appID)
	if err != nil || catalogApp == nil {
		return
	}

	stored, err := o.appStore.GetSSOStrategy(appID)
	if err != nil {
		o.logger.Warn("reconcile sso strategy: read failed", "app", appID, "error", err)
		return
	}
	current := catalogApp.SSO.Strategy
	if stored == current {
		return
	}

	if stored != "" && stored != "none" && o.sso != nil {
		if err := o.sso.Deprovision(ctx, appID, catalogApp.DisplayName, stored); err != nil {
			o.logger.Warn("reconcile sso strategy: deprovision failed",
				"app", appID, "strategy", stored, "error", err)
			return
		}
	}

	if err := o.appStore.SetSSOStrategy(appID, current); err != nil {
		o.logger.Warn("reconcile sso strategy: record failed", "app", appID, "error", err)
	}
}
