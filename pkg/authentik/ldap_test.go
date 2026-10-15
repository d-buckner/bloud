// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeLDAPAuthentik serves the endpoints ensureLDAPProvider reads and records
// every write it receives. provider is nil when no LDAP provider exists yet.
type fakeLDAPAuthentik struct {
	flows    map[string]string
	provider map[string]any
	writes   []ldapWrite
}

type ldapWrite struct {
	method string
	path   string
	body   map[string]any
}

func (f *fakeLDAPAuthentik) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/flows/instances/{slug}/", func(w http.ResponseWriter, r *http.Request) {
		pk, ok := f.flows[r.PathValue("slug")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"pk": pk})
	})
	mux.HandleFunc("GET /api/v3/providers/ldap/{$}", func(w http.ResponseWriter, _ *http.Request) {
		results := []map[string]any{}
		if f.provider != nil {
			results = append(results, map[string]any{"pk": 7, "name": ldapProviderName})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	mux.HandleFunc("GET /api/v3/providers/ldap/7/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(f.provider)
	})
	mux.HandleFunc("PATCH /api/v3/providers/ldap/7/", func(w http.ResponseWriter, r *http.Request) {
		f.record(t, r)
		_ = json.NewEncoder(w).Encode(f.provider)
	})
	mux.HandleFunc("POST /api/v3/providers/ldap/{$}", func(w http.ResponseWriter, r *http.Request) {
		f.record(t, r)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"pk": 7})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	return mux
}

func (f *fakeLDAPAuthentik) record(t *testing.T, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s %s: %v", r.Method, r.URL.Path, err)
	}
	f.writes = append(f.writes, ldapWrite{r.Method, r.URL.Path, body})
}

func runEnsureLDAPProvider(t *testing.T, f *fakeLDAPAuthentik) (int, error) {
	t.Helper()
	server := httptest.NewServer(f.handler(t))
	t.Cleanup(server.Close)
	return NewClient(server.URL, "test-token").ensureLDAPProvider(context.Background(), "admins-pk")
}

var ldapTestFlows = map[string]string{
	"default-authentication-flow":        "default-flow-pk",
	ldapBindFlowSlug:                     "bind-flow-pk",
	"default-provider-invalidation-flow": "invalidation-flow-pk",
}

func TestEnsureLDAPProvider_CreatesOnTheBindFlowInCachedMode(t *testing.T) {
	f := &fakeLDAPAuthentik{flows: ldapTestFlows}

	pk, err := runEnsureLDAPProvider(t, f)
	if err != nil {
		t.Fatalf("ensureLDAPProvider: %v", err)
	}
	if pk != 7 {
		t.Errorf("pk = %d, want 7", pk)
	}
	if len(f.writes) != 1 || f.writes[0].method != http.MethodPost {
		t.Fatalf("writes = %+v, want one POST", f.writes)
	}
	body := f.writes[0].body
	if body["authorization_flow"] != "bind-flow-pk" || body["bind_mode"] != "cached" || body["search_mode"] != "direct" {
		t.Errorf("create payload = %v, want the bind flow in cached bind mode", body)
	}
}

// An install created before the bind cache has a provider on the 90-day default
// flow in direct mode. Both must move in one write: cached mode left on the
// default flow would remember a bind for 90 days.
func TestEnsureLDAPProvider_MovesAnExistingDirectProviderOntoTheBindFlow(t *testing.T) {
	f := &fakeLDAPAuthentik{
		flows: ldapTestFlows,
		provider: map[string]any{
			"authorization_flow": "default-flow-pk",
			"bind_mode":          "direct",
			"search_mode":        "direct",
		},
	}

	if _, err := runEnsureLDAPProvider(t, f); err != nil {
		t.Fatalf("ensureLDAPProvider: %v", err)
	}
	if len(f.writes) != 1 || f.writes[0].method != http.MethodPatch {
		t.Fatalf("writes = %+v, want one PATCH", f.writes)
	}
	body := f.writes[0].body
	if body["authorization_flow"] != "bind-flow-pk" || body["bind_mode"] != "cached" || body["search_mode"] != "direct" {
		t.Errorf("patch = %v, want the bind flow in cached bind mode", body)
	}
}

func TestEnsureLDAPProvider_ConvergedProviderIsReadOnly(t *testing.T) {
	f := &fakeLDAPAuthentik{
		flows: ldapTestFlows,
		provider: map[string]any{
			"authorization_flow": "bind-flow-pk",
			"bind_mode":          "cached",
			"search_mode":        "direct",
		},
	}

	if _, err := runEnsureLDAPProvider(t, f); err != nil {
		t.Fatalf("ensureLDAPProvider: %v", err)
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %+v, want none on a converged provider", f.writes)
	}
}

// Without the bind flow there is no safe cached configuration, so the provider
// is left exactly as it is and the pass reports the missing flow.
func TestEnsureLDAPProvider_MissingBindFlowWritesNothing(t *testing.T) {
	f := &fakeLDAPAuthentik{
		flows: map[string]string{"default-authentication-flow": "default-flow-pk"},
		provider: map[string]any{
			"authorization_flow": "default-flow-pk",
			"bind_mode":          "direct",
			"search_mode":        "direct",
		},
	}

	if _, err := runEnsureLDAPProvider(t, f); err == nil {
		t.Fatal("ensureLDAPProvider succeeded without the bind flow")
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %+v, want none", f.writes)
	}
}
