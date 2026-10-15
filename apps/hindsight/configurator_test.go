// SPDX-License-Identifier: AGPL-3.0-only

package hindsight

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

// inferenceState is the AppState the graph produces once the operator has filled
// in an AI endpoint under Settings.
func inferenceState(dataDir string) *configurator.AppState {
	return &configurator.AppState{
		DataPath: dataDir,
		Integrations: bindings.Integrations{
			Inference: []bindings.InferenceBinding{{
				ProviderRef:  bindings.ProviderRef{App: "instance", Installed: true},
				Endpoint:     "http://apps-gateway:4000/v1",
				APIKey:       "sk-test-key",
				DefaultModel: "gpt-4o-mini",
			}},
		},
	}
}

func readEnvFile(t *testing.T, state *configurator.AppState) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(state.DataPath, configDir, envFileName))
	require.NoError(t, err)
	return string(content)
}

// envValue returns the value of one line of the rendered env file, or "" when the
// line is absent. Absent and empty are different states in this file, so a test
// needs to tell them apart.
func envValue(t *testing.T, content, key string) (string, bool) {
	t.Helper()
	for _, line := range strings.Split(content, "\n") {
		if name, value, ok := strings.Cut(line, "="); ok && name == key {
			return value, true
		}
	}
	return "", false
}

// TestPreStartMintsAndWritesBothCredentials is the whole reason the env file
// exists: the image reads its configuration from process env only, and the two
// credentials it needs are minted here rather than written down.
func TestPreStartMintsAndWritesBothCredentials(t *testing.T) {
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets})
	state := &configurator.AppState{DataPath: t.TempDir()}

	first, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded, "the first write must recreate the container")

	content := readEnvFile(t, state)
	bearer, ok := envValue(t, content, envTenantAPIKey)
	require.True(t, ok, "the MCP bearer line must be present")
	require.NotEmpty(t, bearer)
	require.Equal(t, bearer, secrets.values[httpTokenKey],
		"the bearer the file carries is the one published to the mcp contract")

	accessKey, ok := envValue(t, content, envCPAccessKey)
	require.True(t, ok, "the access key line must be present")
	require.Equal(t, accessKey, secrets.values[passwordKey])

	// The Control Plane calls the API server-side and re-exposes the result under
	// its own routes, so it must carry the same bearer a consumer presents. A UI
	// that loads, signs in, and then 401s on every panel is the failure mode.
	dataplane, ok := envValue(t, content, envCPDataplaneKey)
	require.True(t, ok)
	require.Equal(t, bearer, dataplane,
		"the Control Plane must present the published bearer to the API")

	require.Equal(t, tenantExtensionAPIKey, mustEnv(t, content, envTenantExtension),
		"the API only checks the bearer once the tenant extension is loaded")
	require.Equal(t, 2, secrets.writes, "both credentials are minted once")
}

func mustEnv(t *testing.T, content, key string) string {
	t.Helper()
	value, ok := envValue(t, content, key)
	require.Truef(t, ok, "%s must be present", key)
	return value
}

// TestPreStartIsIdempotent is invariant 2: PreStart runs on every pass, so a
// steady-state pass must write identical bytes and ask for no recreate.
func TestPreStartIsIdempotent(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	state := &configurator.AppState{DataPath: t.TempDir()}

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	first := readEnvFile(t, state)

	second, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.False(t, second.RestartNeeded, "an unchanged config must not recreate the container")
	require.Equal(t, first, readEnvFile(t, state))
}

// TestNoInferenceSelectsTheNoneProvider guards the one default Bloud must never
// fall through to. The image's own default provider is OpenAI with a hosted key,
// so an install whose operator configured nothing must say so in its own config
// rather than dial a provider nobody chose.
func TestNoInferenceSelectsTheNoneProvider(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	state := &configurator.AppState{DataPath: t.TempDir()}

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	content := readEnvFile(t, state)

	require.Equal(t, llmProviderNone, mustEnv(t, content, envLLMProvider))
	_, hasBaseURL := envValue(t, content, envLLMBaseURL)
	require.False(t, hasBaseURL, "with no endpoint configured no address may be written")
}

// TestInferenceBindingReachesTheApp is the contract wiring: the Settings row is
// an OpenAI-compatible endpoint, and the image speaks that through its `openai`
// provider.
func TestInferenceBindingReachesTheApp(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	state := inferenceState(t.TempDir())

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	content := readEnvFile(t, state)

	require.Equal(t, llmProviderOpenAI, mustEnv(t, content, envLLMProvider))
	require.Equal(t, "http://apps-gateway:4000/v1", mustEnv(t, content, envLLMBaseURL))
	require.Equal(t, "sk-test-key", mustEnv(t, content, envLLMAPIKey))
	require.Equal(t, "gpt-4o-mini", mustEnv(t, content, envLLMModel))
}

// TestKeylessInferenceStillWritesAKey records a constraint measured against the
// real image rather than guessed: the app builds an OpenAI SDK client, and that
// client refuses to construct with no key at all, while the app collapses an
// empty string to "no key" on the way through. So a keyless upstream needs a
// non-empty value that is obviously not a secret.
func TestKeylessInferenceStillWritesAKey(t *testing.T) {
	state := inferenceState(t.TempDir())
	state.Integrations.Inference[0].APIKey = ""
	state.Integrations.Inference[0].DefaultModel = ""

	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	content := readEnvFile(t, state)

	require.Equal(t, noCredentialKey, mustEnv(t, content, envLLMAPIKey))
	_, hasModel := envValue(t, content, envLLMModel)
	require.False(t, hasModel, "an endpoint with no default model must not be given one")
}

// TestAnInferenceLineIsNeverEmpty is the shape rule the two tests above add up
// to: every key the file writes has a value, because an empty
// HINDSIGHT_API_LLM_API_KEY= reads as "configured with nothing" to a reader and
// as "not configured" to the SDK.
func TestAnInferenceLineIsNeverEmpty(t *testing.T) {
	for name, state := range map[string]*configurator.AppState{
		"no inference":   {DataPath: t.TempDir()},
		"with inference": inferenceState(t.TempDir()),
	} {
		c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
		_, err := c.PreStart(context.Background(), state)
		require.NoError(t, err, name)

		for _, line := range strings.Split(readEnvFile(t, state), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			_, value, ok := strings.Cut(line, "=")
			require.Truef(t, ok, "%s: every line is KEY=value: %q", name, line)
			require.NotEmptyf(t, value, "%s: no key may be written empty: %q", name, line)
		}
	}
}

// TestEnvFileIsNotWorldReadable pins the one property that separates this file
// from the gate's nginx config: it holds two live credentials and no container
// ever reads it, so nothing outside this host may.
func TestEnvFileIsNotWorldReadable(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	state := &configurator.AppState{DataPath: t.TempDir()}

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(state.DataPath, configDir, envFileName))
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0o077,
		"a file holding the MCP bearer and the access key must not be readable by others")
}

// TestCredentialsAreMintedOnceAndRotationReachesTheApp covers rotation, which is
// what `rotate: bloud` on the clientPassword offer claims. The credential lives
// in the store; the app only ever sees the file, so a rotation has to change the
// file and ask for a recreate or the label is a lie.
func TestCredentialsAreMintedOnceAndRotationReachesTheApp(t *testing.T) {
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets})
	state := &configurator.AppState{DataPath: t.TempDir()}

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.Equal(t, 2, secrets.writes)

	_, err = c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.Equal(t, 2, secrets.writes, "a second pass reads the stored values, it does not mint")

	secrets.values[httpTokenKey] = "rotated-bearer"
	third, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, third.RestartNeeded, "a rotated bearer must reach the running container")
	require.Equal(t, "rotated-bearer", mustEnv(t, readEnvFile(t, state), envTenantAPIKey))
}

// TestPreStartWithoutASecretsStoreIsStillIdempotent covers the CLI and test
// context, where nothing can be stored. The render must be stable, because a
// value invented per pass would restart the container on every reconciliation.
func TestPreStartWithoutASecretsStoreIsStillIdempotent(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{})
	state := &configurator.AppState{DataPath: t.TempDir()}

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	content := readEnvFile(t, state)

	_, hasBearer := envValue(t, content, envTenantAPIKey)
	require.False(t, hasBearer, "nothing may be written as a credential when none can be stored")
	_, hasExtension := envValue(t, content, envTenantExtension)
	require.False(t, hasExtension,
		"the extension must not load with no key behind it; the gate is what refuses")

	second, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.False(t, second.RestartNeeded, "an unstorable config must not churn the container")
	require.Equal(t, content, readEnvFile(t, state))
}

// TestCredentialsAreHighEntropyAndURLSafe checks the property randomToken exists
// for, not just that it returns something: both values go into an env file line,
// and base64url needs no quoting there.
func TestCredentialsAreHighEntropyAndURLSafe(t *testing.T) {
	seen := map[string]bool{}
	for range 8 {
		token, err := randomToken()
		require.NoError(t, err)
		require.Len(t, token, 43, "32 bytes as base64url without padding")
		require.False(t, seen[token], "two calls must never agree")
		seen[token] = true
		require.NotContains(t, token, "=")
		require.NotContains(t, token, "+")
		require.NotContains(t, token, "/")
	}
}

// TestPickInferenceIgnoresAnUnresolvedBinding pins the rule that a provider the
// graph has not converged on is not a provider. Writing its address anyway would
// configure the app against an endpoint that does not answer yet and report it as
// configured.
func TestPickInferenceIgnoresAnUnresolvedBinding(t *testing.T) {
	state := inferenceState(t.TempDir())
	state.Integrations.Inference[0].Installed = false
	if _, ok := pickInference(state); ok {
		t.Fatal("a provider that is not installed is not an endpoint")
	}

	state = inferenceState(t.TempDir())
	state.Integrations.Inference[0].Endpoint = "  "
	if _, ok := pickInference(state); ok {
		t.Fatal("a provider with no endpoint is not an endpoint")
	}

	if _, ok := pickInference(nil); ok {
		t.Fatal("no state is no endpoint")
	}
}

// embeddingsState is an AppState whose `embeddings` contract resolved to the
// instance's Settings row.
func embeddingsState(dataDir string) *configurator.AppState {
	return &configurator.AppState{
		DataPath: dataDir,
		Integrations: bindings.Integrations{
			Embeddings: []bindings.EmbeddingsBinding{{
				ProviderRef: bindings.ProviderRef{App: "instance", Installed: true},
				Endpoint:    "http://apps-gateway:4000/v1",
				APIKey:      "sk-embed",
				Model:       "bge-m3",
			}},
		},
	}
}

// TestEmbeddingsBindingReachesTheApp is the wiring the slim image depends on: it
// has no embedding model of its own, so the Settings row is the only thing that
// can make a memory storable.
func TestEmbeddingsBindingReachesTheApp(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	state := embeddingsState(t.TempDir())

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	content := readEnvFile(t, state)

	require.Equal(t, llmProviderOpenAI, mustEnv(t, content, envEmbeddingsProvider))
	require.Equal(t, "http://apps-gateway:4000/v1", mustEnv(t, content, envEmbeddingsBaseURL))
	require.Equal(t, "sk-embed", mustEnv(t, content, envEmbeddingsAPIKey))
	require.Equal(t, "bge-m3", mustEnv(t, content, envEmbeddingsModel))
}

// TestNoEmbeddingsWritesNoEmbeddingBlock records why the block is omitted rather
// than defaulted. The image's own default is a local provider, and the slim build
// has no sentence-transformers module to satisfy it: writing that value would
// take a clear startup failure about a missing endpoint and replace it with a
// module import error.
func TestNoEmbeddingsWritesNoEmbeddingBlock(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	state := &configurator.AppState{DataPath: t.TempDir()}

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	content := readEnvFile(t, state)

	for _, key := range []string{envEmbeddingsProvider, envEmbeddingsBaseURL, envEmbeddingsAPIKey, envEmbeddingsModel} {
		_, present := envValue(t, content, key)
		require.False(t, present, "%s must be absent rather than defaulted", key)
	}
}

// TestEmbeddingsBlockLandsInBothInferenceShapes is the reason the block is
// rendered after the LLM arms return: the no-inference shape returns early, and
// an embedding endpoint resolved while chat stayed unset is a real state (the
// operator filled one Settings row and not the other).
func TestEmbeddingsBlockLandsInBothInferenceShapes(t *testing.T) {
	for name, state := range map[string]*configurator.AppState{
		"no inference":   embeddingsState(t.TempDir()),
		"with inference": bothState(t.TempDir()),
	} {
		c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})

		_, err := c.PreStart(context.Background(), state)
		require.NoError(t, err)
		content := readEnvFile(t, state)

		require.Equal(t, "bge-m3", mustEnv(t, content, envEmbeddingsModel), name)
	}
}

// TestAnEmbeddingsLineIsNeverEmpty is the same rule the inference block carries,
// for the same reason: the image builds an OpenAI SDK client for embeddings too,
// and that client refuses to construct with an empty key.
func TestAnEmbeddingsLineIsNeverEmpty(t *testing.T) {
	state := embeddingsState(t.TempDir())
	state.Integrations.Embeddings[0].APIKey = ""
	state.Integrations.Embeddings[0].Model = ""

	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	content := readEnvFile(t, state)

	require.Equal(t, noCredentialKey, mustEnv(t, content, envEmbeddingsAPIKey))
	_, hasModel := envValue(t, content, envEmbeddingsModel)
	require.False(t, hasModel, "an endpoint with no default model must not be given one")
}

func TestPickEmbeddingsIgnoresAnUnresolvedBinding(t *testing.T) {
	state := embeddingsState(t.TempDir())
	state.Integrations.Embeddings[0].Installed = false
	if _, ok := pickEmbeddings(state); ok {
		t.Fatal("a provider that is not installed is not an endpoint")
	}

	state = embeddingsState(t.TempDir())
	state.Integrations.Embeddings[0].Endpoint = "  "
	if _, ok := pickEmbeddings(state); ok {
		t.Fatal("a provider with no endpoint is not an endpoint")
	}

	if _, ok := pickEmbeddings(nil); ok {
		t.Fatal("no state is no endpoint")
	}
}

// bothState carries both endpoint contracts resolved to the same gateway, which
// is the common single-server case the Settings section offers as one click.
func bothState(dataDir string) *configurator.AppState {
	s := embeddingsState(dataDir)
	s.Integrations.Inference = inferenceState(dataDir).Integrations.Inference
	return s
}
