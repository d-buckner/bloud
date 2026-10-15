// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordingAuthentik is a fake Authentik API that records the requests it
// served in order, so a test can assert not just what got deleted but the
// sequence. The order is load-bearing in DeleteAppSSO: the application link is
// what makes a provider findable, so it has to be read before the application
// is gone.
type recordingAuthentik struct {
	mu       sync.Mutex
	requests []string // "METHOD path"
	oauth2   []ProviderResponse
	proxy    []ProviderResponse
}

func (f *recordingAuthentik) served(method, pathSubstring string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if strings.HasPrefix(r, method+" ") && strings.Contains(r, pathSubstring) {
			return true
		}
	}
	return false
}

func (f *recordingAuthentik) order() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.requests))
	copy(out, f.requests)
	return out
}

func (f *recordingAuthentik) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.mu.Unlock()

		switch r.Method {
		case http.MethodGet:
			results := f.proxy
			if strings.HasPrefix(r.URL.Path, "/api/v3/providers/oauth2/") {
				results = f.oauth2
			}
			resp := PaginatedResponse{Results: results}
			resp.Pagination.Count = len(results)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func newRecordingClient(t *testing.T, f *recordingAuthentik) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "test-token")
}

// A provider's name embeds the display name it was created with. When the
// catalog renames an app, the name-based delete searches for a string nothing
// produces any more, finds nothing, and reports success while the credential
// stays behind. The application link is keyed on the catalog ID, which cannot
// drift, so it is what the delete must use.
func TestDeleteAppSSO_FindsProviderByApplicationSlugNotDisplayName(t *testing.T) {
	f := &recordingAuthentik{oauth2: []ProviderResponse{
		{PK: 2, Name: "Bloud OAuth2 Provider"},
		{PK: 9, Name: "Hermes Dashboard OAuth2 Provider", AssignedApplicationSlug: "hermes"},
	}}
	c := newRecordingClient(t, f)

	// "Hermes" is the display name now; the provider was created under a
	// different one. The name lookup would miss it.
	if err := c.DeleteAppSSO(context.Background(), "hermes", "Hermes", "native-oidc"); err != nil {
		t.Fatalf("DeleteAppSSO: %v", err)
	}

	if !f.served(http.MethodDelete, "/api/v3/providers/oauth2/9/") {
		t.Errorf("the provider attached to the app slug must be deleted; requests: %v", f.order())
	}
	if !f.served(http.MethodDelete, "/api/v3/core/applications/hermes/") {
		t.Errorf("the application must be deleted; requests: %v", f.order())
	}
	if f.served(http.MethodDelete, "/api/v3/providers/oauth2/2/") {
		t.Errorf("must not delete a provider belonging to another application; requests: %v", f.order())
	}
}

// Deleting the application first clears assigned_application_slug on the
// provider it owned, which is the field the find-by-application lookup reads.
// A reorder would make the provider unfindable rather than deleted.
func TestDeleteAppSSO_DeletesProviderBeforeApplication(t *testing.T) {
	f := &recordingAuthentik{oauth2: []ProviderResponse{
		{PK: 11, Name: "Vaultwarden OAuth2 Provider", AssignedApplicationSlug: "vaultwarden"},
	}}
	c := newRecordingClient(t, f)

	if err := c.DeleteAppSSO(context.Background(), "vaultwarden", "Vaultwarden", "native-oidc"); err != nil {
		t.Fatalf("DeleteAppSSO: %v", err)
	}

	var providerAt, appAt = -1, -1
	for i, r := range f.order() {
		switch r {
		case "DELETE /api/v3/providers/oauth2/11/":
			providerAt = i
		case "DELETE /api/v3/core/applications/vaultwarden/":
			appAt = i
		}
	}
	if providerAt == -1 || appAt == -1 {
		t.Fatalf("expected both deletes, got %v", f.order())
	}
	if providerAt > appAt {
		t.Errorf("provider (idx %d) must be deleted before the application (idx %d): %v",
			providerAt, appAt, f.order())
	}
}

func TestDeleteAppSSO_ForwardAuthDeletesProxyProviderByApplication(t *testing.T) {
	f := &recordingAuthentik{proxy: []ProviderResponse{
		{PK: 4, Name: "Something Else Proxy Provider", AssignedApplicationSlug: "other"},
		{PK: 7, Name: "Old Name Proxy Provider", AssignedApplicationSlug: "navidrome"},
	}}
	c := newRecordingClient(t, f)

	if err := c.DeleteAppSSO(context.Background(), "navidrome", "Navidrome", "forward-auth"); err != nil {
		t.Fatalf("DeleteAppSSO: %v", err)
	}
	if !f.served(http.MethodDelete, "/api/v3/providers/proxy/7/") {
		t.Errorf("the app's proxy provider must be deleted; requests: %v", f.order())
	}
	if f.served(http.MethodDelete, "/api/v3/providers/proxy/4/") {
		t.Errorf("must not touch another app's proxy provider; requests: %v", f.order())
	}
	// The proxy provider must not be looked up in the oauth2 collection, and
	// vice versa: a wrong providerType would delete the wrong object class.
	if f.served(http.MethodDelete, "/api/v3/providers/oauth2/") {
		t.Errorf("forward-auth must not delete from the oauth2 collection; requests: %v", f.order())
	}
}

// "ldap" authenticates through the shared outpost and "none" joins the
// provider not at all: neither owns a per-app provider, so only the
// application goes.
func TestDeleteAppSSO_StrategiesWithoutAPerAppProvider(t *testing.T) {
	for _, strategy := range []string{"ldap", "none", ""} {
		t.Run("strategy_"+strategy, func(t *testing.T) {
			f := &recordingAuthentik{
				oauth2: []ProviderResponse{{PK: 3, Name: "Jellyfin OAuth2 Provider", AssignedApplicationSlug: "jellyfin"}},
				proxy:  []ProviderResponse{{PK: 8, Name: "Jellyfin Proxy Provider", AssignedApplicationSlug: "jellyfin"}},
			}
			c := newRecordingClient(t, f)

			if err := c.DeleteAppSSO(context.Background(), "jellyfin", "Jellyfin", strategy); err != nil {
				t.Fatalf("DeleteAppSSO(%q): %v", strategy, err)
			}
			if !f.served(http.MethodDelete, "/api/v3/core/applications/jellyfin/") {
				t.Errorf("the application must still be deleted for %q; requests: %v", strategy, f.order())
			}
			if f.served(http.MethodDelete, "/api/v3/providers/") {
				t.Errorf("strategy %q owns no per-app provider, nothing under providers/ may be deleted; requests: %v",
					strategy, f.order())
			}
		})
	}
}

// A provider that exists but carries no application link is still caught by
// the name the provisioning path would have used for it.
func TestDeleteAppSSO_FallsBackToNameWhenProviderIsUnlinked(t *testing.T) {
	f := &recordingAuthentik{oauth2: []ProviderResponse{
		{PK: 21, Name: "Paperless-ngx OAuth2 Provider", AssignedApplicationSlug: ""},
	}}
	c := newRecordingClient(t, f)

	if err := c.DeleteAppSSO(context.Background(), "paperless-ngx", "Paperless-ngx", "native-oidc"); err != nil {
		t.Fatalf("DeleteAppSSO: %v", err)
	}
	if !f.served(http.MethodDelete, "/api/v3/providers/oauth2/21/") {
		t.Errorf("an unlinked provider matching the display-name pattern must still be deleted; requests: %v", f.order())
	}
}

// Uninstalling twice, or uninstalling an app whose objects were already
// removed by hand, must not error: every delete treats "already gone" as
// success and every lookup treats an empty list as nothing to do.
func TestDeleteAppSSO_IdempotentWhenNothingExists(t *testing.T) {
	f := &recordingAuthentik{}
	c := newRecordingClient(t, f)

	for i := 0; i < 2; i++ {
		if err := c.DeleteAppSSO(context.Background(), "gone-app", "Gone App", "native-oidc"); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	if !f.served(http.MethodDelete, "/api/v3/core/applications/gone-app/") {
		t.Errorf("expected the application delete to be attempted; requests: %v", f.order())
	}
}

func TestProviderTypeForStrategy(t *testing.T) {
	cases := map[string]struct {
		providerType string
		ok           bool
	}{
		"native-oidc":  {"oauth2", true},
		"forward-auth": {"proxy", true},
		"ldap":         {"", false},
		"none":         {"", false},
		"":             {"", false},
		"bogus":        {"", false},
	}
	for strategy, want := range cases {
		got, ok := providerTypeForStrategy(strategy)
		if got != want.providerType || ok != want.ok {
			t.Errorf("providerTypeForStrategy(%q) = (%q, %v), want (%q, %v)",
				strategy, got, ok, want.providerType, want.ok)
		}
	}
}

func TestProviderNameForStrategyMatchesProvisioning(t *testing.T) {
	if got := providerNameForStrategy("native-oidc", "Vaultwarden"); got != "Vaultwarden OAuth2 Provider" {
		t.Errorf("native-oidc provider name = %q", got)
	}
	if got := providerNameForStrategy("forward-auth", "Navidrome"); got != "Navidrome Proxy Provider" {
		t.Errorf("forward-auth provider name = %q", got)
	}
	if got := providerNameForStrategy("ldap", "Jellyfin"); got != "" {
		t.Errorf("ldap must not name a per-app provider, got %q", got)
	}
}
