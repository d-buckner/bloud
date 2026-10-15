// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/embeddings"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/transcription"
)

// The embeddings contract shares the apply path and the binder with
// transcription, so these tests assert the same five behaviours through the
// embeddings arm. A shared implementation needs a test on each of its two
// instances: a spec field wired to the wrong contract name passes one of them
// and fails the other, and passing only one is exactly the drift the shared code
// is supposed to prevent.

func putEmbeddingsRecord(t *testing.T, ext store.ExternalAppStoreInterface, p embeddings.Provider) {
	t.Helper()
	require.NoError(t, ext.Upsert(embeddings.ToExternal(p)))
}

func countEmbeddingsRecords(t *testing.T, orch *Orchestrator) int {
	t.Helper()
	records, err := orch.externalApps.FindAllBySource(store.ExternalAppSourceForContract(embeddings.ContractName))
	require.NoError(t, err)
	return len(records)
}

func TestBuildIntegrations_EmbeddingsFromTheInstanceSetting(t *testing.T) {
	appStore := NewFakeAppStore()
	install(t, appStore, "hindsight", nil)

	consumer := settingConsumerApp("hindsight", "embeddings", requires("apiKey"))
	orch, secrets := bindingsOrchestrator(t, appStore, consumer)
	putEmbeddingsRecord(t, orch.externalApps, embeddings.Provider{
		ID: embeddings.DefaultID, Name: "Embeddings",
		BaseURL: "https://api.example.com/v1", Model: "bge-m3",
	})
	secrets.publish(store.ExternalSecretScope(embeddings.DefaultID), "embeddings", "sk-embed")

	bindings := orch.buildIntegrations("hindsight", consumer).Embeddings
	require.Len(t, bindings, 1)
	assert.Equal(t, "https://api.example.com/v1", bindings[0].Endpoint)
	assert.Equal(t, "sk-embed", bindings[0].APIKey.String())
	assert.Equal(t, "bge-m3", bindings[0].Model)
	assert.True(t, bindings[0].Installed)
	assert.Equal(t, embeddings.DefaultID, bindings[0].App)
}

func TestBuildIntegrations_EmbeddingsSecretOnlyWhenRequired(t *testing.T) {
	appStore := NewFakeAppStore()
	install(t, appStore, "hindsight", nil)

	consumer := settingConsumerApp("hindsight", "embeddings", nil)
	orch, secrets := bindingsOrchestrator(t, appStore, consumer)
	putEmbeddingsRecord(t, orch.externalApps, embeddings.Provider{
		ID: embeddings.DefaultID, BaseURL: "https://api.example.com/v1",
	})
	secrets.publish(store.ExternalSecretScope(embeddings.DefaultID), "embeddings", "sk-embed")

	bindings := orch.buildIntegrations("hindsight", consumer).Embeddings
	require.Len(t, bindings, 1)
	assert.Equal(t, "https://api.example.com/v1", bindings[0].Endpoint)
	assert.Empty(t, bindings[0].APIKey, "a consumer that did not declare the secret must not be handed it")
}

func TestBuildIntegrations_NoEmbeddingsRecordBindsNothing(t *testing.T) {
	appStore := NewFakeAppStore()
	install(t, appStore, "hindsight", nil)

	consumer := settingConsumerApp("hindsight", "embeddings", requires("apiKey"))
	orch, _ := bindingsOrchestrator(t, appStore, consumer)

	assert.Empty(t, orch.buildIntegrations("hindsight", consumer).Embeddings,
		"nothing configured reads to the consumer as no provider, not as a broken one")
}

func TestApplySetEmbeddingsIntent_StoresRecordAndCredential(t *testing.T) {
	orch, secrets := applyOrchestrator(t)

	orch.applySetEmbeddingsIntent(NewSetEmbeddingsIntent(
		"https://api.example.com/v1/", "Ollama", "bge-m3", strPtr("sk-key")))

	records, err := orch.externalApps.FindAllBySource(store.ExternalAppSourceForContract(embeddings.ContractName))
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "https://api.example.com/v1", records[0].URL,
		"the stored value is the canonical endpoint, not what was typed")
	assert.Equal(t, "bge-m3", records[0].Value(embeddings.ContractName, embeddings.ValueModel))
	assert.Equal(t, "sk-key", secrets.GetAppSecret(store.ExternalSecretScope(embeddings.DefaultID), "embeddings"))
}

func TestApplySetEmbeddingsIntent_ReformattingIsANoOp(t *testing.T) {
	orch, _ := applyOrchestrator(t)
	orch.applySetEmbeddingsIntent(NewSetEmbeddingsIntent("https://api.example.com/v1", "E", "m", strPtr("k")))

	before := countEmbeddingsRecords(t, orch)
	orch.applySetEmbeddingsIntent(NewSetEmbeddingsIntent("https://api.example.com/v1/", "E", "m", strPtr("k")))

	assert.Equal(t, before, countEmbeddingsRecords(t, orch))
}

func TestApplySetEmbeddingsIntent_RemovalTakesTheCredentialWithIt(t *testing.T) {
	orch, secrets := applyOrchestrator(t)
	orch.applySetEmbeddingsIntent(NewSetEmbeddingsIntent("https://api.example.com/v1", "E", "m", strPtr("k")))

	orch.applySetEmbeddingsIntent(NewSetEmbeddingsIntent("", "", "", nil))

	assert.Zero(t, countEmbeddingsRecords(t, orch))
	assert.Empty(t, secrets.GetAppSecret(store.ExternalSecretScope(embeddings.DefaultID), "embeddings"),
		"a key left behind under a deleted record is a credential nothing owns")
}

func TestApplySetEmbeddingsIntent_RejectsAnEndpointThatDoesNotParse(t *testing.T) {
	orch, _ := applyOrchestrator(t)

	orch.applySetEmbeddingsIntent(NewSetEmbeddingsIntent("https://api.example.com/v1?x=1", "E", "m", nil))

	assert.Zero(t, countEmbeddingsRecords(t, orch),
		"a value that cannot be understood is refused at the save, not diagnosed at every pass")
}

// TestApplySetEndpointIntent_TwoContractsDoNotShareARecord is the reason the two
// contracts keep separate record IDs and separate secret scopes: an operator who
// points chat, transcription, and embeddings at one server still has three rows,
// and removing one capability must not delete the others.
func TestApplySetEndpointIntent_TwoContractsDoNotShareARecord(t *testing.T) {
	orch, secrets := applyOrchestrator(t)

	orch.applySetTranscriptionIntent(NewSetTranscriptionIntent("https://one.example/v1", "T", "w", strPtr("kt")))
	orch.applySetEmbeddingsIntent(NewSetEmbeddingsIntent("https://two.example/v1", "E", "b", strPtr("ke")))

	assert.Equal(t, 1, countTranscriptionRecords(t, orch))
	assert.Equal(t, 1, countEmbeddingsRecords(t, orch))

	orch.applySetTranscriptionIntent(NewSetTranscriptionIntent("", "", "", nil))

	assert.Zero(t, countTranscriptionRecords(t, orch))
	assert.Equal(t, 1, countEmbeddingsRecords(t, orch), "removing one capability leaves the other standing")
	assert.Equal(t, "ke", secrets.GetAppSecret(store.ExternalSecretScope(embeddings.DefaultID), "embeddings"))
	assert.Empty(t, secrets.GetAppSecret(store.ExternalSecretScope(transcription.DefaultID), "transcription"))
}
