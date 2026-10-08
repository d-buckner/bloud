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

// inferenceConsumer builds an app that declares the inference contract the way
// Hermes does, including the least-privilege `requires` gate.
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

var instanceSrc = catalog.CompatibleApp{Source: catalog.SettingProviderSource}

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

// The instance-as-provider path, which is the only inference source that exists
// today: the setting is populated, the consumer resolves it, and the binding
// carries the operator's own credential rather than a gateway-issued one.
func TestResolveInference_InstanceServesAsProvider(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "hermes", nil)

	consumer := inferenceConsumer("hermes", instanceSrc)
	orch, secrets := bindingsOrchestrator(t, store, consumer)
	secrets.publish("ai", "apiKey", "operator-key")
	configureInstance(t, orch.settings.(*fakeSettings), "https://api.example.com/v1", "gpt-4o-mini")

	out := orch.buildIntegrations("hermes", consumer)

	require.Len(t, out.Inference, 1)
	b := out.Inference[0]
	assert.Equal(t, catalog.SettingProviderSource, b.App)
	assert.Equal(t, configurator.ProviderKindSetting, b.Kind)
	assert.True(t, b.Installed, "the setting being populated is the instance analogue of an installed provider")
	assert.Equal(t, "https://api.example.com/v1", b.Endpoint)
	assert.Equal(t, "operator-key", b.APIKey)
	assert.Equal(t, "gpt-4o-mini", b.DefaultModel)
	assert.False(t, b.ViaGateway, "this is the raw upstream, not a gateway")
	assert.Empty(t, b.Node, "an instance provider has no container and no node")
	assert.Zero(t, b.Port)
}

// Nothing configured and nothing installed resolves to no binding, which a
// consumer treats the way it treats an uninstalled provider.
func TestResolveInference_NothingConfigured(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "hermes", nil)

	consumer := inferenceConsumer("hermes", instanceSrc)
	orch, _ := bindingsOrchestrator(t, store, consumer)

	out := orch.buildIntegrations("hermes", consumer)
	assert.Empty(t, out.Inference)
}

// There is no validity check on the stored default: a model absent from any
// discovered list still resolves into the binding unchanged. The consumer's own
// request is the check, not Bloud's cache.
func TestResolveInference_DefaultModelIsNeverValidated(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "hermes", nil)

	consumer := inferenceConsumer("hermes", instanceSrc)
	orch, _ := bindingsOrchestrator(t, store, consumer)
	configureInstance(t, orch.settings.(*fakeSettings), "https://api.example.com/v1", "a-model-that-no-longer-exists")

	out := orch.buildIntegrations("hermes", consumer)

	require.Len(t, out.Inference, 1)
	assert.Equal(t, "a-model-that-no-longer-exists", out.Inference[0].DefaultModel,
		"Bloud does not second-guess the stored default")
}

// The credential gate is real: a consumer that never declared `requires: apiKey`
// is not handed the operator's credential by accident.
func TestResolveInference_RequiresGatesTheCredential(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "hermes", nil)

	consumer := &catalog.App{
		CatalogID: "hermes",
		Integrations: map[string]catalog.Integration{
			"inference": {
				Compatible: []catalog.CompatibleApp{instanceSrc},
			},
		},
	}
	orch, secrets := bindingsOrchestrator(t, store, consumer)
	secrets.publish("ai", "apiKey", "operator-key")
	configureInstance(t, orch.settings.(*fakeSettings), "https://api.example.com/v1", "gpt-4o")

	out := orch.buildIntegrations("hermes", consumer)

	require.Len(t, out.Inference, 1)
	assert.Empty(t, out.Inference[0].APIKey,
		"a consumer that did not ask for the credential does not get it")
}

// The load-bearing graph property: an instance provider never produces a node or
// an edge. Without this filter the graph fills with phantom nodes for settings.
func TestComputeAppDeps_InstanceProviderCreatesNoEdge(t *testing.T) {
	apps := map[string]*store.InstalledApp{
		"hermes": {CatalogID: "hermes"},
	}
	cache := NewFakeCatalogCache()
	cache.AddApp(inferenceConsumer("hermes", instanceSrc))

	deps := computeAppDeps(apps, cache)

	for _, dep := range deps["hermes"] {
		assert.NotEqual(t, catalog.SettingProviderSource, dep,
			"the instance is never a graph dependency")
		assert.NotEmpty(t, dep, "an empty provider id must never become an edge")
	}
}

// The instance provider must not leak into non-inference contracts: a consumer
// declaring `source: setting` for some other contract gets no binding rather
// than a half-populated one.
func TestBuildIntegrations_InstanceSourceOnlyResolvesInference(t *testing.T) {
	consumer := &catalog.App{
		CatalogID: "something",
		Integrations: map[string]catalog.Integration{
			"pvr": {Required: false, Compatible: []catalog.CompatibleApp{
				{Source: catalog.SettingProviderSource},
			}},
		},
	}
	store := NewFakeAppStore()
	install(t, store, "something", nil)

	orch, _ := bindingsOrchestrator(t, store, consumer)
	out := orch.buildIntegrations("something", consumer)

	assert.Empty(t, out.PVRs, "the instance provides no pvr contract")
}
