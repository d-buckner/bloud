// SPDX-License-Identifier: AGPL-3.0-only

package immichmcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// storeSecrets is an in-memory AppSecretsProvider that keeps what the
// configurator publishes, so the bearer's generate-once/read-back cycle can be
// exercised across passes rather than only observed in one direction.
type storeSecrets struct {
	mu      sync.Mutex
	secrets map[string]string
}

func newStoreSecrets() *storeSecrets { return &storeSecrets{secrets: map[string]string{}} }

func (s *storeSecrets) GenerateAppAdminPassword(string) (string, error) { return "", nil }

func (s *storeSecrets) GetAppSecret(_, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secrets[key]
}

func (s *storeSecrets) SetAppSecret(_, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[key] = value
	return nil
}

func (s *storeSecrets) SetAppContractValue(string, string, string, string) error { return nil }

func (s *storeSecrets) GetAppContractValue(string, string, string) string { return "" }

// stateWithImmich is a pass with the resolved appToken binding Immich
// publishes: the minted key plus the address the wrapper dials.
func stateWithImmich(dataDir string) *configurator.AppState {
	return &configurator.AppState{
		DataPath: dataDir,
		Integrations: configurator.Integrations{
			AppTokens: []configurator.AppTokenBinding{{
				ProviderRef: configurator.ProviderRef{
					Kind:      configurator.ProviderKindApp,
					App:       "immich",
					Installed: true,
					Node:      "apps-immich-server",
					Port:      2283,
					BaseURL:   "http://apps-immich-server:2283",
					LocalURL:  "http://localhost:2283",
				},
				Token: "immich-api-key-1",
			}},
		},
	}
}

func configPath(dataDir, name string) string {
	return filepath.Join(dataDir, configDirName, name)
}

func TestPreStart_WritesBothFilesAndPublishesBearer(t *testing.T) {
	secrets := newStoreSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	dir := t.TempDir()

	result, err := c.PreStart(context.Background(), stateWithImmich(dir))
	require.NoError(t, err)
	assert.True(t, result.RestartNeeded, "a changed edge config has to recreate the edge")

	settings, err := os.ReadFile(configPath(dir, appSettingsFile))
	require.NoError(t, err)
	var doc map[string]map[string]string
	require.NoError(t, json.Unmarshal(settings, &doc))
	assert.Equal(t, "http://apps-immich-server:2283", doc["Immich"]["BaseUrl"])
	assert.Equal(t, "immich-api-key-1", doc["Immich"]["ApiKey"])

	caddy, err := os.ReadFile(configPath(dir, caddyfileName))
	require.NoError(t, err)
	token := secrets.GetAppSecret(appName, httpTokenKey)
	require.NotEmpty(t, token, "the app's own bearer must be published under the mcp contract")
	assert.Contains(t, string(caddy), "Bearer "+token)
	assert.Contains(t, string(caddy), "reverse_proxy "+upstreamNode+":5000")
	assert.Contains(t, string(caddy), "path /health")
}

func TestPreStart_IsIdempotent(t *testing.T) {
	secrets := newStoreSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	dir := t.TempDir()
	state := stateWithImmich(dir)

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	second, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, second.RestartNeeded, "a second pass with the same inputs must converge")
}

// A pass with no published Immich key still writes the upstream config; the
// key field is empty, so the readiness probe fails and the node retries rather
// than a wrapper reporting RUNNING that 401s every call.
func TestPreStart_NoPublishedKeyStillWritesTheUpstreamConfig(t *testing.T) {
	secrets := newStoreSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	dir := t.TempDir()

	_, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: dir})
	require.NoError(t, err, "a missing binding is not a PreStart failure")

	settings, err := os.ReadFile(configPath(dir, appSettingsFile))
	require.NoError(t, err)
	assert.Contains(t, string(settings), `"ApiKey": ""`,
		"the readiness probe, not the config file, is what fails without a key")

	// The edge's own bearer is independent of the Immich key, so it is still
	// published and still gates the proxy.
	caddy, err := os.ReadFile(configPath(dir, caddyfileName))
	require.NoError(t, err)
	assert.Contains(t, string(caddy), "reverse_proxy "+upstreamNode+":5000")
}

// With no secrets store at all (CLI/tests) there is no bearer to check, so the
// edge config serves only its liveness probe rather than a literal empty one.
func TestPreStart_NoSecretsServesOnlyHealth(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	dir := t.TempDir()

	_, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: dir})
	require.NoError(t, err)

	caddy, err := os.ReadFile(configPath(dir, caddyfileName))
	require.NoError(t, err)
	assert.Contains(t, string(caddy), "path /health")
	assert.NotContains(t, string(caddy), "reverse_proxy", "no bearer means nothing is proxied")
	assert.NotContains(t, string(caddy), "Bearer ")
}

func TestEnsureHTTPToken_ReusesStoredToken(t *testing.T) {
	secrets := newStoreSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})

	first, err := c.ensureHTTPToken()
	require.NoError(t, err)
	require.NotEmpty(t, first)

	second, err := c.ensureHTTPToken()
	require.NoError(t, err)
	assert.Equal(t, first, second, "a restart must not invalidate an already-registered namespace")
}

func TestRenderCaddyfile_RejectsUnquotableToken(t *testing.T) {
	_, err := renderCaddyfile(upstreamNode, defaultPort, `bad"token`)
	require.Error(t, err)
}

func TestAppTokenBinding_SkipsIncompleteBindings(t *testing.T) {
	state := &configurator.AppState{
		Integrations: configurator.Integrations{
			AppTokens: []configurator.AppTokenBinding{
				{ProviderRef: configurator.ProviderRef{App: "immich", Installed: true, BaseURL: "http://apps-immich:2283"}, Token: ""},
				{ProviderRef: configurator.ProviderRef{App: "immich", Installed: true}, Token: "key-without-address"},
				{ProviderRef: configurator.ProviderRef{App: "immich", BaseURL: "http://apps-immich:2283"}, Token: "key-not-installed"},
			},
		},
	}
	_, _, ok := appTokenBinding(state)
	assert.False(t, ok, "an empty token, address, or an uninstalled provider is not a usable credential")

	state.Integrations.AppTokens = append(state.Integrations.AppTokens, configurator.AppTokenBinding{
		ProviderRef: configurator.ProviderRef{App: "immich", Installed: true, BaseURL: "http://apps-immich:2283/"},
		Token:       "usable",
	})
	token, base, ok := appTokenBinding(state)
	require.True(t, ok)
	assert.Equal(t, "usable", token)
	assert.Equal(t, "http://apps-immich:2283", base, "a trailing slash is trimmed before it reaches the image")
}

// PostStart is the resync path: a key Immich replaced after the wrapper came up
// reaches the upstream by rewriting its config and restarting that container,
// because the upstream reads the file only at startup.
func TestPostStart_RestartsUpstreamWhenConfigChanges(t *testing.T) {
	var restarted []string
	c := NewConfigurator(0, configurator.Deps{
		Secrets: newStoreSecrets(),
		Logger:  quietLogger(),
		RestartContainer: func(_ context.Context, name string) error {
			restarted = append(restarted, name)
			return nil
		},
	})
	dir := t.TempDir()

	// Seed the file with the value a previous pass wrote, so the pass below
	// has something to change.
	_, err := managedfile.Write(configPath(dir, appSettingsFile), []byte("stale\n"), managedfile.ModeHostOnly)
	require.NoError(t, err)

	require.NoError(t, c.PostStart(context.Background(), stateWithImmich(dir)))
	require.Equal(t, []string{upstreamNode}, restarted)

	restarted = nil
	require.NoError(t, c.PostStart(context.Background(), stateWithImmich(dir)))
	assert.Empty(t, restarted, "an unchanged config must not restart the upstream")
}
