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
	t              *testing.T
	mu             sync.Mutex
	restarts       []string // container names handed to the restart callback
	onboarded      bool
	postBodies     []string
	tokenReqs      []string // form bodies seen on /auth/token
	authCodeIssued bool     // an auth code was handed out and not yet exchanged
	oidcLive       bool
	deregistered   bool            // GET /api/onboarding always 404s (all steps closed)
	stepsCompleted map[string]bool // interactive step paths already closed

	// Proxy-trust lifecycle (models HA's forwarded middleware). trustLive
	// mirrors what the RUNNING process has loaded: false answers any
	// X-Forwarded-For-bearing request with 400 ("not set-up for reverse
	// proxies") — the CI failure; a restart with a valid token flips it
	// (after trustFlipDelay, simulating reload latency) when
	// restartAppliesTrust. xffRejected records the probe addresses seen
	// while stale, so tests can assert the wait actually held.
	trustLive           bool
	xffRejected         []string
	restartAppliesTrust bool
	trustFlipDelay      time.Duration

	srv *httptest.Server
}

func newAPIServer(t *testing.T, oidcLive bool) *apiServer {
	s := &apiServer{t: t, oidcLive: oidcLive, stepsCompleted: map[string]bool{}, restartAppliesTrust: true}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/":
			// Model HA 2026.9's forwarded middleware: a request carrying
			// X-Forwarded-For is rejected 400 until the running process has
			// the trust loaded. Once trust is live the forward check passes and
			// the (unauthenticated) request falls through to the auth layer,
			// which answers 401 Bearer — the live-trust signal is "not 400".
			// An unforwarded (no-XFF) request always answers 200 here, so the
			// plain wait still sees the listener up.
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				s.mu.Lock()
				live := s.trustLive
				if !live {
					s.xffRejected = append(s.xffRejected, xff)
				}
				s.mu.Unlock()
				if !live {
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, "400: Bad Request")
					return
				}
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="http://localhost:8123/.well-known/oauth-protected-resource"`)
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, "401: Unauthorized")
				return
			}
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

func (s *apiServer) setTrustLive(v bool) {
	s.mu.Lock()
	s.trustLive = v
	s.mu.Unlock()
}

// restartContainer returns the host-runtime callback to inject into the
// configurator via SetRestartContainer. It models a real container stop+start:
// the re-exec'd process re-reads the patched .storage/http, so it flips
// trustLive (after trustFlipDelay, modelling restart latency) when
// restartAppliesTrust. restartAppliesTrust=false models a restart that never
// reloads, so the forwarded-400 persists. Records each container name it is
// asked to restart.
func (s *apiServer) restartContainer() func(context.Context, string) error {
	return func(_ context.Context, name string) error {
		s.mu.Lock()
		s.restarts = append(s.restarts, name)
		applies := s.restartAppliesTrust
		delay := s.trustFlipDelay
		s.mu.Unlock()
		if applies {
			if delay > 0 {
				timer := time.AfterFunc(delay, func() { s.setTrustLive(true) })
				s.t.Cleanup(func() { timer.Stop() })
			} else {
				s.setTrustLive(true)
			}
		}
		return nil
	}
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
// PostStart must patch it AND restart the Home Assistant CONTAINER — that
// restart is what makes proxied OIDC callbacks work. The restart is a
// host-runtime container restart (via the injected callback), not HA's own
// soft restart service, so it needs no admin token.
func TestPostStartAppliesConfigAndRestarts(t *testing.T) {
	fakeSrv := newAPIServer(t, true)

	data := t.TempDir()
	storedPath := writeConfig(t, filepath.Join(data, "config"), storageJSON)

	cfg := NewConfigurator(0, &fakeSecrets{pw: "pwd"}, nil)
	cfg.baseURLOverride = fakeSrv.srv.URL
	cfg.SetRestartContainer(fakeSrv.restartContainer())
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

	// and the container was restarted through the runtime (by its own name)
	fakeSrv.mu.Lock()
	defer fakeSrv.mu.Unlock()
	require.Len(t, fakeSrv.restarts, 1)
	assert.Equal(t, "apps-homeassistant", fakeSrv.restarts[0])
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

// Regression for the reported failure, now fixed: on a RETRY (owner already
// created), HA never re-issues an auth code, so there is no admin token — the
// old API-restart path died there and had to surface a self-healing ERROR. A
// container restart needs no token, so PostStart now succeeds on the retry: it
// patches the on-disk trust and restarts the container, which re-execs HA to
// load it. This is the case that used to strand the app in ERROR.
func TestPostStartAlreadyOnboardedAppliesTrustViaContainerRestart(t *testing.T) {
	srv := newAPIServer(t, true)
	srv.mu.Lock()
	srv.onboarded = true // owner exists; onboarding step done → no token available
	srv.mu.Unlock()

	data := t.TempDir()
	storedPath := writeConfig(t, filepath.Join(data, "config"), storageJSON)

	cfg := NewConfigurator(0, &fakeSecrets{pw: "pwd"}, nil)
	cfg.baseURLOverride = srv.srv.URL
	cfg.SetRestartContainer(srv.restartContainer())
	cfg.pollInterval = 10 * time.Millisecond
	cfg.postStartTimeout = 2 * time.Second

	require.NoError(t, cfg.PostStart(context.Background(), &configurator.AppState{
		DataPath:   data,
		SSOEnabled: true,
		OIDC:       testOIDC(),
	}))

	// trust is on disk…
	doc := readStoredJSON(t, storedPath)
	h := configBlock(t, doc, "stable")
	assert.Equal(t, true, h["use_x_forwarded_for"])
	// …and the container WAS restarted (no token needed), applying it live.
	srv.mu.Lock()
	defer srv.mu.Unlock()
	require.Len(t, srv.restarts, 1, "the retry must restart the container to apply trust")
	assert.Equal(t, "apps-homeassistant", srv.restarts[0])
	assert.True(t, srv.trustLive, "the restarted process loaded the patched trust")
}

// trustedStorageJSON: the stored http entry as it looks AFTER Bloud patched
// it (v2 layout, trust enabled) but before we know whether the running
// process loaded it.
const trustedStorageJSON = `{
  "version": 2,
  "minor_version": 2,
  "key": "http",
  "data": {
    "stable": {
      "server_port": 8123,
      "trusted_proxies": ["10.0.0.0/8"],
      "use_x_forwarded_for": true,
      "custom_key": "keep"
    },
    "pending": null,
    "yaml_migration_done": true
  }
}`

// The CI failure: the restart API returns 200 long before HA reloads (QEMU
// needed ~4.4s; the old code's waitForAPI passed in 336ms against the stale
// process). PostStart must hold until the FORWARDED probe goes live, not
// return on the first 200. The fake flips trust with a delay; the test
// asserts the wait actually observed the stale 400s before succeeding.
func TestPostStartWaitsForTrustReloadAfterRestart(t *testing.T) {
	srv := newAPIServer(t, true)
	srv.mu.Lock()
	srv.trustFlipDelay = 150 * time.Millisecond
	srv.mu.Unlock()

	data := t.TempDir()
	writeConfig(t, filepath.Join(data, "config"), storageJSON) // untrusted on disk

	c := NewConfigurator(0, &fakeSecrets{pw: "pwd"}, nil)
	c.baseURLOverride = srv.srv.URL
	c.SetRestartContainer(srv.restartContainer())
	c.pollInterval = 10 * time.Millisecond
	c.postStartTimeout = 5 * time.Second

	require.NoError(t, c.PostStart(context.Background(), &configurator.AppState{
		DataPath: data, SSOEnabled: true, OIDC: testOIDC(),
	}))

	srv.mu.Lock()
	defer srv.mu.Unlock()
	assert.NotEmpty(t, srv.xffRejected, "the trust wait must have seen the stale process reject forwarded requests before the reload landed")
}

// Restart accepted but never reloads (the ~100s-stale CI state): the wait
// must time out into an ERROR naming the reload failure — never a silent
// success that marks a stale, proxy-rejecting process RUNNING.
func TestPostStartFailsWhenRestartNeverAppliesTrust(t *testing.T) {
	srv := newAPIServer(t, true)
	srv.mu.Lock()
	srv.restartAppliesTrust = false
	srv.mu.Unlock()

	data := t.TempDir()
	writeConfig(t, filepath.Join(data, "config"), storageJSON)

	c := NewConfigurator(0, &fakeSecrets{pw: "pwd"}, nil)
	c.baseURLOverride = srv.srv.URL
	c.SetRestartContainer(srv.restartContainer())
	c.pollInterval = 10 * time.Millisecond
	c.postStartTimeout = 300 * time.Millisecond

	err := c.PostStart(context.Background(), &configurator.AppState{
		DataPath: data, SSOEnabled: true, OIDC: testOIDC(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reverse-proxy trust")
	assert.Contains(t, err.Error(), "restart never took effect")

	srv.mu.Lock()
	defer srv.mu.Unlock()
	assert.NotEmpty(t, srv.xffRejected, "must have polled the running process until the deadline")
}

// Steady-state self-heal wiring: the disk entry is trusted but the RUNNING
// process still rejects forwarded requests (restart fired on an earlier
// pass and never took). PreStart must report changed=true with no file
// rewrite, forcing the orchestrator's container-recreate — a cold boot
// applies the patch with no admin token needed.
func TestPreStartForcesRecreateWhenRunningProcessStale(t *testing.T) {
	srv := newAPIServer(t, true)
	srv.mu.Lock()
	srv.restartAppliesTrust = false
	srv.mu.Unlock()

	data := t.TempDir()
	storedPath := writeConfig(t, filepath.Join(data, "config"), trustedStorageJSON)

	c := NewConfigurator(0, &fakeSecrets{pw: "x"}, nil)
	c.baseURLOverride = srv.srv.URL
	c.pollInterval = 10 * time.Millisecond

	changed, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: data})
	require.NoError(t, err)
	assert.True(t, changed, "stale live process against trusted disk entry must force a recreate")

	// the file is untouched (no rewrite churn)
	doc := readStoredJSON(t, storedPath)
	h := configBlock(t, doc, "stable")
	assert.Equal(t, true, h["use_x_forwarded_for"])

	srv.mu.Lock()
	defer srv.mu.Unlock()
	assert.NotEmpty(t, srv.xffRejected, "the staleness check must probe the running process")
}

// A refused probe (fresh install / mid-crash — process not up yet) must NOT
// force a recreate: the normal start path handles that; forcing would churn
// containers during recovery.
func TestPreStartDoesNotForceWhenProcessUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	url := srv.URL
	srv.Close() // closed port → connection refused

	data := t.TempDir()
	writeConfig(t, filepath.Join(data, "config"), trustedStorageJSON)

	c := NewConfigurator(0, &fakeSecrets{pw: "x"}, nil)
	c.baseURLOverride = url
	c.pollInterval = 10 * time.Millisecond

	changed, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: data})
	require.NoError(t, err)
	assert.False(t, changed, "unreachable process must not force a recreate")
}

// The probe itself: 400 on forwarded requests reads as not-live-but-
// reachable; 200 reads as live. (Unit-guards the three-state mapping the
// waits above depend on.)
func TestProbeProxyTrustStates(t *testing.T) {
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer live.Close()
	c := NewConfigurator(0, &fakeSecrets{}, nil)
	c.baseURLOverride = live.URL
	ok, reachable, status, perr := c.probeProxyTrust(context.Background())
	assert.True(t, ok)
	assert.True(t, reachable)
	assert.Equal(t, 200, status)
	assert.NoError(t, perr)

	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
	}))
	defer stale.Close()
	c.baseURLOverride = stale.URL
	ok, reachable, status, perr = c.probeProxyTrust(context.Background())
	assert.False(t, ok)
	assert.True(t, reachable)
	assert.Equal(t, 400, status)
	assert.NoError(t, perr)

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	downURL := down.URL
	down.Close()
	c.baseURLOverride = downURL
	ok, reachable, status, perr = c.probeProxyTrust(context.Background())
	assert.False(t, ok)
	assert.False(t, reachable)
	assert.Equal(t, 0, status)
	assert.Error(t, perr)
}
