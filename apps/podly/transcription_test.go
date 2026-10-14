// SPDX-License-Identifier: AGPL-3.0-only

package podly

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// transcriptionState returns an app state whose integration bindings carry one
// transcription endpoint, which is what the resolver produces when the operator
// has filled Settings -> Transcription.
func transcriptionState(t *testing.T, endpoint, apiKey, model string) *configurator.AppState {
	t.Helper()
	state := testState(t)
	state.Integrations = configurator.Integrations{
		Transcription: []configurator.TranscriptionBinding{{
			ProviderRef: configurator.ProviderRef{
				Kind:      configurator.ProviderKindSetting,
				App:       "setting",
				Installed: true,
			},
			Endpoint: endpoint,
			APIKey:   configurator.Secret(apiKey),
			Model:    model,
		}},
	}
	return state
}

// TestPostStartDeliversTheTranscriptionEndpoint is the lite image's half of the
// integration: the app has no local model, so the endpoint the instance declares
// has to reach the app's own config store, and it has to arrive as the app's
// remote-whisper section rather than as something only Bloud remembers.
func TestPostStartDeliversTheTranscriptionEndpoint(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)
	state := transcriptionState(t, "https://api.example.com/v1", "sk-test-1", "whisper-1")

	require.NoError(t, cfg.PostStart(context.Background(), state))

	got := fake.whisperState()
	assert.Equal(t, "remote", got.whisperType)
	assert.Equal(t, "https://api.example.com/v1", got.baseURL)
	assert.Equal(t, "sk-test-1", got.apiKey)
	assert.Equal(t, "whisper-1", got.model)
	assert.Equal(t, 1, fake.count(http.MethodPut, configPath))
}

// TestPostStartLeavesTranscriptionAloneWhenNothingIsDeclared covers the
// `required: false` promise: with no endpoint wired the app is not touched, so an
// operator who configured the app directly keeps whatever they set and the node
// still converges.
func TestPostStartLeavesTranscriptionAloneWhenNothingIsDeclared(t *testing.T) {
	fake := newFakePodly()
	fake.setWhisper(fakeWhisper{whisperType: "groq", model: "whisper-large-v3-turbo"})
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)
	state := testState(t)

	require.NoError(t, cfg.PostStart(context.Background(), state))

	assert.Zero(t, fake.count(http.MethodPut, configPath),
		"with nothing wired Bloud has no opinion to write")
	assert.Equal(t, "groq", fake.whisperState().whisperType,
		"the operator's own setting must survive a pass that has no binding")
}

// TestSteadyStatePostStartRewritesNoTranscription is the resync contract for the
// config API. The pass runs every minute forever; a write each time would rewrite
// the app's config store and reset its processor on a loop.
func TestSteadyStatePostStartRewritesNoTranscription(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)
	state := transcriptionState(t, "https://api.example.com/v1", "sk-test-1", "whisper-1")

	require.NoError(t, cfg.PostStart(context.Background(), state))
	require.Equal(t, 1, fake.count(http.MethodPut, configPath))

	for i := 0; i < 3; i++ {
		fake.reset()
		require.NoError(t, cfg.PostStart(context.Background(), state))
		assert.Zero(t, fake.count(http.MethodPut, configPath), "pass %d rewrote a config nobody changed", i)
	}
}

// TestPostStartReappliesAChangedTranscriptionEndpoint is what makes the settings
// change real: the orchestrator resets the consumer, the pass runs, and the new
// endpoint lands in the app.
func TestPostStartReappliesAChangedTranscriptionEndpoint(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)
	state := transcriptionState(t, "https://api.example.com/v1", "sk-test-1", "whisper-1")

	require.NoError(t, cfg.PostStart(context.Background(), state))

	state.Integrations.Transcription[0].Model = "whisper-large-v3-turbo"
	fake.reset()
	require.NoError(t, cfg.PostStart(context.Background(), state))

	assert.Equal(t, "whisper-large-v3-turbo", fake.whisperState().model)
	assert.Equal(t, 1, fake.count(http.MethodPut, configPath), "only the changed pass writes")
}

// TestPostStartReappliesA rotatedKey is the same rule applied to the credential.
// A rotation is invisible in the endpoint and the model, so a digest that covered
// only those would leave the app dialing with a retired key forever.
func TestPostStartReappliesARotatedKey(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)
	state := transcriptionState(t, "https://api.example.com/v1", "sk-old", "whisper-1")

	require.NoError(t, cfg.PostStart(context.Background(), state))

	state.Integrations.Transcription[0].APIKey = "sk-new"
	require.NoError(t, cfg.PostStart(context.Background(), state))

	assert.Equal(t, "sk-new", fake.whisperState().apiKey)
}

// TestPostStartLeavesAnOperatorEditOfTranscriptionAlone is the reason the write
// is gated on what Bloud applied rather than on what the app holds. The app's
// config API masks the credential, so Bloud cannot tell an operator's edit from
// its own; what it can tell is that it has already delivered this exact value,
// and re-delivering it would be Bloud overwriting a human on a timer.
func TestPostStartLeavesAnOperatorEditOfTranscriptionAlone(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)
	state := transcriptionState(t, "https://api.example.com/v1", "sk-test-1", "whisper-1")

	require.NoError(t, cfg.PostStart(context.Background(), state))

	fake.setWhisper(fakeWhisper{whisperType: "remote", baseURL: "https://other.example/v1", apiKey: "sk-theirs"})
	fake.reset()
	require.NoError(t, cfg.PostStart(context.Background(), state))

	assert.Equal(t, "https://other.example/v1", fake.whisperState().baseURL,
		"a human edit has to outlive the pass that follows it")
	assert.Zero(t, fake.count(http.MethodPut, configPath))
}

// TestPostStartLeavesUntouchedWhisperFieldsAlone: the app merges the keys a
// payload carries, and Bloud carries an opinion about three of them. Language,
// timeout and chunk size belong to the person listening.
func TestPostStartLeavesUntouchedWhisperFieldsAlone(t *testing.T) {
	fake := newFakePodly()
	fake.setWhisper(fakeWhisper{whisperType: "remote", language: "de"})
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)
	state := transcriptionState(t, "https://api.example.com/v1", "sk-test-1", "")

	require.NoError(t, cfg.PostStart(context.Background(), state))

	assert.Equal(t, "de", fake.whisperState().language,
		"Bloud wires the endpoint; the listener's language is not Bloud's to set")
}

// TestPostStartSkipsTranscriptionWithoutAnAdminSession: every path that warns
// instead of signing in has to stop the config write too, because the config API
// is admin-gated and a 401 there would park a running app in ERROR.
func TestPostStartSkipsTranscriptionWithoutAnAdminSession(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	secrets := newStoreSecrets()
	cfg := newAppConfigurator(t, secrets, srv)
	state := transcriptionState(t, "https://api.example.com/v1", "sk-test-1", "whisper-1")

	// Stand in for the password having been changed inside the app: Bloud holds
	// one value, the app now accepts another, and no admin call can follow.
	_, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)
	fake.removeUser(serviceUser)

	require.NoError(t, cfg.PostStart(context.Background(), state))
	assert.Zero(t, fake.count(http.MethodPut, configPath))
}
