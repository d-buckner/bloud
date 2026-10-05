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
