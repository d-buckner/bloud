// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func putPublicURL(t *testing.T, mod *settingsModule, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Put("/settings/public-url", mod.SetPublicURLHandler())
	req := httptest.NewRequest(http.MethodPut, "/settings/public-url", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func getPublicURL(t *testing.T, mod *settingsModule) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/settings/public-url", mod.GetPublicURLHandler())
	req := httptest.NewRequest(http.MethodGet, "/settings/public-url", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// The save carries the whole origin, scheme and port included, as one string.
// That is the shape the UI sends and the shape the orchestrator persists.
func TestSetPublicURLHandler(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"url":"https://bloud.example.com"}`, "https://bloud.example.com"},
		{`{"url":"http://bloud.example.com"}`, "http://bloud.example.com"},
		{`{"url":"https://bloud.example.com:8443"}`, "https://bloud.example.com:8443"},
		// A bare host is a thing people type; it means http.
		{`{"url":"bloud.example.com"}`, "http://bloud.example.com"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			mod := newFirstRunModule(t, "http://localhost:8080")
			w := putPublicURL(t, mod, c.in)
			require.Equal(t, http.StatusAccepted, w.Code)

			intent, ok := mod.orch.(*FakeOrchestrator).LastIntent().(orchestrator.SetPublicURLIntent)
			require.True(t, ok)
			assert.Equal(t, c.want, intent.URL)
		})
	}
}

// The value submitted is re-rendered from the parsed origin, not echoed from
// the request. A save that only changed capitalization, a trailing slash, or an
// explicit default port therefore arrives as the same canonical string the
// orchestrator's no-op guard compares against, and does not restart every SSO
// app for a cosmetic edit.
func TestSetPublicURLHandler_CanonicalizesBeforeSubmitting(t *testing.T) {
	mod := newFirstRunModule(t, "http://localhost:8080")

	w := putPublicURL(t, mod, `{"url":"  HTTPS://Bloud.Example.COM:443/  "}`)
	require.Equal(t, http.StatusAccepted, w.Code)

	intent := mod.orch.(*FakeOrchestrator).LastIntent().(orchestrator.SetPublicURLIntent)
	assert.Equal(t, "https://bloud.example.com", intent.URL)
}

// A bad value gets a 400 naming what is wrong with the string the operator
// just typed, rather than a 202 that quietly does nothing and surfaces later
// as a login that will not round-trip.
func TestSetPublicURLHandler_RejectsBadURLs(t *testing.T) {
	for _, body := range []string{
		`{"url":""}`,
		`{"url":"ftp://bloud.example.com"}`,
		`{"url":"https://bloud.example.com/some/path"}`,
		`{"url":"https://bloud.example.com?a=b"}`,
		`{"url":"http://user:pass@bloud.example.com"}`,
		`{"url":"https://10.0.0.210"}`,
		`{"url":"https://bloud.example.com:99999"}`,
		`{"url":"not a url at all"}`,
	} {
		t.Run(body, func(t *testing.T) {
			mod := newFirstRunModule(t, "http://localhost:8080")
			w := putPublicURL(t, mod, body)
			assert.Equal(t, http.StatusBadRequest, w.Code, "%s must be rejected", body)
			assert.Equal(t, 0, mod.orch.(*FakeOrchestrator).IntentCount(),
				"a rejected request must not submit an intent")
		})
	}
}

func TestSetPublicURLHandler_RejectsMalformedBody(t *testing.T) {
	mod := newFirstRunModule(t, "http://localhost:8080")
	w := putPublicURL(t, mod, `{not json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSetPublicURLHandler_NoOrchestratorIs503(t *testing.T) {
	mod := newFirstRunModule(t, "http://localhost:8080")
	mod.orch = nil

	w := putPublicURL(t, mod, `{"url":"https://bloud.example.com"}`)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// GET reports the live address as one string. The built-in local aliases are
// not part of the response: they are not a setting, and the reachability that
// matters (localhost and bloud.local still registered with the provider) is
// asserted on AllBaseURLs in the hostset tests, not on what the UI shows.
func TestGetPublicURLHandler(t *testing.T) {
	mod := newFirstRunModule(t, "https://bloud.example.com")

	w := getPublicURL(t, mod)
	require.Equal(t, http.StatusOK, w.Code)

	var resp publicURLResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "https://bloud.example.com", resp.URL)
}

// A localhost install reports the default address as-is, so the field is never
// blank on a fresh install and the operator sees what is actually live.
func TestGetPublicURLHandler_LocalhostInstall(t *testing.T) {
	mod := newFirstRunModule(t, "http://localhost:8080")

	w := getPublicURL(t, mod)
	require.Equal(t, http.StatusOK, w.Code)

	var resp publicURLResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "http://localhost:8080", resp.URL)
}

// With no live address state the endpoint still answers, with the default
// address rather than an empty string or a panic.
func TestGetPublicURLHandler_WithoutHostState(t *testing.T) {
	mod := newSettingsModule(t, nil)
	mod.hostState = nil

	w := getPublicURL(t, mod)
	require.Equal(t, http.StatusOK, w.Code)

	var resp publicURLResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, hostset.DefaultPublicURL, resp.URL)
}
