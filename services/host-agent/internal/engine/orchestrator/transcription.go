// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/transcription"
)

// applySetTranscriptionIntent makes the instance's speech-to-text endpoint the
// one the operator just described, then resets every consumer of the contract so
// their configurators re-run against it.
//
// The no-op guard compares the parsed endpoint rather than the request, so a save
// that only reformatted the same address (`https://x/v1/` against
// `https://x/v1`) does not restart every wired app. The credential is excluded
// from the comparison for the same reason inference excludes it: rewriting the
// same key is not a change an app needs to hear about, and not sending one is
// not a reason to skip storing it.
func (o *Orchestrator) applySetTranscriptionIntent(intent SetTranscriptionIntent) {
	if o.externalApps == nil || o.secrets == nil {
		o.logger.Error("cannot apply transcription settings: the external app or secrets store is missing")
		return
	}

	existing := transcription.FromExternals(o.loadExternalRegistry().externalForContract(transcription.ContractName))
	wanted, ok := o.validateTranscription(intent)
	if !ok {
		return
	}

	unchanged := len(existing) == 1 && existing[0] == wanted

	if err := o.syncTranscriptionProvider(wanted); err != nil {
		o.logger.Error("failed to persist transcription settings", "error", err)
		return
	}
	if intent.APIKey != nil {
		if err := o.secrets.SetAppSecret(store.ExternalSecretScope(wanted.ID), transcription.ContractName, *intent.APIKey); err != nil {
			o.logger.Error("failed to store the transcription API key", "id", wanted.ID, "error", err)
		}
	}

	if unchanged {
		o.logger.Info("transcription settings unchanged, skipping side effects")
		return
	}
	if wanted.BaseURL == "" {
		o.logger.Info("transcription provider removed")
	} else {
		o.logger.Info("applied transcription settings", "endpoint", wanted.BaseURL, "model", wanted.Model)
	}
	o.resetContractConsumers(transcription.ContractName)
}

// validateTranscription turns the request into the provider that should exist.
// An empty address is not a validation failure: it is the operator removing the
// provider, which renders as the zero Provider and drives the delete in the
// sync below.
//
// The endpoint is parsed before anything is written so a stored value that does
// not parse is diagnosed at the save that caused it rather than at every
// convergence pass afterwards.
func (o *Orchestrator) validateTranscription(intent SetTranscriptionIntent) (transcription.Provider, bool) {
	base := strings.TrimSpace(intent.BaseURL)
	if base == "" {
		return transcription.Provider{}, true
	}
	ep, err := inference.ParseEndpoint(base)
	if err != nil {
		o.logger.Error("rejected transcription settings", "error", err)
		return transcription.Provider{}, false
	}
	name := strings.TrimSpace(intent.Name)
	if name == "" {
		name = "Transcription"
	}
	return transcription.Provider{
		ID:      transcription.DefaultID,
		Name:    name,
		BaseURL: ep.String(),
		Model:   strings.TrimSpace(intent.Model),
	}, true
}

// syncTranscriptionProvider makes the `contract:transcription` records match
// the one provider the operator described: upsert it when there is one, and
// delete every record that is not it, credential included.
//
// Deleting the secret with the record is the point. A key left behind under
// `external/<id>` after the row is gone is a credential nothing owns and nothing
// rotates, and the next provider that happens to reuse the ID would inherit it.
func (o *Orchestrator) syncTranscriptionProvider(want transcription.Provider) error {
	for _, rec := range o.loadExternalRegistry().externalForContract(transcription.ContractName) {
		if want.BaseURL != "" && rec.ID == want.ID {
			continue
		}
		if err := o.externalApps.Delete(rec.ID); err != nil {
			return err
		}
		if err := o.secrets.DeleteAppSecrets(store.ExternalSecretScope(rec.ID)); err != nil {
			o.logger.Warn("could not clear the credential of a removed transcription provider",
				"id", rec.ID, "error", err)
		}
	}
	if want.BaseURL == "" {
		return nil
	}
	return o.externalApps.Upsert(transcription.ToExternal(want))
}
