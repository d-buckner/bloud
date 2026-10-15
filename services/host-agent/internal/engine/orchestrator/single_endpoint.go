// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/embeddings"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/transcription"
)

// singleEndpoint names the three identifiers one single-endpoint contract uses,
// plus how its record is rendered.
//
// Two contracts have this shape today (`transcription` and `embeddings`) and
// they differ in nothing but these five fields: one endpoint the operator types,
// one bearer, one optional model id, stored as one external record with one
// credential. Writing the apply path once is what keeps a fix to the no-op guard
// or the credential cleanup from landing in one of them and not the other, which
// is the failure mode two copies of ninety lines always reach.
type singleEndpoint struct {
	// contract is the contract name, the record's `contract:<name>` source, and
	// the secret key the credential is stored under.
	contract string
	// defaultID is the stable record ID the Settings surface manages.
	defaultID string
	// defaultName labels the record when the operator left the name blank.
	defaultName string
	// record renders the external record, so the contract's own package stays
	// the only place its value keys are written down.
	record func(id, name, baseURL, model string) *store.ExternalApp
	// provider reads one record back, so the read path and the write path agree
	// on what counts as this contract's provider.
	provider func(*store.ExternalApp) (endpointValues, bool)
}

// endpointValues is the comparison form of a single-endpoint provider: enough to
// decide whether a save changed anything, and never carrying the credential.
type endpointValues struct {
	ID      string
	Name    string
	BaseURL string
	Model   string
}

var (
	transcriptionEndpoint = singleEndpoint{
		contract:    transcription.ContractName,
		defaultID:   transcription.DefaultID,
		defaultName: "Transcription",
		record: func(id, name, baseURL, model string) *store.ExternalApp {
			return transcription.ToExternal(transcription.Provider{
				ID: id, Name: name, BaseURL: baseURL, Model: model,
			})
		},
		provider: readTranscription,
	}
	embeddingsEndpoint = singleEndpoint{
		contract:    embeddings.ContractName,
		defaultID:   embeddings.DefaultID,
		defaultName: "Embeddings",
		record: func(id, name, baseURL, model string) *store.ExternalApp {
			return embeddings.ToExternal(embeddings.Provider{
				ID: id, Name: name, BaseURL: baseURL, Model: model,
			})
		},
		provider: readEmbeddings,
	}
)

func readTranscription(rec *store.ExternalApp) (endpointValues, bool) {
	p, ok := transcription.FromExternal(rec)
	if !ok {
		return endpointValues{}, false
	}
	return endpointValues{ID: p.ID, Name: p.Name, BaseURL: p.BaseURL, Model: p.Model}, true
}

func readEmbeddings(rec *store.ExternalApp) (endpointValues, bool) {
	p, ok := embeddings.FromExternal(rec)
	if !ok {
		return endpointValues{}, false
	}
	return endpointValues{ID: p.ID, Name: p.Name, BaseURL: p.BaseURL, Model: p.Model}, true
}

// endpointIntent is the payload a single-endpoint contract accepts. APIKey is a
// *string so the API can distinguish "leave the stored credential alone" (nil)
// from "clear it" (a pointer to ""), and it travels here rather than being
// written by the handler so the secrets manager is only ever touched by the
// orchestrator (invariant 1).
type endpointIntent struct {
	BaseURL string
	Name    string
	Model   string
	APIKey  *string
}

// applySetEndpointIntent makes the instance's provider for one contract the
// endpoint the operator just described, then resets every consumer of that
// contract so their configurators re-run against it.
//
// The no-op guard compares the parsed endpoint rather than the request, so a save
// that only reformatted the same address (`https://x/v1/` against
// `https://x/v1`) does not restart every wired app. The credential is excluded
// from the comparison for the same reason inference excludes it: rewriting the
// same key is not a change an app needs to hear about, and not sending one is
// not a reason to skip storing it.
func (o *Orchestrator) applySetEndpointIntent(ep singleEndpoint, in endpointIntent) {
	if o.externalApps == nil || o.secrets == nil {
		o.logger.Error("cannot apply endpoint settings: the external app or secrets store is missing",
			"contract", ep.contract)
		return
	}

	existing := o.endpointProviders(ep)
	wanted, ok := o.validateEndpoint(ep, in)
	if !ok {
		return
	}
	unchanged := len(existing) == 1 && existing[0] == wanted

	if err := o.syncEndpointProvider(ep, wanted); err != nil {
		o.logger.Error("failed to persist endpoint settings", "contract", ep.contract, "error", err)
		return
	}
	if in.APIKey != nil {
		if err := o.secrets.SetAppSecret(store.ExternalSecretScope(wanted.ID), ep.contract, *in.APIKey); err != nil {
			o.logger.Error("failed to store an endpoint credential",
				"contract", ep.contract, "id", wanted.ID, "error", err)
		}
	}

	if unchanged {
		o.logger.Info("endpoint settings unchanged, skipping side effects", "contract", ep.contract)
		return
	}
	if wanted.BaseURL == "" {
		o.logger.Info("endpoint provider removed", "contract", ep.contract)
	} else {
		o.logger.Info("applied endpoint settings",
			"contract", ep.contract, "endpoint", wanted.BaseURL, "model", wanted.Model)
	}
	o.resetContractConsumers(ep.contract)
}

// endpointProviders reads back the records that fill this contract.
func (o *Orchestrator) endpointProviders(ep singleEndpoint) []endpointValues {
	out := []endpointValues{}
	for _, rec := range o.loadExternalRegistry().externalForContract(ep.contract) {
		if v, ok := ep.provider(rec); ok {
			out = append(out, v)
		}
	}
	return out
}

// validateEndpoint turns the request into the provider that should exist. An
// empty address is not a validation failure: it is the operator removing the
// provider, which renders as the zero value and drives the delete in the sync
// below.
//
// The endpoint is parsed before anything is written so a stored value that does
// not parse is diagnosed at the save that caused it rather than at every
// convergence pass afterwards.
func (o *Orchestrator) validateEndpoint(ep singleEndpoint, in endpointIntent) (endpointValues, bool) {
	base := strings.TrimSpace(in.BaseURL)
	if base == "" {
		return endpointValues{}, true
	}
	parsed, err := inference.ParseEndpoint(base)
	if err != nil {
		o.logger.Error("rejected endpoint settings", "contract", ep.contract, "error", err)
		return endpointValues{}, false
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = ep.defaultName
	}
	return endpointValues{
		ID:      ep.defaultID,
		Name:    name,
		BaseURL: parsed.String(),
		Model:   strings.TrimSpace(in.Model),
	}, true
}

// syncEndpointProvider makes the records that fill this contract match the one
// provider the operator described: upsert it when there is one, and delete every
// record that is not it, credential included.
//
// Deleting the secret with the record is the point. A key left behind under
// `external/<id>` after the row is gone is a credential nothing owns and nothing
// rotates, and the next provider that happens to reuse the ID would inherit it.
func (o *Orchestrator) syncEndpointProvider(ep singleEndpoint, want endpointValues) error {
	for _, rec := range o.loadExternalRegistry().externalForContract(ep.contract) {
		if want.BaseURL != "" && rec.ID == want.ID {
			continue
		}
		if err := o.externalApps.Delete(rec.ID); err != nil {
			return err
		}
		if err := o.secrets.DeleteAppSecrets(store.ExternalSecretScope(rec.ID)); err != nil {
			o.logger.Warn("could not clear the credential of a removed endpoint provider",
				"contract", ep.contract, "id", rec.ID, "error", err)
		}
	}
	if want.BaseURL == "" {
		return nil
	}
	return o.externalApps.Upsert(ep.record(want.ID, want.Name, want.BaseURL, want.Model))
}

// applySetTranscriptionIntent is the transcription arm of the single-writer
// queue: it carries the contract's identity to the shared apply path.
func (o *Orchestrator) applySetTranscriptionIntent(intent SetTranscriptionIntent) {
	o.applySetEndpointIntent(transcriptionEndpoint, endpointIntent{
		BaseURL: intent.BaseURL,
		Name:    intent.Name,
		Model:   intent.Model,
		APIKey:  intent.APIKey,
	})
}

// applySetEmbeddingsIntent is the embeddings arm of the single-writer queue.
func (o *Orchestrator) applySetEmbeddingsIntent(intent SetEmbeddingsIntent) {
	o.applySetEndpointIntent(embeddingsEndpoint, endpointIntent{
		BaseURL: intent.BaseURL,
		Name:    intent.Name,
		Model:   intent.Model,
		APIKey:  intent.APIKey,
	})
}
