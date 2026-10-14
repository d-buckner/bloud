// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/transcription"
)

// settingConsumerApp is catalog metadata for an app that declares one contract
// and names the operator's Settings as its only provider.
func settingConsumerApp(id, contract string, secrets []string) *catalog.App {
	return &catalog.App{
		CatalogID: id,
		Integrations: map[string]catalog.Integration{
			contract: {
				Requires: secrets,
				Compatible: []catalog.CompatibleApp{
					{Source: catalog.SettingProviderSource},
				},
			},
		},
	}
}

// putTranscriptionRecord stores a transcription provider the way the Settings
// surface would, so a resolution test reads the same row the operator wrote.
func putTranscriptionRecord(t *testing.T, ext store.ExternalAppStoreInterface, p transcription.Provider) {
	t.Helper()
	require.NoError(t, ext.Upsert(transcription.ToExternal(p)))
}

func TestBuildIntegrations_TranscriptionFromTheInstanceSetting(t *testing.T) {
	appStore := NewFakeAppStore()
	install(t, appStore, "podly", nil)

	consumer := settingConsumerApp("podly", "transcription", requires("apiKey"))
	orch, secrets := bindingsOrchestrator(t, appStore, consumer)
	ext := orch.externalApps
	putTranscriptionRecord(t, ext, transcription.Provider{
		ID: transcription.DefaultID, Name: "Transcription",
		BaseURL: "https://api.example.com/v1", Model: "whisper-1",
	})
	secrets.publish(store.ExternalSecretScope(transcription.DefaultID), "transcription", "sk-instance")

	bindings := orch.buildIntegrations("podly", consumer).Transcription
	require.Len(t, bindings, 1)
	assert.Equal(t, "https://api.example.com/v1", bindings[0].Endpoint)
	assert.Equal(t, "sk-instance", bindings[0].APIKey.String())
	assert.Equal(t, "whisper-1", bindings[0].Model)
	assert.True(t, bindings[0].Installed, "the operator filled the role, which is the setting analogue of a graph edge")
	assert.Equal(t, transcription.DefaultID, bindings[0].App,
		"a setting provider is named by its record, the same shape an inference binding carries")
}

// TestBuildIntegrations_TranscriptionSecretOnlyWhenRequired is invariant 15's
// least-privilege gate applied to a credential the operator typed: declaring the
// contract gets the address, never the key.
func TestBuildIntegrations_TranscriptionSecretOnlyWhenRequired(t *testing.T) {
	appStore := NewFakeAppStore()
	install(t, appStore, "podly", nil)

	consumer := settingConsumerApp("podly", "transcription", nil)
	orch, secrets := bindingsOrchestrator(t, appStore, consumer)
	putTranscriptionRecord(t, orch.externalApps, transcription.Provider{
		ID: transcription.DefaultID, BaseURL: "https://api.example.com/v1",
	})
	secrets.publish(store.ExternalSecretScope(transcription.DefaultID), "transcription", "sk-instance")

	bindings := orch.buildIntegrations("podly", consumer).Transcription
	require.Len(t, bindings, 1)
	assert.Equal(t, "https://api.example.com/v1", bindings[0].Endpoint)
	assert.Empty(t, bindings[0].APIKey, "a consumer that did not declare the secret must not be handed it")
}

func TestBuildIntegrations_NoTranscriptionRecordBindsNothing(t *testing.T) {
	appStore := NewFakeAppStore()
	install(t, appStore, "podly", nil)

	consumer := settingConsumerApp("podly", "transcription", requires("apiKey"))
	orch, _ := bindingsOrchestrator(t, appStore, consumer)

	bindings := orch.buildIntegrations("podly", consumer).Transcription
	assert.Empty(t, bindings, "nothing configured reads to the consumer as no provider, not as a broken one")
}

// applyOrchestrator wires an orchestrator with the three stores the settings
// intent writes, on a real database.
func applyOrchestrator(t *testing.T) (*Orchestrator, *fakeSecrets) {
	t.Helper()
	secrets := newFakeSecrets()
	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()), new(MockConfiguratorRegistry), NewFakeCatalogCache(),
		"/tmp/bloud-test", newTestLogger(),
		OrchestratorConfig{Stores: StoresConfig{
			AppStore:     NewFakeAppStore(),
			Secrets:      secrets,
			ExternalApps: store.NewExternalAppStore(testdb.SetupTestDB(t)),
		}},
	)
	orch.settings = newFakeSettings()
	return orch, secrets
}

func TestApplySetTranscriptionIntent_StoresRecordAndCredential(t *testing.T) {
	orch, secrets := applyOrchestrator(t)

	orch.applySetTranscriptionIntent(NewSetTranscriptionIntent(
		"https://api.example.com/v1/", "Groq", "whisper-large-v3-turbo", strPtr("sk-key")))

	records, err := orch.externalApps.FindAllBySource(store.ExternalAppSourceForContract(transcription.ContractName))
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "https://api.example.com/v1", records[0].URL,
		"the stored value is the canonical endpoint, not what was typed")
	assert.Equal(t, "whisper-large-v3-turbo", records[0].Value(transcription.ContractName, transcription.ValueModel))
	assert.Equal(t, "sk-key", secrets.GetAppSecret(store.ExternalSecretScope(transcription.DefaultID), "transcription"))
}

// TestApplySetTranscriptionIntent_ReformattingIsANoOp is the same guard the
// public URL and inference settings carry: a save that changed nothing must not
// restart every app that consumes the contract.
func TestApplySetTranscriptionIntent_ReformattingIsANoOp(t *testing.T) {
	orch, _ := applyOrchestrator(t)
	orch.applySetTranscriptionIntent(NewSetTranscriptionIntent("https://api.example.com/v1", "T", "m", strPtr("k")))

	before := countTranscriptionRecords(t, orch)
	orch.applySetTranscriptionIntent(NewSetTranscriptionIntent("https://api.example.com/v1/", "T", "m", strPtr("k")))

	assert.Equal(t, before, countTranscriptionRecords(t, orch))
}

func TestApplySetTranscriptionIntent_RemovalTakesTheCredentialWithIt(t *testing.T) {
	orch, secrets := applyOrchestrator(t)
	orch.applySetTranscriptionIntent(NewSetTranscriptionIntent("https://api.example.com/v1", "T", "m", strPtr("k")))

	orch.applySetTranscriptionIntent(NewSetTranscriptionIntent("", "", "", nil))

	assert.Zero(t, countTranscriptionRecords(t, orch))
	assert.Empty(t, secrets.GetAppSecret(store.ExternalSecretScope(transcription.DefaultID), "transcription"),
		"a key left behind under a deleted record is a credential nothing owns")
}

func TestApplySetTranscriptionIntent_RejectsAnEndpointThatDoesNotParse(t *testing.T) {
	orch, _ := applyOrchestrator(t)

	orch.applySetTranscriptionIntent(NewSetTranscriptionIntent("https://api.example.com/v1?x=1", "T", "m", nil))

	assert.Zero(t, countTranscriptionRecords(t, orch),
		"a value that cannot be understood is refused at the save, not diagnosed at every pass")
}

func countTranscriptionRecords(t *testing.T, orch *Orchestrator) int {
	t.Helper()
	records, err := orch.externalApps.FindAllBySource(store.ExternalAppSourceForContract(transcription.ContractName))
	require.NoError(t, err)
	return len(records)
}

func strPtr(s string) *string { return &s }
