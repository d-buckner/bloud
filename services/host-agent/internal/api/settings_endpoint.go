// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/embeddings"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/transcription"
)

// endpointSettingsModule owns the settings surface of one single-endpoint
// contract: the OpenAI-compatible endpoint the operator declares, and the list of
// installed apps that consume it.
//
// Two contracts have this shape (`transcription` and `embeddings`) and they
// differ only in the four fields of `endpointContract` below. One module with two
// instances is what keeps the credential rule, the no-op behaviour, and the
// "declares it and is installed" answer in servedTo identical between them,
// rather than two files that drift the first time one is fixed.
type endpointSettingsModule struct {
	spec         endpointContract
	secrets      configurator.AppSecretsProvider
	appStore     store.AppStoreInterface
	catalog      catalog.CacheInterface
	orch         orchestratorCaller
	logger       *slog.Logger
	externalApps store.ExternalAppStoreInterface
}

// endpointContract is what differs between the single-endpoint surfaces: the
// contract name, the URL segment, the label used when the operator left the name
// blank, and how a stored record is read back into one shape.
type endpointContract struct {
	contract string
	path     string
	label    string
	read     func([]*store.ExternalApp) (endpointRecord, bool)
	submit   func(orchestratorCaller, string, string, string, *string)
}

// endpointRecord is the module's normalized view of a provider record. The
// credential is absent by construction: it lives in the secrets manager under the
// record's `external/<id>` scope, and a struct that could carry it would end up
// echoed into a settings response.
type endpointRecord struct {
	ID      string
	Name    string
	BaseURL string
	Model   string
}

// transcriptionContract wires the speech-to-text surface.
func transcriptionContract() endpointContract {
	return endpointContract{
		contract: transcription.ContractName,
		path:     "transcription",
		label:    "Transcription",
		read: func(records []*store.ExternalApp) (endpointRecord, bool) {
			ps := transcription.FromExternals(records)
			if len(ps) == 0 {
				return endpointRecord{}, false
			}
			return endpointRecord{ID: ps[0].ID, Name: ps[0].Name, BaseURL: ps[0].BaseURL, Model: ps[0].Model}, true
		},
		submit: func(o orchestratorCaller, base, name, model string, key *string) {
			o.Submit(orchestrator.NewSetTranscriptionIntent(base, name, model, key))
		},
	}
}

// embeddingsContract wires the text-embedding surface.
func embeddingsContract() endpointContract {
	return endpointContract{
		contract: embeddings.ContractName,
		path:     "embeddings",
		label:    "Embeddings",
		read: func(records []*store.ExternalApp) (endpointRecord, bool) {
			ps := embeddings.FromExternals(records)
			if len(ps) == 0 {
				return endpointRecord{}, false
			}
			return endpointRecord{ID: ps[0].ID, Name: ps[0].Name, BaseURL: ps[0].BaseURL, Model: ps[0].Model}, true
		},
		submit: func(o orchestratorCaller, base, name, model string, key *string) {
			o.Submit(orchestrator.NewSetEmbeddingsIntent(base, name, model, key))
		},
	}
}

func newEndpointSettingsModule(spec endpointContract, deps *routerDeps, logger *slog.Logger) *endpointSettingsModule {
	return &endpointSettingsModule{
		spec:         spec,
		secrets:      deps.secrets,
		appStore:     deps.appStore,
		catalog:      deps.catalogCache,
		orch:         deps.orchCaller,
		logger:       logger,
		externalApps: deps.externalApps,
	}
}

func newTranscriptionSettingsModule(deps *routerDeps, logger *slog.Logger) *endpointSettingsModule {
	return newEndpointSettingsModule(transcriptionContract(), deps, logger)
}

func newEmbeddingsSettingsModule(deps *routerDeps, logger *slog.Logger) *endpointSettingsModule {
	return newEndpointSettingsModule(embeddingsContract(), deps, logger)
}

// endpointSettingsResponse is the surface as the UI sees it. The credential is
// never included: only whether one is stored, same rule as the AI upstreams.
type endpointSettingsResponse struct {
	Configured bool     `json:"configured"`
	Name       string   `json:"name"`
	BaseURL    string   `json:"baseUrl"`
	Model      string   `json:"model"`
	HasAPIKey  bool     `json:"hasApiKey"`
	ServedTo   []string `json:"servedTo"`
}

// GetHandler returns the current provider for this contract.
func (m *endpointSettingsModule) GetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		provider, configured := m.read()
		resp := endpointSettingsResponse{
			Configured: configured,
			Name:       provider.Name,
			BaseURL:    provider.BaseURL,
			Model:      provider.Model,
			ServedTo:   m.servedTo(),
		}
		if configured && m.secrets != nil {
			resp.HasAPIKey = m.secrets.GetAppSecret(
				store.ExternalSecretScope(provider.ID), m.spec.contract) != ""
		}
		respondJSON(w, http.StatusOK, resp)
	}
}

// endpointSettingsRequest is the PUT body. An empty BaseURL removes the
// provider. APIKey is a pointer so omitted means "keep the stored credential"
// and an explicit empty string means "clear it".
type endpointSettingsRequest struct {
	BaseURL string  `json:"baseUrl"`
	Name    string  `json:"name"`
	Model   string  `json:"model"`
	APIKey  *string `json:"apiKey"`
}

// SetHandler validates the payload and submits the contract's intent. It never
// writes a store itself: the orchestrator is the single writer.
func (m *endpointSettingsModule) SetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req endpointSettingsRequest
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

		m.spec.submit(m.orch, base, strings.TrimSpace(req.Name), strings.TrimSpace(req.Model), req.APIKey)

		respondJSON(w, http.StatusAccepted, map[string]any{
			"status":     "accepted",
			"baseUrl":    base,
			"model":      strings.TrimSpace(req.Model),
			"configured": base != "",
		})
	}
}

// discoverModelsHandler dials the candidate endpoint's /models and returns what
// it found, so the model picker offers the endpoint's real list rather than a
// list Bloud maintains.
//
// This is a probe, and like the AI endpoint check it is allowed: nothing here
// prunes anything and convergence never consults it (invariant 15 forbids the
// reconciler probing a provider's port to decide whether it is installed, which
// is a different question asked for a different reason).
func (m *endpointSettingsModule) discoverModelsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req endpointSettingsRequest
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
func (m *endpointSettingsModule) read() (endpointRecord, bool) {
	if m.externalApps == nil {
		return endpointRecord{}, false
	}
	records, err := m.externalApps.FindAllBySource(store.ExternalAppSourceForContract(m.spec.contract))
	if err != nil {
		m.logger.Warn("could not read an endpoint provider", "contract", m.spec.contract, "error", err)
		return endpointRecord{}, false
	}
	return m.spec.read(records)
}

// servedTo names each installed app that declares this contract. It reads the
// catalog rather than the resolver: unlike the AI surface there is no serving
// path to distinguish, so "declares it and is installed" is the whole answer, and
// the UI must not imply a wiring the app does not have.
func (m *endpointSettingsModule) servedTo() []string {
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
		if _, declares := catalogApp.Integrations[m.spec.contract]; declares {
			out = append(out, app.CatalogID)
		}
	}
	return out
}

// RegisterTranscriptionRoutes mounts the transcription settings surface on an
// admin router.
func RegisterTranscriptionRoutes(mod *endpointSettingsModule, r chi.Router) {
	registerEndpointRoutes(mod, r)
}

// RegisterEmbeddingsRoutes mounts the embeddings settings surface on an admin
// router.
func RegisterEmbeddingsRoutes(mod *endpointSettingsModule, r chi.Router) {
	registerEndpointRoutes(mod, r)
}

func registerEndpointRoutes(mod *endpointSettingsModule, r chi.Router) {
	base := "/settings/" + mod.spec.path
	r.Get(base, mod.GetHandler())
	r.Put(base, mod.SetHandler())
	r.Post(base+"/models", mod.discoverModelsHandler())
}
