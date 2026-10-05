// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
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
