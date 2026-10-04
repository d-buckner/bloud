// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"fmt"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
)

// registerContainerOwner records that containerName belongs to appCatalogID.
// Called during convergence when multi-container nodes are created.
func (o *Orchestrator) registerContainerOwner(containerName, appCatalogID string) {
	o.containerOwner[containerName] = appCatalogID
}

// ownerApp returns the app catalog ID that owns the given node ID.
// For multi-container nodes, returns the owning app's catalog ID.
// For single-container nodes (or unregistered nodes), returns nodeID itself.
func (o *Orchestrator) ownerApp(nodeID string) string {
	if appID, ok := o.containerOwner[nodeID]; ok {
		return appID
	}
	return nodeID
}

// containerDefForNode returns the ContainerDef for a multi-container node,
// along with the owning app's catalog ID. Returns nil, "" for single-container nodes.
func (o *Orchestrator) containerDefForNode(nodeID string) (*catalog.ContainerDef, string) {
	appID := o.ownerApp(nodeID)
	if appID == nodeID {
		return nil, "" // not a registered multi-container node
	}
	if o.catalog == nil {
		return nil, appID
	}
	catalogApp, err := o.catalog.Get(appID)
	if err != nil || catalogApp == nil {
		return nil, appID
	}
	for _, def := range catalogApp.ContainerDefs() {
		if def.Name == nodeID {
			d := def
			return &d, appID
		}
	}
	return nil, appID
}

// convertHealthCheckTest converts Docker-style health check test format to podman exec args.
// CMD-SHELL: ["CMD-SHELL", "cmd"] → ["/bin/sh", "-c", "cmd"]
// CMD:       ["CMD", "exec", "arg1"] → ["exec", "arg1"]
func convertHealthCheckTest(test []string) []string {
	if len(test) == 0 {
		return test
	}
	switch test[0] {
	case "CMD-SHELL":
		if len(test) == 1 {
			return []string{"/bin/sh", "-c", ""}
		}
		return []string{"/bin/sh", "-c", test[1]}
	case "CMD":
		if len(test) <= 1 {
			return test
		}
		return test[1:]
	default:
		return test
	}
}

// runContainerHealthCheck polls the health check command inside the named container
// until it passes or retries are exhausted, respecting context cancellation.
func (o *Orchestrator) runContainerHealthCheck(ctx context.Context, containerName string, hc *catalog.ContainerHealthCheck) error {
	if o.config.Runtime.Containers == nil {
		return nil
	}
	interval := time.Duration(hc.Interval) * time.Second
	if interval == 0 {
		interval = 5 * time.Second
	}
	timeout := time.Duration(hc.Timeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	retries := hc.Retries
	if retries == 0 {
		retries = 3
	}

	for attempt := 0; attempt < retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		}
		execCtx, cancel := context.WithTimeout(ctx, timeout)
		execCmd := convertHealthCheckTest(hc.Test)
		err := o.config.Runtime.Containers.Exec(execCtx, containerName, execCmd)
		cancel()
		if err == nil {
			return nil
		}
		o.logger.Info("container health check attempt failed", "container", containerName, "attempt", attempt+1, "retries", retries, "error", err)
	}
	return fmt.Errorf("container health check failed after %d attempts for %q", retries, containerName)
}
