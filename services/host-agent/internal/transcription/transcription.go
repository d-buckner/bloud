// SPDX-License-Identifier: AGPL-3.0-only

// Package transcription holds the shape of the `transcription` contract: one
// OpenAI-compatible speech-to-text endpoint the operator declares, stored as an
// external provider record so the Settings surface and the resolver read the
// same row rather than two stores kept in step.
//
// It is deliberately smaller than the inference package. Inference carries a
// list of upstreams, an active-selection rule, a default model, and promotion
// from a modelSource, because a completions endpoint sits under half the
// catalog. Transcription has one endpoint, no promotion (an inference upstream
// is not known to transcribe, which is the entire reason the contract exists),
// and a model id that only ever names a choice among that endpoint's own models.
package transcription

import (
	"encoding/json"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// ContractName is the contract a transcription endpoint fills. It is the name a
// consumer declares the integration under (`integrations.transcription`) and the
// name a `source: contract:<name>` record carries.
const ContractName = "transcription"

// DefaultID is the record ID for the single provider the Settings surface
// manages. A stable ID is what lets a save update the row instead of removing
// and re-adding it under a consumer that is mid-reconcile, and it is what the
// credential's `external/<id>` secret scope is derived from.
const DefaultID = "transcription"

// ValueModel is the record's model value for this contract: the model id a
// consumer should request. Empty means the endpoint's own default stands.
const ValueModel = "model"

// Provider is the operator's declared transcription endpoint.
//
// The credential is absent by construction: it lives in the secrets manager
// under the record's `external/<id>` scope, and a struct that could carry it
// would end up echoed into a settings response.
type Provider struct {
	// ID is the external record's stable ID.
	ID string `json:"id"`
	// Name is the operator-facing label.
	Name string `json:"name"`
	// BaseURL is the OpenAI-compatible endpoint as entered, path included.
	BaseURL string `json:"baseUrl"`
	// Model is the model id to request, empty for the endpoint's default.
	Model string `json:"model,omitempty"`
}

// FromExternal converts one external record, reporting whether it is a
// transcription provider at all.
func FromExternal(app *store.ExternalApp) (Provider, bool) {
	if app == nil {
		return Provider{}, false
	}
	kind, ref, ok := store.ParseExternalAppSource(app.Source)
	if !ok || kind != store.ExternalAppSourceKindContract || ref != ContractName {
		return Provider{}, false
	}
	return Provider{
		ID:      app.ID,
		Name:    app.Name,
		BaseURL: app.URL,
		Model:   app.Value(ContractName, ValueModel),
	}, true
}

// FromExternals converts a registry slice, skipping everything that is not a
// transcription provider. A launcher or a remote AFFiNE in that list is not a
// malformed transcription endpoint, so a non-match is skipped, not reported.
func FromExternals(apps []*store.ExternalApp) []Provider {
	out := make([]Provider, 0, len(apps))
	for _, app := range apps {
		if p, ok := FromExternal(app); ok {
			out = append(out, p)
		}
	}
	return out
}

// ToExternal renders the reverse: the record a provider is stored as.
func ToExternal(p Provider) *store.ExternalApp {
	fields := map[string]string{}
	if strings.TrimSpace(p.Model) != "" {
		fields[ValueModel] = strings.TrimSpace(p.Model)
	}
	encoded := "{}"
	if len(fields) > 0 {
		b, err := json.Marshal(map[string]any{ContractName: fields})
		if err == nil {
			encoded = string(b)
		}
	}
	return &store.ExternalApp{
		ID:     p.ID,
		Kind:   string(store.ExternalAppKindProvider),
		Source: store.ExternalAppSourceForContract(ContractName),
		Name:   p.Name,
		URL:    p.BaseURL,
		Values: encoded,
	}
}
