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
//  2. Nodes already at RUNNING whose dependency appeared in changedIDs (staleness).
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
// converged; a node already at its target is queued only when a dependency
// just changed, which is the staleness re-run of PostStart.
func (o *Orchestrator) collectNodeWork(id string, node *graph.Node, changedIDs map[string]bool) (bool, error) {
	if node.TargetStatus == node.ActualStatus {
		if node.ActualStatus != graph.StatusRunning {
			return false, nil
		}
		return o.isStaleAtTarget(id, changedIDs)
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

// isStaleAtTarget reports whether a node already at its target needs its
// PostStart re-run because a direct dependency completed this pass.
func (o *Orchestrator) isStaleAtTarget(id string, changedIDs map[string]bool) (bool, error) {
	deps, err := o.graph.GetDependencies(id)
	if err != nil {
		return false, fmt.Errorf("get dependencies for %q: %w", id, err)
	}
	for _, dep := range deps {
		if changedIDs[dep] {
			o.logger.Info("queuing stale node for PostStart re-run", "app", id, "changed_dep", dep)
			return true, nil
		}
	}
	return false, nil
}
