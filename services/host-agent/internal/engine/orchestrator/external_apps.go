// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"encoding/json"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// applyAddExternalAppIntent persists a new external app and its credentials.
//
// That is the whole effect. There is no container to build, no route to emit,
// and no SSO client to mint, because the operator is telling Bloud about
// something Bloud does not run. Keeping the applier this small is the
// load-bearing filter from docs/plans/external-apps.md: the convergence layer
// is never handed an external record, so it can never expand the catalog app's
// `containers:` by accident.
func (o *Orchestrator) applyAddExternalAppIntent(intent AddExternalAppIntent) {
	o.upsertExternalApp(intent.Spec)
}

// applyUpdateExternalAppIntent changes an external app. It reads the stored row
// first so an update never resurrects a row that was removed between submit and
// drain, and so a provider's kind and source cannot be rewritten into a
// different shape by a partial edit.
func (o *Orchestrator) applyUpdateExternalAppIntent(intent UpdateExternalAppIntent) {
	if o.externalApps == nil {
		return
	}
	existing, err := o.externalApps.Get(intent.Spec.ID)
	if err != nil || existing == nil {
		o.logger.Warn("cannot update external app", "id", intent.Spec.ID, "error", err)
		return
	}
	spec := intent.Spec
	spec.Kind = existing.Kind
	spec.Source = existing.Source
	// A PATCH replaces only what it names. The values column is the record's
	// non-secret contract payload, and a consumer's binding is built from those
	// keys, so an edit that only renames the app or moves its endpoint must not
	// blank the payload: a wiped payload silently un-resolves the binding and
	// leaves the consumer writing an unauthenticated config against a provider
	// that is otherwise perfectly wired. Clearing a provider's values is not an
	// operation this surface offers; you remove the record and add it again.
	if len(spec.Values) == 0 {
		spec.Values = decodeExternalValues(existing.Values)
	}
	o.upsertExternalApp(spec)
}

// applyRemoveExternalAppIntent deletes an external app and the credentials it
// owned in the external scope.
//
// The credentials go with the record, and the record is read before it is
// deleted because after that there is nothing left to say which scope was
// theirs. Leaving a live secret keyed to an id nothing references, which a
// later record that reused the id would inherit, is the failure mode.
func (o *Orchestrator) applyRemoveExternalAppIntent(intent RemoveExternalAppIntent) {
	if o.externalApps == nil {
		return
	}
	existing, err := o.externalApps.Get(intent.ID)
	if err != nil {
		o.logger.Error("failed to read external app for removal", "id", intent.ID, "error", err)
		return
	}
	if err := o.externalApps.Delete(intent.ID); err != nil {
		o.logger.Error("failed to remove external app", "id", intent.ID, "error", err)
		return
	}
	if existing != nil {
		o.clearExternalSecrets(existing)
	}
}

// externalSecretCleaner is the optional half of a secrets provider that can
// drop a whole scope at once. The orchestrator holds the narrow
// AppSecretsProvider, so the delete capability is asserted rather than added
// to the shared interface every fake and every configurator-facing consumer
// would have to carry.
type externalSecretCleaner interface {
	DeleteAppSecrets(appName string) error
}

// upsertExternalApp writes the record and then its credentials, in that order.
//
// The record lands first so a consumer's next pass never sees a credential
// whose owning record does not exist; the reverse order could hand a consumer
// a provider whose endpoint the resolver could not look up.
func (o *Orchestrator) upsertExternalApp(spec ExternalAppSpec) {
	if o.externalApps == nil {
		return
	}
	values, err := encodeExternalValues(spec.Values)
	if err != nil {
		o.logger.Error("failed to encode external app values", "id", spec.ID, "error", err)
		return
	}
	app := &store.ExternalApp{
		ID:     spec.ID,
		Kind:   spec.Kind,
		Source: spec.Source,
		Name:   spec.Name,
		URL:    spec.URL,
		Icon:   spec.Icon,
		Values: values,
	}
	if err := o.externalApps.Upsert(app); err != nil {
		o.logger.Error("failed to add external app", "id", spec.ID, "error", err)
		return
	}
	o.storeExternalSecrets(spec)
}

// storeExternalSecrets writes each operator-supplied credential into the
// external scope, keyed by the contract it satisfies.
//
// Keyed by contract rather than by the registry's secret name because two
// contracts can name the same secret (`apiKey` is the credential for `pvr`,
// `icsFeed`, `inference`, and `agentApi`) while a given provider means two
// different things by it. The contract is the unambiguous key, and the
// registry's name is still what gates whether a consumer may read it.
func (o *Orchestrator) storeExternalSecrets(spec ExternalAppSpec) {
	if o.secrets == nil || len(spec.Secrets) == 0 {
		return
	}
	scope := store.ExternalSecretScope(spec.ID)
	for contract, value := range spec.Secrets {
		if err := o.secrets.SetAppSecret(scope, contract, value); err != nil {
			o.logger.Error("failed to store external app credential",
				"id", spec.ID, "contract", contract, "error", err)
		}
	}
}

// clearExternalSecrets removes every credential the deleted record's external
// scope holds. The scope is namespaced by id and only that record ever wrote to
// it, so dropping the whole scope cannot reach anything else.
//
// A provider without the delete capability falls back to blanking each contract
// key the record named, so removal still stops the credential from resolving on
// a store that can only write.
func (o *Orchestrator) clearExternalSecrets(app *store.ExternalApp) {
	if o.secrets == nil {
		return
	}
	scope := store.ExternalSecretScope(app.ID)
	if cleaner, ok := o.secrets.(externalSecretCleaner); ok {
		if err := cleaner.DeleteAppSecrets(scope); err != nil {
			o.logger.Error("failed to clear external app credentials", "id", app.ID, "error", err)
		}
		return
	}
	for _, contract := range o.externalContractKeys(app) {
		if err := o.secrets.SetAppSecret(scope, contract, ""); err != nil {
			o.logger.Warn("failed to clear external app credential",
				"id", app.ID, "contract", contract, "error", err)
		}
	}
}

// externalContractKeys enumerates the contract names an external record could
// have written credentials under: the contracts its stored values name, plus,
// for an `app:` source, the contracts the catalog entry provides. The union is
// needed because a provider registered with credentials and no non-secret
// values leaves the values column empty, and the catalog side is the only
// remaining record of which contracts were offered.
func (o *Orchestrator) externalContractKeys(app *store.ExternalApp) []string {
	seen := map[string]bool{}
	var out []string
	add := func(contract string) {
		if contract != "" && !seen[contract] {
			seen[contract] = true
			out = append(out, contract)
		}
	}
	for _, contract := range externalValueContracts(app.Values) {
		add(contract)
	}
	if kind, ref, ok := store.ParseExternalAppSource(app.Source); ok && kind == store.ExternalAppSourceKindApp && o.catalog != nil {
		if catalogApp, err := o.catalog.Get(ref); err == nil && catalogApp != nil {
			for contract := range catalogApp.Provides {
				add(contract)
			}
		}
	}
	return out
}

// externalValueContracts returns the top-level contract keys of a stored values
// JSON object. An unreadable body yields nothing, which means a corrupt record
// leaves its credentials in place rather than silently orphaning them under
// keys nobody can enumerate.
func externalValueContracts(values string) []string {
	if values == "" {
		return nil
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(values), &parsed); err != nil {
		return nil
	}
	out := make([]string, 0, len(parsed))
	for k := range parsed {
		out = append(out, k)
	}
	return out
}

// decodeExternalValues parses the stored values column back into the
// per-contract map. Anything that will not parse reads as empty: the column is
// written by encodeExternalValues, so a value that fails here was not written
// by us, and treating it as absent is the only safe reading.
func decodeExternalValues(values string) map[string]map[string]string {
	var parsed map[string]map[string]string
	if err := json.Unmarshal([]byte(values), &parsed); err != nil {
		return nil
	}
	return parsed
}

// encodeExternalValues renders the per-contract value map for storage, or "{}"
// when there is nothing to store, so the column is never an empty string that a
// later reader has to special-case.
func encodeExternalValues(values map[string]map[string]string) (string, error) {
	if len(values) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// remoteProviderFor reports whether the operator registered catalogID as a
// remote install rather than running it here.
//
// It is the install side of the XOR rule in docs/plans/external-apps.md: a
// catalog ID has one instantiation. A consumer whose required contract is
// already answered by an external record must not drag the local workload in
// behind it, because a `multi: false` consumer facing two bindings of the same
// catalog ID has no way to tell them apart.
//
// A store failure reads as "not remote", which is the conservative answer: it
// leaves the pre-existing install behavior intact rather than silently
// suppressing a provider the registry could not be consulted about.
func (o *Orchestrator) remoteProviderFor(catalogID string) bool {
	if o.externalApps == nil || catalogID == "" {
		return false
	}
	record, err := o.externalApps.FindBySource(store.ExternalAppSourceForApp(catalogID))
	if err != nil {
		o.logger.Warn("could not consult the external registry before installing a provider",
			"provider", catalogID, "error", err)
		return false
	}
	return record != nil
}
