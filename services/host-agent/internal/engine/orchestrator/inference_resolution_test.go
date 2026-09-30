// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// fakeSettings is an in-memory SettingsStoreInterface so the resolver can read
// the instance's AI settings without a database.
type fakeSettings struct {
	values map[string]string
}

func newFakeSettings() *fakeSettings { return &fakeSettings{values: map[string]string{}} }

func (f *fakeSettings) Get(key string) (string, error) { return f.values[key], nil }
func (f *fakeSettings) Set(key, value string) error {
	f.values[key] = value
	return nil
}

var _ store.SettingsStoreInterface = (*fakeSettings)(nil)

func inferenceConsumer(id string, compatible ...catalog.CompatibleApp) *catalog.App {
	return &catalog.App{
		CatalogID: id,
		Integrations: map[string]catalog.Integration{
			"inference": {
				Required:   false,
				Requires:   []string{"apiKey"},
				Compatible: compatible,
			},
		},
	}
}

func gatewayApp() *catalog.App {
	return providerApp("litellm", 4000, "inference", catalog.ContractProvides{
		Secrets: []string{"apiKey"},
		Values:  map[string]string{"path": "/v1"},
	})
}

func ollamaApp() *catalog.App {
	return providerApp("ollama", 11434, "modelSource", catalog.ContractProvides{
		Values: map[string]string{"path": "/v1"},
	})
}

var (
	litellmSource = catalog.CompatibleApp{App: "litellm", Default: true}
	instanceSrc   = catalog.CompatibleApp{Source: catalog.InstanceProviderSource}
)

// configureInstance sets the instance's upstream and default model.
func configureInstance(t *testing.T, s *fakeSettings, baseURL, defaultModel string) {
	t.Helper()
	upstreams, err := inference.EncodeUpstreams([]inference.Upstream{
		{ID: "default", Name: "Primary", BaseURL: baseURL, Enabled: true},
	})
	require.NoError(t, err)
	s.values[inference.SettingUpstreams] = upstreams
	s.values[inference.SettingDefaultModel] = defaultModel
}

// A gateway app that is installed serves inference, and the binding says so:
// ViaGateway distinguishes a gateway credential from the operator's own.
func TestResolveInference_GatewayWins(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "hermes", nil)
	install(t, store, "litellm", nil)

	orch, secrets := bindingsOrchestrator(t, store, inferenceConsumer("hermes", litellmSource, instanceSrc), gatewayApp(), ollamaApp())
	secrets.publish("litellm", "apiKey", "gateway-key")
	configureInstance(t, orch.settings.(*fakeSettings), "https://api.example.com/v1", "gpt-4o")

	out := orch.buildIntegrations("hermes", inferenceConsumer("hermes", litellmSource, instanceSrc))

	require.Len(t, out.Inference, 1, "a consumer dials exactly one inference endpoint")
	b := out.Inference[0]
	assert.Equal(t, "litellm", b.App)
	assert.Equal(t, configurator.ProviderKindApp, b.Kind)
	assert.Equal(t, "http://apps-litellm:4000/v1", b.Endpoint)
	assert.Equal(t, "gateway-key", b.APIKey)
	assert.Equal(t, "gpt-4o", b.DefaultModel, "the instance default propagates through the gateway")
	assert.True(t, b.ViaGateway)
}

// With no gateway installed the instance's own upstream serves inference, and
// the credential carried is the operator's, not a gateway-issued one.
func TestResolveInference_InstanceServesWhenNoGateway(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "hermes", nil)

	orch, secrets := bindingsOrchestrator(t, store, inferenceConsumer("hermes", litellmSource, instanceSrc), gatewayApp(), ollamaApp())
	secrets.publish("ai", "apiKey", "operator-key")
	configureInstance(t, orch.settings.(*fakeSettings), "https://api.example.com/v1", "gpt-4o-mini")

	out := orch.buildIntegrations("hermes", inferenceConsumer("hermes", litellmSource, instanceSrc))

	require.Len(t, out.Inference, 1)
	b := out.Inference[0]
	assert.Equal(t, catalog.InstanceProviderSource, b.App)
	assert.Equal(t, configurator.ProviderKindInstance, b.Kind)
	assert.True(t, b.Installed, "the setting being populated is the instance analogue of an installed provider")
	assert.Equal(t, "https://api.example.com/v1", b.Endpoint)
	assert.Equal(t, "operator-key", b.APIKey)
	assert.Equal(t, "gpt-4o-mini", b.DefaultModel)
	assert.False(t, b.ViaGateway, "this is the raw upstream, not a gateway")
	assert.Empty(t, b.Node, "an instance provider has no container and no node")
	assert.Zero(t, b.Port)
}

// A bare Ollama serves a consumer that never named it, because the inference
// contract declares modelSource as its fallback.
func TestResolveInference_PromotesModelSource(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "hermes", nil)
	install(t, store, "ollama", nil)

	orch, _ := bindingsOrchestrator(t, store, inferenceConsumer("hermes", litellmSource, instanceSrc), gatewayApp(), ollamaApp())

	out := orch.buildIntegrations("hermes", inferenceConsumer("hermes", litellmSource, instanceSrc))

	require.Len(t, out.Inference, 1)
	b := out.Inference[0]
	assert.Equal(t, "ollama", b.App, "promoted from the modelSource contract the consumer never named")
	assert.Equal(t, "http://apps-ollama:11434/v1", b.Endpoint)
	assert.False(t, b.ViaGateway, "a promoted source is the raw upstream")
	assert.Empty(t, b.APIKey, "a keyless local runtime stays keyless through promotion")
}

// Promotion must not outrank the instance setting: precedence is gateway, then
// instance, then promoted sources.
func TestResolveInference_PromotionIsLastResort(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "hermes", nil)
	install(t, store, "ollama", nil)

	orch, secrets := bindingsOrchestrator(t, store, inferenceConsumer("hermes", litellmSource, instanceSrc), gatewayApp(), ollamaApp())
	secrets.publish("ai", "apiKey", "operator-key")
	configureInstance(t, orch.settings.(*fakeSettings), "https://api.example.com/v1", "gpt-4o")

	out := orch.buildIntegrations("hermes", inferenceConsumer("hermes", litellmSource, instanceSrc))

	require.Len(t, out.Inference, 1)
	assert.Equal(t, catalog.InstanceProviderSource, out.Inference[0].App,
		"the configured instance upstream wins over a promoted Ollama")
}

// Nothing configured and nothing installed resolves to no binding, which a
// consumer treats the way it treats an uninstalled provider.
func TestResolveInference_NothingConfigured(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "hermes", nil)

	orch, _ := bindingsOrchestrator(t, store, inferenceConsumer("hermes", litellmSource, instanceSrc), gatewayApp(), ollamaApp())

	out := orch.buildIntegrations("hermes", inferenceConsumer("hermes", litellmSource, instanceSrc))
	assert.Empty(t, out.Inference)
}

// There is no validity check on the stored default: a model absent from any
// discovered list still resolves into the binding unchanged. The consumer's own
// request is the check, not Bloud's cache.
func TestResolveInference_DefaultModelIsNeverValidated(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "hermes", nil)

	orch, _ := bindingsOrchestrator(t, store, inferenceConsumer("hermes", litellmSource, instanceSrc), gatewayApp(), ollamaApp())
	configureInstance(t, orch.settings.(*fakeSettings), "https://api.example.com/v1", "a-model-that-no-longer-exists")

	out := orch.buildIntegrations("hermes", inferenceConsumer("hermes", litellmSource, instanceSrc))

	require.Len(t, out.Inference, 1)
	assert.Equal(t, "a-model-that-no-longer-exists", out.Inference[0].DefaultModel,
		"Bloud does not second-guess the stored default")
}

// The load-bearing graph property: an instance provider never produces a node or
// an edge. Without this filter the graph fills with phantom nodes for settings.
func TestComputeAppDeps_InstanceProviderCreatesNoEdge(t *testing.T) {
	apps := map[string]*store.InstalledApp{
		"hermes": {CatalogID: "hermes"},
	}
	cache := NewFakeCatalogCache()
	cache.AddApp(inferenceConsumer("hermes", litellmSource, instanceSrc))

	deps := computeAppDeps(apps, cache)

	for _, dep := range deps["hermes"] {
		assert.NotEqual(t, catalog.InstanceProviderSource, dep,
			"the instance is never a graph dependency")
		assert.NotEmpty(t, dep, "an empty provider id must never become an edge")
	}
}

// A modelSource consumer (the gateway itself) gets its upstreams bound, so
// LiteLLM can merge the instance's server and a local Ollama into one config.
func TestBuildIntegrations_ModelSourceConsumer(t *testing.T) {
	consumer := &catalog.App{
		CatalogID: "litellm",
		Integrations: map[string]catalog.Integration{
			"modelSource": {Required: false, Multi: true, Compatible: []catalog.CompatibleApp{
				{App: "ollama"},
				{Source: catalog.InstanceProviderSource},
			}},
		},
	}
	store := NewFakeAppStore()
	install(t, store, "litellm", nil)
	install(t, store, "ollama", nil)

	orch, secrets := bindingsOrchestrator(t, store, consumer, ollamaApp())
	secrets.publish("ai", "apiKey", "operator-key")
	configureInstance(t, orch.settings.(*fakeSettings), "https://api.example.com/v1", "gpt-4o")

	out := orch.buildIntegrations("litellm", consumer)

	require.Len(t, out.ModelSources, 1, "the app provider binds; the instance is resolved by the gateway path")
	assert.Equal(t, "ollama", out.ModelSources[0].App)
	assert.Equal(t, "http://apps-ollama:11434/v1", out.ModelSources[0].Endpoint)
}

// The instance provider must not leak into non-inference contracts: a consumer
// declaring `source: instance` for some other contract gets no binding rather
// than a half-populated one.
func TestBuildIntegrations_InstanceSourceOnlyResolvesInference(t *testing.T) {
	consumer := &catalog.App{
		CatalogID: "something",
		Integrations: map[string]catalog.Integration{
			"pvr": {Required: false, Compatible: []catalog.CompatibleApp{
				{Source: catalog.InstanceProviderSource},
			}},
		},
	}
	store := NewFakeAppStore()
	install(t, store, "something", nil)

	orch, _ := bindingsOrchestrator(t, store, consumer)
	out := orch.buildIntegrations("something", consumer)

	assert.Empty(t, out.PVRs, "the instance provides no pvr contract")
}
