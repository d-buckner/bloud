// SPDX-License-Identifier: AGPL-3.0-only

package affinemcp

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
func (s *storeSecrets) DeleteAppSecrets(string) error                     { return nil }

// stateWithAffine is a pass with the resolved appApi binding AFFiNE publishes.
func stateWithAffine(dataDir string) *configurator.AppState {
	return &configurator.AppState{
		DataPath: dataDir,
		Integrations: configurator.Integrations{
			AppAPIs: []configurator.AppAPIBinding{{
				ProviderRef: configurator.ProviderRef{
					Kind:      configurator.ProviderKindApp,
					App:       "affine",
					Installed: true,
					Node:      "apps-affine",
					Port:      3010,
					BaseURL:   "http://apps-affine:3010",
					LocalURL:  "http://localhost:3010",
				},
				Username:    "admin@affine.localhost",
				Password:    "owner-password",
				WorkspaceID: "ws-shared-1",
			}},
		},
	}
}

func configPath(dataDir string) string {
	return filepath.Join(dataDir, configDirName, configSubdir, configFileName)
}

func configuratorForServer(t *testing.T, url string) *Configurator {
	t.Helper()
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	c.baseURL = url
	return c
}

func TestPreStart_WritesConfigAndPublishesBearer(t *testing.T) {
	secrets := newStoreSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	dir := t.TempDir()

	result, err := c.PreStart(context.Background(), stateWithAffine(dir))
	require.NoError(t, err)
	assert.True(t, result.RestartNeeded)

	body, err := os.ReadFile(configPath(dir))
	require.NoError(t, err)
	content := string(body)
	assert.Contains(t, content, "AFFINE_BASE_URL=http://apps-affine:3010")
	assert.Contains(t, content, "AFFINE_EMAIL=admin@affine.localhost")
	assert.Contains(t, content, "AFFINE_PASSWORD=owner-password")
	assert.Contains(t, content, "AFFINE_WORKSPACE_ID=ws-shared-1")
	assert.Contains(t, content, "AFFINE_MCP_AUTH_MODE=bearer")

	token := secrets.GetAppSecret(appName, httpTokenKey)
	require.NotEmpty(t, token, "the wrapper's own bearer must be published under the mcp contract")
	assert.Contains(t, content, "AFFINE_MCP_HTTP_TOKEN="+token)
}

func TestPreStart_IsIdempotent(t *testing.T) {
	secrets := newStoreSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	dir := t.TempDir()
	state := stateWithAffine(dir)

	first, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded)
	token := secrets.GetAppSecret(appName, httpTokenKey)
	require.NotEmpty(t, token)

	second, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, second.RestartNeeded, "a steady-state pass must not recreate the container")
	assert.Equal(t, token, secrets.GetAppSecret(appName, httpTokenKey),
		"the MCP bearer must not rotate on every pass, or a harness's namespace would be invalidated")
}

func TestPreStart_IncompleteBindingWritesNoCredential(t *testing.T) {
	secrets := newStoreSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	dir := t.TempDir()

	// The binding is present but the provider has not published the password:
	// that is "not ready", not an empty credential.
	state := stateWithAffine(dir)
	state.Integrations.AppAPIs[0].Password = ""

	result, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.True(t, result.RestartNeeded)

	content, err := os.ReadFile(configPath(dir))
	require.NoError(t, err)
	assert.NotContains(t, string(content), "AFFINE_PASSWORD=")
	assert.NotContains(t, string(content), "AFFINE_EMAIL=")
	assert.NotContains(t, string(content), "AFFINE_WORKSPACE_ID=")
	assert.Contains(t, string(content), "AFFINE_MCP_HTTP_TOKEN=")
}

// A provider that published no scope leaves AFFINE_WORKSPACE_ID unset, which is
// the wrapper's documented mode where the agent supplies the id per call. Writing
// an empty value would be read as a configured, empty scope.
func TestPreStart_WithoutWorkspaceScopeOmitsThePin(t *testing.T) {
	secrets := newStoreSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	dir := t.TempDir()
	state := stateWithAffine(dir)
	state.Integrations.AppAPIs[0].WorkspaceID = ""

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	content, err := os.ReadFile(configPath(dir))
	require.NoError(t, err)
	assert.NotContains(t, string(content), "AFFINE_WORKSPACE_ID")
	assert.Contains(t, string(content), "AFFINE_PASSWORD=owner-password")
}

func TestPreStart_NilSecretsSafeAndConverges(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	dir := t.TempDir()

	first, err := c.PreStart(context.Background(), stateWithAffine(dir))
	require.NoError(t, err)
	assert.True(t, first.RestartNeeded)

	second, err := c.PreStart(context.Background(), stateWithAffine(dir))
	require.NoError(t, err)
	assert.False(t, second.RestartNeeded, "PreStart must converge even without a secrets store")
}

func TestRenderConfig_RejectsLineBreakInValue(t *testing.T) {
	_, err := renderConfig(configurator.AppAPIBinding{Username: "a\nB", Password: "p"}, true, "t")
	require.Error(t, err, "a value containing a newline would inject a second config setting")
}

func TestPostStart_WaitsForReady(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == readyPath {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := configuratorForServer(t, server.URL)
	require.NoError(t, c.PostStart(context.Background(), &configurator.AppState{}))
	assert.Equal(t, 1, hits, "a ready wrapper is probed exactly once")
}

func TestPostStart_ReturnsErrorWhenNotReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"status":"unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	c := configuratorForServer(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	require.Error(t, c.PostStart(ctx, &configurator.AppState{}),
		"a wrapper that cannot reach its target must fail the pass rather than report success")
}

// Guard against an accidental rename of the config subdirectory: the image's
// path is fixed and a mismatch would silently leave the server unconfigured.
func TestConfigPathShape(t *testing.T) {
	assert.True(t, strings.HasSuffix(configPath("/data"), "/config/affine-mcp/config"))
}
