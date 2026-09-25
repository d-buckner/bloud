// SPDX-License-Identifier: AGPL-3.0-only

package prowlarr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/servarr"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/xmlutil"
)

const (
	// existingAPIKey is the 32-hex key an instance generates for itself; the
	// configurator must never replace it.
	existingAPIKey = "0123456789abcdef0123456789abcdef"
	// hostPath is the /config/host resource under Prowlarr's api/v1 root.
	hostPath = "/api/v1/config/host"

	// sonarrAPIKey/radarrAPIKey stand in for the keys the sibling PVRs publish
	// as their APIKey, which is where a binding carries one.
	sonarrAPIKey = "11111111111111111111111111111111"
	radarrAPIKey = "22222222222222222222222222222222"

	// sonarrNode/sonarrPort and radarrNode/radarrPort are the catalog's address
	// for each PVR: what the orchestrator resolves into a binding's Node, Port
	// and BaseURL. The configurator re-states none of this; the values are here
	// because the documents Bloud writes are pinned literally.
	sonarrNode = "apps-sonarr"
	sonarrPort = 8989
	radarrNode = "apps-radarr"
	radarrPort = 7878
)

var apiKeyPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// appState returns the state a reconciliation passes for one install:
// <tmp>/appdata is the app's own data dir, <tmp>/data the shared Bloud one. Its
// integration bindings are left empty, so a test that wires a PVR seeds them
// itself with withPvrs and a test that does not sees no PVR traffic at all.
func appState(t *testing.T) *configurator.AppState {
	t.Helper()
	root := t.TempDir()
	return &configurator.AppState{
		DataPath:      filepath.Join(root, "appdata"),
		BloudDataPath: filepath.Join(root, "data"),
	}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// pvrBinding is one binding the orchestrator resolves for a PVR: the catalog's
// address for it, plus the key it published. Installed is the whole of the "is
// this PVR there" question now (nothing here probes a PVR's port to find out),
// and an empty apiKey stands for a PVR that is installed but has not published
// its secret yet.
func pvrBinding(t *testing.T, appID string, installed bool, apiKey string) configurator.PVRBinding {
	t.Helper()

	var node string
	var port int
	switch appID {
	case sonarrAppID:
		node, port = sonarrNode, sonarrPort
	case radarrAppID:
		node, port = radarrNode, radarrPort
	default:
		t.Fatalf("unknown PVR %q", appID)
	}

	return configurator.PVRBinding{
		ProviderRef: configurator.ProviderRef{
			App:       appID,
			Installed: installed,
			Node:      node,
			Port:      port,
			// BaseURL is how the Prowlarr container reaches the PVR, which is
			// what Bloud stores; LocalURL is the host vantage point. They
			// differ on purpose: a document that stored the host address would
			// fail here.
			BaseURL:  "http://" + node + ":" + strconv.Itoa(port),
			LocalURL: "http://localhost:" + strconv.Itoa(port),
		},
		// The binding carries the key as a field, empty when the PVR has not
		// published one yet: no consumer names the secret.
		APIKey: apiKey,
	}
}

// withPvrs seeds the `pvr` contract with the bindings the orchestrator hands
// over. The slice carries an entry for every declared-compatible PVR, installed
// or not: that flag is what tells the reconcile "wire this PVR" from "the PVR
// is gone, so prune the entry Bloud wrote for it".
func withPvrs(state *configurator.AppState, bindings ...configurator.PVRBinding) {
	state.Integrations.PVRs = bindings
}

// --- fake Prowlarr ---

// recordedRequest is one request the fake instance received.
type recordedRequest struct {
	method string
	path   string
	apiKey string
	body   []byte
}

// fakeProwlarr stands in for a running Prowlarr instance: its
// /api/v1/config/host auth resource, and its /api/v1/applications resource
// with the /test endpoint. It mirrors the upstream behaviour the configurator
// depends on: 201 on create, 200 with an empty body on delete, and a masked
// apiKey on every read.
type fakeProwlarr struct {
	mu       sync.Mutex
	mode     string
	requests []recordedRequest

	// applications is the instance's application list; nextID is what a create
	// assigns.
	appsList []application
	nextID   int

	// testFails makes POST /applications/test answer the 400 a real instance
	// returns when the PVR rejects the document; tested records the documents
	// the endpoint received.
	testFails bool
	tested    []application
	// testStatus overrides the status /applications/test answers (0 → 200), for
	// the instance failing in its own right rather than rejecting the document.
	testStatus int

	// invalid marks stored entries POST /applications/testall reports as
	// unreachable: the verdict a real instance gives for an entry whose stored
	// key the PVR no longer accepts (a purged-and-reinstalled sibling).
	invalid map[int]bool
	// testAllStatus overrides the status /applications/testall answers (0 →
	// 200).
	testAllStatus int
}

func (f *fakeProwlarr) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{
			method: r.Method,
			path:   r.URL.Path,
			apiKey: r.Header.Get("X-Api-Key"),
			body:   body,
		})
		f.mu.Unlock()

		switch {
		case r.URL.Path == hostPath:
			f.serveHostConfig(w, r, body)
		case r.URL.Path == applicationTestPath:
			f.serveTest(w, body)
		case r.URL.Path == applicationTestAllPath:
			f.serveTestAll(w)
		case r.URL.Path == applicationsPath:
			f.serveApplications(w, r, body)
		case strings.HasPrefix(r.URL.Path, applicationsPath+"/"):
			f.serveApplication(w, r, body)
		default:
			http.NotFound(w, r)
		}
	})
}

// serveHostConfig reports the auth mode the instance is in and switches to
// whatever a PUT sets.
func (f *fakeProwlarr) serveHostConfig(w http.ResponseWriter, r *http.Request, body []byte) {
	if r.Method == http.MethodPut {
		var doc map[string]any
		_ = json.Unmarshal(body, &doc)
		if next, ok := doc["authenticationMethod"].(string); ok {
			f.mu.Lock()
			f.mode = next
			f.mu.Unlock()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"authenticationMethod":%q,"authenticationRequired":"enabled"}`, f.modeNow())
}

func (f *fakeProwlarr) serveApplications(w http.ResponseWriter, r *http.Request, body []byte) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.wireList())
	case http.MethodPost:
		var app application
		_ = json.Unmarshal(body, &app)

		// The resource's shared validator rejects a name another entry holds.
		if f.nameTaken(app) {
			writeValidationError(w, "Name", duplicateNameError)
			return
		}

		f.mu.Lock()
		f.nextID++
		app.ID = f.nextID
		f.appsList = append(f.appsList, app)
		stored := mask(app)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(stored)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// nameTaken reports whether another entry already holds the document's name,
// mirroring ProviderControllerBase's shared validator.
func (f *fakeProwlarr) nameTaken(app application) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, existing := range f.appsList {
		if existing.ID != app.ID && strings.EqualFold(existing.Name, app.Name) {
			return true
		}
	}
	return false
}

// writeValidationError renders the validation-error array Prowlarr answers a
// rejected document with.
func writeValidationError(w http.ResponseWriter, field, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = fmt.Fprintf(w, `[{"propertyName":%q,"errorMessage":%q,"severity":"error"}]`, field, message)
}

func (f *fakeProwlarr) serveApplication(w http.ResponseWriter, r *http.Request, body []byte) {
	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, applicationsPath+"/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		f.mu.Lock()
		for i, app := range f.appsList {
			if app.ID == id {
				f.appsList = append(f.appsList[:i], f.appsList[i+1:]...)
				break
			}
		}
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{}`)
	case http.MethodPut:
		// RestPutById: the same shared validator and connection test as a
		// create, then the entry is replaced in place and returned (masked).
		var app application
		_ = json.Unmarshal(body, &app)
		if f.nameTaken(app) {
			writeValidationError(w, "Name", duplicateNameError)
			return
		}

		f.mu.Lock()
		for i, existing := range f.appsList {
			if existing.ID == id {
				app.ID = id
				f.appsList[i] = app
				break
			}
		}
		stored := mask(app)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stored)
	default:
		http.NotFound(w, r)
	}
}

// serveTestAll models POST /applications/testall: the instance's own connection
// test for every entry it holds, answered as one {id, isValid} record each. A
// test marks an entry unreachable with markInvalid.
func (f *fakeProwlarr) serveTestAll(w http.ResponseWriter) {
	f.mu.Lock()
	status := f.testAllStatus
	if status == 0 {
		status = http.StatusOK
	}
	if status != http.StatusOK {
		f.mu.Unlock()
		http.Error(w, "testall failed", status)
		return
	}
	out := make([]map[string]any, 0, len(f.appsList))
	for _, app := range f.appsList {
		out = append(out, map[string]any{
			"id":                 app.ID,
			"isValid":            !f.invalid[app.ID],
			"validationFailures": []any{},
		})
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// markInvalid makes the fake report a stored entry as unreachable, the way a
// real instance does once the PVR behind it no longer accepts the key it holds.
func (f *fakeProwlarr) markInvalid(id int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.invalid == nil {
		f.invalid = map[int]bool{}
	}
	f.invalid[id] = true
}

func (f *fakeProwlarr) serveTest(w http.ResponseWriter, body []byte) {
	var app application
	_ = json.Unmarshal(body, &app)

	f.mu.Lock()
	f.tested = append(f.tested, app)
	fails := f.testFails
	status := f.testStatus
	f.mu.Unlock()

	// The instance failing in its own right: no verdict about the document, just
	// a status the caller has to classify (see servarr.TransientFailure).
	if status != 0 {
		http.Error(w, "the application test did not run", status)
		return
	}

	// The endpoint runs the resource's shared validator, so a name another
	// entry already holds is rejected before the connection is attempted. This
	// is what makes the order of the reconcile observable (and is why testing
	// after the create cannot work against a real instance).
	if f.nameTaken(app) {
		writeValidationError(w, "Name", duplicateNameError)
		return
	}

	if fails {
		// The shape Prowlarr returns for a failed test: a validation-error
		// array naming the field that failed.
		writeValidationError(w, "ApiKey", "API Key is invalid")
		return
	}
	_, _ = io.WriteString(w, `{}`)
}

// wireList renders the configured entries the way a real instance does: the
// stored document, the fields Prowlarr fills in from its own schema, and the
// apiKey replaced with the mask. The schema fields matter: their values are
// not strings (the sync-category arrays, the checkboxes) and an empty advanced
// field has no value key at all, which a reader that insists on strings cannot
// decode.
func (f *fakeProwlarr) wireList() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]map[string]any, 0, len(f.appsList))
	for _, app := range f.appsList {
		fields := make([]map[string]any, 0, len(app.Fields)+3)
		for _, field := range app.Fields {
			value := any(field.Value)
			if field.Name == fieldAPIKey && field.Value != "" {
				value = maskedSecret
			}
			fields = append(fields, map[string]any{"name": field.Name, "value": value})
		}
		fields = append(fields,
			map[string]any{"name": "syncCategories", "value": []int{5000, 5010, 5070}},
			map[string]any{"name": "animeSyncCategories", "value": []int{5070}},
			map[string]any{"name": "syncAnimeStandardFormatSearch", "value": true},
			map[string]any{"name": "authUsername"},
		)

		out = append(out, map[string]any{
			"id":                 app.ID,
			"name":               app.Name,
			"implementation":     app.Implementation,
			"implementationName": app.Implementation,
			"configContract":     app.ConfigContract,
			"syncLevel":          app.SyncLevel,
			"enable":             true,
			"tags":               app.Tags,
			"fields":             fields,
		})
	}
	return out
}

// mask replaces the apiKey with Prowlarr's privacy mask, mirroring
// SchemaBuilder.ToSchema: a stored key is never readable through the API.
func mask(app application) application {
	fields := make([]applicationField, len(app.Fields))
	copy(fields, app.Fields)
	for i := range fields {
		if fields[i].Name == fieldAPIKey && fields[i].Value != "" {
			fields[i].Value = maskedSecret
		}
	}
	app.Fields = fields
	return app
}

// indexOf returns the position of the first request matching a method and path
// in the order the fake received them, or -1.
func (f *fakeProwlarr) indexOf(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	for i, r := range f.requests {
		if r.method == method && r.path == path {
			return i
		}
	}
	return -1
}

// paths returns the path of every request the fake received.
func (f *fakeProwlarr) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]string, 0, len(f.requests))
	for _, r := range f.requests {
		out = append(out, r.path)
	}
	return out
}

// requestsFor returns the requests the fake received for one method and path.
func (f *fakeProwlarr) requestsFor(method, path string) []recordedRequest {
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

// putRequests returns the PUT requests the fake received.
func (f *fakeProwlarr) putRequests() []recordedRequest {
	return f.requestsFor(http.MethodPut, hostPath)
}

func (f *fakeProwlarr) modeNow() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mode
}

// applications returns the entries the fake holds, unmasked: what the
// instance actually stored.
func (f *fakeProwlarr) applications() []application {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]application, len(f.appsList))
	copy(out, f.appsList)
	return out
}

func (f *fakeProwlarr) testedApplications() []application {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]application, len(f.tested))
	copy(out, f.tested)
	return out
}

// configuratorForServer points a configurator at an httptest Prowlarr so the
// whole PostStart path runs without a real instance. The PVRs a test wires come
// from the state's integration bindings alone: nothing here reaches one.
func configuratorForServer(t *testing.T, fake *fakeProwlarr) *Configurator {
	t.Helper()
	return configuratorForServerWith(t, fake, appclient.RetryPolicy{})
}

// configuratorForServerWith is configuratorForServer with the clients' retry
// policy chosen by the caller. The one use is a test that drives a failure it
// expects to be classified transient: collapsing the policy to a single attempt
// skips the default backoff, and the classification (a 5xx is transient) is the
// same either way.
func configuratorForServerWith(t *testing.T, fake *fakeProwlarr, retry appclient.RetryPolicy) *Configurator {
	t.Helper()
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)

	c := NewConfigurator(0, configurator.Deps{
		HTTP:   configurator.ClientFactory{Retry: retry},
		Logger: quietLogger(),
	})
	c.baseURL = server.URL
	return c
}

// applicationsForServer builds the typed applications client against an
// httptest Prowlarr, for tests that exercise the resource directly.
func applicationsForServer(t *testing.T, fake *fakeProwlarr, apiKey string) *applicationsAPI {
	t.Helper()
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	return newApplicationsAPI(configurator.ClientFactory{}, func() string { return server.URL }, apiKey)
}

// wantApplicationJSON is the exact document Bloud must send for one PVR. It is
// pinned as JSON rather than as a struct because it is a contract with
// Prowlarr's schema: a missing, renamed or extra field has to fail here.
func wantApplicationJSON(implementation, configContract, baseURL, apiKey string) string {
	return wantApplicationJSONNamed(implementation+managedNameSuffix, implementation, configContract, baseURL, apiKey, []int{})
}

// wantApplicationJSONNamed is wantApplicationJSON for a document that carries a
// specific name and tags: an in-place repair keeps whatever name and tags the
// entry already has (the operator may have renamed it), and an entry Bloud wrote
// before the reserved name existed keeps the implementation's name.
func wantApplicationJSONNamed(name, implementation, configContract, baseURL, apiKey string, tags []int) string {
	return fmt.Sprintf(`{
		"name": %q,
		"implementation": %q,
		"configContract": %q,
		"syncLevel": "fullSync",
		"tags": %v,
		"fields": [
			{"name": "prowlarrUrl", "value": "http://apps-prowlarr:9696"},
			{"name": "baseUrl", "value": %q},
			{"name": "apiKey", "value": %q}
		]}`, name, implementation, configContract, tags, baseURL, apiKey)
}

// withID adds an entry id to a document built by wantApplicationJSON, for
// asserting the body of an in-place update (PUT /applications/{id}).
func withID(doc string, id int) string {
	return fmt.Sprintf(`{"id": %d, %s`, id, strings.TrimPrefix(strings.TrimSpace(doc), "{"))
}

// assertSameJSON compares two JSON documents by value, so key order and
// whitespace do not matter.
func assertSameJSON(t *testing.T, what string, got []byte, want string) {
	t.Helper()

	var gotDoc, wantDoc any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("%s: decode %s: %v", what, got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantDoc); err != nil {
		t.Fatalf("%s: decode expected document: %v", what, err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

// --- tests ---

func TestConfigurator_Name(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	if got := c.Name(); got != "apps-prowlarr" {
		t.Errorf("Name() = %q, want %q", got, "apps-prowlarr")
	}
}

func TestNewConfigurator_Port(t *testing.T) {
	if got := NewConfigurator(0, configurator.Deps{Logger: quietLogger()}).port; got != 9696 {
		t.Errorf("NewConfigurator(0).port = %d, want 9696", got)
	}
	if got := NewConfigurator(9000, configurator.Deps{Logger: quietLogger()}).port; got != 9000 {
		t.Errorf("NewConfigurator(9000).port = %d, want 9000", got)
	}
}

func TestPreStart_CreatesDirsAndConfigOnce(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	state := appState(t)

	changed, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	if !changed.RestartNeeded {
		t.Error("PreStart() changed = false, want true for a missing config.xml")
	}

	for _, dir := range []string{
		filepath.Join(state.DataPath, "config"),
		filepath.Join(state.BloudDataPath, "downloads"),
	} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("directory %s not created (err = %v)", dir, err)
		}
	}

	cfg, err := xmlutil.Open(c.configPath(state), "Config")
	if err != nil {
		t.Fatalf("open config.xml: %v", err)
	}
	if got := cfg.GetElement("AuthenticationMethod"); got != "External" {
		t.Errorf("AuthenticationMethod = %q, want %q", got, "External")
	}
	if got := cfg.GetElement("AuthenticationRequired"); got != "Enabled" {
		t.Errorf("AuthenticationRequired = %q, want %q", got, "Enabled")
	}
	if key := cfg.GetElement("ApiKey"); !apiKeyPattern.MatchString(key) {
		t.Errorf("ApiKey = %q, want 32 lowercase hex characters", key)
	}

	changed, err = c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("second PreStart() error = %v", err)
	}
	if changed.RestartNeeded {
		t.Error("second PreStart() changed = true, want false (would restart the container every cycle)")
	}
}

func TestPreStart_PreservesExistingAPIKey(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	state := appState(t)
	writeConfig(t, c.configPath(state), "<Config>\n  <Port>9696</Port>\n  <ApiKey>"+existingAPIKey+"</ApiKey>\n</Config>")

	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}

	cfg, err := xmlutil.Open(c.configPath(state), "Config")
	if err != nil {
		t.Fatalf("open config.xml: %v", err)
	}
	if got := cfg.GetElement("ApiKey"); got != existingAPIKey {
		t.Errorf("ApiKey = %q, want the instance's own key %q", got, existingAPIKey)
	}
	if got := cfg.GetElement("Port"); got != "9696" {
		t.Errorf("Port = %q, want %q (unrelated keys must survive)", got, "9696")
	}
}

func TestRemove_IsNoOp(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	if err := c.Remove(context.Background(), appState(t), true); err != nil {
		t.Errorf("Remove() error = %v, want nil", err)
	}
}

func TestPostStart_AlreadyExternal(t *testing.T) {
	fake := &fakeProwlarr{mode: "external"}
	c := configuratorForServer(t, fake)
	state := appState(t)
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}
	if puts := fake.putRequests(); len(puts) != 0 {
		t.Errorf("PUT count = %d, want 0 for an already-external instance", len(puts))
	}
	// No PVR is bound to this state, so the only other traffic is reading the
	// (empty) application list.
	want := []string{hostPath, applicationsPath}
	if paths := fake.paths(); !reflect.DeepEqual(paths, want) {
		t.Errorf("request paths = %v, want %v", paths, want)
	}
}

func TestPostStart_RepairsAfterUIEdit(t *testing.T) {
	fake := &fakeProwlarr{mode: "forms"}
	c := configuratorForServer(t, fake)
	state := appState(t)
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	key, err := servarr.APIKey(c.configPath(state))
	if err != nil {
		t.Fatalf("read generated key: %v", err)
	}

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	puts := fake.putRequests()
	if len(puts) != 1 {
		t.Fatalf("PUT count = %d, want 1", len(puts))
	}
	if puts[0].apiKey != key {
		t.Errorf("PUT X-Api-Key = %q, want the instance key %q", puts[0].apiKey, key)
	}
	// GET (read mode), PUT (repair), GET (confirm) on the host resource.
	if hosts := fake.requestsFor(http.MethodGet, hostPath); len(hosts) != 2 {
		t.Errorf("GET %s count = %d, want 2", hostPath, len(hosts))
	}
	if got := fake.modeNow(); got != "external" {
		t.Errorf("instance mode after repair = %q, want %q", got, "external")
	}
}

func TestPostStart_MissingAPIKeyErrors(t *testing.T) {
	c := configuratorForServer(t, &fakeProwlarr{mode: "external"})
	state := appState(t)

	err := c.PostStart(context.Background(), state)
	if err == nil {
		t.Fatal("PostStart() error = nil, want an error when config.xml has no ApiKey")
	}
	if !strings.Contains(err.Error(), c.configPath(state)) {
		t.Errorf("error %q does not name the config path %q", err, c.configPath(state))
	}
}

func TestCreateApplication_DuplicateNameIsAlreadyDone(t *testing.T) {
	fake := &fakeProwlarr{mode: "external"}
	// An entry Bloud does not own already holds the name: a manual Prowlarr
	// entry, or another writer racing this reconciliation.
	fake.appsList = []application{{
		ID:             1,
		Name:           sonarrImplementation,
		Implementation: sonarrImplementation,
		ConfigContract: sonarrConfigContract,
		SyncLevel:      syncLevelFullSync,
		Tags:           []int{},
		Fields:         []applicationField{{Name: fieldBaseURL, Value: "http://apps-sonarr:8989"}},
	}}
	fake.nextID = 1
	api := applicationsForServer(t, fake, existingAPIKey)

	created, err := api.createApplication(context.Background(), application{
		Name:           sonarrImplementation,
		Implementation: sonarrImplementation,
		ConfigContract: sonarrConfigContract,
		SyncLevel:      syncLevelFullSync,
		Tags:           []int{},
		Fields:         []applicationField{{Name: fieldBaseURL, Value: "http://apps-sonarr:8989"}},
	})
	if err != nil {
		t.Fatalf("createApplication() error = %v, want nil for a duplicate name", err)
	}
	if created {
		t.Error("createApplication() created = true, want false for a duplicate name")
	}
	if got := len(fake.applications()); got != 1 {
		t.Errorf("stored applications = %d, want the existing entry untouched", got)
	}
}

func TestPostStart_WiresInstalledPvrs(t *testing.T) {
	fake := &fakeProwlarr{mode: "external"}
	c := configuratorForServer(t, fake)
	state := appState(t)
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	withPvrs(state,
		pvrBinding(t, sonarrAppID, true, sonarrAPIKey),
		pvrBinding(t, radarrAppID, true, radarrAPIKey),
	)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	want := map[string]string{
		sonarrImplementation: wantApplicationJSON(sonarrImplementation, sonarrConfigContract, "http://apps-sonarr:8989", sonarrAPIKey),
		radarrImplementation: wantApplicationJSON(radarrImplementation, radarrConfigContract, "http://apps-radarr:7878", radarrAPIKey),
	}

	instanceKey, err := servarr.APIKey(c.configPath(state))
	if err != nil {
		t.Fatalf("read instance key: %v", err)
	}

	creates := fake.requestsFor(http.MethodPost, applicationsPath)
	if len(creates) != 2 {
		t.Fatalf("create count = %d, want 2 (one per installed PVR)", len(creates))
	}
	for _, req := range creates {
		if req.apiKey != instanceKey {
			t.Errorf("create X-Api-Key = %q, want the instance key %q", req.apiKey, instanceKey)
		}
		var app application
		if err := json.Unmarshal(req.body, &app); err != nil {
			t.Fatalf("decode created application %s: %v", req.body, err)
		}
		expected, ok := want[app.Implementation]
		if !ok {
			t.Fatalf("created an unexpected application: %s", req.body)
		}
		assertSameJSON(t, "create body for "+app.Implementation, req.body, expected)
	}

	// Every created application is tested with the same document first, and
	// only then stored: Prowlarr's /test endpoint rejects a name an existing
	// entry already holds, so a create that came first would make its own test
	// fail with "Should be unique".
	if ti, ci := fake.indexOf(http.MethodPost, applicationTestPath), fake.indexOf(http.MethodPost, applicationsPath); ti < 0 || ci < 0 || ti > ci {
		t.Errorf("first test request at %d, first create at %d: the test must come first", ti, ci)
	}
	tests := fake.requestsFor(http.MethodPost, applicationTestPath)
	if len(tests) != 2 {
		t.Fatalf("test count = %d, want 2", len(tests))
	}
	for _, req := range tests {
		var app application
		if err := json.Unmarshal(req.body, &app); err != nil {
			t.Fatalf("decode tested application %s: %v", req.body, err)
		}
		assertSameJSON(t, "test body for "+app.Implementation, req.body, want[app.Implementation])
	}

	// The instance holds exactly the two documents Bloud sent.
	stored := fake.applications()
	if len(stored) != 2 {
		t.Fatalf("stored applications = %d, want 2", len(stored))
	}
	for _, app := range stored {
		if got := app.field(fieldAPIKey); got != sonarrAPIKey && got != radarrAPIKey {
			t.Errorf("stored %s apiKey = %q, want the sibling's own key", app.Implementation, got)
		}
		if got := app.field(fieldProwlarrURL); got != "http://apps-prowlarr:9696" {
			t.Errorf("stored %s prowlarrUrl = %q, want http://apps-prowlarr:9696", app.Implementation, got)
		}
	}
}

func TestPostStart_SecondRunIsNoOp(t *testing.T) {
	fake := &fakeProwlarr{mode: "external"}
	c := configuratorForServer(t, fake)
	state := appState(t)
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	withPvrs(state,
		pvrBinding(t, sonarrAppID, true, sonarrAPIKey),
		pvrBinding(t, radarrAppID, true, radarrAPIKey),
	)

	ctx := context.Background()
	if err := c.PostStart(ctx, state); err != nil {
		t.Fatalf("first PostStart() error = %v", err)
	}
	if err := c.PostStart(ctx, state); err != nil {
		t.Fatalf("second PostStart() error = %v", err)
	}

	// Two creates and two tests from the first run; the second run must add
	// nothing. The instance masks every apiKey it returns, so this is what
	// proves the comparison does not mistake a masked key for drift and
	// recreate the entries on every reconciliation.
	if got := len(fake.requestsFor(http.MethodPost, applicationsPath)); got != 2 {
		t.Errorf("create count after two runs = %d, want 2", got)
	}
	if got := len(fake.requestsFor(http.MethodPost, applicationTestPath)); got != 2 {
		t.Errorf("test count after two runs = %d, want 2", got)
	}
	for _, path := range []string{applicationsPath + "/1", applicationsPath + "/2"} {
		if got := len(fake.requestsFor(http.MethodDelete, path)); got != 0 {
			t.Errorf("DELETE %s count = %d, want 0", path, got)
		}
	}
}

// An entry at an address Bloud does not write belongs to whoever wrote it: a
// remote PVR, or an entry added by hand. Bloud leaves it alone (no repair, no
// delete) instead of overwriting it, and reports its own link as unwired.
func TestPostStart_LeavesAnEntryAtAnotherAddressAlone(t *testing.T) {
	fake := &fakeProwlarr{mode: "external"}
	fake.appsList = []application{{
		ID:             7,
		Name:           sonarrImplementation,
		Implementation: sonarrImplementation,
		ConfigContract: sonarrConfigContract,
		SyncLevel:      syncLevelFullSync,
		Tags:           []int{1},
		Fields: []applicationField{
			{Name: fieldProwlarrURL, Value: "http://prowlarr.example:9696"},
			{Name: fieldBaseURL, Value: "http://sonarr.example:8989"},
			{Name: fieldAPIKey, Value: sonarrAPIKey},
		},
	}}
	fake.nextID = 7

	c := configuratorForServer(t, fake)
	state := appState(t)
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	withPvrs(state, pvrBinding(t, sonarrAppID, true, sonarrAPIKey))

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v, want nil: someone else's entry is not a failure", err)
	}

	if got := len(fake.requestsFor(http.MethodDelete, applicationsPath+"/7")); got != 0 {
		t.Errorf("DELETE count = %d, want 0: the entry is not Bloud's to remove", got)
	}
	if got := len(fake.requestsFor(http.MethodPut, applicationsPath+"/7")); got != 0 {
		t.Errorf("PUT count = %d, want 0: the entry is not Bloud's to rewrite", got)
	}
	stored := fake.applications()
	if len(stored) != 2 {
		t.Fatalf("stored applications = %d, want the operator's entry plus Bloud's own", len(stored))
	}
	byName := map[string]application{}
	for _, app := range stored {
		byName[app.Name] = app
	}
	operator, ok := byName[sonarrImplementation]
	if !ok {
		t.Fatalf("stored applications = %v, want the operator's %q kept", byName, sonarrImplementation)
	}
	if got := operator.field(fieldBaseURL); got != "http://sonarr.example:8989" {
		t.Errorf("operator baseUrl = %q, want their address left alone", got)
	}
	if got := operator.Tags; !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("operator tags = %v, want their tags left alone", got)
	}
	ours, ok := byName[sonarrImplementation+managedNameSuffix]
	if !ok {
		t.Fatalf("stored applications = %v, want Bloud's entry created under its reserved name", byName)
	}
	if got := ours.field(fieldBaseURL); got != "http://apps-sonarr:8989" {
		t.Errorf("Bloud's baseUrl = %q, want the container address", got)
	}
}

// An entry whose apiKey field is empty is readable drift (Prowlarr masks only
// the keys it holds, so an empty one reads back empty) and must be repaired with
// the key the binding published, in place.
func TestPostStart_RepairsEntryWithoutStoredKey(t *testing.T) {
	fake := &fakeProwlarr{mode: "external"}
	// The addresses are right but the key is empty. Prowlarr masks only the
	// keys it holds, so an empty one reads back as empty: a readable drift the
	// comparison must not treat as converged.
	fake.appsList = []application{{
		ID:             3,
		Name:           radarrImplementation,
		Implementation: radarrImplementation,
		ConfigContract: radarrConfigContract,
		SyncLevel:      syncLevelFullSync,
		Tags:           []int{4},
		Fields: []applicationField{
			{Name: fieldProwlarrURL, Value: "http://apps-prowlarr:9696"},
			{Name: fieldBaseURL, Value: "http://apps-radarr:7878"},
			{Name: fieldAPIKey, Value: ""},
		},
	}}
	fake.nextID = 3

	c := configuratorForServer(t, fake)
	state := appState(t)
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	withPvrs(state, pvrBinding(t, radarrAppID, true, radarrAPIKey))

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	// The entry is repaired in place: it keeps its id, its name and its tags,
	// and nothing is deleted and re-created around it.
	if got := len(fake.requestsFor(http.MethodDelete, applicationsPath+"/3")); got != 0 {
		t.Errorf("DELETE count = %d, want 0: the entry is repaired in place", got)
	}
	puts := fake.requestsFor(http.MethodPut, applicationsPath+"/3")
	if len(puts) != 1 {
		t.Fatalf("PUT count = %d, want 1", len(puts))
	}
	assertSameJSON(t, "repair body", puts[0].body,
		withID(wantApplicationJSONNamed(radarrImplementation, radarrImplementation, radarrConfigContract, "http://apps-radarr:7878", radarrAPIKey, []int{4}), 3))

	stored := fake.applications()
	if len(stored) != 1 {
		t.Fatalf("stored applications = %d, want 1", len(stored))
	}
	if got := stored[0].Tags; !reflect.DeepEqual(got, []int{4}) {
		t.Errorf("stored tags = %v, want the entry's own tags kept", got)
	}
}

// A PVR that was purged and reinstalled keeps its address but mints a fresh
// key, and Prowlarr's copy of the old one reads back masked, so nothing in the
// document comparison can see it. The instance's own test of the entry is what
// catches it, and the repair is a re-push with the key the binding now publishes.
func TestPostStart_RepushesAnApplicationTheInstanceCannotReach(t *testing.T) {
	const staleKey = "ffffffffffffffffffffffffffffffff"

	fake := &fakeProwlarr{mode: "external"}
	fake.appsList = []application{{
		ID:             9,
		Name:           sonarrImplementation + managedNameSuffix,
		Implementation: sonarrImplementation,
		ConfigContract: sonarrConfigContract,
		SyncLevel:      syncLevelFullSync,
		Tags:           []int{},
		Fields: []applicationField{
			{Name: fieldProwlarrURL, Value: "http://apps-prowlarr:9696"},
			{Name: fieldBaseURL, Value: "http://apps-sonarr:8989"},
			{Name: fieldAPIKey, Value: staleKey},
		},
	}}
	fake.nextID = 9
	// The sibling no longer accepts what Prowlarr holds: it was reinstalled, and
	// the key on disk is a new one.
	fake.markInvalid(9)

	c := configuratorForServer(t, fake)
	state := appState(t)
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	withPvrs(state, pvrBinding(t, sonarrAppID, true, sonarrAPIKey))

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	puts := fake.requestsFor(http.MethodPut, applicationsPath+"/9")
	if len(puts) != 1 {
		t.Fatalf("PUT count = %d, want 1: the entry is re-pushed", len(puts))
	}
	assertSameJSON(t, "re-pushed body", puts[0].body,
		withID(wantApplicationJSON(sonarrImplementation, sonarrConfigContract, "http://apps-sonarr:8989", sonarrAPIKey), 9))

	stored := fake.applications()
	if len(stored) != 1 {
		t.Fatalf("stored applications = %d, want 1", len(stored))
	}
	if got := stored[0].field(fieldAPIKey); got != sonarrAPIKey {
		t.Errorf("stored apiKey = %q, want the sibling's current key %q", got, sonarrAPIKey)
	}
	if got := len(fake.requestsFor(http.MethodPost, applicationsPath)); got != 0 {
		t.Errorf("create count = %d, want 0: the entry already exists", got)
	}
}

// The name is the operator's field: Prowlarr's UI lets them change it, so a
// renamed entry is converged rather than drift, and when the entry does need a
// repair, the name and the tags survive it.
func TestPostStart_KeepsAnAdminRename(t *testing.T) {
	const renamed = "Sonarr Remote"

	seeded := func(key string) *fakeProwlarr {
		fake := &fakeProwlarr{mode: "external"}
		fake.appsList = []application{{
			ID:             5,
			Name:           renamed,
			Implementation: sonarrImplementation,
			ConfigContract: sonarrConfigContract,
			SyncLevel:      syncLevelFullSync,
			Tags:           []int{2},
			Fields: []applicationField{
				{Name: fieldProwlarrURL, Value: "http://apps-prowlarr:9696"},
				{Name: fieldBaseURL, Value: "http://apps-sonarr:8989"},
				{Name: fieldAPIKey, Value: key},
			},
		}}
		fake.nextID = 5
		return fake
	}
	run := func(t *testing.T, fake *fakeProwlarr) {
		t.Helper()
		c := configuratorForServer(t, fake)
		state := appState(t)
		if _, err := c.PreStart(context.Background(), state); err != nil {
			t.Fatalf("PreStart() error = %v", err)
		}
		withPvrs(state, pvrBinding(t, sonarrAppID, true, sonarrAPIKey))
		if err := c.PostStart(context.Background(), state); err != nil {
			t.Fatalf("PostStart() error = %v", err)
		}
	}

	t.Run("a matching entry is left as it is", func(t *testing.T) {
		fake := seeded(sonarrAPIKey)
		run(t, fake)

		if got := len(fake.requestsFor(http.MethodPut, applicationsPath+"/5")); got != 0 {
			t.Errorf("PUT count = %d, want 0: the rename is not drift", got)
		}
		if got := len(fake.requestsFor(http.MethodDelete, applicationsPath+"/5")); got != 0 {
			t.Errorf("DELETE count = %d, want 0", got)
		}
	})

	t.Run("a repair keeps the rename and the tags", func(t *testing.T) {
		fake := seeded(sonarrAPIKey)
		fake.markInvalid(5)
		run(t, fake)

		puts := fake.requestsFor(http.MethodPut, applicationsPath+"/5")
		if len(puts) != 1 {
			t.Fatalf("PUT count = %d, want 1", len(puts))
		}
		assertSameJSON(t, "repair body", puts[0].body,
			withID(wantApplicationJSONNamed(renamed, sonarrImplementation, sonarrConfigContract, "http://apps-sonarr:8989", sonarrAPIKey, []int{2}), 5))
	})
}

// The stored-entry verdict is a nicety, not a precondition: an instance that
// cannot answer it transiently must not fail the node, while a verdict the
// endpoint cannot answer at all (a version without it) is loud.
func TestPostStart_StoredEntryVerdictFailurePolicy(t *testing.T) {
	seeded := func(status int) *fakeProwlarr {
		fake := &fakeProwlarr{mode: "external"}
		fake.appsList = []application{{
			ID:             1,
			Name:           sonarrImplementation + managedNameSuffix,
			Implementation: sonarrImplementation,
			ConfigContract: sonarrConfigContract,
			SyncLevel:      syncLevelFullSync,
			Tags:           []int{},
			Fields: []applicationField{
				{Name: fieldProwlarrURL, Value: "http://apps-prowlarr:9696"},
				{Name: fieldBaseURL, Value: "http://apps-sonarr:8989"},
				{Name: fieldAPIKey, Value: sonarrAPIKey},
			},
		}}
		fake.nextID = 1
		fake.testAllStatus = status
		return fake
	}
	run := func(t *testing.T, fake *fakeProwlarr) error {
		t.Helper()
		c := configuratorForServer(t, fake)
		state := appState(t)
		if _, err := c.PreStart(context.Background(), state); err != nil {
			t.Fatalf("PreStart() error = %v", err)
		}
		withPvrs(state, pvrBinding(t, sonarrAppID, true, sonarrAPIKey))
		return c.PostStart(context.Background(), state)
	}

	t.Run("a transient failure accepts the stored entry", func(t *testing.T) {
		fake := seeded(http.StatusServiceUnavailable)
		if err := run(t, fake); err != nil {
			t.Fatalf("PostStart() error = %v, want nil: an unanswerable test is retried, not fatal", err)
		}
		if got := len(fake.requestsFor(http.MethodPut, applicationsPath+"/1")); got != 0 {
			t.Errorf("PUT count = %d, want 0 without a verdict", got)
		}
	})

	t.Run("a missing endpoint is reported", func(t *testing.T) {
		fake := seeded(http.StatusNotFound)
		err := run(t, fake)
		if err == nil {
			t.Fatal("PostStart() error = nil, want the unsupported endpoint reported")
		}
		if !strings.Contains(err.Error(), applicationTestAllPath) {
			t.Errorf("error %q does not name %q", err, applicationTestAllPath)
		}
	})
}

func TestPostStart_PrunesApplicationsForUninstalledPvrs(t *testing.T) {
	fake := &fakeProwlarr{mode: "external"}
	fake.appsList = []application{
		{
			ID:             11,
			Name:           sonarrImplementation,
			Implementation: sonarrImplementation,
			ConfigContract: sonarrConfigContract,
			SyncLevel:      syncLevelFullSync,
			Tags:           []int{},
			Fields: []applicationField{
				{Name: fieldProwlarrURL, Value: "http://apps-prowlarr:9696"},
				{Name: fieldBaseURL, Value: "http://apps-sonarr:8989"},
				{Name: fieldAPIKey, Value: sonarrAPIKey},
			},
		},
		{
			ID:             12,
			Name:           radarrImplementation,
			Implementation: radarrImplementation,
			ConfigContract: radarrConfigContract,
			SyncLevel:      syncLevelFullSync,
			Tags:           []int{},
			Fields: []applicationField{
				{Name: fieldProwlarrURL, Value: "http://apps-prowlarr:9696"},
				{Name: fieldBaseURL, Value: "http://apps-radarr:7878"},
				{Name: fieldAPIKey, Value: radarrAPIKey},
			},
		},
	}

	c := configuratorForServer(t, fake)
	state := appState(t)
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	// Neither PVR is installed. Their bindings say so and still carry the
	// address Bloud wrote for them, which is what identifies the entries to
	// prune: nothing else has to be reachable, and no key is involved. This is
	// the signal that replaced a probe of the PVR's published port, so it is
	// the only thing that decides a prune.
	withPvrs(state,
		pvrBinding(t, sonarrAppID, false, ""),
		pvrBinding(t, radarrAppID, false, ""),
	)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	for _, id := range []string{"11", "12"} {
		if got := len(fake.requestsFor(http.MethodDelete, applicationsPath+"/"+id)); got != 1 {
			t.Errorf("DELETE %s count = %d, want 1", id, got)
		}
	}
	if got := len(fake.requestsFor(http.MethodPost, applicationsPath)); got != 0 {
		t.Errorf("create count = %d, want 0 with no PVR installed", got)
	}
	if got := len(fake.requestsFor(http.MethodPost, applicationTestPath)); got != 0 {
		t.Errorf("test count = %d, want 0 with no PVR installed", got)
	}
	if stored := fake.applications(); len(stored) != 0 {
		t.Errorf("stored applications = %v, want none", stored)
	}
}

// A failure of the instance's own connection test is transient when the instance
// itself is failing (a 5xx): the link waits for the next reconciliation instead
// of failing the node, and because the document is tested before it is stored,
// nothing half-written is left behind.
func TestPostStart_TransientTestFailureIsRetried(t *testing.T) {
	fake := &fakeProwlarr{mode: "external", testStatus: http.StatusServiceUnavailable}
	// One attempt: the failure is the expected outcome, so the default backoff
	// would only slow the test down.
	c := configuratorForServerWith(t, fake, appclient.RetryPolicy{MaxAttempts: 1})
	state := appState(t)
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	withPvrs(state, pvrBinding(t, sonarrAppID, true, sonarrAPIKey))

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v, want nil: a 5xx from the instance is retried, not fatal", err)
	}

	if got := len(fake.testedApplications()); got != 1 {
		t.Errorf("test count = %d, want 1: the link was attempted, not skipped", got)
	}
	if got := len(fake.requestsFor(http.MethodPost, applicationsPath)); got != 0 {
		t.Errorf("create count = %d, want 0: the failed test wrote nothing", got)
	}
	if stored := fake.applications(); len(stored) != 0 {
		t.Errorf("stored applications = %v, want none", stored)
	}
}

func TestPostStart_TestFailureNamesTheStatus(t *testing.T) {
	fake := &fakeProwlarr{mode: "external", testFails: true}
	c := configuratorForServer(t, fake)
	state := appState(t)
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	withPvrs(state, pvrBinding(t, sonarrAppID, true, sonarrAPIKey))

	err := c.PostStart(context.Background(), state)
	if err == nil {
		t.Fatal("PostStart() error = nil, want an error when the PVR rejects the application")
	}
	for _, want := range []string{sonarrImplementation, "400"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	// A rejected link is a real misconfiguration: the node goes to ERROR rather
	// than being quietly left half-wired, and because the document is tested
	// before it is stored, nothing is written at all.
	if got := len(fake.testedApplications()); got != 1 {
		t.Errorf("test count = %d, want 1", got)
	}
	if got := len(fake.requestsFor(http.MethodPost, applicationsPath)); got != 0 {
		t.Errorf("create count = %d, want 0 when the test fails", got)
	}
	if stored := fake.applications(); len(stored) != 0 {
		t.Errorf("stored applications = %v, want none", stored)
	}
}

// A PVR that is installed but has not published its key yet (its PreStart has
// not run, or it is still converging) is neither a fault nor an uninstall: the
// binding carries no secret, so nothing is written (a keyless entry would only
// have to be corrected later) and nothing is pruned, because the PVR is still
// installed. The entry a previous install left behind waits for the key.
func TestPostStart_PvrWithoutPublishedKeyIsSkipped(t *testing.T) {
	fake := &fakeProwlarr{mode: "external"}
	fake.appsList = []application{{
		ID:             3,
		Name:           sonarrImplementation + managedNameSuffix,
		Implementation: sonarrImplementation,
		ConfigContract: sonarrConfigContract,
		SyncLevel:      syncLevelFullSync,
		Tags:           []int{},
		Fields: []applicationField{
			{Name: fieldProwlarrURL, Value: "http://apps-prowlarr:9696"},
			{Name: fieldBaseURL, Value: "http://apps-sonarr:8989"},
			{Name: fieldAPIKey, Value: ""},
		},
	}}
	fake.nextID = 3

	c := configuratorForServer(t, fake)
	state := appState(t)
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	// Installed, but the provider's secret is absent (not empty): the
	// orchestrator only sets the key once the PVR has published one.
	withPvrs(state, pvrBinding(t, sonarrAppID, true, ""))

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v, want nil: a key that is not published yet is not a failure", err)
	}

	if got := len(fake.requestsFor(http.MethodPost, applicationTestPath)); got != 0 {
		t.Errorf("test count = %d, want 0 without a published key", got)
	}
	if got := len(fake.requestsFor(http.MethodPost, applicationsPath)); got != 0 {
		t.Errorf("create count = %d, want 0 without a published key", got)
	}
	if got := len(fake.requestsFor(http.MethodPut, applicationsPath+"/3")); got != 0 {
		t.Errorf("PUT count = %d, want 0: the entry waits for the key instead of being rewritten", got)
	}
	if got := len(fake.requestsFor(http.MethodDelete, applicationsPath+"/3")); got != 0 {
		t.Errorf("DELETE count = %d, want 0: only an uninstalled PVR is pruned", got)
	}
	if stored := fake.applications(); len(stored) != 1 {
		t.Errorf("stored applications = %d, want the existing entry left as it is", len(stored))
	}
}

// What Prowlarr is told about a PVR is what the orchestrator resolved for it:
// the document carries the binding's BaseURL, i.e. the address the Prowlarr
// container reaches the PVR at (not the binding's LocalURL, which is only good
// from the host), and the key the binding published. Nothing here could have
// derived either.
func TestPostStart_StoresTheResolvedBinding(t *testing.T) {
	const (
		baseURL = "http://apps-sonarr:9999"
		apiKey  = "33333333333333333333333333333333"
	)

	fake := &fakeProwlarr{mode: "external"}
	c := configuratorForServer(t, fake)
	state := appState(t)
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	binding := pvrBinding(t, sonarrAppID, true, apiKey)
	binding.BaseURL = baseURL
	binding.Port = 9999
	binding.LocalURL = "http://localhost:9999"
	withPvrs(state, binding)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	creates := fake.requestsFor(http.MethodPost, applicationsPath)
	if len(creates) != 1 {
		t.Fatalf("create count = %d, want 1", len(creates))
	}
	assertSameJSON(t, "create body", creates[0].body,
		wantApplicationJSON(sonarrImplementation, sonarrConfigContract, baseURL, apiKey))

	stored := fake.applications()
	if len(stored) != 1 {
		t.Fatalf("stored applications = %d, want 1", len(stored))
	}
	if got := stored[0].field(fieldBaseURL); got != baseURL {
		t.Errorf("stored baseUrl = %q, want the binding's BaseURL %q", got, baseURL)
	}
	if got := stored[0].field(fieldAPIKey); got != apiKey {
		t.Errorf("stored apiKey = %q, want the binding's published key %q", got, apiKey)
	}
}
