// SPDX-License-Identifier: AGPL-3.0-only

package podly

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

// llmStateFileName records what Bloud last applied to the app's llm section.
const llmStateFileName = "llm.state"

// noCredentialKey is sent as the API key when the instance's endpoint needs no
// credential.
//
// The app builds an OpenAI SDK client for both halves of its pipeline, and the
// SDK refuses to construct at all when the key is absent: `OpenAI(api_key=None)`
// raises "The api_key client option must be set". An empty string constructs,
// but the app's own LLM path collapses one to None (`llm.get("llm_api_key") or
// ...`), so empty is not a way out either. A keyless upstream therefore needs a
// non-empty value that is obviously not a secret, and the endpoint ignores it:
// measured against a keyless upstream, both /v1/audio/transcriptions and
// /v1/chat/completions answer 200 with `Authorization: Bearer placeholder`.
const noCredentialKey = "placeholder"

// orPlaceholder keeps a credential field non-empty, which is what the app's SDK
// requires, without inventing a secret: the value says what it is.
func orPlaceholder(key string) string {
	if strings.TrimSpace(key) == "" {
		return noCredentialKey
	}
	return key
}

// reconcileInference points the app's ad classifier at the model the instance
// declares, when it declares one.
//
// This is the half the transcription contract cannot cover: the app dials
// /v1/audio/transcriptions for the transcript and /v1/chat/completions to decide
// which spans of that transcript are advertising. Wiring only the first produces
// an install that transcribes every episode and cuts nothing, which looks like a
// working app that simply never removes an ad.
//
// As with transcription, no binding leaves the app alone rather than resetting it,
// and the write is digest-gated for the same reason: the config API masks the
// credential it stores, so a read cannot answer "is this already what I have?".
func (c *Configurator) reconcileInference(ctx context.Context, state *configurator.AppState) error {
	binding, ok := pickInference(state)
	if !ok {
		return nil
	}
	desired := llmSection{
		Model:   litellmModel(binding.DefaultModel),
		BaseURL: binding.Endpoint,
		APIKey:  orPlaceholder(binding.APIKey.String()),
	}
	if inferenceApplied(state, desired) {
		return nil
	}
	if _, err := c.api.setLLM(ctx, desired); err != nil {
		return fmt.Errorf("%s: setting the ad-classifier model: %w", appName, err)
	}
	if err := recordInferenceApplied(state, desired); err != nil {
		return err
	}

	c.logger.Info("configured the app's ad-classifier model",
		"endpoint", desired.BaseURL, "model", orEndpointDefault(desired.Model))
	return nil
}

// litellmModel puts a provider on the model name, because the app's ad classifier
// calls `litellm.completion()` rather than an SDK directly, and litellm routes on
// the prefix: `qwen3.8-flash-next` raises "LLM Provider NOT provided" client-side
// before a request is ever sent, while `openai/qwen3.8-flash-next` reaches the
// same endpoint and answers. Verified against the running image with both spellings
// against one OpenAI-compatible upstream.
//
// `openai/` is the right prefix for every Bloud inference provider: the contract's
// premise is an OpenAI-compatible API, which is exactly what that prefix selects.
// A name already carrying a slash is left alone, so an operator who wrote their own
// routing string (upstream's own default is `groq/openai/gpt-oss-120b`) keeps it.
func litellmModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" || strings.Contains(model, "/") {
		return model
	}
	return "openai/" + model
}

// pickInference returns the completion endpoint to dial. The first installed one
// wins, for the reason in pickTranscription.
func pickInference(state *configurator.AppState) (configurator.InferenceBinding, bool) {
	for _, b := range state.Integrations.Inference {
		if b.Installed && strings.TrimSpace(b.Endpoint) != "" {
			return b, true
		}
	}
	return configurator.InferenceBinding{}, false
}

func inferenceApplied(state *configurator.AppState, want llmSection) bool {
	raw, err := os.ReadFile(filepath.Join(state.DataPath, configDir, llmStateFileName))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(raw)) == llmDigest(want)
}

// recordInferenceApplied writes the state file only after the app accepted the
// settings, so a pass that applied nothing cannot claim it did.
func recordInferenceApplied(state *configurator.AppState, want llmSection) error {
	path := filepath.Join(state.DataPath, configDir, llmStateFileName)
	_, err := managedfile.Write(path, []byte(llmDigest(want)+"\n"), managedfile.ModeHostOnly)
	if err != nil {
		return fmt.Errorf("%s: write %s: %w", appName, llmStateFileName, err)
	}
	return nil
}

func llmDigest(w llmSection) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{w.Model, w.BaseURL, w.APIKey}, "\x00")))
	return hex.EncodeToString(sum[:])
}
