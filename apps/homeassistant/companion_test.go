// SPDX-License-Identifier: AGPL-3.0-only

package homeassistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// fakeCompanionHA is the slice of Home Assistant the companion token needs: the
// credential login flow, the token endpoint, the admin-gated probe that proves a
// bearer still works, and the WebSocket API that mints and deletes long-lived
// tokens. Every handler answers the way the corresponding view in Home
// Assistant's `components/auth` does, and INTEGRATION.md records where each
// shape came from.
type fakeCompanionHA struct {
	t *testing.T

	mu sync.Mutex

	// ownerPassword is what the credential handler accepts. Flipping it models
	// the password having changed outside Bloud.
	ownerPassword string
	// validTokens is the set of bearer strings the instance accepts. A mint
	// adds one; a delete removes the one it labels.
	validTokens map[string]bool
	// tokenEntries are the long-lived tokens on the account, as
	// auth/refresh_tokens reports them.
	tokenEntries []map[string]any

	mints   int
	deletes []string
	// configStatus answers GET /api/config: 200 for a live token, 401 for a
	// dead one. A test can pin it to 500 to model an unreachable instance.
	configStatus int

	upgrader websocket.Upgrader
}

func newFakeCompanionHA(t *testing.T) *fakeCompanionHA {
	return &fakeCompanionHA{
		t:             t,
		ownerPassword: "bootstrap-password",
		validTokens:   map[string]bool{},
		upgrader:      websocket.Upgrader{},
	}
}

func (f *fakeCompanionHA) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/login_flow", f.handleFlowInit)
	mux.HandleFunc("/auth/login_flow/", f.handleFlowStep)
	mux.HandleFunc("/auth/token", f.handleToken)
	mux.HandleFunc("/api/config", f.handleConfig)
	mux.HandleFunc("/api/websocket", f.handleWS)
	srv := httptest.NewServer(mux)
	f.t.Cleanup(srv.Close)
	return srv
}

func (f *fakeCompanionHA) handleFlowInit(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&body))
	require.Equal(f.t, onboardingClientID, body["client_id"])
	require.Equal(f.t, loginRedirectURI, body["redirect_uri"])
	// Home Assistant's schema wants a [type, id] pair and keys its provider
	// registry by that pair. The built-in provider's id is null, and a pair of
	// two type names is a 500 out of the real view, so the fake refuses it too.
	handler, ok := body["handler"].([]any)
	require.True(f.t, ok, "handler must be a [type, id] pair")
	require.Len(f.t, handler, 2, "handler is a [type, id] pair")
	require.Equal(f.t, credentialAuthHandler, handler[0])
	require.Nil(f.t, handler[1], "the built-in provider's id is null, not its name")
	writeJSON(w, map[string]any{"flow_id": "flow-1", "handler": "homeassistant", "data_schema": []any{}})
}

func (f *fakeCompanionHA) handleFlowStep(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&body))
	f.mu.Lock()
	password := f.ownerPassword
	f.mu.Unlock()
	if body["password"] != password || body["username"] != bootstrapUsername {
		writeJSON(w, map[string]any{"type": "form", "errors": map[string]string{"base": "invalid_auth"}})
		return
	}
	writeJSON(w, map[string]any{"type": "create_entry", "result": "auth-code-1"})
}

func (f *fakeCompanionHA) handleToken(w http.ResponseWriter, r *http.Request) {
	require.NoError(f.t, r.ParseForm())
	if r.FormValue("code") != "auth-code-1" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"access_token": "short-lived", "token_type": "Bearer", "expires_in": 1800})
}

// handleConfig models the auth-required probe: any bearer the instance still
// knows answers 200, anything else answers 401.
func (f *fakeCompanionHA) handleConfig(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	status := f.configStatus
	live := f.validTokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if !live {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]any{"server": map[string]any{"version": "2026.9.0"}})
}

// handleWS speaks just enough of the WebSocket API: the auth handshake, then
// id-matched results for the three commands this package sends.
func (f *fakeCompanionHA) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := f.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	_ = conn.WriteJSON(map[string]any{"type": "auth_required", "ha_version": "2026.9.0"})
	var auth struct {
		Type        string `json:"type"`
		AccessToken string `json:"access_token"`
	}
	if err := conn.ReadJSON(&auth); err != nil || auth.Type != "auth" {
		return
	}
	f.mu.Lock()
	live := f.validTokens[auth.AccessToken]
	if auth.AccessToken == "short-lived" {
		live = true
	}
	f.mu.Unlock()
	if !live {
		_ = conn.WriteJSON(map[string]any{"type": "auth_error", "message": "Invalid access token provided"})
		return
	}
	_ = conn.WriteJSON(map[string]any{"type": "auth_ok", "ha_version": "2026.9.0"})

	for {
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		id := msg["id"]
		switch msg["type"] {
		case "auth/long_lived_access_token":
			token := "long-lived-1"
			f.mu.Lock()
			f.mints++
			f.validTokens[token] = true
			f.tokenEntries = append(f.tokenEntries, map[string]any{
				"id": "entry-1", "client_name": msg["client_name"], "type": longLivedTokenType,
			})
			f.mu.Unlock()
			_ = conn.WriteJSON(map[string]any{"id": id, "type": "result", "success": true, "result": token})
		case "auth/refresh_tokens":
			f.mu.Lock()
			entries := f.tokenEntries
			f.mu.Unlock()
			_ = conn.WriteJSON(map[string]any{"id": id, "type": "result", "success": true, "result": entries})
		case "auth/delete_refresh_token":
			id := msg["refresh_token_id"]
			f.mu.Lock()
			f.deletes = append(f.deletes, anyString(id))
			kept := f.tokenEntries[:0]
			for _, e := range f.tokenEntries {
				if e["id"] != id {
					kept = append(kept, e)
				}
			}
			f.tokenEntries = kept
			delete(f.validTokens, "long-lived-1")
			f.mu.Unlock()
			_ = conn.WriteJSON(map[string]any{"id": msg["id"], "type": "result", "success": true, "result": map[string]any{}})
		default:
			_ = conn.WriteJSON(map[string]any{"id": id, "type": "result", "success": false, "message": "unknown command"})
		}
	}
}

func anyString(v any) string {
	s, _ := v.(string)
	return s
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// companionSecrets is the in-memory secrets provider the mint flow needs.
type companionSecrets struct {
	values map[string]string
}

func newCompanionSecrets() *companionSecrets {
	return &companionSecrets{values: map[string]string{}}
}

func (s *companionSecrets) GenerateAppAdminPassword(string) (string, error) {
	return "bootstrap-password", nil
}
func (s *companionSecrets) GetAppSecret(_, key string) string { return s.values[key] }
func (s *companionSecrets) SetAppSecret(_, key, value string) error {
	s.values[key] = value
	return nil
}
func (s *companionSecrets) SetAppContractValue(_, _, _, _ string) error { return nil }
func (s *companionSecrets) GetAppContractValue(_, _, _ string) string   { return "" }
func (s *companionSecrets) DeleteAppSecrets(string) error               { return nil }

// newCompanionConfigurator wires a configurator at the fake instance.
func newCompanionConfigurator(t *testing.T, fake *fakeCompanionHA, secrets *companionSecrets) *Configurator {
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, HTTP: appclient.Factory{}})
	c.SetBaseURL(fake.server().URL)
	return c
}

// TestEnsureCompanionTokenMintsOnce is the whole point of the feature: the
// first pass mints over the WebSocket API and stores the result, and a later
// pass reads the stored token instead of minting a second one. A mint per
// reconciliation pass would leave a new row on the owner's profile every
// minute.
func TestEnsureCompanionTokenMintsOnce(t *testing.T) {
	fake := newFakeCompanionHA(t)
	secrets := newCompanionSecrets()
	c := newCompanionConfigurator(t, fake, secrets)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	token, err := c.ensureCompanionToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "long-lived-1", token)
	require.Equal(t, "long-lived-1", secrets.values[companionTokenKey])

	again, err := c.ensureCompanionToken(ctx)
	require.NoError(t, err)
	require.Equal(t, token, again)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Equal(t, 1, fake.mints, "a usable stored token must never be re-minted")
}

// TestEnsureCompanionTokenRemintsWhenRejected covers the operator who deletes
// the token from Home Assistant's profile page: the next pass notices the 401
// and mints a replacement rather than leaving the consumer authenticated with
// nothing.
func TestEnsureCompanionTokenRemintsWhenRejected(t *testing.T) {
	fake := newFakeCompanionHA(t)
	secrets := newCompanionSecrets()
	secrets.values[companionTokenKey] = "revoked-token"
	c := newCompanionConfigurator(t, fake, secrets)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	token, err := c.ensureCompanionToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "long-lived-1", token)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Equal(t, 1, fake.mints)
}

// TestEnsureCompanionTokenKeepsTokenWhenUnreachable pins the inconclusive
// answer: a Home Assistant that is mid-restart must not be read as "the token
// is dead", because that turns every outage into a mint per pass.
func TestEnsureCompanionTokenKeepsTokenWhenUnreachable(t *testing.T) {
	fake := newFakeCompanionHA(t)
	fake.configStatus = http.StatusInternalServerError
	secrets := newCompanionSecrets()
	secrets.values[companionTokenKey] = "stored-token"
	c := newCompanionConfigurator(t, fake, secrets)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	token, err := c.ensureCompanionToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "stored-token", token)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Zero(t, fake.mints)
}

// TestEnsureCompanionTokenReportsABadPassword is the diagnostic that matters
// when it breaks: the password here is one Bloud generated, so a refusal means
// someone changed it in Home Assistant.
func TestEnsureCompanionTokenReportsABadPassword(t *testing.T) {
	fake := newFakeCompanionHA(t)
	fake.ownerPassword = "changed-by-hand"
	c := newCompanionConfigurator(t, fake, newCompanionSecrets())

	_, err := c.ensureCompanionToken(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), bootstrapUsername)
	require.Contains(t, err.Error(), "invalid_auth")
}

// TestRevokeCompanionTokenOnlyDeletesItsOwn covers teardown: the token labelled
// for this integration goes, and a token belonging to something else on the same
// account is left alone.
func TestRevokeCompanionTokenOnlyDeletesItsOwn(t *testing.T) {
	fake := newFakeCompanionHA(t)
	secrets := newCompanionSecrets()
	c := newCompanionConfigurator(t, fake, secrets)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.ensureCompanionToken(ctx)
	require.NoError(t, err)

	fake.mu.Lock()
	fake.tokenEntries = append(fake.tokenEntries, map[string]any{
		"id": "someone-elses", "client_name": "GPS Logger", "type": longLivedTokenType,
	})
	fake.mu.Unlock()

	require.NoError(t, c.revokeCompanionToken(ctx))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Equal(t, []string{"entry-1"}, fake.deletes)
}

// TestRevokeCompanionTokenWithoutATokenIsSilent: an install that never minted
// one has nothing to undo, and must not reach the network to find out.
func TestRevokeCompanionTokenWithoutATokenIsSilent(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: newCompanionSecrets()})
	c.SetBaseURL("http://127.0.0.1:1")
	require.NoError(t, c.revokeCompanionToken(context.Background()))
}

// TestRevokeCompanionTokenSaysWhenItCouldNotAct: an uninstall that could not
// reach Home Assistant leaves an admin token alive somewhere, and the operator
// has to be told that rather than shown a clean removal.
func TestRevokeCompanionTokenSaysWhenItCouldNotAct(t *testing.T) {
	fake := newFakeCompanionHA(t)
	secrets := newCompanionSecrets()
	secrets.values[companionTokenKey] = "a-token-ha-no-longer-knows"
	c := newCompanionConfigurator(t, fake, secrets)

	err := c.revokeCompanionToken(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "revoke")
}

// TestWsURLFor pins the scheme swap: the configurator talks to Home Assistant
// over http everywhere else, and the WebSocket endpoint is the same origin.
func TestWsURLFor(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://localhost:8123", "ws://localhost:8123/api/websocket"},
		{"http://localhost:8123/", "ws://localhost:8123/api/websocket"},
		{"https://ha.example.com", "wss://ha.example.com/api/websocket"},
	} {
		got, err := wsURLFor(tc.in)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
	_, err := wsURLFor("ftp://ha")
	require.Error(t, err)
}
