// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"fmt"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// providerSource identifies one provider of one contract: either an installed
// catalog app or the instance itself.
//
// The instance is a provider because the operator's external inference server is
// a thing they configure, not a thing Bloud runs. Giving it a source rather than a
// catalog entry keeps it out of the graph: no node, no lifecycle, no image to pin.
type providerSource struct {
	kind configurator.ProviderKind
	// id is the catalog app ID for an app provider, or
	// catalog.SettingProviderSource for the instance.
	id string
	// external is the operator-registered record when this provider is a
	// remote install of the catalog app rather than a local one. Nil for every
	// local app, for the instance, and for every catalog scan that has not been
	// consulted against the external registry.
	//
	// It travels on the source rather than being looked up at each read so the
	// registry is consulted once per resolution, and so the three places that
	// answer "where is this provider" (the ref, the secret, the value) cannot
	// disagree about which record they resolved.
	external *store.ExternalApp
}

func appSource(id string) providerSource {
	return providerSource{kind: configurator.ProviderKindApp, id: id}
}

// externalAppSource resolves one declared `app:` provider against the
// external registry: the record wins over the local install, because the
// operator registered it precisely to say "this app lives over there".
func externalAppSource(id string, record *store.ExternalApp) providerSource {
	if record == nil {
		return appSource(id)
	}
	return providerSource{kind: configurator.ProviderKindExternalApp, id: id, external: record}
}

func (s providerSource) isSetting() bool { return s.kind == configurator.ProviderKindSetting }

// isExternal reports whether this provider is a remote install rather than a
// container Bloud runs.
func (s providerSource) isExternal() bool { return s.external != nil }

// externalAppProviderRef builds the ProviderRef for a remote install of a
// catalog app.
//
// App keeps the catalog ID, which is what makes this different from a setting
// provider: a setting is anonymous, a remote AFFiNE is still
// AFFiNE, and a consumer that names AFFiNE in its `compatible:` list should
// see AFFiNE come back. Node and Port stay empty because there is neither.
// BaseURL and LocalURL are the same origin: with no container network between
// them, what the consumer's app stores and what its configurator dials are
// one address.
func externalAppProviderRef(catalogID, endpoint string) configurator.ProviderRef {
	return configurator.ProviderRef{
		Kind:      configurator.ProviderKindExternalApp,
		App:       catalogID,
		Installed: true,
		BaseURL:   endpoint,
		LocalURL:  endpoint,
	}
}

// settingProviderRef builds the ProviderRef for the instance's own settings as
// a contract provider.
//
// Node, Port, BaseURL and LocalURL stay empty on purpose: there is no container
// and no container-network address. The contract's own endpoint field carries the
// value a consumer dials. `installed` here means "the setting is populated",
// which is the instance analogue of a graph edge existing.
func settingProviderRef(populated bool) configurator.ProviderRef {
	return configurator.ProviderRef{
		Kind:      configurator.ProviderKindSetting,
		App:       catalog.SettingProviderSource,
		Installed: populated,
	}
}

// settingRecordProviderRef builds the ProviderRef for an off-host record that
// fills a contract directly, with no catalog app behind it.
//
// App carries the record's own ID rather than the literal "setting". A
// consumer that names its provider in a config file needs something stable to
// write, and two records for the same contract have to be tellable apart. The
// Kind stays ProviderKindSetting, which is what says "the operator filled this
// role" rather than "Bloud booted this app".
func settingRecordProviderRef(rec *store.ExternalApp) configurator.ProviderRef {
	return configurator.ProviderRef{
		Kind:      configurator.ProviderKindSetting,
		App:       rec.ID,
		Installed: true,
		BaseURL:   rec.URL,
		LocalURL:  rec.URL,
	}
}

// defaultModel reads the instance's default model. It stays a settings key
// rather than a provider attribute because it names a choice among upstreams,
// not a property of any one of them.
func (o *Orchestrator) defaultModel() string {
	if o.settings == nil {
		return ""
	}
	v, err := o.settings.Get(inference.SettingDefaultModel)
	if err != nil {
		o.logger.Warn("cannot read the AI default model; consumers fall back to their own", "error", err)
		return ""
	}
	return v
}

// settingInferenceSource resolves the operator's own AI upstreams into an
// inference binding. The upstreams are the external provider records that fill
// the `inference` contract, and the first enabled one is the active one, which
// is the same rule the Settings surface has always applied.
//
// It returns ok=false when nothing is configured; a parse failure is logged and
// also reads as not-configured, because a binding built from an endpoint that
// does not parse would be worse than none.
func (o *Orchestrator) settingInferenceSource(requires []string, external externalRegistry) (configurator.InferenceBinding, bool) {
	records := external.externalForContract(inference.ContractName)
	if len(records) == 0 {
		return configurator.InferenceBinding{}, false
	}
	settings := inference.Settings{
		Upstreams:    inference.UpstreamsFromExternal(records),
		DefaultModel: o.defaultModel(),
	}
	ep, ok, err := settings.Endpoint()
	if err != nil {
		o.logger.Warn("configured AI upstream endpoint does not parse; providing no inference binding", "error", err)
		return configurator.InferenceBinding{}, false
	}
	if !ok {
		return configurator.InferenceBinding{}, false
	}

	upstream, _ := settings.ActiveUpstream()
	binding := configurator.InferenceBinding{
		ProviderRef:  settingProviderRef(true),
		Endpoint:     ep.String(),
		DefaultModel: settings.DefaultModel,
		Models:       upstream.Models,
		// ViaGateway is false: this is the operator's raw upstream, and the
		// credential carried with it is theirs, not a gateway-issued one.
		ViaGateway: false,
	}
	if secretAllowed(requires, "apiKey") && o.secrets != nil {
		binding.APIKey = o.secrets.GetAppSecret(store.ExternalSecretScope(upstream.ID), inference.ContractName)
	}
	return binding, true
}

// secretAllowed applies the contract's least-privilege gate to a credential the
// resolver reads from a non-catalog scope. The registry gate in publishedSecret
// covers app providers; the instance's own scope needs the same rule, so a
// consumer that never declared `requires: [apiKey]` is not handed the
// operator's credential by accident.
func secretAllowed(requires []string, name string) bool {
	for _, r := range requires {
		if r == name {
			return true
		}
	}
	return false
}

// applySetInferenceIntent persists the AI settings and resets every installed
// inference consumer so its PreStart re-runs against the new value.
//
// The no-op guard compares the canonical rendering, not the raw request, so a
// save that only reformatted the same configuration does not restart every
// wired app. The credential is excluded from the comparison: writing the same
// key again is not a reason to churn the stack, and not writing one is not a
// reason to skip storing it.
func (o *Orchestrator) applySetInferenceIntent(intent SetInferenceIntent) {
	if o.settings == nil || o.externalApps == nil || o.secrets == nil {
		o.logger.Error("cannot apply inference settings: the settings, external app, or secrets store is missing")
		return
	}

	// Validate before persisting: a stored value that does not parse would be
	// diagnosed at every convergence pass instead of at the save that caused it.
	settings, err := inference.DecodeSettings(intent.UpstreamsJSON, intent.DefaultModel)
	if err != nil {
		o.logger.Error("rejected inference settings", "error", err)
		return
	}
	if _, _, err := settings.Endpoint(); err != nil {
		o.logger.Error("rejected inference settings", "error", err)
		return
	}

	current := inference.UpstreamsFromExternal(o.loadExternalRegistry().externalForContract(inference.ContractName))
	unchanged := canonicalUpstreams(current) == canonicalUpstreams(settings.Upstreams) &&
		o.defaultModel() == settings.DefaultModel

	if err := o.syncInferenceUpstreams(settings.Upstreams); err != nil {
		o.logger.Error("failed to persist inference upstreams", "error", err)
		return
	}
	if err := o.settings.Set(inference.SettingDefaultModel, settings.DefaultModel); err != nil {
		o.logger.Error("failed to persist the AI default model", "error", err)
		return
	}
	o.storeInferenceAPIKey(settings.Upstreams, intent.APIKey)

	if unchanged {
		o.logger.Info("inference settings unchanged, skipping side effects")
		return
	}
	o.logger.Info("applied inference settings",
		"upstreams", len(settings.Upstreams),
		"defaultModel", settings.DefaultModel)

	o.resetInferenceConsumers()
}

// syncInferenceUpstreams makes the `contract:inference` records match the
// list the operator saved: upsert what is there, delete what is gone, and take
// the credential of a deleted upstream with it so a removed upstream cannot
// leave a key behind that nothing owns.
//
// The upstream's stable ID is the record ID, so renaming an upstream updates
// the same row rather than removing one and adding another out from under a
// consumer that is mid-reconcile.
func (o *Orchestrator) syncInferenceUpstreams(want []inference.Upstream) error {
	existing := o.loadExternalRegistry().externalForContract(inference.ContractName)
	keep := make(map[string]bool, len(want))
	for _, u := range want {
		keep[u.ID] = true
	}
	for _, rec := range existing {
		if keep[rec.ID] {
			continue
		}
		if err := o.externalApps.Delete(rec.ID); err != nil {
			return fmt.Errorf("remove stale inference record %q: %w", rec.ID, err)
		}
		if err := o.secrets.DeleteAppSecrets(store.ExternalSecretScope(rec.ID)); err != nil {
			o.logger.Warn("could not clear the credential of a removed AI upstream",
				"id", rec.ID, "error", err)
		}
	}
	for _, u := range want {
		if err := o.externalApps.Upsert(inference.ExternalForUpstream(u)); err != nil {
			return fmt.Errorf("upsert inference record %q: %w", u.ID, err)
		}
	}
	return nil
}

// storeInferenceAPIKey writes the credential the operator entered onto the
// active upstream's own record. A nil pointer means the caller did not touch
// the field, so nothing is written.
//
// It goes to the active upstream alone rather than to every one of them. The
// Settings page has a single key field, so the value entered describes the
// upstream that was selected when it was typed; stamping it onto the others
// would attribute one provider's credential to providers it was never meant
// for.
func (o *Orchestrator) storeInferenceAPIKey(upstreams []inference.Upstream, apiKey *string) {
	if apiKey == nil || o.secrets == nil {
		return
	}
	settings := inference.Settings{Upstreams: upstreams}
	active, ok := settings.ActiveUpstream()
	if !ok {
		return
	}
	if err := o.secrets.SetAppSecret(store.ExternalSecretScope(active.ID), inference.ContractName, *apiKey); err != nil {
		o.logger.Error("failed to store the inference API key", "id", active.ID, "error", err)
	}
}

// resetInferenceConsumers drops every installed inference consumer out of
// RUNNING so its PreStart re-runs and rewrites whatever it manages from the new
// binding. This is the propagation mechanism: a settings change reaches an app
// by re-running that app's own configurator, never by pushing into it.
func (o *Orchestrator) resetInferenceConsumers() {
	if o.graph == nil || o.appStore == nil || o.catalog == nil {
		return
	}
	apps, err := o.appStore.GetAll()
	if err != nil {
		return
	}
	for _, app := range apps {
		if app.IsSystem {
			continue
		}
		catalogApp, err := o.catalog.Get(app.CatalogID)
		if err != nil || catalogApp == nil {
			continue
		}
		if _, declares := catalogApp.Integrations["inference"]; !declares {
			continue
		}
		for _, container := range catalogApp.Containers {
			node, err := o.graph.GetNode(container.Name)
			if err != nil || node == nil {
				continue
			}
			if node.ActualStatus != graph.StatusRunning {
				// The API has already answered 202 accepted by the time this
				// runs, so a skipped consumer has to be visible somewhere or
				// the response reads as a promise nothing keeps. The pass that
				// owns this case is the periodic self-heal pass, not the save.
				o.logger.Info("inference consumer not reset, not running",
					"node", container.Name,
					"status", node.ActualStatus,
					"note", "picked up by the next self-healing pass once it is healthy")
				continue
			}
			o.logger.Info("resetting inference consumer for settings change", "node", container.Name)
			_ = o.graph.SetActualStatus(container.Name, graph.StatusInitializing, "")
		}
	}
}

// canonicalUpstreams renders the list in a stable form so the no-op guard
// compares meaning rather than formatting. Disabled entries keep their order and
// fields; only whitespace differences collapse.
func canonicalUpstreams(upstreams []inference.Upstream) string {
	encoded, err := inference.EncodeUpstreams(upstreams)
	if err != nil {
		return ""
	}
	return encoded
}

// providersOfContract returns every catalog app that declares `provides:` for the
// named contract. It is what contract promotion scans: a consumer asking for
// `inference` with no gateway installed needs to discover that Ollama can stand
// in, and Ollama never appears in that consumer's own `compatible:` list.
func (o *Orchestrator) providersOfContract(contract string) []providerSource {
	if o.catalog == nil {
		return nil
	}
	apps, err := o.catalog.GetAll()
	if err != nil {
		o.logger.Warn("cannot scan the catalog for contract providers", "contract", contract, "error", err)
		return nil
	}
	var out []providerSource
	for _, app := range apps {
		if app == nil {
			continue
		}
		if _, offers := app.Provides[contract]; offers {
			out = append(out, appSource(app.CatalogID))
		}
	}
	return out
}

// promotedSources resolves the fallback providers for a contract that has no
// provider of its own, following the registry's SatisfiedBy list in order.
//
// The rule lives in the contract registry rather than here so a person reading a
// consumer's metadata can see why an unmet contract still resolves. The first
// fallback contract with an installed provider wins; falling through every
// fallback and binding nothing is the correct result when there is genuinely
// nothing to serve the contract.
func (o *Orchestrator) promotedSources(contract string, installed map[string]bool) (string, []providerSource) {
	spec, known := catalog.ContractFor(contract)
	if !known || len(spec.SatisfiedBy) == 0 {
		return "", nil
	}
	for _, fallback := range spec.SatisfiedBy {
		candidates := o.providersOfContract(fallback)
		var ready []providerSource
		for _, src := range candidates {
			if src.isSetting() {
				continue
			}
			if installed[src.id] {
				ready = append(ready, src)
			}
		}
		if len(ready) > 0 {
			return fallback, ready
		}
	}
	return "", nil
}
