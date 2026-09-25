// SPDX-License-Identifier: AGPL-3.0-only

package vaultwarden

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func staticBaseURL(u string) func() string {
	return func() string { return u }
}

func testOIDC() *configurator.OIDCOutput {
	return &configurator.OIDCOutput{
		ClientID:     "vaultwarden-client",
		ClientSecret: "secret-value",
		IssuerURL:    "http://sso.localhost:8080/application/o/vaultwarden/",
		RedirectURI:  "http://vaultwarden.localhost:8080" + callbackPath,
	}
}

// callbackPath is the redirect URI path Vaultwarden derives from its DOMAIN. It
// is asserted against metadata.yaml below, which is what the host-agent
// registers with the identity provider.
const callbackPath = "/identity/connect/oidc-signin"

// fakeApp stands in for Vaultwarden's HTTP surface for the endpoints PostStart
// probes, and records which of them were called.
type fakeApp struct {
	mu sync.Mutex

	ssoEnabled      bool // /identity/sso/prevalidate: 200 with a token vs 400
	authorizeStatus int  // /identity/connect/authorize
	aliveStatus     int

	calls []string
	query map[string][]string // the last authorize request's query, for assertions
}

func newFakeApp() *fakeApp {
	return &fakeApp{ssoEnabled: true, authorizeStatus: http.StatusFound, aliveStatus: http.StatusOK}
}

func (f *fakeApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.URL.Path)

	switch r.URL.Path {
	case alivePath:
		w.WriteHeader(f.aliveStatus)
	case prevalidatePath:
		if !f.ssoEnabled {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "SSO sign-in is not available"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"token": "sso-token"})
	case authorizePath:
		f.query = r.URL.Query()
		if f.authorizeStatus >= 300 && f.authorizeStatus < 400 {
			w.Header().Set("Location", "http://sso.localhost:8080/application/o/authorize/")
		}
		w.WriteHeader(f.authorizeStatus)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeApp) called(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == path {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// testConfigurator builds a configurator whose API clients point at a fake app
// server, with the wait backoff collapsed so probes resolve immediately. It
// returns the configurator and the app data dir the caller passes in AppState.
func testConfigurator(t *testing.T, app http.Handler) (*Configurator, string) {
	t.Helper()
	c := NewConfigurator(0, configurator.Deps{
		PrimaryBaseURL: staticBaseURL("http://localhost:8080"),
		Logger:         quietLogger(),
	})
	if app != nil {
		server := httptest.NewServer(app)
		t.Cleanup(server.Close)
		c.baseURL = server.URL
	}
	c.api.cl.WithSleeper(func(time.Duration) {})
	c.api.noRedirect.WithSleeper(func(time.Duration) {})
	return c, t.TempDir()
}

func appState(dataPath string, oidc *configurator.OIDCOutput) *configurator.AppState {
	return &configurator.AppState{DataPath: dataPath, SSOEnabled: oidc != nil, OIDC: oidc}
}

func readEnvFile(t *testing.T, dataPath string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dataPath, "config", envFileName))
	require.NoError(t, err)
	return string(b)
}

// ---- rendering ----

func TestRenderEnv_WithoutSSOOnlySetsDomain(t *testing.T) {
	got, err := renderEnv("http://vaultwarden.localhost:8080", nil, false)
	require.NoError(t, err)

	assert.Contains(t, got, "DOMAIN='http://vaultwarden.localhost:8080'\n")
	assert.NotContains(t, got, "SSO_")
	// Without SSO there is no other way to get an account or sign in, so the
	// app's own defaults (signups open, master-password login on) must be left
	// alone; SSO_ONLY without a working provider would lock everyone out.
	assert.NotContains(t, got, "SIGNUPS_ALLOWED")
}

func TestRenderEnv_WithSSOWiresTheProvider(t *testing.T) {
	got, err := renderEnv("http://vaultwarden.localhost:8080", testOIDC(), false)
	require.NoError(t, err)

	for _, want := range []string{
		"DOMAIN='http://vaultwarden.localhost:8080'\n",
		"SIGNUPS_ALLOWED='false'\n",
		"SSO_ENABLED='true'\n",
		// The identity provider is the only way in: master-password login is off.
		"SSO_ONLY='true'\n",
		// The trailing slash matters: Vaultwarden compares the authority to the
		// issuer claim exactly.
		"SSO_AUTHORITY='http://sso.localhost:8080/application/o/vaultwarden/'\n",
		"SSO_CLIENT_ID='vaultwarden-client'\n",
		"SSO_CLIENT_SECRET='secret-value'\n",
		"SSO_SCOPES='" + ssoScopes + "'\n",
		"SSO_PKCE='true'\n",
	} {
		assert.Contains(t, got, want)
	}
	// Local registration is closed, but SSO signup must stay open: it is a
	// separate switch, and disabling it would lock every identity provider user
	// out of first sign-in.
	assert.NotContains(t, got, "SSO_SIGNUPS_ALLOWED")
}

func TestRenderEnv_RejectsSingleQuote(t *testing.T) {
	oidc := testOIDC()
	oidc.ClientSecret = "bad'secret"

	_, err := renderEnv("http://vaultwarden.localhost:8080", oidc, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "SSO_CLIENT_SECRET")
	assert.NotContains(t, err.Error(), "bad'secret", "the error must not echo the secret")
}

// The image's own healthcheck script sources the file as shell, and Vaultwarden
// loads it as dotenv. Single quotes must deliver every value verbatim to both,
// including a secret full of shell metacharacters.
func TestRenderEnv_SurvivesBeingSourcedAsShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	oidc := testOIDC()
	oidc.ClientSecret = "a$b`c\"d\\e;f&g|h (i) $(touch pwned) #j"

	content, err := renderEnv("http://vaultwarden.localhost:8080", oidc, false)
	require.NoError(t, err)
	dir := t.TempDir()
	path := filepath.Join(dir, envFileName)
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))

	cmd := exec.Command("sh", "-c", `. "$1"; printf '%s|%s|%s' "$DOMAIN" "$SSO_CLIENT_SECRET" "$SSO_AUTHORITY"`, "sh", path)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	assert.Equal(t, "http://vaultwarden.localhost:8080|"+oidc.ClientSecret+"|"+oidc.IssuerURL, string(out))
	_, statErr := os.Stat(filepath.Join(dir, "pwned"))
	assert.True(t, os.IsNotExist(statErr), "sourcing the file must not execute anything in a value")
}

// ---- PreStart ----

func TestPreStart_WritesEnvFileWithRestrictedMode(t *testing.T) {
	c, dir := testConfigurator(t, nil)

	changed, err := c.PreStart(context.Background(), appState(dir, testOIDC()))

	require.NoError(t, err)
	assert.True(t, changed.RestartNeeded, "a new file must restart the container")
	body := readEnvFile(t, dir)
	assert.Contains(t, body, "DOMAIN='http://vaultwarden.localhost:8080'")
	assert.Contains(t, body, "SSO_CLIENT_SECRET='secret-value'")

	info, err := os.Stat(filepath.Join(dir, "config", envFileName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(), "the file carries the OIDC client secret")
}

func TestPreStart_UnchangedConfigDoesNotRestart(t *testing.T) {
	c, dir := testConfigurator(t, nil)
	state := appState(dir, testOIDC())

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	changed, err := c.PreStart(context.Background(), state)

	require.NoError(t, err)
	assert.False(t, changed.RestartNeeded, "an unchanged config must not churn the container")
}

func TestPreStart_FollowsSSOAndHostChanges(t *testing.T) {
	base := "http://localhost:8080"
	c := NewConfigurator(0, configurator.Deps{PrimaryBaseURL: func() string { return base }, Logger: quietLogger()})
	dir := t.TempDir()
	ctx := context.Background()

	_, err := c.PreStart(ctx, appState(dir, nil))
	require.NoError(t, err)
	assert.NotContains(t, readEnvFile(t, dir), "SSO_ENABLED")

	// SSO gets wired later (Authentik installed after Vaultwarden): the file
	// changes and the container restarts.
	changed, err := c.PreStart(ctx, appState(dir, testOIDC()))
	require.NoError(t, err)
	assert.True(t, changed.RestartNeeded)
	assert.Contains(t, readEnvFile(t, dir), "SSO_ENABLED='true'")

	// The primary host changes in the UI: DOMAIN follows it without a restart
	// of the host-agent, because the base URL is read on every PreStart.
	base = "http://bloud.local"
	changed, err = c.PreStart(ctx, appState(dir, testOIDC()))
	require.NoError(t, err)
	assert.True(t, changed.RestartNeeded)
	assert.Contains(t, readEnvFile(t, dir), "DOMAIN='http://vaultwarden.bloud.local'")
}

func TestPreStart_FallsBackToDevURLWithoutBaseURL(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	dir := t.TempDir()

	_, err := c.PreStart(context.Background(), appState(dir, nil))

	require.NoError(t, err)
	assert.Contains(t, readEnvFile(t, dir), "DOMAIN='http://vaultwarden.localhost:8080'")
}

// ---- PostStart ----

func TestPostStart_VerifiesSSOEndToEnd(t *testing.T) {
	app := newFakeApp()
	c, dir := testConfigurator(t, app)

	require.NoError(t, c.PostStart(context.Background(), appState(dir, testOIDC())))

	assert.True(t, app.called(alivePath))
	assert.True(t, app.called(prevalidatePath))
	assert.True(t, app.called(authorizePath))
	// The probe presents what the web vault presents: the token prevalidate
	// issued, a PKCE challenge, and a redirect back to the app's own public URL.
	q := app.query
	assert.Equal(t, "web", q["client_id"][0])
	assert.Equal(t, "sso-token", q["ssoToken"][0])
	assert.Equal(t, "S256", q["code_challenge_method"][0])
	assert.Equal(t, "http://vaultwarden.localhost:8080/sso-connector.html", q["redirect_uri"][0])
	assert.Contains(t, q["scope"][0], "offline_access")
}

func TestPostStart_WithoutSSOOnlyWaitsForTheServer(t *testing.T) {
	app := newFakeApp()
	c, dir := testConfigurator(t, app)

	require.NoError(t, c.PostStart(context.Background(), appState(dir, nil)))

	assert.True(t, app.called(alivePath))
	assert.False(t, app.called(prevalidatePath), "no SSO wiring means nothing to verify")
	assert.False(t, app.called(authorizePath))
}

func TestPostStart_FailsWhenSSOIsOffInTheRunningApp(t *testing.T) {
	// The env file was not applied (stale container, unreadable file): the app
	// reports SSO unavailable, and the node must fail rather than report a
	// login that cannot work.
	app := newFakeApp()
	app.ssoEnabled = false
	c, dir := testConfigurator(t, app)

	err := c.PostStart(context.Background(), appState(dir, testOIDC()))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "SSO is enabled in the running app")
	assert.False(t, app.called(authorizePath), "no point probing the issuer when SSO is off")
}

func TestPostStart_FailsWhenTheIssuerIsUnreachable(t *testing.T) {
	// The app answers 400 when discovery against the issuer fails (wrong
	// authority, sso.localhost not resolving inside the container).
	app := newFakeApp()
	app.authorizeStatus = http.StatusBadRequest
	c, dir := testConfigurator(t, app)

	err := c.PostStart(context.Background(), appState(dir, testOIDC()))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "OIDC authorization redirect")
}

func TestPostStart_ToleratesSlowBoot(t *testing.T) {
	// The first boot creates the database and keys before it listens.
	app := newFakeApp()
	var alive int
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == alivePath {
			alive++
			if alive < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		app.ServeHTTP(w, r)
	})
	c, dir := testConfigurator(t, wrapped)

	require.NoError(t, c.PostStart(context.Background(), appState(dir, testOIDC())))

	assert.GreaterOrEqual(t, alive, 3)
}

// ---- the manifest and the code must agree ----

// metadata mirrors the fields of metadata.yaml the configurator's constants
// must agree with.
type metadata struct {
	Port int `yaml:"port"`
	SSO  struct {
		Strategy           string   `yaml:"strategy"`
		CallbackPath       string   `yaml:"callbackPath"`
		Scopes             []string `yaml:"scopes"`
		AccessTokenMinutes int      `yaml:"accessTokenMinutes"`
	} `yaml:"sso"`
	Containers []struct {
		Name        string            `yaml:"name"`
		Command     []string          `yaml:"command"`
		Environment map[string]string `yaml:"environment"`
		Ports       []struct {
			Host      int `yaml:"host"`
			Container int `yaml:"container"`
		} `yaml:"ports"`
		Volumes []struct {
			Source      string `yaml:"source"`
			Destination string `yaml:"destination"`
		} `yaml:"volumes"`
	} `yaml:"containers"`
}

func loadMetadata(t *testing.T) metadata {
	t.Helper()
	raw, err := os.ReadFile("metadata.yaml")
	require.NoError(t, err)
	var m metadata
	require.NoError(t, yaml.Unmarshal(raw, &m))
	return m
}

func TestMetadata_AgreesWithTheConfigurator(t *testing.T) {
	m := loadMetadata(t)

	assert.Equal(t, "native-oidc", m.SSO.Strategy)
	assert.Equal(t, callbackPath, m.SSO.CallbackPath,
		"the redirect URI the host-agent registers must be the one Vaultwarden derives from DOMAIN")

	// Every scope the manifest asks the host-agent to attach to the provider
	// must be one the app actually requests, and vice versa for the extras.
	requested := strings.Fields(ssoScopes)
	assert.NotContains(t, requested, "openid", "Vaultwarden adds openid itself; listing it sends it twice")
	require.NotEmpty(t, m.SSO.Scopes)
	for _, scope := range m.SSO.Scopes {
		assert.Contains(t, requested, scope, "sso.scopes lists a scope the app never requests")
	}
	for _, scope := range requested {
		if scope == "openid" || scope == "profile" || scope == "email" {
			continue // carried by every native-oidc provider
		}
		assert.Contains(t, m.SSO.Scopes, scope, "the app requests a scope the provider would not carry")
	}
	assert.Greater(t, m.SSO.AccessTokenMinutes, 5, "the wiki requires a lifetime beyond the 5 minute default")
}

func TestMetadata_ContainerMatchesTheGeneratedFileLocation(t *testing.T) {
	m := loadMetadata(t)
	require.Len(t, m.Containers, 1)
	ctr := m.Containers[0]

	assert.Equal(t, "apps-vaultwarden", ctr.Name, "must equal the registered node name")
	assert.Equal(t, "/config/"+envFileName, ctr.Environment["ENV_FILE"])

	var mountsConfig bool
	for _, v := range ctr.Volumes {
		if v.Destination == "/config" && strings.HasSuffix(v.Source, "/config") {
			mountsConfig = true // PreStart writes <DataPath>/config
		}
	}
	assert.True(t, mountsConfig, "the container must mount the directory PreStart writes")

	require.Len(t, ctr.Ports, 1)
	assert.Equal(t, m.Port, ctr.Ports[0].Host, "the routed port must be the published host port")
}

// ---- the plain-HTTP dev switch ----

// switchedConfigurator builds a configurator whose host-agent environment and
// primary base URL are fixed by the test.
func switchedConfigurator(t *testing.T, switchValue, baseURL string) *Configurator {
	t.Helper()
	c := NewConfigurator(0, configurator.Deps{
		PrimaryBaseURL: staticBaseURL(baseURL),
		Logger:         quietLogger(),
	})
	c.getenv = func(key string) string {
		if key == devSwitchEnv {
			return switchValue
		}
		return ""
	}
	return c
}

func TestRenderEnv_DevSwitchMarker(t *testing.T) {
	on, err := renderEnv("http://vaultwarden.localhost:8080", testOIDC(), true)
	require.NoError(t, err)
	assert.Contains(t, on, devSwitchKey+"='true'\n")

	off, err := renderEnv("http://vaultwarden.localhost:8080", testOIDC(), false)
	require.NoError(t, err)
	assert.NotContains(t, off, devSwitchKey)
}

func TestPreStart_DevSwitchIsOptIn(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        bool
	}{
		{"unset is off", "", false},
		{"explicit off", "0", false},
		{"garbage is off", "maybe", false},
		{"1 is on", "1", true},
		{"true is on", "true", true},
		{"case and space are tolerated", " TRUE ", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := switchedConfigurator(t, tc.value, "http://localhost:8080")
			dir := t.TempDir()

			_, err := c.PreStart(context.Background(), appState(dir, testOIDC()))

			require.NoError(t, err)
			assert.Equal(t, tc.want, strings.Contains(readEnvFile(t, dir), devSwitchKey+"='true'"))
		})
	}
}

func TestPreStart_DevSwitchIsRefusedOffLocalhost(t *testing.T) {
	// A real LAN or domain install must never be switched into plain-HTTP mode by
	// an environment variable, whatever its value.
	for _, base := range []string{
		"http://bloud.local",
		"http://192.168.1.29:8080",
		"https://home.example.com",
		"http://evil-localhost:8080",
		"http://localhost.example.com:8080",
	} {
		t.Run(base, func(t *testing.T) {
			c := switchedConfigurator(t, "1", base)
			dir := t.TempDir()

			_, err := c.PreStart(context.Background(), appState(dir, testOIDC()))

			require.NoError(t, err)
			assert.NotContains(t, readEnvFile(t, dir), devSwitchKey)
		})
	}
}

func TestPreStart_TogglingTheSwitchRestartsTheContainer(t *testing.T) {
	// The wrapper runs at container start, so a change must recreate the
	// container: that is what makes turning the switch off restore the pristine
	// bundle (a recreated container starts from the unmodified image).
	dir := t.TempDir()
	on := switchedConfigurator(t, "1", "http://localhost:8080")
	off := switchedConfigurator(t, "", "http://localhost:8080")

	changed, err := off.PreStart(context.Background(), appState(dir, testOIDC()))
	require.NoError(t, err)
	assert.True(t, changed.RestartNeeded)
	changed, err = on.PreStart(context.Background(), appState(dir, testOIDC()))
	require.NoError(t, err)
	assert.True(t, changed.RestartNeeded, "turning the switch on must restart the container")
	changed, err = on.PreStart(context.Background(), appState(dir, testOIDC()))
	require.NoError(t, err)
	assert.False(t, changed.RestartNeeded, "an unchanged switch must not churn it")
	changed, err = off.PreStart(context.Background(), appState(dir, testOIDC()))
	require.NoError(t, err)
	assert.True(t, changed.RestartNeeded, "turning the switch off must restart the container")
}

// wrapperScript returns the shell script from the manifest's container command.
func wrapperScript(t *testing.T) string {
	t.Helper()
	m := loadMetadata(t)
	require.Len(t, m.Containers, 1)
	cmd := m.Containers[0].Command
	require.Len(t, cmd, 3, "the command must be: sh -c <script>")
	require.Equal(t, []string{"sh", "-c"}, cmd[:2])
	return cmd[2]
}

func TestMetadata_WrapperMatchesTheEnvFileMarker(t *testing.T) {
	assert.Contains(t, wrapperScript(t), devSwitchKey+"='true'",
		"the wrapper must look for exactly the line the configurator writes")
	assert.Contains(t, wrapperScript(t), "exec /start.sh", "the wrapper must hand over to the image's own start script")
}

// runWrapper executes the manifest's wrapper against a fake web vault and a stub
// /start.sh, with the container paths redirected into a temp dir. It returns the
// bundle's content afterwards, whether the stub start script ran, the exit code
// and stderr.
func runWrapper(t *testing.T, bundle string, envFile string) (after string, started bool, exit int, stderr string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	appDir := filepath.Join(root, "web-vault", "app")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	bundlePath := filepath.Join(appDir, "main.abc123.js")
	require.NoError(t, os.WriteFile(bundlePath, []byte(bundle), 0o644))
	envPath := filepath.Join(root, "vaultwarden.env")
	require.NoError(t, os.WriteFile(envPath, []byte(envFile), 0o644))
	marker := filepath.Join(root, "started")
	startStub := filepath.Join(root, "start.sh")
	require.NoError(t, os.WriteFile(startStub, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755))

	script := strings.ReplaceAll(wrapperScript(t), "/web-vault/", filepath.Join(root, "web-vault")+"/")
	script = strings.ReplaceAll(script, "/start.sh", startStub)

	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "ENV_FILE="+envPath)
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exit = 0
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	got, readErr := os.ReadFile(bundlePath)
	require.NoError(t, readErr)
	_, statErr := os.Stat(marker)
	return string(got), statErr == nil, exit, errBuf.String()
}

const (
	unpatchedBundle = "a();class P{isDev(){return!1}}b();"
	patchedBundle   = "a();class P{isDev(){return!0}}b();"
	switchOnEnv     = "DOMAIN='http://vaultwarden.localhost:8080'\n" + devSwitchKey + "='true'\n"
	switchOffEnv    = "DOMAIN='http://vaultwarden.localhost:8080'\n"
)

func TestWrapper_OffLeavesTheBundleAloneAndStarts(t *testing.T) {
	after, started, exit, _ := runWrapper(t, unpatchedBundle, switchOffEnv)

	assert.Equal(t, unpatchedBundle, after, "without the marker the client must stay untouched")
	assert.True(t, started)
	assert.Equal(t, 0, exit)
}

func TestWrapper_OnFlipsTheConstantAndStarts(t *testing.T) {
	after, started, exit, stderr := runWrapper(t, unpatchedBundle, switchOnEnv)

	assert.Equal(t, patchedBundle, after)
	assert.True(t, started)
	assert.Equal(t, 0, exit)
	assert.Contains(t, stderr, "WARNING", "an insecure start must say so")
}

func TestWrapper_OnIsSafeOnRestart(t *testing.T) {
	// A container restart re-runs the wrapper against the already patched bundle.
	after, started, exit, _ := runWrapper(t, patchedBundle, switchOnEnv)

	assert.Equal(t, patchedBundle, after)
	assert.True(t, started, "an already patched bundle must not stop a restart")
	assert.Equal(t, 0, exit)
}

func TestWrapper_OnRefusesToStartWhenTheConstantIsGone(t *testing.T) {
	// An image bump changed the bundle: the operator asked for the switch, so
	// silently running an unpatched (HTTPS-only) client would be a lie.
	after, started, exit, stderr := runWrapper(t, "a();class P{}b();", switchOnEnv)

	assert.Equal(t, "a();class P{}b();", after)
	assert.False(t, started, "must fail closed, not start unpatched")
	assert.NotEqual(t, 0, exit)
	assert.Contains(t, stderr, "refusing to start")
}
