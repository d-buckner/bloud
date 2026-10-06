// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"sync"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// revokeTracker holds the pending session-revoke requests between the drain
// phase that records them and the convergence pass that carries them out.
//
// The split is not ceremony. The drain phase is allowed to mutate stores; it is
// not allowed to exec inside a container. The convergence pass is where side
// effects happen, and it runs after the drain. So a revoke is recorded here and
// consumed there, which keeps invariant 1 intact: the API submits an intent,
// the orchestrator performs the side effect.
type revokeTracker struct {
	mu      sync.Mutex
	pending map[string]bool
}

func newRevokeTracker() *revokeTracker {
	return &revokeTracker{pending: make(map[string]bool)}
}

func (t *revokeTracker) request(appName string) {
	if t == nil || appName == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending[appName] = true
}

// take consumes the request for an app, returning true exactly once per
// request. A revoke that fired on every pass would log every user out on every
// resync, which is a different product than the one being built.
func (t *revokeTracker) take(appName string) bool {
	if t == nil || appName == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.pending[appName] {
		return false
	}
	delete(t.pending, appName)
	return true
}

// applyRevokeClientSessionsIntent records the request. The work happens in
// handlePendingSessionRevoke during convergence.
func (o *Orchestrator) applyRevokeClientSessionsIntent(intent RevokeClientSessionsIntent) {
	if intent.AppName == "" {
		return
	}
	if o.sessionRevokes == nil {
		o.sessionRevokes = newRevokeTracker()
	}
	o.sessionRevokes.request(intent.AppName)
	o.logger.Info("session revoke requested", "app", intent.AppName)
}

// handlePendingSessionRevoke carries out a recorded revoke for one node.
//
// It runs before PreStart on the resync path and forces a recreate when the
// revocation succeeded. The order matters: clearing the store and leaving the
// container running would do nothing at all, because the app loaded its sessions
// into memory at import time and never re-reads the file while it runs. Only the
// recreate makes an emptied store the store the app is actually using.
//
// A configurator that does not implement SessionRevoker has nothing to revoke
// through the host, and the request is dropped with a log rather than failing
// the pass. The operator asked for something this app cannot do; that is worth
// saying out loud, and it is not a reason to disturb the node.
func (o *Orchestrator) handlePendingSessionRevoke(ctx context.Context, id string, cfg configurator.NodeLifecycle) (bool, string) {
	appID := o.ownerApp(id)
	if !o.sessionRevokes.take(appID) {
		return false, ""
	}

	revoker, ok := cfg.(configurator.SessionRevoker)
	if !ok {
		o.logger.Warn("session revoke requested for an app that cannot revoke through the host",
			"app", id, "owner", appID)
		return false, ""
	}

	state := o.buildAppState(id)
	pctx, cancel := o.appPhaseCtx(ctx)
	defer cancel()

	if err := revoker.RevokeSessions(pctx, state); err != nil {
		// The request stays consumed. A revoke that silently retried on every
		// pass would hammer a container whose exec path is broken, and the
		// operator would get no signal that it never worked.
		o.logger.Error("session revoke failed", "app", id, "error", err)
		return false, ""
	}

	o.logger.Warn("sessions revoked, recreating so the app drops its in-memory copy",
		"app", id, "owner", appID)
	return true, "client sessions revoked"
}
