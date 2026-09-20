// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// Operation-state recording (docs/plans/operation-state-design.md).
//
// The drive path reports phase boundaries through these helpers; the
// operations row is authoritative for failure context and
// user-intent outcome, never for convergence control. Every helper is
// best-effort: a recorder failure logs and returns; it must never
// change lifecycle behavior.

// newOperationID returns a fresh operation identifier.
func newOperationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is catastrophic-level; degrade to a
		// distinguishable non-unique id rather than panic.
		return "op-unavailable"
	}
	return hex.EncodeToString(b[:])
}

// opCause names the acting container node in the failure cause when
// the operation row is keyed by the owning app but the failure
// happened on one of its container nodes (design §Q2: one row per
// app, container named in the cause).
func opCause(nodeID, owner string, err error) error {
	if err == nil {
		return nil
	}
	if nodeID == owner {
		return err
	}
	return fmt.Errorf("%s: %w", nodeID, err)
}

// recordOpStart begins a new operation row for the owning app.
func (o *Orchestrator) recordOpStart(appName, opType, phase string) {
	if o.config.Operations == nil {
		return
	}
	if err := o.config.Operations.Start(appName, newOperationID(), opType, phase); err != nil {
		o.logger.Warn("operation recorder: start failed", "app", appName, "type", opType, "error", err)
	}
}

// ensureOpDrive makes sure the app has a running operation row for the
// current drive. A Submit-created row (install/uninstall) that is still
// running is continued as-is; otherwise a reconcile drive row is
// started. The Get->Start window can race a concurrent Submit by one
// phase update; the row is diagnostic, convergence is idempotent, so
// the mixing is harmless.
func (o *Orchestrator) ensureOpDrive(appName string) {
	if o.config.Operations == nil {
		return
	}
	op, err := o.config.Operations.Get(appName)
	if err != nil {
		o.logger.Warn("operation recorder: read failed", "app", appName, "error", err)
		return
	}
	if op != nil && op.Status == store.OpStatusRunning {
		return
	}
	o.recordOpStart(appName, store.OpTypeReconcile, store.OpPhaseTopology)
}

// recordOpPhase advances the running operation row into a new phase.
// Only running rows move; terminal rows are never resurrected.
func (o *Orchestrator) recordOpPhase(appName, phase string) {
	if o.config.Operations == nil {
		return
	}
	if err := o.config.Operations.AdvancePhase(appName, phase); err != nil {
		o.logger.Warn("operation recorder: phase failed", "app", appName, "phase", phase, "error", err)
	}
}

// recordOpFail marks the running operation failed at the named phase.
func (o *Orchestrator) recordOpFail(appName, phase string, cause error, retryable bool) {
	if o.config.Operations == nil || cause == nil {
		return
	}
	if err := o.config.Operations.Fail(appName, phase, cause.Error(), retryable); err != nil {
		o.logger.Warn("operation recorder: fail failed", "app", appName, "phase", phase, "error", err)
	}
}

// recordOpComplete marks the running operation complete. Called where
// the app reaches its converged state; only touches running rows.
func (o *Orchestrator) recordOpComplete(appName string) {
	if o.config.Operations == nil {
		return
	}
	if err := o.config.Operations.Complete(appName); err != nil {
		o.logger.Warn("operation recorder: complete failed", "app", appName, "error", err)
	}
}

// healOp resolves a stale failed-reconcile row when a staleness
// re-run succeeds without having started a new drive: the previous
// failure is no longer the app's last word.
func (o *Orchestrator) healOp(appName string) {
	if o.config.Operations == nil {
		return
	}
	if err := o.config.Operations.ResolveFailed(appName); err != nil {
		o.logger.Warn("operation recorder: heal failed", "app", appName, "error", err)
	}
}
