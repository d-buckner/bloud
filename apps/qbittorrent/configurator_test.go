// SPDX-License-Identifier: AGPL-3.0-only

package qbittorrent

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// wantPreferences are the conf values PreStart must leave behind, spelled out
// independently of the configurator's own map so a renamed or revalued key
// fails here.
var wantPreferences = map[string]string{
	"Connection\\UPnP":                  "false",
	"Downloads\\SavePath":               "/downloads/",
	"Downloads\\TempPath":               "/downloads/incomplete/",
	"WebUI\\Address":                    "*",
	"WebUI\\Port":                       "8081",
	"WebUI\\ServerDomains":              "*",
	"WebUI\\HostHeaderValidation":       "false",
	"WebUI\\AuthSubnetWhitelistEnabled": "true",
	"WebUI\\AuthSubnetWhitelist":        "0.0.0.0/0",
	"WebUI\\LocalHostAuth":              "false",
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testState(t *testing.T) *configurator.AppState {
	t.Helper()
	return &configurator.AppState{
		DataPath:      filepath.Join(t.TempDir(), "qbittorrent"),
		BloudDataPath: filepath.Join(t.TempDir(), "bloud"),
	}
}

// newTestConfigurator points a configurator at an httptest server, so PostStart's
// verification probe runs against a fake WebUI, with the production poll cadence
// collapsed to milliseconds.
func newTestConfigurator(t *testing.T, handler http.Handler) *Configurator {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	c.baseURL = srv.URL
	c.pollInterval = 10 * time.Millisecond
	return c
}

func writeConf(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
}

func readSection(t *testing.T, path, section string) *configurator.INISection {
	t.Helper()
	conf, err := configurator.LoadINI(path)
	require.NoError(t, err)
	return conf.Section(section)
}

func TestConfigurator_Name(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	assert.Equal(t, "apps-qbittorrent", c.Name())
	assert.Equal(t, webUIPort, c.port, "port 0 must select the published WebUI port")
}

func TestPreStartFreshConfWritesManagedKeys(t *testing.T) {
	state := testState(t)
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})

	changed, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.True(t, changed, "a fresh conf must report a change")

	for _, dir := range []string{
		filepath.Join(state.DataPath, "config", "qBittorrent"),
		filepath.Join(state.BloudDataPath, "downloads"),
		filepath.Join(state.BloudDataPath, "downloads", "incomplete"),
	} {
		info, err := os.Stat(dir)
		require.NoErrorf(t, err, "directory %s", dir)
		assert.Truef(t, info.IsDir(), "%s must be a directory", dir)
	}

	confPath := filepath.Join(state.DataPath, confRelPath)

	accepted, ok := readSection(t, confPath, legalNoticeSection).Get("Accepted")
	require.True(t, ok, "LegalNotice\\Accepted must be set")
	assert.Equal(t, "true", accepted)

	prefs := readSection(t, confPath, preferencesSection)
	for key, want := range wantPreferences {
		got, ok := prefs.Get(key)
		require.Truef(t, ok, "missing %s", key)
		assert.Equalf(t, want, got, "%s", key)
	}
}

// The daemon runs as LSIO's `abc`, a host subuid under rootless podman that the
// host agent is neither owner nor group member of, and its init chowns what it
// mounts. So every path Bloud merges into or pre-creates has to stay writable by
// both; otherwise the merge that repairs a drifted WebUI key fails with EACCES,
// and a PreStart failure is terminal for the node.
func TestPreStartMakesItsWritablePathsWorldWritable(t *testing.T) {
	state := testState(t)
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	confPath := filepath.Join(state.DataPath, confRelPath)

	// A conf the daemon already wrote, and directories an earlier run left
	// tightly moded.
	writeConf(t, confPath, `[LegalNotice]
Accepted=true
`)
	require.NoError(t, os.Chmod(confPath, 0o644))

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	info, err := os.Stat(confPath)
	require.NoErrorf(t, err, "stat %s", confPath)
	assert.Equalf(t, os.FileMode(0o666), info.Mode().Perm(), "%s mode", confPath)

	for _, dir := range []string{
		filepath.Join(state.DataPath, "config"),
		filepath.Join(state.DataPath, "config", "qBittorrent"),
		filepath.Join(state.BloudDataPath, "downloads"),
		filepath.Join(state.BloudDataPath, "downloads", "incomplete"),
	} {
		info, err := os.Stat(dir)
		require.NoErrorf(t, err, "stat %s", dir)
		assert.Equalf(t, os.FileMode(0o777), info.Mode().Perm(), "%s mode", dir)
	}
}

func TestPreStartPreservesKeysBloudDoesNotOwn(t *testing.T) {
	state := testState(t)
	confPath := filepath.Join(state.DataPath, confRelPath)
	writeConf(t, confPath, `[LegalNotice]
Accepted=false

[BitTorrent]
Session\Port=51413

[Preferences]
WebUI\Port=8080
WebUI\HostHeaderValidation=true
WebUI\AuthSubnetWhitelistEnabled=false
WebUI\AuthSubnetWhitelist=192.168.1.0/24
Downloads\SavePath=/media/user-picks/
Session\DefaultSavePath=/config/session
WebUI\Username=someone
`)

	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	changed, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.True(t, changed, "correcting drifted keys must report a change")

	prefs := readSection(t, confPath, preferencesSection)
	// Drifted Bloud-owned keys are corrected, including the download path: it
	// must point at the volume Bloud mounts.
	for key, want := range wantPreferences {
		got, ok := prefs.Get(key)
		require.Truef(t, ok, "missing %s", key)
		assert.Equalf(t, want, got, "%s", key)
	}
	accepted, ok := readSection(t, confPath, legalNoticeSection).Get("Accepted")
	require.True(t, ok)
	assert.Equal(t, "true", accepted, "the legal notice is a managed key")
	// Keys Bloud does not own survive untouched, in their own section too.
	for key, want := range map[string]string{
		"Session\\DefaultSavePath": "/config/session",
		"WebUI\\Username":          "someone",
	} {
		got, ok := prefs.Get(key)
		require.Truef(t, ok, "missing %s", key)
		assert.Equalf(t, want, got, "%s", key)
	}
	port, ok := readSection(t, confPath, "BitTorrent").Get("Session\\Port")
	require.True(t, ok, "unowned [BitTorrent] section must survive")
	assert.Equal(t, "51413", port)
}

func TestPreStartSecondRunReportsNoChange(t *testing.T) {
	state := testState(t)
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	confPath := filepath.Join(state.DataPath, confRelPath)

	changed, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, changed)
	first, err := os.ReadFile(confPath)
	require.NoError(t, err)

	changed, err = c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, changed, "an already-configured conf must not recreate the container")

	second, err := os.ReadFile(confPath)
	require.NoError(t, err)
	assert.Equal(t, first, second, "an unchanged conf must not be rewritten")
}

func TestPostStartSucceedsWhenWebUIAcceptsAnonymousRequest(t *testing.T) {
	var hits atomic.Int32
	c := newTestConfigurator(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/app/version" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		_, _ = w.Write([]byte("v5.2.3"))
	}))

	require.NoError(t, c.PostStart(context.Background(), testState(t)))
	assert.Positive(t, hits.Load(), "PostStart must probe the WebUI API")
}

func TestPostStartReportsRejectedStatus(t *testing.T) {
	c := newTestConfigurator(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))

	err := c.PostStart(context.Background(), testState(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403", "the error must name the observed status")
}
