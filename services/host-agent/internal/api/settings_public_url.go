// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
)

// publicURLResponse is the API representation of the address setting.
//
// URL is the configured origin: scheme, host, and the port the proxy is
// dialed on, all in one string.
type publicURLResponse struct {
	URL string `json:"url"`
}

func (m *settingsModule) currentPublicURL() publicURLResponse {
	return publicURLResponse{URL: m.liveHostSet().PrimaryBaseURL()}
}

// liveHostSet returns the live address state, or the default address when the
// orchestrator never installed one.
func (m *settingsModule) liveHostSet() hostset.HostSet {
	if m.hostState != nil {
		return m.hostState.Get()
	}
	hs, err := hostset.ParsePublicURL(hostset.DefaultPublicURL)
	if err != nil {
		return hostset.HostSet{}
	}
	return hostset.New(hs)
}

// GetPublicURLHandler returns the address this Bloud is reachable at.
func (m *settingsModule) GetPublicURLHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, m.currentPublicURL())
	}
}

// setPublicURLRequest is the request body for PUT /api/settings/public-url.
type setPublicURLRequest struct {
	URL string `json:"url"`
}

// SetPublicURLHandler validates the address and enqueues a SetPublicURLIntent.
// The orchestrator persists the change, re-provisions SSO, and restarts SSO
// apps so they pick up the new URLs.
//
// The value is parsed here as well as in the orchestrator because the API owes
// the operator a 400 naming what is wrong with the string they just typed,
// rather than a 202 that quietly does nothing. The origin is re-rendered from
// the parsed value before it is submitted, so the store receives the
// canonical form and a save that only changed capitalization or a trailing
// slash is not mistaken for a move.
func (m *settingsModule) SetPublicURLHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req setPublicURLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if m.orch == nil {
			respondError(w, http.StatusServiceUnavailable, "orchestrator not available")
			return
		}
		public, err := hostset.ParsePublicURL(req.URL)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		intent := orchestrator.NewSetPublicURLIntent(public.Origin())
		m.orch.Submit(intent)

		// The canonical origin comes back on the response so the caller can
		// tell "applied" from "submitted" without re-implementing the parser:
		// a save of "bloud.example.com" is stored as
		// "http://bloud.example.com", and the UI's poll has to compare
		// against the stored form or a successful save looks like a failure.
		respondJSON(w, http.StatusAccepted, map[string]string{
			"intentId": intent.IntentID(),
			"url":      public.Origin(),
		})
	}
}

// ---- Setup Wizard ----

// requestScheme reports the scheme the client reached this request over.
//
// TLS on the socket is definitive. Behind a TLS-terminating proxy the socket
// is plain http and X-Forwarded-Proto is the only signal that exists at
// all, so it is read here. That is safe in this one place because the value
// only ever lands on the host this same request is adopting, on a first-run
// request made by whoever is creating the admin account; it is never read
// from anonymous traffic, and the login path has no orchestrator to submit
// the change with even if it tried.
func requestScheme(r *http.Request) hostset.Scheme {
	if r.TLS != nil {
		return hostset.SchemeHTTPS
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		// The first entry is the client-facing scheme; later ones are hops
		// appended downstream.
		if s := hostset.NormalizeScheme(strings.Split(fwd, ",")[0]); s != "" {
			return s
		}
	}
	return hostset.SchemeHTTP
}
