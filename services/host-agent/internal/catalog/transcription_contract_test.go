// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShippedCatalog_TranscriptionConsumerNamesTheSettingAsItsProvider pins the
// one declaration the transcription contract has in the shipped catalog.
//
// A consumer of a setting-sourced contract has no graph edge: nothing orders it
// after a provider, because the provider is a row in a settings table. The
// declaration is therefore the whole wiring, and a catalog that lost the
// `source: setting` entry would leave the app asking for a provider no
// compatible app can satisfy, which reads as an app that never gets its
// endpoint rather than as a catalog that lost a line.
func TestShippedCatalog_TranscriptionConsumerNamesTheSettingAsItsProvider(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	spec, known := ContractFor("transcription")
	require.True(t, known, "the transcription contract must be in the registry")
	assert.Equal(t, []string{"apiKey"}, spec.Secrets,
		"an OpenAI-compatible endpoint authenticates with one bearer")
	require.Len(t, spec.Values, 1,
		"the model is the one fact about the endpoint a consumer needs beyond its address")
	assert.Equal(t, "model", spec.Values[0].Key)
	assert.Nil(t, spec.SatisfiedBy,
		"the contract is served by a setting, so no catalog app can satisfy it")

	consumer := apps["podly"]
	require.NotNil(t, consumer, "podly must be in the shipped catalog")

	integration, ok := consumer.Integrations["transcription"]
	require.True(t, ok, "podly must declare integrations.transcription")
	assert.Equal(t, []string{"apiKey"}, integration.Requires,
		"the endpoint is reached with a key, so the consumer has to ask for it")
	assert.False(t, integration.Required,
		"an instance with no transcription endpoint is a working app with no transcripts, not a broken install")

	settingProviders := 0
	for _, declared := range integration.Compatible {
		if declared.Source == SettingProviderSource {
			settingProviders++
		}
	}
	assert.Equal(t, 1, settingProviders,
		"the operator's Settings entry is the only provider this contract has")
}

// TestShippedCatalog_TranscriptionConsumerRunsTheImageThatNeedsAnEndpoint keeps
// the contract and the image choice tied together.
//
// The lite image is what makes the contract necessary: it ships no local model,
// so a transcript is only possible if an endpoint reaches the app. The full image
// carries its own model and needs nothing, which would leave the whole settings
// surface wiring an app that never reads it. If the image ever changes back, the
// contract's reason for existing has to be re-examined, and this says so.
func TestShippedCatalog_TranscriptionConsumerRunsTheImageThatNeedsAnEndpoint(t *testing.T) {
	apps, err := NewLoader(realCatalogDir(t)).LoadAll()
	require.NoError(t, err)

	consumer := apps["podly"]
	require.NotNil(t, consumer)

	var image string
	for _, container := range consumer.Containers {
		if container.Name == "apps-"+consumer.CatalogID {
			image = container.Image
		}
	}
	require.NotEmpty(t, image, "the app container's image must be declared")
	assert.True(t, strings.HasSuffix(image, "-lite"),
		"%s: the transcription contract exists because the image has no local model; "+
			"a build that bundles one does not need this wiring", image)
}
