// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// externalProviderField is one input the operator fills in for one contract of
// an external provider.
type externalProviderField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Required bool   `json:"required"`
	// Help names where the value comes from, because the answer differs by
	// whether the provider was booted by Bloud or is someone's install down
	// the hall: a runtime value a configurator would have minted is now
	// something the operator reads out of the remote app's own UI.
	Help string `json:"help,omitempty"`
}

// externalProviderContract groups one contract's fields for the form.
type externalProviderContract struct {
	Name   string                  `json:"name"`
	Fields []externalProviderField `json:"fields"`
}

// externalProviderOption is one catalog app the operator can point Bloud at,
// with the whole form shape derived from its `provides:` and the contract
// registry rather than hand-written per app.
type externalProviderOption struct {
	App         string                     `json:"app"`
	DisplayName string                     `json:"displayName"`
	Description string                     `json:"description"`
	Installed   bool                       `json:"installed"`
	Contracts   []externalProviderContract `json:"contracts"`
}

// ProvidersHandler returns the selectable external providers.
//
// The form is generated, not authored: the contract registry already states
// what a provider must publish, so the same list that makes the loader reject a
// bad `provides:` declaration is what makes this form ask for exactly the right
// fields. An app that gains a contract needs no change here.
func (m *externalAppsModule) ProvidersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if m.catalog == nil {
			respondJSON(w, http.StatusOK, []externalProviderOption{})
			return
		}
		apps, err := m.catalog.GetAll()
		if err != nil {
			m.logger.Error("failed to list catalog for external providers", "error", err)
			respondError(w, http.StatusInternalServerError, "could not list external providers")
			return
		}
		installed := m.installedCatalogIDs()
		out := make([]externalProviderOption, 0, len(apps))
		for _, app := range apps {
			if app == nil || app.IsSystem || len(app.Provides) == 0 {
				continue
			}
			out = append(out, externalProviderOption{
				App:         app.CatalogID,
				DisplayName: app.DisplayName,
				Description: app.Description,
				Installed:   installed[app.CatalogID],
				Contracts:   providerContractFields(app),
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
		respondJSON(w, http.StatusOK, out)
	}
}

// providerContractFields derives the form fields for every contract a provider
// app offers.
func providerContractFields(app *catalog.App) []externalProviderContract {
	contracts := make([]string, 0, len(app.Provides))
	for name := range app.Provides {
		contracts = append(contracts, name)
	}
	sort.Strings(contracts)

	out := make([]externalProviderContract, 0, len(contracts))
	for _, name := range contracts {
		offer := app.Provides[name]
		spec, known := catalog.ContractFor(name)
		if !known {
			continue
		}
		fields := make([]externalProviderField, 0, len(spec.Values)+len(spec.Secrets))
		for _, vs := range spec.Values {
			fields = append(fields, externalProviderField{
				Key:      vs.Key,
				Label:    humanizeFieldKey(vs.Key),
				Kind:     "value",
				Required: !vs.Optional,
				Help:     runtimeValueHelp(offer, vs.Key),
			})
		}
		for _, secret := range spec.Secrets {
			fields = append(fields, externalProviderField{
				Key:      secret,
				Label:    humanizeFieldKey(secret),
				Kind:     "secret",
				Required: true,
			})
		}
		out = append(out, externalProviderContract{Name: name, Fields: fields})
	}
	return out
}

// runtimeValueHelp explains, for a value the provider would normally mint at
// boot, that the operator is now the one who has to supply it.
func runtimeValueHelp(offer catalog.ContractProvides, key string) string {
	if !containsString(offer.RuntimeValues, key) {
		return ""
	}
	return "A local install mints this itself; for a remote one, read it from that instance."
}

// decodeProvider validates a `kind: provider` request against the catalog and
// the contract registry and returns the spec the intent will carry.
func (m *externalAppsModule) decodeProvider(name, rawURL, icon string, req setExternalAppRequest, id string) (orchestrator.ExternalAppSpec, error) {
	kind, ref, ok := store.ParseExternalAppSource(req.Source)
	if !ok {
		return orchestrator.ExternalAppSpec{}, fmt.Errorf("source must be app:<catalog app> or contract:<contract name>")
	}
	if kind == store.ExternalAppSourceKindContract {
		return m.decodeContractProvider(ref, name, rawURL, icon, req, id)
	}
	if kind != store.ExternalAppSourceKindApp {
		return orchestrator.ExternalAppSpec{}, fmt.Errorf("source %q is not selectable yet; only app:<catalog app> is", strings.TrimSpace(req.Source))
	}
	if m.catalog == nil {
		return orchestrator.ExternalAppSpec{}, fmt.Errorf("catalog unavailable")
	}
	provider, err := m.catalog.Get(ref)
	if err != nil || provider == nil {
		return orchestrator.ExternalAppSpec{}, fmt.Errorf("unknown catalog app %q", ref)
	}
	if provider.IsSystem {
		return orchestrator.ExternalAppSpec{}, fmt.Errorf("%q is a system app and cannot be externalized", ref)
	}
	if len(provider.Provides) == 0 {
		return orchestrator.ExternalAppSpec{}, fmt.Errorf("%q provides no integration contract, so pointing at a remote copy would wire nothing", ref)
	}
	if m.installedCatalogIDs()[ref] {
		return orchestrator.ExternalAppSpec{}, fmt.Errorf("%q is installed locally; uninstall it before pointing Bloud at a remote one", ref)
	}
	endpoint, err := validateEndpointURL(rawURL)
	if err != nil {
		return orchestrator.ExternalAppSpec{}, err
	}
	values, err := validateProviderValues(provider, req.Values)
	if err != nil {
		return orchestrator.ExternalAppSpec{}, err
	}
	// An update that names no credentials is not a claim that this provider
	// has none. It means "leave the ones on file alone", and the orchestrator
	// honors that by never blanking a stored credential an update did not
	// mention. Requiring one here would force a client to resend a secret it
	// cannot read back, which is why the rename case has to be exempted rather
	// than merely tolerated.
	keepExistingSecrets := id != "" && len(req.Secrets) == 0
	secrets, err := validateProviderSecrets(provider, req.Secrets, keepExistingSecrets)
	if err != nil {
		return orchestrator.ExternalAppSpec{}, err
	}
	return orchestrator.ExternalAppSpec{
		ID:      id,
		Kind:    string(store.ExternalAppKindProvider),
		Source:  store.ExternalAppSourceForApp(ref),
		Name:    name,
		URL:     endpoint,
		Icon:    icon,
		Values:  values,
		Secrets: secrets,
	}, nil
}

// Contracts that cannot be filled from off-host through this mechanism.
//
// Each is load-bearing inside the instance in a way a pointer cannot carry.
// `proxy` is the thing the instance itself is reached through; `database` is
// wired into app containers as a credential minted at install; `sso` is the
// identity boundary, and pointing it somewhere else would hand a third party
// the keys to every app on the box. A consumer that needs one of those gets
// the local provider or nothing.
var nonExternalizableContracts = map[string]string{
	"proxy":    "Bloud's own reverse proxy cannot be replaced by a remote one",
	"database": "database credentials are minted per app at install time",
	"sso":      "the identity provider is the instance's own trust boundary",
}

// decodeContractProvider validates a `source: contract:<name>` request: a bare
// off-host provider that fills one named role with no catalog app behind it.
//
// The contract registry is the whole schema here. The operator is the runtime,
// so the values a provider would mint at install are the values this form asks
// for, validated against the same `Values` spec the catalog loader applies to a
// provider's own declaration.
func (m *externalAppsModule) decodeContractProvider(
	contractName, name, rawURL, icon string,
	req setExternalAppRequest,
	id string,
) (orchestrator.ExternalAppSpec, error) {
	if reason, blocked := nonExternalizableContracts[contractName]; blocked {
		return orchestrator.ExternalAppSpec{}, fmt.Errorf("contract %q cannot be filled externally: %s", contractName, reason)
	}
	spec, known := catalog.ContractFor(contractName)
	if !known {
		return orchestrator.ExternalAppSpec{}, fmt.Errorf("unknown contract %q", contractName)
	}
	endpoint, err := validateEndpointURL(rawURL)
	if err != nil {
		return orchestrator.ExternalAppSpec{}, err
	}

	declared := declaredValueKeys(spec, catalog.ContractProvides{})
	supplied := req.Values[contractName]
	for key := range supplied {
		if _, ok := declared[key]; !ok {
			return orchestrator.ExternalAppSpec{}, fmt.Errorf("contract %q does not declare a value %q", contractName, key)
		}
	}
	values := map[string]map[string]string{}
	for key, info := range declared {
		if info.required && strings.TrimSpace(supplied[key]) == "" {
			return orchestrator.ExternalAppSpec{}, fmt.Errorf("contract %q requires a value for %q", contractName, key)
		}
	}
	if len(supplied) > 0 {
		values[contractName] = supplied
	}

	secrets := map[string]string{}
	keepExistingSecrets := id != "" && len(req.Secrets) == 0
	if len(spec.Secrets) > 0 && !keepExistingSecrets {
		if strings.TrimSpace(req.Secrets[contractName]) == "" {
			return orchestrator.ExternalAppSpec{}, fmt.Errorf("contract %q requires a credential", contractName)
		}
		secrets[contractName] = req.Secrets[contractName]
	}

	return orchestrator.ExternalAppSpec{
		ID:      id,
		Kind:    string(store.ExternalAppKindProvider),
		Source:  store.ExternalAppSourceForContract(contractName),
		Name:    name,
		URL:     endpoint,
		Icon:    icon,
		Values:  values,
		Secrets: secrets,
	}, nil
}

// validateProviderValues checks the operator-supplied non-secret values against
// what the provider's contracts declare, and requires every non-optional one.
//
// The requirement is deliberate. The record claims to satisfy every contract
// the catalog app provides, so a missing required value would resolve into a
// consumer binding with an empty field, which is indistinguishable from "the
// provider has not published it yet". Rejecting at the boundary is the same
// rule the catalog loader applies to a provider's own declaration.
func validateProviderValues(provider *catalog.App, in map[string]map[string]string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for _, contractName := range sortedContractNames(provider.Provides) {
		offer := provider.Provides[contractName]
		spec, known := catalog.ContractFor(contractName)
		if !known {
			return nil, fmt.Errorf("unknown contract %q", contractName)
		}
		declared := declaredValueKeys(spec, offer)
		supplied := in[contractName]
		for key := range supplied {
			if _, ok := declared[key]; !ok {
				return nil, fmt.Errorf("contract %q does not declare a value %q", contractName, key)
			}
		}
		for key, info := range declared {
			if !info.required {
				continue
			}
			if strings.TrimSpace(supplied[key]) == "" {
				return nil, fmt.Errorf("contract %q requires a value for %q", contractName, key)
			}
		}
		inner := map[string]string{}
		for key, value := range supplied {
			if _, ok := declared[key]; ok {
				inner[key] = value
			}
		}
		if len(inner) > 0 {
			out[contractName] = inner
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// validateProviderSecrets checks the operator-supplied credentials. Each
// contract that publishes a secret must be given one, for the same reason a
// required value must be: a provider that claims the contract has to be able
// to authenticate against it.
//
// keepExisting relaxes that for an update that supplies no secrets at all, on
// the reading that the client is declining to touch credentials rather than
// asserting there are none. It is scoped to "nothing named": a body that
// supplies a credential for one contract and not another is still held to the
// rule, so the exemption cannot be used to quietly drop a secret.
func validateProviderSecrets(provider *catalog.App, in map[string]string, keepExisting bool) (map[string]string, error) {
	if keepExisting && len(in) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, contractName := range sortedContractNames(provider.Provides) {
		spec, known := catalog.ContractFor(contractName)
		if !known || len(spec.Secrets) == 0 {
			continue
		}
		if strings.TrimSpace(in[contractName]) == "" {
			return nil, fmt.Errorf("contract %q requires a credential", contractName)
		}
		out[contractName] = in[contractName]
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// valueInfo is what the derivation needs to know about one declared value.
type valueInfo struct {
	required bool
}

// declaredValueKeys unions the contract's static value specs with the keys the
// provider fills at runtime, because for an external provider the operator is
// the runtime.
func declaredValueKeys(spec catalog.Contract, offer catalog.ContractProvides) map[string]valueInfo {
	out := map[string]valueInfo{}
	for _, vs := range spec.Values {
		out[vs.Key] = valueInfo{required: !vs.Optional}
	}
	for _, key := range offer.RuntimeValues {
		if _, ok := out[key]; !ok {
			out[key] = valueInfo{required: true}
		}
	}
	return out
}

func (m *externalAppsModule) installedCatalogIDs() map[string]bool {
	out := map[string]bool{}
	if m.appStore == nil {
		return out
	}
	apps, err := m.appStore.GetAll()
	if err != nil {
		return out
	}
	for _, app := range apps {
		out[app.CatalogID] = true
	}
	return out
}

// toResponse maps a stored record to the API shape, including which contracts
// have a stored credential so the form can render "set" without rendering the
// value.
func (m *externalAppsModule) toResponse(app *store.ExternalApp) externalAppResponse {
	resp := toExternalAppResponse(app)
	if kind, ref, ok := store.ParseExternalAppSource(app.Source); ok && kind == store.ExternalAppSourceKindApp {
		resp.App = ref
	}
	if m.secrets == nil || app.Kind != string(store.ExternalAppKindProvider) {
		return resp
	}
	scope := store.ExternalSecretScope(app.ID)
	for _, contract := range m.providerContracts(app) {
		if m.secrets.GetAppSecret(scope, contract) != "" {
			resp.SecretContracts = append(resp.SecretContracts, contract)
		}
	}
	return resp
}

// providerContracts lists the contracts a provider record can carry a
// credential for. It reads the catalog when the app is known, and falls back to
// the contract keys already present in the record's own values so a record
// whose catalog app has since disappeared can still be edited.
func (m *externalAppsModule) providerContracts(app *store.ExternalApp) []string {
	kind, ref, ok := store.ParseExternalAppSource(app.Source)
	if ok && kind == store.ExternalAppSourceKindApp && m.catalog != nil {
		if catalogApp, err := m.catalog.Get(ref); err == nil && catalogApp != nil && len(catalogApp.Provides) > 0 {
			return sortedContractNames(catalogApp.Provides)
		}
	}
	var out []string
	for contract := range decodeExternalValues(app.Values) {
		out = append(out, contract)
	}
	sort.Strings(out)
	return out
}

func sortedContractNames(provides catalog.Provides) []string {
	out := make([]string, 0, len(provides))
	for name := range provides {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// humanizeFieldKey turns a camelCase contract key into a form label without a
// per-app label table.
func humanizeFieldKey(key string) string {
	if key == "" {
		return key
	}
	var b strings.Builder
	b.WriteString(strings.ToUpper(key[:1]))
	for i := 1; i < len(key); i++ {
		c := key[i]
		if c >= 'A' && c <= 'Z' && i > 1 {
			b.WriteByte(' ')
		}
		b.WriteByte(c)
	}
	return b.String()
}

// responseFromSpec echoes a spec when no store is wired, so a 202 response
// stays self-contained in tests and in a runtime that has not opened the
// external-app table.
func responseFromSpec(spec orchestrator.ExternalAppSpec) externalAppResponse {
	resp := externalAppResponse{
		ID:     spec.ID,
		Kind:   spec.Kind,
		Source: spec.Source,
		Name:   spec.Name,
		URL:    spec.URL,
		Icon:   spec.Icon,
		Values: spec.Values,
	}
	if kind, ref, ok := store.ParseExternalAppSource(spec.Source); ok && kind == store.ExternalAppSourceKindApp {
		resp.App = ref
	}
	for contract := range spec.Secrets {
		resp.SecretContracts = append(resp.SecretContracts, contract)
	}
	sort.Strings(resp.SecretContracts)
	return resp
}
