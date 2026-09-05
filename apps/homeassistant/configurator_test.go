// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package homeassistant

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

type fakeSecrets struct{ pw string }

func (f *fakeSecrets) GenerateAppAdminPassword(string) (string, error) { return f.pw, nil }
func (f *fakeSecrets) GetAppSecret(string, string) string              { return "" }

func testOIDC() *configurator.OIDCOutput {
	return &configurator.OIDCOutput{
		ClientID:     "cid123",
		ClientSecret: "s3cr3t",
		IssuerURL:    "http://sso.localhost:8080/application/o/homeassistant/",
		RedirectURI:  "http://homeassistant.localhost:8080/auth/oidc/callback",
	}
}

type sliceWriter struct{ buf *[]byte }

func (s *sliceWriter) Write(p []byte) (int, error) {
	*s.buf = append(*s.buf, p...)
	return len(p), nil
}

// newZip builds a flat hass-oidc-auth release fixture (files at archive root,
// matching the real asset layout) and returns its bytes + sha256 hex.
func newZip(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var raw []byte
	zw := zip.NewWriter(&sliceWriter{buf: &raw})
	for name, content := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return raw, fmt.Sprintf("%x", sum)
}

func newTestConfigurator(t *testing.T, zipBody []byte, zipSHA string) *Configurator {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".zip") {
			w.Header().Set("Content-Type", "application/zip")
			w.Write(zipBody)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	c := NewConfigurator(0, &fakeSecrets{pw: "test-bootstrap-pw"}, nil)
	c.componentURL = srv.URL + "/hass-oidc-auth.zip"
	c.componentSHA = zipSHA
	c.pollInterval = 10 * time.Millisecond
	c.postStartTimeout = 2 * time.Second
	return c
}

func TestPreStartCreatesDirs(t *testing.T) {
	c := NewConfigurator(0, &fakeSecrets{}, nil)
	data := t.TempDir()
	changed, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: data})
	require.NoError(t, err)
	assert.False(t, changed)
	_, err = os.Stat(filepath.Join(data, "config"))
	require.NoError(t, err)
}

func TestPreStartInstallsComponentAndWritesBlock(t *testing.T) {
	zipBody, sha := newZip(t, map[string]string{
		"manifest.json": `{"domain":"auth_oidc","name":"OIDC Auth","version":"v1.2.1"}`,
		"__init__.py":   "# integration\n",
	})
	c := newTestConfigurator(t, zipBody, sha)
	data := t.TempDir()
	state := &configurator.AppState{DataPath: data, SSOEnabled: true, OIDC: testOIDC()}

	changed, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.True(t, changed, "first PreStart must report changed")

	// component installed at the right path with the right manifest
	manifest, err := os.ReadFile(filepath.Join(data, "config", "custom_components", "auth_oidc", "manifest.json"))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), `"auth_oidc"`)

	// configuration.yaml block written with the full well-known discovery URL
	yaml, err := os.ReadFile(filepath.Join(data, "config", "configuration.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(yaml), `client_id: "cid123"`)
	assert.Contains(t, string(yaml), `client_secret: "s3cr3t"`)
	assert.Contains(t, string(yaml),
		`discovery_url: "http://sso.localhost:8080/application/o/homeassistant/.well-known/openid-configuration"`)
	assert.Contains(t, string(yaml), `admin: "authentik Admins"`)

	// second cycle is a no-op (no churn)
	changed2, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, changed2, "unchanged config must not churn")
}

func TestPreStartChecksumMismatchLeavesNoTree(t *testing.T) {
	zipBody, _ := newZip(t, map[string]string{
		"manifest.json": `{"domain":"auth_oidc","version":"v1.2.1"}`,
	})
	c := newTestConfigurator(t, zipBody, "000000000000000000000000000000000000000000000000000000000000000")
	data := t.TempDir()
	_, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: data, SSOEnabled: true, OIDC: testOIDC()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")

	// never a half-installed component tree
	_, err = os.Stat(filepath.Join(data, "config", "custom_components", "auth_oidc"))
	assert.True(t, os.IsNotExist(err), "must not leave a half-installed component")
	// and no configuration written
	_, err = os.Stat(filepath.Join(data, "config", "configuration.yaml"))
	assert.True(t, os.IsNotExist(err))
}

func TestPreStartPreservesUserConfigAndUpdatesOnDrift(t *testing.T) {
	zipBody, sha := newZip(t, map[string]string{"manifest.json": `{"domain":"auth_oidc","version":"v1.2.1"}`})
	c := newTestConfigurator(t, zipBody, sha)
	data := t.TempDir()
	cfgDir := filepath.Join(data, "config")
	require.NoError(t, os.MkdirAll(cfgDir, 0755))
	user := "default_config:\n\n# my custom stuff\nhistory:\n  include:\n    - domain: light\n"
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "configuration.yaml"), []byte(user), 0o644))

	state := &configurator.AppState{DataPath: data, SSOEnabled: true, OIDC: testOIDC()}
	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	// drift: client id rotates
	rotated := testOIDC()
	rotated.ClientID = "cid456"
	state.OIDC = rotated
	changed, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.True(t, changed)

	yaml, err := os.ReadFile(filepath.Join(cfgDir, "configuration.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(yaml), `client_id: "cid456"`)
	assert.NotContains(t, string(yaml), `client_id: "cid123"`)
	// user content intact
	assert.Contains(t, string(yaml), "# my custom stuff")
	assert.Contains(t, string(yaml), "default_config:")
	// exactly one managed block
	assert.Equal(t, 1, strings.Count(string(yaml), managedBegin))
	assert.Equal(t, 1, strings.Count(string(yaml), managedEnd))
}

func TestPreStartRemovesBlockWhenSSODisabled(t *testing.T) {
	zipBody, sha := newZip(t, map[string]string{"manifest.json": `{"domain":"auth_oidc","version":"v1.2.1"}`})
	c := newTestConfigurator(t, zipBody, sha)
	data := t.TempDir()
	cfgDir := filepath.Join(data, "config")
	require.NoError(t, os.MkdirAll(cfgDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "configuration.yaml"), []byte("default_config:\n"), 0o644))

	_, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: data, SSOEnabled: true, OIDC: testOIDC()})
	require.NoError(t, err)

	changed, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: data, SSOEnabled: false})
	require.NoError(t, err)
	assert.True(t, changed, "removing the leftover block is a change")
	yaml, err := os.ReadFile(filepath.Join(cfgDir, "configuration.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(yaml), managedBegin)
	assert.NotContains(t, string(yaml), "auth_oidc")
	assert.Contains(t, string(yaml), "default_config:")

	changed2, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: data, SSOEnabled: false})
	require.NoError(t, err)
	assert.False(t, changed2)
}

// apiServer is a fake Home Assistant for PostStart tests.
type apiServer struct {
	mu             sync.Mutex
	restarts       []string // Authorization headers seen on restart calls
	onboarded      bool
	postBodies     []string
	tokenReqs      []string // form bodies seen on /auth/token
	authCodeIssued bool     // an auth code was handed out and not yet exchanged
	oidcLive       bool
	deregistered   bool            // GET /api/onboarding always 404s (all steps closed)
	stepsCompleted map[string]bool // interactive step paths already closed
	srv            *httptest.Server
}

func newAPIServer(t *testing.T, oidcLive bool) *apiServer {
	t.Helper()
	s := &apiServer{oidcLive: oidcLive, stepsCompleted: map[string]bool{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/":
			w.WriteHeader(200)
			io.WriteString(w, `{"message":"API running."}`)
		case "/api/onboarding":
			s.mu.Lock()
			onboarded := s.onboarded
			dereg := s.deregistered
			s.mu.Unlock()
			if dereg {
				// Real HA deregisters this endpoint once every step is closed.
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `404: Not Found`)
				return
			}
			w.WriteHeader(200)
			if onboarded {
				// Realistic post-owner flow: remaining interactive steps
				// are still pending; onboarding must NOT be re-run.
				io.WriteString(w, `[{"step":"user","done":true},{"step":"core_config","done":false},{"step":"analytics","done":false},{"step":"integration","done":false}]`)
			} else {
				io.WriteString(w, `[{"step":"user","done":false},{"step":"core_config","done":false},{"step":"analytics","done":false},{"step":"integration","done":false}]`)
			}
		case "/api/onboarding/core_config", "/api/onboarding/analytics", "/api/onboarding/integration":
			s.mu.Lock()
			if s.stepsCompleted[r.URL.Path] {
				// Real HA: already-closed steps answer 403 (replay).
				s.mu.Unlock()
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `{"message":"step already done"}`)
				return
			}
			s.stepsCompleted[r.URL.Path] = true
			s.mu.Unlock()
			w.WriteHeader(200)
			io.WriteString(w, `{}`)
		case "/api/onboarding/users":
			body, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			if s.onboarded {
				// Real HA: the user step is already done → 403, no code re-issued.
				s.mu.Unlock()
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `{"message":"User step already done"}`)
				return
			}
			s.postBodies = append(s.postBodies, string(body))
			s.onboarded = true
			s.authCodeIssued = true
			s.mu.Unlock()
			w.WriteHeader(201)
			io.WriteString(w, `{"auth_code":"code123"}`)
		case "/auth/token":
			form, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			s.tokenReqs = append(s.tokenReqs, string(form))
			ok := s.authCodeIssued && strings.Contains(string(form), "grant_type=authorization_code") && strings.Contains(string(form), "code=code123")
			if ok {
				s.authCodeIssued = false // one-shot code, like real HA
			}
			s.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"invalid_request"}`)
				return
			}
			w.WriteHeader(200)
			io.WriteString(w, `{"access_token":"tok","token_type":"Bearer","refresh_token":"rtok","expires_in":1800}`)
		case "/auth/oidc/welcome":
			if s.isOIDCLive() {
				io.WriteString(w, `<!doctype html><title>Sign in with Bloud</title>`)
				return
			}
			http.NotFound(w, r)
		case "/api/services/homeassistant/restart":
			authz := r.Header.Get("Authorization")
			s.mu.Lock()
			// Real HA requires a valid admin bearer token; without one the call is
			// rejected (auth_middleware leaves it unauthenticated → 401).
			if authz == "" || authz == "Bearer " {
				s.mu.Unlock()
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			s.restarts = append(s.restarts, authz)
			s.mu.Unlock()
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *apiServer) isOIDCLive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.oidcLive
}

func (s *apiServer) setOIDCLive(v bool) {
	s.mu.Lock()
	s.oidcLive = v
	s.mu.Unlock()
}

func TestPostStartCompletesOnboardingAndVerifiesOIDC(t *testing.T) {
	srv := newAPIServer(t, false)
	c := NewConfigurator(0, &fakeSecrets{pw: "test-bootstrap-pw"}, nil)
	c.baseURLOverride = srv.srv.URL
	c.pollInterval = 10 * time.Millisecond
	c.postStartTimeout = 3 * time.Second

	// provider becomes live asynchronously, like HA finishing setup
	go func() {
		time.Sleep(50 * time.Millisecond)
		srv.setOIDCLive(true)
	}()

	require.NoError(t, c.PostStart(context.Background(), &configurator.AppState{SSOEnabled: true, OIDC: testOIDC()}))

	srv.mu.Lock()
	defer srv.mu.Unlock()
	require.Len(t, srv.postBodies, 1, "must create the owner exactly once")
	var req map[string]string
	require.NoError(t, json.Unmarshal([]byte(srv.postBodies[0]), &req))
	assert.Equal(t, onboardingClientID, req["client_id"])
	assert.Equal(t, bootstrapUsername, req["username"])
	assert.Equal(t, "test-bootstrap-pw", req["password"])
	assert.Equal(t, bootstrapFullname, req["name"])
}

// A fully-onboarded HA *deregisters* GET /api/onboarding: the endpoint 404s
// forever. The old code read that as "still booting", burned the whole
// postStart timeout into an ERROR — and since PostStart re-runs on every
// reconcile, the app could never reach 'running'. With a non-system owner in
// the auth store, the permanent 404 now means "already onboarded" (no token
// needed) and the probe never blocks.
func TestPostStartSkipsWhenAlreadyOnboarded(t *testing.T) {
	srv := newAPIServer(t, true)
	srv.mu.Lock()
	srv.deregistered = true
	srv.mu.Unlock()

	data := t.TempDir()
	writeOwnerFile(t, filepath.Join(data, "config"))

	c := NewConfigurator(0, &fakeSecrets{pw: "x"}, nil)
	c.baseURLOverride = srv.srv.URL
	c.pollInterval = 10 * time.Millisecond
	c.postStartTimeout = 2 * time.Second

	require.NoError(t, c.PostStart(context.Background(), &configurator.AppState{
		DataPath: data, SSOEnabled: true, OIDC: testOIDC(),
	}))
	srv.mu.Lock()
	defer srv.mu.Unlock()
	assert.Empty(t, srv.postBodies, "must not re-run onboarding")
}

// writeOwnerFile drops a minimal .storage/auth document holding a
// non-system-generated owner user — exactly what ownerOnDisk reads.
func writeOwnerFile(t *testing.T, cfgDir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(cfgDir, ".storage"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, ".storage", "auth"),
		[]byte(`{"version":1,"key":"auth","data":{"users":[{"id":"1","is_owner":true,"system_generated":false}]}}`), 0o600))
}

func TestPostStartFailsWhenProviderNeverLives(t *testing.T) {
	srv := newAPIServer(t, false)
	c := NewConfigurator(0, &fakeSecrets{}, nil)
	c.baseURLOverride = srv.srv.URL
	c.pollInterval = 10 * time.Millisecond
	c.postStartTimeout = 200 * time.Millisecond

	err := c.PostStart(context.Background(), &configurator.AppState{SSOEnabled: true, OIDC: testOIDC()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never became live")
}

func TestPostStartNoSSOSkipsOIDCProbe(t *testing.T) {
	srv := newAPIServer(t, false)
	srv.mu.Lock()
	srv.onboarded = true
	srv.mu.Unlock()
	c := NewConfigurator(0, &fakeSecrets{}, nil)
	c.baseURLOverride = srv.srv.URL
	c.pollInterval = 10 * time.Millisecond
	c.postStartTimeout = 300 * time.Millisecond

	require.NoError(t, c.PostStart(context.Background(), &configurator.AppState{SSOEnabled: false}))
}

func TestMergeManagedBlockAppendsToEmpty(t *testing.T) {
	out := mergeManagedBlock("", "auth_oidc:\n  client_id: \"x\"\n# END")
	assert.Contains(t, out, "auth_oidc:")
	assert.True(t, strings.HasSuffix(out, "\n"))
}

func TestYamlQuoteEscapes(t *testing.T) {
	assert.Equal(t, `"a\"b\\c"`, yamlQuote(`a"b\c`))
}

// --- reverse-proxy trust in the stored http config entry -----------------

// writeStoredHTTP writes a stored-config document for the http integration at
// <dir>/.storage/http (where <data> is the HA config dir) and returns its path.
func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".storage"), 0o755))
	p := filepath.Join(dir, ".storage", "http")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func readStoredJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

// configBlock returns the live settings block (v2: data.stable) nested under
// the config document's "data" object.
func configBlock(t *testing.T, doc map[string]any, key string) map[string]any {
	t.Helper()
	d, ok := doc["data"].(map[string]interface{})
	require.True(t, ok, "config file has no data section")
	v, ok := d[key].(map[string]interface{})
	require.True(t, ok, "missing %v section", key)
	return v
}

// storageJSON mirrors the v2 layout HA 2026.9 writes to .storage/http: live
// settings under data.stable (see INTEGRATION.md "Reverse proxy").
const storageJSON = `{
  "version": 2,
  "minor_version": 2,
  "key": "http",
  "data": {
    "stable": {
      "server_port": 8123,
      "trusted_proxies": ["10.0.0.0/8"],
      "use_x_forwarded_for": false,
      "custom_key": "keep"
    },
    "pending": null,
    "yaml_migration_done": true
  }
}`

// A fresh install must not get a hand-written config entry: HA rejects foreign
// entries and takes the whole web stack down with it.
func TestPreStartDoesNotCreateConfigFile(t *testing.T) {
	c := NewConfigurator(0, &fakeSecrets{}, nil)
	data := t.TempDir()

	changed, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: data})
	require.NoError(t, err)
	assert.False(t, changed, "must not create the stored http config before HA does")
	assert.NoFileExists(t, filepath.Join(data, "config", ".storage", "http"))
}

// Against a real-shaped stored file the configurator flips trust on, leaves
// unrelated values alone, and settles on the second cycle.
func TestPreStartPatchesStoredConfig(t *testing.T) {
	c := NewConfigurator(0, &fakeSecrets{}, nil)
	data := t.TempDir()
	path := writeConfig(t, filepath.Join(data, "config"), storageJSON)

	changed, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: data})
	require.NoError(t, err)
	assert.True(t, changed, "enabling proxy trust in an existing file is a change")

	doc := readStoredJSON(t, path)
	hcfg := configBlock(t, doc, "stable")
	assert.Equal(t, true, hcfg["use_x_forwarded_for"])
	assert.Equal(t, []interface{}{"10.0.0.0/8"}, hcfg["trusted_proxies"])
	assert.Equal(t, "keep", hcfg["custom_key"])
	assert.Equal(t, float64(8123), hcfg["server_port"])

	// second cycle: already trusted, no churn
	changed2, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: data})
	require.NoError(t, err)
	assert.False(t, changed2, "idempotent after first write")
}

// A corrupt stored file must surface as an error, not be silently ignored.
func TestPreStartRejectsCorruptConfigFile(t *testing.T) {
	c := NewConfigurator(0, &fakeSecrets{}, nil)
	data := t.TempDir()
	writeConfig(t, filepath.Join(data, "config"), "{ nope")

	_, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: data})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "valid JSON")
}

// End-to-end through the POST path: with the stored entry untrusted,
// PostStart must patch it AND restart Home Assistant via the API — that
// restart is what makes proxied OIDC callbacks work.
func TestPostStartAppliesConfigAndRestarts(t *testing.T) {
	fakeSrv := newAPIServer(t, true)

	data := t.TempDir()
	storedPath := writeConfig(t, filepath.Join(data, "config"), storageJSON)

	cfg := NewConfigurator(0, &fakeSecrets{pw: "pwd"}, nil)
	cfg.baseURLOverride = fakeSrv.srv.URL
	cfg.pollInterval = 10 * time.Millisecond
	cfg.postStartTimeout = 3 * time.Second

	require.NoError(t, cfg.PostStart(context.Background(), &configurator.AppState{
		DataPath:   data,
		SSOEnabled: true,
		OIDC:       testOIDC(),
	}))

	// the stored config file was rewritten with trust enabled
	doc := readStoredJSON(t, storedPath)
	h := configBlock(t, doc, "stable")
	assert.Equal(t, true, h["use_x_forwarded_for"])
	assert.Equal(t, []interface{}{"10.0.0.0/8"}, h["trusted_proxies"])

	// and HA was restarted through the API with the session token
	fakeSrv.mu.Lock()
	defer fakeSrv.mu.Unlock()
	require.Len(t, fakeSrv.restarts, 1)
	assert.Equal(t, "Bearer tok", fakeSrv.restarts[0])
}

// Regression: HA hands out an authorization CODE, never an access_token (core
// 2026.9 onboarding/views.py). The old code parsed "access_token" straight off
// the onboarding response and always got "" — so the post-trust restart died
// with "no access token". Onboard → exchange → restart must use the exchanged
// token, and the exchange must actually hit /auth/token.
func TestEnsureOnboardedExchangesAuthCodeForToken(t *testing.T) {
	srv := newAPIServer(t, false)
	c := NewConfigurator(0, &fakeSecrets{pw: "test-bootstrap-pw"}, nil)
	c.baseURLOverride = srv.srv.URL
	c.pollInterval = 10 * time.Millisecond
	c.postStartTimeout = 2 * time.Second

	token, err := c.ensureOnboarded(context.Background(), t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "tok", token, "must return the exchanged access token, not the raw code")

	srv.mu.Lock()
	defer srv.mu.Unlock()
	require.Len(t, srv.tokenReqs, 1, "must exchange the code exactly once")
	assert.Contains(t, srv.tokenReqs[0], "grant_type=authorization_code")
	assert.Contains(t, srv.tokenReqs[0], "code=code123")
	assert.Contains(t, srv.tokenReqs[0], "client_id="+url.QueryEscape(onboardingClientID))
}

// Regression for the reported failure: on a RETRY (owner already created), HA
// never re-issues an auth code — /api/onboarding/users returns 403 and there is
// no token to restart with. Patching reverse-proxy trust then cannot do the
// in-place restart; that must surface as a self-healing ERROR (the patched entry
// is on disk, so the next reconcile recreates the container and applies it) —
// never a silent success, and never a crash.
func TestPostStartAlreadyOnboardedDefersRestart(t *testing.T) {
	srv := newAPIServer(t, true)
	srv.mu.Lock()
	srv.onboarded = true // owner exists; onboarding step done → no token available
	srv.mu.Unlock()

	data := t.TempDir()
	storedPath := writeConfig(t, filepath.Join(data, "config"), storageJSON)

	cfg := NewConfigurator(0, &fakeSecrets{pw: "pwd"}, nil)
	cfg.baseURLOverride = srv.srv.URL
	cfg.pollInterval = 10 * time.Millisecond
	cfg.postStartTimeout = 2 * time.Second

	err := cfg.PostStart(context.Background(), &configurator.AppState{
		DataPath:   data,
		SSOEnabled: true,
		OIDC:       testOIDC(),
	})
	require.Error(t, err, "trust was patched but cannot be applied without a token")
	assert.Contains(t, err.Error(), "could not be restarted")

	// the trust IS still written to disk (so a container restart applies it)…
	doc := readStoredJSON(t, storedPath)
	h := configBlock(t, doc, "stable")
	assert.Equal(t, true, h["use_x_forwarded_for"])
	// …and no restart was attempted with an empty token.
	srv.mu.Lock()
	defer srv.mu.Unlock()
	assert.Empty(t, srv.restarts, "must not fire a tokenless restart")
}
