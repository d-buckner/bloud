// SPDX-License-Identifier: AGPL-3.0-only

package jellyfinmcp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fakes ---

// fakeSecrets is an in-memory stand-in for the secrets manager. It records
// every write so a test can assert both what got persisted and that a later
// pass did not persist it again.
type fakeSecrets struct {
	mu     sync.Mutex
	values map[string]string
	sets   []string
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{values: map[string]string{}}
}

func (f *fakeSecrets) key(app, k string) string { return app + "/" + k }

func (f *fakeSecrets) Get(app, k string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[f.key(app, k)]
}

func (f *fakeSecrets) GenerateAppAdminPassword(string) (string, error) { return "unused", nil }

func (f *fakeSecrets) GetAppSecret(app, k string) string { return f.Get(app, k) }

func (f *fakeSecrets) SetAppSecret(app, k, v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, f.key(app, k))
	f.values[f.key(app, k)] = v
	return nil
}

func (f *fakeSecrets) SetAppContractValue(string, string, string, string) error { return nil }
func (f *fakeSecrets) GetAppContractValue(string, string, string) string        { return "" }

// DeleteAppSecrets drops every key stored under one scope, the same teardown
// the real manager performs when a provider record goes away.
func (f *fakeSecrets) DeleteAppSecrets(app string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := app + "/"
	for k := range f.values {
		if strings.HasPrefix(k, prefix) {
			delete(f.values, k)
		}
	}
	return nil
}

func (f *fakeSecrets) writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sets))
	copy(out, f.sets)
	return out
}

// fakeJellyfin is a stand-in for the Jellyfin API surface this app calls: the
// login, the key table, and key creation. It counts creates so a test can
// prove the lookup-before-create rule actually holds.
type fakeJellyfin struct {
	server      *httptest.Server
	mu          sync.Mutex
	keys        map[string]string
	creates     int
	logins      int
	usernames   []string
	requireAuth bool
}

func newFakeJellyfin(t *testing.T) *fakeJellyfin {
	t.Helper()
	f := &fakeJellyfin{keys: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/Users/AuthenticateByName", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Username string `json:"Username"`
		}
		// Best-effort: a body this test cannot parse still counts as a login, and
		// the assertion that cares about the username supplies a well-formed one.
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.logins++
		f.usernames = append(f.usernames, body.Username)
		f.mu.Unlock()
		if !strings.Contains(r.Header.Get("Authorization"), `Client="jellyfin-mcp"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"AccessToken": "session-token-1"})
	})
	mux.HandleFunc("/auth/keys", func(w http.ResponseWriter, r *http.Request) {
		if f.requireAuth && !strings.Contains(r.Header.Get("Authorization"), `Token="`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			name := r.URL.Query().Get("app")
			if name == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.creates++
			f.keys[name] = fmt.Sprintf("key-%s-%d", name, f.creates)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		f.mu.Lock()
		items := make([]map[string]string, 0, len(f.keys))
		for name, token := range f.keys {
			items = append(items, map[string]string{"AppName": name, "AccessToken": token})
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": items})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeJellyfin) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates
}

func (f *fakeJellyfin) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

// loginUsernames returns the `Username` of every login this fake received, in
// order. It is what lets a test assert *which* account the configurator tried,
// not merely that it tried once.
func (f *fakeJellyfin) loginUsernames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.usernames...)
}

func (f *fakeJellyfin) seed(name, token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[name] = token
}

// mediaServerBindingFor builds the binding the orchestrator would hand this
// app once a *locally installed* Jellyfin has converged, pointed at a fake
// server for the host-side calls and at the container address for what gets
// written into the config. The username is the managed bootstrap account a
// Bloud-booted provider publishes; remoteBindingFor is its off-host twin.
func mediaServerBindingFor(localURL string) configurator.MediaServerBinding {
	return configurator.MediaServerBinding{
		ProviderRef: configurator.ProviderRef{
			App:       "jellyfin",
			Installed: true,
			Node:      "apps-jellyfin",
			Port:      8096,
			BaseURL:   "http://apps-jellyfin:8096",
			LocalURL:  localURL,
		},
		AdminUsername: "bloud-bootstrap-admin",
		AdminPassword: "bootstrap-admin-password",
	}
}

// remoteBindingFor is the same binding for a Jellyfin the operator registered
// as an external app: the operator's own origin, the operator's own admin
// account, and no container node or port behind either.
func remoteBindingFor(localURL string) configurator.MediaServerBinding {
	b := mediaServerBindingFor(localURL)
	b.Kind = configurator.ProviderKindExternalApp
	b.Node = ""
	b.Port = 0
	b.BaseURL = localURL
	b.AdminUsername = "daniel"
	b.AdminPassword = "the-remote-password"
	return b
}

func stateWith(binding configurator.MediaServerBinding, dataDir string) *configurator.AppState {
	return &configurator.AppState{
		DataPath: dataDir,
		Integrations: configurator.Integrations{
			MediaServers: []configurator.MediaServerBinding{binding},
		},
	}
}

// --- env file ---

func TestRenderEnvFileCarriesTheResolvedBindings(t *testing.T) {
	content := renderEnvFile(mediaServerBindingFor("http://ignored"), true, "jf-key-1", "hmac-secret")

	assert.Contains(t, content, "JELLYFIN_URL='http://apps-jellyfin:8096'")
	assert.Contains(t, content, "JELLYFIN_API_KEY='jf-key-1'")
	assert.Contains(t, content, "FASTMCP_SERVER_AUTH_JWT_PUBLIC_KEY='hmac-secret'")
}

// TestRenderEnvFileOmitsWhatHasNotResolved pins the difference between
// "absent" and "empty". Writing JELLYFIN_API_KEY=” would tell the server it
// was configured with a blank credential; omitting the key leaves it unset,
// which the server reads as not configured. Only the second one is true, and
// only the second one lets a later pass restart the container once the
// binding lands.
func TestRenderEnvFileOmitsWhatHasNotResolved(t *testing.T) {
	content := renderEnvFile(configurator.MediaServerBinding{}, false, "", "")

	assert.NotContains(t, content, "JELLYFIN_URL")
	assert.NotContains(t, content, "JELLYFIN_API_KEY")
	assert.NotContains(t, content, "FASTMCP_SERVER_AUTH_JWT_PUBLIC_KEY")
	assert.NotContains(t, content, "=''")
}

// TestRenderEnvFileDoesNotEchoARejectedValue: the guard fires on a value that
// cannot be quoted safely. The comment it writes must name the key and never
// the value, because two of these keys are credentials and this file sits on
// disk.
func TestRenderEnvFileDoesNotEchoARejectedValue(t *testing.T) {
	content := renderEnvFile(configurator.MediaServerBinding{}, false, "has'quote", "ok-secret")

	assert.Contains(t, content, "# JELLYFIN_API_KEY omitted")
	assert.NotContains(t, content, "has'quote")
	assert.Contains(t, content, "FASTMCP_SERVER_AUTH_JWT_PUBLIC_KEY='ok-secret'")
}

// TestRenderEnvFileIsByteStable is what makes the steady-state resync a
// read-only diff: managedfile.Write compares bytes to decide whether to report
// a change, so a renderer that reordered or re-touched anything would drive a
// container recreate on every pass.
func TestRenderEnvFileIsByteStable(t *testing.T) {
	first := renderEnvFile(mediaServerBindingFor("x"), true, "jf-key-1", "hmac-secret")
	second := renderEnvFile(mediaServerBindingFor("x"), true, "jf-key-1", "hmac-secret")
	assert.Equal(t, first, second)
}

// TestEnvFileKeysAreTheOnesUpstreamReads guards the one thing this
// integration cannot type-check: the string names the image looks for in the
// process environment. A typo here is a container that starts and serves
// nothing.
func TestEnvFileKeysAreTheOnesUpstreamReads(t *testing.T) {
	assert.Equal(t, "JELLYFIN_URL", envJellyfinURL)
	assert.Equal(t, "JELLYFIN_API_KEY", envJellyfinAPIKey)
	assert.Equal(t, "FASTMCP_SERVER_AUTH_JWT_PUBLIC_KEY", envVerificationSecret)
	assert.Equal(t, "env", envFileName)
	assert.Equal(t, "config", configDir)
}

func TestRenderEnvFileEveryLineIsAnAssignment(t *testing.T) {
	content := renderEnvFile(mediaServerBindingFor("x"), true, "jf-key-1", "hmac-secret")
	for i, line := range strings.Split(strings.TrimSpace(content), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		assert.Contains(t, line, "=", "line %d is not a KEY=value assignment: %q", i+1, line)
	}
}

// --- PreStart ---

func TestPreStartWritesTheEnvFileAndProvisionsEveryCredential(t *testing.T) {
	jf := newFakeJellyfin(t)
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, HTTP: configurator.ClientFactory{}})
	dir := t.TempDir()

	result, err := c.PreStart(t.Context(), stateWith(mediaServerBindingFor(jf.server.URL), dir))
	require.NoError(t, err)
	require.True(t, result.RestartNeeded, "a first pass that wrote the config must ask for a recreate")

	raw, err := os.ReadFile(filepath.Join(dir, configDir, envFileName))
	require.NoError(t, err)
	content := string(raw)
	assert.Contains(t, content, "JELLYFIN_URL='http://apps-jellyfin:8096'")
	assert.Contains(t, content, "JELLYFIN_API_KEY='key-jellyfin-mcp-1'")
	assert.Contains(t, content, "FASTMCP_SERVER_AUTH_JWT_PUBLIC_KEY='")

	// The published bearer and the two private credentials all landed in the
	// store, under this app's name.
	assert.Contains(t, secrets.Get(appName, httpTokenKey), ".")
	assert.NotEmpty(t, secrets.Get(appName, verificationSecretKey))
	assert.Equal(t, "key-jellyfin-mcp-1", secrets.Get(appName, jellyfinAPIKeyKey))
}

// TestPreStartIsIdempotentOnASteadyState: the second pass rewrites nothing
// and asks for no recreate. PreStart runs on every resync, so a configurator
// that reported a change every time would recreate its container forever.
func TestPreStartIsIdempotentOnASteadyState(t *testing.T) {
	jf := newFakeJellyfin(t)
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, HTTP: configurator.ClientFactory{}})
	dir := t.TempDir()
	state := stateWith(mediaServerBindingFor(jf.server.URL), dir)

	_, err := c.PreStart(t.Context(), state)
	require.NoError(t, err)
	writesAfterFirst := len(secrets.writes())
	loginsAfterFirst := jf.loginCount()

	second, err := c.PreStart(t.Context(), state)
	require.NoError(t, err)
	assert.False(t, second.RestartNeeded, "a steady-state pass must not ask for a recreate")
	assert.Len(t, secrets.writes(), writesAfterFirst, "a steady-state pass must persist nothing new")
	assert.Equal(t, 1, jf.createCount(), "a steady-state pass must not mint a second Jellyfin key")
	assert.Equal(t, loginsAfterFirst, jf.loginCount(), "a steady-state pass must not log into Jellyfin again")
}

// TestPreStartAdoptsAnExistingJellyfinKey: the create endpoint appends a new
// key every call, so a configurator that minted without looking would stack
// identical keys in Jellyfin's Security screen on every Bloud database reset.
// The lookup has to come first, and the adopted key is the one that gets
// published and written.
func TestPreStartAdoptsAnExistingJellyfinKey(t *testing.T) {
	jf := newFakeJellyfin(t)
	jf.seed(jellyfinKeyName, "key-from-a-previous-install")
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, HTTP: configurator.ClientFactory{}})
	dir := t.TempDir()

	_, err := c.PreStart(t.Context(), stateWith(mediaServerBindingFor(jf.server.URL), dir))
	require.NoError(t, err)

	assert.Equal(t, 0, jf.createCount(), "an existing key must be adopted, not duplicated")
	assert.Equal(t, "key-from-a-previous-install", secrets.Get(appName, jellyfinAPIKeyKey))
}

// TestPreStartLogsInAsTheAccountTheProviderPublished is the remote case, and
// the reason this code reads the username off the binding at all. A Jellyfin
// the operator registered from off-host has an account Bloud did not name, so
// a configurator that logged in as the local bootstrap account could never
// authenticate against it: the mint 401s, PreStart fails, and the node never
// converges. The address written into the container is the operator's origin
// too, not a container name nothing would resolve.
func TestPreStartLogsInAsTheAccountTheProviderPublished(t *testing.T) {
	jf := newFakeJellyfin(t)
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, HTTP: configurator.ClientFactory{}})
	dir := t.TempDir()

	_, err := c.PreStart(t.Context(), stateWith(remoteBindingFor(jf.server.URL), dir))
	require.NoError(t, err)

	assert.Equal(t, []string{"daniel"}, jf.loginUsernames(),
		"the login must use the account the provider published, not a name this app assumed")

	raw, err := os.ReadFile(filepath.Join(dir, configDir, envFileName))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "JELLYFIN_URL='"+jf.server.URL+"'")
	assert.Contains(t, string(raw), "JELLYFIN_API_KEY='key-jellyfin-mcp-1'")
}

// TestPreStartLogsInAsTheBootstrapAccountForALocalProvider: the same read
// answers the local case, because a Bloud-booted Jellyfin publishes its own
// bootstrap account name. This pins that the generalization did not quietly
// break the install path that already worked.
func TestPreStartLogsInAsTheBootstrapAccountForALocalProvider(t *testing.T) {
	jf := newFakeJellyfin(t)
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets(), HTTP: configurator.ClientFactory{}})

	_, err := c.PreStart(t.Context(), stateWith(mediaServerBindingFor(jf.server.URL), t.TempDir()))
	require.NoError(t, err)
	assert.Equal(t, []string{"bloud-bootstrap-admin"}, jf.loginUsernames())
}

// TestPreStartFailsWhenTheProviderPublishesNoAdminUsername: a binding with a
// password and no username cannot be logged into, and guessing one would be
// worse than failing loudly. Nothing is persisted, so the next pass retries
// clean rather than caching a credential minted against a guess.
func TestPreStartFailsWhenTheProviderPublishesNoAdminUsername(t *testing.T) {
	jf := newFakeJellyfin(t)
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, HTTP: configurator.ClientFactory{}})

	binding := mediaServerBindingFor(jf.server.URL)
	binding.AdminUsername = ""
	_, err := c.PreStart(t.Context(), stateWith(binding, t.TempDir()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin username")
	assert.Equal(t, 0, jf.loginCount(), "a binding with no username must not be guessed at")
	assert.Empty(t, secrets.Get(appName, jellyfinAPIKeyKey))
}

// TestPreStartWithoutABindingStaysOffline: with no media server resolved the
// pass writes a config that carries the inbound credentials and no Jellyfin
// address, and it must not touch the network at all. This is the state the
// conformance harness runs the app in.
func TestPreStartWithoutABindingStaysOffline(t *testing.T) {
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, HTTP: configurator.ClientFactory{}})
	dir := t.TempDir()

	_, err := c.PreStart(t.Context(), &configurator.AppState{DataPath: dir})
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(dir, configDir, envFileName))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "JELLYFIN_URL")
	assert.NotContains(t, string(raw), "JELLYFIN_API_KEY")
	assert.Contains(t, string(raw), "FASTMCP_SERVER_AUTH_JWT_PUBLIC_KEY='")
}

// TestPreStartFailsWhenTheKeyCannotBeProvisioned: a wrapper that cannot get a
// credential into its target has nothing to serve, so the pass fails rather
// than promoting a server whose every tool call is about to fail. The self-
// healing pass retries.
func TestPreStartFailsWhenTheKeyCannotBeProvisioned(t *testing.T) {
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, HTTP: configurator.ClientFactory{}})
	dir := t.TempDir()

	// A LocalURL with nothing behind it: the login call cannot connect.
	binding := mediaServerBindingFor("http://127.0.0.1:1")
	_, err := c.PreStart(t.Context(), stateWith(binding, dir))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jellyfin")
	assert.Empty(t, secrets.Get(appName, jellyfinAPIKeyKey))
}

// TestPreStartDoesNotRotateTheBearerAcrossPasses: a harness caches the bearer
// it registered with, so regenerating it would invalidate every namespace
// already wired.
func TestPreStartDoesNotRotateTheBearerAcrossPasses(t *testing.T) {
	jf := newFakeJellyfin(t)
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, HTTP: configurator.ClientFactory{}})
	dir := t.TempDir()
	state := stateWith(mediaServerBindingFor(jf.server.URL), dir)

	_, err := c.PreStart(t.Context(), state)
	require.NoError(t, err)
	first := secrets.Get(appName, httpTokenKey)

	_, err = c.PreStart(t.Context(), state)
	require.NoError(t, err)
	assert.Equal(t, first, secrets.Get(appName, httpTokenKey))
}

// --- PostStart ---

// fakeMCPServer answers the MCP handshake only for the bearer it was given,
// which is what makes the probe worth running: an unauthenticated probe would
// report a healthy node even if the listener had come up with no
// verification at all.
func fakeMCPServer(t *testing.T, bearer string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if bearer != "" && r.Header.Get("Authorization") != "Bearer "+bearer {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"serverInfo\":{\"name\":\"jellyfin-mcp MCP\",\"version\":\"1\"}}}\n\n"))
	}))
}

func TestPostStartServesMCPWithThePublishedBearer(t *testing.T) {
	secrets := newFakeSecrets()
	jf := newFakeJellyfin(t)
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, HTTP: configurator.ClientFactory{}})
	dir := t.TempDir()
	_, err := c.PreStart(t.Context(), stateWith(mediaServerBindingFor(jf.server.URL), dir))
	require.NoError(t, err)

	srv := fakeMCPServer(t, secrets.Get(appName, httpTokenKey))
	defer srv.Close()
	c.baseURL = srv.URL

	require.NoError(t, c.PostStart(t.Context(), stateWith(mediaServerBindingFor(jf.server.URL), dir)))
}

// TestPostStartFailsWithoutAMediaServer: the node must not reach RUNNING as a
// tool surface with nothing behind it.
func TestPostStartFailsWithoutAMediaServer(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets(), HTTP: configurator.ClientFactory{}})
	err := c.PostStart(t.Context(), &configurator.AppState{DataPath: t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no media server is bound")
}

// TestPostStartFailsWhenTheBearerIsRefused: a listener that rejects the
// credential Bloud published is not a serving node, even though its process
// and its /health are fine.
func TestPostStartFailsWhenTheBearerIsRefused(t *testing.T) {
	srv := fakeMCPServer(t, "some-other-bearer")
	defer srv.Close()
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets(), HTTP: configurator.ClientFactory{}})
	c.baseURL = srv.URL

	ctx, cancel := context.WithTimeout(t.Context(), probeTestBudget)
	defer cancel()
	require.Error(t, c.PostStart(ctx, stateWith(mediaServerBindingFor("http://x"), t.TempDir())))
}

// --- token minting ---

// probeTestBudget is shorter than the production wait so the refusal test
// fails fast; the wait itself is under test elsewhere.
const probeTestBudget = 3 * time.Second

func decodeJWTSegment(t *testing.T, seg string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func TestMintTokenIsAVerifiableHS256JWT(t *testing.T) {
	token := mustMint(t, "secret-1", "bloud", "jellyfin-mcp", "bloud:jellyfin-mcp")
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3, "a JWT is three dot-separated segments")

	header := decodeJWTSegment(t, parts[0])
	assert.Equal(t, "HS256", header["alg"])

	claims := decodeJWTSegment(t, parts[1])
	assert.Equal(t, "bloud", claims["iss"])
	assert.Equal(t, "jellyfin-mcp", claims["aud"])
	assert.Equal(t, "bloud:jellyfin-mcp", claims["sub"])

	// The signature must check with the right key and fail with any other,
	// because that is the whole gate on the inbound listener.
	mac := hmac.New(sha256.New, []byte("secret-1"))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	assert.True(t, hmac.Equal(mac.Sum(nil), mustBase64Decode(t, parts[2])))

	other := hmac.New(sha256.New, []byte("secret-2"))
	_, _ = other.Write([]byte(parts[0] + "." + parts[1]))
	assert.False(t, hmac.Equal(other.Sum(nil), mustBase64Decode(t, parts[2])))
}

// TestMintTokenCarriesNoExpiry pins the deliberate choice: a bearer that
// expires would break every registered namespace on a date nobody chose.
// Rotation is the control here, not expiry.
func TestMintTokenCarriesNoExpiry(t *testing.T) {
	token := mustMint(t, "secret-1", "bloud", "jellyfin-mcp", "sub")
	claims := decodeJWTSegment(t, strings.Split(token, ".")[1])
	assert.NotContains(t, claims, "exp")
	assert.NotContains(t, claims, "nbf")
}

func mustMint(t *testing.T, secret, iss, aud, sub string) string {
	t.Helper()
	tok, err := mintToken(secret, iss, aud, sub)
	require.NoError(t, err)
	return tok
}

func mustBase64Decode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	require.NoError(t, err)
	return b
}

// TestIssuerAndAudienceMatchTheContainerSpec: the verifier requires the pair
// set in metadata.yaml, so a token minted with anything else is refused even
// when it is signed with the right key. The two sides live in different files
// and nothing but this test keeps them together.
func TestIssuerAndAudienceMatchTheContainerSpec(t *testing.T) {
	assert.Equal(t, "bloud", jwtIssuer)
	assert.Equal(t, "jellyfin-mcp", jwtAudience)
	assert.Equal(t, jwtIssuer, configuredEnvValue(t, "FASTMCP_SERVER_AUTH_JWT_ISSUER"))
	assert.Equal(t, jwtAudience, configuredEnvValue(t, "FASTMCP_SERVER_AUTH_JWT_AUDIENCE"))
}

// configuredEnvValue reads a static `environment:` entry out of this app's
// metadata.yaml. Deliberately a line scan rather than a catalog load: the
// catalog package is the host-agent's, and this only has to answer "what does
// the container get told?".
func configuredEnvValue(t *testing.T, key string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", appName, "metadata.yaml"))
	require.NoError(t, err)
	prefix := key + ":"
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
	}
	t.Fatalf("%s is not set in %s/metadata.yaml", key, appName)
	return ""
}
