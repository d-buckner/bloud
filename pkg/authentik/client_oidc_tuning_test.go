// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAuthentik serves just the routes EnsureNativeOIDC touches, and records
// what the client wrote. existing is the provider that already exists (nil for
// a fresh install); withOfflineAccess controls whether the instance has the
// offline_access scope mapping at all.
type fakeAuthentik struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	existing *fakeProvider
	created  map[string]any   // body of the POST /providers/oauth2/, nil when none
	patches  []map[string]any // bodies of PATCHes to the existing provider
}

type fakeProvider struct {
	mappings []string
	validity string
}

func newFakeAuthentik(t *testing.T, existing *fakeProvider, withOfflineAccess bool) *fakeAuthentik {
	t.Helper()
	f := &fakeAuthentik{t: t, existing: existing}

	scopes := []map[string]any{
		{"pk": "openid-uuid", "scope_name": "openid", "name": "OpenID 'openid'", "managed": "goauthentik.io/providers/oauth2/scope-openid"},
		{"pk": "profile-uuid", "scope_name": "profile", "name": "OpenID 'profile'", "managed": "goauthentik.io/providers/oauth2/scope-profile"},
		{"pk": "managed-email-uuid", "scope_name": "email", "name": "OpenID 'email'", "managed": "goauthentik.io/providers/oauth2/scope-email"},
		{"pk": "bloud-email-uuid", "scope_name": "email", "name": bloudEmailScopeMappingName, "managed": ""},
	}
	if withOfflineAccess {
		scopes = append(scopes, map[string]any{
			"pk": "offline-uuid", "scope_name": "offline_access", "name": "OpenID 'offline_access'",
			"managed": "goauthentik.io/providers/oauth2/scope-offline_access",
		})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/propertymappings/provider/scope/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": scopes})
	})
	mux.HandleFunc("/api/v3/flows/instances/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"pk": "flow-uuid"})
	})
	mux.HandleFunc("/api/v3/crypto/certificatekeypairs/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"pk": "cert-uuid"}}})
	})
	mux.HandleFunc("/api/v3/core/applications/", func(w http.ResponseWriter, r *http.Request) {
		// The application already exists, so only the provider is under test.
		_ = json.NewEncoder(w).Encode(map[string]any{"slug": "vaultwarden"})
	})
	mux.HandleFunc("/api/v3/providers/oauth2/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodGet: // findProviderID search
			results := []map[string]any{}
			if f.existing != nil {
				results = append(results, map[string]any{"pk": 7, "name": "Vaultwarden OAuth2 Provider"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(body, &f.created))
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"pk": 9})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	mux.HandleFunc("/api/v3/providers/oauth2/7/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"property_mappings":     f.existing.mappings,
				"access_token_validity": f.existing.validity,
				"redirect_uris":         []map[string]string{{"matching_mode": "strict", "url": "http://vaultwarden.localhost:8080/identity/connect/oidc-signin"}},
			})
		case http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			var patch map[string]any
			require.NoError(t, json.Unmarshal(body, &patch))
			f.patches = append(f.patches, patch)
			if m, ok := patch["property_mappings"].([]any); ok {
				f.existing.mappings = nil
				for _, v := range m {
					f.existing.mappings = append(f.existing.mappings, v.(string))
				}
			}
			if v, ok := patch["access_token_validity"].(string); ok {
				f.existing.validity = v
			}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAuthentik) ensure(tuning OIDCTuning) error {
	c := NewClient(f.server.URL, "test-token")
	return c.EnsureNativeOIDC(context.Background(), "vaultwarden", "Vaultwarden", "client-id", "client-secret",
		[]string{"http://vaultwarden.localhost:8080/identity/connect/oidc-signin"}, "", tuning)
}

func (f *fakeAuthentik) createdMappings() []string {
	var out []string
	for _, v := range f.created["property_mappings"].([]any) {
		out = append(out, v.(string))
	}
	return out
}

func TestEnsureNativeOIDCCreateAppliesTuning(t *testing.T) {
	f := newFakeAuthentik(t, nil, true)

	require.NoError(t, f.ensure(OIDCTuning{ExtraScopes: []string{"offline_access"}, AccessTokenMinutes: 60}))

	require.NotNil(t, f.created, "a provider must be created")
	assert.Equal(t, "minutes=60", f.created["access_token_validity"])
	assert.Equal(t, []string{"openid-uuid", "profile-uuid", "bloud-email-uuid", "offline-uuid"}, f.createdMappings(),
		"the extra scope is added after the built-ins, and email stays Bloud's verified mapping")
}

func TestEnsureNativeOIDCCreateWithoutTuningKeepsDefaults(t *testing.T) {
	// Regression guard: an app that declares nothing is provisioned exactly as
	// before this feature existed.
	f := newFakeAuthentik(t, nil, true)

	require.NoError(t, f.ensure(OIDCTuning{}))

	assert.Equal(t, "minutes=5", f.created["access_token_validity"])
	assert.Equal(t, []string{"openid-uuid", "profile-uuid", "bloud-email-uuid"}, f.createdMappings())
}

func TestEnsureNativeOIDCCreateFailsLoudlyOnMissingScope(t *testing.T) {
	// An Authentik without the offline_access mapping must not yield a provider
	// that silently lacks it: the app would fail at sign-in with no clue why.
	f := newFakeAuthentik(t, nil, false)

	err := f.ensure(OIDCTuning{ExtraScopes: []string{"offline_access"}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no scope mapping for offline_access")
	assert.Nil(t, f.created, "no provider may be created")
}

func TestEnsureNativeOIDCExistingProviderIsReconciled(t *testing.T) {
	// A provider created before the app declared tuning (or by a reinstall)
	// gains the scope and the lifetime without being recreated.
	f := newFakeAuthentik(t, &fakeProvider{
		mappings: []string{"openid-uuid", "profile-uuid", "bloud-email-uuid"},
		validity: "minutes=5",
	}, true)

	require.NoError(t, f.ensure(OIDCTuning{ExtraScopes: []string{"offline_access"}, AccessTokenMinutes: 60}))

	assert.Nil(t, f.created, "the existing provider is reused")
	assert.Equal(t, []string{"openid-uuid", "profile-uuid", "bloud-email-uuid", "offline-uuid"}, f.existing.mappings)
	assert.Equal(t, "minutes=60", f.existing.validity)
}

func TestEnsureProviderTuningIsIdempotent(t *testing.T) {
	// The steady-state reconciliation pass must not write.
	f := newFakeAuthentik(t, &fakeProvider{
		mappings: []string{"openid-uuid", "offline-uuid", "bloud-email-uuid"},
		validity: "minutes=60",
	}, true)
	c := NewClient(f.server.URL, "test-token")

	require.NoError(t, c.ensureProviderTuning(context.Background(), 7,
		OIDCTuning{ExtraScopes: []string{"offline_access"}, AccessTokenMinutes: 60}))

	assert.Empty(t, f.patches, "an in-sync provider must not be PATCHed")
}

func TestEnsureProviderTuningPatchesOnlyWhatDrifted(t *testing.T) {
	// Scope present, lifetime wrong: only the lifetime is written, and the
	// existing mappings are untouched.
	f := newFakeAuthentik(t, &fakeProvider{
		mappings: []string{"openid-uuid", "offline-uuid"},
		validity: "minutes=5",
	}, true)
	c := NewClient(f.server.URL, "test-token")

	require.NoError(t, c.ensureProviderTuning(context.Background(), 7,
		OIDCTuning{ExtraScopes: []string{"offline_access"}, AccessTokenMinutes: 60}))

	require.Len(t, f.patches, 1)
	assert.Equal(t, map[string]any{"access_token_validity": "minutes=60"}, f.patches[0])
}

func TestEnsureProviderTuningNeverRemovesMappings(t *testing.T) {
	// Tuning only adds. A mapping an operator attached by hand survives.
	f := newFakeAuthentik(t, &fakeProvider{
		mappings: []string{"openid-uuid", "operator-added-uuid"},
		validity: "minutes=5",
	}, true)
	c := NewClient(f.server.URL, "test-token")

	require.NoError(t, c.ensureProviderTuning(context.Background(), 7,
		OIDCTuning{ExtraScopes: []string{"offline_access"}}))

	assert.Equal(t, []string{"openid-uuid", "operator-added-uuid", "offline-uuid"}, f.existing.mappings)
	assert.Equal(t, "minutes=5", f.existing.validity, "a lifetime the app did not declare is left alone")
}

func TestOIDCTuningAccessTokenValidity(t *testing.T) {
	assert.Equal(t, "minutes=5", OIDCTuning{}.accessTokenValidity())
	assert.Equal(t, "minutes=60", OIDCTuning{AccessTokenMinutes: 60}.accessTokenValidity())
	assert.True(t, OIDCTuning{}.isZero())
	assert.False(t, OIDCTuning{ExtraScopes: []string{"offline_access"}}.isZero())
	assert.False(t, OIDCTuning{AccessTokenMinutes: 1}.isZero())
}
