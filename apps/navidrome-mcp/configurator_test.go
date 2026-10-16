// SPDX-License-Identifier: AGPL-3.0-only

package navidromemcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// fakeSecrets implements configurator.AppSecretsProvider with an in-memory map,
// so the mint-and-publish flow can be observed.
type fakeSecrets struct {
	values map[string]string
	// writes counts SetAppSecret calls, so a test can assert the bearer is minted
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

// boundState is the AppState the graph produces once a music server is installed
// and has published its account credential.
func boundState(dataDir string) *configurator.AppState {
	return &configurator.AppState{
		DataPath: dataDir,
		Integrations: bindings.Integrations{
			MediaServers: []bindings.MediaServerBinding{{
				ProviderRef: bindings.ProviderRef{
					App:       "navidrome",
					Installed: true,
					BaseURL:   "http://apps-navidrome:4533",
				},
				AdminUsername: "bloud-admin",
				AdminPassword: "navidrome-account-password",
			}},
		},
	}
}

func readEnv(t *testing.T, state *configurator.AppState) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(state.DataPath, configDir, envFileName))
	require.NoError(t, err)
	return string(content)
}

// TestPreStartWritesResolvedBindings is the whole reason the env file exists: the
// image reads its configuration from process env, and the three values that point
// it at a library are a resolved binding and a published secret.
func TestPreStartWritesResolvedBindings(t *testing.T) {
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets})
	state := boundState(t.TempDir())

	first, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded, "the first write must recreate the container")

	content := readEnv(t, state)
	require.Contains(t, content, envNavidromeURL+"=http://apps-navidrome:4533\n")
	require.Contains(t, content, envNavidromeUser+"=bloud-admin\n")
	require.Contains(t, content, envNavidromePass+"=navidrome-account-password\n")
	require.Contains(t, content, envAuthToken+"=")
	require.NotContains(t, content, "absent:")

	second, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.False(t, second.RestartNeeded, "a steady-state pass must not ask for a recreate")
	require.Equal(t, content, readEnv(t, state))
}

// TestPreStartUnboundWritesNoCredential pins the shape the conformance harness
// exercises: no provider means the credential variables are absent, not empty. An
// empty NAVIDROME_URL is a destination the server would try; an absent one is a
// configuration error it reports.
func TestPreStartUnboundWritesNoCredential(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	state := &configurator.AppState{DataPath: t.TempDir()}

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	content := readEnv(t, state)
	require.NotContains(t, content, envNavidromeURL+"=")
	require.NotContains(t, content, envNavidromePass+"=")
}

// TestPreStartProviderWithoutCredentialSaysSo is the difference between "not ready
// yet" and "configured with nothing", which is the distinction the next pass needs.
func TestPreStartProviderWithoutCredentialSaysSo(t *testing.T) {
	state := boundState(t.TempDir())
	state.Integrations.MediaServers[0].AdminPassword = ""

	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	content := readEnv(t, state)
	require.Contains(t, content, envNavidromeURL+"=http://apps-navidrome:4533\n")
	require.Contains(t, content, "# "+envNavidromePass+" absent:")
	require.NotContains(t, content, envNavidromePass+"=")
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
	require.NotContains(t, readEnv(t, state), envAuthToken+"=")
}

// TestBearerMintedOnce is the reuse rule: mint what is missing, then read it back
// forever. A regenerated bearer would revoke every harness that was given it.
func TestBearerMintedOnce(t *testing.T) {
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets})

	first, err := c.ensureBearer()
	require.NoError(t, err)
	require.NotEmpty(t, first)
	require.Equal(t, 1, secrets.writes)

	second, err := c.ensureBearer()
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, secrets.writes, "a second pass must mint nothing")
	require.Equal(t, first, c.currentBearer())
}

// TestPreStartFileModes: the env file holds the music server's account password,
// and only the host reads it, because the runtime passes the values in at create
// time and never mounts the file. The directory beside it is the /config mount,
// which the image's own uid has to be able to write.
func TestPreStartFileModes(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	state := boundState(t.TempDir())

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	dir := filepath.Join(state.DataPath, configDir)
	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(configDirPerm), dirInfo.Mode().Perm())

	envInfo, err := os.Stat(filepath.Join(dir, envFileName))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), envInfo.Mode().Perm())
}

// TestEnvValue covers the writer's half of the env-file contract: what survives
// one `KEY=value` line unchanged.
func TestEnvValue(t *testing.T) {
	cases := []struct {
		name  string
		value string
		ok    bool
	}{
		{"a URL", "http://apps-navidrome:4533", true},
		{"a base64url token", "Zm9vYmFy-Q_x", true},
		{"an apostrophe inside a password", "d'artagnan", true},
		{"a leading quote that is not paired", `"quoted"`, false},
		{"a line break", "two\nlines", false},
		{"nothing at all", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := envValue(tc.value)
			require.Equal(t, tc.ok, ok)
			if ok {
				require.Equal(t, tc.value, got, "a value that fits is written as it came in")
			}
		})
	}
}

// TestRenderEnvFileOmitsAnUnrenderableValueWithoutEchoingIt: the omitted value is
// a password, so the file records that something was left out and never what it
// was.
func TestRenderEnvFileOmitsAnUnrenderableValueWithoutEchoingIt(t *testing.T) {
	provider := bindings.MediaServerBinding{
		ProviderRef:   bindings.ProviderRef{App: "navidrome", Installed: true, BaseURL: "http://n:4533"},
		AdminUsername: "bloud-admin",
		AdminPassword: "two\nlines",
	}

	content, omitted := renderEnvFile(provider, true, "bearer-value")
	require.Equal(t, envNavidromePass, omitted)
	require.Contains(t, content, "# "+envNavidromePass+" omitted:")
	require.NotContains(t, content, envNavidromePass+"=")
	require.NotContains(t, content, "two")
	require.Contains(t, content, envNavidromeUser+"=bloud-admin\n")
}

// TestMetadataPathMatchesPublishedContractValue keeps the published endpoint and
// the path the probe dials from drifting apart: a consumer calls the value in
// metadata.yaml, and this constant is the only thing Bloud calls.
func TestMetadataPathMatchesPublishedContractValue(t *testing.T) {
	raw, err := os.ReadFile("metadata.yaml")
	require.NoError(t, err)
	// The block is small enough to check as text: the value lives under
	// provides.mcp.values, and it is the only `path:` key in the file.
	require.Contains(t, string(raw), "path: "+mcpPath+"\n")
	require.Contains(t, string(raw), "secrets: ["+httpTokenKey+"]\n")
}
