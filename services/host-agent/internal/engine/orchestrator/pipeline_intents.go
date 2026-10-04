// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/google/uuid"
)

// applySetTailnetIntent deletes any existing connection and creates a new one.
func (o *Orchestrator) applySetTailnetIntent(intent SetTailnetIntent) {
	if o.tailnetStore == nil {
		o.logger.Warn("tailnet store not configured, skipping SetTailnet intent")
		return
	}

	// Delete any existing active connection (MVP: single connection).
	existing, _ := o.tailnetStore.GetActive()
	if existing != nil {
		if err := o.tailnetStore.Delete(existing.ID); err != nil {
			o.logger.Error("failed to delete existing tailnet connection", "error", err)
			return
		}
	}

	conn := store.TailnetConnection{
		ID:         uuid.New().String(),
		Name:       intent.Name,
		Type:       intent.Type,
		AuthKey:    intent.AuthKey,
		ControlURL: intent.ControlURL,
		Status:     "active",
	}

	if err := o.tailnetStore.Create(conn); err != nil {
		o.logger.Error("failed to create tailnet connection", "error", err)
	}
}

// applyDeleteTailnetIntent removes the active tailnet connection from the store.
func (o *Orchestrator) applyDeleteTailnetIntent() {
	if o.tailnetStore == nil {
		o.logger.Warn("tailnet store not configured, skipping DeleteTailnet intent")
		return
	}

	conn, err := o.tailnetStore.GetActive()
	if err != nil {
		o.logger.Error("failed to get active tailnet connection", "error", err)
		return
	}
	if conn == nil {
		return
	}

	if err := o.tailnetStore.Delete(conn.ID); err != nil {
		o.logger.Error("failed to delete tailnet connection", "error", err)
	}
}

// applyAddRemoteAppIntent resolves catalog metadata and creates a remote app in the store.
func (o *Orchestrator) applyAddRemoteAppIntent(intent AddRemoteAppIntent) {
	if o.remoteAppStore == nil {
		o.logger.Warn("remote app store not configured, skipping AddRemoteApp intent")
		return
	}

	catalogApp, err := o.catalog.Get(intent.AppID)
	if err != nil {
		o.logger.Error("failed to resolve catalog app for remote app", "appId", intent.AppID, "error", err)
		return
	}
	if catalogApp == nil {
		o.logger.Error("catalog app not found for remote app", "appId", intent.AppID)
		return
	}

	bypassPaths := catalogApp.SSO.BypassPaths
	if bypassPaths == nil {
		bypassPaths = []string{}
	}

	app := store.RemoteApp{
		ID:          uuid.New().String(),
		HostLabel:   intent.HostLabel,
		AppID:       intent.AppID,
		AppName:     catalogApp.DisplayName,
		SSOStrategy: catalogApp.SSO.Strategy,
		BypassPaths: bypassPaths,
		TailnetAddr: intent.TailnetAddr,
		Status:      "active",
	}

	if err := o.remoteAppStore.Create(app); err != nil {
		o.logger.Error("failed to create remote app", "appId", intent.AppID, "error", err)
	}
}

// applyDeleteRemoteAppIntent removes a remote app from the store.
func (o *Orchestrator) applyDeleteRemoteAppIntent(intent DeleteRemoteAppIntent) {
	if o.remoteAppStore == nil {
		o.logger.Warn("remote app store not configured, skipping DeleteRemoteApp intent")
		return
	}

	if err := o.remoteAppStore.Delete(intent.RemoteAppID); err != nil {
		o.logger.Error("failed to delete remote app", "id", intent.RemoteAppID, "error", err)
	}
}

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
