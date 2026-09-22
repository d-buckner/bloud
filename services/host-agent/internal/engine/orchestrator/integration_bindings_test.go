// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
)

// fakeSecrets is an in-memory AppSecretsProvider: the published bag the
// orchestrator resolves a contract's credentials from.
type fakeSecrets struct {
	published map[string]map[string]string
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{published: make(map[string]map[string]string)}
}

func (f *fakeSecrets) publish(app, key, value string) {
	if f.published[app] == nil {
		f.published[app] = make(map[string]string)
	}
	f.published[app][key] = value
}

func (f *fakeSecrets) GenerateAppAdminPassword(appName string) (string, error) {
	return f.GetAppSecret(appName, "adminPassword"), nil
}

func (f *fakeSecrets) GetAppSecret(appName, key string) string {
	return f.published[appName][key]
}

func (f *fakeSecrets) SetAppSecret(appName, key, value string) error {
	f.publish(appName, key, value)
	return nil
}

// providerApp is catalog metadata for a single-container provider that offers
// one contract.
func providerApp(id string, port int, contract string, offer catalog.ContractProvides) *catalog.App {
	return &catalog.App{
		CatalogID: id,
		Port:      port,
		Provides:  catalog.Provides{contract: offer},
		Containers: []catalog.ContainerDef{
			{Name: "apps-" + id, Image: "example/" + id},
		},
	}
}

// consumerApp is catalog metadata for an app declaring one contract, with the
// compatible providers named after the integration.
func consumerApp(id, contract string, integration catalog.Integration, providers ...string) *catalog.App {
	compatible := make([]catalog.CompatibleApp, 0, len(providers))
	for _, provider := range providers {
		compatible = append(compatible, catalog.CompatibleApp{App: provider})
	}
	integration.Compatible = compatible
	if len(providers) > 1 {
		integration.Multi = true
	}
	return &catalog.App{
		CatalogID:    id,
		Integrations: map[string]catalog.Integration{contract: integration},
	}
}

// requires builds the consumer-side declaration of what an app reads out of a
// contract payload.
func requires(secrets ...string) []string {
	return secrets
}

// bindingsOrchestrator wires an orchestrator with a catalog, a store and a
// secret store, which is everything binding resolution reads.
func bindingsOrchestrator(t *testing.T, appStore *FakeAppStore, apps ...*catalog.App) (*Orchestrator, *fakeSecrets) {
	t.Helper()
	cache := NewFakeCatalogCache()
	for _, app := range apps {
		cache.AddApp(app)
	}
	secrets := newFakeSecrets()
	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		new(MockConfiguratorRegistry),
		cache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{AppStore: appStore, Secrets: secrets},
	)
	return orch, secrets
}

// install registers an app in the store, optionally with a recorded choice.
func install(t *testing.T, store *FakeAppStore, id string, integrationConfig map[string]string) {
	t.Helper()
	require.NoError(t, store.Install(id, id, "1", integrationConfig, nil))
}

// A contract's payload is built from the contract the consumer declared, with
// the provider's address resolved from the catalog, its credential from the host
// secret store, and its declared values assembled into the payload. A provider
// that is not installed is still described, so its entry can be pruned.
func TestBuildIntegrations_ResolvesContractPayloads(t *testing.T) {
	consumer := consumerApp("prowlarr", "pvr", catalog.Integration{Requires: requires("apiKey")}, "sonarr", "radarr")
	consumer.Integrations["mcp"] = catalog.Integration{
		Requires:   requires("httpToken"),
		Compatible: []catalog.CompatibleApp{{App: "affine-mcp"}},
	}
	store := NewFakeAppStore()
	install(t, store, "prowlarr", nil)
	install(t, store, "sonarr", nil)
	install(t, store, "affine-mcp", nil)

	orch, secrets := bindingsOrchestrator(t, store,
		consumer,
		providerApp("sonarr", 8989, "pvr", catalog.ContractProvides{Secrets: []string{"apiKey"}}),
		providerApp("radarr", 7878, "pvr", catalog.ContractProvides{Secrets: []string{"apiKey"}}),
		providerApp("affine-mcp", 3011, "mcp", catalog.ContractProvides{
			Secrets: []string{"httpToken"},
			Values:  map[string]string{"path": "/mcp", "serverName": "affine"},
		}),
	)
	secrets.publish("sonarr", "apiKey", "sonarr-key")
	secrets.publish("affine-mcp", "httpToken", "bearer-token")

	out := orch.buildIntegrations("prowlarr", consumer)

	require.Len(t, out.PVRs, 2, "one binding per declared PVR, in declaration order")
	sonarr, radarr := out.PVRs[0], out.PVRs[1]

	assert.Equal(t, "sonarr", sonarr.App)
	assert.True(t, sonarr.Installed)
	assert.Equal(t, "apps-sonarr", sonarr.Node)
	assert.Equal(t, 8989, sonarr.Port)
	assert.Equal(t, "http://apps-sonarr:8989", sonarr.BaseURL, "containers reach the provider on the app network")
	assert.Equal(t, "http://localhost:8989", sonarr.LocalURL, "the configurator reaches it from the host")
	assert.Equal(t, "sonarr-key", sonarr.APIKey, "the key the provider published for this contract")

	assert.Equal(t, "radarr", radarr.App)
	assert.False(t, radarr.Installed)
	assert.Equal(t, "http://apps-radarr:7878", radarr.BaseURL, "an uninstalled provider still has the address Bloud wrote")
	assert.Empty(t, radarr.APIKey, "an unpublished credential is empty, not a stale value")

	require.Len(t, out.MCPServers, 1)
	mcp := out.MCPServers[0]
	assert.Equal(t, "affine", mcp.ServerName)
	assert.Equal(t, "http://apps-affine-mcp:3011/mcp", mcp.URL, "the endpoint is the resolved address plus the provider's path")
	assert.Equal(t, "bearer-token", mcp.Token)

	assert.Empty(t, out.MediaServers, "a contract the consumer does not declare yields nothing")
	assert.Empty(t, out.DownloadClients)
	assert.Empty(t, out.SSO)
}

// A contract the consumer declares but whose provider publishes nothing yet
// still yields the binding: "not published" is a state the consumer handles, not
// a missing binding.
func TestBuildIntegrations_UnpublishedSecretIsEmpty(t *testing.T) {
	consumer := consumerApp("seerr", "mediaServer", catalog.Integration{Requires: requires("adminPassword")}, "jellyfin")
	store := NewFakeAppStore()
	install(t, store, "seerr", nil)
	install(t, store, "jellyfin", nil)

	orch, _ := bindingsOrchestrator(t, store,
		consumer,
		providerApp("jellyfin", 8096, "mediaServer", catalog.ContractProvides{Secrets: []string{"adminPassword"}}),
	)

	out := orch.buildIntegrations("seerr", consumer)
	require.Len(t, out.MediaServers, 1)
	assert.True(t, out.MediaServers[0].Installed)
	assert.Empty(t, out.MediaServers[0].AdminPassword)
}

// A required contract binds the provider that was chosen for it; an optional one
// binds everything the metadata declares, which is the same set the dependency
// edges order.
func TestBuildIntegrations_FollowsChoiceForRequiredContracts(t *testing.T) {
	consumer := consumerApp("radarr", "downloadClient", catalog.Integration{Required: true}, "qbittorrent", "deluge")
	consumer.Integrations["pvr"] = catalog.Integration{
		Compatible: []catalog.CompatibleApp{{App: "prowlarr"}},
	}
	store := NewFakeAppStore()
	install(t, store, "radarr", map[string]string{"downloadClient": "deluge"})
	install(t, store, "qbittorrent", nil)
	install(t, store, "deluge", nil)
	install(t, store, "prowlarr", nil)

	orch, _ := bindingsOrchestrator(t, store,
		consumer,
		providerApp("qbittorrent", 8081, "downloadClient", catalog.ContractProvides{}),
		providerApp("deluge", 8112, "downloadClient", catalog.ContractProvides{}),
		providerApp("prowlarr", 9696, "pvr", catalog.ContractProvides{Secrets: []string{"apiKey"}}),
	)

	out := orch.buildIntegrations("radarr", consumer)

	require.Len(t, out.DownloadClients, 1, "a required contract binds only the chosen provider")
	assert.Equal(t, "deluge", out.DownloadClients[0].App)
	require.Len(t, out.PVRs, 1, "an optional contract binds the compatible providers")
	assert.Equal(t, "prowlarr", out.PVRs[0].App)
}

// A contract with no payload (proxy, database) reaches the consumer as the graph
// edge without appearing in any typed slice, and an app is never its own
// provider.
func TestBuildIntegrations_NoPayloadContractAndSelfProvider(t *testing.T) {
	consumer := consumerApp("sonarr", "proxy", catalog.Integration{Required: true}, "traefik")
	consumer.Integrations["pvr"] = catalog.Integration{
		Compatible: []catalog.CompatibleApp{{App: "sonarr"}, {App: "prowlarr"}},
	}
	store := NewFakeAppStore()
	install(t, store, "sonarr", map[string]string{"proxy": "traefik"})
	install(t, store, "traefik", nil)
	install(t, store, "prowlarr", nil)

	orch, _ := bindingsOrchestrator(t, store,
		consumer,
		providerApp("traefik", 80, "proxy", catalog.ContractProvides{}),
		providerApp("prowlarr", 9696, "pvr", catalog.ContractProvides{Secrets: []string{"apiKey"}}),
	)

	out := orch.buildIntegrations("sonarr", consumer)
	require.Len(t, out.PVRs, 1)
	assert.Equal(t, "prowlarr", out.PVRs[0].App, "an app is not its own provider")
	assert.Empty(t, out.MediaServers)
	assert.Empty(t, out.MCPServers)
}

// Without a store there is nothing to resolve.
func TestBuildIntegrations_WithoutStore(t *testing.T) {
	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		new(MockConfiguratorRegistry),
		nil,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{},
	)
	out := orch.buildIntegrations("seerr", consumerApp("seerr", "mediaServer", catalog.Integration{}, "jellyfin"))
	assert.Empty(t, out.PVRs)
	assert.Empty(t, out.MediaServers)
	assert.Empty(t, out.MCPServers)
}

// A configurator's state carries the typed integrations, so the resolution runs
// on the same path every lifecycle phase uses.
func TestBuildAppState_CarriesIntegrationBindings(t *testing.T) {
	consumer := consumerApp("seerr", "pvr", catalog.Integration{Requires: requires("apiKey")}, "radarr")
	store := NewFakeAppStore()
	install(t, store, "seerr", nil)
	install(t, store, "radarr", nil)

	orch, secrets := bindingsOrchestrator(t, store,
		consumer,
		providerApp("radarr", 7878, "pvr", catalog.ContractProvides{Secrets: []string{"apiKey"}}),
	)
	secrets.publish("radarr", "apiKey", "radarr-key")

	state, err := orch.buildAppState("seerr")
	require.NoError(t, err)
	require.Len(t, state.Integrations.PVRs, 1)
	assert.Equal(t, "radarr-key", state.Integrations.PVRs[0].APIKey)
}

// A consumer is handed only the credentials it declared it reads: integrating
// with the identity provider for SSO does not, by itself, hand an app the
// provider's API token. This is the difference between "this app can manage the
// identity provider" and "this app can mirror its users".
func TestBuildIntegrations_ResolvesOnlyRequiredSecrets(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "navidrome", nil)
	install(t, store, "affine", nil)
	install(t, store, "authentik", nil)

	authentik := providerApp("authentik", 9001, "sso", catalog.ContractProvides{Secrets: []string{"apiToken"}})

	orch, secrets := bindingsOrchestrator(t, store,
		consumerApp("navidrome", "sso", catalog.Integration{Requires: requires("apiToken")}, "authentik"),
		consumerApp("affine", "sso", catalog.Integration{}, "authentik"),
		authentik,
	)
	secrets.publish("authentik", "apiToken", "admin-token")

	navidrome := orch.buildIntegrations("navidrome", consumerApp("navidrome", "sso", catalog.Integration{Requires: requires("apiToken")}, "authentik"))
	require.Len(t, navidrome.SSO, 1)
	assert.Equal(t, "admin-token", navidrome.SSO[0].APIToken, "the app that declared the requirement is handed the token")

	affine := orch.buildIntegrations("affine", consumerApp("affine", "sso", catalog.Integration{}, "authentik"))
	require.Len(t, affine.SSO, 1)
	assert.True(t, affine.SSO[0].Installed, "the binding is still resolved: the address is what a non-reader needs")
	assert.Empty(t, affine.SSO[0].APIToken, "and an app that did not declare the requirement is not handed the credential")
}
