// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// aiSettingsModule owns the AI settings surface: the upstream list, the instance
// default model, and the operator-initiated endpoint check.
//
// It is a separate module from settingsModule rather than a few more handlers on
// it, because it reads a different set of collaborators (the secrets manager and
// the catalog, for the "served to" view) and its own request/response types.
type aiSettingsModule struct {
	settingsStore store.SettingsStoreInterface
	secrets       configurator.AppSecretsProvider
	appStore      store.AppStoreInterface
	catalog       catalog.CacheInterface
	orch          orchestratorCaller
	logger        *slog.Logger
}

// aiUpstreamResponse is one upstream as the UI sees it. The credential is never
// included: only whether one is stored.
type aiUpstreamResponse struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	BaseURL string   `json:"baseUrl"`
	Models  []string `json:"models,omitempty"`
	Enabled bool     `json:"enabled"`
}

// aiServedToResponse names a wired consumer and how it reaches inference.
type aiServedToResponse struct {
	App   string `json:"app"`
	Via   string `json:"via"`
	Model string `json:"model,omitempty"`
}

type aiSettingsResponse struct {
	Upstreams    []aiUpstreamResponse `json:"upstreams"`
	DefaultModel string               `json:"defaultModel"`
	HasAPIKey    bool                 `json:"hasApiKey"`
	ServedTo     []aiServedToResponse `json:"servedTo"`
}

// GetAIHandler returns the current AI settings.
func (m *aiSettingsModule) GetAIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		settings, err := m.readSettings()
		if err != nil {
			respondError(w, http.StatusInternalServerError, "could not read AI settings")
			return
		}

		resp := aiSettingsResponse{
			Upstreams:    []aiUpstreamResponse{},
			DefaultModel: settings.DefaultModel,
			HasAPIKey:    m.hasAPIKey(),
			ServedTo:     m.servedTo(settings),
		}
		for _, u := range settings.Upstreams {
			resp.Upstreams = append(resp.Upstreams, aiUpstreamResponse{
				ID: u.ID, Name: u.Name, BaseURL: u.BaseURL, Models: u.Models, Enabled: u.Enabled,
			})
		}
		respondJSON(w, http.StatusOK, resp)
	}
}

// setAIRequest is the PUT body. APIKey is a pointer so omitted means "keep the
// stored credential" and an explicit empty string means "clear it".
type setAIRequest struct {
	Upstreams    []aiUpstreamResponse `json:"upstreams"`
	DefaultModel string               `json:"defaultModel"`
	APIKey       *string              `json:"apiKey"`
}

// SetAIHandler validates the payload and submits a SetInferenceIntent. It never
// writes a store itself: the orchestrator is the single writer.
func (m *aiSettingsModule) SetAIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req setAIRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		upstreams := make([]inference.Upstream, 0, len(req.Upstreams))
		for _, u := range req.Upstreams {
			ep, err := inference.ParseEndpoint(u.BaseURL)
			if err != nil {
				respondError(w, http.StatusBadRequest, fmt.Sprintf("upstream %q: %s", u.Name, err))
				return
			}
			id := strings.TrimSpace(u.ID)
			if id == "" {
				id = "default"
			}
			upstreams = append(upstreams, inference.Upstream{
				ID:      id,
				Name:    strings.TrimSpace(u.Name),
				BaseURL: ep.String(),
				Models:  u.Models,
				Enabled: u.Enabled,
			})
		}

		encoded, err := inference.EncodeUpstreams(upstreams)
		if err != nil {
			respondError(w, http.StatusBadRequest, "could not encode upstreams")
			return
		}

		m.orch.Submit(orchestrator.NewSetInferenceIntent(encoded, strings.TrimSpace(req.DefaultModel), req.APIKey))

		// The response echoes the canonical values the orchestrator will store,
		// so the UI can poll for convergence without re-deriving them.
		respondJSON(w, http.StatusAccepted, map[string]any{
			"status":       "accepted",
			"defaultModel": strings.TrimSpace(req.DefaultModel),
			"upstreams":    upstreams,
		})
	}
}

// testAIRequest is the POST body for the operator-initiated endpoint check.
type testAIRequest struct {
	BaseURL string  `json:"baseUrl"`
	APIKey  *string `json:"apiKey"`
}

// TestAIHandler dials the candidate endpoint's /models and reports what it
// found.
//
// This is a probe, and it is allowed. Invariant 15 forbids the reconciler from
// probing a port to decide whether a provider is installed, because a probe
// cannot separate "not installed" from "restarting" and a prune keyed off it
// deletes wiring that is still wanted. Nothing here prunes anything, and
// convergence never consults it: it exists so an operator can catch a typo
// before saving it.
func (m *aiSettingsModule) TestAIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req testAIRequest
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

func (m *aiSettingsModule) readSettings() (inference.Settings, error) {
	if m.settingsStore == nil {
		return inference.Settings{}, nil
	}
	upstreamsJSON, err := m.settingsStore.Get(inference.SettingUpstreams)
	if err != nil {
		return inference.Settings{}, err
	}
	defaultModel, err := m.settingsStore.Get(inference.SettingDefaultModel)
	if err != nil {
		return inference.Settings{}, err
	}
	return inference.DecodeSettings(upstreamsJSON, defaultModel)
}

func (m *aiSettingsModule) hasAPIKey() bool {
	if m.secrets == nil {
		return false
	}
	return m.secrets.GetAppSecret(inference.SecretScope, inference.SecretAPIKey) != ""
}

// servedTo names each installed app that declares the inference contract and how
// it currently reaches one. It reads the same resolver the configurator reads, so
// the UI cannot show a wiring the app does not have.
func (m *aiSettingsModule) servedTo(settings inference.Settings) []aiServedToResponse {
	out := []aiServedToResponse{}
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
		if _, declares := catalogApp.Integrations["inference"]; !declares {
			continue
		}
		out = append(out, aiServedToResponse{
			App:   app.CatalogID,
			Via:   m.viaLabel(settings),
			Model: settings.DefaultModel,
		})
	}
	return out
}

// viaLabel describes the serving path for display. A gateway app that is
// installed is the path; otherwise the instance setting is raw, and with neither
// the consumer is unwired.
func (m *aiSettingsModule) viaLabel(settings inference.Settings) string {
	if m.gatewayInstalled() {
		return "gateway"
	}
	if _, ok, err := settings.Endpoint(); err == nil && ok {
		return "direct"
	}
	return "none"
}

func (m *aiSettingsModule) gatewayInstalled() bool {
	if m.appStore == nil || m.catalog == nil {
		return false
	}
	apps, err := m.appStore.GetAll()
	if err != nil {
		return false
	}
	for _, app := range apps {
		catalogApp, err := m.catalog.Get(app.CatalogID)
		if err != nil || catalogApp == nil {
			continue
		}
		if _, offers := catalogApp.Provides["inference"]; offers {
			return true
		}
	}
	return false
}

// fetchModels calls the OpenAI-compatible GET /models with a bounded timeout.
func fetchModels(ep inference.Endpoint, apiKey string) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, ep.ModelsURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", ep.ModelsURL(), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned %s", ep.ModelsURL(), resp.Status)
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("response is not an OpenAI model list: %w", err)
	}

	models := make([]string, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		if d.ID != "" {
			models = append(models, d.ID)
		}
	}
	return models, nil
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// RegisterAIRoutes mounts the AI settings surface on an admin router.
func RegisterAIRoutes(mod *aiSettingsModule, r chi.Router) {
	r.Get("/settings/ai", mod.GetAIHandler())
	r.Put("/settings/ai", mod.SetAIHandler())
	r.Post("/settings/ai/test", mod.TestAIHandler())
}
