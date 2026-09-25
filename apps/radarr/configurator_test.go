// SPDX-License-Identifier: AGPL-3.0-only

package radarr

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
	"slices"
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
	// hostPath is the /config/host resource under Radarr's api/v3 root.
	hostPath = "/api/v3/config/host"
	// downloadClientEndpoint and its /test sibling are the download-client
	// resources PostStart reads and writes.
	downloadClientEndpoint     = "/api/v3/downloadclient"
	downloadClientTestEndpoint = downloadClientEndpoint + "/test"
)

var apiKeyPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// secretWrite is one credential a configurator published for an app.
type secretWrite struct {
	app   string
	key   string
	value string
}

// fakeSecrets implements configurator.AppSecretsProvider and records what this
// instance publishes. Radarr generates its own admin credential (it never asks
// the store for one) and owns exactly one secret: its ApiKey, which its
// consumers receive through their integration bindings instead of reading
// config.xml.
type fakeSecrets struct {
	mu      sync.Mutex
	publish []secretWrite
}

func (f *fakeSecrets) GenerateAppAdminPassword(string) (string, error) { return "", nil }
func (f *fakeSecrets) GetAppSecret(string, string) string              { return "" }

func (f *fakeSecrets) SetAppSecret(app, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publish = append(f.publish, secretWrite{app: app, key: key, value: value})
	return nil
}

// published returns the credentials written so far, oldest first.
func (f *fakeSecrets) published() []secretWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]secretWrite{}, f.publish...)
}

// appState returns the state a reconciliation passes for one install:
// <tmp>/appdata is the app's own data dir, <tmp>/data the shared Bloud one.
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

// recordedRequest is one request a fake received.
type recordedRequest struct {
	method string
	path   string
	apiKey string
	body   []byte
}

// --- fake Servarr API ---

// fakeClient is one entry of the fake instance's download-client list. The
// JSON tags make the fields export-worthy: the fake round-trips them through
// the real resource shape, including `fields`, which is what identifies the
// entry as Bloud's (host and port).
type fakeClient struct {
	ID             int               `json:"id"`
	Name           string            `json:"name"`
	Implementation string            `json:"implementation"`
	Fields         []fakeClientField `json:"fields"`
}

// fakeClientField is one entry of the fields array the real resource returns.
// Port comes back as a JSON number, like the real API.
type fakeClientField struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// bloudClient is the entry Bloud stores: the qBittorrent implementation at its
// own provider's coordinates, the identity pkg/servarr matches on.
func bloudClient(id int) fakeClient {
	return fakeClient{
		ID:             id,
		Name:           "qBittorrent (Bloud)",
		Implementation: "QBittorrent",
		Fields: []fakeClientField{
			{Name: "host", Value: "apps-qbittorrent"},
			{Name: "port", Value: 8081},
		},
	}
}

// foreignClient is an entry the operator added by hand: the same
// implementation at a different address (a seedbox). Bloud must never prune it.
func foreignClient(id int) fakeClient {
	return fakeClient{
		ID:             id,
		Name:           "seedbox",
		Implementation: "QBittorrent",
		Fields: []fakeClientField{
			{Name: "host", Value: "seedbox.example.net"},
			{Name: "port", Value: 8080},
		},
	}
}

// fakeInstance stands in for a Radarr instance: it models the three resources
// PostStart touches (the host config it repairs, the root-folder list, and
// the download-client list) and records every request so the tests can assert
// the calls.
type fakeInstance struct {
	mu           sync.Mutex
	mode         string
	folders      []string
	clients      []fakeClient
	nextClientID int
	// testStatus is what POST /downloadclient/test answers (0 → 200).
	testStatus int
	requests   []recordedRequest
}

func newFakeInstance(mode string) *fakeInstance {
	return &fakeInstance{mode: mode}
}

func (f *fakeInstance) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{
			method: r.Method,
			path:   r.URL.Path,
			apiKey: r.Header.Get(servarr.APIKeyHeader),
			body:   body,
		})
		mode := f.mode
		f.mu.Unlock()

		switch {
		case r.URL.Path == hostPath:
			f.handleHostConfig(w, r, body, mode)
		case r.URL.Path == rootFolderResource:
			f.handleRootFolder(w, r, body)
		case r.URL.Path == downloadClientEndpoint:
			f.handleDownloadClient(w, r, body)
		case r.URL.Path == downloadClientTestEndpoint:
			f.handleDownloadClientTest(w)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, downloadClientEndpoint+"/"):
			f.handleDeleteDownloadClient(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// handleHostConfig models /config/host: it reports the auth mode it is in and
// switches to whatever a PUT sets.
func (f *fakeInstance) handleHostConfig(w http.ResponseWriter, r *http.Request, body []byte, mode string) {
	if r.Method == http.MethodPut {
		var doc map[string]any
		_ = json.Unmarshal(body, &doc)
		next, _ := doc["authenticationMethod"].(string)
		f.mu.Lock()
		f.mode = next
		f.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"authenticationMethod":%q,"authenticationRequired":"enabled"}`, mode)
}

// handleRootFolder models /rootfolder: a JSON array of {path}.
func (f *fakeInstance) handleRootFolder(w http.ResponseWriter, r *http.Request, body []byte) {
	switch r.Method {
	case http.MethodGet:
		// The real resource is a JSON array of root-folder documents.
		folders := []map[string]string{}
		for _, path := range f.foldersNow() {
			folders = append(folders, map[string]string{"path": path})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(folders)
	case http.MethodPost:
		var doc struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(body, &doc)
		f.mu.Lock()
		f.folders = append(f.folders, doc.Path)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleDownloadClient models /downloadclient: a JSON array of
// {id, implementation}.
func (f *fakeInstance) handleDownloadClient(w http.ResponseWriter, r *http.Request, body []byte) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.clientsNow())
	case http.MethodPost:
		var dc struct {
			Name           string            `json:"name"`
			Implementation string            `json:"implementation"`
			Fields         []fakeClientField `json:"fields"`
		}
		_ = json.Unmarshal(body, &dc)
		f.mu.Lock()
		f.nextClientID++
		f.clients = append(f.clients, fakeClient{
			ID:             f.nextClientID,
			Name:           dc.Name,
			Implementation: dc.Implementation,
			Fields:         dc.Fields,
		})
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeInstance) handleDownloadClientTest(w http.ResponseWriter) {
	f.mu.Lock()
	status := f.testStatus
	f.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"isValid":true}`)
}

func (f *fakeInstance) handleDeleteDownloadClient(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, downloadClientEndpoint+"/"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	kept := []fakeClient{}
	f.mu.Lock()
	for _, dc := range f.clients {
		if dc.ID != id {
			kept = append(kept, dc)
		}
	}
	f.clients = kept
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// requestsTo returns the recorded requests for one method and path.
func (f *fakeInstance) requestsTo(method, path string) []recordedRequest {
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

func (f *fakeInstance) foldersNow() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.folders...)
}

func (f *fakeInstance) clientsNow() []fakeClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeClient{}, f.clients...)
}

func (f *fakeInstance) modeNow() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mode
}

// deletedPaths returns the path of every DELETE the fake received.
func (f *fakeInstance) deletedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if r.method == http.MethodDelete {
			out = append(out, r.path)
		}
	}
	return out
}

// --- fake qBittorrent ---

// fakeQBittorrent stands in for the provider on its published port: it models
// createCategory, including the 409 Conflict a repeated creation gets. It has
// nothing else to answer, because nothing else about the provider is discovered
// any more: whether it is installed, where it lives and whether it is ready
// arrive in the integration binding, so no consumer probes the provider to find
// out. It still records every request, which is how the tests observe that a
// reconciliation that had no business calling the provider did not call it.
type fakeQBittorrent struct {
	mu       sync.Mutex
	create   int // createCategory status (0 → 200/409 from state)
	category []string
	requests []recordedRequest
	// baseURL is this fake's own server, set by configuratorFor: the binding's
	// LocalURL (how the configurator reaches the provider from the host).
	baseURL string
}

func newFakeQBittorrent() *fakeQBittorrent {
	return &fakeQBittorrent{}
}

func (f *fakeQBittorrent) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{
			method: r.Method,
			path:   r.URL.Path,
			body:   []byte(r.PostForm.Encode()),
		})
		create := f.create
		f.mu.Unlock()

		switch r.URL.Path {
		case qbittorrentCategoryResource:
			if create != 0 {
				w.WriteHeader(create)
				_, _ = io.WriteString(w, "boom")
				return
			}
			name := r.PostForm.Get("category")
			f.mu.Lock()
			exists := slices.Contains(f.category, name)
			if !exists {
				f.category = append(f.category, name)
			}
			f.mu.Unlock()
			if exists {
				// APIErrorType::Conflict maps to 409 Conflict in
				// webapplication.cpp, and addCategory returns false for a
				// category that is already there.
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (f *fakeQBittorrent) requestsTo(method, path string) []recordedRequest {
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

func (f *fakeQBittorrent) categoriesNow() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.category...)
}

// --- test wiring ---

// configuratorFor points a configurator at an httptest-backed Radarr instance
// and a controlled qBittorrent provider, so PostStart's whole path runs
// without a real stack. The provider's reachability travels through the
// binding withDownloadClient seeds, never through a constructor argument.
func configuratorFor(t *testing.T, instance *fakeInstance, qb *fakeQBittorrent) *Configurator {
	t.Helper()
	server := httptest.NewServer(instance.handler())
	t.Cleanup(server.Close)

	c := NewConfigurator(0, configurator.Deps{
		Logger: quietLogger(),
		// One attempt per call: the fakes answer deterministically, so a
		// failure path must not sleep through the retry backoff.
		HTTP: configurator.ClientFactory{Retry: appclient.RetryPolicy{MaxAttempts: 1}},
	})
	c.baseURL = server.URL
	if qb != nil {
		qbServer := httptest.NewServer(qb.handler())
		t.Cleanup(qbServer.Close)
		qb.baseURL = qbServer.URL
	}
	return c
}

// withDownloadClient seeds the integration binding the orchestrator hands a
// reconciliation for the downloadClient contract: the qBittorrent provider at
// its catalog coordinates (Node and Port are what Radarr stores, so they are
// also what identifies the entry Bloud wrote) and, as LocalURL, the fake's own
// server (how the configurator itself reaches the provider from the host).
//
// installed=false is how the orchestrator reports an uninstalled provider: it
// is the signal to prune. The provider's address stays in the binding either
// way, which is exactly what a prune needs to recognise the stale entry.
func withDownloadClient(state *configurator.AppState, qb *fakeQBittorrent, installed bool) {
	state.Integrations.DownloadClients = []configurator.DownloadClientBinding{{
		ProviderRef: configurator.ProviderRef{
			App:       qbittorrentAppID,
			Installed: installed,
			Node:      "apps-qbittorrent",
			Port:      8081,
			BaseURL:   "http://apps-qbittorrent:8081",
			LocalURL:  qb.baseURL,
		},
	}}
}

// withConfig runs PreStart, which is what gives PostStart an ApiKey and makes
// its own-API calls possible.
func withConfig(t *testing.T, c *Configurator, state *configurator.AppState) {
	t.Helper()
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
}

// --- tests ---

func TestConfigurator_Name(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	if got := c.Name(); got != "apps-radarr" {
		t.Errorf("Name() = %q, want %q", got, "apps-radarr")
	}
}

func TestNewConfigurator_Port(t *testing.T) {
	if got := NewConfigurator(0, configurator.Deps{Logger: quietLogger()}).port; got != 7878 {
		t.Errorf("NewConfigurator(0).port = %d, want 7878", got)
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
		filepath.Join(state.BloudDataPath, "media", "movies"),
		filepath.Join(state.BloudDataPath, "downloads"),
	} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("directory %s not created (err = %v)", dir, err)
		}
	}

	// The media library is written by the container as uid 1000, which maps to
	// a host subuid under rootless podman, so it must be world-writable or the
	// instance rejects it as a root folder.
	mediaInfo, err := os.Stat(filepath.Join(state.BloudDataPath, "media", "movies"))
	if err != nil {
		t.Fatalf("stat media dir: %v", err)
	}
	if got := mediaInfo.Mode().Perm(); got != 0o777 {
		t.Errorf("media library mode = %o, want 0777", got)
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
	writeConfig(t, c.configPath(state), "<Config>\n  <Port>7878</Port>\n  <ApiKey>"+existingAPIKey+"</ApiKey>\n</Config>")

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
	if got := cfg.GetElement("Port"); got != "7878" {
		t.Errorf("Port = %q, want %q (unrelated keys must survive)", got, "7878")
	}
}

// The instance's ApiKey is what this app's consumers (Prowlarr, Seerr)
// authenticate with, and they receive it through their integration bindings, so
// PreStart has to put it in the secret store. The value must be the key the
// running instance actually uses - the one in config.xml - rather than one
// generated here: the instance rewrites its own key when its settings are saved
// from the UI, and a published key that no longer matches would leave every
// consumer with a 401.
func TestPreStart_PublishesTheInstanceStateKey(t *testing.T) {
	secrets := &fakeSecrets{}
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger(), Secrets: secrets})
	state := appState(t)
	writeConfig(t, c.configPath(state), "<Config>\n  <ApiKey>"+existingAPIKey+"</ApiKey>\n</Config>")

	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}

	writes := secrets.published()
	if len(writes) != 1 {
		t.Fatalf("SetAppSecret calls = %d, want 1", len(writes))
	}
	want := secretWrite{app: appName, key: servarr.SecretAPIKey, value: existingAPIKey}
	if writes[0] != want {
		t.Errorf("published secret = %+v, want %+v", writes[0], want)
	}

	// The instance rewrote config.xml from its own settings page: the next
	// reconciliation publishes the key it now authenticates with.
	rotated := "fedcba9876543210fedcba9876543210"
	writeConfig(t, c.configPath(state), "<Config>\n  <ApiKey>"+rotated+"</ApiKey>\n</Config>")

	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("second PreStart() error = %v", err)
	}
	writes = secrets.published()
	if len(writes) != 2 {
		t.Fatalf("SetAppSecret calls after the rotation = %d, want 2", len(writes))
	}
	if got := writes[1]; got != (secretWrite{app: appName, key: servarr.SecretAPIKey, value: rotated}) {
		t.Errorf("published secret after the rotation = %+v, want the rotated key %q", got, rotated)
	}
}

func TestRemove_IsNoOp(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	if err := c.Remove(context.Background(), appState(t), true); err != nil {
		t.Errorf("Remove() error = %v, want nil", err)
	}
}

func TestPostStart_AlreadyExternal(t *testing.T) {
	instance := newFakeInstance("external")
	qb := newFakeQBittorrent()
	c := configuratorFor(t, instance, qb)
	state := appState(t)
	withDownloadClient(state, qb, true)
	withConfig(t, c, state)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}
	if puts := instance.requestsTo(http.MethodPut, hostPath); len(puts) != 0 {
		t.Errorf("PUT count = %d, want 0 for an already-external instance", len(puts))
	}
	if gets := instance.requestsTo(http.MethodGet, hostPath); len(gets) != 1 {
		t.Errorf("GET %s count = %d, want 1", hostPath, len(gets))
	}
}

func TestPostStart_RepairsAfterUIEdit(t *testing.T) {
	instance := newFakeInstance("forms")
	qb := newFakeQBittorrent()
	c := configuratorFor(t, instance, qb)
	state := appState(t)
	withDownloadClient(state, qb, true)
	withConfig(t, c, state)
	key, err := servarr.APIKey(c.configPath(state))
	if err != nil {
		t.Fatalf("read generated key: %v", err)
	}

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	puts := instance.requestsTo(http.MethodPut, hostPath)
	if len(puts) != 1 {
		t.Fatalf("PUT count = %d, want 1", len(puts))
	}
	if puts[0].apiKey != key {
		t.Errorf("PUT X-Api-Key = %q, want the instance key %q", puts[0].apiKey, key)
	}
	if gets := instance.requestsTo(http.MethodGet, hostPath); len(gets) != 2 {
		t.Errorf("GET %s count = %d, want 2 (read, then verify after the write)", hostPath, len(gets))
	}
	if got := instance.modeNow(); got != "external" {
		t.Errorf("instance mode after repair = %q, want %q", got, "external")
	}
}

func TestPostStart_MissingAPIKeyErrors(t *testing.T) {
	c := configuratorFor(t, newFakeInstance("external"), nil)
	state := appState(t)

	err := c.PostStart(context.Background(), state)
	if err == nil {
		t.Fatal("PostStart() error = nil, want an error when config.xml has no ApiKey")
	}
	if !strings.Contains(err.Error(), c.configPath(state)) {
		t.Errorf("error %q does not name the config path %q", err, c.configPath(state))
	}
}

func TestPostStart_CreatesRootFolderOnce(t *testing.T) {
	instance := newFakeInstance("external")
	qb := newFakeQBittorrent()
	c := configuratorFor(t, instance, qb)
	state := appState(t)
	withDownloadClient(state, qb, true)
	withConfig(t, c, state)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}
	if got := instance.foldersNow(); !reflect.DeepEqual(got, []string{rootFolderMount}) {
		t.Errorf("root folders = %v, want [%s]", got, rootFolderMount)
	}

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("second PostStart() error = %v", err)
	}
	if posts := instance.requestsTo(http.MethodPost, rootFolderResource); len(posts) != 1 {
		t.Errorf("POST %s count = %d, want 1: an existing root folder must not be added again",
			rootFolderResource, len(posts))
	}
}

func TestPostStart_KeepsExistingRootFolder(t *testing.T) {
	instance := newFakeInstance("external")
	instance.folders = []string{rootFolderMount}
	qb := newFakeQBittorrent()
	c := configuratorFor(t, instance, qb)
	state := appState(t)
	withDownloadClient(state, qb, true)
	withConfig(t, c, state)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}
	if posts := instance.requestsTo(http.MethodPost, rootFolderResource); len(posts) != 0 {
		t.Errorf("POST %s count = %d, want 0 for an instance that already has the mount", rootFolderResource, len(posts))
	}
}

func TestPostStart_WiresCategoryAndDownloadClient(t *testing.T) {
	instance := newFakeInstance("external")
	qb := newFakeQBittorrent()
	c := configuratorFor(t, instance, qb)
	state := appState(t)
	withDownloadClient(state, qb, true)
	withConfig(t, c, state)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	// The category has to exist before the client that stores it.
	creates := qb.requestsTo(http.MethodPost, qbittorrentCategoryResource)
	if len(creates) != 1 {
		t.Fatalf("POST %s count = %d, want 1", qbittorrentCategoryResource, len(creates))
	}
	if got, want := string(creates[0].body), "category="+downloadCategory; got != want {
		t.Errorf("createCategory body = %q, want %q (qBittorrent's API is form-encoded)", got, want)
	}
	if got := qb.categoriesNow(); !reflect.DeepEqual(got, []string{downloadCategory}) {
		t.Errorf("qBittorrent categories = %v, want [%s]", got, downloadCategory)
	}

	posts := instance.requestsTo(http.MethodPost, downloadClientEndpoint)
	if len(posts) != 1 {
		t.Fatalf("POST %s count = %d, want 1", downloadClientEndpoint, len(posts))
	}
	var payload struct {
		Implementation string `json:"implementation"`
		Priority       int    `json:"priority"`
		Enable         bool   `json:"enable"`
		Fields         []struct {
			Name  string `json:"name"`
			Value any    `json:"value"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(posts[0].body, &payload); err != nil {
		t.Fatalf("download client payload is not JSON: %v (%s)", err, posts[0].body)
	}
	if payload.Implementation != "QBittorrent" {
		t.Errorf("payload implementation = %q, want %q", payload.Implementation, "QBittorrent")
	}
	if payload.Priority != 1 {
		t.Errorf("payload priority = %d, want 1", payload.Priority)
	}
	if !payload.Enable {
		t.Error("payload enable = false, want true")
	}
	fields := map[string]any{}
	for _, f := range payload.Fields {
		fields[f.Name] = f.Value
	}
	want := map[string]any{
		"host":                "apps-qbittorrent",
		"port":                float64(8081),
		"useSsl":              false,
		"urlBase":             "",
		"username":            "",
		"password":            "",
		downloadCategoryField: downloadCategory,
	}
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("payload fields = %v, want %v", fields, want)
	}
	if tests := instance.requestsTo(http.MethodPost, downloadClientTestEndpoint); len(tests) != 1 {
		t.Errorf("POST %s count = %d, want 1: the create must be proven against the provider",
			downloadClientTestEndpoint, len(tests))
	}

	// A second run re-sends the category, which qBittorrent answers with 409:
	// that is the normal outcome and must not fail the node. The client itself
	// is already there and must be left alone.
	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("second PostStart() error = %v", err)
	}
	if got := qb.categoriesNow(); !reflect.DeepEqual(got, []string{downloadCategory}) {
		t.Errorf("qBittorrent categories after a second run = %v, want one entry", got)
	}
	if creates := qb.requestsTo(http.MethodPost, qbittorrentCategoryResource); len(creates) != 2 {
		t.Errorf("createCategory count = %d, want 2 (the second answers 409 and is accepted)", len(creates))
	}
	if posts := instance.requestsTo(http.MethodPost, downloadClientEndpoint); len(posts) != 1 {
		t.Errorf("POST %s count = %d after a second run, want 1", downloadClientEndpoint, len(posts))
	}
}

// An uninstalled provider is pruned, and pruned through the coordinates the
// binding carries: the entry Bloud wrote is identified by node and port, so a
// mismatch between what the orchestrator resolves and what Radarr stores would
// leave a dead client behind. The provider itself is not contacted at all - an
// app that is gone cannot answer, and nothing needs its opinion to remove what
// Bloud wrote for it.
func TestPostStart_PrunesStaleDownloadClientWhenProviderIsGone(t *testing.T) {
	instance := newFakeInstance("external")
	instance.clients = []fakeClient{bloudClient(3)}
	qb := newFakeQBittorrent()
	c := configuratorFor(t, instance, qb)
	state := appState(t)
	withDownloadClient(state, qb, false)
	withConfig(t, c, state)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v, want nil: an absent provider is not a failure", err)
	}
	if got := instance.clientsNow(); len(got) != 0 {
		t.Errorf("download clients after the provider went away = %v, want none", got)
	}
	if deletes := instance.deletedPaths(); !reflect.DeepEqual(deletes, []string{downloadClientEndpoint + "/3"}) {
		t.Errorf("DELETE paths = %v, want [%s/3]", deletes, downloadClientEndpoint)
	}
	if creates := qb.requestsTo(http.MethodPost, qbittorrentCategoryResource); len(creates) != 0 {
		t.Errorf("createCategory count = %d, want 0: an uninstalled provider must not be written to", len(creates))
	}
	if got := instance.foldersNow(); !reflect.DeepEqual(got, []string{rootFolderMount}) {
		t.Errorf("root folders = %v, want [%s]: the root folder needs no provider", got, rootFolderMount)
	}
}

// An entry the operator added for the same implementation (a seedbox, a second
// daemon) is identified by its own address, not by the implementation id, so
// Bloud neither prunes it nor mistakes it for its own client.
func TestPostStart_LeavesForeignDownloadClientsAlone(t *testing.T) {
	t.Run("provider gone: the operator's entry survives", func(t *testing.T) {
		instance := newFakeInstance("external")
		instance.clients = []fakeClient{foreignClient(7)}
		qb := newFakeQBittorrent()
		c := configuratorFor(t, instance, qb)
		state := appState(t)
		withDownloadClient(state, qb, false)
		withConfig(t, c, state)

		if err := c.PostStart(context.Background(), state); err != nil {
			t.Fatalf("PostStart() error = %v, want nil", err)
		}
		got := instance.clientsNow()
		if len(got) != 1 || got[0].Name != "seedbox" {
			t.Errorf("download clients = %v, want the operator's seedbox untouched", got)
		}
		if deletes := instance.deletedPaths(); len(deletes) != 0 {
			t.Errorf("DELETE paths = %v, want none", deletes)
		}
	})

	t.Run("provider present: Bloud's client is added beside it", func(t *testing.T) {
		instance := newFakeInstance("external")
		instance.clients = []fakeClient{foreignClient(7)}
		qb := newFakeQBittorrent()
		c := configuratorFor(t, instance, qb)
		state := appState(t)
		withDownloadClient(state, qb, true)
		withConfig(t, c, state)

		if err := c.PostStart(context.Background(), state); err != nil {
			t.Fatalf("PostStart() error = %v", err)
		}
		got := instance.clientsNow()
		if len(got) != 2 {
			t.Fatalf("download clients = %v, want the seedbox plus Bloud's client", got)
		}
		if got[0].Name != "seedbox" {
			t.Errorf("first entry = %q, want the operator's entry left in place", got[0].Name)
		}
		added := got[1]
		if added.Implementation != "QBittorrent" {
			t.Errorf("added implementation = %q, want QBittorrent", added.Implementation)
		}
		if host, port := fakeField(added, "host"), fakeField(added, "port"); host != "apps-qbittorrent" || port != "8081" {
			t.Errorf("added client address = %s:%s, want apps-qbittorrent:8081", host, port)
		}
		if deletes := instance.deletedPaths(); len(deletes) != 0 {
			t.Errorf("DELETE paths = %v, want none: the operator's entry is not Bloud's to remove", deletes)
		}
	})
}

// fakeField renders one field of a fake client entry as a string.
func fakeField(c fakeClient, name string) string {
	for _, f := range c.Fields {
		if f.Name == name {
			return fmt.Sprintf("%v", f.Value)
		}
	}
	return ""
}

func TestPostStart_RejectedDownloadClientContractSurfacesError(t *testing.T) {
	instance := newFakeInstance("external")
	instance.testStatus = http.StatusBadRequest
	qb := newFakeQBittorrent()
	c := configuratorFor(t, instance, qb)
	state := appState(t)
	withDownloadClient(state, qb, true)
	withConfig(t, c, state)

	err := c.PostStart(context.Background(), state)
	if err == nil {
		t.Fatal("PostStart() error = nil, want the failed download-client test")
	}
	for _, want := range []string{"download client test", "400"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	// The document is proven before it is stored, so a rejected one leaves
	// nothing behind.
	if posts := instance.requestsTo(http.MethodPost, downloadClientEndpoint); len(posts) != 0 {
		t.Errorf("POST %s count = %d, want 0 when the test failed", downloadClientEndpoint, len(posts))
	}
}

func TestPostStart_ProviderGoneWithNothingToPruneIsANoOp(t *testing.T) {
	instance := newFakeInstance("external")
	qb := newFakeQBittorrent()
	c := configuratorFor(t, instance, qb)
	state := appState(t)
	withDownloadClient(state, qb, false)
	withConfig(t, c, state)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v, want nil", err)
	}
	fake := instance.clientsNow()
	if len(fake) != 0 {
		t.Errorf("download clients = %v, want none", fake)
	}
	if deletes := instance.deletedPaths(); len(deletes) != 0 {
		t.Errorf("DELETE paths = %v, want none when there is nothing to prune", deletes)
	}
}

// An installed provider that is up but failing *transiently* must not fail the
// node: ERROR is terminal in the orchestrator, so the app would stay "failed"
// over a condition the next reconciliation fixes by itself. It must not be
// mistaken for a provider that is gone either - the entry Bloud wrote is left
// in place, because the provider is still installed and will answer again.
func TestPostStart_TransientProviderFailureIsSkipped(t *testing.T) {
	instance := newFakeInstance("external")
	instance.clients = []fakeClient{bloudClient(3)}
	qb := newFakeQBittorrent()
	qb.create = http.StatusServiceUnavailable
	c := configuratorFor(t, instance, qb)
	state := appState(t)
	withDownloadClient(state, qb, true)
	withConfig(t, c, state)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v, want nil: a 5xx from the provider is retried, not fatal", err)
	}
	// The call went to the binding's address, from the host.
	if creates := qb.requestsTo(http.MethodPost, qbittorrentCategoryResource); len(creates) != 1 {
		t.Errorf("createCategory count = %d, want 1: the reconciliation has to try before giving up", len(creates))
	}
	if got := instance.clientsNow(); len(got) != 1 || got[0].ID != 3 {
		t.Errorf("download clients = %v, want Bloud's entry kept while the provider only fails transiently", got)
	}
	if deletes := instance.deletedPaths(); len(deletes) != 0 {
		t.Errorf("DELETE paths = %v, want none: an installed provider is never pruned", deletes)
	}
	if posts := instance.requestsTo(http.MethodPost, downloadClientEndpoint); len(posts) != 0 {
		t.Errorf("POST %s count = %d, want 0: the client must not store a category that does not exist",
			downloadClientEndpoint, len(posts))
	}
}

// A rejection is not a hiccup: a 403 (a key or whitelist the provider no longer
// accepts) does not fix itself, so it is reported with its status.
func TestPostStart_RejectedCategorySurfacesError(t *testing.T) {
	instance := newFakeInstance("external")
	qb := newFakeQBittorrent()
	qb.create = http.StatusForbidden
	c := configuratorFor(t, instance, qb)
	state := appState(t)
	withDownloadClient(state, qb, true)
	withConfig(t, c, state)

	err := c.PostStart(context.Background(), state)
	if err == nil {
		t.Fatal("PostStart() error = nil, want the rejected category creation")
	}
	for _, want := range []string{downloadCategory, "403"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if posts := instance.requestsTo(http.MethodPost, downloadClientEndpoint); len(posts) != 0 {
		t.Errorf("POST %s count = %d, want 0: the client must not store a category that does not exist",
			downloadClientEndpoint, len(posts))
	}
}

// PreStart has to leave the directories the container writes world-writable.
// LSIO's init chowns /config to `abc`, which under rootless podman is a host
// subuid the host agent is neither owner nor group member of, and Bloud's own
// later writes go through the directory (pkg/managedfile creates its temp file
// there), so a 0755 config dir turns the next auth repair into an EACCES
// PreStart failure, which is terminal for the node.
func TestPreStart_MakesTheContainerWritableDirectoriesWorldWritable(t *testing.T) {
	c := configuratorFor(t, newFakeInstance("external"), nil)
	state := appState(t)

	// A tree an operator or an earlier pass left tightly moded.
	dirs := []string{
		filepath.Join(state.DataPath, "config"),
		filepath.Join(state.BloudDataPath, "media", "movies"),
		filepath.Join(state.BloudDataPath, "downloads"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	for _, dir := range dirs {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if got := fi.Mode().Perm(); got != 0o777 {
			t.Errorf("%s mode = %o, want 777: the container's user and the host agent both have to write it", dir, got)
		}
	}
}
