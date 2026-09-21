// SPDX-License-Identifier: AGPL-3.0-only

package servarr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

const (
	// downloadClientPath is the /downloadclient collection under the api/v3
	// root the tests use.
	downloadClientPath = "/api/v3/downloadclient"
	// downloadClientTestPath is the contract-validation endpoint.
	downloadClientTestPath = downloadClientPath + "/test"
)

// testDownloadClientSpec is the Sonarr shape of the client; tests that need
// Radarr's swap CategoryField/Category.
func testDownloadClientSpec() DownloadClientSpec {
	return DownloadClientSpec{
		APIPath:       testAPI,
		APIKey:        testAPIKey,
		Host:          "apps-qbittorrent",
		Port:          8081,
		CategoryField: "tvCategory",
		Category:      "tv-sonarr",
	}
}

// fakeDownloadClients stands in for one instance's /downloadclient resource:
// it stores what a POST creates, reports it from the next GET, and records
// every request. listStatus/testStatus override those responses.
type fakeDownloadClients struct {
	mu         sync.Mutex
	clients    []downloadClientResource
	nextID     int
	listStatus int
	testStatus int
	// listBody, when set, is what the list read answers verbatim.
	listBody []byte
	requests []recordedRequest
}

func (f *fakeDownloadClients) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{
			method: r.Method,
			path:   r.URL.Path,
			apiKey: r.Header.Get(APIKeyHeader),
			body:   body,
		})
		listStatus, testStatus := f.listStatus, f.testStatus
		f.mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == downloadClientTestPath:
			if testStatus != 0 {
				w.WriteHeader(testStatus)
				_, _ = io.WriteString(w, `{"isValid":false}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"isValid":true}`)
		case r.Method == http.MethodGet && r.URL.Path == downloadClientPath:
			if listStatus != 0 {
				w.WriteHeader(listStatus)
				_, _ = io.WriteString(w, "denied")
				return
			}
			f.mu.Lock()
			raw := append([]byte(nil), f.listBody...)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if raw != nil {
				_, _ = w.Write(raw)
				return
			}
			_ = json.NewEncoder(w).Encode(f.clientsNow())
		case r.Method == http.MethodPost && r.URL.Path == downloadClientPath:
			var dc downloadClientResource
			_ = json.Unmarshal(body, &dc)
			f.mu.Lock()
			f.nextID++
			dc.ID = f.nextID
			f.clients = append(f.clients, dc)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, downloadClientPath+"/"):
			id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, downloadClientPath+"/"))
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			kept := []downloadClientResource{}
			f.mu.Lock()
			for _, dc := range f.clients {
				if dc.ID != id {
					kept = append(kept, dc)
				}
			}
			f.clients = kept
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// requestsTo returns the recorded requests for one method and path.
func (f *fakeDownloadClients) requestsTo(method, path string) []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedRequest
	for _, r := range f.requests {
		if r.method == method && r.path == path {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeDownloadClients) clientsNow() []downloadClientResource {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]downloadClientResource{}, f.clients...)
}

// postPaths returns the path of every POST the fake received, in order.
func (f *fakeDownloadClients) postPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if r.method == http.MethodPost {
			out = append(out, r.path)
		}
	}
	return out
}

// downloadClientClient points an appclient at an httptest fake. The zero
// ClientFactory is usable, so no Deps plumbing is needed.
func downloadClientClient(t *testing.T, fake *fakeDownloadClients) *appclient.Client {
	t.Helper()
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	return appclient.New(appclient.Spec{Name: testApp, BaseURLFn: staticBaseURL(server.URL)})
}

// assertQBittorrentPayload pins the body the pinned images' schema expects:
// the identity, the toggles, and every settings field (including the ones
// left empty), so the stored entry does not depend on app defaults.
func assertQBittorrentPayload(t *testing.T, body []byte, categoryField, category string) {
	t.Helper()

	var doc struct {
		Name           string                `json:"name"`
		Implementation string                `json:"implementation"`
		ConfigContract string                `json:"configContract"`
		Protocol       string                `json:"protocol"`
		Priority       int                   `json:"priority"`
		Enable         bool                  `json:"enable"`
		Tags           []string              `json:"tags"`
		Fields         []downloadClientField `json:"fields"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("payload is not JSON: %v (%s)", err, body)
	}

	if doc.Name != downloadClientName {
		t.Errorf("payload name = %q, want %q", doc.Name, downloadClientName)
	}
	if doc.Implementation != ImplementationQBittorrent {
		t.Errorf("payload implementation = %q, want %q", doc.Implementation, ImplementationQBittorrent)
	}
	if doc.ConfigContract != downloadClientContract {
		t.Errorf("payload configContract = %q, want %q", doc.ConfigContract, downloadClientContract)
	}
	if doc.Protocol != downloadClientProtocol {
		t.Errorf("payload protocol = %q, want %q", doc.Protocol, downloadClientProtocol)
	}
	if doc.Priority != downloadClientPriority {
		t.Errorf("payload priority = %d, want %d", doc.Priority, downloadClientPriority)
	}
	if !doc.Enable {
		t.Error("payload enable = false, want true")
	}
	if doc.Tags == nil || len(doc.Tags) != 0 {
		t.Errorf("payload tags = %v, want an empty array (not null)", doc.Tags)
	}

	fields := map[string]any{}
	for _, f := range doc.Fields {
		fields[f.Name] = f.Value
	}
	want := map[string]any{
		"host":        "apps-qbittorrent",
		"port":        float64(8081),
		"useSsl":      false,
		"urlBase":     "",
		"username":    "",
		"password":    "",
		categoryField: category,
	}
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("payload fields = %v, want %v", fields, want)
	}
}

func TestEnsureDownloadClient_AddsAndTests(t *testing.T) {
	// The only per-app difference is the category field name and value: the
	// two PVRs share the QBittorrentSettings contract.
	for _, tc := range []struct{ name, field, category string }{
		{"sonarr", "tvCategory", "tv-sonarr"},
		{"radarr", "movieCategory", "movie-radarr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDownloadClients{}
			spec := testDownloadClientSpec()
			spec.CategoryField, spec.Category = tc.field, tc.category

			changed, err := EnsureDownloadClient(context.Background(), downloadClientClient(t, fake), spec)
			if err != nil {
				t.Fatalf("EnsureDownloadClient() error = %v", err)
			}
			if !changed {
				t.Error("EnsureDownloadClient() changed = false, want true when the client was added")
			}

			// Order is load-bearing: /downloadclient/test runs the resource's
			// shared validator, whose uniqueness rule rejects a name that is
			// already stored, so the document has to be validated first.
			if got, want := fake.postPaths(), []string{downloadClientTestPath, downloadClientPath}; !reflect.DeepEqual(got, want) {
				t.Errorf("POST order = %v, want %v", got, want)
			}

			creates := fake.requestsTo(http.MethodPost, downloadClientPath)
			tests := fake.requestsTo(http.MethodPost, downloadClientTestPath)
			if len(creates) != 1 || len(tests) != 1 {
				t.Fatalf("POST %s = %d and POST %s = %d, want 1 each",
					downloadClientPath, len(creates), downloadClientTestPath, len(tests))
			}
			if creates[0].apiKey != testAPIKey {
				t.Errorf("POST %s = %q, want %q", APIKeyHeader, creates[0].apiKey, testAPIKey)
			}
			assertQBittorrentPayload(t, creates[0].body, tc.field, tc.category)
			if string(tests[0].body) != string(creates[0].body) {
				t.Errorf("test body = %s, want the stored body %s", tests[0].body, creates[0].body)
			}
			if got := fake.clientsNow(); len(got) != 1 {
				t.Errorf("stored clients = %v, want 1", got)
			}
		})
	}
}

func TestEnsureDownloadClient_AlreadyPresent(t *testing.T) {
	fake := &fakeDownloadClients{clients: []downloadClientResource{bloudClientResource(7)}}
	spec := testDownloadClientSpec()

	changed, err := EnsureDownloadClient(context.Background(), downloadClientClient(t, fake), spec)
	if err != nil {
		t.Fatalf("EnsureDownloadClient() error = %v", err)
	}
	if changed {
		t.Error("EnsureDownloadClient() changed = true, want false for an instance that already has one")
	}
	if gets := fake.requestsTo(http.MethodGet, downloadClientPath); len(gets) != 1 {
		t.Errorf("GET %s count = %d, want 1", downloadClientPath, len(gets))
	}
	for _, path := range []string{downloadClientPath, downloadClientTestPath} {
		if posts := fake.requestsTo(http.MethodPost, path); len(posts) != 0 {
			t.Errorf("POST %s count = %d, want 0 for an already-present client", path, len(posts))
		}
	}
}

// bloudClientResource is the entry Bloud stores: the qBittorrent implementation
// at its own provider's coordinates: the identity the package matches on.
func bloudClientResource(id int) downloadClientResource {
	return downloadClientResource{
		ID:             id,
		Name:           downloadClientName,
		Implementation: ImplementationQBittorrent,
		Fields: []downloadClientField{
			{Name: fieldHost, Value: "apps-qbittorrent"},
			{Name: fieldPort, Value: 8081},
		},
	}
}

// foreignClientResource is an entry the operator added: the same implementation
// at a different address (a seedbox). Bloud neither adopts it as its own nor
// deletes it.
func foreignClientResource(id int) downloadClientResource {
	return downloadClientResource{
		ID:             id,
		Name:           "seedbox",
		Implementation: ImplementationQBittorrent,
		Fields: []downloadClientField{
			{Name: fieldHost, Value: "seedbox.example.net"},
			{Name: fieldPort, Value: 8080},
		},
	}
}

// Another qBittorrent client is not this one: the instance is read, the entry
// is added beside it, and the operator's entry is left where it is.
func TestEnsureDownloadClient_AddsItsOwnBesideForeignEntries(t *testing.T) {
	fake := &fakeDownloadClients{clients: []downloadClientResource{foreignClientResource(4)}}

	changed, err := EnsureDownloadClient(context.Background(), downloadClientClient(t, fake), testDownloadClientSpec())
	if err != nil {
		t.Fatalf("EnsureDownloadClient() error = %v", err)
	}
	if !changed {
		t.Error("EnsureDownloadClient() changed = false, want true: Bloud's own client was missing")
	}
	tests := fake.requestsTo(http.MethodPost, downloadClientTestPath)
	if len(tests) != 1 {
		t.Errorf("POST %s count = %d, want 1: the new client is proven before it is stored", downloadClientTestPath, len(tests))
	}
	assertQBittorrentPayload(t, tests[0].body, "tvCategory", "tv-sonarr")

	stored := fake.clientsNow()
	if len(stored) != 2 {
		t.Fatalf("stored clients = %v, want the operator's entry plus Bloud's", stored)
	}
	if stored[0].Name != "seedbox" {
		t.Errorf("first stored client = %q, want the operator's entry untouched", stored[0].Name)
	}
	if got := stored[1].field(fieldHost); got != "apps-qbittorrent" {
		t.Errorf("added client host = %q, want apps-qbittorrent", got)
	}
}

// A list read that answers `null` is not an empty list: treating it as one
// would store a second client next to the one that exists.
func TestListDownloadClients_RejectsANullDocument(t *testing.T) {
	fake := &fakeDownloadClients{listBody: []byte("null")}

	_, err := EnsureDownloadClient(context.Background(), downloadClientClient(t, fake), testDownloadClientSpec())
	if err == nil {
		t.Fatal("EnsureDownloadClient() error = nil, want the null list rejected")
	}
	if !strings.Contains(err.Error(), "empty JSON document") {
		t.Errorf("error %q does not name the empty document", err)
	}
	if posts := fake.requestsTo(http.MethodPost, downloadClientPath); len(posts) != 0 {
		t.Errorf("POST %s count = %d, want 0", downloadClientPath, len(posts))
	}
}

func TestEnsureDownloadClient_TestFailureSurfacesStatus(t *testing.T) {
	fake := &fakeDownloadClients{testStatus: http.StatusBadRequest}

	changed, err := EnsureDownloadClient(context.Background(), downloadClientClient(t, fake), testDownloadClientSpec())
	if err == nil {
		t.Fatal("EnsureDownloadClient() error = nil, want the failed contract test")
	}
	if changed {
		t.Error("EnsureDownloadClient() changed = true, want false when the test failed")
	}
	for _, want := range []string{testApp, "400"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if tests := fake.requestsTo(http.MethodPost, downloadClientTestPath); len(tests) != 1 {
		t.Errorf("POST %s count = %d, want 1", downloadClientTestPath, len(tests))
	}
	if creates := fake.requestsTo(http.MethodPost, downloadClientPath); len(creates) != 0 {
		t.Errorf("POST %s count = %d, want 0: a document that failed its test must not be stored",
			downloadClientPath, len(creates))
	}
	if stored := fake.clientsNow(); len(stored) != 0 {
		t.Errorf("stored clients = %v, want none", stored)
	}
}

func TestEnsureDownloadClient_ListFailureSurfacesStatus(t *testing.T) {
	fake := &fakeDownloadClients{listStatus: http.StatusUnauthorized}

	_, err := EnsureDownloadClient(context.Background(), downloadClientClient(t, fake), testDownloadClientSpec())
	if err == nil {
		t.Fatal("EnsureDownloadClient() error = nil, want the 401")
	}
	for _, want := range []string{testApp, "401"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if posts := fake.requestsTo(http.MethodPost, downloadClientPath); len(posts) != 0 {
		t.Errorf("POST %s count = %d, want 0: an unreadable list must not be written over", downloadClientPath, len(posts))
	}
}

func TestRemoveDownloadClient_RemovesOnlyItsOwnEntry(t *testing.T) {
	fake := &fakeDownloadClients{clients: []downloadClientResource{
		bloudClientResource(1),
		{ID: 2, Implementation: "Transmission"},
		foreignClientResource(3),
	}}

	changed, err := RemoveDownloadClient(context.Background(), downloadClientClient(t, fake), testDownloadClientSpec())
	if err != nil {
		t.Fatalf("RemoveDownloadClient() error = %v", err)
	}
	if !changed {
		t.Error("RemoveDownloadClient() changed = false, want true")
	}
	var deleted []string
	fake.mu.Lock()
	for _, r := range fake.requests {
		if r.method == http.MethodDelete {
			deleted = append(deleted, r.path)
		}
	}
	fake.mu.Unlock()
	// Only the entry at Bloud's own address goes: another qBittorrent client is
	// the operator's, and the provider being gone says nothing about it.
	want := []string{downloadClientPath + "/1"}
	if !reflect.DeepEqual(deleted, want) {
		t.Errorf("DELETE paths = %v, want %v", deleted, want)
	}
	stored := fake.clientsNow()
	if len(stored) != 2 {
		t.Fatalf("stored clients after prune = %v, want the other two kept", stored)
	}
	if stored[1].Name != "seedbox" {
		t.Errorf("kept entries = %v, want the operator's seedbox untouched", stored)
	}
}

func TestRemoveDownloadClient_NoMatch(t *testing.T) {
	fake := &fakeDownloadClients{clients: []downloadClientResource{{ID: 4, Implementation: "Deluge"}}}

	changed, err := RemoveDownloadClient(context.Background(), downloadClientClient(t, fake), testDownloadClientSpec())
	if err != nil {
		t.Fatalf("RemoveDownloadClient() error = %v", err)
	}
	if changed {
		t.Error("RemoveDownloadClient() changed = true, want false when nothing matches")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, r := range fake.requests {
		if r.method != http.MethodGet {
			t.Errorf("unexpected %s %s, want only the list read", r.method, r.path)
		}
	}
}
