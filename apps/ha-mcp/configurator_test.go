// SPDX-License-Identifier: AGPL-3.0-only

package hamcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// fakeSecrets implements configurator.AppSecretsProvider with an in-memory map,
// so the mint-and-publish flow can be observed.
type fakeSecrets struct {
	values map[string]string
	// writes counts SetAppSecret calls, so a test can assert a value is minted
	// once and then read back rather than regenerated.
	writes int
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{values: map[string]string{}}
}

func (f *fakeSecrets) GenerateAppAdminPassword(string) (string, error) { return "", nil }
func (f *fakeSecrets) GetAppSecret(_, key string) string               { return f.values[key] }
func (f *fakeSecrets) SetAppSecret(_, key, value string) error {
	f.values[key] = value
	f.writes++
	return nil
}
func (f *fakeSecrets) SetAppContractValue(_, _, _, _ string) error { return nil }
func (f *fakeSecrets) GetAppContractValue(_, _, _ string) string   { return "" }
func (f *fakeSecrets) DeleteAppSecrets(string) error               { return nil }

// boundState is the AppState the graph produces once Home Assistant is installed
// and has published its token.
func boundState(dataDir string) *configurator.AppState {
	return &configurator.AppState{
		DataPath: dataDir,
		Integrations: bindings.Integrations{
			AppAPIs: []bindings.AppAPIBinding{{
				ProviderRef: bindings.ProviderRef{
					App:       "homeassistant",
					Installed: true,
					BaseURL:   "http://apps-homeassistant:8123",
				},
				Username: "bloud-bootstrap-admin",
				Password: "ha-long-lived-token",
			}},
		},
	}
}

func readEnv(t *testing.T, c *Configurator, state *configurator.AppState) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(state.DataPath, configDir, envFileName))
	require.NoError(t, err)
	return string(content)
}

// TestPreStartWritesResolvedBindings is the whole reason the env file exists:
// the image reads its configuration from process env only, and three of the
// values it needs are not static metadata.
func TestPreStartWritesResolvedBindings(t *testing.T) {
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets})
	state := boundState(t.TempDir())

	first, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded, "the first write must recreate the container")

	content := readEnv(t, c, state)
	require.Contains(t, content, "HOMEASSISTANT_URL='http://apps-homeassistant:8123'")
	require.Contains(t, content, "HOMEASSISTANT_TOKEN='ha-long-lived-token'")
	require.Contains(t, content, "MCP_SECRET_PATH='"+endpointPathPrefix)

	second, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.False(t, second.RestartNeeded, "a steady-state pass must not ask for a recreate")
	require.Equal(t, content, readEnv(t, c, state))
}

// TestPreStartUnboundWritesNoCredential pins the shape the conformance harness
// exercises: no bindings means the credential variables are absent, not empty.
// An empty HOMEASSISTANT_TOKEN is a credential the server would try; an absent
// one is a configuration error it reports.
func TestPreStartUnboundWritesNoCredential(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	state := &configurator.AppState{DataPath: t.TempDir()}

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	content := readEnv(t, c, state)
	require.NotContains(t, content, "HOMEASSISTANT_TOKEN")
	require.NotContains(t, content, "HOMEASSISTANT_URL")
	// The endpoint path is this app's own, so it exists even with no provider.
	require.Contains(t, content, "MCP_SECRET_PATH='"+endpointPathPrefix)
}

// TestPreStartWithoutSecretsProviderIsStable: a value that could not be stored
// must not be invented per pass, or every reconciliation rewrites the file and
// recreates the container.
func TestPreStartWithoutSecretsProviderIsStable(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{})
	state := boundState(t.TempDir())

	first, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded)

	second, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.False(t, second.RestartNeeded, "nothing was written twice, so nothing changed")

	content := readEnv(t, c, state)
	require.NotContains(t, content, "MCP_SECRET_PATH", "an unstorable secret is no secret")
}

// TestEnsureCredentialsMintsOnce is the reuse rule behind both files: mint what
// is missing, then read it back forever.
func TestEnsureCredentialsMintsOnce(t *testing.T) {
	secrets := newFakeSecrets()
	logger := NewConfigurator(0, configurator.Deps{}).logger

	bearer, path, err := ensureCredentials(secrets, logger)
	require.NoError(t, err)
	require.NotEmpty(t, bearer)
	require.True(t, strings.HasPrefix(path, endpointPathPrefix))
	require.Equal(t, 2, secrets.writes, "one for the bearer, one for the path")

	again, againPath, err := ensureCredentials(secrets, logger)
	require.NoError(t, err)
	require.Equal(t, bearer, again)
	require.Equal(t, path, againPath)
	require.Equal(t, 2, secrets.writes, "a second pass must mint nothing")
}

// TestRenderEnvFileRejectsAnUnquotableValue: every value here is generated or
// resolved from catalog metadata, so one carrying a quote means something
// upstream is very wrong, and a file whose quoting lies is worse than none.
func TestRenderEnvFileRejectsAnUnquotableValue(t *testing.T) {
	_, err := renderEnvFile("http://x:8123", true, "bad'\nvalue", true, "/mcp-abc")
	require.Error(t, err)
	require.Contains(t, err.Error(), envTokenKey)
}

// TestMCPPathMatchesPublishedContractValue keeps the gate's route and the
// contract's advertised path from drifting: a consumer calls the value in
// metadata.yaml, and the gate only serves this constant.
//
// It also guards the one property the gate exists to hold. SetAppSecret stores
// the bearer and the server's internal path in the same bag; what separates them
// is the catalog declaration, because a consumer cannot require a secret no
// contract lists. So "the internal path is private" is a fact about this YAML
// file, and it belongs in a test.
func TestMCPPathMatchesPublishedContractValue(t *testing.T) {
	raw, err := os.ReadFile("metadata.yaml")
	require.NoError(t, err)
	// The block is small enough to check as text: the value lives under
	// provides.mcp.values, and it is the only `path:` key in the file.
	require.Contains(t, string(raw), "path: "+mcpPublicPath+"\n")

	// The published credential is the bearer and nothing else, and the internal
	// path is not offered under any name.
	require.Contains(t, string(raw), "secrets: ["+httpTokenKey+"]\n")
	require.NotContains(t, string(raw), endpointPathKey,
		"the server's internal path must never be a declared contract secret")
}
