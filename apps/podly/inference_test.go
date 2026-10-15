// SPDX-License-Identifier: AGPL-3.0-only

package podly

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// inferenceState returns an app state carrying one inference endpoint, which is
// what the resolver produces when the operator has filled Settings -> AI.
func inferenceState(t *testing.T, endpoint, apiKey, model string) *configurator.AppState {
	t.Helper()
	state := testState(t)
	state.Integrations = bindings.Integrations{
		Inference: []bindings.InferenceBinding{{
			ProviderRef: bindings.ProviderRef{
				Kind:      bindings.ProviderKindSetting,
				App:       "setting",
				Installed: true,
			},
			Endpoint:     endpoint,
			APIKey:       bindings.Secret(apiKey),
			DefaultModel: model,
		}},
	}
	return state
}

// TestPostStartDeliversTheAdClassifierModel is the other half of the lite image's
// integration. Transcription produces a transcript; this model decides which
// spans of it are advertising, so an install with only whisper wired transcribes
// everything and cuts nothing.
func TestPostStartDeliversTheAdClassifierModel(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)
	state := inferenceState(t, "https://api.example.com/v1", "sk-llm-1", "gpt-4o")

	require.NoError(t, cfg.PostStart(context.Background(), state))

	got := fake.llmState()
	assert.Equal(t, "https://api.example.com/v1", got.baseURL)
	assert.Equal(t, "sk-llm-1", got.apiKey)
	assert.Equal(t, "openai/gpt-4o", got.model,
		"litellm routes on the provider prefix, so a bare name never reaches the endpoint")
}

// TestLitellmModelRouting pins the two cases the prefix rule has to get right: a
// bare name gains the provider, and a name that already carries one is left alone.
// Upstream's own default (`groq/openai/gpt-oss-120b`) is the second case, and
// double-prefixing it would send the app to a provider the operator did not pick.
func TestLitellmModelRouting(t *testing.T) {
	assert.Equal(t, "openai/qwen3.8-flash-next", litellmModel("qwen3.8-flash-next"))
	assert.Equal(t, "groq/openai/gpt-oss-120b", litellmModel("groq/openai/gpt-oss-120b"))
	assert.Equal(t, "", litellmModel(""))
}

// TestPostStartSendsAPlaceholderKeyWhenNoneIsNeeded covers the case a keyless
// upstream creates. The app builds an OpenAI SDK client for both halves of its
// pipeline and the SDK refuses to construct with no key at all, while the app's
// own LLM path turns an empty string into none. So a provider that needs no
// credential still has to be handed a non-empty value, and the endpoint ignores
// it.
func TestPostStartSendsAPlaceholderKeyWhenNoneIsNeeded(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)
	state := inferenceState(t, "https://api.example.com/v1", "", "qwen-x")
	state.Integrations.Transcription = []bindings.TranscriptionBinding{{
		ProviderRef: bindings.ProviderRef{
			Kind:      bindings.ProviderKindSetting,
			App:       "setting",
			Installed: true,
		},
		Endpoint: "https://api.example.com/v1",
		Model:    "whisper-large-v3-turbo",
	}}

	require.NoError(t, cfg.PostStart(context.Background(), state))

	assert.Equal(t, noCredentialKey, fake.llmState().apiKey,
		"the ad classifier needs a non-empty key even for a keyless endpoint")
	assert.Equal(t, noCredentialKey, fake.whisperState().apiKey,
		"and so does the transcriber, for the same SDK reason")
}

// TestPostStartLeavesTheAdClassifierAloneWithNoInference: no declared model means
// no write, so an operator who set one inside the app keeps it.
func TestPostStartLeavesTheAdClassifierAloneWithNoInference(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)

	require.NoError(t, cfg.PostStart(context.Background(), testState(t)))

	assert.Equal(t, fakeLLM{}, fake.llmState())
	assert.Equal(t, 0, fake.count(http.MethodPut, configPath))
}

// TestPostStartAdClassifierSteadyState: the resync runs PostStart on every pass,
// so a pass where nothing changed must write nothing.
func TestPostStartAdClassifierSteadyState(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)
	state := inferenceState(t, "https://api.example.com/v1", "sk-llm-1", "gpt-4o")

	require.NoError(t, cfg.PostStart(context.Background(), state))
	require.NoError(t, cfg.PostStart(context.Background(), state))

	assert.Equal(t, 1, fake.count(http.MethodPut, configPath),
		"a steady-state pass must not rewrite the app's config")
}

// TestPostStartAdClassifierReappliesOnModelChange: the operator changing the
// instance's default model is a change Bloud has to carry to the app.
func TestPostStartAdClassifierReappliesOnModelChange(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)
	require.NoError(t, cfg.PostStart(context.Background(),
		inferenceState(t, "https://api.example.com/v1", "sk-llm-1", "gpt-4o")))

	require.NoError(t, cfg.PostStart(context.Background(),
		inferenceState(t, "https://api.example.com/v1", "sk-llm-1", "gpt-5")))

	got := fake.llmState()
	assert.Equal(t, "openai/gpt-5", got.model)
	assert.Equal(t, 2, fake.count(http.MethodPut, configPath))
}

// TestPostStartAdClassifierDoesNotClobberTranscription: the two halves share one
// config route, so writing one must not reset the other.
func TestPostStartAdClassifierDoesNotClobberTranscription(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)
	state := inferenceState(t, "https://api.example.com/v1", "sk-llm-1", "gpt-4o")
	state.Integrations.Transcription = []bindings.TranscriptionBinding{{
		ProviderRef: bindings.ProviderRef{
			Kind:      bindings.ProviderKindSetting,
			App:       "setting",
			Installed: true,
		},
		Endpoint: "https://w.example.com/v1",
		Model:    "whisper-1",
	}}

	require.NoError(t, cfg.PostStart(context.Background(), state))

	assert.Equal(t, "https://w.example.com/v1", fake.whisperState().baseURL)
	assert.Equal(t, "https://api.example.com/v1", fake.llmState().baseURL)
}
