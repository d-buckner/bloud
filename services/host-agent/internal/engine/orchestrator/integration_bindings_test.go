// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
)

// fakeSecrets is an in-memory AppSecretsProvider: the published bag the
// orchestrator resolves a contract's credentials from.
type fakeSecrets struct {
	published map[string]map[string]string
	values    map[string]string
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{published: make(map[string]map[string]string), values: make(map[string]string)}
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

func (f *fakeSecrets) SetAppContractValue(appName, contract, key, value string) error {
	if f.values == nil {
		f.values = make(map[string]string)
	}
	f.values[appName+"/"+contract+"/"+key] = value
	return nil
}

func (f *fakeSecrets) GetAppContractValue(appName, contract, key string) string {
	return f.values[appName+"/"+contract+"/"+key]
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
		OrchestratorConfig{Stores: StoresConfig{AppStore: appStore, Secrets: secrets}},
	)
	// Every binding test gets a settings store so the instance can act as a
	// contract provider; an empty map reads as "nothing configured".
	orch.settings = newFakeSettings()
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
	store := NewFakeAppStore()
	install(t, store, "prowlarr", nil)
	install(t, store, "sonarr", nil)

	orch, secrets := bindingsOrchestrator(t, store,
		consumer,
		providerApp("sonarr", 8989, "pvr", catalog.ContractProvides{Secrets: []string{"apiKey"}}),
		providerApp("radarr", 7878, "pvr", catalog.ContractProvides{Secrets: []string{"apiKey"}}),
	)
	secrets.publish("sonarr", "apiKey", "sonarr-key")

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

// The set model: every installed declared provider binds, whatever a defunct
// choice system recorded. The stale `downloadClient: deluge` record is ignored,
// and both installed download clients bind; an optional contract does the same
// for the providers it declares.
func TestBuildIntegrations_BindsEveryInstalledDeclaredProvider(t *testing.T) {
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

	require.Len(t, out.DownloadClients, 2, "every installed declared provider binds; nothing is chosen")
	assert.ElementsMatch(t,
		[]string{out.DownloadClients[0].App, out.DownloadClients[1].App},
		[]string{"qbittorrent", "deluge"})
	require.Len(t, out.PVRs, 1, "an optional contract binds the declared providers")
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

// An MCP provider whose endpoint shape is known up front declares both values in
// metadata. The binding carries the path and the namespace separately and never a
// composed URL, because the consumer picks which address its own network position
// can dial.
func TestBuildIntegrations_MCPStaticValues(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "hermes", nil)
	install(t, store, "simple-mcp", nil)

	orch, secrets := bindingsOrchestrator(t, store,
		consumerApp("hermes", "mcp", catalog.Integration{Requires: requires("httpToken")}, "simple-mcp"),
		providerApp("simple-mcp", 3011, "mcp", catalog.ContractProvides{
			Secrets: []string{"httpToken"},
			Values:  map[string]string{"path": "/mcp", "serverName": "simple"},
		}),
	)
	secrets.publish("simple-mcp", "httpToken", "bearer-1")

	out := orch.buildIntegrations("hermes", consumerApp("hermes", "mcp", catalog.Integration{Requires: requires("httpToken")}, "simple-mcp"))
	require.Len(t, out.MCPServers, 1)
	binding := out.MCPServers[0]
	assert.Equal(t, "simple", binding.ServerName)
	assert.Equal(t, "/mcp", binding.Path)
	assert.Equal(t, "bearer-1", binding.Token)
	assert.Equal(t, "http://apps-simple-mcp:3011", binding.BaseURL)
	assert.Equal(t, "http://localhost:3011", binding.LocalURL)
}

// A provider whose endpoint path its own app mints declares the key under
// `runtimeValues`, and the published value wins over anything in metadata. This
// is the AFFiNE shape: /api/workspaces/<id>/mcp, where the id is created by
// AFFiNE on first boot.
func TestBuildIntegrations_MCPRuntimePublishedValue(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "hermes", nil)
	install(t, store, "affine", nil)

	offer := catalog.ContractProvides{
		Secrets:       []string{"httpToken"},
		Values:        map[string]string{"serverName": "affine"},
		RuntimeValues: []string{"path"},
	}
	orch, secrets := bindingsOrchestrator(t, store,
		consumerApp("hermes", "mcp", catalog.Integration{Requires: requires("httpToken")}, "affine"),
		providerApp("affine", 3010, "mcp", offer),
	)
	require.NoError(t, secrets.SetAppContractValue("affine", "mcp", "path", "/api/workspaces/ws-42/mcp"))
	secrets.publish("affine", "httpToken", "aff_mcp_v1.cred.secret")

	out := orch.buildIntegrations("hermes", consumerApp("hermes", "mcp", catalog.Integration{Requires: requires("httpToken")}, "affine"))
	require.Len(t, out.MCPServers, 1)
	binding := out.MCPServers[0]
	assert.Equal(t, "/api/workspaces/ws-42/mcp", binding.Path, "the runtime-published path is what the consumer gets")
	assert.Equal(t, "affine", binding.ServerName, "a value the provider did declare statically still resolves")
	assert.Equal(t, "aff_mcp_v1.cred.secret", binding.Token)
}

// A runtime value the provider has not published yet reads as empty, which the
// consumer must treat as "not ready" rather than as a path to write down. The
// harness filter (skip anything with an empty token) is what keeps this from
// registering a broken tool namespace; the empty path is the same signal one
// level earlier.
func TestBuildIntegrations_MCPNotPublishedYet(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "hermes", nil)
	install(t, store, "affine", nil)

	orch, _ := bindingsOrchestrator(t, store,
		consumerApp("hermes", "mcp", catalog.Integration{Requires: requires("httpToken")}, "affine"),
		providerApp("affine", 3010, "mcp", catalog.ContractProvides{
			Secrets:       []string{"httpToken"},
			Values:        map[string]string{"serverName": "affine"},
			RuntimeValues: []string{"path"},
		}),
	)

	out := orch.buildIntegrations("hermes", consumerApp("hermes", "mcp", catalog.Integration{Requires: requires("httpToken")}, "affine"))
	require.Len(t, out.MCPServers, 1, "an unready provider is still bound, so the harness can prune an entry Bloud wrote for it")
	assert.Empty(t, out.MCPServers[0].Path, "the runtime value is empty until the provider publishes it")
	assert.Empty(t, out.MCPServers[0].Token, "and so is the token")
	assert.True(t, out.MCPServers[0].Installed)
}

// A harness that declares `mcp` without requiring the token gets the address and
// the namespace but no credential, the same least-privilege rule every other
// contract enforces.
func TestBuildIntegrations_MCPTokenOnlyForDeclaredRequires(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "reader", nil)
	install(t, store, "simple-mcp", nil)

	orch, secrets := bindingsOrchestrator(t, store,
		consumerApp("reader", "mcp", catalog.Integration{}, "simple-mcp"),
		providerApp("simple-mcp", 3011, "mcp", catalog.ContractProvides{
			Secrets: []string{"httpToken"},
			Values:  map[string]string{"path": "/mcp", "serverName": "simple"},
		}),
	)
	secrets.publish("simple-mcp", "httpToken", "bearer-1")

	out := orch.buildIntegrations("reader", consumerApp("reader", "mcp", catalog.Integration{}, "simple-mcp"))
	require.Len(t, out.MCPServers, 1)
	assert.Empty(t, out.MCPServers[0].Token, "declaring the contract does not by itself hand over the bearer")
	assert.Equal(t, "/mcp", out.MCPServers[0].Path, "the non-secret values are still available")
}

// An appApi provider hands a companion the account it signs in with when it
// cannot join the identity provider. The username is a non-secret runtime value
// (the account the provider bootstrapped) and the password is the secret.
func TestBuildIntegrations_AppAPIUsernameValueAndPasswordSecret(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "affine-mcp", nil)
	install(t, store, "affine", nil)

	orch, secrets := bindingsOrchestrator(t, store,
		consumerApp("affine-mcp", "appApi", catalog.Integration{Requires: requires("password")}, "affine"),
		providerApp("affine", 3010, "appApi", catalog.ContractProvides{
			Secrets:       []string{"password"},
			RuntimeValues: []string{"username", "workspaceId"},
		}),
	)
	require.NoError(t, secrets.SetAppContractValue("affine", "appApi", "username", "admin@affine.localhost"))
	require.NoError(t, secrets.SetAppContractValue("affine", "appApi", "workspaceId", "ws-shared-1"))
	secrets.publish("affine", "password", "owner-password")

	out := orch.buildIntegrations("affine-mcp", consumerApp("affine-mcp", "appApi", catalog.Integration{Requires: requires("password")}, "affine"))
	require.Len(t, out.AppAPIs, 1)
	binding := out.AppAPIs[0]
	assert.Equal(t, "admin@affine.localhost", binding.Username, "the username is a value, not a credential")
	assert.Equal(t, "owner-password", binding.Password)
	assert.Equal(t, "ws-shared-1", binding.WorkspaceID, "the provider's default scope travels with the credential")
	assert.Equal(t, "http://apps-affine:3010", binding.BaseURL)
}

// A consumer that declares `appApi` without requiring the secret gets the
// username and no password, the same least-privilege rule every other contract
// enforces.
func TestBuildIntegrations_AppAPIPasswordOnlyForDeclaredRequires(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "reader", nil)
	install(t, store, "affine", nil)

	orch, secrets := bindingsOrchestrator(t, store,
		consumerApp("reader", "appApi", catalog.Integration{}, "affine"),
		providerApp("affine", 3010, "appApi", catalog.ContractProvides{
			Secrets:       []string{"password"},
			RuntimeValues: []string{"username", "workspaceId"},
		}),
	)
	require.NoError(t, secrets.SetAppContractValue("affine", "appApi", "username", "admin@affine.localhost"))
	require.NoError(t, secrets.SetAppContractValue("affine", "appApi", "workspaceId", "ws-shared-1"))
	secrets.publish("affine", "password", "owner-password")

	out := orch.buildIntegrations("reader", consumerApp("reader", "appApi", catalog.Integration{}, "affine"))
	require.Len(t, out.AppAPIs, 1)
	assert.Equal(t, "admin@affine.localhost", out.AppAPIs[0].Username)
	assert.Equal(t, "ws-shared-1", out.AppAPIs[0].WorkspaceID, "the scope is a value, so a non-reader still gets it")
	assert.Empty(t, out.AppAPIs[0].Password,
		"declaring the contract does not by itself hand over the password")
}

// A CalDAV provider hands its consumer the address and the DAV root, and no
// credential of any kind. The contract carries no secret because the credential
// is the person's own password, which their client sends to the provider
// directly; nothing in Bloud's secret store is read on this path.
func TestBuildIntegrations_CalDAVAddressOnlyNoCredential(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "calino", nil)
	install(t, store, "radicale", nil)

	orch, secrets := bindingsOrchestrator(t, store,
		consumerApp("calino", "caldav", catalog.Integration{}, "radicale"),
		providerApp("radicale", 5232, "caldav", catalog.ContractProvides{
			Values: map[string]string{"path": "/"},
		}),
	)
	// A credential stored under the provider is still not handed over: the
	// contract names none, so there is no field for it to arrive in.
	secrets.publish("radicale", "apiKey", "must-not-travel")

	out := orch.buildIntegrations("calino", consumerApp("calino", "caldav", catalog.Integration{}, "radicale"))
	require.Len(t, out.CalDAVServers, 1)
	binding := out.CalDAVServers[0]
	assert.Equal(t, "radicale", binding.App)
	assert.Equal(t, "apps-radicale", binding.Node)
	assert.Equal(t, "http://apps-radicale:5232", binding.BaseURL)
	assert.Equal(t, "/", binding.Path)
	assert.True(t, binding.Installed)
}

// The public half of the address is the part a browser-based consumer cannot
// derive for itself, and the container address is useless to it, so the binding
// carries the app subdomain of the instance's live public URL.
func TestBuildIntegrations_CalDAVPublicURLFollowsTheLiveHostSet(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "calino", nil)
	install(t, store, "radicale", nil)

	public, err := hostset.ParsePublicURL("https://home.example.com")
	require.NoError(t, err)

	cache := NewFakeCatalogCache()
	cache.AddApp(consumerApp("calino", "caldav", catalog.Integration{}, "radicale"))
	cache.AddApp(providerApp("radicale", 5232, "caldav", catalog.ContractProvides{
		Values: map[string]string{"path": "/"},
	}))
	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		new(MockConfiguratorRegistry),
		cache,
		t.TempDir(),
		newTestLogger(),
		OrchestratorConfig{Stores: StoresConfig{AppStore: store}, Hosts: HostsConfig{Hosts: hostset.NewState(hostset.New(public))}},
	)

	out := orch.buildIntegrations("calino", consumerApp("calino", "caldav", catalog.Integration{}, "radicale"))
	require.Len(t, out.CalDAVServers, 1)
	assert.Equal(t, "https://radicale.home.example.com", out.CalDAVServers[0].PublicURL)
}

// A provider that is not installed is still bound, with Installed false, so a
// consumer that wrote an entry for it can recognize and prune that entry. The
// address still resolves from catalog metadata, which is what a prune needs.
func TestBuildIntegrations_CalDAVProviderNotInstalled(t *testing.T) {
	store := NewFakeAppStore()
	install(t, store, "calino", nil)

	orch, _ := bindingsOrchestrator(t, store,
		consumerApp("calino", "caldav", catalog.Integration{}, "radicale"),
		providerApp("radicale", 5232, "caldav", catalog.ContractProvides{
			Values: map[string]string{"path": "/"},
		}),
	)

	out := orch.buildIntegrations("calino", consumerApp("calino", "caldav", catalog.Integration{}, "radicale"))
	require.Len(t, out.CalDAVServers, 1)
	assert.False(t, out.CalDAVServers[0].Installed)
	assert.Equal(t, "/", out.CalDAVServers[0].Path)
}

// An ICS feed binding carries the provider's address and the facts the
// consumer needs to compose the feed URL and name the calendar it creates. The
// key is resolved only because the consumer declared it.
func TestBuildIntegrations_ICSFeedCarriesTheKeyAndFeedFacts(t *testing.T) {
	consumer := consumerApp("radicale", "icsFeed", catalog.Integration{Requires: requires("apiKey")}, "radarr")
	store := NewFakeAppStore()
	install(t, store, "radicale", nil)
	install(t, store, "radarr", nil)

	orch, secrets := bindingsOrchestrator(t, store,
		consumer,
		providerApp("radarr", 7878, "icsFeed", catalog.ContractProvides{
			Secrets: []string{"apiKey"},
			Values: map[string]string{
				"path":        "/feed/v3/calendar/Radarr.ics",
				"displayName": "Radarr Movies",
			},
		}),
	)
	secrets.publish("radarr", "apiKey", "radarr-key")

	out := orch.buildIntegrations("radicale", consumer)

	require.Len(t, out.ICSFeeds, 1)
	feed := out.ICSFeeds[0]
	assert.Equal(t, "radarr", feed.App)
	assert.Equal(t, "apps-radarr", feed.Node)
	assert.Equal(t, "http://apps-radarr:7878", feed.BaseURL, "the plugin fetches from the app network")
	assert.Equal(t, "/feed/v3/calendar/Radarr.ics", feed.Path)
	assert.Equal(t, "Radarr Movies", feed.DisplayName)
	assert.Equal(t, "radarr-key", feed.APIKey, "the consumer declared it requires the key")
}

// Least privilege: a consumer that did not list apiKey under `requires` gets an
// empty field, which it reads as "not published" and writes no sync job for.
func TestBuildIntegrations_ICSFeedKeyOnlyForDeclaredRequires(t *testing.T) {
	consumer := consumerApp("radicale", "icsFeed", catalog.Integration{}, "radarr")
	store := NewFakeAppStore()
	install(t, store, "radicale", nil)
	install(t, store, "radarr", nil)

	orch, secrets := bindingsOrchestrator(t, store,
		consumer,
		providerApp("radarr", 7878, "icsFeed", catalog.ContractProvides{
			Secrets: []string{"apiKey"},
			Values: map[string]string{
				"path":        "/feed/v3/calendar/Radarr.ics",
				"displayName": "Radarr Movies",
			},
		}),
	)
	secrets.publish("radarr", "apiKey", "radarr-key")

	out := orch.buildIntegrations("radicale", consumer)

	require.Len(t, out.ICSFeeds, 1)
	assert.Empty(t, out.ICSFeeds[0].APIKey)
}
