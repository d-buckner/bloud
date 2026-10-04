// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

func specRevisionFor(t *testing.T, orch *Orchestrator, def catalog.ContainerDef, appID string) string {
	t.Helper()
	spec, err := orch.computeContainerSpec(&def, appID)
	require.NoError(t, err)
	rev, err := spec.Revision()
	require.NoError(t, err)
	return rev
}

func toAppMap(apps *FakeAppStore) map[string]*store.InstalledApp {
	all, _ := apps.GetAll()
	m := make(map[string]*store.InstalledApp, len(all))
	for _, a := range all {
		m[a.CatalogID] = a
	}
	return m
}

// One of two container revisions changed: exactly that node resets, the
// unchanged sibling is left alone.
func TestReconcileCatalogUpdates_ResetsOnlyChangedNode(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "demo", DisplayName: "Demo", Status: "running"})
	cat := NewFakeCatalogCache()
	defA := catalog.ContainerDef{Name: "apps-demo-a", Image: "alpine:3.20"}
	defB := catalog.ContainerDef{Name: "apps-demo-b", Image: "alpine:3.21"}
	cat.AddApp(&catalog.App{CatalogID: "demo", Containers: []catalog.ContainerDef{defA, defB}})

	g := graph.New(graph.NewMapRepository())
	seedRunningNode(t, g, "apps-demo-a", graph.StatusRunning)
	seedRunningNode(t, g, "apps-demo-b", graph.StatusRunning)

	rt := new(MockContainerRuntime)
	orch := NewOrchestrator(g, new(MockConfiguratorRegistry), cat, t.TempDir(), newTestLogger(),
		OrchestratorConfig{AppStore: apps, Containers: rt})

	revA := specRevisionFor(t, orch, defA, "demo")
	rt.On("ListContainers", mock.Anything).Return([]containerruntime.ContainerInfo{
		{Name: "apps-demo-a", Labels: map[string]string{containerruntime.AppLabel: "demo", containerruntime.SpecRevisionLabel: revA}},
		{Name: "apps-demo-b", Labels: map[string]string{containerruntime.AppLabel: "demo", containerruntime.SpecRevisionLabel: "stale"}},
	}, nil)

	orch.reconcileCatalogUpdates(context.Background(), toAppMap(apps))

	nodeA, err := g.GetNode("apps-demo-a")
	require.NoError(t, err)
	nodeB, err := g.GetNode("apps-demo-b")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, nodeA.ActualStatus, "unchanged sibling must not reset")
	assert.Equal(t, graph.StatusInitializing, nodeB.ActualStatus, "changed node must reset")
}

// A matched revision never resets, across repeated passes: the flap-loop guard.
func TestReconcileCatalogUpdates_NoDiffIsStable(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "demo", DisplayName: "Demo", Status: "running"})
	cat := NewFakeCatalogCache()
	def := catalog.ContainerDef{Name: "apps-demo", Image: "alpine:3.20"}
	cat.AddApp(&catalog.App{CatalogID: "demo", Containers: []catalog.ContainerDef{def}})

	g := graph.New(graph.NewMapRepository())
	seedRunningNode(t, g, "apps-demo", graph.StatusRunning)

	rt := new(MockContainerRuntime)
	orch := NewOrchestrator(g, new(MockConfiguratorRegistry), cat, t.TempDir(), newTestLogger(),
		OrchestratorConfig{AppStore: apps, Containers: rt})

	rev := specRevisionFor(t, orch, def, "demo")
	rt.On("ListContainers", mock.Anything).Return([]containerruntime.ContainerInfo{
		{Name: "apps-demo", Labels: map[string]string{containerruntime.AppLabel: "demo", containerruntime.SpecRevisionLabel: rev}},
	}, nil)

	m := toAppMap(apps)
	orch.reconcileCatalogUpdates(context.Background(), m)
	orch.reconcileCatalogUpdates(context.Background(), m)

	node, err := g.GetNode("apps-demo")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus, "a matching revision must never reset")
}

// A container the catalog no longer declares is pruned: removed, its node
// deleted, and the surviving sibling left untouched.
func TestReconcileCatalogUpdates_PrunesRemovedContainer(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "demo", DisplayName: "Demo", Status: "running"})
	cat := NewFakeCatalogCache()
	def := catalog.ContainerDef{Name: "apps-demo-a", Image: "alpine:3.20"}
	cat.AddApp(&catalog.App{CatalogID: "demo", Containers: []catalog.ContainerDef{def}})

	g := graph.New(graph.NewMapRepository())
	seedRunningNode(t, g, "apps-demo-a", graph.StatusRunning)
	seedRunningNode(t, g, "apps-demo-removed", graph.StatusRunning)

	rt := new(MockContainerRuntime)
	registry := new(MockConfiguratorRegistry)
	registry.On("Get", mock.Anything).Return(nil)
	orch := NewOrchestrator(g, registry, cat, t.TempDir(), newTestLogger(),
		OrchestratorConfig{AppStore: apps, Containers: rt})

	rev := specRevisionFor(t, orch, def, "demo")
	rt.On("ListContainers", mock.Anything).Return([]containerruntime.ContainerInfo{
		{Name: "apps-demo-a", Labels: map[string]string{containerruntime.AppLabel: "demo", containerruntime.SpecRevisionLabel: rev}},
		{Name: "apps-demo-removed", Labels: map[string]string{containerruntime.AppLabel: "demo"}},
	}, nil)
	rt.On("Remove", mock.Anything, "apps-demo-removed").Return(nil)

	orch.reconcileCatalogUpdates(context.Background(), toAppMap(apps))

	rt.AssertCalled(t, "Remove", mock.Anything, "apps-demo-removed")
	rt.AssertNotCalled(t, "Remove", mock.Anything, "apps-demo-a")
	node, err := g.GetNode("apps-demo-removed")
	require.NoError(t, err)
	assert.Nil(t, node, "the orphan node must be deleted")
	nodeA, err := g.GetNode("apps-demo-a")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, nodeA.ActualStatus)
}

// A failed container removal (the container is already gone) still deletes the
// node: the node is the durable orphan, and its deletion is the point.
func TestReconcileCatalogUpdates_PruneRemoveErrorStillDeletesNode(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "demo", DisplayName: "Demo", Status: "running"})
	cat := NewFakeCatalogCache()
	def := catalog.ContainerDef{Name: "apps-demo-a", Image: "alpine:3.20"}
	cat.AddApp(&catalog.App{CatalogID: "demo", Containers: []catalog.ContainerDef{def}})

	g := graph.New(graph.NewMapRepository())
	seedRunningNode(t, g, "apps-demo-a", graph.StatusRunning)
	seedRunningNode(t, g, "apps-demo-removed", graph.StatusRunning)

	rt := new(MockContainerRuntime)
	registry := new(MockConfiguratorRegistry)
	registry.On("Get", mock.Anything).Return(nil)
	orch := NewOrchestrator(g, registry, cat, t.TempDir(), newTestLogger(),
		OrchestratorConfig{AppStore: apps, Containers: rt})

	rev := specRevisionFor(t, orch, def, "demo")
	rt.On("ListContainers", mock.Anything).Return([]containerruntime.ContainerInfo{
		{Name: "apps-demo-a", Labels: map[string]string{containerruntime.AppLabel: "demo", containerruntime.SpecRevisionLabel: rev}},
		{Name: "apps-demo-removed", Labels: map[string]string{containerruntime.AppLabel: "demo"}},
	}, nil)
	rt.On("Remove", mock.Anything, "apps-demo-removed").Return(assert.AnError)

	require.NotPanics(t, func() {
		orch.reconcileCatalogUpdates(context.Background(), toAppMap(apps))
	})

	node, err := g.GetNode("apps-demo-removed")
	require.NoError(t, err)
	assert.Nil(t, node, "the node is deleted even when the container removal fails")
}

// An installed app with no catalog entry is left alone: nothing is pruned or
// reset, and the pass does not panic on the nil catalog lookup.
func TestReconcileCatalogUpdates_CatalogMissDoesNothing(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "ghost", DisplayName: "Ghost", Status: "running"})

	rt := new(MockContainerRuntime)
	rt.On("ListContainers", mock.Anything).Return(nil, nil)

	orch := NewOrchestrator(graph.New(graph.NewMapRepository()), new(MockConfiguratorRegistry), NewFakeCatalogCache(), t.TempDir(), newTestLogger(),
		OrchestratorConfig{AppStore: apps, Containers: rt})

	require.NotPanics(t, func() {
		orch.reconcileCatalogUpdates(context.Background(), toAppMap(apps))
	})
	rt.AssertNotCalled(t, "Remove", mock.Anything, mock.Anything)
}

// A strategy change resets the primary node so the full lifecycle re-runs
// ensureSSO and reconcileSSOStrategy.
func TestResetForSSOChange_ResetsPrimaryNode(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "demo", DisplayName: "Demo", Status: "running"})
	require.NoError(t, apps.SetSSOStrategy("demo", "forward-auth"))
	demoApp := &catalog.App{CatalogID: "demo", DisplayName: "Demo", SSO: catalog.SSO{Strategy: "native-oidc"}}
	cat := NewFakeCatalogCache()
	cat.AddApp(demoApp)

	g := graph.New(graph.NewMapRepository())
	seedRunningNode(t, g, "demo", graph.StatusRunning)

	orch := NewOrchestrator(g, new(MockConfiguratorRegistry), cat, t.TempDir(), newTestLogger(),
		OrchestratorConfig{AppStore: apps})

	orch.resetForSSOChange("demo", demoApp)

	node, err := g.GetNode("demo")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusInitializing, node.ActualStatus)
}

// No strategy change (stored equals current, or nothing recorded yet) means no
// reset.
func TestResetForSSOChange_NoChangeDoesNothing(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "demo", DisplayName: "Demo", Status: "running"})
	demoApp := &catalog.App{CatalogID: "demo", DisplayName: "Demo", SSO: catalog.SSO{Strategy: "native-oidc"}}
	cat := NewFakeCatalogCache()
	cat.AddApp(demoApp)

	g := graph.New(graph.NewMapRepository())
	seedRunningNode(t, g, "demo", graph.StatusRunning)

	orch := NewOrchestrator(g, new(MockConfiguratorRegistry), cat, t.TempDir(), newTestLogger(),
		OrchestratorConfig{AppStore: apps})

	// Empty stored strategy: first convergence, nothing to reset.
	orch.resetForSSOChange("demo", demoApp)
	node, err := g.GetNode("demo")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus)

	// Equal stored strategy: no change.
	require.NoError(t, apps.SetSSOStrategy("demo", "native-oidc"))
	orch.resetForSSOChange("demo", demoApp)
	node, err = g.GetNode("demo")
	require.NoError(t, err)
	assert.Equal(t, graph.StatusRunning, node.ActualStatus)
}

// reconcileSSOStrategy deprovisions the previous strategy and records the new
// one. It runs in the full lifecycle after ensureSSO, which is what guarantees
// the new provider exists before the old one is deleted.
func TestReconcileSSOStrategy_DeprovisionsOldAndRecordsNew(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "demo", DisplayName: "Demo", Status: "running"})
	require.NoError(t, apps.SetSSOStrategy("demo", "forward-auth"))
	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{CatalogID: "demo", DisplayName: "Demo", SSO: catalog.SSO{Strategy: "native-oidc"}})

	sso := new(MockSSOProvisioner)
	sso.On("Deprovision", "demo", "Demo", "forward-auth").Return(nil)

	orch := NewOrchestrator(graph.New(graph.NewMapRepository()), new(MockConfiguratorRegistry), cat, t.TempDir(), newTestLogger(),
		OrchestratorConfig{AppStore: apps, SSO: sso})

	orch.reconcileSSOStrategy(context.Background(), "demo")

	sso.AssertCalled(t, "Deprovision", "demo", "Demo", "forward-auth")
	strat, err := apps.GetSSOStrategy("demo")
	require.NoError(t, err)
	assert.Equal(t, "native-oidc", strat)
}

// Switching to "none" deprovisions the old strategy and records "none"; there
// is nothing new to provision, and reconcileSSOStrategy never deprovisions a
// strategy that had no per-app provider.
func TestReconcileSSOStrategy_ToNoneDeprovisionsOld(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "demo", DisplayName: "Demo", Status: "running"})
	require.NoError(t, apps.SetSSOStrategy("demo", "native-oidc"))
	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{CatalogID: "demo", DisplayName: "Demo", SSO: catalog.SSO{Strategy: "none"}})

	sso := new(MockSSOProvisioner)
	sso.On("Deprovision", "demo", "Demo", "native-oidc").Return(nil)

	orch := NewOrchestrator(graph.New(graph.NewMapRepository()), new(MockConfiguratorRegistry), cat, t.TempDir(), newTestLogger(),
		OrchestratorConfig{AppStore: apps, SSO: sso})

	orch.reconcileSSOStrategy(context.Background(), "demo")

	sso.AssertCalled(t, "Deprovision", "demo", "Demo", "native-oidc")
	strat, err := apps.GetSSOStrategy("demo")
	require.NoError(t, err)
	assert.Equal(t, "none", strat)
}
