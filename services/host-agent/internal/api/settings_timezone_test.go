// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/timezone"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func putTimezone(t *testing.T, mod *settingsModule, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Put("/settings/timezone", mod.SetTimezoneHandler())
	req := httptest.NewRequest(http.MethodPut, "/settings/timezone", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func getTimezone(t *testing.T, mod *settingsModule) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/settings/timezone", mod.GetTimezoneHandler())
	req := httptest.NewRequest(http.MethodGet, "/settings/timezone", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// A valid IANA name is submitted as an intent, and the response echoes the zone
// the save accepted so the UI can poll for it without a second parser.
func TestSetTimezoneHandler(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.timezone = timezone.NewState("")

	w := putTimezone(t, mod, `{"zone":" Europe/Berlin "}`)
	require.Equal(t, http.StatusAccepted, w.Code)

	intent, ok := mod.orch.(*FakeOrchestrator).LastIntent().(orchestrator.SetTimezoneIntent)
	require.True(t, ok)
	assert.Equal(t, "Europe/Berlin", intent.Zone)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "Europe/Berlin", resp["zone"])
	assert.NotEmpty(t, resp["intentId"])
}

// An empty zone is a request, not a mistake: it clears the setting and returns
// every container to the default its image ships with.
func TestSetTimezoneHandler_EmptyClears(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.timezone = timezone.NewState("Europe/Berlin")

	w := putTimezone(t, mod, `{"zone":""}`)
	require.Equal(t, http.StatusAccepted, w.Code)

	intent, ok := mod.orch.(*FakeOrchestrator).LastIntent().(orchestrator.SetTimezoneIntent)
	require.True(t, ok)
	assert.Equal(t, "", intent.Zone)
}

// A name nothing can resolve gets a 400 rather than a 202 that quietly does
// nothing and later shows up as a container that cannot set its own clock.
func TestSetTimezoneHandler_RejectsBadZones(t *testing.T) {
	for _, body := range []string{
		`{"zone":"Mars/Olympus"}`,
		`{"zone":"Local"}`,
		`{"zone":"+02:00"}`,
		`{"zone":"/usr/share/zoneinfo/Europe/Berlin"}`,
		`{"zone":"../../etc/passwd"}`,
		`{"zone":"Europe/Berlin/"}`,
	} {
		t.Run(body, func(t *testing.T) {
			mod := newSettingsModule(t, nil)
			mod.timezone = timezone.NewState("Etc/UTC")

			w := putTimezone(t, mod, body)
			assert.Equal(t, http.StatusBadRequest, w.Code, "%s must be rejected", body)
			assert.Equal(t, 0, mod.orch.(*FakeOrchestrator).IntentCount(),
				"a rejected request must not submit an intent")
			assert.Equal(t, "Etc/UTC", mod.timezone.Get(), "a rejection changes nothing")
		})
	}
}

func TestSetTimezoneHandler_RejectsMalformedBody(t *testing.T) {
	mod := newSettingsModule(t, nil)
	w := putTimezone(t, mod, `{not json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSetTimezoneHandler_NoOrchestratorIs503(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.orch = nil

	w := putTimezone(t, mod, `{"zone":"Europe/Berlin"}`)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// GET reports the live zone, which is the value a container spec is built from.
func TestGetTimezoneHandler(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.timezone = timezone.NewState("Asia/Kolkata")

	w := getTimezone(t, mod)
	require.Equal(t, http.StatusOK, w.Code)

	var resp timezoneResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "Asia/Kolkata", resp.Zone)
}

// With no clock state the endpoint answers with no zone rather than failing, so
// a fresh install renders the form instead of an error.
func TestGetTimezoneHandler_WithoutState(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.timezone = nil

	w := getTimezone(t, mod)
	require.Equal(t, http.StatusOK, w.Code)

	var resp timezoneResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "", resp.Zone)
}
