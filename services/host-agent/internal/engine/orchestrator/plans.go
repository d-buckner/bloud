// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"errors"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
)

// ErrPlanUnavailable reports that no dependency graph was wired, so no plan
// can be computed at all. It is deliberately distinct from a plan that says
// "no": a caller that conflated the two would render an empty plan as a plan
// with nothing to install, which is the opposite of what happened.
var ErrPlanUnavailable = errors.New("dependency graph unavailable")

// PlanInstall reports what installing appName would do: which providers it
// needs, which are already present, which choices remain open, and which
// installed apps would be reconfigured afterwards.
//
// This is a read-through of the graph the orchestrator already owns, not a
// second planner. The orchestrator refreshes that graph's installed set on
// every convergence pass (see convergeFromStores), so a plan read here is
// computed against the same installed set the next install intent will be
// planned against.
//
// The result is advisory. Reality can move between this read and the intent
// that follows, and applyInstallIntent re-plans regardless rather than
// trusting a plan handed over from the edge.
func (o *Orchestrator) PlanInstall(appName string) (*catalog.InstallPlan, error) {
	if o.catalogGraph == nil {
		return nil, ErrPlanUnavailable
	}
	return o.catalogGraph.PlanInstall(appName)
}

// PlanRemove reports what removing appName would do: which installed apps
// would lose an integration, and whether any of them requires this provider
// with no alternative available, which makes the removal impossible rather
// than merely disruptive.
//
// Read-only, and read through the same live graph as PlanInstall. Nothing in
// the uninstall path consults this yet: applyUninstallIntent marks the app
// uninstalling without asking whether it may. Surfacing the answer is the
// first step toward enforcing it, and the two are separate changes on purpose.
func (o *Orchestrator) PlanRemove(appName string) (*catalog.RemovePlan, error) {
	if o.catalogGraph == nil {
		return nil, ErrPlanUnavailable
	}
	return o.catalogGraph.PlanRemove(appName)
}
