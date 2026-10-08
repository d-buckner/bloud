// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// externalAppsModule owns the external-app surface. PR 1 exposes only
// launchers: a named tile that opens a URL and wires to nothing.
type externalAppsModule struct {
	externalApps store.ExternalAppStoreInterface
	orch         orchestratorCaller
	logger       *slog.Logger
}

// externalAppResponse is one external app as the API surfaces it. PR 1 carries
// the launcher fields only; later kinds extend it.
type externalAppResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	Icon string `json:"icon"`
}

// setExternalAppRequest is the add/update body.
type setExternalAppRequest struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Icon string `json:"icon"`
}

// Register mounts the external-app routes on an admin router.
func (m *externalAppsModule) Register(r chi.Router) {
	r.Get("/external-apps", m.ListHandler())
	r.Post("/external-apps", m.AddHandler())
	r.Patch("/external-apps/{id}", m.UpdateHandler())
	r.Delete("/external-apps/{id}", m.DeleteHandler())
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
		respondJSON(w, http.StatusOK, toExternalAppResponses(apps))
	}
}

// AddHandler validates a launcher and submits an add intent. It never writes
// the store itself: the orchestrator is the single writer.
func (m *externalAppsModule) AddHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, canonicalURL, icon, ok := m.decodeRequest(w, r)
		if !ok {
			return
		}
		if m.orch == nil {
			respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
			return
		}
		id := uuid.NewString()
		intent := orchestrator.NewAddExternalAppIntent(id, name, canonicalURL, icon)
		m.orch.Submit(intent)
		respondJSON(w, http.StatusAccepted, map[string]any{
			"status":   "accepted",
			"intentId": intent.IntentID(),
			"app":      m.readBack(id, name, canonicalURL, icon),
		})
	}
}

// UpdateHandler changes a launcher's name, URL, or icon.
func (m *externalAppsModule) UpdateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, canonicalURL, icon, ok := m.decodeRequest(w, r)
		if !ok {
			return
		}
		if m.orch == nil {
			respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
			return
		}
		id := chi.URLParam(r, "id")
		intent := orchestrator.NewUpdateExternalAppIntent(id, name, canonicalURL, icon)
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

// decodeRequest parses and validates the shared add/update body. It writes the
// error response on failure and reports whether the caller may continue.
func (m *externalAppsModule) decodeRequest(w http.ResponseWriter, r *http.Request) (name, canonicalURL, icon string, ok bool) {
	var req setExternalAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return "", "", "", false
	}
	name = strings.TrimSpace(req.Name)
	if name == "" {
		respondError(w, http.StatusBadRequest, "name is required")
		return "", "", "", false
	}
	canonicalURL, err := validateLauncherURL(req.URL)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return "", "", "", false
	}
	return name, canonicalURL, strings.TrimSpace(req.Icon), true
}

// readBack returns the canonical record the orchestrator just persisted. It
// reads, never writes, so the handler still observes invariant 1. The fallback
// echo keeps the 202 response self-contained when no store is wired (tests).
func (m *externalAppsModule) readBack(id, name, canonicalURL, icon string) externalAppResponse {
	if m.externalApps != nil {
		if app, err := m.externalApps.Get(id); err == nil && app != nil {
			return toExternalAppResponse(app)
		}
	}
	return externalAppResponse{ID: id, Name: name, URL: canonicalURL, Icon: icon}
}

// toExternalAppResponses maps store records to the API shape.
func toExternalAppResponses(apps []*store.ExternalApp) []externalAppResponse {
	out := make([]externalAppResponse, 0, len(apps))
	for _, app := range apps {
		out = append(out, toExternalAppResponse(app))
	}
	return out
}

func toExternalAppResponse(app *store.ExternalApp) externalAppResponse {
	return externalAppResponse{ID: app.ID, Name: app.Name, URL: app.URL, Icon: app.Icon}
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
