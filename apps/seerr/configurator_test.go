// SPDX-License-Identifier: AGPL-3.0-only

package seerr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSecrets implements configurator.AppSecretsProvider. The credentials Seerr
// onboards and repairs with come from the bindings the orchestrator resolves,
// so this only has to satisfy the dependency the configurator is built with.
type fakeSecrets struct {
	password string
}

func (f *fakeSecrets) GenerateAppAdminPassword(string) (string, error) { return f.password, nil }

func (f *fakeSecrets) GetAppSecret(string, string) string { return "" }

func (f *fakeSecrets) SetAppSecret(string, string, string) error { return nil }

// recordedRequest is one request the fake Seerr received.
type recordedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

// fakeSeerr is an httptest-backed stand-in for a Seerr instance. It records
// every request so tests can assert the onboarding flow's exact calls, and
// models the pieces of server state the flow depends on: whether
// settings/public reports the instance as initialized, and the two DVR lists
// it stores PVR entries in.
type fakeSeerr struct {
	mu          sync.Mutex
	requests    []recordedRequest
	initialized bool
	// initializeFlips models whether POST /settings/initialize really persists
	// the public flag.
	initializeFlips bool
	// Jellyfin is already configured, which is when Seerr rejects the
	// wizard's hostname (500 "Jellyfin hostname already configured").
	jellyfinAlreadyConfigured bool
	libraryIDs                []string

	// dvrs holds the stored DVR entries per route segment ("radarr"/"sonarr"),
	// as maps so a test can seed any shape and the handler can echo bodies
	// back verbatim. nextDVRID is what POST assigns
	// (server/routes/settings/radarr.ts:15-37 appends with last id + 1).
	dvrs      map[string][]map[string]any
	nextDVRID int

	// jellyfinSettings is what POST /api/v1/settings/jellyfin last stored.
	jellyfinSettings map[string]any
}

// storedJellyfinSettings returns the settings.jellyfin the fake last stored.
func (f *fakeSeerr) storedJellyfinSettings() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jellyfinSettings
}

func newFakeSeerr() *fakeSeerr {
	return &fakeSeerr{
		initializeFlips: true,
		libraryIDs:      []string{"shows-key", "movies-key"},
	}
}

// setInitialized flips what GET /settings/public reports.
func (f *fakeSeerr) setInitialized(initialized bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initialized = initialized
}

// seedDVR stores one entry the way a configurator run would have left it.
func (f *fakeSeerr) seedDVR(service string, entry map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dvrs == nil {
		f.dvrs = map[string][]map[string]any{}
	}
	if id, ok := entry["id"].(int); ok && id >= f.nextDVRID {
		f.nextDVRID = id + 1
	}
	f.dvrs[service] = append(f.dvrs[service], entry)
}

// dvrList returns a copy of one stored list.
func (f *fakeSeerr) dvrList(service string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.dvrs[service]...)
}

func (f *fakeSeerr) record(r *http.Request) recordedRequest {
	body, _ := io.ReadAll(r.Body)
	req := recordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Header: r.Header.Clone(),
		Body:   body,
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	return req
}

func (f *fakeSeerr) all() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func (f *fakeSeerr) requestsFor(method, path string) []recordedRequest {
	var out []recordedRequest
	for _, r := range f.all() {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeSeerr) serveHTTP(w http.ResponseWriter, r *http.Request) {
	recorded := f.record(r)
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/settings/public":
		f.mu.Lock()
		initialized := f.initialized
		f.mu.Unlock()
		writeJSON(w, map[string]any{"initialized": initialized})

	case isDVRPath(r.URL.Path):
		f.serveDVR(w, r, recorded.Body)

	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/jellyfin":
		f.mu.Lock()
		alreadyConfigured := f.jellyfinAlreadyConfigured
		f.mu.Unlock()
		if alreadyConfigured {
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]any{"error": alreadyConfiguredError})
			return
		}
		writeJSON(w, map[string]any{"id": 1, "username": jellyfinAdminUsername})

	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/settings/jellyfin":
		// server/routes/settings/index.ts: the body is merged into
		// settings.jellyfin, but only after Seerr has tested the resulting
		// connection. The fake records the write; a real instance calls
		// Jellyfin here and answers 400 for a key it rejects.
		var doc map[string]any
		_ = json.Unmarshal(recorded.Body, &doc)
		f.mu.Lock()
		f.jellyfinSettings = doc
		f.mu.Unlock()
		writeJSON(w, doc)

	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/settings/jellyfin/library":
		f.mu.Lock()
		ids := append([]string(nil), f.libraryIDs...)
		f.mu.Unlock()
		libraries := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			libraries = append(libraries, map[string]any{"id": id, "name": id, "enabled": false})
		}
		writeJSON(w, libraries)

	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/settings/jellyfin/sync":
		writeJSON(w, map[string]any{"running": true})

	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/settings/initialize":
		f.mu.Lock()
		if f.initializeFlips {
			f.initialized = true
		}
		initialized := f.initialized
		f.mu.Unlock()
		writeJSON(w, map[string]any{"initialized": initialized})

	default:
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"error": "not found"})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v)
}

// isDVRPath reports whether path addresses a DVR list or one of its entries
// (/api/v1/settings/radarr, .../radarr/3).
func isDVRPath(path string) bool {
	for _, service := range []string{string(dvrRadarr), string(dvrSonarr)} {
		if path == apiRoot+"/settings/"+service || strings.HasPrefix(path, apiRoot+"/settings/"+service+"/") {
			return true
		}
	}
	return false
}

// serveDVR models the four DVR routes
// (server/routes/settings/radarr.ts:9-13,15-37,77-108,137-153): GET lists,
// POST appends with the next id and answers 201, PUT /:id replaces and answers
// 200, DELETE /:id removes and answers 200.
func (f *fakeSeerr) serveDVR(w http.ResponseWriter, r *http.Request, body []byte) {
	rest := strings.TrimPrefix(r.URL.Path, apiRoot+"/settings/")
	service, idPart, _ := strings.Cut(rest, "/")

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dvrs == nil {
		f.dvrs = map[string][]map[string]any{}
	}

	switch {
	case r.Method == http.MethodGet && idPart == "":
		writeJSON(w, f.dvrs[service])

	case r.Method == http.MethodPost && idPart == "":
		var entry map[string]any
		if err := json.Unmarshal(body, &entry); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "invalid body"})
			return
		}
		entry["id"] = f.nextDVRID
		f.nextDVRID++
		f.dvrs[service] = append(f.dvrs[service], entry)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, entry)

	case r.Method == http.MethodPut:
		id, err := strconv.Atoi(idPart)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]any{"error": "settings instance not found"})
			return
		}
		var entry map[string]any
		if err := json.Unmarshal(body, &entry); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "invalid body"})
			return
		}
		entry["id"] = id
		for i, stored := range f.dvrs[service] {
			if current, ok := stored["id"].(int); ok && current == id {
				f.dvrs[service][i] = entry
				writeJSON(w, entry)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"error": "settings instance not found"})

	case r.Method == http.MethodDelete:
		id, err := strconv.Atoi(idPart)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]any{"error": "settings instance not found"})
			return
		}
		for i, stored := range f.dvrs[service] {
			if current, ok := stored["id"].(int); ok && current == id {
				f.dvrs[service] = append(f.dvrs[service][:i], f.dvrs[service][i+1:]...)
				writeJSON(w, stored)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"error": "settings instance not found"})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		writeJSON(w, map[string]any{"error": "method not allowed"})
	}
}

// fakeJellyfin is a stand-in for the media server Seerr onboards against and
// keeps its connection to: it validates the API keys Bloud checks (GET
// /System/Info answers 401 for one it does not know), the admin login, and
// minting a key (POST /Auth/Keys answers 204, the token is read back from the
// list: the shape the pinned server really has). hits counts every request, so
// a test can prove a pass reached the media server, or did not.
type fakeJellyfin struct {
	server *httptest.Server
	hits   atomic.Int32

	// validKeys are the API keys this server accepts. Any other non-empty key
	// is answered 401, which is what a reinstalled server does to the key a
	// consumer kept.
	validKeys map[string]bool
	// mintKey is the token GET /Auth/Keys reports after a mint.
	mintKey string
	// adminPassword is what POST /Users/AuthenticateByName accepts ("" accepts
	// any).
	adminPassword string

	mu      sync.Mutex
	minted  []string
	checked []string
}

func newFakeJellyfin(t *testing.T) *fakeJellyfin {
	t.Helper()
	f := &fakeJellyfin{
		validKeys: map[string]bool{},
		mintKey:   "fresh-jellyfin-key",
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		switch {
		case r.URL.Path == jellyfinKeyPath:
			key := r.Header.Get(jellyfinKeyHeader)
			f.mu.Lock()
			f.checked = append(f.checked, key)
			valid := f.validKeys[key]
			f.mu.Unlock()
			if !valid {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeJSON(w, map[string]any{"Id": "jellyfin-server-id", "ServerName": "jellyfin"})

		case r.Method == http.MethodPost && r.URL.Path == jellyfinAuthPath:
			var body struct {
				Pw string `json:"Pw"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if f.adminPassword != "" && body.Pw != f.adminPassword {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeJSON(w, map[string]any{"AccessToken": "jellyfin-admin-token"})

		case r.Method == http.MethodPost && r.URL.Path == jellyfinKeysPath:
			app := r.URL.Query().Get("App")
			f.mu.Lock()
			f.minted = append(f.minted, app)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodGet && r.URL.Path == jellyfinKeysPath:
			f.mu.Lock()
			app := jellyfinKeyApp
			if len(f.minted) > 0 {
				app = f.minted[len(f.minted)-1]
			}
			f.mu.Unlock()
			writeJSON(w, map[string]any{
				"Items": []map[string]any{{
					"Id":          0,
					"AppName":     app,
					"AccessToken": f.mintKey,
					"DateCreated": "2026-01-01T00:00:00.0000000Z",
				}},
				"TotalRecordCount": 1,
				"StartIndex":       0,
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// checkedKeys returns the keys GET /System/Info was asked to validate.
func (f *fakeJellyfin) checkedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.checked...)
}

// mintedApps returns the App names POST /Auth/Keys was called with.
func (f *fakeJellyfin) mintedApps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.minted...)
}

// newConfigurator wires a configurator to the fake Seerr through the baseURL
// test seam. The providers are not wired here: their address and credentials
// reach the configurator as the bindings the orchestrator resolves into
// AppState, which is what newState builds.
func newConfigurator(t *testing.T, fake *fakeSeerr, secrets configurator.AppSecretsProvider) *Configurator {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	t.Cleanup(server.Close)

	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger(), Secrets: secrets})
	c.baseURL = server.URL
	return c
}

// deadURL returns a URL nothing listens on any more.
func deadURL(t *testing.T) string {
	t.Helper()
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	return url
}

// newState returns an AppState in the test's own temp dir carrying the
// integration bindings the orchestrator resolves. It is the only place a test
// says where a provider lives: a configurator discovers nothing itself.
func newState(t *testing.T, integrations configurator.Integrations) *configurator.AppState {
	t.Helper()
	return &configurator.AppState{
		DataPath:     filepath.Join(t.TempDir(), "seerr"),
		Integrations: integrations,
	}
}

// stateWithJellyfin is newState for the common stack: Seerr plus the installed
// Jellyfin it onboards against.
func stateWithJellyfin(t *testing.T, binding configurator.MediaServerBinding) *configurator.AppState {
	t.Helper()
	return newState(t, configurator.Integrations{
		MediaServers: []configurator.MediaServerBinding{binding},
	})
}

// jellyfinBinding is the `mediaServer` binding the orchestrator resolves for an
// installed Jellyfin: the node and port Seerr stores for the media server, the
// bootstrap admin password Jellyfin published, and the address this
// configurator reaches it at from the host (here, the fake). An empty password
// is the provider that has not published one yet, which defers onboarding.
func jellyfinBinding(localURL, password string) configurator.MediaServerBinding {
	return configurator.MediaServerBinding{
		ProviderRef: configurator.ProviderRef{
			App:       jellyfinAppName,
			Installed: true,
			Node:      "apps-jellyfin",
			Port:      8096,
			BaseURL:   "http://apps-jellyfin:8096",
			LocalURL:  localURL,
		},
		AdminPassword: password,
	}
}

// radarrBinding and sonarrBinding are the `pvr` bindings for the two PVRs Seerr
// can fulfil requests through, with the address and the key each published. An
// empty key is the provider that has not published one yet, which leaves its
// DVR entry alone.
func radarrBinding(localURL, apiKey string) configurator.PVRBinding {
	return configurator.PVRBinding{
		ProviderRef: configurator.ProviderRef{
			App:       radarrAppName,
			Installed: true,
			Node:      "apps-radarr",
			Port:      7878,
			BaseURL:   "http://apps-radarr:7878",
			LocalURL:  localURL,
		},
		APIKey: apiKey,
	}
}

func sonarrBinding(localURL, apiKey string) configurator.PVRBinding {
	return configurator.PVRBinding{
		ProviderRef: configurator.ProviderRef{
			App:       sonarrAppName,
			Installed: true,
			Node:      "apps-sonarr",
			Port:      8989,
			BaseURL:   "http://apps-sonarr:8989",
			LocalURL:  localURL,
		},
		APIKey: apiKey,
	}
}

// notInstalledPVR is the binding of a PVR the stack does not include. Only the
// edge changes: the address survives, because it comes from the provider's
// catalog metadata, which outlives its installation, and that is what lets a
// consumer recognise - and here, prune - the entry it wrote for it.
func notInstalledPVR(binding configurator.PVRBinding) configurator.PVRBinding {
	binding.Installed = false
	return binding
}

// notInstalledMediaServer is notInstalledPVR for the media server: the provider
// is not part of the stack, so Seerr's onboarding defers on it.
func notInstalledMediaServer(binding configurator.MediaServerBinding) configurator.MediaServerBinding {
	binding.Installed = false
	return binding
}

// fakePVR is an httptest-backed stand-in for one Servarr instance: it serves the
// quality profile list (the one call Seerr's configurator makes to a PVR) and
// records the API key that read carried.
type fakePVR struct {
	server *httptest.Server

	// profileStatus, when non-zero, is what the profile list answers with
	// instead of the list itself.
	profileStatus int
	profiles      []map[string]any

	mu          sync.Mutex
	profileKeys []string
}

func newFakePVR(t *testing.T, profiles []map[string]any) *fakePVR {
	t.Helper()
	f := &fakePVR{profiles: profiles}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case qualityProfilePath:
			f.mu.Lock()
			f.profileKeys = append(f.profileKeys, r.Header.Get(servarrAPIKeyHeader))
			status := f.profileStatus
			profiles := f.profiles
			f.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				writeJSON(w, map[string]any{"error": "unavailable"})
				return
			}
			writeJSON(w, profiles)
		default:
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]any{"error": "not found"})
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// profileKey returns the key the most recent profile read carried.
func (f *fakePVR) profileKey() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.profileKeys) == 0 {
		return ""
	}
	return f.profileKeys[len(f.profileKeys)-1]
}

// profileReads returns how many times the profile list was asked for.
func (f *fakePVR) profileReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.profileKeys)
}

// appAPIKey is the key a booted Seerr instance mints for itself
// (server/lib/settings/index.ts); the fixture below carries it exactly as the
// app would have written it.
const appAPIKey = "0123456789abcdef0123456789abcdef"

// writeAppSettings writes the settings.json a booted Seerr instance leaves
// behind: main.apiKey (minted during load) plus the top-level defaults whose
// presence gates its own startup. Bloud never writes this file (see
// INTEGRATION.md), so tests seed it the way the app does.
func writeAppSettings(t *testing.T, state *configurator.AppState) {
	t.Helper()
	writeAppSettingsWithJellyfinKey(t, state, "")
}

// writeAppSettingsWithJellyfinKey is writeAppSettings for an instance that has
// completed onboarding: settings.jellyfin records the media server's address
// and the key Seerr minted into it, which is what the connection check verifies.
func writeAppSettingsWithJellyfinKey(t *testing.T, state *configurator.AppState, jellyfinAPIKey string) {
	t.Helper()
	dir := filepath.Join(state.DataPath, configDirName)
	if err := os.MkdirAll(dir, configDirPerm); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"clientId": "test-client-id",
		"main":     map[string]any{"apiKey": appAPIKey, "mediaServerType": 4, "applicationUrl": ""},
		"network":  map[string]any{"trustProxy": false},
		"public":   map[string]any{"initialized": false},
	}
	if jellyfinAPIKey != "" {
		doc["jellyfin"] = map[string]any{
			"name":     "jellyfin",
			"ip":       "apps-jellyfin",
			"port":     8096,
			"apiKey":   jellyfinAPIKey,
			"useSsl":   false,
			"serverId": "jellyfin-server-id",
		}
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(state), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func settingsPath(state *configurator.AppState) string {
	return filepath.Join(state.DataPath, configDirName, settingsFileName)
}

func TestNewConfigurator_DefaultsPort(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{})
	if c.port != 5055 {
		t.Errorf("NewConfigurator(0).port = %d, want 5055", c.port)
	}
	if got := c.Name(); got != "apps-seerr" {
		t.Errorf("Name() = %q, want %q", got, "apps-seerr")
	}
}

func TestPreStart_CreatesWritableConfigDirOnly(t *testing.T) {
	state := &configurator.AppState{DataPath: filepath.Join(t.TempDir(), "seerr")}
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger(), Secrets: &fakeSecrets{}})

	changed, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	if changed.RestartNeeded {
		t.Error("PreStart() changed = true, want false (nothing the container reads at boot is written here)")
	}

	dir := filepath.Join(state.DataPath, configDirName)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != configDirPerm {
		t.Errorf("config dir mode = %o, want %o (the container runs as uid 1000)", perm, configDirPerm)
	}

	// Pre-seeding settings.json crash-loops the container: a partial file
	// replaces whole top-level default objects on load (see INTEGRATION.md).
	if _, err := os.Stat(settingsPath(state)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("settings.json exists (stat err = %v), want it untouched by Bloud", err)
	}
}

func TestPreStart_LeavesAnAppWrittenSettingsFileAlone(t *testing.T) {
	state := &configurator.AppState{DataPath: filepath.Join(t.TempDir(), "seerr")}
	dir := filepath.Join(state.DataPath, configDirName)
	if err := os.MkdirAll(dir, configDirPerm); err != nil {
		t.Fatal(err)
	}
	// The shape Seerr writes for itself: main.apiKey plus the unrelated keys a
	// booted instance carries, including the defaults that gate its startup.
	existing := `{
  "clientId": "abc123",
  "main": {"apiKey": "deadbeefdeadbeefdeadbeefdeadbeef", "mediaServerType": 4, "applicationUrl": ""},
  "network": {"trustProxy": false}
}`
	path := settingsPath(state)
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger(), Secrets: &fakeSecrets{}})
	for run := 1; run <= 2; run++ {
		changed, err := c.PreStart(context.Background(), state)
		if err != nil {
			t.Fatalf("PreStart() run %d error = %v", run, err)
		}
		if changed.RestartNeeded {
			t.Errorf("PreStart() run %d changed = true, want false", run)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != existing {
			t.Errorf("PreStart() run %d rewrote settings.json:\n--- want\n%s\n--- got\n%s", run, existing, after)
		}
	}
}

func TestPostStart_ReturnsEarlyWhenAlreadyInitialized(t *testing.T) {
	fake := newFakeSeerr()
	fake.initialized = true
	jellyfin := newFakeJellyfin(t)
	c := newConfigurator(t, fake, &fakeSecrets{})

	// No settings.json: an instance that has not written one has no stored key
	// to check and no PVR list to wire, so the pass is the initialization read
	// alone.
	state := stateWithJellyfin(t, jellyfinBinding(jellyfin.server.URL, "pw"))
	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	if got := fake.all(); len(got) != 1 || got[0].Path != "/api/v1/settings/public" {
		t.Errorf("requests = %s, want only GET /api/v1/settings/public", requestSummary(got))
	}
	for _, path := range []string{"/api/v1/auth/jellyfin", "/api/v1/settings/initialize"} {
		if got := fake.requestsFor(http.MethodPost, path); len(got) != 0 {
			t.Errorf("%s calls = %d, want 0: the wizard is not re-run", path, len(got))
		}
	}
	if got := jellyfin.hits.Load(); got != 0 {
		t.Errorf("media server calls = %d, want 0: with no settings.json there is no key to verify", got)
	}
}

func TestPostStart_DefersWhenTheMediaServerIsNotUsable(t *testing.T) {
	// Seerr's only non-interactive onboarding path starts with a Jellyfin
	// login, so it needs a Jellyfin that is part of the stack and has published
	// its bootstrap admin password. Neither a media server the stack does not
	// include nor one that has not published a password yet is an error: the
	// node stays up (its setup wizard is reachable) and the next
	// reconciliation picks the media server up.
	tests := []struct {
		name    string
		binding func(jellyfinURL string) configurator.MediaServerBinding
	}{
		{
			name: "not installed",
			binding: func(string) configurator.MediaServerBinding {
				return notInstalledMediaServer(jellyfinBinding("", "pw"))
			},
		},
		{
			name: "installed but its password is not published yet",
			binding: func(jellyfinURL string) configurator.MediaServerBinding {
				return jellyfinBinding(jellyfinURL, "")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeSeerr()
			// The media server answers any call, so a call it must not receive
			// is visible rather than a connection error.
			jellyfin := newFakeJellyfin(t)
			c := newConfigurator(t, fake, &fakeSecrets{})

			// No PreStart and no settings.json: deferral is decided before the
			// API key is needed, so it must not depend on the config file.
			state := newState(t, configurator.Integrations{
				MediaServers: []configurator.MediaServerBinding{tt.binding(jellyfin.server.URL)},
			})
			if err := c.PostStart(context.Background(), state); err != nil {
				t.Fatalf("PostStart() error = %v, want nil (no usable media server defers onboarding)", err)
			}

			if got := fake.all(); len(got) != 1 || got[0].Path != "/api/v1/settings/public" {
				t.Errorf("seerr requests = %s, want only the initialization read", requestSummary(got))
			}
			if writes := dvrWrites(fake); len(writes) != 0 {
				t.Errorf("DVR writes = %+v, want none (PVR wiring is deferred with the wizard)", writes)
			}
			if got := jellyfin.hits.Load(); got != 0 {
				t.Errorf("media server calls = %d, want 0 (nothing is attempted with no usable media server)", got)
			}
		})
	}
}

func TestPostStart_OnboardsThroughJellyfinAndInitializes(t *testing.T) {
	ctx := context.Background()
	const jellyfinPassword = "jellyfin-admin-pw"

	fake := newFakeSeerr()
	jellyfin := newFakeJellyfin(t)
	c := newConfigurator(t, fake, &fakeSecrets{password: jellyfinPassword})

	state := stateWithJellyfin(t, jellyfinBinding(jellyfin.server.URL, jellyfinPassword))
	if _, err := c.PreStart(ctx, state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	// The container writes settings.json (with its own API key) while booting;
	// PostStart then reads that key for the admin-only calls.
	writeAppSettings(t, state)

	if err := c.PostStart(ctx, state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	assertJellyfinLoginPayload(t, fake, "apps-jellyfin", 8096, jellyfinPassword)
	assertLibrarySync(t, fake)
	assertAdminCallsCarryKey(t, fake, appAPIKey)
	assertInitializeOrder(t, fake)

	// A second reconciliation is a no-op.
	before := len(fake.all())
	if err := c.PostStart(ctx, state); err != nil {
		t.Fatalf("second PostStart() error = %v", err)
	}
	if after := fake.all(); len(after) != before+1 {
		t.Errorf("second PostStart() made %d request(s), want 1 (the public check)", len(after)-before)
	}
}

// assertJellyfinLoginPayload asserts the admin account was created from the
// Jellyfin bootstrap admin, at the media server's address, with exactly the
// payload Seerr's own wizard sends.
func assertJellyfinLoginPayload(t *testing.T, fake *fakeSeerr, hostname string, port int, password string) {
	t.Helper()
	logins := fake.requestsFor(http.MethodPost, "/api/v1/auth/jellyfin")
	if len(logins) != 1 {
		t.Fatalf("auth/jellyfin calls = %d, want 1", len(logins))
	}
	var payload map[string]any
	if err := json.Unmarshal(logins[0].Body, &payload); err != nil {
		t.Fatalf("decoding auth/jellyfin body %q: %v", logins[0].Body, err)
	}
	want := map[string]any{
		"username":   "bloud-bootstrap-admin",
		"password":   password,
		"hostname":   hostname,
		"port":       float64(port),
		"useSsl":     false,
		"urlBase":    "",
		"email":      "bloud-admin@localhost",
		"serverType": float64(2),
	}
	if !reflect.DeepEqual(payload, want) {
		t.Errorf("auth/jellyfin payload = %#v, want %#v", payload, want)
	}
}

// assertLibrarySync asserts the library step re-reads the libraries from
// Jellyfin, enables every one of them, and starts the scan.
func assertLibrarySync(t *testing.T, fake *fakeSeerr) {
	t.Helper()
	calls := fake.requestsFor(http.MethodGet, "/api/v1/settings/jellyfin/library")
	if len(calls) != 2 {
		t.Fatalf("library calls = %d, want 2 (sync, then enable)", len(calls))
	}
	if got := calls[0].Query.Get("sync"); got != "true" {
		t.Errorf("first library call sync = %q, want \"true\"", got)
	}
	if got := calls[1].Query.Get("enable"); got != "shows-key,movies-key" {
		t.Errorf("second library call enable = %q, want %q", got, "shows-key,movies-key")
	}

	scans := fake.requestsFor(http.MethodPost, "/api/v1/settings/jellyfin/sync")
	if len(scans) != 1 {
		t.Fatalf("jellyfin/sync calls = %d, want 1", len(scans))
	}
	if string(scans[0].Body) != `{"start":true}` {
		t.Errorf("jellyfin/sync body = %s, want {\"start\":true}", scans[0].Body)
	}
}

// assertAdminCallsCarryKey asserts every ADMIN-only call was authenticated with
// the API key Seerr stores, and that there are exactly the expected four.
func assertAdminCallsCarryKey(t *testing.T, fake *fakeSeerr, key string) {
	t.Helper()
	var admin []recordedRequest
	admin = append(admin, fake.requestsFor(http.MethodGet, "/api/v1/settings/jellyfin/library")...)
	admin = append(admin, fake.requestsFor(http.MethodPost, "/api/v1/settings/jellyfin/sync")...)
	admin = append(admin, fake.requestsFor(http.MethodPost, "/api/v1/settings/initialize")...)
	if len(admin) != 4 {
		t.Fatalf("admin calls = %d, want 4 (sync, enable, scan, initialize)", len(admin))
	}
	for _, call := range admin {
		if got := call.Header.Get(apiKeyHeader); got != key {
			t.Errorf("%s %s %s = %q, want the instance's own key", call.Method, call.Path, apiKeyHeader, got)
		}
	}
}

// assertInitializeOrder asserts setup ends with settings/initialize and that the
// only call after it is the confirmation read of the public flag.
func assertInitializeOrder(t *testing.T, fake *fakeSeerr) {
	t.Helper()
	requests := fake.all()
	if len(requests) < 2 {
		t.Fatalf("requests = %d, want the full onboarding flow", len(requests))
	}
	if inits := fake.requestsFor(http.MethodPost, "/api/v1/settings/initialize"); len(inits) != 1 {
		t.Fatalf("settings/initialize calls = %d, want 1", len(inits))
	}
	beforeLast, last := requests[len(requests)-2], requests[len(requests)-1]
	if beforeLast.Method != http.MethodPost || beforeLast.Path != "/api/v1/settings/initialize" {
		t.Errorf("request before the confirmation = %s %s, want POST /api/v1/settings/initialize", beforeLast.Method, beforeLast.Path)
	}
	if last.Method != http.MethodGet || last.Path != "/api/v1/settings/public" {
		t.Errorf("last request = %s %s, want the confirming GET /api/v1/settings/public", last.Method, last.Path)
	}
}

// A key Jellyfin no longer accepts (the media server was purged and reinstalled)
// leaves every Jellyfin-backed Seerr feature failing with 401 and nothing in
// the UI explaining why. Seerr only mints a key while it onboards, so an
// initialized instance never repairs itself: Bloud has to notice, by using the
// key, and re-issue one through each app's own supported call.
func TestPostStart_RepairsAStaleJellyfinKey(t *testing.T) {
	const (
		staleKey = "11111111111111111111111111111111"
		freshKey = "22222222222222222222222222222222"
	)
	fake := newFakeSeerr()
	fake.setInitialized(true)
	jellyfin := newFakeJellyfin(t)
	jellyfin.adminPassword = "jellyfin-admin-pw"
	jellyfin.validKeys = map[string]bool{} // the reinstalled server knows no key
	jellyfin.mintKey = freshKey

	// The password the repair logs in with is the one the media server
	// published in its binding; the secrets store holds something else, which
	// this Jellyfin rejects.
	c := newConfigurator(t, fake, &fakeSecrets{password: "not-the-bootstrap-password"})
	state := stateWithJellyfin(t, jellyfinBinding(jellyfin.server.URL, jellyfin.adminPassword))
	writeAppSettingsWithJellyfinKey(t, state, staleKey)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	// The stored key was verified by using it...
	if got := jellyfin.checkedKeys(); !reflect.DeepEqual(got, []string{staleKey}) {
		t.Errorf("verified keys = %v, want the stored key %q", got, staleKey)
	}
	// ...a fresh one was minted in Jellyfin and pushed into Seerr...
	if got := jellyfin.mintedApps(); !reflect.DeepEqual(got, []string{jellyfinKeyApp}) {
		t.Errorf("minted apps = %v, want [%s]", got, jellyfinKeyApp)
	}
	pushes := fake.requestsFor(http.MethodPost, "/api/v1/settings/jellyfin")
	if len(pushes) != 1 {
		t.Fatalf("POST /settings/jellyfin count = %d, want 1", len(pushes))
	}
	if got := pushes[0].Header.Get(apiKeyHeader); got != appAPIKey {
		t.Errorf("push %s = %q, want Seerr's own key %q", apiKeyHeader, got, appAPIKey)
	}
	var push map[string]string
	if err := json.Unmarshal(pushes[0].Body, &push); err != nil {
		t.Fatalf("decode push body %q: %v", pushes[0].Body, err)
	}
	if push["apiKey"] != freshKey {
		t.Errorf("pushed apiKey = %q, want the minted %q", push["apiKey"], freshKey)
	}
	if got := fake.storedJellyfinSettings()["apiKey"]; got != freshKey {
		t.Errorf("Seerr stored apiKey = %v, want the minted %q", got, freshKey)
	}
	// ...and the library list was fetched again with the working credential.
	if got := len(fake.requestsFor(http.MethodGet, "/api/v1/settings/jellyfin/library")); got == 0 {
		t.Errorf("library calls = 0, want the library list re-fetched after the repair; seerr requests: %s; media server calls: %d",
			requestSummary(fake.all()), jellyfin.hits.Load())
	}
}

// requestSummary renders the requests a fake received, for failure messages.
func requestSummary(reqs []recordedRequest) string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Method+" "+r.Path)
	}
	return strings.Join(out, ", ")
}

// A key that still works is left alone: one check, no write, no mint.
func TestPostStart_LeavesAValidJellyfinKeyAlone(t *testing.T) {
	const goodKey = "33333333333333333333333333333333"
	fake := newFakeSeerr()
	fake.setInitialized(true)
	jellyfin := newFakeJellyfin(t)
	jellyfin.validKeys = map[string]bool{goodKey: true}

	c := newConfigurator(t, fake, &fakeSecrets{})
	state := stateWithJellyfin(t, jellyfinBinding(jellyfin.server.URL, "pw"))
	writeAppSettingsWithJellyfinKey(t, state, goodKey)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}
	if got := jellyfin.checkedKeys(); !reflect.DeepEqual(got, []string{goodKey}) {
		t.Errorf("verified keys = %v, want [%s]", got, goodKey)
	}
	if got := jellyfin.mintedApps(); len(got) != 0 {
		t.Errorf("minted apps = %v, want none for a working key", got)
	}
	if got := len(fake.requestsFor(http.MethodPost, "/api/v1/settings/jellyfin")); got != 0 {
		t.Errorf("POST /settings/jellyfin count = %d, want 0", got)
	}
}

// Jellyfin being unreachable is not evidence that the key is stale: nothing is
// minted, nothing is written, and the node is not failed over it.
func TestPostStart_UnreachableJellyfinLeavesTheCouplingAlone(t *testing.T) {
	fake := newFakeSeerr()
	fake.setInitialized(true)

	c := newConfigurator(t, fake, &fakeSecrets{})
	state := stateWithJellyfin(t, jellyfinBinding(deadURL(t), "pw"))
	writeAppSettingsWithJellyfinKey(t, state, "44444444444444444444444444444444")

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v, want nil: an unreachable media server is not a node failure", err)
	}
	if got := len(fake.requestsFor(http.MethodPost, "/api/v1/settings/jellyfin")); got != 0 {
		t.Errorf("POST /settings/jellyfin count = %d, want 0", got)
	}
}

func TestPostStart_ResumesWhenJellyfinIsAlreadyConfigured(t *testing.T) {
	// A crash between the login and settings/initialize leaves Jellyfin
	// configured with the admin already created. Seerr then rejects the
	// wizard's hostname with 500 "Jellyfin hostname already configured", which
	// must read as already done rather than block onboarding forever.
	fake := newFakeSeerr()
	fake.jellyfinAlreadyConfigured = true
	jellyfin := newFakeJellyfin(t)
	c := newConfigurator(t, fake, &fakeSecrets{})

	state := stateWithJellyfin(t, jellyfinBinding(jellyfin.server.URL, "pw"))
	writeAppSettings(t, state)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v, want nil (the admin already exists)", err)
	}
	if got := fake.requestsFor(http.MethodPost, "/api/v1/settings/initialize"); len(got) != 1 {
		t.Errorf("settings/initialize calls = %d, want 1", len(got))
	}
}

func TestPostStart_ErrorsWhenInitializeDoesNotFlipInitialized(t *testing.T) {
	fake := newFakeSeerr()
	fake.initializeFlips = false
	jellyfin := newFakeJellyfin(t)
	c := newConfigurator(t, fake, &fakeSecrets{})

	state := stateWithJellyfin(t, jellyfinBinding(jellyfin.server.URL, "pw"))
	writeAppSettings(t, state)

	err := c.PostStart(context.Background(), state)
	if err == nil {
		t.Fatal("PostStart() error = nil, want an error when initialized stays false")
	}
	if got := err.Error(); !regexp.MustCompile(`initialized=false`).MatchString(got) {
		t.Errorf("PostStart() error = %q, want it to name the observed initialized value", got)
	}
}

func TestPostStart_ErrorsWhenAPIKeyIsMissing(t *testing.T) {
	fake := newFakeSeerr()
	jellyfin := newFakeJellyfin(t)
	c := newConfigurator(t, fake, &fakeSecrets{})

	state := stateWithJellyfin(t, jellyfinBinding(jellyfin.server.URL, "pw"))
	err := c.PostStart(context.Background(), state)
	if err == nil {
		t.Fatal("PostStart() error = nil, want an error naming the missing settings.json")
	}
	if got := err.Error(); !regexp.MustCompile(regexp.QuoteMeta(settingsPath(state))).MatchString(got) {
		t.Errorf("PostStart() error = %q, want it to name %s", got, settingsPath(state))
	}
}

func TestPostStart_ErrorsWhenSettingsFileHasNoAPIKey(t *testing.T) {
	fake := newFakeSeerr()
	jellyfin := newFakeJellyfin(t)
	c := newConfigurator(t, fake, &fakeSecrets{})

	state := stateWithJellyfin(t, jellyfinBinding(jellyfin.server.URL, "pw"))
	dir := filepath.Join(state.DataPath, configDirName)
	if err := os.MkdirAll(dir, configDirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(state), []byte(`{"main":{"applicationUrl":""}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := c.PostStart(context.Background(), state)
	if err == nil {
		t.Fatal("PostStart() error = nil, want an error when main.apiKey is absent")
	}
	if got := err.Error(); !regexp.MustCompile(regexp.QuoteMeta(settingsPath(state))).MatchString(got) {
		t.Errorf("PostStart() error = %q, want it to name %s", got, settingsPath(state))
	}
}

func TestRemove_IsNoOp(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	if err := c.Remove(context.Background(), &configurator.AppState{}, true); err != nil {
		t.Errorf("Remove() error = %v, want nil", err)
	}
}

// The API keys the `pvr` bindings carry: 32 hex characters, the shape
// pkg/servarr generates and Servarr accepts.
const (
	radarrAPIKey = "11111111111111111111111111111111"
	sonarrAPIKey = "22222222222222222222222222222222"
)

// pvrFixture is a Seerr instance mid-onboarding with both PVRs installed: the
// fake Seerr has empty DVR lists, each fake PVR serves a quality profile list,
// and each `pvr` binding carries the address and the key Seerr stores.
type pvrFixture struct {
	c             *Configurator
	fake          *fakeSeerr
	radarr        *fakePVR
	sonarr        *fakePVR
	radarrBinding configurator.PVRBinding
	sonarrBinding configurator.PVRBinding
	state         *configurator.AppState
}

func newPVRFixture(t *testing.T) *pvrFixture {
	t.Helper()
	fake := newFakeSeerr()
	jellyfin := newFakeJellyfin(t)
	c := newConfigurator(t, fake, &fakeSecrets{})

	// Radarr ships an extra profile besides HD-1080p, so the tests that read
	// the entry also prove the preferred profile is chosen over the first.
	radarr := newFakePVR(t, []map[string]any{
		{"id": 1, "name": "Any"},
		{"id": 4, "name": "HD-1080p"},
	})
	sonarr := newFakePVR(t, []map[string]any{{"id": 4, "name": "HD-1080p"}})

	radarrIntegration := radarrBinding(radarr.server.URL, radarrAPIKey)
	sonarrIntegration := sonarrBinding(sonarr.server.URL, sonarrAPIKey)
	state := newState(t, configurator.Integrations{
		MediaServers: []configurator.MediaServerBinding{jellyfinBinding(jellyfin.server.URL, "pw")},
		PVRs:         []configurator.PVRBinding{radarrIntegration, sonarrIntegration},
	})
	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("PreStart() error = %v", err)
	}
	// The container writes settings.json (with its own API key) while booting;
	// PostStart then reads that key for the admin-only calls.
	writeAppSettings(t, state)

	return &pvrFixture{
		c: c, fake: fake, radarr: radarr, sonarr: sonarr,
		radarrBinding: radarrIntegration, sonarrBinding: sonarrIntegration,
		state: state,
	}
}

// setPVRBindings re-resolves the `pvr` integration, so a test can hand Seerr a
// different stack than the fixture's default (a PVR that is gone, or one that
// has not published its key yet) without building the whole fixture again.
func (f *pvrFixture) setPVRBindings(bindings ...configurator.PVRBinding) {
	f.state.Integrations.PVRs = bindings
}

// setProfiles replaces the profile list the fake PVR serves.
func (f *fakePVR) setProfiles(profiles []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profiles = profiles
}

// setProfileStatus makes the profile list answer with status instead.
func (f *fakePVR) setProfileStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profileStatus = status
}

// assertDVRRequest asserts exactly one request was made to path with the given
// payload, and (for writes) with Seerr's admin key.
func assertDVRRequest(t *testing.T, fake *fakeSeerr, method, path string, want map[string]any) {
	t.Helper()
	calls := fake.requestsFor(method, path)
	if len(calls) != 1 {
		t.Fatalf("%s %s calls = %d, want 1", method, path, len(calls))
	}
	var payload map[string]any
	if err := json.Unmarshal(calls[0].Body, &payload); err != nil {
		t.Fatalf("decoding %s %s body %q: %v", method, path, calls[0].Body, err)
	}
	if !reflect.DeepEqual(payload, want) {
		t.Errorf("%s %s payload =\n%#v\nwant\n%#v", method, path, payload, want)
	}
	if got := calls[0].Header.Get(apiKeyHeader); got != appAPIKey {
		t.Errorf("%s %s %s = %q, want the instance's own key", method, path, apiKeyHeader, got)
	}
}

// dvrWrites returns every DVR list mutation the fake saw.
func dvrWrites(fake *fakeSeerr) []recordedRequest {
	var out []recordedRequest
	for _, r := range fake.all() {
		if isDVRPath(r.Path) && r.Method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}

// radarrPayload is the body Seerr receives for a Radarr entry: the address,
// port and API key the `pvr` binding resolves, and the profile the PVR lists.
func radarrPayload(profileID int, profileName string) map[string]any {
	return map[string]any{
		"name":              "Radarr",
		"hostname":          "apps-radarr",
		"port":              float64(7878),
		"apiKey":            radarrAPIKey,
		"useSsl":            false,
		"baseUrl":           "",
		"activeProfileId":   float64(profileID),
		"activeProfileName": profileName,
		"activeDirectory":   "/movies",
		"isDefault":         true,
		"is4k":              false,
		"syncEnabled":       true,
		"tags":              []any{},
		// Required by RadarrSettings (seerr-api.yml).
		"minimumAvailability": minimumAvailabilityReleased,
	}
}

// sonarrPayload is that body for Sonarr, which carries the three
// SonarrSettings-only fields on top of the shared ones.
func sonarrPayload() map[string]any {
	payload := radarrPayload(4, "HD-1080p")
	payload["name"] = "Sonarr"
	payload["hostname"] = "apps-sonarr"
	payload["port"] = float64(8989)
	payload["apiKey"] = sonarrAPIKey
	payload["activeDirectory"] = "/shows"
	payload["seriesType"] = "standard"
	payload["animeSeriesType"] = "standard"
	payload["enableSeasonFolders"] = true
	// RadarrSettings-only: Sonarr's own schema has no such property, and Bloud
	// omits it from a Sonarr payload.
	delete(payload, "minimumAvailability")
	return payload
}

func TestPostStart_CreatesPVREntries(t *testing.T) {
	f := newPVRFixture(t)

	if err := f.c.PostStart(context.Background(), f.state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	// Radarr: the shared payload, with the profile picked out of its own list
	// (HD-1080p is not the first entry there).
	assertDVRRequest(t, f.fake, http.MethodPost, "/api/v1/settings/radarr", radarrPayload(4, "HD-1080p"))

	// Sonarr: the same plus the three SonarrSettings-only fields.
	assertDVRRequest(t, f.fake, http.MethodPost, "/api/v1/settings/sonarr", sonarrPayload())

	// The profile read authenticated with the PVR's own key, not Seerr's.
	if got := f.radarr.profileKey(); got != radarrAPIKey {
		t.Errorf("radarr profile read %s = %q, want the PVR's key", servarrAPIKeyHeader, got)
	}
	if got := f.sonarr.profileKey(); got != sonarrAPIKey {
		t.Errorf("sonarr profile read %s = %q, want the PVR's key", servarrAPIKeyHeader, got)
	}

	// Both entries are stored, and the DVR writes came after the instance was
	// marked initialized: the step is the last one of onboarding.
	if got := len(f.fake.dvrList("radarr")); got != 1 {
		t.Errorf("stored radarr entries = %d, want 1", got)
	}
	if got := len(f.fake.dvrList("sonarr")); got != 1 {
		t.Errorf("stored sonarr entries = %d, want 1", got)
	}
	assertDVRWiringAfterInitialize(t, f.fake)
}

// assertDVRWiringAfterInitialize asserts every DVR write happens after
// POST /settings/initialize, which is what makes a deferred onboarding defer
// PVR wiring with it.
func assertDVRWiringAfterInitialize(t *testing.T, fake *fakeSeerr) {
	t.Helper()
	requests := fake.all()
	initialize := -1
	for i, r := range requests {
		if r.Method == http.MethodPost && r.Path == "/api/v1/settings/initialize" {
			initialize = i
			break
		}
	}
	if initialize == -1 {
		t.Fatal("no POST /api/v1/settings/initialize was made")
	}
	for i, r := range requests {
		if isDVRPath(r.Path) && r.Method != http.MethodGet && i < initialize {
			t.Errorf("DVR write %s %s at position %d, before settings/initialize at %d", r.Method, r.Path, i, initialize)
		}
	}
}

// A provider's address and credentials come from the binding the orchestrator
// resolves: nothing is probed and nothing is read out of a provider's own files.
// Values no code could have hardcoded prove the payloads Seerr receives are
// exactly what its bindings hold.
func TestPostStart_UsesTheBindingsAddressAndSecrets(t *testing.T) {
	const (
		jellyfinNode     = "media.internal"
		jellyfinPort     = 8097
		jellyfinPassword = "binding-bootstrap-pw"
		radarrNode       = "pvr.internal"
		radarrPort       = 17878
		radarrKey        = "99999999999999999999999999999999"
	)
	fake := newFakeSeerr()
	jellyfin := newFakeJellyfin(t)
	radarr := newFakePVR(t, []map[string]any{{"id": 4, "name": "HD-1080p"}})
	c := newConfigurator(t, fake, &fakeSecrets{})

	state := newState(t, configurator.Integrations{
		MediaServers: []configurator.MediaServerBinding{{
			ProviderRef: configurator.ProviderRef{
				App:       jellyfinAppName,
				Installed: true,
				Node:      jellyfinNode,
				Port:      jellyfinPort,
				BaseURL:   "http://media.internal:8097",
				LocalURL:  jellyfin.server.URL,
			},
			AdminPassword: jellyfinPassword,
		}},
		PVRs: []configurator.PVRBinding{{
			ProviderRef: configurator.ProviderRef{
				App:       radarrAppName,
				Installed: true,
				Node:      radarrNode,
				Port:      radarrPort,
				BaseURL:   "http://pvr.internal:17878",
				LocalURL:  radarr.server.URL,
			},
			APIKey: radarrKey,
		}},
	})
	writeAppSettings(t, state)

	if err := c.PostStart(context.Background(), state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	// Seerr onboards against the media server the binding names, with the
	// password the binding carries...
	assertJellyfinLoginPayload(t, fake, jellyfinNode, jellyfinPort, jellyfinPassword)

	// ...and stores the PVR address and key it was handed.
	stored := fake.dvrList("radarr")
	if len(stored) != 1 {
		t.Fatalf("stored radarr entries = %d, want 1", len(stored))
	}
	if got := stored[0]["hostname"]; got != radarrNode {
		t.Errorf("stored hostname = %v, want the binding's %q", got, radarrNode)
	}
	if got := stored[0]["port"]; got != float64(radarrPort) {
		t.Errorf("stored port = %v, want the binding's %d", got, radarrPort)
	}
	if got := stored[0]["apiKey"]; got != radarrKey {
		t.Errorf("stored apiKey = %v, want the binding's %q", got, radarrKey)
	}
	// The key Seerr stores is the one the PVR was read with.
	if got := radarr.profileKey(); got != radarrKey {
		t.Errorf("radarr profile read %s = %q, want the binding's key", servarrAPIKeyHeader, got)
	}
}

func TestPostStart_SkipsUninstalledPVRs(t *testing.T) {
	// Bindings for PVRs the stack does not include: onboarding must not touch
	// them at all (no profile read, no DVR write) and must not fail over them.
	f := newPVRFixture(t)
	f.setPVRBindings(notInstalledPVR(f.radarrBinding), notInstalledPVR(f.sonarrBinding))

	if err := f.c.PostStart(context.Background(), f.state); err != nil {
		t.Fatalf("PostStart() error = %v, want nil (an uninstalled PVR is not an error)", err)
	}
	if writes := dvrWrites(f.fake); len(writes) != 0 {
		t.Errorf("DVR writes = %+v, want none with no PVR installed", writes)
	}
	if got := f.radarr.profileReads(); got != 0 {
		t.Errorf("radarr profile reads = %d, want 0", got)
	}
	if got := f.sonarr.profileReads(); got != 0 {
		t.Errorf("sonarr profile reads = %d, want 0", got)
	}
	// Onboarding itself still completed.
	if got := f.fake.requestsFor(http.MethodPost, "/api/v1/settings/initialize"); len(got) != 1 {
		t.Errorf("settings/initialize calls = %d, want 1", len(got))
	}
}

func TestPostStart_WiresPVRInstalledAfterOnboarding(t *testing.T) {
	// The instance was onboarded by an earlier pass that saw no PVRs.
	// Installing one creates the integration edge, and the staleness path
	// re-runs PostStart, which must wire the PVR without touching the wizard.
	f := newPVRFixture(t)
	f.fake.setInitialized(true)

	if err := f.c.PostStart(context.Background(), f.state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	if got := f.fake.requestsFor(http.MethodPost, "/api/v1/settings/radarr"); len(got) != 1 {
		t.Errorf("radarr POST calls = %d, want 1", len(got))
	}
	if got := f.fake.requestsFor(http.MethodPost, "/api/v1/settings/sonarr"); len(got) != 1 {
		t.Errorf("sonarr POST calls = %d, want 1", len(got))
	}
	for _, path := range []string{"/api/v1/auth/jellyfin", "/api/v1/settings/initialize", "/api/v1/settings/jellyfin/sync"} {
		if got := f.fake.requestsFor(http.MethodPost, path); len(got) != 0 {
			t.Errorf("initialized instance made %d POST %s call(s), want 0", len(got), path)
		}
	}
}

func TestReconcilePVRs_SecondRunWritesNothing(t *testing.T) {
	f := newPVRFixture(t)
	ctx := context.Background()

	if err := f.c.reconcilePVRs(ctx, f.state, appAPIKey); err != nil {
		t.Fatalf("first reconcilePVRs() error = %v", err)
	}
	if got := len(dvrWrites(f.fake)); got != 2 {
		t.Fatalf("first run DVR writes = %d, want 2 (one entry per PVR)", got)
	}

	before := len(f.fake.all())
	if err := f.c.reconcilePVRs(ctx, f.state, appAPIKey); err != nil {
		t.Fatalf("second reconcilePVRs() error = %v", err)
	}
	if got := dvrWrites(f.fake); len(got) != 2 {
		t.Errorf("DVR writes after the second run = %d, want 2 (nothing new)", len(got))
	}
	// The second run only reads each list: the entries already match.
	if got := len(f.fake.all()) - before; got != 2 {
		t.Errorf("second run requests = %d, want 2 (one list read per PVR)", got)
	}
	if got := len(f.fake.dvrList("radarr")); got != 1 {
		t.Errorf("stored radarr entries = %d, want 1 (POST must not be used to update)", got)
	}
}

func TestReconcilePVRs_RepairsDriftedEntry(t *testing.T) {
	f := newPVRFixture(t)
	// The entry Bloud added earlier, after the PVR changed port and key and
	// someone picked a different profile.
	drifted := radarrPayload(1, "Any")
	drifted["id"] = 3
	drifted["port"] = 1
	drifted["apiKey"] = "stale-key"
	f.fake.seedDVR("radarr", drifted)

	if err := f.c.PostStart(context.Background(), f.state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	if got := f.fake.requestsFor(http.MethodPost, "/api/v1/settings/radarr"); len(got) != 0 {
		t.Errorf("radarr POST calls = %d, want 0 (an update must not append a second entry)", len(got))
	}
	want := radarrPayload(4, "HD-1080p")
	want["id"] = float64(3)
	assertDVRRequest(t, f.fake, http.MethodPut, "/api/v1/settings/radarr/3", want)
}

func TestPostStart_PrunesStaleEntryWhenPVRIsGone(t *testing.T) {
	// The PVRs are uninstalled and the bindings say so: the entry Bloud wrote
	// for one is pruned, identified by the address the binding still carries,
	// while an entry an admin added by hand through the UI is left alone.
	f := newPVRFixture(t)
	f.setPVRBindings(notInstalledPVR(f.radarrBinding), notInstalledPVR(f.sonarrBinding))

	stale := sonarrPayload()
	stale["id"] = 7
	f.fake.seedDVR("sonarr", stale)
	manual := sonarrPayload()
	manual["id"] = 8
	manual["hostname"] = "localhost"
	manual["name"] = "Sonarr (manual)"
	f.fake.seedDVR("sonarr", manual)

	if err := f.c.PostStart(context.Background(), f.state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	if got := f.fake.requestsFor(http.MethodDelete, "/api/v1/settings/sonarr/7"); len(got) != 1 {
		t.Errorf("DELETE /api/v1/settings/sonarr/7 calls = %d, want 1", len(got))
	}
	if got := f.fake.requestsFor(http.MethodDelete, "/api/v1/settings/sonarr/8"); len(got) != 0 {
		t.Errorf("DELETE /api/v1/settings/sonarr/8 calls = %d, want 0 (an admin's own entry)", len(got))
	}
	if got := f.fake.requestsFor(http.MethodPost, "/api/v1/settings/sonarr"); len(got) != 0 {
		t.Errorf("sonarr POST calls = %d, want 0 (the PVR is gone)", len(got))
	}
	stored := f.fake.dvrList("sonarr")
	if len(stored) != 1 || stored[0]["id"] != 8 {
		t.Errorf("stored sonarr entries = %+v, want only the manual one", stored)
	}
	// A PVR that is gone is not asked anything.
	if got := f.sonarr.profileReads(); got != 0 {
		t.Errorf("sonarr profile reads = %d, want 0", got)
	}
}

func TestPostStart_KeepsTheEntryOfAPVRThatIsNotAnswering(t *testing.T) {
	// Not answering is not the same as gone: only an uninstalled PVR has its
	// entry pruned, so a 5xx (still booting, or a crashed instance) leaves the
	// stored entry untouched and the next reconciliation retries it.
	f := newPVRFixture(t)
	f.radarr.setProfileStatus(http.StatusServiceUnavailable)
	existing := radarrPayload(4, "HD-1080p")
	existing["id"] = 5
	f.fake.seedDVR("radarr", existing)

	if err := f.c.PostStart(context.Background(), f.state); err != nil {
		t.Fatalf("PostStart() error = %v, want nil (a PVR whose profiles are not listable yet is a retry)", err)
	}
	if got := f.fake.requestsFor(http.MethodPost, "/api/v1/settings/radarr"); len(got) != 0 {
		t.Errorf("radarr POST calls = %d, want 0", len(got))
	}
	if got := len(f.fake.dvrList("radarr")); got != 1 {
		t.Errorf("stored radarr entries = %d, want the existing entry kept", got)
	}
	if got := f.fake.requestsFor(http.MethodDelete, "/api/v1/settings/radarr/5"); len(got) != 0 {
		t.Errorf("DELETE /api/v1/settings/radarr/5 calls = %d, want 0 (an installed PVR is never pruned)", len(got))
	}
	// The other PVR is unaffected.
	if got := f.fake.requestsFor(http.MethodPost, "/api/v1/settings/sonarr"); len(got) != 1 {
		t.Errorf("sonarr POST calls = %d, want 1", len(got))
	}
}

func TestPostStart_FallsBackToFirstProfile(t *testing.T) {
	f := newPVRFixture(t)
	// No HD-1080p: the first profile the PVR lists is used instead.
	f.radarr.setProfiles([]map[string]any{
		{"id": 2, "name": "Any"},
		{"id": 3, "name": "HD-720p"},
	})

	if err := f.c.PostStart(context.Background(), f.state); err != nil {
		t.Fatalf("PostStart() error = %v", err)
	}

	stored := f.fake.dvrList("radarr")
	if len(stored) != 1 {
		t.Fatalf("stored radarr entries = %d, want 1", len(stored))
	}
	if got := stored[0]["activeProfileId"]; got != float64(2) {
		t.Errorf("activeProfileId = %v, want 2 (the first profile)", got)
	}
	if got := stored[0]["activeProfileName"]; got != "Any" {
		t.Errorf("activeProfileName = %v, want Any", got)
	}
}

func TestPostStart_ErrorsWhenPVRRejectsTheAPIKey(t *testing.T) {
	f := newPVRFixture(t)
	f.radarr.setProfileStatus(http.StatusUnauthorized)

	err := f.c.PostStart(context.Background(), f.state)
	if err == nil {
		t.Fatal("PostStart() error = nil, want a PVR that rejects us to fail the node")
	}
	for _, want := range []string{"radarr", "401", qualityProfilePath} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("PostStart() error = %q, want it to name %q", err, want)
		}
	}
}

func TestPostStart_SkipsPVRWithUnpublishedKey(t *testing.T) {
	// The PVR is installed but has not published its API key yet (its own
	// PreStart has not run, or it is still converging): the binding's key is
	// empty, so wire nothing rather than writing an entry with an empty key,
	// and leave the rest alone.
	f := newPVRFixture(t)
	unpublished := f.radarrBinding
	unpublished.APIKey = ""
	f.setPVRBindings(unpublished, f.sonarrBinding)

	if err := f.c.PostStart(context.Background(), f.state); err != nil {
		t.Fatalf("PostStart() error = %v, want nil", err)
	}
	if got := f.fake.requestsFor(http.MethodPost, "/api/v1/settings/radarr"); len(got) != 0 {
		t.Errorf("radarr POST calls = %d, want 0", len(got))
	}
	if got := f.radarr.profileReads(); got != 0 {
		t.Errorf("radarr profile reads = %d, want 0 (nothing is asked of a PVR with no key)", got)
	}
	if got := f.fake.requestsFor(http.MethodPost, "/api/v1/settings/sonarr"); len(got) != 1 {
		t.Errorf("sonarr POST calls = %d, want 1", len(got))
	}
}
