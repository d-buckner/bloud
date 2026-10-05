// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
)

// processLevel runs all work items for one topological level concurrently,
// then updates changedIDs with the IDs of nodes that successfully reached
// their target status.
func (o *Orchestrator) processLevel(ctx context.Context, nodeIDs []string, changedIDs map[string]bool) error {
	work, err := o.collectWorkForLevel(nodeIDs, changedIDs)
	if err != nil {
		return err
	}
	o.logger.Info("level work collected", "nodes", len(nodeIDs), "work", len(work))
	if len(work) == 0 {
		return nil
	}

	type result struct {
		id      string
		success bool
	}
	results := make(chan result, len(work))

	g, gCtx := errgroup.WithContext(ctx)
	for _, id := range work {
		id := id
		g.Go(func() error {
			success := o.runConfigurator(gCtx, id)
			results <- result{id, success}
			return nil // app errors are captured as node status, not propagated
		})
	}
	_ = g.Wait()
	close(results)

	for r := range results {
		if r.success {
			changedIDs[r.id] = true
		}
	}

	return nil
}

// collectWorkForLevel returns the subset of nodeIDs that need action this pass:
//  1. Nodes whose actual status hasn't reached their target (excluding ERROR nodes
//     and nodes whose dependencies have not yet completed: neither RUNNING from a
//     prior pass nor present in changedIDs from this pass).
//  2. Nodes already at RUNNING, for the periodic config resync (see
//     readyForConfigResync).
func (o *Orchestrator) collectWorkForLevel(nodeIDs []string, changedIDs map[string]bool) ([]string, error) {
	var work []string
	for _, id := range nodeIDs {
		node, err := o.graph.GetNode(id)
		if err != nil {
			return nil, fmt.Errorf("get node %q: %w", id, err)
		}
		if node == nil {
			continue
		}

		// ERROR is terminal: never retry without an explicit status reset.
		if node.ActualStatus == graph.StatusError {
			o.logger.Info("skipping node in ERROR status", "app", id, "error", node.Error)
			continue
		}

		queued, err := o.collectNodeWork(id, node, changedIDs)
		if err != nil {
			return nil, err
		}
		if queued {
			work = append(work, id)
		}
	}
	return work, nil
}

// collectNodeWork decides whether one node has work this pass. A node below its
// target is queued unless a dependency is neither RUNNING nor freshly
// converged; a node already at RUNNING is queued for the config resync.
func (o *Orchestrator) collectNodeWork(id string, node *graph.Node, changedIDs map[string]bool) (bool, error) {
	if node.TargetStatus == node.ActualStatus {
		if node.ActualStatus != graph.StatusRunning {
			return false, nil
		}
		return o.readyForConfigResync(id)
	}
	blocker, err := o.blockingDependency(id, changedIDs)
	if err != nil {
		return false, err
	}
	if blocker != "" {
		o.logger.Info("skipping blocked node", "app", id, "actual", node.ActualStatus,
			"target", node.TargetStatus, "blocking_dep", blocker)
		return false, nil
	}
	o.logger.Info("queuing node for lifecycle", "app", id, "actual", node.ActualStatus, "target", node.TargetStatus)
	return true, nil
}

// blockingDependency returns the first dependency that is neither RUNNING from
// a prior pass nor converged earlier in this pass, or "" when every dependency
// is ready. A dependency in ERROR comes back the same way: it blocks regardless.
func (o *Orchestrator) blockingDependency(id string, changedIDs map[string]bool) (string, error) {
	deps, err := o.graph.GetDependencies(id)
	if err != nil {
		return "", fmt.Errorf("get dependencies for %q: %w", id, err)
	}
	for _, dep := range deps {
		depNode, err := o.graph.GetNode(dep)
		if err != nil || depNode == nil {
			continue
		}
		if depNode.ActualStatus != graph.StatusRunning && !changedIDs[dep] {
			return dep, nil
		}
	}
	return "", nil
}

// readyForConfigResync reports whether a node already at RUNNING should get
// its config phases re-run this pass.
//
// RUNNING means the lifecycle phases completed once, not that the app's config
// still matches reality. The config phases are where a configurator diffs its
// own state against the outside world, and the outside world moves on its own
// schedule: a user added in Settings after the install, a credential rotated
// by hand, a provider's address changed, a provider app installed after this
// one. A node that only re-runs its config phases when one of its dependencies
// moves never sees any of those, which is why a Bloud user created after
// AFFiNE was installed never got its shared-workspace invitation and a
// Navidrome account created after Navidrome was never synced.
//
// Every node at RUNNING is therefore queued, on every pass. The one thing that
// stops the resync is a direct dependency sitting in ERROR: re-running a
// config sync against a provider this same pass could not bring up buys a
// guaranteed failure and nothing else, and the pass that heals that dependency
// re-runs this node's config phases anyway, because a node that converges
// lands in changedIDs.
//
// The cost is one PreStart and one PostStart per running app per pass. Every
// configurator's phases are required to be idempotent (invariant 2), and an
// idempotent pair that finds nothing to change is a handful of reads and a
// `managedfile.Write` that reports changed=false, which is what makes this
// affordable on a 60s floor. What it is not affordable as is a configurator
// that reports a change every time: that one restarts its container once a
// minute, which is why the resync breaker caps it.
func (o *Orchestrator) readyForConfigResync(id string) (bool, error) {
	deps, err := o.graph.GetDependencies(id)
	if err != nil {
		return false, fmt.Errorf("get dependencies for %q: %w", id, err)
	}
	for _, dep := range deps {
		depNode, err := o.graph.GetNode(dep)
		if err != nil || depNode == nil {
			continue
		}
		if depNode.ActualStatus == graph.StatusError {
			o.logger.Info("skipping PostStart resync: a dependency is in ERROR",
				"app", id, "errored_dep", dep)
			return false, nil
		}
	}
	return true, nil
}
