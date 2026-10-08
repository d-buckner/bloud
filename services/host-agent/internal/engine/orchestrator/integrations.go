// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"fmt"
	"slices"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/dirs"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// buildAppState constructs a configurator.AppState for the given app ID
// using catalog metadata when available.
func (o *Orchestrator) buildAppState(id string) *configurator.AppState {
	state := &configurator.AppState{
		DataPath:      dirs.AppDataDir(o.dataDir, o.ownerApp(id)),
		BloudDataPath: o.dataDir,
	}

	if o.catalog == nil {
		return state
	}

	catalogApp, err := o.catalog.Get(id)
	if (err != nil || catalogApp == nil) && o.ownerApp(id) != id {
		catalogApp, err = o.catalog.Get(o.ownerApp(id))
	}
	if err != nil || catalogApp == nil {
		return state
	}

	ssoEnabled := catalogApp.SSO.Strategy != "" && catalogApp.SSO.Strategy != "none"
	state.SSOEnabled = ssoEnabled
	if ssoEnabled {
		o.logger.Info("SSO enabled for app", "app", id, "strategy", catalogApp.SSO.Strategy)
		switch catalogApp.SSO.Strategy {
		case "ldap":
			if o.config.SSO.LDAPOutput != nil {
				state.LDAP = o.config.SSO.LDAPOutput
			}
		case "native-oidc":
			if inputs := o.oidcInputsForApp(catalogApp, o.resolveSSOURLs()); inputs != nil && len(inputs.RedirectURIs) > 0 {
				state.OIDC = &configurator.OIDCOutput{
					ClientID:     inputs.ClientID,
					ClientSecret: inputs.ClientSecret,
					IssuerURL:    inputs.IssuerURL,
					RedirectURI:  inputs.RedirectURIs[0],
				}
			}
		}
	}

	state.Integrations = o.buildIntegrations(catalogApp.CatalogID, catalogApp)

	return state
}

// buildIntegrations resolves an app's integration contracts into typed
// bindings, so a consumer is handed its providers' identity, address and
// contract payload instead of discovering any of it itself (probing a port,
// reading a sibling's config file).
//
// A contract binds every provider the app's metadata declares for it: the
// choice recorded in the app's integration config, plus the compatible apps in
// the app's own metadata (which is what the graph ordered for the same set in
// computeAppDeps). Providers that are not installed are bound too, with
// Installed false: a consumer needs their address to prune the entry Bloud wrote
// for them.
func (o *Orchestrator) buildIntegrations(app string, catalogApp *catalog.App) configurator.Integrations {
	var out configurator.Integrations
	if o.appStore == nil || len(catalogApp.Integrations) == 0 {
		return out
	}
	installedApps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Warn("cannot resolve integration bindings; configurators run without them", "app", app, "error", err)
		return out
	}
	installed := installedSet(installedApps)
	external := o.loadExternalRegistry()

	for contract, integration := range catalogApp.Integrations {
		if contract == "inference" {
			// Inference has its own resolution because it is the one contract
			// that can be served by a provider the consumer never named: a
			// gateway app, the operator's own upstream setting, or promotion
			// from an installed modelSource.
			if binding, ok := o.resolveInference(integration, installed, app, external); ok {
				out.Inference = append(out.Inference, binding)
			}
			continue
		}
		o.bindAppProviders(&out, contract, integration, installed, app, external)
	}
	return out
}

// externalRegistry is the external app table read once per resolution pass,
// indexed the two ways resolution needs it.
//
// byApp answers "did the operator register a remote copy of this catalog
// app". byContract answers "did the operator declare something that fills
// this role", which is what a consumer's `compatible: [{source: setting}]`
// is asking. A record can appear in both only if it were both kinds, which the
// source grammar makes impossible, so the two indexes never disagree.
type externalRegistry struct {
	byApp      map[string]*store.ExternalApp
	byContract map[string][]*store.ExternalApp
}

// externalForContract returns the records that fill one contract. Order is the
// registry's own; where exactly one is wanted the caller picks the first
// enabled, which is the same rule the AI Settings already used.
func (r externalRegistry) externalForContract(contract string) []*store.ExternalApp {
	return r.byContract[contract]
}

func (o *Orchestrator) loadExternalRegistry() externalRegistry {
	reg := externalRegistry{
		byApp:      map[string]*store.ExternalApp{},
		byContract: map[string][]*store.ExternalApp{},
	}
	if o.externalApps == nil {
		return reg
	}
	apps, err := o.externalApps.GetAll()
	if err != nil {
		o.logger.Warn("cannot read the external app registry; resolving every provider as local", "error", err)
		return reg
	}
	for _, app := range apps {
		kind, ref, ok := store.ParseExternalAppSource(app.Source)
		if !ok {
			continue
		}
		switch kind {
		case store.ExternalAppSourceKindApp:
			reg.byApp[ref] = app
		case store.ExternalAppSourceKindContract:
			reg.byContract[ref] = append(reg.byContract[ref], app)
		}
	}
	return reg
}

// installedSet is the set of catalog IDs that have an installed app row.
func installedSet(apps []*store.InstalledApp) map[string]bool {
	out := make(map[string]bool, len(apps))
	for _, a := range apps {
		out[a.CatalogID] = true
	}
	return out
}

// bindAppProviders binds every declared provider of one contract that is a
// real, non-self app: a local install, or an external record standing in for
// one.
func (o *Orchestrator) bindAppProviders(out *configurator.Integrations, contract string, integration catalog.Integration, installed map[string]bool, app string, external externalRegistry) {
	for _, src := range resolveProviders(contract, integration, external) {
		// An app cannot be its own provider: a self-edge would also make
		// the graph order the node after itself.
		if src.kind == configurator.ProviderKindApp && src.id == app {
			continue
		}
		if src.isSetting() {
			// A setting source is an off-host record that fills the role. There is
			// no catalog app behind it, so the address is the record's own and the
			// offer is the contract's declared shape with nothing runtime-filled:
			// the operator is the runtime.
			o.bindContract(out, contract, settingRecordProviderRef(src.external),
				catalog.ContractProvides{}, src, integration.Requires)
			continue
		}
		provider, err := o.catalog.Get(src.id)
		if err != nil || provider == nil {
			continue
		}
		o.bindContract(out, contract, o.refFor(src, provider, installed[src.id]), provider.Provides[contract], src, integration.Requires)
	}
}

// refFor answers where one resolved provider is reachable. An external record
// short-circuits the container-address computation entirely: there is no node
// to name and no port to compose, only the endpoint the operator gave.
func (o *Orchestrator) refFor(src providerSource, provider *catalog.App, installed bool) configurator.ProviderRef {
	if src.isExternal() {
		return externalAppProviderRef(src.id, src.external.URL)
	}
	return o.providerRef(src.id, provider, installed)
}

// resolveInference picks the single inference endpoint a consumer gets, by
// precedence:
//
//  1. an installed gateway app the consumer named (ViaGateway true): the real
//     provider always wins, so adding a gateway retires the fallback rather than
//     competing with it;
//  2. the instance's configured upstream (ViaGateway false), which is the
//     operator's raw server and carries their credential;
//  3. promotion from an installed modelSource provider, per the registry's
//     SatisfiedBy list, so a bare Ollama serves a consumer that never named it.
//
// One binding, not several: a consumer dialing two inference endpoints has no
// defined meaning, so the multi case is resolved here rather than pushed onto
// every configurator.
func (o *Orchestrator) resolveInference(integration catalog.Integration, installed map[string]bool, consumer string, external externalRegistry) (configurator.InferenceBinding, bool) {
	for _, src := range resolveProviders(inference.ContractName, integration, external) {
		if src.isSetting() || src.id == consumer || (!installed[src.id] && !src.isExternal()) {
			continue
		}
		ref, offer, ok := o.usableInferenceProvider("inference", src)
		if !ok {
			continue
		}
		return configurator.InferenceBinding{
			ProviderRef:  ref,
			Endpoint:     ref.BaseURL + offer.Values["path"],
			APIKey:       o.publishedSecret(src, "inference", offer, integration.Requires),
			DefaultModel: o.defaultModel(),
			ViaGateway:   true,
		}, true
	}

	if binding, ok := o.settingInferenceSource(integration.Requires, external); ok {
		return binding, true
	}

	from, sources := o.promotedSources("inference", installed)
	if from == "" {
		return configurator.InferenceBinding{}, false
	}
	for _, src := range sources {
		if src.id == consumer {
			continue
		}
		ref, offer, ok := o.usableInferenceProvider(from, src)
		if !ok {
			continue
		}
		return configurator.InferenceBinding{
			ProviderRef:  ref,
			Endpoint:     ref.BaseURL + offer.Values["path"],
			DefaultModel: o.defaultModel(),
			// Promoted from a keyless source: the consumer is talking to
			// the raw upstream, not a gateway, and holds no gateway key.
			ViaGateway: false,
		}, true
	}

	return configurator.InferenceBinding{}, false
}

// usableInferenceProvider resolves one source to an inference provider that
// actually has an address to hand out. Anything else reports false so the
// caller falls through to the next candidate rather than binding a provider
// nothing can dial.
func (o *Orchestrator) usableInferenceProvider(contract string, src providerSource) (configurator.ProviderRef, catalog.ContractProvides, bool) {
	provider, err := o.catalog.Get(src.id)
	if err != nil || provider == nil {
		return configurator.ProviderRef{}, catalog.ContractProvides{}, false
	}
	offer, ok := provider.Provides[contract]
	if !ok {
		return configurator.ProviderRef{}, catalog.ContractProvides{}, false
	}
	ref := o.refFor(src, provider, true)
	if ref.BaseURL == "" {
		return configurator.ProviderRef{}, catalog.ContractProvides{}, false
	}
	return ref, offer, true
}

// bindContract appends one provider's binding for one contract. The payload it
// builds is the only contract-specific code in the resolver: a provider of an
// existing contract is pure metadata, and adding a contract means adding an arm
// here plus its payload type and its registry entry.
//
// Required secret names come from the registry rather than being repeated here,
// so the name a provider publishes and the name the payload reads cannot drift,
// and a secret is resolved only when the consumer declared it in
// `integrations.<contract>.requires`. That is what keeps the payload least
// privilege: an app that integrates with the identity provider for SSO is not
// handed the provider's API token unless it says it reads it.
func (o *Orchestrator) bindContract(
	out *configurator.Integrations,
	contract string,
	ref configurator.ProviderRef,
	offer catalog.ContractProvides,
	src providerSource,
	requires []string,
) {
	switch contract {
	case "pvr":
		out.PVRs = append(out.PVRs, configurator.PVRBinding{ProviderRef: ref, APIKey: o.publishedSecret(src, contract, offer, requires)})
	case "mediaServer":
		out.MediaServers = append(out.MediaServers, configurator.MediaServerBinding{
			ProviderRef:   ref,
			AdminUsername: o.contractValue(src, contract, offer, "adminUsername"),
			AdminPassword: o.publishedSecret(src, contract, offer, requires),
		})
	case "sso":
		out.SSO = append(out.SSO, configurator.SSOBinding{ProviderRef: ref, APIToken: o.publishedSecret(src, contract, offer, requires)})
	case "downloadClient":
		out.DownloadClients = append(out.DownloadClients, configurator.DownloadClientBinding{ProviderRef: ref})
	case "modelSource":
		// An app provider of modelSource (Ollama) is keyless by contract: the
		// credential a gateway needs for the operator's external server comes
		// from the instance scope, not from this provider.
		out.ModelSources = append(out.ModelSources, configurator.ModelSourceBinding{
			ProviderRef: ref,
			Endpoint:    ref.BaseURL + offer.Values["path"],
		})
	case "mcp":
		// The path may be one the provider minted at runtime (an endpoint under
		// an id the app generated on first boot), so it resolves through the
		// published-value channel with the static metadata as the fallback.
		out.MCPServers = append(out.MCPServers, configurator.MCPBinding{
			ProviderRef: ref,
			ServerName:  o.contractValue(src, contract, offer, "serverName"),
			Path:        o.contractValue(src, contract, offer, "path"),
			Token:       o.publishedSecret(src, contract, offer, requires),
		})
	case "appApi":
		// The username is a non-secret value the provider mints at runtime (the
		// account it bootstrapped), so it resolves through the published-value
		// channel; the password is the ordinary single-secret payload. A consumer
		// that did not require the secret gets the username and an empty password.
		//
		// For an external provider the same three reads answer the operator
		// instead of a local boot, which is the whole mechanism: the consumer's
		// configurator cannot tell the two apart, and does not need to.
		out.AppAPIs = append(out.AppAPIs, configurator.AppAPIBinding{
			ProviderRef: ref,
			Username:    o.contractValue(src, contract, offer, "username"),
			Password:    o.publishedSecret(src, contract, offer, requires),
			WorkspaceID: o.contractValue(src, contract, offer, "workspaceId"),
		})
	case "agentApi":
		out.AgentAPIs = append(out.AgentAPIs, o.agentAPIBinding(ref, contract, offer, src, requires))
	case "caldav":
		// No secret arm: the `caldav` contract publishes none, because the
		// credential is the person's own password and it never crosses an app
		// boundary. The address is the whole payload, and the browser-facing
		// half of it is the part a consumer cannot derive for itself.
		out.CalDAVServers = append(out.CalDAVServers, configurator.CalDAVBinding{
			ProviderRef: ref,
			PublicURL:   o.providerPublicURL(src),
			Path:        offer.Values["path"],
		})
	case "icsFeed":
		// The consumer (Radicale's ics-sync storage) composes the feed URL from
		// the provider's address, the declared path, and the key as a query
		// parameter. The Servarr feed endpoint accepts no header auth, which is
		// the whole reason the key travels in the URL.
		out.ICSFeeds = append(out.ICSFeeds, configurator.ICSFeedBinding{
			ProviderRef:  ref,
			APIKey:       o.publishedSecret(src, contract, offer, requires),
			Path:         offer.Values["path"],
			CalendarName: offer.Values["calendarName"],
		})
	default:
		// Contracts with no payload (proxy, database) need no consumer input
		// beyond the address, which the graph edge already encodes. A contract
		// that *does* carry a payload and lands here is a bug in this switch,
		// and silence would look exactly like "the provider published nothing",
		// so say so.
		if spec, known := catalog.ContractFor(contract); known && (len(spec.Secrets) > 0 || len(spec.Values) > 0) {
			o.logger.Warn("integration contract carries a payload but has no binding here; consumers of it receive nothing",
				"contract", contract, "provider", src.id)
		}
	}
}

// agentAPIBinding builds one agent endpoint binding.
//
// The endpoint is composed here rather than left for the consumer to assemble,
// because the address a consumer can dial is not the one in the provider's own
// metadata. A host-networked agent does not resolve by container name from the
// app network, so ProviderRef's container-DNS BaseURL is the one address that
// cannot work; the routed public origin is the one that can.
func (o *Orchestrator) agentAPIBinding(
	ref configurator.ProviderRef,
	contract string,
	offer catalog.ContractProvides,
	src providerSource,
	requires []string,
) configurator.AgentAPIBinding {
	return configurator.AgentAPIBinding{
		ProviderRef: ref,
		Endpoint:    o.routedEndpoint(src, offer),
		APIKey:      o.publishedSecret(src, contract, offer, requires),
		ModelName:   o.contractValue(src, contract, offer, "modelName"),
	}
}

// routedEndpoint composes the address a consumer dials for a contract served
// on one of the provider's ports: the provider's public origin with that
// port's declared path prefix appended.
//
// The public origin rather than the container address is the whole point. A
// provider on the app network and a provider sharing the host namespace are
// not equally reachable from a consumer's network position, but the routed
// origin is reachable from every one of them, because it is the origin the
// proxy itself serves. Composing here means a consumer never has to know
// which topology its provider happens to sit in.
//
// Returns empty when the provider cannot be routed, which a consumer reads as
// "not ready" and writes nothing.
func (o *Orchestrator) routedEndpoint(src providerSource, offer catalog.ContractProvides) string {
	provider, err := o.catalog.Get(src.id)
	if err != nil || provider == nil {
		return ""
	}
	prefix := ""
	if offer.Port != "" {
		ep := provider.ExtraPort(offer.Port)
		if ep == nil {
			// The loader rejects an unresolvable port name, so reaching here
			// means a catalog was mutated after the load. Say so rather than
			// silently routing to the UI port instead.
			o.logger.Warn("contract offer names a port the provider does not declare",
				"provider", src.id, "port", offer.Port)
			return ""
		}
		prefix = ep.PathPrefix
	}
	if src.isExternal() {
		// The remote install serves the same paths its catalog entry describes;
		// only the origin differs. Composing the declared prefix onto the
		// operator's endpoint is what keeps the prefix single-sourced.
		return src.external.URL + prefix
	}
	return o.appPublicURL(src.id) + prefix
}

// providerPublicURL is the browser-facing origin of one provider: the routed
// public URL for a local install, the operator's endpoint for a remote one.
func (o *Orchestrator) providerPublicURL(src providerSource) string {
	if src.isExternal() {
		return src.external.URL
	}
	return o.appPublicURL(src.id)
}

// publishedSecret returns the secret a single-secret contract carries, or "" when
// the consumer did not require it or the provider has not published it yet. The
// two are the same empty field on purpose: a consumer has to tell "not ready"
// from an empty credential, and "I did not ask for it" is a metadata mistake it
// can see in its own `requires`.
func (o *Orchestrator) publishedSecret(src providerSource, contract string, offer catalog.ContractProvides, requires []string) string {
	spec, ok := catalog.ContractFor(contract)
	if !ok || len(spec.Secrets) != 1 {
		return ""
	}
	if o.secrets == nil {
		return ""
	}
	// The least-privilege gate runs before the external branch, not after it:
	// a consumer that did not declare the secret does not get it, whether the
	// provider is a container Bloud booted or a password the operator typed.
	if !slices.Contains(requires, spec.Secrets[0]) {
		return ""
	}
	if src.isExternal() {
		return o.secrets.GetAppSecret(store.ExternalSecretScope(src.external.ID), contract)
	}
	if len(offer.Secrets) == 0 {
		return ""
	}
	return o.secrets.GetAppSecret(src.id, spec.Secrets[0])
}

// contractValue resolves one non-secret contract value: the runtime-published one
// if the provider declared the key under `runtimeValues` and has published it,
// otherwise the static value from the offer.
//
// The runtime value wins over the static one rather than merging into it because
// the loader forbids a key being declared both ways, so there is never a
// disagreement to arbitrate: a key is either metadata-owned or runtime-owned.
func (o *Orchestrator) contractValue(src providerSource, contract string, offer catalog.ContractProvides, key string) string {
	if src.isExternal() {
		// There is no runtime-published channel for a provider Bloud does not
		// boot: the operator's value is the authoritative one, and it is static
		// until they edit it.
		return src.external.Value(contract, key)
	}
	if slices.Contains(offer.RuntimeValues, key) && o.secrets != nil {
		if v := o.secrets.GetAppContractValue(src.id, contract, key); v != "" {
			return v
		}
	}
	return offer.Values[key]
}

// providerRef resolves where a provider is reachable: its node on the app
// network, its published port, and the two URLs a consumer needs (what its app
// stores, and what its configurator calls).
func (o *Orchestrator) providerRef(appID string, provider *catalog.App, installed bool) configurator.ProviderRef {
	node := o.primaryContainerNode(appID)
	ref := configurator.ProviderRef{
		Kind:      configurator.ProviderKindApp,
		App:       appID,
		Installed: installed,
		Node:      node,
		Port:      provider.Port,
	}
	if provider.Port > 0 {
		ref.BaseURL = fmt.Sprintf("http://%s:%d", node, provider.Port)
		ref.LocalURL = fmt.Sprintf("http://localhost:%d", provider.Port)
	}
	return ref
}

// resolveProviders returns the providers an integration declares, in
// declaration order.
//
// The rule is catalog.DeclaredProviders, shared with computeAppDeps and with
// the developer graph's edge builder so the three cannot disagree. The set is
// the declaration: every compatible provider. Whether each one is actually
// wired is the caller's question, answered by whether it is installed (or,
// for the instance, configured).
//
// A `source: setting` entry becomes one setting providerSource per external
// record that fills this contract. That is the generalization the rename was
// for: the consumer named a role, and any off-host thing the operator declared
// for that role satisfies it. With no such record the entry resolves to
// nothing, which reads to the consumer as "no provider", the same way an
// uninstalled app does.
//
// An `app:` entry is resolved against the external registry the caller loaded,
// so a remote install of a catalog app arrives as an external providerSource
// rather than a local one.
func resolveProviders(contract string, integration catalog.Integration, external externalRegistry) []providerSource {
	var out []providerSource
	for _, declared := range catalog.DeclaredProviders(integration) {
		if declared.IsSetting() {
			for _, rec := range external.externalForContract(contract) {
				out = append(out, providerSource{
					kind:     configurator.ProviderKindSetting,
					id:       catalog.SettingProviderSource,
					external: rec,
				})
			}
			continue
		}
		out = append(out, externalAppSource(declared.App, external.byApp[declared.App]))
	}
	return out
}
