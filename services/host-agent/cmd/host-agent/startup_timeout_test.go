// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

// TestStartupGateExceedsNodePostStartBudget pins the coupling that keeps one
// bad app from taking the control plane down.
//
// A node's PostStart runs under DefaultPostStartBudget. The startup gate is
// the wall-clock limit on the whole first convergence pass, and tripping it
// exits the process. If the gate were at or below the per-node budget, a
// single node that spends its entire budget would trip the startup timeout
// and kill the control plane, instead of just parking that one node in
// ERROR. The gate has to sit above the budget with room for the other levels
// that converge in the same window.
func TestStartupGateExceedsNodePostStartBudget(t *testing.T) {
	if systemConvergenceTimeout <= orchestrator.DefaultPostStartBudget {
		t.Fatalf(
			"startup gate %s must exceed the per-node PostStart budget %s: one node spending its full budget would trip the startup timeout and exit the control plane",
			systemConvergenceTimeout, orchestrator.DefaultPostStartBudget)
	}
	if systemConvergenceTimeout <= appclient.MaxWaitBudget {
		t.Fatalf(
			"startup gate %s must exceed appclient.MaxWaitBudget %s, which is what the per-node budget is defined as",
			systemConvergenceTimeout, appclient.MaxWaitBudget)
	}
}
