// SPDX-License-Identifier: AGPL-3.0-only

package servarr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const (
	testApp    = "sonarr"
	testAPI    = "api/v3"
	testAPIKey = "0123456789abcdef0123456789abcdef"
	hostPath   = "/api/v3/config/host"
)

// recordedRequest is one request the fake instance received.
type recordedRequest struct {
	method string
	path   string
	apiKey string
	body   []byte
}

// fakeHostConfig stands in for a Servarr instance's
// /api/{apiPath}/config/host resource: it reports the auth mode it is in and
// switches to whatever a successful PUT sets, exactly like a real instance.
type fakeHostConfig struct {
	mu       sync.Mutex
	mode     string
	requests []recordedRequest

	// getStatus/getBody override the GET response (error status, undecodable
	// body). putStatus makes PUT fail with that status.
	getStatus int
	getBody   string
	putStatus int
	// ignorePuts accepts a PUT with 200 but keeps the old mode, modelling an
	// instance that silently refuses the change.
	ignorePuts bool
}

func (f *fakeHostConfig) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{
			method: r.Method,
			path:   r.URL.Path,
			apiKey: r.Header.Get(apiKeyHeader),
			body:   body,
		})
		mode, getStatus, getBody, putStatus, ignorePuts :=
			f.mode, f.getStatus, f.getBody, f.putStatus, f.ignorePuts
		f.mu.Unlock()

		switch r.Method {
		case http.MethodGet:
			if getStatus != 0 {
				w.WriteHeader(getStatus)
				_, _ = io.WriteString(w, "denied")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if getBody != "" {
				_, _ = io.WriteString(w, getBody)
				return
			}
			_, _ = fmt.Fprintf(w,
				`{"authenticationMethod":%q,"authenticationRequired":"enabled","branch":"main","allowedHosts":"*"}`,
				mode)
		case http.MethodPut:
			if putStatus != 0 {
				w.WriteHeader(putStatus)
				_, _ = io.WriteString(w, "rejected")
				return
			}
			var doc map[string]any
			_ = json.Unmarshal(body, &doc)
			next, _ := doc[authMethodField].(string)
			f.mu.Lock()
			if !ignorePuts {
				f.mode = next
			}
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

// calls returns the recorded requests for one method.
func (f *fakeHostConfig) calls(method string) []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedRequest
	for _, r := range f.requests {
		if r.method == method {
			out = append(out, r)
		}
	}
	return out
}

// modeNow returns the auth mode the fake currently reports.
func (f *fakeHostConfig) modeNow() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mode
}

// clientForServer points a Client at an httptest server. The zero
// ClientFactory is usable (lazy defaults), so no Deps plumbing is needed.
func clientForServer(t *testing.T, fake *fakeHostConfig) *Client {
	t.Helper()
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	return NewClient(configurator.ClientFactory{}, testApp, testAPI, staticBaseURL(server.URL))
}

// staticBaseURL adapts a fixed URL to the client's func() string seam.
func staticBaseURL(u string) func() string {
	return func() string { return u }
}

func TestClientEnsureExternalAuth_AlreadyExternal(t *testing.T) {
	// Servarr parses the enum case-insensitively, so a form the app itself
	// might have written must not trigger a repair.
	for _, mode := range []string{"external", "External"} {
		t.Run(mode, func(t *testing.T) {
			fake := &fakeHostConfig{mode: mode}
			c := clientForServer(t, fake)

			changed, err := c.EnsureExternalAuth(context.Background(), testAPIKey)
			if err != nil {
				t.Fatalf("EnsureExternalAuth() error = %v", err)
			}
			if changed {
				t.Error("EnsureExternalAuth() changed = true, want false")
			}

			gets := fake.calls(http.MethodGet)
			if len(gets) != 1 {
				t.Fatalf("GET count = %d, want 1", len(gets))
			}
			if gets[0].path != hostPath {
				t.Errorf("GET path = %q, want %q", gets[0].path, hostPath)
			}
			if gets[0].apiKey != testAPIKey {
				t.Errorf("GET %s = %q, want %q", apiKeyHeader, gets[0].apiKey, testAPIKey)
			}
			if puts := fake.calls(http.MethodPut); len(puts) != 0 {
				t.Errorf("PUT count = %d, want 0 for an already-external instance", len(puts))
			}
		})
	}
}

func TestClientEnsureExternalAuth_RepairsNonExternalMode(t *testing.T) {
	fake := &fakeHostConfig{mode: "forms"}
	c := clientForServer(t, fake)

	changed, err := c.EnsureExternalAuth(context.Background(), testAPIKey)
	if err != nil {
		t.Fatalf("EnsureExternalAuth() error = %v", err)
	}
	if !changed {
		t.Error("EnsureExternalAuth() changed = false, want true after repairing a non-external instance")
	}

	if gets := fake.calls(http.MethodGet); len(gets) != 2 {
		t.Fatalf("GET count = %d, want 2 (read, then verify after the write)", len(gets))
	}
	puts := fake.calls(http.MethodPut)
	if len(puts) != 1 {
		t.Fatalf("PUT count = %d, want 1", len(puts))
	}
	if puts[0].apiKey != testAPIKey {
		t.Errorf("PUT %s = %q, want %q", apiKeyHeader, puts[0].apiKey, testAPIKey)
	}

	var sent map[string]any
	if err := json.Unmarshal(puts[0].body, &sent); err != nil {
		t.Fatalf("PUT body is not JSON: %v (%s)", err, puts[0].body)
	}
	if got := sent[authMethodField]; got != fieldAuthExternal {
		t.Errorf("PUT %s = %v, want %q", authMethodField, got, fieldAuthExternal)
	}
	if got := sent[authRequiredField]; got != fieldAuthEnabled {
		t.Errorf("PUT %s = %v, want %q", authRequiredField, got, fieldAuthEnabled)
	}
	// Read-modify-write: fields the app reported must be echoed back, because
	// the endpoint rejects a document with a null AllowedHosts or empty Branch.
	for field, want := range map[string]any{"branch": "main", "allowedHosts": "*"} {
		if got := sent[field]; got != want {
			t.Errorf("PUT %s = %v, want %v (field must be preserved, not dropped)", field, got, want)
		}
	}
	if got := fake.modeNow(); got != fieldAuthExternal {
		t.Errorf("instance mode after PUT = %q, want %q", got, fieldAuthExternal)
	}
}

func TestClientEnsureExternalAuth_RepairNotTakingEffect(t *testing.T) {
	fake := &fakeHostConfig{mode: "forms", ignorePuts: true}
	c := clientForServer(t, fake)

	_, err := c.EnsureExternalAuth(context.Background(), testAPIKey)
	if err == nil {
		t.Fatal("EnsureExternalAuth() error = nil, want an error when the instance keeps reporting the old mode")
	}
	if !strings.Contains(err.Error(), `"forms"`) {
		t.Errorf("error %q does not name the observed value", err)
	}
	if !strings.Contains(err.Error(), authMethodField) {
		t.Errorf("error %q does not name the %s field", err, authMethodField)
	}
}

func TestClientEnsureExternalAuth_PutFailureSurfacesError(t *testing.T) {
	fake := &fakeHostConfig{mode: "forms", putStatus: http.StatusForbidden}
	c := clientForServer(t, fake)

	_, err := c.EnsureExternalAuth(context.Background(), testAPIKey)
	if err == nil {
		t.Fatal("EnsureExternalAuth() error = nil, want the PUT failure")
	}
	for _, want := range []string{testApp, "403"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if gets := fake.calls(http.MethodGet); len(gets) != 1 {
		t.Errorf("GET count = %d, want 1 (no verification after a failed write)", len(gets))
	}
}

func TestClientEnsureExternalAuth_NonSuccessGetSurfacesError(t *testing.T) {
	fake := &fakeHostConfig{mode: "external", getStatus: http.StatusUnauthorized}
	c := clientForServer(t, fake)

	_, err := c.EnsureExternalAuth(context.Background(), testAPIKey)
	if err == nil {
		t.Fatal("EnsureExternalAuth() error = nil, want the 401")
	}
	for _, want := range []string{testApp, "401"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if puts := fake.calls(http.MethodPut); len(puts) != 0 {
		t.Errorf("PUT count = %d, want 0: an unreadable config must not be overwritten", len(puts))
	}
}

func TestClientEnsureExternalAuth_DecodeFailureSurfacesError(t *testing.T) {
	fake := &fakeHostConfig{mode: "external", getBody: "<html>login</html>"}
	c := clientForServer(t, fake)

	_, err := c.EnsureExternalAuth(context.Background(), testAPIKey)
	if err == nil {
		t.Fatal("EnsureExternalAuth() error = nil, want a decode error")
	}
	for _, want := range []string{testApp, "decode JSON"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if puts := fake.calls(http.MethodPut); len(puts) != 0 {
		t.Errorf("PUT count = %d, want 0", len(puts))
	}
}

func TestClientEnsureExternalAuth_NullDocumentSurfacesError(t *testing.T) {
	fake := &fakeHostConfig{mode: "external", getBody: "null"}
	c := clientForServer(t, fake)

	_, err := c.EnsureExternalAuth(context.Background(), testAPIKey)
	if err == nil {
		t.Fatal("EnsureExternalAuth() error = nil, want an error for a null document")
	}
	if !strings.Contains(err.Error(), "empty JSON document") {
		t.Errorf("error %q does not explain the null document", err)
	}
}

// TestNewClient_NormalisesAPIPath pins the parameter contract: callers pass
// the versioned root with or without slashes, and the request path is always
// /<root>/config/host (Prowlarr's root is api/v1).
func TestNewClient_NormalisesAPIPath(t *testing.T) {
	for _, apiPath := range []string{"api/v1", "/api/v1/", "api/v1/"} {
		fake := &fakeHostConfig{mode: "external"}
		server := httptest.NewServer(fake.handler())
		t.Cleanup(server.Close)

		c := NewClient(configurator.ClientFactory{}, "prowlarr", apiPath, staticBaseURL(server.URL))
		if _, err := c.EnsureExternalAuth(context.Background(), testAPIKey); err != nil {
			t.Fatalf("EnsureExternalAuth(%q) error = %v", apiPath, err)
		}
		gets := fake.calls(http.MethodGet)
		if len(gets) != 1 || gets[0].path != "/api/v1/config/host" {
			t.Errorf("apiPath %q produced GET %v, want /api/v1/config/host", apiPath, gets)
		}
	}
}
