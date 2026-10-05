// SPDX-License-Identifier: AGPL-3.0-only

package radicale

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- helpers ----

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// newTestConfigurator builds a configurator whose client resolves to the given
// handler instead of the real app port. A nil handler means no server at all,
// which is how the unreachable case is exercised.
func newTestConfigurator(t *testing.T, app http.Handler) (*Configurator, string) {
	t.Helper()
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	if app != nil {
		server := httptest.NewServer(app)
		t.Cleanup(server.Close)
		c.baseURL = server.URL
	}
	c.api.cl.WithSleeper(func(time.Duration) {})
	return c, t.TempDir()
}

func ldapOutput() *configurator.LDAPOutput {
	return &configurator.LDAPOutput{
		Host:         "apps-authentik-ldap",
		Port:         3389,
		BaseDN:       "dc=ldap,dc=goauthentik,dc=io",
		BindUser:     "cn=ldap-service,ou=users,dc=ldap,dc=goauthentik,dc=io",
		BindPassword: "reader-secret-value",
	}
}

func appState(dataPath string, ldap *configurator.LDAPOutput) *configurator.AppState {
	return &configurator.AppState{DataPath: dataPath, SSOEnabled: ldap != nil, LDAP: ldap}
}

func readConfigFile(t *testing.T, dataPath string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dataPath, "config", configFileName))
	require.NoError(t, err)
	return string(b)
}

func readSecretFile(t *testing.T, dataPath string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dataPath, "config", ldapSecretFileName))
	require.NoError(t, err)
	return string(b)
}

// ---- rendering ----

func TestRenderConfigWithLDAPWiresTheAuthentikOutpost(t *testing.T) {
	got := renderConfig(5232, ldapOutput())

	for _, want := range []string{
		"type = ldap",
		"ldap_uri = ldap://apps-authentik-ldap:3389",
		"ldap_base = ou=users,dc=ldap,dc=goauthentik,dc=io",
		"ldap_reader_dn = cn=ldap-service,ou=users,dc=ldap,dc=goauthentik,dc=io",
		"ldap_secret_file = /config/ldap-secret",
		"ldap_filter = (sAMAccountName={0})",
		"ldap_ignore_attribute_create_modify_timestamp = true",
		"hosts = 0.0.0.0:5232",
		"filesystem_folder = /var/lib/radicale/collections",
		"type = owner_only",
		"type = csv",
		"collection_by_map = true",
		"type = internal",
	} {
		assert.Contains(t, got, want, "rendered config must carry %q", want)
	}

	// The secret is referenced by path, never inlined: the config file is the
	// artifact an operator reads and shares when debugging.
	assert.NotContains(t, got, "reader-secret-value")
	assert.NotContains(t, got, "ldap_secret = ")
}

func TestRenderConfigWithoutProviderDeniesEverything(t *testing.T) {
	got := renderConfig(5232, nil)

	assert.Contains(t, got, "type = denyall")
	// A locked install must not still name a reader DN: nothing should be
	// configured to bind with a credential the app will never use.
	assert.NotContains(t, got, "ldap_")
	// The rest of the app is unchanged: it is locked, not absent.
	assert.Contains(t, got, "type = owner_only")
	assert.Contains(t, got, "filesystem_folder = /var/lib/radicale/collections")
}

func TestRenderConfigSecretFileMatchesTheMount(t *testing.T) {
	// The path written into the config has to be the path metadata.yaml mounts.
	// If the two drift, Radicale fails at startup with a missing-file error and
	// the cause reads like a broken image rather than a mismatched constant.
	metadata, err := os.ReadFile("metadata.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(metadata), "destination: "+containerConfigDir)
	assert.Contains(t, string(metadata), "destination: "+containerStorageDir)
	assert.Contains(t, renderConfig(5232, ldapOutput()),
		fmt.Sprintf("ldap_secret_file = %s/%s", containerConfigDir, ldapSecretFileName))
}

func TestRenderConfigIsDeterministic(t *testing.T) {
	// PreStart compares rendered bytes against the file on disk to decide
	// whether the container has to be recreated. A renderer that produced a
	// different string for the same input would recreate the container on
	// every reconciliation pass.
	a := renderConfig(5232, ldapOutput())
	b := renderConfig(5232, ldapOutput())
	assert.Equal(t, a, b)
}

func TestLDAPSearchBaseScopesToTheUserTree(t *testing.T) {
	// Searched from the directory base, Authentik's virtual-groups tree also
	// matches a login name, so every user resolves to two entries and Radicale
	// refuses the ambiguous filter. The users OU is what makes it unique.
	assert.Equal(t,
		"ou=users,dc=ldap,dc=goauthentik,dc=io",
		ldapSearchBase("dc=ldap,dc=goauthentik,dc=io"))

	// Idempotent: a provider that already reports the scoped base must not get
	// a second prefix, which would search a DN that does not exist.
	assert.Equal(t,
		"ou=users,dc=ldap,dc=goauthentik,dc=io",
		ldapSearchBase("ou=users,dc=ldap,dc=goauthentik,dc=io"))
}

func TestLDAPUserFilterIsNotUID(t *testing.T) {
	// In Authentik's LDAP, `uid` is a content hash rather than the login name,
	// so a uid-based filter matches nothing and every sign-in fails.
	assert.NotContains(t, ldapUserFilter, "uid=")
	assert.Equal(t, "(sAMAccountName={0})", ldapUserFilter)
}

// ---- PreStart ----

func TestPreStartWritesConfigAndSecret(t *testing.T) {
	c, dataDir := newTestConfigurator(t, nil)

	res, err := c.PreStart(context.Background(), appState(dataDir, ldapOutput()))
	require.NoError(t, err)
	assert.True(t, res.RestartNeeded, "a freshly written config must ask for a recreate")
	assert.NotEmpty(t, res.Reason)

	cfg := readConfigFile(t, dataDir)
	assert.Contains(t, cfg, "type = ldap")
	assert.Equal(t, "reader-secret-value\n", readSecretFile(t, dataDir))

	info, err := os.Stat(filepath.Join(dataDir, "config", configFileName))
	require.NoError(t, err)
	// ModeSharedConfig: the reader is the container's `radicale` uid, not the
	// host uid that wrote the file, so 0600 would lock the app out of its own
	// configuration.
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestPreStartOpensStorageForTheContainerUser(t *testing.T) {
	c, dataDir := newTestConfigurator(t, nil)
	_, err := c.PreStart(context.Background(), appState(dataDir, ldapOutput()))
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(dataDir, "collections"))
	require.NoError(t, err)
	// The container writes here as a subordinate uid the host agent cannot
	// become, so the directory has to carry the world-writable bit.
	assert.Equal(t, os.FileMode(0o777), info.Mode().Perm())
}

func TestPreStartIsIdempotent(t *testing.T) {
	c, dataDir := newTestConfigurator(t, nil)
	state := appState(dataDir, ldapOutput())

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	res, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, res.RestartNeeded, "a second pass over unchanged files must not ask for a recreate")
}

func TestPreStartRemovesTheSecretWhenTheProviderGoes(t *testing.T) {
	c, dataDir := newTestConfigurator(t, nil)

	_, err := c.PreStart(context.Background(), appState(dataDir, ldapOutput()))
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dataDir, "config", ldapSecretFileName))

	res, err := c.PreStart(context.Background(), appState(dataDir, nil))
	require.NoError(t, err)
	assert.True(t, res.RestartNeeded, "losing the identity provider changes the config")

	assert.NoFileExists(t, filepath.Join(dataDir, "config", ldapSecretFileName),
		"a credential nothing reads must not stay on disk")
	assert.Contains(t, readConfigFile(t, dataDir), "type = denyall")
}

func TestPreStartRewritesConfigWhenTheProviderChanges(t *testing.T) {
	c, dataDir := newTestConfigurator(t, nil)
	_, err := c.PreStart(context.Background(), appState(dataDir, ldapOutput()))
	require.NoError(t, err)

	changed := ldapOutput()
	changed.Host = "other-ldap"
	res, err := c.PreStart(context.Background(), appState(dataDir, changed))
	require.NoError(t, err)
	assert.True(t, res.RestartNeeded)
	assert.Contains(t, readConfigFile(t, dataDir), "ldap_uri = ldap://other-ldap:3389")
}

func TestPreStartToleratesEmptyProviderPassword(t *testing.T) {
	// A provider output with no reader password is not a usable provider. The
	// install must come up locked rather than writing an empty secret that
	// Radicale would try to bind with.
	c, dataDir := newTestConfigurator(t, nil)
	empty := ldapOutput()
	empty.BindPassword = ""

	_, err := c.PreStart(context.Background(), appState(dataDir, empty))
	require.NoError(t, err)

	assert.Contains(t, readConfigFile(t, dataDir), "type = denyall")
	assert.NoFileExists(t, filepath.Join(dataDir, "config", ldapSecretFileName))
}

// ---- PostStart ----

func TestPostStartAcceptsAChallenge(t *testing.T) {
	var probed []string
	c, _ := newTestConfigurator(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probed = append(probed, r.Method+" "+r.URL.Path)
		w.Header().Set("WWW-Authenticate", `Basic realm="Bloud"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))

	require.NoError(t, c.PostStart(context.Background(), appState("", ldapOutput())))
	assert.Equal(t, []string{"GET " + probePath}, probed,
		"the probe is one unauthenticated GET on a collection path")
}

func TestPostStartAcceptsForbidden(t *testing.T) {
	c, _ := newTestConfigurator(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	require.NoError(t, c.PostStart(context.Background(), nil))
}

func TestPostStartFailsWhenTheServerIsOpen(t *testing.T) {
	// A 200 on an unauthenticated collection request means the [auth] section
	// the configurator wrote is not what the running process is using. That is
	// the one outcome PostStart must refuse to pass: an open calendar server.
	c, _ := newTestConfigurator(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	err := c.PostStart(context.Background(), appState("", ldapOutput()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "200")
	assert.Contains(t, err.Error(), "the server is open")
}

func TestPostStartPassesOnABackendOutage(t *testing.T) {
	// 500 is Radicale's answer when the LDAP outpost is unreachable. The app
	// is configured correctly and the provider is down; parking the calendar in
	// ERROR on every Authentik restart would bury the real signal.
	for _, status := range []int{
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	} {
		c, _ := newTestConfigurator(t, func() http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }
		}())
		require.NoError(t, c.PostStart(context.Background(), appState("", ldapOutput())),
			"status %d is a provider outage, not a config fault", status)
	}
}

func TestPostStartPassesWhenUnreachable(t *testing.T) {
	// No server at all. Liveness is the container health check's job, and
	// PostStart runs after it passes, so a failure here is a race.
	c, _ := newTestConfigurator(t, nil)
	require.NoError(t, c.PostStart(context.Background(), appState("", ldapOutput())))
}

// TestPostStartRestartsWhenFeedsChange is the propagation path for the calendar
// aggregation: a RUNNING Radicale only ever gets a PostStart resync when a feed
// provider (Radarr, Sonarr) is installed or removed, so the feed jobs have to be
// The sharing database is re-rendered on every pass, not only in PreStart. A
// change requires a container restart because Radicale reads it at process
// start.
func TestPostStartRestartsWhenSharesChange(t *testing.T) {
	directory := authentikDirectory(t, []map[string]any{
		{"pk": 1, "username": "alice", "is_active": true, "type": "internal"},
	})
	t.Cleanup(directory.Close)

	c, dataPath := newTestConfigurator(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Bloud"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))

	var restarted []string
	c.restartContainerFn = func(_ context.Context, name string) error {
		restarted = append(restarted, name)
		return nil
	}

	state := appState(dataPath, ldapOutput())
	state.Integrations.SSO = ssoState(t, directory.URL).Integrations.SSO
	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	// A feed provider appears, which changes who the shares cover, so the
	// resync rewrites sharing.csv and restarts.
	state.Integrations.ICSFeeds = []configurator.ICSFeedBinding{feedBinding()}
	require.NoError(t, c.PostStart(context.Background(), state))
	assert.Equal(t, []string{nodeName}, restarted)

	sharing, err := os.ReadFile(filepath.Join(
		dataPath, "collections", sharesDirName, sharesFileName,
	))
	require.NoError(t, err)
	assert.Contains(t, string(sharing), "/"+calendarOwner+"/radarr/")
	assert.Contains(t, string(sharing), ";alice;")

	// Steady state: an unchanged share list must not restart the container.
	restarted = nil
	require.NoError(t, c.PostStart(context.Background(), state))
	assert.Empty(t, restarted)
}

func TestPostStartRestartFailureIsAnError(t *testing.T) {
	directory := authentikDirectory(t, []map[string]any{
		{"pk": 1, "username": "alice", "is_active": true, "type": "internal"},
	})
	t.Cleanup(directory.Close)

	c, dataPath := newTestConfigurator(t, nil)
	c.restartContainerFn = func(_ context.Context, _ string) error {
		return errors.New("runtime refused")
	}
	state := appState(dataPath, ldapOutput())
	state.Integrations.SSO = ssoState(t, directory.URL).Integrations.SSO
	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	state.Integrations.ICSFeeds = []configurator.ICSFeedBinding{feedBinding()}

	err = c.PostStart(context.Background(), state)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not be restarted")
}

// ---- ICS feed sync ----

func feedBinding() configurator.ICSFeedBinding {
	return configurator.ICSFeedBinding{
		ProviderRef: configurator.ProviderRef{
			Kind:      configurator.ProviderKindApp,
			App:       "radarr",
			Installed: true,
			Node:      "apps-radarr",
			Port:      7878,
			BaseURL:   "http://apps-radarr:7878",
		},
		APIKey:      "abc123",
		Path:        "/feed/v3/calendar/Radarr.ics",
		DisplayName: "Radarr Movies",
	}
}

// The server is plain Radicale again. Feeds used to arrive through a vendored
// plugin that wrapped this backend and wrote into it from inside the server
// process; they now arrive over CalDAV from a sidecar, so nothing here should
// reference that plugin.
func TestRenderConfigUsesTheNativeFilesystemStorage(t *testing.T) {
	got := renderConfig(5232, ldapOutput())

	assert.Contains(t, got, "type = multifilesystem")
	assert.Contains(t, got, "filesystem_folder = /var/lib/radicale/collections")
	// `filesystem` is not a type Radicale knows. It registers exactly two
	// internal storage backends, and a name outside that set fails at startup.
	assert.NotContains(t, got, "type = filesystem\n")
	for _, gone := range []string{"radicale_ics_sync", "ics_config", "hash_db"} {
		assert.NotContains(t, got, gone, "the vendored plugin is retired")
	}
}

// Calino is a browser SPA: its DAV calls to radicale.<host> are cross-origin,
// so without CORS headers the browser refuses the response and no calendar
// ever appears. Radicale's [headers] section is applied to every response,
// including the anonymous OPTIONS preflight, which is what makes the direct
// (proxy-free) connection work.
func TestRenderConfigAllowsBrowserDAVOrigins(t *testing.T) {
	got := renderConfig(5232, ldapOutput())

	for _, want := range []string{
		"Access-Control-Allow-Origin = *",
		"Access-Control-Allow-Methods = GET, HEAD, OPTIONS, PROPFIND, PROPPATCH, REPORT, PUT, DELETE, MKCALENDAR, MKCOL, MOVE, COPY",
	} {
		assert.Contains(t, got, want)
	}
	// The headers a DAV REPORT/PROPFIND preflight asks for, plus the response
	// headers a sync client reads back (ETag, Sync-Token, DAV).
	assert.Contains(t, got, "authorization")
	assert.Contains(t, got, "depth")
	assert.Contains(t, got, "Sync-Token")
	assert.Contains(t, got, "ETag")
}

// ---- config/metadata agreement ----

func TestProbePathIsNotASpecialRoute(t *testing.T) {
	// The probe relies on Radicale treating the path as an ordinary collection.
	// A path that starts with a dot is reserved for well-known routing, and a
	// redirect there would read as "not enforced".
	assert.False(t, strings.HasPrefix(strings.Trim(probePath, "/"), "."),
		"the probe path must be an ordinary collection segment, not a well-known one")
	assert.True(t, strings.HasSuffix(probePath, "/"),
		"the probe path needs its trailing slash so the server does not redirect")
}
