// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/timezone"
)

// applyRenameAppIntent updates an app's display name in the store.
func (o *Orchestrator) applyRenameAppIntent(intent RenameAppIntent) {
	if o.appStore == nil {
		return
	}
	if err := o.appStore.UpdateDisplayName(intent.AppName, intent.DisplayName); err != nil {
		o.logger.Error("failed to rename app", "app", intent.AppName, "error", err)
	}
}

// applySetPublicURLIntent persists the new public address, swaps the runtime
// URL state, and resets SSO-dependent nodes so the convergence pass that
// follows this drain re-runs their full lifecycle: PreStart rewrites app
// configs with the new URLs (recreating changed containers), ensureSSO
// re-provisions the Authentik providers with the new redirect URIs, and
// PostStart re-applies outpost/launch configuration.
func (o *Orchestrator) applySetPublicURLIntent(intent SetPublicURLIntent) {
	public, err := hostset.ParsePublicURL(intent.URL)
	if err != nil {
		o.logger.Error("rejected public url", "url", intent.URL, "error", err)
		return
	}

	// The served port is re-applied here rather than inherited, because the
	// set is built from scratch. A set that lost it would render the detected
	// LAN URLs on port 80 while the entrypoint serves something else, and the
	// redirect URIs would be re-registered against a port nothing answers on.
	hs := hostset.New(public).WithServedPort(o.config.Runtime.TraefikPort)

	// No-op guard: skip the side effects when the derived address did not
	// change. The base URL is compared rather than the raw string, so a save
	// that only reformatted the same origin (trailing slash, an explicit
	// default port, mixed case) does not restart every SSO app for nothing.
	if o.hosts != nil && o.hosts.Get().PrimaryBaseURL() == hs.PrimaryBaseURL() {
		o.logger.Info("public url unchanged, skipping side effects", "url", hs.PrimaryBaseURL())
		return
	}

	o.logger.Info("applying public url", "url", hs.PrimaryBaseURL())

	// 1. Persist the normalized origin, so what the store holds and what the
	//    live set derives cannot diverge.
	if o.settings != nil {
		if err := o.settings.Set(store.SettingPublicURL, hs.PrimaryBaseURL()); err != nil {
			o.logger.Error("failed to persist public url", "error", err)
			return
		}
	}

	// 2. Swap the runtime URL state before the convergence pass so SSO
	//    provisioning and app config generation see the new URLs.
	if o.hosts != nil {
		o.hosts.Set(hs)
	}

	// 3. Reset SSO-dependent nodes so they re-run their full lifecycle.
	o.resetSSONodes()

	// 4. Notify the API layer (dashboard OAuth app re-ensure, etc.).
	if o.onHostsChanged != nil {
		o.onHostsChanged()
	}
}

// applySetTimezoneIntent persists the instance clock, swaps the live value the
// reconciler reads, and resets the running nodes so their containers are
// recreated with the new TZ. An empty zone clears the setting, which puts every
// container back on the default its image ships with.
//
// The reset is what makes the change real rather than cosmetic. A container's
// environment is fixed when the runtime creates it, so a new zone reaches a
// running app only when the reconciler creates that container again.
func (o *Orchestrator) applySetTimezoneIntent(intent SetTimezoneIntent) {
	zone := strings.TrimSpace(intent.Zone)
	if zone != "" {
		parsed, err := timezone.Parse(zone)
		if err != nil {
			o.logger.Error("rejected timezone", "timezone", intent.Zone, "error", err)
			return
		}
		zone = parsed
	}

	// The live state is what a container spec is built from, so an orchestrator
	// wired without it cannot apply this intent at all. Persisting the value and
	// then ignoring it would leave the store and reality disagreeing, which is
	// worse than refusing the change.
	if o.timezone == nil {
		o.logger.Error("cannot apply timezone: orchestrator has no clock state")
		return
	}

	// No-op guard: a save of the zone already in force must not recreate every
	// container on the instance for nothing.
	if o.timezone.Get() == zone {
		o.logger.Info("timezone unchanged, skipping side effects", "timezone", zone)
		return
	}

	if o.settings == nil {
		o.logger.Error("cannot apply timezone: no settings store")
		return
	}
	if err := o.settings.Set(store.SettingTimezone, zone); err != nil {
		o.logger.Error("failed to persist timezone", "error", err)
		return
	}

	o.logger.Info("applying timezone", "timezone", zone, "cleared", zone == "")
	o.timezone.Set(zone)
	o.resetRunningNodes()
}

// resetRunningNodes drops every RUNNING node back to INITIALIZING, so the
// convergence pass that follows this drain re-runs each node's full lifecycle.
//
// This is the widest reset the engine has, and only a change that reaches every
// container earns it. The instance clock is the only such change today; an
// address change resets the SSO-dependent subset, and a settings-sourced
// contract resets its consumers.
//
// A node whose desired spec did not actually change costs one lifecycle pass and
// no restart: Ensure compares the spec revision before it recreates anything.
func (o *Orchestrator) resetRunningNodes() {
	if o.graph == nil {
		return
	}
	nodes, err := o.graph.Nodes()
	if err != nil {
		o.logger.Error("failed to list nodes for the instance clock reset", "error", err)
		return
	}
	for _, node := range nodes {
		if node.ActualStatus != graph.StatusRunning {
			continue
		}
		o.logger.Info("resetting node for instance clock change", "node", node.ID)
		_ = o.graph.SetActualStatus(node.ID, graph.StatusInitializing, "")
	}
}
