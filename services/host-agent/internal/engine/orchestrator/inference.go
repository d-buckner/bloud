// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
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
	// catalog.InstanceProviderSource for the instance.
	id string
}

func appSource(id string) providerSource {
	return providerSource{kind: configurator.ProviderKindApp, id: id}
}

func instanceSource() providerSource {
	return providerSource{kind: configurator.ProviderKindInstance, id: catalog.InstanceProviderSource}
}

func (s providerSource) isInstance() bool { return s.kind == configurator.ProviderKindInstance }

// instanceProviderRef builds the ProviderRef for the instance as a contract
// provider.
//
// Node, Port, BaseURL and LocalURL stay empty on purpose: there is no container
// and no container-network address. The contract's own endpoint field carries the
// value a consumer dials. `installed` here means "the setting is populated",
// which is the instance analogue of a graph edge existing.
func instanceProviderRef(populated bool) configurator.ProviderRef {
	return configurator.ProviderRef{
		Kind:      configurator.ProviderKindInstance,
		App:       catalog.InstanceProviderSource,
		Installed: populated,
	}
}

// inferenceSettings reads the AI settings the instance holds as a contract
// provider. A missing settings store reads as "nothing configured" rather than
// an error, so a resolver call never fails a convergence pass over an absent
// optional setting.
func (o *Orchestrator) inferenceSettings() inference.Settings {
	if o.settings == nil {
		return inference.Settings{}
	}
	upstreamsJSON, err := o.settings.Get(inference.SettingUpstreams)
	if err != nil {
		o.logger.Warn("cannot read AI upstreams; the instance provides no inference", "error", err)
		return inference.Settings{}
	}
	defaultModel, err := o.settings.Get(inference.SettingDefaultModel)
	if err != nil {
		o.logger.Warn("cannot read AI default model; consumers fall back to their own", "error", err)
		defaultModel = ""
	}
	settings, err := inference.DecodeSettings(upstreamsJSON, defaultModel)
	if err != nil {
		o.logger.Warn("AI upstreams are stored in an unreadable form", "error", err)
		return inference.Settings{}
	}
	return settings
}

// instanceInferenceSource resolves the instance's configured upstream into an
// inference binding. It returns ok=false when nothing is configured; a parse
// failure is logged and also reads as not-configured, because a binding built
// from an endpoint that does not parse would be worse than none.
func (o *Orchestrator) instanceInferenceSource(requires []string) (configurator.InferenceBinding, bool) {
	settings := o.inferenceSettings()
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
		ProviderRef:  instanceProviderRef(true),
		Endpoint:     ep.String(),
		DefaultModel: settings.DefaultModel,
		Models:       upstream.Models,
		// ViaGateway is false: this is the operator's raw upstream, and the
		// credential carried with it is theirs, not a gateway-issued one.
		ViaGateway: false,
	}
	if secretAllowed(requires, "apiKey") && o.secrets != nil {
		binding.APIKey = o.secrets.GetAppSecret(inference.SecretScope, inference.SecretAPIKey)
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
	if o.settings == nil {
		o.logger.Error("cannot apply inference settings: no settings store")
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

	current := o.inferenceSettings()
	if canonicalUpstreams(current.Upstreams) == canonicalUpstreams(settings.Upstreams) &&
		current.DefaultModel == settings.DefaultModel {
		o.logger.Info("inference settings unchanged, skipping side effects")
		// The credential can still need storing or clearing even when nothing
		// else moved, so it is written before the guard returns.
		o.storeInferenceAPIKey(intent.APIKey)
		return
	}

	if err := o.settings.Set(inference.SettingUpstreams, canonicalUpstreams(settings.Upstreams)); err != nil {
		o.logger.Error("failed to persist inference upstreams", "error", err)
		return
	}
	if err := o.settings.Set(inference.SettingDefaultModel, settings.DefaultModel); err != nil {
		o.logger.Error("failed to persist inference default model", "error", err)
		return
	}
	o.storeInferenceAPIKey(intent.APIKey)

	o.logger.Info("applied inference settings",
		"upstreams", len(settings.Upstreams),
		"defaultModel", settings.DefaultModel)

	o.resetInferenceConsumers()
}

// storeInferenceAPIKey writes or clears the instance-scoped upstream credential.
// A nil pointer means the caller did not touch the field, so nothing is written.
func (o *Orchestrator) storeInferenceAPIKey(apiKey *string) {
	if apiKey == nil || o.secrets == nil {
		return
	}
	if err := o.secrets.SetAppSecret(inference.SecretScope, inference.SecretAPIKey, *apiKey); err != nil {
		o.logger.Error("failed to store the inference API key", "error", err)
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
			if err != nil || node == nil || node.ActualStatus != graph.StatusRunning {
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
			if src.isInstance() {
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
