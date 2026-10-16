// SPDX-License-Identifier: AGPL-3.0-only

// Package embeddings holds the shape of the `embeddings` contract: one
// OpenAI-compatible text-embedding endpoint the operator declares, stored as an
// external provider record so the Settings surface and the resolver read the
// same row rather than two stores kept in step.
//
// It mirrors the transcription package on purpose, including its size. Both are
// one endpoint with one credential and one optional model id, and neither has
// the list, the active-selection rule, or the default model the inference
// package carries. What differs is only which question the endpoint answers.
package embeddings

import (
	"encoding/json"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// ContractName is the contract an embedding endpoint fills. It is the name a
// consumer declares the integration under (`integrations.embeddings`) and the
// name a `source: contract:<name>` record carries.
const ContractName = "embeddings"

// DefaultID is the record ID for the single provider the Settings surface
// manages. A stable ID is what lets a save update the row instead of removing
// and re-adding it under a consumer that is mid-reconcile, and it is what the
// credential's `external/<id>` secret scope is derived from.
const DefaultID = "embeddings"

// ValueModel is the record's model value for this contract: the model id a
// consumer should request. Empty means the endpoint's own default stands.
const ValueModel = "model"

// Provider is the operator's declared embedding endpoint.
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

// FromExternal converts one external record, reporting whether it is an
// embeddings provider at all.
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

// FromExternals converts a registry slice, skipping everything that is not an
// embeddings provider. A transcription endpoint or a launcher in that list is
// not a malformed embedding endpoint, so a non-match is skipped, not reported.
func FromExternals(apps []*store.ExternalApp) []Provider {
	out := make([]Provider, 0, len(apps))
	for _, app := range apps {
		if p, ok := FromExternal(app); ok {
			out = append(out, p)
		}
	}
	return out
}

// Configured reports whether a provider is a real endpoint rather than the zero
// value. Every caller that asks has one question: is there something to dial.
func Configured(p Provider) bool {
	return strings.TrimSpace(p.BaseURL) != ""
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
