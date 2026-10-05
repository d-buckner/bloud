// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

// TestStartupGateExceedsNodePhaseBudget pins the coupling that keeps one bad
// app from taking the control plane down.
//
// Each configurator phase of a node runs under DefaultAppPhaseBudget. The
// startup gate is the wall-clock limit on the whole first convergence pass,
// and tripping it exits the process. If the gate were at or below the per-node
// budget, a single node that spends its entire budget would trip the startup
// timeout and kill the control plane, instead of just parking that one node in
// ERROR. The gate has to sit above the budget with room for the other levels
// that converge in the same window.
func TestStartupGateExceedsNodePhaseBudget(t *testing.T) {
	if systemConvergenceTimeout <= orchestrator.DefaultAppPhaseBudget {
		t.Fatalf(
			"startup gate %s must exceed the per-node phase budget %s: one node spending its full budget would trip the startup timeout and exit the control plane",
			systemConvergenceTimeout, orchestrator.DefaultAppPhaseBudget)
	}
	// The gate is expressed in terms of MaxWaitBudget, and the phase budget is
	// now defined tighter than that (configurator.PhaseBudget). The gate only
	// has to clear the tighter of the two, so this asserts against both: the
	// library ceiling any declared wait is bounded by, and the framework
	// ceiling each phase is bounded by.
	if systemConvergenceTimeout <= appclient.MaxWaitBudget {
		t.Fatalf(
			"startup gate %s must exceed appclient.MaxWaitBudget %s, the ceiling on any single declared wait",
			systemConvergenceTimeout, appclient.MaxWaitBudget)
	}
	if orchestrator.DefaultAppPhaseBudget > appclient.MaxWaitBudget {
		t.Fatalf(
			"phase budget %s must not exceed appclient.MaxWaitBudget %s, or a declared wait could fit the phase but not the library ceiling",
			orchestrator.DefaultAppPhaseBudget, appclient.MaxWaitBudget)
	}
}
