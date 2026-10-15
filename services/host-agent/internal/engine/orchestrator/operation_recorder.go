// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// Operation-state recording (docs/plans/archive/operation-state-design.md).
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

// opCause names the acting container node in the failure cause when the
// failure happened on a container node rather than on the app's own row.
// The node is still named in the cause after the per-node re-key, because
// the app-level projection shows one node's row at a time and a reader of
// the app needs to know which container it came from.
func opCause(nodeID, owner string, err error) error {
	if err == nil {
		return nil
	}
	if nodeID == owner {
		return err
	}
	return fmt.Errorf("%s: %w", nodeID, err)
}

// recordOpStart begins a new operation row for one node of an app.
func (o *Orchestrator) recordOpStart(appName, node, opType, phase string) {
	if o.config.Stores.Operations == nil {
		return
	}
	if err := o.config.Stores.Operations.Start(appName, node, newOperationID(), opType, phase); err != nil {
		o.logger.Warn("operation recorder: start failed", "app", appName, "node", node, "type", opType, "error", err)
	}
}

// ensureOpDrive makes sure the app has a running operation row for the
// current drive. A Submit-created row (install/uninstall) that is still
// running is continued as-is; otherwise a reconcile drive row is
// started. The Get->Start window can race a concurrent Submit by one
// phase update; the row is diagnostic, convergence is idempotent, so
// the mixing is harmless.
func (o *Orchestrator) ensureOpDrive(appName, node string) {
	if o.config.Stores.Operations == nil {
		return
	}
	op, err := o.config.Stores.Operations.Get(appName)
	if err != nil {
		o.logger.Warn("operation recorder: read failed", "app", appName, "error", err)
		return
	}
	if op != nil && op.Status == store.OpStatusRunning {
		// Another node of this app is already in a drive. Join it, so the
		// app's rows share one drive identity instead of every node
		// inventing its own.
		if err := o.config.Stores.Operations.Ensure(appName, node, op.ID, op.Type, op.Phase); err != nil {
			o.logger.Warn("operation recorder: ensure failed", "app", appName, "node", node, "error", err)
		}
		return
	}
	o.recordOpStart(appName, node, store.OpTypeReconcile, store.OpPhaseTopology)
}

// recordOpPhase advances the running operation row into a new phase.
// Only running rows move; terminal rows are never resurrected.
func (o *Orchestrator) recordOpPhase(appName, node, phase string) {
	if o.config.Stores.Operations == nil {
		return
	}
	if err := o.config.Stores.Operations.AdvancePhase(appName, node, phase); err != nil {
		o.logger.Warn("operation recorder: phase failed", "app", appName, "node", node, "phase", phase, "error", err)
	}
}

// recordOpFail marks the running operation failed at the named phase. Every
// failure the orchestrator records is retryable: the self-healing timer drives
// the retry, so a failure row is the signal to run again rather than a
// terminal verdict.
func (o *Orchestrator) recordOpFail(appName, node, phase string, cause error) {
	if o.config.Stores.Operations == nil || cause == nil {
		return
	}
	if err := o.config.Stores.Operations.Fail(appName, node, phase, cause.Error(), true); err != nil {
		o.logger.Warn("operation recorder: fail failed", "app", appName, "node", node, "phase", phase, "error", err)
	}
}

// recordOpComplete marks the running operation complete. Called where
// the app reaches its converged state; only touches running rows.
func (o *Orchestrator) recordOpComplete(appName, node string) {
	if o.config.Stores.Operations == nil {
		return
	}
	if err := o.config.Stores.Operations.Complete(appName, node); err != nil {
		o.logger.Warn("operation recorder: complete failed", "app", appName, "node", node, "error", err)
	}
}

// healOp resolves a stale failed-reconcile row when a staleness
// re-run succeeds without having started a new drive: the previous
// failure is no longer the app's last word.
func (o *Orchestrator) healOp(appName, node string) {
	if o.config.Stores.Operations == nil {
		return
	}
	if err := o.config.Stores.Operations.ResolveFailed(appName, node); err != nil {
		o.logger.Warn("operation recorder: heal failed", "app", appName, "node", node, "error", err)
	}
}
