// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// The load-bearing case of external apps: a remote install of a catalog app
// satisfies a consumer's contract binding with the consumer's configurator
// unchanged. The consumer reads ProviderRef.BaseURL and the contract payload;
// those fields carry the operator's endpoint and credentials instead of a
// container address and a boot-minted secret, and the consumer cannot tell.

// remoteAffine is the provider's catalog metadata exactly as apps/affine
// declares it: the containers, the port, the SSO block, and the appApi offer.
// Every one of those fields describes the local workload the operator chose not
// to run, which is why the isolation test asserts none of them materialize.
func remoteAffine() *catalog.App {
	return &catalog.App{
		CatalogID:   "affine",
		DisplayName: "AFFiNE",
		Port:        3010,
		Provides: catalog.Provides{
			"appApi": catalog.ContractProvides{
				Secrets:       []string{"password"},
				RuntimeValues: []string{"username", "workspaceId"},
			},
		},
		Containers: []catalog.ContainerDef{
			{Name: "apps-affine-postgres", Image: "pgvector/pgvector:pg16"},
			{Name: "apps-affine-redis", Image: "redis:7-alpine"},
			{Name: "apps-affine", Image: "affine:0.27.4"},
		},
	}
}

// affineMCPConsumer is the real consumer shape: it declares appApi, requires
// the password, and names affine as its provider.
func affineMCPConsumer() *catalog.App {
	app := consumerApp("affine-mcp", "appApi", catalog.Integration{
		Required: true,
		Requires: requires("password"),
	}, "affine")
	app.Headless = true
	app.Containers = []catalog.ContainerDef{{Name: "apps-affine-mcp", Image: "ghcr.io/dawncr0w/affine-mcp-server:3.8.5"}}
	return app
}

// externalProviderRecord is the operator-registered remote AFFiNE.
func externalProviderRecord(id, url string) *store.ExternalApp {
	return &store.ExternalApp{
		ID:     id,
		Kind:   string(store.ExternalAppKindProvider),
		Source: store.ExternalAppSourceForApp("affine"),
		Name:   "NAS AFFiNE",
		URL:    url,
		Values: `{"appApi":{"username":"op@example.com","workspaceId":"ws-42"}}`,
	}
}

// externalBindings wires an orchestrator with the catalog, the installed set,
// the secrets manager, and a real external-app registry backed by SQLite.
func externalBindings(t *testing.T, installed []string, records ...*store.ExternalApp) (*Orchestrator, *fakeSecrets, *store.ExternalAppStore) {
	t.Helper()
	cache := NewFakeCatalogCache()
	cache.AddApp(remoteAffine())
	cache.AddApp(affineMCPConsumer())

	appStore := NewFakeAppStore()
	for _, id := range installed {
		install(t, appStore, id, nil)
	}

	extStore := store.NewExternalAppStore(testdb.SetupTestDB(t))
	for _, rec := range records {
		require.NoError(t, extStore.Upsert(rec))
	}

	secrets := newFakeSecrets()
	registry := new(MockConfiguratorRegistry)
	registry.On("Get", mock.Anything).Return(nil).Maybe()

	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		registry,
		cache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{Stores: StoresConfig{
			AppStore:     appStore,
			Secrets:      secrets,
			ExternalApps: extStore,
		}},
	)
	orch.settings = newFakeSettings()
	return orch, secrets, extStore
}

// TestExternalAppResolvesAppAPIBinding is the whole feature in one assertion:
// the consumer's binding carries the operator's origin and credentials, in the
// same fields a local AFFiNE would have filled.
func TestExternalAppResolvesAppAPIBinding(t *testing.T) {
	orch, secrets, _ := externalBindings(t,
		[]string{"affine-mcp"},
		externalProviderRecord("ext-affine", "https://affine.example.com"),
	)
	secrets.publish(store.ExternalSecretScope("ext-affine"), "appApi", "the-remote-password")

	state := orch.buildAppState("affine-mcp")
	require.Len(t, state.Integrations.AppAPIs, 1)
	binding := state.Integrations.AppAPIs[0]

	assert.Equal(t, configurator.ProviderKindExternalApp, binding.Kind)
	assert.Equal(t, "affine", binding.App, "the catalog ID is retained: a remote AFFiNE is still AFFiNE")
	assert.True(t, binding.Installed, "the operator registered it, which is the same condition as the graph edge")
	assert.Empty(t, binding.Node, "there is no container to name")
	assert.Zero(t, binding.Port, "the port lives inside the endpoint")
	assert.Equal(t, "https://affine.example.com", binding.BaseURL)
	assert.Equal(t, "https://affine.example.com", binding.LocalURL)
	assert.Equal(t, "op@example.com", binding.Username)
	assert.Equal(t, "ws-42", binding.WorkspaceID)
	assert.Equal(t, "the-remote-password", binding.Password)
}

// TestExternalAppBindingWithoutLocalInstallStillResolves pins that the external
// record alone is enough: the consumer needs no local provider row, because
// `Installed` on the binding is the operator's registration, not a store row.
func TestExternalAppBindingWithoutLocalInstallStillResolves(t *testing.T) {
	orch, secrets, _ := externalBindings(t,
		[]string{"affine-mcp"},
		externalProviderRecord("ext-affine", "http://192.168.1.40:3010"),
	)
	secrets.publish(store.ExternalSecretScope("ext-affine"), "appApi", "lan-password")

	binding := orch.buildAppState("affine-mcp").Integrations.AppAPIs[0]
	assert.Equal(t, "http://192.168.1.40:3010", binding.BaseURL)
	assert.Equal(t, "lan-password", binding.Password)
}

// TestExternalAppSecretRespectsRequires is invariant 15 unchanged: a consumer
// that did not declare the secret does not get it, whether the provider is a
// container or a password the operator typed.
func TestExternalAppSecretRespectsRequires(t *testing.T) {
	cache := NewFakeCatalogCache()
	cache.AddApp(remoteAffine())
	cache.AddApp(consumerApp("noscret", "appApi", catalog.Integration{}, "affine"))

	extStore := store.NewExternalAppStore(testdb.SetupTestDB(t))
	require.NoError(t, extStore.Upsert(externalProviderRecord("ext-affine", "https://affine.example.com")))

	secrets := newFakeSecrets()
	secrets.publish(store.ExternalSecretScope("ext-affine"), "appApi", "should-not-be-handed-over")

	appStore := NewFakeAppStore()
	install(t, appStore, "noscret", nil)

	registry := new(MockConfiguratorRegistry)
	registry.On("Get", mock.Anything).Return(nil).Maybe()

	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		registry,
		cache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{Stores: StoresConfig{AppStore: appStore, Secrets: secrets, ExternalApps: extStore}},
	)
	orch.settings = newFakeSettings()

	binding := orch.buildAppState("noscret").Integrations.AppAPIs[0]
	assert.Empty(t, binding.Password, "a consumer that never required the secret must not be handed it")
	assert.Equal(t, "op@example.com", binding.Username, "the non-secret value still resolves")
}

// TestExternalAppLocalAndExternalDoNotCrossFeeds checks a local install's
// published credential is never used for the external record and vice versa:
// the scopes are separate, so a remote password cannot leak into a local
// provider's binding and a local secret cannot masquerade as the remote one.
func TestExternalAppLocalAndExternalDoNotCrossFeeds(t *testing.T) {
	orch, secrets, _ := externalBindings(t,
		[]string{"affine-mcp"},
		externalProviderRecord("ext-affine", "https://affine.example.com"),
	)
	secrets.publish("affine", "password", "LOCAL-AFFINE-PASSWORD")

	binding := orch.buildAppState("affine-mcp").Integrations.AppAPIs[0]
	assert.NotEqual(t, "LOCAL-AFFINE-PASSWORD", binding.Password, "the external record reads the external scope only")
	assert.Empty(t, binding.Password, "with nothing in the external scope the password is empty, never the local one")
}

// TestExternalAppProducesNoGraphNodesContainersOrRoutes is the non-negotiable
// regression gate from docs/plans/external-apps.md: the external record's
// catalog metadata still declares three containers and an SSO strategy, and
// none of it may ever be built, routed, or provisioned.
func TestExternalAppProducesNoGraphNodesContainersOrRoutes(t *testing.T) {
	orch, _, _ := externalBindings(t,
		[]string{"affine-mcp"},
		externalProviderRecord("ext-affine", "https://affine.example.com"),
	)

	installed := map[string]*store.InstalledApp{
		"affine-mcp": {CatalogID: "affine-mcp", DisplayName: "AFFiNE MCP"},
	}
	orch.populateGraphNodes(installed)

	nodes, err := orch.graph.Nodes()
	require.NoError(t, err)
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.ID)
	}
	assert.Equal(t, []string{"apps-affine-mcp"}, names,
		"an external app must contribute no node for itself or for the containers its catalog entry declares")
	for _, forbidden := range []string{"apps-affine", "apps-affine-postgres", "apps-affine-redis"} {
		assert.NotContains(t, names, forbidden)
	}
}

// TestAddExternalAppIntentWritesOnlyExternalScope proves the credential lands
// under external/<id> and never under the catalog app's own scope, which is
// what keeps a remote password from overwriting what a local install publishes.
func TestAddExternalAppIntentWritesOnlyExternalScope(t *testing.T) {
	orch, secrets, extStore := externalBindings(t, nil)

	orch.applyAddExternalAppIntent(NewAddExternalAppIntent(ExternalAppSpec{
		ID:      "ext-1",
		Kind:    string(store.ExternalAppKindProvider),
		Source:  store.ExternalAppSourceForApp("affine"),
		Name:    "NAS AFFiNE",
		URL:     "https://affine.example.com",
		Values:  map[string]map[string]string{"appApi": {"username": "op@example.com"}},
		Secrets: map[string]string{"appApi": "remote-secret"},
	}))

	assert.Equal(t, "remote-secret", secrets.GetAppSecret(store.ExternalSecretScope("ext-1"), "appApi"))
	assert.Empty(t, secrets.GetAppSecret("affine", "password"), "the catalog app's own scope is untouched")
	assert.Empty(t, secrets.GetAppSecret("affine", "appApi"), "nothing is written under the bare catalog ID")

	stored, err := extStore.Get("ext-1")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.NotContains(t, stored.Values, "remote-secret", "credentials never enter the external_apps table")
}

// TestRemoveExternalAppIntentClearsItsScope: a deleted record must not leave a
// live credential behind under an id nothing references.
func TestRemoveExternalAppIntentClearsItsScope(t *testing.T) {
	orch, secrets, extStore := externalBindings(t, nil)
	require.NoError(t, secrets.SetAppSecret(store.ExternalSecretScope("ext-1"), "appApi", "remote-secret"))
	require.NoError(t, extStore.Upsert(externalProviderRecord("ext-1", "https://affine.example.com")))

	orch.applyRemoveExternalAppIntent(NewRemoveExternalAppIntent("ext-1"))

	assert.Empty(t, secrets.GetAppSecret(store.ExternalSecretScope("ext-1"), "appApi"))
	got, err := extStore.Get("ext-1")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestUpdateExternalAppIntentKeepsKindAndSource: an edit cannot mutate what a
// record is.
func TestUpdateExternalAppIntentKeepsKindAndSource(t *testing.T) {
	orch, _, extStore := externalBindings(t, nil)
	require.NoError(t, extStore.Upsert(externalProviderRecord("ext-1", "https://old.example.com")))

	orch.applyUpdateExternalAppIntent(NewUpdateExternalAppIntent(ExternalAppSpec{
		ID:   "ext-1",
		Kind: string(store.ExternalAppKindLauncher),
		Name: "Renamed",
		URL:  "https://new.example.com",
	}))

	got, err := extStore.Get("ext-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, string(store.ExternalAppKindProvider), got.Kind)
	assert.Equal(t, store.ExternalAppSourceForApp("affine"), got.Source)
	assert.Equal(t, "Renamed", got.Name)
	assert.Equal(t, "https://new.example.com", got.URL)
}

// TestExternalAppDoesNotAffectLocalResolution: with no external record, the
// same consumer resolves the local provider exactly as before. The external
// path is additive and never intercepts a local install.
func TestExternalAppDoesNotAffectLocalResolution(t *testing.T) {
	orch, secrets, _ := externalBindings(t, []string{"affine-mcp", "affine"})
	secrets.publish("affine", "password", "local-password")
	secrets.values = map[string]string{"affine/appApi/username": "boot@example.com"}

	binding := orch.buildAppState("affine-mcp").Integrations.AppAPIs[0]
	assert.Equal(t, configurator.ProviderKindApp, binding.Kind)
	assert.Equal(t, "http://apps-affine:3010", binding.BaseURL)
	assert.Equal(t, "boot@example.com", binding.Username)
	assert.Equal(t, "local-password", binding.Password)
}

// TestInstallConsumerDoesNotInstallARemoteProviderLocally is the install side
// of the XOR rule. affine-mcp declares appApi as required with affine as its
// default provider, so a plain install pulls AFFiNE's four containers in. When
// the operator has registered a remote AFFiNE, that must not happen: the
// contract is already answered, and a consumer that cannot tell two bindings
// of the same catalog ID apart must never face both.
func TestInstallConsumerDoesNotInstallARemoteProviderLocally(t *testing.T) {
	ctx := context.Background()
	cache := NewFakeCatalogCache()
	cache.AddApp(remoteAffine())
	cache.AddApp(affineMCPConsumer())

	appStore := NewFakeAppStore()
	appGraph := NewFakeAppGraph()
	appGraph.SetInstallPlan("affine-mcp", &catalog.InstallPlan{
		App:        "affine-mcp",
		CanInstall: true,
		RequiredProviders: []catalog.ConfigTask{
			{Target: "affine-mcp", Source: "affine", Integration: "appApi"},
		},
	})

	extStore := store.NewExternalAppStore(testdb.SetupTestDB(t))
	require.NoError(t, extStore.Upsert(externalProviderRecord("ext-affine", "https://affine.example.com")))

	registry := new(MockConfiguratorRegistry)
	registry.On("Get", mock.Anything).Return(nil).Maybe()

	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		registry,
		cache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{
			Stores:       StoresConfig{AppStore: appStore, ExternalApps: extStore},
			CatalogGraph: appGraph,
		},
	)
	orch.settings = newFakeSettings()

	orch.converge(ctx, []Intent{NewInstallAppIntent("affine-mcp")})

	installed, err := appStore.IsInstalled("affine")
	require.NoError(t, err)
	assert.False(t, installed, "a remote provider must not be installed locally behind the consumer's back")

	installed, err = appStore.IsInstalled("affine-mcp")
	require.NoError(t, err)
	assert.True(t, installed, "the consumer itself still installs")
}

// TestInstallConsumerStillInstallsALocalDefaultProvider is the control: with
// nothing in the external registry, the required default provider installs as
// it always has.
func TestInstallConsumerStillInstallsALocalDefaultProvider(t *testing.T) {
	ctx := context.Background()
	cache := NewFakeCatalogCache()
	cache.AddApp(remoteAffine())
	cache.AddApp(affineMCPConsumer())

	appStore := NewFakeAppStore()
	appGraph := NewFakeAppGraph()
	appGraph.SetInstallPlan("affine-mcp", &catalog.InstallPlan{
		App:        "affine-mcp",
		CanInstall: true,
		RequiredProviders: []catalog.ConfigTask{
			{Target: "affine-mcp", Source: "affine", Integration: "appApi"},
		},
	})

	registry := new(MockConfiguratorRegistry)
	registry.On("Get", mock.Anything).Return(nil).Maybe()

	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		registry,
		cache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{Stores: StoresConfig{AppStore: appStore}, CatalogGraph: appGraph},
	)
	orch.settings = newFakeSettings()

	orch.converge(ctx, []Intent{NewInstallAppIntent("affine-mcp")})

	installed, err := appStore.IsInstalled("affine")
	require.NoError(t, err)
	assert.True(t, installed, "with no external record the declared default provider installs as before")
}

// TestUpdateExternalAppPreservesValuesAndCredential is the regression for a
// partial edit. Renaming a remote provider, or moving its endpoint, says
// nothing about its contract payload, so the values and the stored credential
// must survive the update. A wipe here is not cosmetic: the consumer's binding
// is built from those keys, so the next resync would write an unauthenticated
// config against a provider that is otherwise perfectly wired.
func TestUpdateExternalAppPreservesValuesAndCredential(t *testing.T) {
	orch, secrets, extStore := externalBindings(t,
		[]string{"affine-mcp"},
		externalProviderRecord("ext-affine", "https://old.example.com"),
	)
	secrets.publish(store.ExternalSecretScope("ext-affine"), "appApi", "remote-password")

	orch.applyUpdateExternalAppIntent(NewUpdateExternalAppIntent(ExternalAppSpec{
		ID:  "ext-affine",
		URL: "http://new.example.com:3010",
	}))

	binding := orch.buildAppState("affine-mcp").Integrations.AppAPIs[0]
	assert.Equal(t, "http://new.example.com:3010", binding.BaseURL, "the new endpoint lands")
	assert.Equal(t, "op@example.com", binding.Username, "the payload the update did not name is preserved")
	assert.Equal(t, "ws-42", binding.WorkspaceID)
	assert.Equal(t, "remote-password", binding.Password, "the credential is not disturbed by an edit that never mentions it")

	stored, err := extStore.Get("ext-affine")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Contains(t, stored.Values, "workspaceId")
}

// TestUpdateExternalAppCanReplaceValues is the other half: an update that does
// name the payload replaces it wholesale, so the form is not stuck on the first
// values it was saved with.
func TestUpdateExternalAppCanReplaceValues(t *testing.T) {
	orch, _, _ := externalBindings(t,
		[]string{"affine-mcp"},
		externalProviderRecord("ext-affine", "https://old.example.com"),
	)

	orch.applyUpdateExternalAppIntent(NewUpdateExternalAppIntent(ExternalAppSpec{
		ID:     "ext-affine",
		URL:    "https://old.example.com",
		Values: map[string]map[string]string{"appApi": {"username": "someone@else.com", "workspaceId": "ws-99"}},
	}))

	binding := orch.buildAppState("affine-mcp").Integrations.AppAPIs[0]
	assert.Equal(t, "someone@else.com", binding.Username)
	assert.Equal(t, "ws-99", binding.WorkspaceID)
}

// The generalization the `source: setting` rename was for: a consumer that
// declares a role gets any off-host record the operator declared for that
// role, with no catalog app in between. This is the `contract:<name>` path on
// a contract other than inference, which is where it will be used next.
func TestExternalContractProviderResolvesForASettingConsumer(t *testing.T) {
	consumer := &catalog.App{
		CatalogID: "consumer",
		Integrations: map[string]catalog.Integration{
			"pvr": {
				Requires:   []string{"apiKey"},
				Compatible: []catalog.CompatibleApp{{Source: catalog.SettingProviderSource}},
			},
		},
	}
	cache := NewFakeCatalogCache()
	cache.AddApp(consumer)

	appStore := NewFakeAppStore()
	install(t, appStore, "consumer", nil)

	extStore := store.NewExternalAppStore(testdb.SetupTestDB(t))
	require.NoError(t, extStore.Upsert(&store.ExternalApp{
		ID:     "ext-radarr",
		Kind:   string(store.ExternalAppKindProvider),
		Source: store.ExternalAppSourceForContract("pvr"),
		Name:   "Off-host Radarr",
		URL:    "https://radarr.example.com",
	}))

	secrets := newFakeSecrets()
	secrets.publish(store.ExternalSecretScope("ext-radarr"), "pvr", "off-host-key")

	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		new(MockConfiguratorRegistry),
		cache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{Stores: StoresConfig{
			AppStore:     appStore,
			Secrets:      secrets,
			ExternalApps: extStore,
		}},
	)

	out := orch.buildIntegrations("consumer", consumer)

	require.Len(t, out.PVRs, 1, "the off-host record fills the role the consumer declared")
	binding := out.PVRs[0]
	assert.Equal(t, "ext-radarr", binding.App)
	assert.Equal(t, configurator.ProviderKindSetting, binding.Kind)
	assert.True(t, binding.Installed)
	assert.Equal(t, "https://radarr.example.com", binding.BaseURL)
	assert.Equal(t, "off-host-key", binding.APIKey)
}

// A record declared for one contract must not satisfy a consumer of another.
// The match is on the contract name, not on "the operator put something in
// the registry".
func TestExternalContractProviderDoesNotLeakAcrossContracts(t *testing.T) {
	consumer := &catalog.App{
		CatalogID: "consumer",
		Integrations: map[string]catalog.Integration{
			"pvr": {Compatible: []catalog.CompatibleApp{{Source: catalog.SettingProviderSource}}},
		},
	}
	cache := NewFakeCatalogCache()
	cache.AddApp(consumer)

	appStore := NewFakeAppStore()
	install(t, appStore, "consumer", nil)

	extStore := store.NewExternalAppStore(testdb.SetupTestDB(t))
	require.NoError(t, extStore.Upsert(&store.ExternalApp{
		ID:     "ext-other",
		Kind:   string(store.ExternalAppKindProvider),
		Source: store.ExternalAppSourceForContract("downloadClient"),
		Name:   "Off-host download client",
		URL:    "https://downloads.example.com",
	}))

	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		new(MockConfiguratorRegistry),
		cache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{Stores: StoresConfig{
			AppStore:     appStore,
			ExternalApps: extStore,
		}},
	)

	out := orch.buildIntegrations("consumer", consumer)
	assert.Empty(t, out.PVRs, "a downloadClient record is not a PVR")
}

// A remote Jellyfin has whatever admin account the operator made there. The
// username travels as a contract value so the consumer reads it off the
// binding instead of assuming the name of the account Bloud would have created,
// and the address travels as the record's own URL rather than a container name.
func TestRemoteMediaServerSuppliesItsOwnAdminLoginAndAddress(t *testing.T) {
	consumer := consumerApp("seerr", "mediaServer", catalog.Integration{
		Requires: requires("adminPassword"),
	}, "jellyfin")
	cache := NewFakeCatalogCache()
	cache.AddApp(consumer)
	cache.AddApp(providerApp("jellyfin", 8096, "mediaServer", catalog.ContractProvides{
		Secrets: []string{"adminPassword"},
		Values:  map[string]string{"adminUsername": "bloud-bootstrap-admin"},
	}))

	extStore := store.NewExternalAppStore(testdb.SetupTestDB(t))
	require.NoError(t, extStore.Upsert(&store.ExternalApp{
		ID:     "nas-jellyfin",
		Kind:   string(store.ExternalAppKindProvider),
		Source: store.ExternalAppSourceForApp("jellyfin"),
		Name:   "NAS Jellyfin",
		URL:    "https://jellyfin.example.com",
		Values: `{"mediaServer":{"adminUsername":"daniel"}}`,
	}))

	appStore := NewFakeAppStore()
	install(t, appStore, "seerr", nil)

	secrets := newFakeSecrets()
	secrets.publish("external/nas-jellyfin", "mediaServer", "the-remote-password")

	registry := new(MockConfiguratorRegistry)
	registry.On("Get", mock.Anything).Return(nil).Maybe()
	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()), registry, cache, "/tmp/bloud-test", newTestLogger(),
		OrchestratorConfig{Stores: StoresConfig{AppStore: appStore, Secrets: secrets, ExternalApps: extStore}},
	)
	orch.settings = newFakeSettings()

	out := orch.buildIntegrations("seerr", consumer)
	require.Len(t, out.MediaServers, 1)
	got := out.MediaServers[0]
	assert.Equal(t, "daniel", got.AdminUsername, "the operator's account name, not the local constant")
	assert.Equal(t, "the-remote-password", got.AdminPassword)
	assert.Equal(t, "https://jellyfin.example.com", got.BaseURL)
}

// The local Jellyfin publishes the managed bootstrap account it created, so a
// locally-installed provider binds with that name rather than nothing.
func TestLocalMediaServerPublishesItsBootstrapUsername(t *testing.T) {
	consumer := consumerApp("seerr", "mediaServer", catalog.Integration{
		Requires: requires("adminPassword"),
	}, "jellyfin")
	storeApp := providerApp("jellyfin", 8096, "mediaServer", catalog.ContractProvides{
		Secrets: []string{"adminPassword"},
		Values:  map[string]string{"adminUsername": "bloud-bootstrap-admin"},
	})
	cache := NewFakeCatalogCache()
	cache.AddApp(consumer)
	cache.AddApp(storeApp)

	appStore := NewFakeAppStore()
	install(t, appStore, "seerr", nil)
	install(t, appStore, "jellyfin", nil)

	secrets := newFakeSecrets()
	secrets.publish("jellyfin", "adminPassword", "local-bootstrap-pw")

	registry := new(MockConfiguratorRegistry)
	registry.On("Get", mock.Anything).Return(nil).Maybe()
	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()), registry, cache, "/tmp/bloud-test", newTestLogger(),
		OrchestratorConfig{Stores: StoresConfig{AppStore: appStore, Secrets: secrets}},
	)
	orch.settings = newFakeSettings()

	out := orch.buildIntegrations("seerr", consumer)
	require.Len(t, out.MediaServers, 1)
	assert.Equal(t, "bloud-bootstrap-admin", out.MediaServers[0].AdminUsername)
	assert.Equal(t, "local-bootstrap-pw", out.MediaServers[0].AdminPassword)
}
