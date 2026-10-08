// SPDX-License-Identifier: AGPL-3.0-only

package inference

import (
	"encoding/json"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// ContractName is the contract an AI upstream fills. It is the name a consumer
// declares the integration under (`integrations.inference`), so it is also
// the name a `source: contract:<name>` record carries.
const ContractName = "inference"

// Values keys of an AI upstream stored as an external provider record.
//
// The record's values column is a flat string map per contract, so the two
// non-URL facts about an upstream have to be encoded rather than typed. They
// are deliberately boring: a newline-joined model list and a "true" flag.
// Both are display-and-selection state rather than authority, so a value that
// does not parse degrades to empty instead of to an error.
const (
	// ValueModels holds the last discovered model list, newline-joined.
	ValueModels = "models"
	// ValueEnabled holds "true" when the upstream is the active one.
	ValueEnabled = "enabled"
)

// UpstreamsFromExternal converts the external provider records that fill the
// inference contract into the upstream list the Settings surface and the
// resolver work with.
//
// Records that are not `contract:inference` are skipped rather than reported:
// the caller hands over the whole registry, and a launcher or a remote AFFiNE
// in that list is not a malformed AI upstream.
func UpstreamsFromExternal(apps []*store.ExternalApp) []Upstream {
	out := make([]Upstream, 0, len(apps))
	for _, app := range apps {
		if u, ok := UpstreamFromExternal(app); ok {
			out = append(out, u)
		}
	}
	return out
}

// UpstreamFromExternal converts one record, reporting whether it is an AI
// upstream at all.
func UpstreamFromExternal(app *store.ExternalApp) (Upstream, bool) {
	if app == nil {
		return Upstream{}, false
	}
	kind, ref, ok := store.ParseExternalAppSource(app.Source)
	if !ok || kind != store.ExternalAppSourceKindContract || ref != ContractName {
		return Upstream{}, false
	}
	return Upstream{
		ID:      app.ID,
		Name:    app.Name,
		BaseURL: app.URL,
		Models:  splitModels(app.Value(ContractName, ValueModels)),
		Enabled: app.Value(ContractName, ValueEnabled) == "true",
	}, true
}

// ExternalForUpstream renders the reverse: the record an upstream is stored
// as.
//
// The upstream's own stable ID becomes the record ID, which is what keeps a
// rename from reading as remove-plus-add to a consumer that is mid-reconcile.
// The credential is deliberately absent: it goes to the secrets manager under
// the record's `external/<id>` scope, never into this row.
func ExternalForUpstream(u Upstream) *store.ExternalApp {
	return &store.ExternalApp{
		ID:     u.ID,
		Kind:   string(store.ExternalAppKindProvider),
		Source: store.ExternalAppSourceForContract(ContractName),
		Name:   u.Name,
		URL:    u.BaseURL,
		Values: encodeUpstreamValues(u),
	}
}

// encodeUpstreamValues renders the two non-URL facts as the record's values
// JSON, omitting whichever is unset so an upstream with no discovered models
// and no active flag stores `{}` rather than a row of empty strings.
func encodeUpstreamValues(u Upstream) string {
	fields := map[string]string{}
	if u.Enabled {
		fields[ValueEnabled] = "true"
	}
	if len(u.Models) > 0 {
		fields[ValueModels] = strings.Join(u.Models, "\n")
	}
	if len(fields) == 0 {
		return "{}"
	}
	contract := map[string]any{ContractName: fields}
	b, err := json.Marshal(contract)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// splitModels turns the stored newline-joined list back into a slice, dropping
// blanks so a trailing newline does not become a phantom model.
func splitModels(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
