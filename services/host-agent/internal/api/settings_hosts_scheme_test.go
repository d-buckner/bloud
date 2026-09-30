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
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHostStore records what the orchestrator would persist, so the handler
// tests can assert on the shape the settings API sends without a database.
type fakeHostStore struct {
	hosts   []store.Host
	primary string
}

func (f *fakeHostStore) List() ([]store.Host, error) { return f.hosts, nil }

func (f *fakeHostStore) Replace(hosts []store.Host, primary string) error {
	f.hosts = append([]store.Host(nil), hosts...)
	f.primary = primary
	return nil
}

func putHosts(t *testing.T, mod *settingsModule, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Put("/settings/hosts", mod.SetHostsHandler())
	req := httptest.NewRequest("PUT", "/settings/hosts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Saving a host with https must reach the intent as a scheme, not be dropped
// on the floor: the redirect URI and the issuer both have to move to https
// for a TLS-terminated deployment to work at all.
func TestSetHostsHandler_AcceptsHTTPS(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")

	w := putHosts(t, mod, `{"hosts":["localhost","bloud.local","bloud.example.com"],"primary":"bloud.example.com","schemes":{"bloud.example.com":"https"}}`)
	require.Equal(t, http.StatusAccepted, w.Code)

	intent, ok := mod.orch.(*FakeOrchestrator).LastIntent().(orchestrator.SetHostsIntent)
	require.True(t, ok)
	assert.Equal(t, "bloud.example.com", intent.Primary)
	assert.Equal(t, hostset.SchemeHTTPS, intent.Schemes["bloud.example.com"])
}

// A scheme that is not http or https is a 400 naming the value, not a
// silently ignored field that later surfaces as an OAuth redirect failure.
func TestSetHostsHandler_RejectsBadScheme(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")

	for _, bad := range []string{"ftp", "htps", "HTTPS; drop", "://x"} {
		w := putHosts(t, mod, `{"hosts":["localhost","bloud.local","a.example.com"],"primary":"a.example.com","schemes":{"a.example.com":"`+bad+`"}}`)
		assert.Equal(t, http.StatusBadRequest, w.Code, "scheme %q must be rejected", bad)
		assert.Contains(t, w.Body.String(), "only http and https")
	}
	assert.Equal(t, 0, mod.orch.(*FakeOrchestrator).IntentCount(),
		"a rejected request must not submit an intent")
}

// An unknown hostname in the schemes map is rejected rather than silently
// dropped, so a typo is visible at the API rather than later.
func TestSetHostsHandler_RejectsSchemeForUnknownHost(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")

	w := putHosts(t, mod, `{"hosts":["localhost","bloud.local"],"primary":"localhost","schemes":{"typo.example.com":"https"}}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// No schemes at all is the existing shape and must keep working.
func TestSetHostsHandler_SchemesOptional(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local"}, "localhost")

	w := putHosts(t, mod, `{"hosts":["localhost","bloud.local","a.example.com"],"primary":"a.example.com"}`)
	require.Equal(t, http.StatusAccepted, w.Code)

	intent, ok := mod.orch.(*FakeOrchestrator).LastIntent().(orchestrator.SetHostsIntent)
	require.True(t, ok)
	assert.Empty(t, intent.Schemes)
}

// GET reports the effective scheme from the live host set rather than echoing
// stored state, so what the UI shows is what the OAuth client was given.
func TestGetHostsHandler_ReportsEffectiveScheme(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local", "bloud.example.com"}, "bloud.example.com")
	mod.hostState = hostset.NewState(hostset.New(
		[]string{"localhost", "bloud.local", "bloud.example.com"}, "bloud.example.com",
	).WithSchemes(map[string]hostset.Scheme{"bloud.example.com": hostset.SchemeHTTPS}))
	mod.hostStore = &fakeHostStore{hosts: []store.Host{{Hostname: "bloud.example.com", Primary: true, Scheme: "https"}}}

	r := chi.NewRouter()
	r.Get("/settings/hosts", mod.GetHostsHandler())
	req := httptest.NewRequest("GET", "/settings/hosts", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Hosts []hostResponse `json:"hosts"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

	byName := map[string]hostResponse{}
	for _, h := range resp.Hosts {
		byName[h.Hostname] = h
	}
	assert.Equal(t, "https", byName["bloud.example.com"].Scheme)
	assert.True(t, byName["bloud.example.com"].Primary)
	assert.Equal(t, "http", byName["localhost"].Scheme)
	assert.True(t, byName["localhost"].Builtin)
}

// The host set is the source of truth for the reported scheme: a stored row
// that disagrees with the live set must not win.
func TestGetHostsHandler_LiveSetWinsOverStoredRow(t *testing.T) {
	mod := newFirstRunModule(t, []string{"localhost", "bloud.local", "a.example.com"}, "a.example.com")
	mod.hostStore = &fakeHostStore{hosts: []store.Host{{Hostname: "a.example.com", Primary: true, Scheme: "https"}}}

	// Live set has no https override for a.example.com, so the effective
	// scheme is http even though the stored row claims https.
	r := chi.NewRouter()
	r.Get("/settings/hosts", mod.GetHostsHandler())
	req := httptest.NewRequest("GET", "/settings/hosts", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Hosts []hostResponse `json:"hosts"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	for _, h := range resp.Hosts {
		if h.Hostname == "a.example.com" {
			assert.Equal(t, "http", h.Scheme,
				"the live host set is authoritative; a stale stored scheme must not be reported")
		}
	}
}

var _ = hostset.DefaultPrimary
