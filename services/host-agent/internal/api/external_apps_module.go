// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// externalAppsModule owns the external-app surface: launchers that open a URL,
// and providers that stand in for something Bloud does not run.
type externalAppsModule struct {
	externalApps store.ExternalAppStoreInterface
	appStore     store.AppStoreInterface
	catalog      catalog.CacheInterface
	secrets      configurator.AppSecretsProvider
	orch         orchestratorCaller
	logger       *slog.Logger
}

// newExternalAppsModule builds the module from the router's dependency bag.
// It lives here rather than inline in buildRouterModules so the provider's
// collaborator list (the catalog and the installed set, for validation; the
// secrets manager, for the "credential is set" read) is visible next to the
// type that uses it.
func newExternalAppsModule(deps *routerDeps, logger *slog.Logger) *externalAppsModule {
	return &externalAppsModule{
		externalApps: deps.externalApps,
		appStore:     deps.appStore,
		catalog:      deps.catalogCache,
		secrets:      deps.secrets,
		orch:         deps.orchCaller,
		logger:       logger,
	}
}

// externalAppResponse is one external app as the API surfaces it. The
// credential values are never included, only which contracts have one stored,
// the same rule the AI settings surface follows: an echo of a secret in a list
// response is a secret in a log line one refactor away.
type externalAppResponse struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	// App is the catalog ID this record stands in for, for a `source: app`
	// provider. Empty for a launcher and for a bare contract provider.
	App    string                       `json:"app,omitempty"`
	Name   string                       `json:"name"`
	URL    string                       `json:"url"`
	Icon   string                       `json:"icon"`
	Values map[string]map[string]string `json:"values,omitempty"` // SecretContracts names the contracts whose credential is stored, so the
	// form can show "set" without showing what was set.
	SecretContracts []string `json:"secretContracts,omitempty"`
}

// setExternalAppRequest is the add/update body. Kind defaults to launcher so a
// client written against the launcher-only API keeps working.
type setExternalAppRequest struct {
	Kind    string                       `json:"kind"`
	Source  string                       `json:"source"`
	Name    string                       `json:"name"`
	URL     string                       `json:"url"`
	Icon    string                       `json:"icon"`
	Values  map[string]map[string]string `json:"values"`
	Secrets map[string]string            `json:"secrets"`
	// Exchange carries the inputs of an app's credential exchange (see
	// external_apps_exchange.go), keyed by input key. It replaces the contract
	// secrets the form would otherwise have asked for: what arrives here is a
	// sign-in, and what gets stored is whatever the remote app handed back for
	// it. Like a secret, omitting it on an update means "leave the credential on
	// file alone"; unlike a secret it is never read back.
	Exchange map[string]string `json:"exchange,omitempty"`
	// ExchangeLogin asks Bloud to sign in with the login it already holds for the
	// role the app names, instead of with typed inputs. It exists so a password
	// Bloud minted and stores never has to pass through a browser that cannot
	// show it back.
	ExchangeLogin bool `json:"exchangeLogin,omitempty"`
}

// Register mounts the external-app routes on an admin router.
func (m *externalAppsModule) Register(r chi.Router) {
	r.Get("/external-apps", m.ListHandler())
	r.Post("/external-apps", m.AddHandler())
	r.Patch("/external-apps/{id}", m.UpdateHandler())
	r.Delete("/external-apps/{id}", m.DeleteHandler())
	r.Get("/external-apps/providers", m.ProvidersHandler())
}

// ListHandler returns every external app. An absent store reads as empty, not
// an error, so a runtime without the feature degrades cleanly.
func (m *externalAppsModule) ListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if m.externalApps == nil {
			respondJSON(w, http.StatusOK, []externalAppResponse{})
			return
		}
		apps, err := m.externalApps.GetAll()
		if err != nil {
			m.logger.Error("failed to list external apps", "error", err)
			respondError(w, http.StatusInternalServerError, "could not list external apps")
			return
		}
		out := make([]externalAppResponse, 0, len(apps))
		for _, app := range apps {
			out = append(out, m.toResponse(app))
		}
		respondJSON(w, http.StatusOK, out)
	}
}

// AddHandler validates an external app and submits an add intent. It never
// writes the store itself: the orchestrator is the single writer.
func (m *externalAppsModule) AddHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		spec, ok := m.decodeSpec(w, r, "")
		if !ok {
			return
		}
		if m.orch == nil {
			respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
			return
		}
		spec.ID = uuid.NewString()
		intent := orchestrator.NewAddExternalAppIntent(spec)
		m.orch.Submit(intent)
		respondJSON(w, http.StatusAccepted, map[string]any{
			"status":   "accepted",
			"intentId": intent.IntentID(),
			"app":      m.readBack(spec),
		})
	}
}

// UpdateHandler changes an external app's name, URL, icon, values, or
// credentials. The kind and source are immutable: changing what a record *is*
// means removing it and adding the other thing.
func (m *externalAppsModule) UpdateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var existing *store.ExternalApp
		if m.externalApps != nil {
			record, err := m.externalApps.Get(id)
			if err != nil {
				respondError(w, http.StatusInternalServerError, "could not read external app")
				return
			}
			if record == nil {
				respondError(w, http.StatusNotFound, "external app not found")
				return
			}
			existing = record
		}
		if existing != nil {
			merged, err := mergePatchOntoRecord(existing, r.Body)
			if err != nil {
				respondError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			r.Body = io.NopCloser(merged)
		}
		spec, ok := m.decodeSpec(w, r, id)
		if !ok {
			return
		}
		if m.orch == nil {
			respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
			return
		}
		intent := orchestrator.NewUpdateExternalAppIntent(spec)
		m.orch.Submit(intent)
		respondJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "intentId": intent.IntentID()})
	}
}

// DeleteHandler removes an external app.
func (m *externalAppsModule) DeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.orch == nil {
			respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
			return
		}
		intent := orchestrator.NewRemoveExternalAppIntent(chi.URLParam(r, "id"))
		m.orch.Submit(intent)
		respondJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "intentId": intent.IntentID()})
	}
}

// mergePatchOntoRecord fills the fields a PATCH body omits from the stored
// record, so the shared validator always sees a complete spec.
//
// Without it, renaming an external app means resending its endpoint, its
// source, and its payload: a client that omits one gets a 400 for a field it
// never touched, or worse, a silently blanked column. Key presence rather than
// empty strings is what "the client did not say" means, which is why this reads
// the raw JSON instead of a decoded struct where absent and zero are the same
// value. Secrets are deliberately not filled in: an omitted credential means
// "keep the one on file", and the orchestrator never blanks it on an update.
func mergePatchOntoRecord(existing *store.ExternalApp, body io.Reader) (io.Reader, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(raw, &patch); err != nil {
		return nil, err
	}
	if patch == nil {
		patch = map[string]json.RawMessage{}
	}
	fill := func(key, value string) {
		if _, named := patch[key]; named {
			return
		}
		patch[key] = json.RawMessage(mustJSON(value))
	}
	fill("kind", existing.Kind)
	fill("source", existing.Source)
	fill("name", existing.Name)
	fill("url", existing.URL)
	fill("icon", existing.Icon)
	if _, named := patch["values"]; !named && strings.TrimSpace(existing.Values) != "" {
		patch["values"] = json.RawMessage(existing.Values)
	}
	merged, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(string(merged)), nil
}

// mustJSON encodes a string, falling back to an empty string literal. A plain
// string always encodes, so the fallback is unreachable; it keeps the caller
// free of an error it could not act on.
func mustJSON(s string) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		return []byte(`""`)
	}
	return b
}

// decodeSpec parses and validates the shared add/update body. existingID is
// the record being updated, or empty on add.
//
// It writes the error response on failure and reports whether the caller may
// continue.
func (m *externalAppsModule) decodeSpec(w http.ResponseWriter, r *http.Request, existingID string) (orchestrator.ExternalAppSpec, bool) {
	var req setExternalAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return orchestrator.ExternalAppSpec{}, false
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondError(w, http.StatusBadRequest, "name is required")
		return orchestrator.ExternalAppSpec{}, false
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = string(store.ExternalAppKindLauncher)
	}

	rawURL := strings.TrimSpace(req.URL)
	switch kind {
	case string(store.ExternalAppKindLauncher):
		canonicalURL, err := validateLauncherURL(rawURL)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return orchestrator.ExternalAppSpec{}, false
		}
		return orchestrator.ExternalAppSpec{
			ID:     existingID,
			Kind:   kind,
			Name:   name,
			URL:    canonicalURL,
			Icon:   strings.TrimSpace(req.Icon),
			Values: normalizeStringMap(req.Values),
		}, true
	case string(store.ExternalAppKindProvider):
		spec, err := m.decodeProvider(name, rawURL, strings.TrimSpace(req.Icon), req, existingID)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return orchestrator.ExternalAppSpec{}, false
		}
		return spec, true
	default:
		respondError(w, http.StatusBadRequest, fmt.Sprintf("unknown kind %q", kind))
		return orchestrator.ExternalAppSpec{}, false
	}
}

// readBack returns the canonical record the orchestrator just persisted. It
// reads, never writes, so the handler still observes invariant 1. The fallback
// echo keeps the 202 response self-contained when no store is wired (tests).
func (m *externalAppsModule) readBack(spec orchestrator.ExternalAppSpec) externalAppResponse {
	if m.externalApps != nil {
		if app, err := m.externalApps.Get(spec.ID); err == nil && app != nil {
			return m.toResponse(app)
		}
	}
	return responseFromSpec(spec)
}

// toExternalAppResponse maps one store record to the API shape, without the
// per-record credential lookup the module method adds.
func toExternalAppResponse(app *store.ExternalApp) externalAppResponse {
	return externalAppResponse{
		ID:     app.ID,
		Kind:   app.Kind,
		Source: app.Source,
		Name:   app.Name,
		URL:    app.URL,
		Icon:   app.Icon,
		Values: decodeExternalValues(app.Values),
	}
}

// validateLauncherURL accepts an http or https URL with a host and no embedded
// credentials. A launcher opens exactly what the operator typed, so a path or
// query is legitimate and preserved; only the scheme and host are enforced.
func validateLauncherURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("URL must be http or https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("URL must include a host")
	}
	if u.User != nil {
		return "", fmt.Errorf("URL must not carry credentials")
	}
	return u.String(), nil
}

// validateEndpointURL accepts an http or https origin and nothing else: no
// path, no query, no fragment, no credentials.
//
// Stricter than the launcher rule on purpose. A launcher's URL is a finished
// thing a browser opens; this one is a base that consumers append contract
// paths to, and `https://host/old//v1` plus `/api/x` is an address that
// connects to the wrong place. A path belongs on the contract's values, never
// in the origin.
func validateEndpointURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("endpoint must be http or https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("endpoint must include a host")
	}
	if u.User != nil {
		return "", fmt.Errorf("endpoint must not carry credentials")
	}
	if strings.Trim(u.Path, "/") != "" {
		return "", fmt.Errorf("endpoint must be an origin with no path; a path belongs on the contract's values")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("endpoint must be an origin with no query or fragment")
	}
	return u.Scheme + "://" + u.Host, nil
}

// decodeExternalValues parses the stored values JSON into the map form.
func decodeExternalValues(values string) map[string]map[string]string {
	if values == "" || values == "{}" {
		return nil
	}
	var parsed map[string]map[string]string
	if err := json.Unmarshal([]byte(values), &parsed); err != nil {
		return nil
	}
	return parsed
}

// normalizeStringMap drops empty inner maps so a form that sent an empty
// section does not store a key that resolves to nothing.
func normalizeStringMap(in map[string]map[string]string) map[string]map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]map[string]string, len(in))
	for contract, vals := range in {
		if len(vals) == 0 {
			continue
		}
		inner := make(map[string]string, len(vals))
		for k, v := range vals {
			inner[k] = v
		}
		out[contract] = inner
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
