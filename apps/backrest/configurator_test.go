// SPDX-License-Identifier: AGPL-3.0-only

package backrest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSecrets is an in-memory AppSecretsProvider. Only GetAppSecret and
// SetAppSecret carry state; the contract-value half is required by the
// interface but unused by this app.
type fakeSecrets struct {
	values map[string]string
}

func (f *fakeSecrets) GenerateAppAdminPassword(string) (string, error) { return "", nil }

func (f *fakeSecrets) GetAppSecret(app, key string) string {
	return f.values[app+"\x00"+key]
}

func (f *fakeSecrets) SetAppSecret(app, key, value string) error {
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[app+"\x00"+key] = value
	return nil
}

func (f *fakeSecrets) SetAppContractValue(string, string, string, string) error { return nil }
func (f *fakeSecrets) GetAppContractValue(string, string, string) string        { return "" }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestPreStart_seedsConfigAndRepository pins the whole seed: the config file it
// writes, the password it stores, and the mount sources it creates.
func TestPreStart_seedsConfigAndRepository(t *testing.T) {
	dir := t.TempDir()
	bloud := t.TempDir()
	secrets := &fakeSecrets{}
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})

	res, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: dir, BloudDataPath: bloud})
	require.NoError(t, err)
	assert.True(t, res.RestartNeeded, "the first pass must ask for a recreate so the container picks the config up")

	raw, err := os.ReadFile(filepath.Join(dir, "config", configFileName))
	require.NoError(t, err)

	var cfg backrestConfig
	require.NoError(t, json.Unmarshal(raw, &cfg))
	assert.Equal(t, int64(configVersion), cfg.Version, "Backrest rejects an unversioned non-empty config")
	assert.True(t, cfg.Auth.Disabled, "the forward-auth proxy authenticates the browser")

	require.Len(t, cfg.Repos, 1)
	assert.Equal(t, containerRepoDir+"/"+repoDirName, cfg.Repos[0].URI)
	assert.True(t, cfg.Repos[0].AutoInitialize, "a repository that does not exist yet needs autoInitialize")
	assert.NotEmpty(t, cfg.Repos[0].Password)

	require.Len(t, cfg.Plans, 1)
	assert.Equal(t, defaultRepoID, cfg.Plans[0].Repo)
	assert.Equal(t, []string{containerAppDataDir}, cfg.Plans[0].Paths)
	assert.Equal(t, []string{containerAppDataDir + "/" + appName}, cfg.Plans[0].Excludes,
		"the plan must not back Backrest's own state up into itself")

	// The repository password is the app's generated secret, stored once.
	assert.Equal(t, cfg.Repos[0].Password, secrets.GetAppSecret(appName, resticPasswordKey))

	// Every bind-mount source exists before the container is created.
	for _, d := range []string{"data", "config", "cache", "userdata"} {
		info, statErr := os.Stat(filepath.Join(dir, d))
		require.NoError(t, statErr)
		assert.True(t, info.IsDir())
	}
	info, err := os.Stat(filepath.Join(bloud, backupsDirName))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

// TestPreStart_neverRewritesAnExistingConfig is the load-bearing safety
// property. Backrest rewrites config.json for every change the user makes in
// its UI, so a second PreStart that overwrote it would discard their
// repositories, plans, and schedules on the next reconcile.
func TestPreStart_neverRewritesAnExistingConfig(t *testing.T) {
	dir := t.TempDir()
	bloud := t.TempDir()
	c := NewConfigurator(0, configurator.Deps{Secrets: &fakeSecrets{}, Logger: quietLogger()})
	state := &configurator.AppState{DataPath: dir, BloudDataPath: bloud}

	first, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded)

	// Stand in for a config Backrest has since rewritten.
	path := filepath.Join(dir, "config", configFileName)
	edited := []byte(`{"version":6,"instance":"bloud","repos":[{"id":"user-added"}]}`)
	require.NoError(t, os.WriteFile(path, edited, 0o600))

	second, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, second.RestartNeeded, "a converged pass must not ask for a recreate")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, edited, after, "PreStart must leave the user-owned config untouched")
}

// TestPreStart_withoutSecretsWritesNoConfig pins the zero-Deps contract: with
// no store there is no stable password, so the seed is skipped rather than
// written with a throwaway one.
func TestPreStart_withoutSecretsWritesNoConfig(t *testing.T) {
	dir := t.TempDir()
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})

	res, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: dir, BloudDataPath: t.TempDir()})
	require.NoError(t, err)
	assert.False(t, res.RestartNeeded)

	_, err = os.Stat(filepath.Join(dir, "config", configFileName))
	assert.True(t, os.IsNotExist(err), "no config should be written without a secret store")
}

// TestPreStart_nilStateIsSafe keeps the phase from panicking in the degraded
// contexts the configtest harness also exercises.
func TestPreStart_nilStateIsSafe(t *testing.T) {
	_, err := NewConfigurator(0, configurator.Deps{}).PreStart(context.Background(), nil)
	require.NoError(t, err)
}

// TestRenderConfig_usesProtojsonFieldNames pins the wiring to Backrest's proto
// schema. Backrest parses config.json with protojson and discards unknown
// fields, so a renamed json tag would make it silently ignore the repository or
// the retention policy instead of failing, which is exactly the kind of drift a
// type-checked struct cannot catch on its own.
func TestRenderConfig_usesProtojsonFieldNames(t *testing.T) {
	raw, err := renderConfig("pw")
	require.NoError(t, err)

	var generic map[string]any
	require.NoError(t, json.Unmarshal(raw, &generic))

	repo, ok := generic["repos"].([]any)[0].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, repo, "autoInitialize")
	assert.Contains(t, repo, "prunePolicy")

	plan, ok := generic["plans"].([]any)[0].(map[string]any)
	require.True(t, ok)
	schedule, ok := plan["schedule"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, backupCron, schedule["cron"])
	assert.Equal(t, "CLOCK_LOCAL", schedule["clock"])

	retention, ok := plan["retention"].(map[string]any)
	require.True(t, ok)
	buckets, ok := retention["policyTimeBucketed"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 7, buckets["daily"])
}
