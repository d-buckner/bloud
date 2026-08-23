// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package authentik

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// newScopeMappingTestServer serves the routes ensureProviderEmailScopeMapping
// uses: the scope mapping list, the provider detail, and a recording PATCH.
// currentMappings is the provider's initial property_mappings; bloudExists
// controls whether the list already contains Bloud's verified email mapping.
// It returns the last PATCHed mapping list and whether a PATCH occurred.
func newScopeMappingTestServer(t *testing.T, currentMappings []string, bloudExists bool) (*httptest.Server, *[]string, *bool) {
	t.Helper()
	var (
		mu          sync.Mutex
		patched     []string
		patchCalled bool
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/propertymappings/provider/scope/", func(w http.ResponseWriter, r *http.Request) {
		results := []map[string]any{
			{"pk": "managed-email-uuid", "name": "authentik default OAuth Mapping: OpenID 'email'", "managed": "goauthentik.io/providers/oauth2/scope-email"},
			{"pk": "openid-uuid", "name": "authentik default OAuth Mapping: OpenID 'openid'", "managed": "goauthentik.io/providers/oauth2/scope-openid"},
		}
		if bloudExists {
			results = append(results, map[string]any{
				"pk": "bloud-email-uuid", "name": bloudEmailScopeMappingName, "managed": "",
			})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	mux.HandleFunc("/api/v3/providers/oauth2/7/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{"property_mappings": currentMappings})
		case http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				PropertyMappings []string `json:"property_mappings"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Errorf("decoding PATCH body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			patched = payload.PropertyMappings
			patchCalled = true
			mu.Unlock()
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	server := httptest.NewServer(mux)
	return server, &patched, &patchCalled
}

func TestEnsureProviderEmailScopeMappingIdempotent(t *testing.T) {
	// Regression test for the mapping-drop bug: when the provider already
	// uses Bloud's verified email mapping, the reconciliation pass must
	// leave the mapping list unchanged (no PATCH that drops the email
	// mapping).
	server, patched, patchCalled := newScopeMappingTestServer(t,
		[]string{"bloud-email-uuid", "openid-uuid"}, true)
	defer server.Close()

	client := NewClient(server.URL, "test-token")
	if err := client.ensureProviderEmailScopeMapping(7); err != nil {
		t.Fatalf("ensureProviderEmailScopeMapping() error: %v", err)
	}
	if *patchCalled {
		t.Fatalf("expected no PATCH for an undrifted provider, got mappings %v", *patched)
	}
}

func TestEnsureProviderEmailScopeMappingSwapsManaged(t *testing.T) {
	// The managed email mapping (email_verified: False) must be replaced in
	// place by Bloud's verified mapping.
	server, patched, patchCalled := newScopeMappingTestServer(t,
		[]string{"openid-uuid", "managed-email-uuid", "profile-uuid"}, true)
	defer server.Close()

	client := NewClient(server.URL, "test-token")
	if err := client.ensureProviderEmailScopeMapping(7); err != nil {
		t.Fatalf("ensureProviderEmailScopeMapping() error: %v", err)
	}
	if !*patchCalled {
		t.Fatal("expected a PATCH to swap the managed email mapping")
	}
	want := []string{"openid-uuid", "bloud-email-uuid", "profile-uuid"}
	if len(*patched) != len(want) {
		t.Fatalf("patched = %v, want %v", *patched, want)
	}
	for i := range want {
		if (*patched)[i] != want[i] {
			t.Fatalf("patched = %v, want %v", *patched, want)
		}
	}
}

func TestEnsureProviderEmailScopeMappingAppendsMissing(t *testing.T) {
	// A provider with no email mapping at all gets Bloud's verified mapping
	// appended.
	server, patched, patchCalled := newScopeMappingTestServer(t,
		[]string{"openid-uuid", "profile-uuid"}, true)
	defer server.Close()

	client := NewClient(server.URL, "test-token")
	if err := client.ensureProviderEmailScopeMapping(7); err != nil {
		t.Fatalf("ensureProviderEmailScopeMapping() error: %v", err)
	}
	if !*patchCalled {
		t.Fatal("expected a PATCH to add the missing email mapping")
	}
	want := []string{"openid-uuid", "profile-uuid", "bloud-email-uuid"}
	if len(*patched) != len(want) {
		t.Fatalf("patched = %v, want %v", *patched, want)
	}
	for i := range want {
		if (*patched)[i] != want[i] {
			t.Fatalf("patched = %v, want %v", *patched, want)
		}
	}
}
