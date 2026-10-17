// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/timezone"
)

// timezoneResponse is the API representation of the instance clock setting.
//
// Zone is the IANA name in force, or "" when Bloud states no zone and every
// container keeps the default its image ships with.
type timezoneResponse struct {
	Zone string `json:"zone"`
}

// GetTimezoneHandler returns the instance clock.
//
// It reads the live state rather than the store, because the live state is what
// a container spec is built from. The answer then cannot drift from what an app
// actually runs with while a save is still converging.
func (m *settingsModule) GetTimezoneHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, timezoneResponse{Zone: m.timezone.Get()})
	}
}

// setTimezoneRequest is the request body for PUT /api/settings/timezone.
type setTimezoneRequest struct {
	// Zone is an IANA zone name, for example Europe/Berlin. Empty clears the
	// setting.
	Zone string `json:"zone"`
}

// SetTimezoneHandler validates the zone and enqueues a SetTimezoneIntent. The
// orchestrator persists it and recreates the managed containers so they pick
// the new clock up.
//
// An empty zone is accepted and means "state nothing": it clears the setting and
// returns every container to its image default. A non-empty zone is validated
// here as well as in the orchestrator, because the API owes the operator a 400
// that names what is wrong with the string they typed, rather than a 202 that
// quietly does nothing.
func (m *settingsModule) SetTimezoneHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req setTimezoneRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if m.orch == nil {
			respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
			return
		}
		zone := strings.TrimSpace(req.Zone)
		if zone != "" {
			parsed, err := timezone.Parse(zone)
			if err != nil {
				respondError(w, http.StatusBadRequest, err.Error())
				return
			}
			zone = parsed
		}
		intent := orchestrator.NewSetTimezoneIntent(zone)
		m.orch.Submit(intent)

		// The accepted zone comes back on the response so the caller can tell
		// "applied" from "submitted" without a second parser: the UI polls the
		// live value and compares it against this string.
		respondJSON(w, http.StatusAccepted, map[string]string{
			"intentId": intent.IntentID(),
			"zone":     zone,
		})
	}
}
