// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appflowy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// mockSSOEndpoints stands in for both the GoTrue admin API (under /gotrue)
// and the Authentik API (under /api/v3) on a single server, so a test
// Configurator can reach both through one published port. It records
// mutations and lets tests seed an existing provider or inject failures.
type mockSSOEndpoints struct {
	srv *httptest.Server

	mu         sync.Mutex
	requests   int
	tokenCalls int
	creates    []map[string]any
	deleted    []string
	// patchedRedirectURIs captures the Authentik OAuth2 provider PATCH.
	patchedRedirectURIs []string

	// existing seeds GET /gotrue/admin/custom-providers (zero = none).
	existing customProvider
	// gotrueStatus overrides the create response (0 = 201).
	gotrueCreateStatus int
	gotrueCreateMsg    string

	// admin creds the token endpoint accepts.
	adminEmail    string
	adminPassword string
}

func newMockSSOEndpoints(t *testing.T, stackRoutes bool, adminEmail, adminPassword string) *mockSSOEndpoints {
	t.Helper()
	m := &mockSSOEndpoints{adminEmail: adminEmail, adminPassword: adminPassword}
	mux := http.NewServeMux()

	// --- GoTrue admin API ---
	mux.HandleFunc("/gotrue/token", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.requests++
		m.tokenCalls++
		m.mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Query().Get("grant_type") != "password" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Email != m.adminEmail || body.Password != m.adminPassword {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"test-admin-token"}`)
	})
	mux.HandleFunc("/gotrue/admin/custom-providers", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.requests++
		m.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer test-admin-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			m.mu.Lock()
			providers := []customProvider{}
			if m.existing.Identifier != "" {
				providers = append(providers, m.existing)
			}
			m.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"providers": providers})
		case http.MethodPost:
			data, _ := io.ReadAll(r.Body)
			var payload map[string]any
			if err := json.Unmarshal(data, &payload); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			m.mu.Lock()
			if m.gotrueCreateStatus != 0 {
				status, msg := m.gotrueCreateStatus, m.gotrueCreateMsg
				m.mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"code":%d,"error_code":"validation_failed","msg":%q}`, status, msg)
				return
			}
			m.creates = append(m.creates, payload)
			m.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/gotrue/admin/custom-providers/", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.requests++
		m.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer test-admin-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		identifier := strings.TrimPrefix(r.URL.Path, "/gotrue/admin/custom-providers/")
		if r.Method == http.MethodDelete {
			m.mu.Lock()
			m.deleted = append(m.deleted, identifier)
			m.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	// --- Authentik API (EnsureNativeOIDC, existing-provider path) ---
	mux.HandleFunc("/api/v3/providers/oauth2/", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.requests++
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/api/v3/providers/oauth2/"):
			// findProviderID: provider already exists (pk 7).
			fmt.Fprint(w, `{"results":[{"pk":7,"name":"AppFlowy OAuth2 Provider"}]}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/api/v3/providers/oauth2/7/"):
			// Provider fetch for the email scope-mapping check: already
			// carries the Bloud verified-email mapping (no drift).
			fmt.Fprint(w, `{"pk":7,"property_mappings":["bloud-email-pk"]}`)
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/api/v3/providers/oauth2/7/"):
			var body struct {
				RedirectURIs []struct {
					URL string `json:"url"`
				} `json:"redirect_uris"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			m.mu.Lock()
			for _, u := range body.RedirectURIs {
				m.patchedRedirectURIs = append(m.patchedRedirectURIs, u.URL)
			}
			m.mu.Unlock()
			fmt.Fprint(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	mux.HandleFunc("/api/v3/propertymappings/provider/scope/", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.requests++
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// The Bloud verified-email scope mapping exists; the managed one
		// does not, so EnsureNativeOIDC finds nothing to swap.
		fmt.Fprint(w, `{"results":[{"pk":"bloud-email-pk","name":"Bloud OIDC: OpenID 'email' (verified)","managed":""}]}`)
	})
	mux.HandleFunc("/api/v3/core/applications/appflowy/", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.requests++
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"slug":"appflowy"}`)
	})

	if stackRoutes {
		// The three proxied routes verifyRoutes polls, so PostStart can
		// run against this same server.
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		mux.HandleFunc("/gotrue/health", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}

	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockSSOEndpoints) port() int {
	u, _ := url.Parse(m.srv.URL)
	var port int
	fmt.Sscanf(u.Port(), "%d", &port)
	return port
}

func (m *mockSSOEndpoints) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests
}

// ssoTestConfig returns a complete SSOConfig for a public (registrable)
// issuer.
func ssoTestConfig() SSOConfig {
	return SSOConfig{
		IssuerURL:           "https://sso.example.com:8443/application/o/appflowy/",
		RedirectURI:         "http://appflowy.localhost:8080/gotrue/callback",
		LaunchURL:           "http://appflowy.localhost:8080",
		ClientID:            "appflowy-client",
		ClientSecret:        "derived-client-secret",
		GotrueAdminEmail:    "admin@appflowy.local",
		GotrueAdminPassword: "derived-admin-password",
		AuthentikBaseURL:    "", // set per test to the mock server URL
		AuthentikToken:      "test-authentik-token",
	}
}

func TestSSOConfigEnabled(t *testing.T) {
	full := ssoTestConfig()
	if !full.Enabled() {
		t.Error("complete SSOConfig should be enabled")
	}
	var zero SSOConfig
	if zero.Enabled() {
		t.Error("zero SSOConfig should be disabled")
	}
	for name, mutate := range map[string]func(*SSOConfig){
		"issuer":        func(s *SSOConfig) { s.IssuerURL = "" },
		"redirect":      func(s *SSOConfig) { s.RedirectURI = "" },
		"clientID":      func(s *SSOConfig) { s.ClientID = "" },
		"clientSecret":  func(s *SSOConfig) { s.ClientSecret = "" },
		"adminEmail":    func(s *SSOConfig) { s.GotrueAdminEmail = "" },
		"adminPassword": func(s *SSOConfig) { s.GotrueAdminPassword = "" },
	} {
		cfg := full
		mutate(&cfg)
		if cfg.Enabled() {
			t.Errorf("SSOConfig without %s should be disabled", name)
		}
	}
}

func TestIssuerHostRegistrable(t *testing.T) {
	cases := []struct {
		issuer string
		want   bool
	}{
		// Local hosts: GoTrue rejects literal localhost/loopback.
		{"https://sso.localhost:8443/application/o/appflowy/", false},
		{"https://localhost/application/o/appflowy/", false},
		{"https://deep.sso.localhost/x/", false},
		{"https://127.0.0.1:8443/x/", false},
		{"https://[::1]:8443/x/", false},
		// Private IPs: GoTrue rejects loopback/private resolutions.
		{"https://10.0.2.2:8443/x/", false},
		{"https://192.168.1.5/x/", false},
		// Public: registrable.
		{"https://sso.example.com/application/o/appflowy/", true},
		{"https://sso.example.com:443/application/o/appflowy/", true},
		{"https://203.0.113.7/x/", true}, // public IP literal
		// Garbage.
		{"", false},
		{"not a url", false},
		{"https:///x/", false},
	}
	for _, tc := range cases {
		if got := IssuerRegistrable(tc.issuer); got != tc.want {
			t.Errorf("IssuerRegistrable(%q) = %v, want %v", tc.issuer, got, tc.want)
		}
	}
}

func TestConfigureSSODisabledNoTraffic(t *testing.T) {
	m := newMockSSOEndpoints(t, false, "admin@appflowy.local", "pw")
	c := NewConfigurator(m.port(), SSOConfig{}, testLogger())

	c.configureSSO(context.Background())

	if n := m.count(); n != 0 {
		t.Errorf("disabled SSO made %d requests, want 0", n)
	}
}

func TestConfigureSSOLocalIssuerSkipsWiring(t *testing.T) {
	m := newMockSSOEndpoints(t, false, "admin@appflowy.local", "pw")
	cfg := ssoTestConfig()
	cfg.IssuerURL = "https://sso.localhost:8443/application/o/appflowy/"
	cfg.AuthentikBaseURL = m.srv.URL
	c := NewConfigurator(m.port(), cfg, testLogger())

	c.configureSSO(context.Background())

	// GoTrue rejects local issuers, so the wiring must not even be
	// attempted: no Authentik calls, no gotrue calls.
	if n := m.count(); n != 0 {
		t.Errorf("local issuer made %d requests, want 0 (skip, don't fail)", n)
	}
}

func TestConfigureSSORegistersProvider(t *testing.T) {
	m := newMockSSOEndpoints(t, false, "admin@appflowy.local", "derived-admin-password")
	cfg := ssoTestConfig()
	cfg.AuthentikBaseURL = m.srv.URL
	c := NewConfigurator(m.port(), cfg, testLogger())

	c.configureSSO(context.Background())

	if m.tokenCalls != 1 {
		t.Fatalf("admin token requests = %d, want 1", m.tokenCalls)
	}
	// The redirect URI must be registered with the Authentik provider.
	if len(m.patchedRedirectURIs) != 1 || m.patchedRedirectURIs[0] != cfg.RedirectURI {
		t.Errorf("Authentik redirect URIs = %v, want [%s]", m.patchedRedirectURIs, cfg.RedirectURI)
	}
	// Exactly one GoTrue provider creation, with the desired payload.
	if len(m.creates) != 1 {
		t.Fatalf("provider creations = %d, want 1", len(m.creates))
	}
	p := m.creates[0]
	if p["provider_type"] != "oidc" {
		t.Errorf("provider_type = %v, want oidc", p["provider_type"])
	}
	if p["identifier"] != ssoProviderIdentifier {
		t.Errorf("identifier = %v, want %s", p["identifier"], ssoProviderIdentifier)
	}
	if p["name"] != ssoProviderName {
		t.Errorf("name = %v, want %s", p["name"], ssoProviderName)
	}
	if p["issuer"] != cfg.IssuerURL {
		t.Errorf("issuer = %v, want %s", p["issuer"], cfg.IssuerURL)
	}
	if p["client_id"] != cfg.ClientID {
		t.Errorf("client_id = %v, want %s", p["client_id"], cfg.ClientID)
	}
	if p["client_secret"] != cfg.ClientSecret {
		t.Errorf("client_secret = %v, want %s", p["client_secret"], cfg.ClientSecret)
	}
	scopes, _ := p["scopes"].([]any)
	if len(scopes) != 3 || scopes[0] != "openid" || scopes[1] != "email" || scopes[2] != "profile" {
		t.Errorf("scopes = %v, want [openid email profile]", p["scopes"])
	}
	mapping, _ := p["attribute_mapping"].(map[string]any)
	if mapping["email"] != "{claims.email}" {
		t.Errorf("attribute_mapping = %v, want {email: {claims.email}}", p["attribute_mapping"])
	}
}

func TestConfigureSSOIdempotentWhenProviderMatches(t *testing.T) {
	m := newMockSSOEndpoints(t, false, "admin@appflowy.local", "derived-admin-password")
	cfg := ssoTestConfig()
	cfg.AuthentikBaseURL = m.srv.URL
	// Seed the provider in the exact desired state (scopes shuffled to
	// prove order-insensitive comparison).
	m.existing = customProvider{
		ProviderType:     "oidc",
		Identifier:       ssoProviderIdentifier,
		Name:             ssoProviderName,
		ClientID:         cfg.ClientID,
		Scopes:           []string{"profile", "openid", "email"},
		AttributeMapping: map[string]string{"email": "{claims.email}"},
		Issuer:           cfg.IssuerURL,
	}
	c := NewConfigurator(m.port(), cfg, testLogger())

	c.configureSSO(context.Background())

	if len(m.creates) != 0 {
		t.Errorf("matching provider should not be re-created, got %d creations", len(m.creates))
	}
	if len(m.deleted) != 0 {
		t.Errorf("matching provider should not be deleted, got %v", m.deleted)
	}
}

func TestConfigureSSOReplacesDriftedProvider(t *testing.T) {
	m := newMockSSOEndpoints(t, false, "admin@appflowy.local", "derived-admin-password")
	cfg := ssoTestConfig()
	cfg.AuthentikBaseURL = m.srv.URL
	// Seed a provider whose issuer drifted (e.g. public URL changed).
	m.existing = customProvider{
		ProviderType:     "oidc",
		Identifier:       ssoProviderIdentifier,
		Name:             ssoProviderName,
		ClientID:         cfg.ClientID,
		Scopes:           []string{"openid", "email", "profile"},
		AttributeMapping: map[string]string{"email": "{claims.email}"},
		Issuer:           "https://old.example.com/application/o/appflowy/",
	}
	c := NewConfigurator(m.port(), cfg, testLogger())

	c.configureSSO(context.Background())

	if len(m.deleted) != 1 || m.deleted[0] != ssoProviderIdentifier {
		t.Fatalf("drifted provider deletions = %v, want [%s]", m.deleted, ssoProviderIdentifier)
	}
	if len(m.creates) != 1 {
		t.Fatalf("provider creations = %d, want 1 (re-registration)", len(m.creates))
	}
	if got := m.creates[0]["issuer"]; got != cfg.IssuerURL {
		t.Errorf("re-registered issuer = %v, want %s", got, cfg.IssuerURL)
	}
}

// TestPostStartSurvivesSSOFailure asserts the app-start contract: an SSO
// wiring failure is logged and retried next cycle, never failing PostStart
// (local sign-up must keep working).
func TestPostStartSurvivesSSOFailure(t *testing.T) {
	m := newMockSSOEndpoints(t, true, "admin@appflowy.local", "derived-admin-password")
	m.gotrueCreateStatus = http.StatusInternalServerError
	m.gotrueCreateMsg = "boom"

	cfg := ssoTestConfig()
	cfg.AuthentikBaseURL = m.srv.URL
	c := NewConfigurator(m.port(), cfg, testLogger())

	ctx := context.Background()
	if err := c.PostStart(ctx, &configurator.AppState{}); err != nil {
		t.Fatalf("PostStart must survive an SSO wiring failure, got: %v", err)
	}
	// The wiring was attempted all the way to the create (and failed
	// there) — it was not skipped.
	if m.tokenCalls != 1 {
		t.Errorf("admin token requests = %d, want 1 (wiring attempted)", m.tokenCalls)
	}
	if len(m.creates) != 0 {
		t.Errorf("provider creations = %d, want 0 (create failed)", len(m.creates))
	}
}

// TestPostStartSkipsLocalIssuerWithoutFailing is the dev-environment
// contract: with a localhost issuer the wiring is skipped and PostStart
// passes without touching any SSO endpoint.
func TestPostStartSkipsLocalIssuerWithoutFailing(t *testing.T) {
	m := newMockSSOEndpoints(t, true, "admin@appflowy.local", "pw")

	cfg := ssoTestConfig()
	cfg.IssuerURL = "https://sso.localhost:8443/application/o/appflowy/"
	cfg.AuthentikBaseURL = m.srv.URL
	c := NewConfigurator(m.port(), cfg, testLogger())

	ctx := context.Background()
	if err := c.PostStart(ctx, &configurator.AppState{}); err != nil {
		t.Fatalf("PostStart failed with local issuer: %v", err)
	}
	// Route verification hits the (uncounted) stack routes; no SSO
	// endpoint may be touched at all.
	if n := m.count(); n != 0 {
		t.Errorf("SSO endpoint requests = %d, want 0 (local issuer skipped)", n)
	}
}
