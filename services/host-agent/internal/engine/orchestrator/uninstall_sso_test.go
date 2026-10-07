// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// Uninstalling an app has to remove the identity-provider objects Bloud
// created for it, not just its containers, nodes, routes, and row. An OAuth2
// provider is a live client credential with redirect URIs: left behind, it is
// stale trust sitting in the IdP with nothing behind it, invisible to the
// dashboard and accumulating across install/uninstall cycles.
func TestConvergeUninstalls_DeprovisionsTheAppsSSO(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "hermes", DisplayName: "Hermes", Status: store.AppStatusUninstalling})
	require.NoError(t, apps.SetSSOStrategy("hermes", "native-oidc"))

	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{CatalogID: "hermes", DisplayName: "Hermes", SSO: catalog.SSO{Strategy: "native-oidc"}})

	sso := new(MockSSOProvisioner)
	sso.On("Deprovision", "hermes", "Hermes", "native-oidc").Return(nil)

	g := graph.New(graph.NewMapRepository())
	require.NoError(t, g.AddNode("hermes"))

	registry := new(MockConfiguratorRegistry)
	registry.On("Get", "hermes").Return(nil).Maybe()

	orch := NewOrchestrator(g, registry, cat, t.TempDir(), newTestLogger(),
		OrchestratorConfig{SSO: SSOConfig{SSO: sso}, Stores: StoresConfig{AppStore: apps}})

	appList, err := apps.GetAll()
	require.NoError(t, err)
	appMap := map[string]*store.InstalledApp{"hermes": appList[0]}

	orch.convergeUninstalls(context.Background(), appList, appMap, map[string]bool{"hermes": true})

	sso.AssertCalled(t, "Deprovision", "hermes", "Hermes", "native-oidc")
	ids, err := apps.GetInstalledCatalogIDs()
	require.NoError(t, err)
	assert.NotContains(t, ids, "hermes", "the uninstall completes once the IdP objects are gone")
	node, err := g.GetNode("hermes")
	require.NoError(t, err)
	assert.Nil(t, node, "the graph node is removed as before")
}

// The strategy is read from the store, not the catalog, because the catalog
// entry can be refreshed or gone by the time an uninstall converges. A Bloud
// upgrade that changed the declared strategy between install and uninstall is
// the normal case, and the stored value is the record of what was provisioned.
func TestConvergeUninstalls_DeprovisionsStoredStrategyNotCatalogStrategy(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "demo", DisplayName: "Demo", Status: store.AppStatusUninstalling})
	require.NoError(t, apps.SetSSOStrategy("demo", "forward-auth"))

	// The catalog now declares something else entirely.
	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{CatalogID: "demo", DisplayName: "Demo", SSO: catalog.SSO{Strategy: "native-oidc"}})

	sso := new(MockSSOProvisioner)
	sso.On("Deprovision", "demo", "Demo", "forward-auth").Return(nil)

	registry := new(MockConfiguratorRegistry)
	registry.On("Get", "demo").Return(nil).Maybe()

	orch := NewOrchestrator(graph.New(graph.NewMapRepository()), registry, cat, t.TempDir(),
		newTestLogger(), OrchestratorConfig{SSO: SSOConfig{SSO: sso}, Stores: StoresConfig{AppStore: apps}})

	appList, err := apps.GetAll()
	require.NoError(t, err)
	orch.convergeUninstalls(context.Background(), appList, map[string]*store.InstalledApp{"demo": appList[0]}, nil)

	sso.AssertCalled(t, "Deprovision", "demo", "Demo", "forward-auth")
	sso.AssertNotCalled(t, "Deprovision", "demo", "Demo", "native-oidc")
}

// When the catalog entry is gone the store's display name is all that is left.
// The name only feeds the fallback lookup in DeleteAppSSO (the primary key is
// the application slug), so a missing catalog entry must degrade to the store
// value rather than skip the deprovision.
func TestConvergeUninstalls_FallsBackToStoreDisplayName(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "orphan", DisplayName: "Stored Name", Status: store.AppStatusUninstalling})
	require.NoError(t, apps.SetSSOStrategy("orphan", "native-oidc"))

	sso := new(MockSSOProvisioner)
	sso.On("Deprovision", "orphan", "Stored Name", "native-oidc").Return(nil)

	registry := new(MockConfiguratorRegistry)
	registry.On("Get", "orphan").Return(nil).Maybe()

	orch := NewOrchestrator(graph.New(graph.NewMapRepository()), registry, NewFakeCatalogCache(),
		t.TempDir(), newTestLogger(),
		OrchestratorConfig{SSO: SSOConfig{SSO: sso}, Stores: StoresConfig{AppStore: apps}})

	appList, err := apps.GetAll()
	require.NoError(t, err)
	orch.convergeUninstalls(context.Background(), appList, map[string]*store.InstalledApp{"orphan": appList[0]}, nil)

	sso.AssertCalled(t, "Deprovision", "orphan", "Stored Name", "native-oidc")
}

// An app that never provisioned an IdP object must not make the call, and must
// still uninstall cleanly.
func TestConvergeUninstalls_NoStrategyMeansNoDeprovision(t *testing.T) {
	for _, strategy := range []string{"", "none"} {
		t.Run("strategy_"+strategy, func(t *testing.T) {
			apps := NewFakeAppStore()
			apps.AddApp(&store.InstalledApp{CatalogID: "plain", DisplayName: "Plain", Status: store.AppStatusUninstalling})
			if strategy != "" {
				require.NoError(t, apps.SetSSOStrategy("plain", strategy))
			}

			sso := new(MockSSOProvisioner)
			registry := new(MockConfiguratorRegistry)
			registry.On("Get", "plain").Return(nil).Maybe()

			orch := NewOrchestrator(graph.New(graph.NewMapRepository()), registry,
				NewFakeCatalogCache(), t.TempDir(), newTestLogger(),
				OrchestratorConfig{SSO: SSOConfig{SSO: sso}, Stores: StoresConfig{AppStore: apps}})

			appList, err := apps.GetAll()
			require.NoError(t, err)
			orch.convergeUninstalls(context.Background(), appList, map[string]*store.InstalledApp{"plain": appList[0]}, nil)

			sso.AssertNotCalled(t, "Deprovision", mock.Anything, mock.Anything, mock.Anything)
			ids, err := apps.GetInstalledCatalogIDs()
			require.NoError(t, err)
			assert.NotContains(t, ids, "plain", "an app with no IdP objects uninstalls as before")
		})
	}
}

// A failed deprovision defers the whole uninstall rather than deleting the
// store row anyway. The row is the anchor the retry hangs off: drop it and the
// credential-bearing provider stays in the IdP forever, which is the failure
// this whole path exists to prevent. Reality is left untouched for the pass, so
// the retry starts from a consistent state rather than from half a teardown.
func TestConvergeUninstalls_DeprovisionFailureDefersTheUninstall(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "stuck", DisplayName: "Stuck", Status: store.AppStatusUninstalling})
	require.NoError(t, apps.SetSSOStrategy("stuck", "native-oidc"))

	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{CatalogID: "stuck", DisplayName: "Stuck", SSO: catalog.SSO{Strategy: "native-oidc"}})

	sso := new(MockSSOProvisioner)
	sso.On("Deprovision", "stuck", "Stuck", "native-oidc").Return(errors.New("authentik unreachable"))

	g := graph.New(graph.NewMapRepository())
	require.NoError(t, g.AddNode("stuck"))

	orch := NewOrchestrator(g, new(MockConfiguratorRegistry), cat, t.TempDir(), newTestLogger(),
		OrchestratorConfig{SSO: SSOConfig{SSO: sso}, Stores: StoresConfig{AppStore: apps}})

	appList, err := apps.GetAll()
	require.NoError(t, err)
	appMap := map[string]*store.InstalledApp{"stuck": appList[0]}
	orch.convergeUninstalls(context.Background(), appList, appMap, map[string]bool{"stuck": true})

	ids, err := apps.GetInstalledCatalogIDs()
	require.NoError(t, err)
	assert.Contains(t, ids, "stuck", "the store row survives so the next pass can retry")
	assert.Contains(t, appMap, "stuck", "the pass keeps the app in its working set")
	node, err := g.GetNode("stuck")
	require.NoError(t, err, "the graph node is untouched: nothing was torn down halfway")
	assert.NotNil(t, node)
}

// Once the provider is reachable again the deferred uninstall converges: the
// same pass shape that failed now completes and the row goes.
func TestConvergeUninstalls_RetriesAfterDeprovisionRecovers(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "later", DisplayName: "Later", Status: store.AppStatusUninstalling})
	require.NoError(t, apps.SetSSOStrategy("later", "forward-auth"))

	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{CatalogID: "later", DisplayName: "Later", SSO: catalog.SSO{Strategy: "forward-auth"}})

	sso := new(MockSSOProvisioner)
	// Two expectations, consumed in order: the first pass fails, the second
	// succeeds. testify matches expectations per-call, so a single On with two
	// Return+Once chains would not sequence.
	sso.On("Deprovision", "later", "Later", "forward-auth").
		Return(errors.New("authentik unreachable")).Once()
	sso.On("Deprovision", "later", "Later", "forward-auth").Return(nil).Once()

	g := graph.New(graph.NewMapRepository())
	require.NoError(t, g.AddNode("later"))
	registry := new(MockConfiguratorRegistry)
	registry.On("Get", "later").Return(nil).Maybe()

	orch := NewOrchestrator(g, registry, cat, t.TempDir(), newTestLogger(),
		OrchestratorConfig{SSO: SSOConfig{SSO: sso}, Stores: StoresConfig{AppStore: apps}})

	for pass := 1; pass <= 2; pass++ {
		appList, err := apps.GetAll()
		require.NoError(t, err)
		orch.convergeUninstalls(context.Background(), appList, map[string]*store.InstalledApp{"later": appList[0]}, nil)

		ids, err := apps.GetInstalledCatalogIDs()
		require.NoError(t, err)
		if pass == 1 {
			assert.Contains(t, ids, "later", "pass 1 defers on the failed deprovision")
			continue
		}
		assert.NotContains(t, ids, "later", "pass 2 completes once deprovision succeeds")
	}
	sso.AssertNumberOfCalls(t, "Deprovision", 2)
}

// With no SSO provisioner wired at all (SSO not configured on this instance)
// the uninstall must not touch anything and must not fail.
func TestConvergeUninstalls_NoSSOProvisionerIsANoOp(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "nosso", DisplayName: "No SSO", Status: store.AppStatusUninstalling})

	registry := new(MockConfiguratorRegistry)
	registry.On("Get", "nosso").Return(nil).Maybe()

	orch := NewOrchestrator(graph.New(graph.NewMapRepository()), registry, NewFakeCatalogCache(),
		t.TempDir(), newTestLogger(), OrchestratorConfig{Stores: StoresConfig{AppStore: apps}})

	appList, err := apps.GetAll()
	require.NoError(t, err)
	orch.convergeUninstalls(context.Background(), appList, map[string]*store.InstalledApp{"nosso": appList[0]}, nil)

	ids, err := apps.GetInstalledCatalogIDs()
	require.NoError(t, err)
	assert.NotContains(t, ids, "nosso")
}
