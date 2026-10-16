// SPDX-License-Identifier: AGPL-3.0-only

// Package hindsight configures Hindsight, an agent memory server, as a Bloud app.
//
// The app is three containers, and so three graph nodes. Two of them have a
// configurator:
//
//   - apps-hindsight, the memory server. Its PreStart writes the env file the
//     container is created from, because the image takes its whole configuration
//     through process environment and nothing else, and four of the values it
//     needs are generated secrets or a resolved binding rather than static
//     metadata.
//   - apps-hindsight-gate, the nginx front that holds the app's only published
//     port and branches it between the server's two surfaces. Its PreStart writes
//     the nginx config, and its PostStart is where the composed memory API is
//     proven, because the gate is the last node to come up and the only address
//     anything outside this app can dial.
//
// apps-hindsight-postgres needs no configurator: it is a declared container with
// a declared health check, and the orchestrator drives a node with none.
//
// The two nodes share two credentials, both minted here: the bearer the memory
// API authenticates with, and the key the web UI demands at its sign-in.
// See INTEGRATION.md for what each one is the boundary for.
package hindsight

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
)

const (
	appName = "hindsight"

	// nodeName is the memory server container; gateNodeName is the front that
	// publishes the app's port. Both are graph nodes, and each has its own
	// configurator: the graph sees containers, not apps.
	nodeName     = "apps-hindsight"
	gateNodeName = "apps-hindsight-gate"

	defaultPort = 9999

	// apiPort is the port the memory API listens on inside the server container.
	// The gate routes the API paths here and the Control Plane everything else, so
	// the number is a fact both this package and metadata.yaml need.
	apiPort = 8888

	// configDir is the subdirectory of the app data tree holding everything this
	// app generates. Both containers read out of it, and neither mounts the env
	// file: the runtime reads that on the host and passes the values in at create
	// time.
	configDir   = "config"
	envFileName = "env"

	// The environment variable names the image reads. They are constants so the
	// renderer and the tests name the same keys upstream expects.
	envTenantExtension = "HINDSIGHT_API_TENANT_EXTENSION"
	envTenantAPIKey    = "HINDSIGHT_API_TENANT_API_KEY"
	envCPAccessKey     = "HINDSIGHT_CP_ACCESS_KEY"
	envCPDataplaneKey  = "HINDSIGHT_CP_DATAPLANE_API_KEY"
	envLLMProvider     = "HINDSIGHT_API_LLM_PROVIDER"
	envLLMBaseURL      = "HINDSIGHT_API_LLM_BASE_URL"
	envLLMAPIKey       = "HINDSIGHT_API_LLM_API_KEY"
	envLLMModel        = "HINDSIGHT_API_LLM_MODEL"
	// The embedding block is a separate provider from the LLM block, with its own
	// four variables, because the image treats them as two independent choices and
	// the `embeddings` contract is a different promise from `inference`.
	envEmbeddingsProvider = "HINDSIGHT_API_EMBEDDINGS_PROVIDER"
	envEmbeddingsBaseURL  = "HINDSIGHT_API_EMBEDDINGS_OPENAI_BASE_URL"
	envEmbeddingsAPIKey   = "HINDSIGHT_API_EMBEDDINGS_OPENAI_API_KEY"
	envEmbeddingsModel    = "HINDSIGHT_API_EMBEDDINGS_OPENAI_MODEL"

	// tenantExtensionAPIKey is the dotted path to the image's built-in
	// authentication hook. Setting it is what makes every API request carry a
	// bearer; leaving it unset is upstream's documented "no authentication".
	tenantExtensionAPIKey = "hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension"

	// llmProviderOpenAI is the provider name for any OpenAI-compatible endpoint,
	// which is exactly what the `inference` contract hands over.
	llmProviderOpenAI = "openai"

	// llmProviderNone is the image's supported no-model mode: the server starts,
	// retain stores the text as a chunk, and semantic recall still works. Fact
	// extraction, reflect, and consolidation raise a clear error instead of a
	// connection failure. It is the state Bloud selects when the operator has not
	// filled in an AI endpoint, because an install must not sit in ERROR waiting
	// on a credential only they can supply.
	llmProviderNone = "none"

	// httpTokenKey is the secret name the `memory` contract carries: the bearer a
	// consumer presents and the API checks. The image's own env var for it is
	// HINDSIGHT_API_TENANT_API_KEY, which is a fact of the app rather than of the
	// contract, so the two names stay separate.
	httpTokenKey = "httpToken"

	// passwordKey is the secret name the `clientPassword` contract carries: the
	// key a person types into the web UI.
	passwordKey = "password"
)

// Configurator drives the memory server container: write the env file it is
// created from, and nothing else. Readiness is the gate's to prove, because this
// container publishes no port this process can dial.
type Configurator struct {
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
}

// NewConfigurator builds the server node's configurator.
func NewConfigurator(_ int, deps configurator.Deps) *Configurator {
	return &Configurator{
		secrets: deps.Secrets,
		logger:  configurator.AppLogger(deps.Logger, appName).With("node", nodeName),
	}
}

// Name returns the graph node this configurator owns.
func (c *Configurator) Name() string { return nodeName }

// PreStart writes the env file the container spec declares as its envFile. It
// carries the four values `environment:` cannot render: the two credentials this
// app mints, and the LLM endpoint the `inference` binding resolved to.
//
// A credential that does not exist yet is written as absent rather than as an
// empty value, so "not ready" stays distinguishable from "configured with
// nothing". The gate refuses every request while either is missing, so an absent
// credential leaves the app unreachable rather than open.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	bearer, accessKey, err := ensureCredentials(c.secrets, c.logger)
	if err != nil {
		return configurator.NoRestart(), err
	}
	inference, hasInference := pickInference(state)
	embeds, hasEmbeddings := pickEmbeddings(state)
	content := renderEnvFile(bearer, accessKey, inference, hasInference, embeds, hasEmbeddings)
	if !hasEmbeddings {
		// The catalog blocks the install until the operator fills this row, so
		// reaching here means the setting was removed after the install. Say so:
		// the server fails at startup with its own message about a missing
		// embedding provider, and that message is easier to read next to a log
		// line that names the cause.
		c.logger.Warn("hindsight has no embeddings endpoint configured; "+
			"the slim image cannot embed a memory without one",
			"set", "Settings \u2192 AI \u2192 Embeddings")
	}

	envPath := filepath.Join(state.DataPath, configDir, envFileName)
	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		return configurator.NoRestart(), fmt.Errorf("creating the %s config directory: %w", appName, err)
	}
	// ModeHostOnly, not ModeSharedConfig: this file holds two live credentials and
	// nothing outside this host reads it. The runtime reads it at create time and
	// passes the values into the container's environment, so the file is never
	// mounted and no container process ever needs it. The gate's nginx.conf is the
	// opposite case and is world-readable for that reason.
	result, err := configurator.WriteConfig(envPath, []byte(content), managedfile.ModeHostOnly, "hindsight config rewritten")
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing the %s config: %w", appName, err)
	}
	if result.RestartNeeded {
		c.logger.Info("wrote hindsight config", "path", envPath,
			"api_bearer", bearer != "", "access_key", accessKey != "", "llm", llmProviderOf(hasInference))
	}
	return result, nil
}

// llmProviderOf names the provider the renderer will write for a binding state, so
// the log line says what the file says rather than what the caller hoped for.
func llmProviderOf(hasInference bool) string {
	if hasInference {
		return llmProviderOpenAI
	}
	return llmProviderNone
}

// PostStart does nothing on this node. The container publishes no port, so there
// is nothing this process can reach: the only address into this app is the
// gate's, and the gate is a dependent node that has not started yet when this
// phase runs. apps-hindsight-gate's PostStart proves the endpoint once both
// exist.
func (c *Configurator) PostStart(_ context.Context, _ *configurator.AppState) error {
	return nil
}

// --- credentials ---

// ensureCredentials returns the bearer memory consumers present and the key the web
// UI signs in with, generating and publishing either on first use.
//
// Both containers need both values, and either node's PreStart may run first, so
// this is idempotent by construction: read, and mint only what is missing.
// Without a secrets provider nothing can be stored, and the caller renders the
// "not ready" shape rather than inventing a value that would change on every
// pass.
func ensureCredentials(secrets configurator.AppSecretsProvider, logger *slog.Logger) (bearer, accessKey string, err error) {
	if secrets == nil {
		return "", "", nil
	}
	bearer, minted, err := configurator.EnsureSecret(secrets, appName, httpTokenKey, 32, "memory API bearer")
	if err != nil {
		return "", "", err
	}
	if minted {
		logger.Info("generated the hindsight memory API bearer")
	}
	accessKey, minted, err = configurator.EnsureSecret(secrets, appName, passwordKey, 32, "access key")
	if err != nil {
		return bearer, "", err
	}
	if minted {
		logger.Info("generated the hindsight access key")
	}
	return bearer, accessKey, nil
}

// --- env file rendering ---

// renderEnvFile builds the container's environment from the two credentials and
// the two endpoint bindings, if any.
//
// The LLM block is always present, because the image has a default provider
// (OpenAI with a hosted key) that a Bloud install must never fall through to: an
// install with no endpoint configured has to say so in its own configuration
// rather than dial a provider nobody chose. That is what `none` is for.
//
// The embedding block is omitted rather than defaulted when no endpoint resolved.
// The image's own default for it is a local provider the slim image cannot run, so
// there is no safe value to write, and the app's startup failure names the
// missing endpoint more precisely than any placeholder Bloud could invent.
//
// The credential lines are omitted when the credential is absent. Writing an
// empty `HINDSIGHT_API_TENANT_API_KEY=` would leave the extension loaded with a
// key nothing can match, which is a different failure than the one the gate is
// already reporting, and a harder one to read.
func renderEnvFile(
	bearer, accessKey string,
	inference bindings.InferenceBinding, hasInference bool,
	embeds bindings.EmbeddingsBinding, hasEmbeddings bool,
) string {
	var b strings.Builder
	b.WriteString("# Generated by Bloud; rewritten on every reconciliation.\n")
	if bearer != "" {
		b.WriteString(envTenantExtension + "=" + tenantExtensionAPIKey + "\n")
		b.WriteString(envTenantAPIKey + "=" + bearer + "\n")
		b.WriteString(envCPDataplaneKey + "=" + bearer + "\n")
	}
	if accessKey != "" {
		b.WriteString(envCPAccessKey + "=" + accessKey + "\n")
	}
	if hasInference {
		b.WriteString(envLLMProvider + "=" + llmProviderOpenAI + "\n")
		b.WriteString(envLLMBaseURL + "=" + inference.Endpoint + "\n")
		b.WriteString(envLLMAPIKey + "=" + orPlaceholder(inference.APIKey.String()) + "\n")
		if model := strings.TrimSpace(inference.DefaultModel); model != "" {
			b.WriteString(envLLMModel + "=" + model + "\n")
		}
	} else {
		b.WriteString(envLLMProvider + "=" + llmProviderNone + "\n")
	}
	renderEmbeddings(&b, embeds, hasEmbeddings)
	return b.String()
}

// renderEmbeddings writes the embedding provider block. It is a separate
// function because the LLM arms above return early, and the embedding block has
// to land in both of those shapes.
func renderEmbeddings(b *strings.Builder, embeds bindings.EmbeddingsBinding, has bool) {
	if !has {
		return
	}
	b.WriteString(envEmbeddingsProvider + "=" + llmProviderOpenAI + "\n")
	b.WriteString(envEmbeddingsBaseURL + "=" + embeds.Endpoint + "\n")
	b.WriteString(envEmbeddingsAPIKey + "=" + orPlaceholder(embeds.APIKey.String()) + "\n")
	if model := strings.TrimSpace(embeds.Model); model != "" {
		b.WriteString(envEmbeddingsModel + "=" + model + "\n")
	}
}

// noCredentialKey is what the LLM key line carries when the instance's endpoint
// needs no credential.
//
// The image builds an OpenAI SDK client, and that client refuses to construct at
// all when no key was given. An empty string is not a way out either: the app
// collapses one to "no key" on the way through. A keyless upstream therefore
// needs a non-empty value that is obviously not a secret, and the endpoint
// ignores it. apps/podly hit the same wall from the other direction and settled
// on the same answer for the same reason.
const noCredentialKey = "placeholder"

// orPlaceholder keeps the credential field non-empty without inventing a secret:
// the value says what it is.
func orPlaceholder(key string) string {
	if strings.TrimSpace(key) == "" {
		return noCredentialKey
	}
	return key
}

// pickEmbeddings returns the instance's embedding endpoint, if one is declared.
// The contract is satisfied by a Settings row rather than by an app, so at most
// one binding ever arrives; taking the first installed one keeps that a detail.
func pickEmbeddings(state *configurator.AppState) (bindings.EmbeddingsBinding, bool) {
	if state == nil {
		return bindings.EmbeddingsBinding{}, false
	}
	for _, b := range state.Integrations.Embeddings {
		if b.Installed && strings.TrimSpace(b.Endpoint) != "" {
			return b, true
		}
	}
	return bindings.EmbeddingsBinding{}, false
}

// pickInference returns the instance's inference endpoint, if it declared one.
// The contract is satisfied by a Settings row rather than by an app, so at most
// one binding ever arrives; taking the first installed one keeps that a detail.
func pickInference(state *configurator.AppState) (bindings.InferenceBinding, bool) {
	if state == nil {
		return bindings.InferenceBinding{}, false
	}
	for _, b := range state.Integrations.Inference {
		if b.Installed && strings.TrimSpace(b.Endpoint) != "" {
			return b, true
		}
	}
	return bindings.InferenceBinding{}, false
}
