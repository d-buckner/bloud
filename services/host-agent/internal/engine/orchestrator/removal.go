// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"fmt"
	"os"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/dirs"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// deprovisionAppSSO removes the identity-provider objects Bloud created for
// an app on install: its application and, for the strategies that own one,
// the OAuth2/proxy provider behind it.
//
// The strategy is read from the store rather than the catalog. The catalog
// entry can be refreshed or gone by the time an uninstall converges (a Bloud
// upgrade between install and uninstall is the normal case), while the stored
// strategy is the record of what was actually provisioned. It is the same
// value reconcileSSOStrategy maintains for the strategy-change path.
//
// An app that never provisioned anything (no strategy recorded, "none", or no
// SSO configured at all) is a no-op, so this is safe on every uninstall.
func (o *Orchestrator) deprovisionAppSSO(ctx context.Context, app *store.InstalledApp) error {
	if o.sso == nil || o.appStore == nil {
		return nil
	}
	stored, err := o.appStore.GetSSOStrategy(app.CatalogID)
	if err != nil {
		return fmt.Errorf("reading stored SSO strategy: %w", err)
	}
	if stored == "" || stored == "none" {
		return nil
	}
	// The provider's name embeds the display name the app was provisioned
	// with, so prefer the catalog's, which is the value ensureSSO passed, and
	// fall back to the store's when the catalog entry is gone. This only feeds
	// the name-based fallback in DeleteAppSSO: the primary lookup there is by
	// application slug, which is why a renamed app cannot strand a provider.
	displayName := app.DisplayName
	if o.catalog != nil {
		if cat, cerr := o.catalog.Get(app.CatalogID); cerr == nil && cat != nil {
			displayName = cat.DisplayName
		}
	}
	if err := o.sso.Deprovision(ctx, app.CatalogID, displayName, stored); err != nil {
		return fmt.Errorf("deprovisioning %s SSO: %w", stored, err)
	}
	o.logger.Info("uninstall: deprovisioned app SSO", "app", app.CatalogID, "strategy", stored)
	return nil
}

// RemoveApp calls a configurator's optional Remover.Remove for the named app
// (when one is registered and implements teardown), removes containers, then
// deletes graph node(s).
// For multi-container apps, all container nodes are removed.
// The drive's terminal operation state is recorded here: the whole
// removal is one uninstall phase from the row's point of view.
func (o *Orchestrator) RemoveApp(ctx context.Context, appName string, clearData bool) error {
	// Guarantee a drive row: the Submit-created uninstall row is
	// continued when present; otherwise the removal is recorded as a
	// fresh drive so a failed removal never goes untracked.
	o.ensureOpDrive(appName)
	if err := o.removeApp(ctx, appName, clearData); err != nil {
		o.recordOpFail(appName, store.OpPhaseTopology, err)
		return err
	}
	o.recordOpComplete(appName)
	return nil
}

func (o *Orchestrator) removeApp(ctx context.Context, appName string, clearData bool) error {
	o.logger.Info("removing app", "app", appName, "clear_data", clearData)

	// Multi-container apps: remove each container node individually.
	if o.catalog != nil {
		if catalogApp, err := o.catalog.Get(appName); err == nil && catalogApp != nil {
			if len(catalogApp.Containers) > 0 {
				return o.removeMultiContainerApp(ctx, appName, catalogApp.ContainerDefs(), clearData)
			}
		}
	}

	// Single-container (or system) app. Only a configurator that owns teardown
	// gets a Remove call; container and data removal are the orchestrator's.
	if r, ok := o.registry.Get(appName).(configurator.Remover); ok {
		state := o.buildAppState(appName)
		if err := r.Remove(ctx, state, clearData); err != nil {
			return fmt.Errorf("remove app %q: %w", appName, err)
		}
	}
	return o.graph.DeleteNode(appName)
}

// removeMultiContainerApp removes all container nodes for a multi-container app,
// running per-node configurator Remove() and container runtime Remove() for each.
func (o *Orchestrator) removeMultiContainerApp(ctx context.Context, appName string, defs []catalog.ContainerDef, clearData bool) error {
	for _, def := range defs {
		if r, ok := o.registry.Get(def.Name).(configurator.Remover); ok {
			state := o.buildAppState(def.Name)
			if err := r.Remove(ctx, state, clearData); err != nil {
				o.logger.Warn("configurator remove failed", "container", def.Name, "error", err)
			}
		}
		if o.config.Runtime.Containers != nil {
			if err := o.config.Runtime.Containers.Remove(ctx, def.Name); err != nil {
				o.logger.Warn("failed to remove container", "container", def.Name, "error", err)
			}
		}
		if err := o.graph.DeleteNode(def.Name); err != nil {
			o.logger.Warn("failed to delete graph node", "container", def.Name, "error", err)
		}
		delete(o.containerOwner, def.Name)
	}
	if clearData {
		dataDir := dirs.AppDataDir(o.dataDir, appName)
		if err := o.removeAppData(ctx, dataDir); err != nil {
			o.logger.Warn("failed to remove data directory", "app", appName, "path", dataDir, "error", err)
		}
	}
	return nil
}

// removeAppData deletes an app's data directory now that its containers are
// gone. Containers that write as a non-root user leave files owned, on the
// host, by a mapped uid the host-agent user cannot delete, so os.RemoveAll
// alone fails on them (e.g. the pgvector postgres user). The runtime can
// remove the path as the root of its user namespace; when it can, that is the
// only removal attempted, because it also covers every host-owned file. A
// runtime without the capability falls back to the host-side removal.
//
// This runs after the containers are removed on purpose. Emptying the volumes
// from inside a live container leaves the door open for the app to write again
// while it shuts down (many images rewrite state files on SIGTERM), and those
// bytes are then unreachable to the host user.
func (o *Orchestrator) removeAppData(ctx context.Context, path string) error {
	if o.config.Runtime.Containers != nil {
		if remover, ok := o.config.Runtime.Containers.(containerruntime.PathRemover); ok {
			return remover.RemoveHostPath(ctx, path)
		}
	}
	return os.RemoveAll(path)
}
