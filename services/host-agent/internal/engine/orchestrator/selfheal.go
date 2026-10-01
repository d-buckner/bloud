// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

// Self-healing: the periodic convergence pass.
//
// Until this existed the loop woke only on submitted intents, so an app that
// landed in `error` stayed there until a human retried it and container drift
// was repaired only when something else happened to trigger a pass. The pass
// itself was already built (converge is idempotent); what was missing was a
// trigger that fires when nothing else does.
//
// The trigger is a ReconcileIntent submitted into the same queue every other
// trigger uses, so there is still exactly one serialized path into the engine
// (invariant 1). A second goroutine calling converge directly would have
// been a second writer.

import (
	"context"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// DefaultSelfHealInterval is the gap between self-healing passes when the
// deployment does not name one. It is a floor, not a cadence: the timer is
// idle-based, so any convergence resets it (see startSelfHealing).
//
// 60s is the compromise between how fast a home server should recover from a
// container that died at 3am and how much steady-state work the pass costs
// when there is nothing to heal. Tune it with BLOUD_RECONCILE_INTERVAL.
const DefaultSelfHealInterval = 60 * time.Second

// startSelfHealing submits one ReconcileIntent per idle interval until ctx
// is cancelled. It is armed by Start after the first convergence pass and
// must run in its own goroutine.
//
// The timer is idle-based rather than a plain ticker on purpose. A ticker
// fires on a fixed schedule no matter what else happened, so a user install at
// t+59.9s and a tick at t+60s buy two passes 100ms apart. Here every
// completed pass resets the timer, which makes the interval a floor on the
// gap between passes: at most one convergence per interval, and never one
// stacked directly behind another.
//
// A pass that outlives the interval is not a problem either: after
// submitting, the loop waits for that pass to finish before re-arming, so a
// slow convergence cannot turn into a tight submit cycle.
func (o *Orchestrator) startSelfHealing(ctx context.Context) {
	interval := o.config.SelfHealInterval
	if interval <= 0 {
		o.logger.Info("self-healing pass disabled", "interval", interval.String())
		return
	}
	o.logger.Info("self-healing pass armed", "interval", interval.String())

	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-o.healWake:
			// Some other trigger just converged. Push our own pass out a
			// full interval instead of adding one right behind it.
			rearmTimer(timer, interval)

		case <-timer.C:
			o.logger.Info("self-healing interval elapsed, requesting a convergence pass")
			// Enqueue, not Submit: Submit's extra step is pre-recording the
			// install row a user asked for. Nothing was asked for here.
			o.Enqueue(NewReconcileIntent())
			// Hold until that pass reports back, so the next interval is
			// measured from the end of a pass rather than its start.
			select {
			case <-ctx.Done():
				return
			case <-o.healWake:
			}
			rearmTimer(timer, interval)
		}
	}
}

// rearmTimer restarts an idle timer, draining the value a fired timer may
// have left behind so a stale tick cannot skip straight past the next wait.
func rearmTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// signalConverged tells the self-healing timer a convergence pass just
// finished. Non-blocking: the channel is a one-slot mailbox, so a burst of
// passes collapses into one pending wake rather than growing a queue of
// stale notifications.
func (o *Orchestrator) signalConverged() {
	select {
	case o.healWake <- struct{}{}:
	default:
	}
}

// retryErroredNodes gives the `retryable` flag on an operation row its only
// driver. Every failure the drive path records is marked retryable, and until
// now nothing read that back: the row promised a retry was expected to help
// and no retry ever came.
//
// ERROR stays terminal for every other trigger. collectWorkForLevel skips
// ERROR nodes, and the only resets are the ones an operator asks for: an
// install intent (resetErroredNodes) and this pass. That keeps the
// documented rule intact for normal convergence while making the system
// recover on its own once the underlying cause is gone, which is the case
// that has no other answer: nothing else is going to notice.
//
// Nodes owned by an app that is being uninstalled are skipped: re-driving one
// would fight the removal that is already queued in the same pass.
func (o *Orchestrator) retryErroredNodes() {
	if o.graph == nil || o.config.Operations == nil {
		return
	}
	nodes, err := o.graph.Nodes()
	if err != nil {
		o.logger.Warn("self-heal: failed to read graph nodes", "error", err)
		return
	}

	var retried []string
	owners := o.nodeOwners()
	for _, node := range nodes {
		if node.ActualStatus != graph.StatusError {
			continue
		}
		owner, ok := owners[node.ID]
		if !ok {
			owner = o.ownerApp(node.ID)
		}
		if o.isUninstalling(owner) {
			o.logger.Info("self-heal: skipping errored node of an uninstalling app",
				"node", node.ID, "app", owner)
			continue
		}
		op, err := o.config.Operations.Get(owner)
		if err != nil {
			o.logger.Warn("self-heal: failed to read operation row", "app", owner, "error", err)
			continue
		}
		if op == nil || op.Status != store.OpStatusFailed {
			continue
		}
		if !op.Retryable {
			o.logger.Info("self-heal: failure is not retryable, leaving it terminal",
				"node", node.ID, "app", owner, "cause", op.Cause)
			continue
		}
		o.logger.Info("self-heal: retrying a retryable failure",
			"node", node.ID, "app", owner, "phase", op.Phase, "cause", op.Cause)
		if err := o.graph.SetActualStatus(node.ID, graph.StatusInitializing, ""); err != nil {
			o.logger.Warn("self-heal: failed to reset errored node", "node", node.ID, "error", err)
			continue
		}
		retried = append(retried, node.ID)
	}

	if len(retried) > 0 {
		o.logger.Info("self-heal: retryable failures queued for this pass", "count", len(retried), "nodes", retried)
	}
}

// nodeOwners maps every node to the app whose operation row records its
// failures. The orchestrator's own containerOwner map cannot be used here:
// it is filled in as a pass populates the graph, and the drain that runs
// this retry happens before that step, so on the first pass after a restart
// the map is still empty and every container node would be looked up under
// its own name. This derives the same mapping from the catalog instead.
func (o *Orchestrator) nodeOwners() map[string]string {
	owners := make(map[string]string)
	if o.appStore == nil {
		return owners
	}
	apps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Warn("self-heal: failed to list installed apps for owner resolution", "error", err)
		return owners
	}
	for _, app := range apps {
		defs, hasContainers := o.containerDefsFor(app.CatalogID)
		if !hasContainers {
			owners[app.CatalogID] = app.CatalogID
			continue
		}
		for _, def := range defs {
			owners[def.Name] = app.CatalogID
		}
	}
	return owners
}

// isUninstalling reports whether the app row is on its way out. A missing
// store or a missing row is not an uninstall: there is nothing to fight.
func (o *Orchestrator) isUninstalling(appName string) bool {
	if o.appStore == nil {
		return false
	}
	app, err := o.appStore.GetByCatalogID(appName)
	if err != nil || app == nil {
		return false
	}
	return app.Status == "uninstalling"
}
