// SPDX-License-Identifier: AGPL-3.0-only

package sonarr

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

// rootFolderResource is the endpoint the fake serves, taken from the same
// description the shared lifecycle calls, so the fake cannot drift from it.
var rootFolderResource = app.RootFolderResource()

// Aliases for the shared Servarr vocabulary, read off this app's own
// description so the fakes cannot drift from what the lifecycle calls.
var (
	qbittorrentAppID            = servarr.QBittorrentAppID
	qbittorrentCategoryResource = servarr.QBittorrentCategoryResource
	rootFolderMount             = app.RootFolderMount
	downloadCategory            = app.DownloadCategory
	downloadCategoryField       = app.DownloadCategoryField
)

const (
	// existingAPIKey is the 32-hex key an instance generates for itself; the
	// configurator must never replace it.
	existingAPIKey = "0123456789abcdef0123456789abcdef"
	// hostPath is the /config/host resource under Sonarr's api/v3 root.
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

// fakeSecrets implements configurator.AppSecretsProvider for the tests. It
// records what an app publishes for itself, which is the value a consumer
// receives through its integration binding.
type fakeSecrets struct {
	published []publishedSecret
}

// publishedSecret is one SetAppSecret call.
type publishedSecret struct {
	app   string
	key   string
	value string
}

func (f *fakeSecrets) GenerateAppAdminPassword(string) (string, error) { return "", nil }

func (f *fakeSecrets) GetAppSecret(string, string) string { return "" }

func (f *fakeSecrets) SetAppSecret(app, key, value string) error {
	f.published = append(f.published, publishedSecret{app: app, key: key, value: value})
	return nil
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
// own provider's coordinates: the identity pkg/servarr matches on.
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

// fakeInstance stands in for a Sonarr instance: it models the three resources
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

// fakeQBittorrent stands in for the provider at the address the resolved
// binding points the configurator at: it models createCategory, including the
// 409 Conflict a repeated creation gets.
type fakeQBittorrent struct {
	mu       sync.Mutex
	create   int // createCategory status (0 → 200/409 from state)
	category []string
	requests []recordedRequest

	// url is the httptest server the provider answers on. configuratorFor fills
	// it in and withDownloadClient publishes it as the binding's LocalURL: the
	// vantage point the configurator calls the provider from.
	url string
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

// configuratorFor points a configurator at an httptest-backed Sonarr instance
// and starts the provider's server, so PostStart's whole path runs without a
// real stack. The provider's URL lands on the fake, where withDownloadClient
// picks it up as the binding's LocalURL.
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
	c.SetBaseURL(server.URL)
	if qb != nil {
		qbServer := httptest.NewServer(qb.handler())
		t.Cleanup(qbServer.Close)
		qb.url = qbServer.URL
	}
	return c
}

// withDownloadClient seeds the resolved downloadClient binding the orchestrator
// hands PostStart: the provider's catalog coordinates (Node and Port are what
// the app stores and what a prune matches on) plus LocalURL, the address the
// configurator reaches the provider on from the host.
//
// installed=false is the provider-gone case. The binding still carries the
// provider's address, which is what lets the prune recognise the entry Bloud
// wrote, and it is the only thing that means "not installed": a provider that is
// installed but not answering keeps its binding and is retried.
func withDownloadClient(state *configurator.AppState, qb *fakeQBittorrent, installed bool) {
	state.Integrations.DownloadClients = []configurator.DownloadClientBinding{{
		ProviderRef: configurator.ProviderRef{
			App:       qbittorrentAppID,
			Installed: installed,
			Node:      "apps-qbittorrent",
			Port:      8081,
			BaseURL:   "http://apps-qbittorrent:8081",
			LocalURL:  qb.url,
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
	if got := c.Name(); got != "apps-sonarr" {
		t.Errorf("Name() = %q, want %q", got, "apps-sonarr")
	}
}

func TestNewConfigurator_Port(t *testing.T) {
	if got := NewConfigurator(0, configurator.Deps{Logger: quietLogger()}).Port(); got != 8989 {
		t.Errorf("NewConfigurator(0).port = %d, want 8989", got)
	}
	if got := NewConfigurator(9000, configurator.Deps{Logger: quietLogger()}).Port(); got != 9000 {
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
		filepath.Join(state.BloudDataPath, "media", "shows"),
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
	mediaInfo, err := os.Stat(filepath.Join(state.BloudDataPath, "media", "shows"))
	if err != nil {
		t.Fatalf("stat media dir: %v", err)
	}
	if got := mediaInfo.Mode().Perm(); got != 0o777 {
		t.Errorf("media library mode = %o, want 0777", got)
	}

	cfg, err := xmlutil.Open(c.ConfigPath(state), "Config")
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
	writeConfig(t, c.ConfigPath(state), "<Config>\n  <Port>8989</Port>\n  <ApiKey>"+existingAPIKey+"</ApiKey>\n</Config>")

	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}

	cfg, err := xmlutil.Open(c.ConfigPath(state), "Config")
	if err != nil {
		t.Fatalf("open config.xml: %v", err)
	}
	if got := cfg.GetElement("ApiKey"); got != existingAPIKey {
		t.Errorf("ApiKey = %q, want the instance's own key %q", got, existingAPIKey)
	}
	if got := cfg.GetElement("Port"); got != "8989" {
		t.Errorf("Port = %q, want %q (unrelated keys must survive)", got, "8989")
	}
}

// PreStart publishes the instance's ApiKey under the name the app declares in
// `provides.pvr.secrets`, which is where Prowlarr and Seerr pick it up: consumers
// are handed the key through their integration binding, so none of them reads
// this instance's config.xml. The published value is the key the instance
// authenticates with, adopted from config.xml rather than generated here.
func TestPreStart_PublishesTheInstanceAPIKey(t *testing.T) {
	secrets := &fakeSecrets{}
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger(), Secrets: secrets})
	state := appState(t)
	writeConfig(t, c.ConfigPath(state), "<Config>\n  <ApiKey>"+existingAPIKey+"</ApiKey>\n</Config>")

	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}

	want := []publishedSecret{{app: appName, key: servarr.SecretAPIKey, value: existingAPIKey}}
	if got := secrets.published; !reflect.DeepEqual(got, want) {
		t.Errorf("published secrets = %v, want %v", got, want)
	}

	// Publishing is idempotent: the next pass republishes the same value, never
	// a fresh one (a rotated key would lock every consumer out of the instance).
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("second PreStart() error = %v", err)
	}
	if got := secrets.published; len(got) != 2 || got[1] != want[0] {
		t.Errorf("published secrets after a second pass = %v, want %v twice", got, want)
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
	key, err := servarr.APIKey(c.ConfigPath(state))
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
	if !strings.Contains(err.Error(), c.ConfigPath(state)) {
		t.Errorf("error %q does not name the config path %q", err, c.ConfigPath(state))
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
	// Host and port are the provider's catalog coordinates as the binding
	// carries them: the entry the app stores points at what the binding
	// resolved, and this consumer holds no copy of the provider's address.
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

// The binding's Installed flag is what says the provider is gone: uninstalling
// qBittorrent leaves the client Bloud wrote pointing at a container that no
// longer exists, so the entry is pruned. The binding still carries the
// provider's coordinates, which is how the prune recognises it.
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

// A provider that is installed but failing *transiently* must neither fail the
// node nor lose the link Bloud already wrote: ERROR is terminal in the
// orchestrator, so the app would stay "failed" over a condition the next
// reconciliation fixes by itself, and pruning on "did not answer" would drop a
// client that is about to come back.
func TestPostStart_TransientProviderFailureIsSkipped(t *testing.T) {
	instance := newFakeInstance("external")
	instance.clients = []fakeClient{bloudClient(3)}
	qb := newFakeQBittorrent()
	qb.create = http.StatusInternalServerError
	c := configuratorFor(t, instance, qb)
	state := appState(t)
	withDownloadClient(state, qb, true)
	withConfig(t, c, state)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v, want nil: a 5xx from the provider is retried, not fatal", err)
	}
	if posts := instance.requestsTo(http.MethodPost, downloadClientEndpoint); len(posts) != 0 {
		t.Errorf("POST %s count = %d, want 0: the client must not store a category that does not exist",
			downloadClientEndpoint, len(posts))
	}
	// Only Installed=false prunes: an installed provider that is not answering
	// keeps its entry, and the next pass retries the link.
	if got := instance.clientsNow(); len(got) != 1 || got[0].ID != 3 {
		t.Errorf("download clients = %v, want the existing entry kept", got)
	}
	if deletes := instance.deletedPaths(); len(deletes) != 0 {
		t.Errorf("DELETE paths = %v, want none: a provider that is merely unreachable is still installed", deletes)
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
		filepath.Join(state.BloudDataPath, "media", "shows"),
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
