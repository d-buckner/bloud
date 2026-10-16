// SPDX-License-Identifier: AGPL-3.0-only

package hindsight

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// The environment variable names the local-model tuning writes. They live here
// so the test and the catalog name the same keys, and a rename in one without
// the other is the failure this test catches.
const (
	envLLMMaxConcurrent              = "HINDSIGHT_API_LLM_MAX_CONCURRENT"
	envRetainLLMMaxConcurrent        = "HINDSIGHT_API_RETAIN_LLM_MAX_CONCURRENT"
	envConsolidationLLMMaxConcurrent = "HINDSIGHT_API_CONSOLIDATION_LLM_MAX_CONCURRENT"
	envMentalModelLLMMaxConcurrent   = "HINDSIGHT_API_MENTAL_MODEL_REFRESH_LLM_MAX_CONCURRENT"
	envConsolidationLLMParallelism   = "HINDSIGHT_API_CONSOLIDATION_LLM_PARALLELISM"
	envWorkerMaxSlots                = "HINDSIGHT_API_WORKER_MAX_SLOTS"
	envEmbeddingsMaxConcurrent       = "HINDSIGHT_API_EMBEDDINGS_MAX_CONCURRENT_REQUESTS"
	envLLMTimeout                    = "HINDSIGHT_API_LLM_TIMEOUT"
	envReflectLLMTimeout             = "HINDSIGHT_API_REFLECT_LLM_TIMEOUT"
	envRetainMaxCompletionTokens     = "HINDSIGHT_API_RETAIN_MAX_COMPLETION_TOKENS"
	envRetainReasoningEffort         = "HINDSIGHT_API_RETAIN_LLM_REASONING_EFFORT"
	envConsolidationReasoningEffort  = "HINDSIGHT_API_CONSOLIDATION_LLM_REASONING_EFFORT"
	envEnableObservations            = "HINDSIGHT_API_ENABLE_OBSERVATIONS"
	envEnableAutoConsolidation       = "HINDSIGHT_API_ENABLE_AUTO_CONSOLIDATION"

	// The call-volume levers. These remove calls instead of bounding how many
	// run at once.
	envConsolidationDedupThreshold    = "HINDSIGHT_API_CONSOLIDATION_DEDUP_THRESHOLD"
	envConsolidationReconcileInterval = "HINDSIGHT_API_CONSOLIDATION_RECONCILE_INTERVAL_SECONDS"
	envMentalModelRefreshTick         = "HINDSIGHT_API_MENTAL_MODEL_REFRESH_TICK_SECONDS"
	envMentalModelMinRefreshInterval  = "HINDSIGHT_API_MENTAL_MODEL_MIN_REFRESH_INTERVAL_SECONDS"
	envRetainOptionalFactDimensions   = "HINDSIGHT_API_RETAIN_OPTIONAL_FACT_DIMENSIONS"
)

// imageLLMMaxConcurrentDefault is the pinned image's own default for the global
// LLM cap (config.py DEFAULT_LLM_MAX_CONCURRENT). It is a value for a hosted
// provider, and the local-model tuning exists to stay well below it.
const imageLLMMaxConcurrentDefault = 32

// imageRetainChunkSizeDefault is config.py DEFAULT_RETAIN_CHUNK_SIZE, the floor
// the image enforces on the retain completion cap at startup. The comparison
// runs in characters, and the image refuses a completion cap at or below it.
const imageRetainChunkSizeDefault = 3000

// imageConsolidationSlotReservation is config.py WORKER_SLOT_TYPE_DEFAULTS
// ["consolidation"]. The image refuses to start when the reservations together
// exceed HINDSIGHT_API_WORKER_MAX_SLOTS, so the worker ceiling must cover it.
const imageConsolidationSlotReservation = 2

// imageLLMTimeoutDefault is config.py DEFAULT_LLM_TIMEOUT in seconds.
const imageLLMTimeoutDefault = 120

// serverEnv returns the memory server container's declared environment. It is
// the tuning surface, and it is read from the same metadata.yaml the catalog
// loads, so the test cannot agree with a copy that the runtime does not use.
func serverEnv(t *testing.T) map[string]string {
	t.Helper()
	return container(t, loadMetadata(t), nodeName).Environment
}

// envInt reads one integer value, and fails when the key is absent or is not a
// number. An absent key is the real failure mode: the image then uses a default
// tuned for a hosted provider.
func envInt(t *testing.T, env map[string]string, key string) int {
	t.Helper()
	raw, ok := env[key]
	require.Truef(t, ok, "%s must be declared on %s", key, nodeName)
	n, err := strconv.Atoi(raw)
	require.NoErrorf(t, err, "%s must be an integer, got %q", key, raw)
	return n
}

// TestBackgroundLLMCapsStayBelowTheGlobalCeiling is the shape the shared-endpoint
// tuning exists for. Every background bucket caps at 1, and the global ceiling is
// small, so no single bucket can take the whole ceiling and the agent that calls
// reflect queues behind at most a few Hindsight calls.
func TestBackgroundLLMCapsStayBelowTheGlobalCeiling(t *testing.T) {
	env := serverEnv(t)

	global := envInt(t, env, envLLMMaxConcurrent)
	require.GreaterOrEqual(t, global, 1)
	require.LessOrEqual(t, global, 2,
		"a shared local endpoint must not be filled with Hindsight calls")
	require.Less(t, global, imageLLMMaxConcurrentDefault,
		"the global cap must stay below the image's hosted-provider default")

	for _, key := range []string{
		envRetainLLMMaxConcurrent,
		envConsolidationLLMMaxConcurrent,
		envMentalModelLLMMaxConcurrent,
	} {
		cap := envInt(t, env, key)
		require.Equal(t, 1, cap, "%s must take one slot", key)
		require.Less(t, cap, global, "%s must not take the whole ceiling", key)
	}

	require.Equal(t, 1, envInt(t, env, envConsolidationLLMParallelism),
		"consolidation must work one tag group at a time on a local endpoint")
}

// TestRetainCompletionCapClearsTheChunkSizeFloor pins the startup check the
// image applies: validate_retain_completion_token_budget refuses to start when
// the completion cap is not greater than the chunk size.
func TestRetainCompletionCapClearsTheChunkSizeFloor(t *testing.T) {
	cap := envInt(t, serverEnv(t), envRetainMaxCompletionTokens)
	require.Greater(t, cap, imageRetainChunkSizeDefault,
		"the image refuses to start when the retain completion cap does not exceed the chunk size")
}

// TestWorkerSlotsCoverTheConsolidationReservation pins the other startup check:
// the per-operation reservations must fit inside the worker ceiling. The image
// reserves two slots for consolidation by default.
func TestWorkerSlotsCoverTheConsolidationReservation(t *testing.T) {
	require.GreaterOrEqual(t, envInt(t, serverEnv(t), envWorkerMaxSlots),
		imageConsolidationSlotReservation,
		"the image refuses to start when the reservations exceed the worker ceiling")
}

// TestLocalModelDeadlineIsRaisedAndReflectStaysShorter records the two timeouts
// and their order. A local model loads and generates slowly, so the global
// deadline grows. Reflect answers a waiting caller, so it keeps an explicit
// shorter deadline rather than inheriting the longer one.
func TestLocalModelDeadlineIsRaisedAndReflectStaysShorter(t *testing.T) {
	env := serverEnv(t)

	global := envInt(t, env, envLLMTimeout)
	reflect := envInt(t, env, envReflectLLMTimeout)

	require.Greater(t, global, imageLLMTimeoutDefault,
		"the deadline must grow past the image default for a slow local model")
	require.Less(t, reflect, global,
		"reflect must keep a shorter explicit deadline than the global one")
}

// TestEmbeddingsConcurrencyStaysBelowTheImageDefault covers the second endpoint
// this app dials. The embedding route commonly shares one box with chat, and the
// image's default is eight concurrent requests.
func TestEmbeddingsConcurrencyStaysBelowTheImageDefault(t *testing.T) {
	require.Less(t, envInt(t, serverEnv(t), envEmbeddingsMaxConcurrent), 8)
}

// TestReasoningEffortIsLowOnBackgroundWork sets the level on the two structured
// paths that have nothing to reason about. Interactive reflect is deliberately
// left at the model default.
func TestReasoningEffortIsLowOnBackgroundWork(t *testing.T) {
	env := serverEnv(t)

	require.Equal(t, "low", env[envRetainReasoningEffort])
	require.Equal(t, "low", env[envConsolidationReasoningEffort])

	_, reflectSet := env["HINDSIGHT_API_REFLECT_LLM_REASONING_EFFORT"]
	require.False(t, reflectSet, "interactive reflect must keep the model's own reasoning level")
}

// TestBackgroundCallVolumeIsReduced covers the levers that remove calls rather
// than bound parallelism. The concurrency caps cannot lower the number of LLM
// calls, so these are the only values that make the app less inference heavy.
func TestBackgroundCallVolumeIsReduced(t *testing.T) {
	env := serverEnv(t)

	// 1.0 is the value that turns the per-duplicate merge pass off.
	// consolidator.py skips the pass when the threshold reaches 1.0.
	require.Equal(t, "1.0", env[envConsolidationDedupThreshold])

	// The image's maintenance timers default to 300s and run with no user
	// activity. Both are raised well past that.
	require.Greater(t, envInt(t, env, envConsolidationReconcileInterval), 300)
	require.Greater(t, envInt(t, env, envMentalModelRefreshTick), 300)

	// The image's default of 0 refreshes a mental model on every automatic
	// trigger. Any positive floor reduces that.
	require.Greater(t, envInt(t, env, envMentalModelMinRefreshInterval), 0)

	// The image recommends this for a small model under strict structured
	// output. It removes the JSON-repair re-prompt, a hidden extra call.
	require.Equal(t, "true", env[envRetainOptionalFactDimensions])
}

// TestBackgroundFeaturesStayEnabled records the choice the tuning makes: the
// caps bound observations and auto-consolidation, they do not remove them. The
// values are written down rather than inherited, so a future upstream default
// change cannot turn the features off silently.
func TestBackgroundFeaturesStayEnabled(t *testing.T) {
	env := serverEnv(t)

	require.Equal(t, "true", env[envEnableObservations])
	require.Equal(t, "true", env[envEnableAutoConsolidation])
}
