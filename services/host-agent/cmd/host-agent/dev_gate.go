// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"sort"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
)

// DevFastGateEnv opens the API gate before the first convergence pass finishes.
//
// It is a development-only switch and it deliberately relaxes invariant 5 ("the
// bootstrap converges before the API is usable") for one case: a host-agent
// restarting on top of a stack that is *already* up. That is the hot-reload
// case, and the wait it removes is the whole point of the switch. Measured on a
// warm native stack, the full first pass costs about 15s, nearly all of it in
// Authentik's idempotent PostStart, which is time the dashboard spends behind
// the loading page even though every container it depends on is already
// serving.
//
// The relaxation is narrow on purpose. The gate opens early only when every
// system container is already running and the dashboard's OIDC client is
// already initialized, so the thing invariant 5 protects against, an API that
// answers before its identity provider exists, cannot happen. On a cold stack
// the switch does nothing at all and startup is the ordinary product path.
//
// What is traded away: for the few seconds the background pass is still running,
// the API can report state that is about to change, and a failure in that pass
// is logged rather than fatal. Both are what a developer wants. Neither is
// acceptable in the product path, which is why this is opt-in.
const DevFastGateEnv = "BLOUD_DEV_FAST_GATE"

// containerLister is the read-only slice of the podman client the fast gate
// needs. Declaring it here rather than taking *podman.Client keeps the decision
// testable without a container runtime.
type containerLister interface {
	ListContainers(ctx context.Context) ([]podman.Container, error)
}

// systemContainerNames collects the container names declared by the system
// apps (Traefik, Authentik, and their infra). These are the containers whose
// absence means the control plane really is cold.
//
// It reads the catalog rather than a hardcoded list so a new system app cannot
// be left out of the check and silently weaken it.
func systemContainerNames(apps []*catalog.App) []string {
	var names []string
	for _, app := range apps {
		if app == nil || !app.IsSystem {
			continue
		}
		for _, c := range app.Containers {
			if c.Name != "" {
				names = append(names, c.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// warmStackReport is the outcome of comparing the required system containers
// against what the runtime reports.
type warmStackReport struct {
	Required []string
	Running  []string
	Missing  []string
}

// Complete reports whether every required system container is running.
func (r warmStackReport) Complete() bool { return len(r.Missing) == 0 && len(r.Required) > 0 }

// checkWarmStack reports which system containers are already running.
//
// "Running" is the bar, not "healthy". Health checks are the slow part of a
// cold boot and the fast gate is not trying to certify the stack: it is asking
// whether the process it is about to serve from has its dependencies up. The
// background convergence pass does the real health work either way.
func checkWarmStack(ctx context.Context, lister containerLister, required []string) (warmStackReport, error) {
	report := warmStackReport{Required: required}
	if lister == nil {
		return report, fmt.Errorf("no container runtime to check")
	}
	if len(required) == 0 {
		return report, nil
	}

	containers, err := lister.ListContainers(ctx)
	if err != nil {
		return report, fmt.Errorf("list containers: %w", err)
	}

	running := make(map[string]bool, len(containers))
	for _, c := range containers {
		if c.State == "running" {
			for _, name := range c.Names {
				running[name] = true
			}
		}
	}

	for _, want := range required {
		if running[want] {
			report.Running = append(report.Running, want)
		} else {
			report.Missing = append(report.Missing, want)
		}
	}
	return report, nil
}

// devFastGateEnabled reports whether the fast gate switch is on. Only the exact
// value "1" turns it on, so a stray non-empty value cannot enable it by
// accident.
func devFastGateEnabled(getenv func(string) string) bool {
	return getenv(DevFastGateEnv) == "1"
}

// devFastGateDecision is the verdict plus the reason it was reached. The reason
// is logged either way: a developer who set the switch and does not get the
// fast path needs to know why, or the switch reads as broken.
type devFastGateDecision struct {
	Open   bool
	Reason string
}

// decideDevFastGate is the whole policy, in one place, with no I/O of its own.
func decideDevFastGate(warm warmStackReport, checkErr error, authReady bool) devFastGateDecision {
	if checkErr != nil {
		return devFastGateDecision{Reason: fmt.Sprintf("could not inspect the running stack: %v", checkErr)}
	}
	if !warm.Complete() {
		return devFastGateDecision{Reason: fmt.Sprintf("system containers not running: %v", warm.Missing)}
	}
	if !authReady {
		return devFastGateDecision{Reason: "dashboard OIDC client is not initialized yet"}
	}
	return devFastGateDecision{
		Open:   true,
		Reason: fmt.Sprintf("all %d system containers already running and the dashboard OIDC client is initialized", len(warm.Running)),
	}
}

// closedGate returns a gate channel that is already open: the API is usable the
// moment it starts listening.
func closedGate() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
