// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/transcription"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// transcriptionSettingsModule owns the speech-to-text settings surface: one
// OpenAI-compatible transcription endpoint the operator declares, and the list
// of installed apps that consume it.
//
// It is its own module rather than more handlers on aiSettingsModule because it
// is a different contract with a different shape: one endpoint with no
// active-selection rule and no default model shared with anything else. Folding
// it into the AI module would have two contracts' request types and two
// credentials sharing one file and one name.
type transcriptionSettingsModule struct {
	secrets      configurator.AppSecretsProvider
	appStore     store.AppStoreInterface
	catalog      catalog.CacheInterface
	orch         orchestratorCaller
	logger       *slog.Logger
	externalApps store.ExternalAppStoreInterface
}

// newTranscriptionSettingsModule wires the module from the router's shared
// dependencies. It reads the same three stores the AI module reads: the record
// lives beside the external app records, the credential beside theirs, and the
// orchestrator is what writes both.
func newTranscriptionSettingsModule(deps *routerDeps, logger *slog.Logger) *transcriptionSettingsModule {
	return &transcriptionSettingsModule{
		secrets:      deps.secrets,
		appStore:     deps.appStore,
		catalog:      deps.catalogCache,
		orch:         deps.orchCaller,
		logger:       logger,
		externalApps: deps.externalApps,
	}
}

// transcriptionSettingsResponse is the surface as the UI sees it. The credential
// is never included: only whether one is stored, same rule as the AI upstreams.
type transcriptionSettingsResponse struct {
	Configured bool     `json:"configured"`
	Name       string   `json:"name"`
	BaseURL    string   `json:"baseUrl"`
	Model      string   `json:"model"`
	HasAPIKey  bool     `json:"hasApiKey"`
	ServedTo   []string `json:"servedTo"`
}

// GetTranscriptionHandler returns the current transcription provider.
func (m *transcriptionSettingsModule) GetTranscriptionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider, configured := m.read()
		resp := transcriptionSettingsResponse{
			Configured: configured,
			Name:       provider.Name,
			BaseURL:    provider.BaseURL,
			Model:      provider.Model,
			ServedTo:   m.servedTo(),
		}
		if configured && m.secrets != nil {
			resp.HasAPIKey = m.secrets.GetAppSecret(
				store.ExternalSecretScope(provider.ID), transcription.ContractName) != ""
		}
		respondJSON(w, http.StatusOK, resp)
	}
}

// setTranscriptionRequest is the PUT body. An empty BaseURL removes the
// provider. APIKey is a pointer so omitted means "keep the stored credential"
// and an explicit empty string means "clear it".
type setTranscriptionRequest struct {
	BaseURL string  `json:"baseUrl"`
	Name    string  `json:"name"`
	Model   string  `json:"model"`
	APIKey  *string `json:"apiKey"`
}

// SetTranscriptionHandler validates the payload and submits a
// SetTranscriptionIntent. It never writes a store itself: the orchestrator is
// the single writer.
func (m *transcriptionSettingsModule) SetTranscriptionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req setTranscriptionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		base := strings.TrimSpace(req.BaseURL)
		if base != "" {
			ep, err := inference.ParseEndpoint(base)
			if err != nil {
				respondError(w, http.StatusBadRequest, err.Error())
				return
			}
			base = ep.String()
		}

		m.orch.Submit(orchestrator.NewSetTranscriptionIntent(
			base, strings.TrimSpace(req.Name), strings.TrimSpace(req.Model), req.APIKey))

		respondJSON(w, http.StatusAccepted, map[string]any{
			"status":     "accepted",
			"baseUrl":    base,
			"model":      strings.TrimSpace(req.Model),
			"configured": base != "",
		})
	}
}

// discoverTranscriptionModelsHandler dials the candidate endpoint's /models and
// returns what it found, so the model picker offers the endpoint's real list
// rather than a list Bloud maintains.
//
// This is a probe, and like the AI endpoint check it is allowed: nothing here
// prunes anything and convergence never consults it (invariant 15 forbids the
// reconciler probing a provider's port to decide whether it is installed, which
// is a different question asked for a different reason).
func (m *transcriptionSettingsModule) discoverTranscriptionModelsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req setTranscriptionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		ep, err := inference.ParseEndpoint(req.BaseURL)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		models, err := fetchModels(ep, derefOrEmpty(req.APIKey))
		if err != nil {
			respondJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"ok": true, "models": models})
	}
}

// read returns the configured provider, if any. More than one record can only
// come from a hand-edited registry; the first is what the surface shows and what
// consumers bind, and the next save collapses the set back to one.
func (m *transcriptionSettingsModule) read() (transcription.Provider, bool) {
	if m.externalApps == nil {
		return transcription.Provider{}, false
	}
	records, err := m.externalApps.FindAllBySource(store.ExternalAppSourceForContract(transcription.ContractName))
	if err != nil {
		m.logger.Warn("could not read the transcription provider", "error", err)
		return transcription.Provider{}, false
	}
	providers := transcription.FromExternals(records)
	if len(providers) == 0 {
		return transcription.Provider{}, false
	}
	return providers[0], true
}

// servedTo names each installed app that declares the transcription contract.
// It reads the catalog rather than the resolver: unlike the AI surface there is
// no serving path to distinguish, so "declares it and is installed" is the whole
// answer, and the UI must not imply a wiring the app does not have.
func (m *transcriptionSettingsModule) servedTo() []string {
	out := []string{}
	if m.appStore == nil || m.catalog == nil {
		return out
	}
	apps, err := m.appStore.GetAll()
	if err != nil {
		return out
	}
	for _, app := range apps {
		if app.IsSystem {
			continue
		}
		catalogApp, err := m.catalog.Get(app.CatalogID)
		if err != nil || catalogApp == nil {
			continue
		}
		if _, declares := catalogApp.Integrations[transcription.ContractName]; declares {
			out = append(out, app.CatalogID)
		}
	}
	return out
}

// RegisterTranscriptionRoutes mounts the transcription settings surface on an
// admin router.
func RegisterTranscriptionRoutes(mod *transcriptionSettingsModule, r chi.Router) {
	r.Get("/settings/transcription", mod.GetTranscriptionHandler())
	r.Put("/settings/transcription", mod.SetTranscriptionHandler())
	r.Post("/settings/transcription/models", mod.discoverTranscriptionModelsHandler())
}
