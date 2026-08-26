// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
)

// setupSetHostsTest builds an orchestrator wired with a real host state, a
// real host store (in-memory DB), and a catalog/app store containing:
//
//	immich (native-oidc, multi-container), navidrome (forward-auth),
//	jellyfin (ldap), authentik (system) — all present and RUNNING.
func setupSetHostsTest(t *testing.T, changed *bool) (*Orchestrator, *hostset.State, *store.HostStore, *graph.Graph) {
	t.Helper()

	g := graph.New(graph.NewMapRepository())
	appStore := NewFakeAppStore()
	catCache := NewFakeCatalogCache()

	addApp := func(app *catalog.App, nodeIDs ...string) {
		catCache.AddApp(app)
		if len(nodeIDs) == 0 {
			nodeIDs = []string{app.CatalogID}
		}
		primary := nodeIDs[len(nodeIDs)-1]
		status := "running"
		if app.IsSystem {
			_ = appStore.Install(app.CatalogID, app.CatalogID, "1", nil, &store.InstallOptions{IsSystem: true, Port: app.Port})
		} else {
			_ = appStore.Install(app.CatalogID, app.CatalogID, "1", nil, &store.InstallOptions{IsSystem: false, Port: app.Port})
		}
		_ = status
		_ = primary
		for _, id := range nodeIDs {
			require.NoError(t, g.AddNode(id))
			require.NoError(t, g.SetTargetStatus(id, graph.StatusRunning))
			require.NoError(t, g.SetActualStatus(id, graph.StatusRunning, ""))
		}
	}

	addApp(&catalog.App{
		CatalogID: "immich",
		SSO:       catalog.SSO{Strategy: "native-oidc"},
		Containers: []catalog.ContainerDef{
			{Name: "apps-immich-postgres"},
			{Name: "apps-immich-server"},
		},
	}, "apps-immich-postgres", "apps-immich-server")
	addApp(&catalog.App{
		CatalogID: "navidrome",
		SSO:       catalog.SSO{Strategy: "forward-auth"},
		Containers: []catalog.ContainerDef{
			{Name: "apps-navidrome"},
		},
	}, "apps-navidrome")
	addApp(&catalog.App{
		CatalogID: "jellyfin",
		SSO:       catalog.SSO{Strategy: "ldap"},
		Containers: []catalog.ContainerDef{
			{Name: "apps-jellyfin"},
		},
	}, "apps-jellyfin")
	addApp(&catalog.App{
		CatalogID: "authentik",
		IsSystem:  true,
		Containers: []catalog.ContainerDef{
			{Name: "apps-authentik-server"},
		},
	}, "apps-authentik-server")

	db := testdb.SetupTestDB(t)
	hostStore := store.NewHostStore(db)
	state := hostset.NewState(hostset.New(hostset.BuiltinHosts, hostset.DefaultPrimary))

	orch := NewOrchestrator(
		g,
		new(MockConfiguratorRegistry),
		catCache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{
			AppStore:     appStore,
			Hosts:        state,
			HostStore:    hostStore,
			OnHostsChanged: func() {
				if changed != nil {
					*changed = true
				}
			},
		},
	)
	return orch, state, hostStore, g
}

func nodeStatus(t *testing.T, g *graph.Graph, id string) graph.NodeStatus {
	t.Helper()
	node, err := g.GetNode(id)
	require.NoError(t, err)
	require.NotNil(t, node)
	return node.ActualStatus
}

func TestApplySetHostsIntent(t *testing.T) {
	var changed bool
	orch, state, hostStore, g := setupSetHostsTest(t, &changed)

	orch.applySetHostsIntent(NewSetHostsIntent(
		[]string{"localhost", "bloud.local", "example.com"}, "example.com"))

	// 1. Runtime state updated.
	hs := state.Get()
	assert.Equal(t, "example.com", hs.Primary())
	assert.Equal(t, "http://example.com", hs.PrimaryBaseURL())
	assert.Equal(t, "http://example.com", hs.IssuerBaseURL())

	// 2. Custom hosts persisted; built-ins not.
	stored, err := hostStore.List()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, "example.com", stored[0].Hostname)
	assert.True(t, stored[0].Primary)

	// 3. SSO-dependent nodes reset; others untouched.
	assert.Equal(t, graph.StatusInitializing, nodeStatus(t, g, "apps-immich-server"))
	assert.Equal(t, graph.StatusInitializing, nodeStatus(t, g, "apps-immich-postgres"))
	assert.Equal(t, graph.StatusInitializing, nodeStatus(t, g, "apps-navidrome"))
	assert.Equal(t, graph.StatusInitializing, nodeStatus(t, g, "apps-authentik-server"))
	assert.Equal(t, graph.StatusRunning, nodeStatus(t, g, "apps-jellyfin"))

	// 4. Callback fired.
	assert.True(t, changed)
}

func TestApplySetHostsIntentNoOpWhenUnchanged(t *testing.T) {
	orch, state, _, g := setupSetHostsTest(t, nil)

	// Start from a narrower state so the first apply is a real change.
	state.Set(hostset.New([]string{"localhost"}, "localhost"))

	// First apply: establishes the set, resets nodes, fires the callback.
	orch.applySetHostsIntent(NewSetHostsIntent(hostset.BuiltinHosts, "localhost"))
	require.Equal(t, graph.StatusInitializing, nodeStatus(t, g, "apps-immich-server"))

	// Put the node back to RUNNING and re-apply the same set: the no-op
	// guard must skip all side effects.
	require.NoError(t, g.SetActualStatus("apps-immich-server", graph.StatusRunning, ""))
	var changed bool
	orch.onHostsChanged = func() { changed = true }
	orch.applySetHostsIntent(NewSetHostsIntent([]string{"bloud.local", "localhost"}, "localhost"))
	assert.False(t, changed, "unchanged host set must not fire OnHostsChanged")
	assert.Equal(t, graph.StatusRunning, nodeStatus(t, g, "apps-immich-server"), "unchanged host set must not reset nodes")
}

func TestApplySetHostsIntentBuiltinPrimaryNotStored(t *testing.T) {
	orch, _, hostStore, _ := setupSetHostsTest(t, nil)

	orch.applySetHostsIntent(NewSetHostsIntent(
		[]string{"localhost", "bloud.local", "example.com"}, "localhost"))

	stored, err := hostStore.List()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, "example.com", stored[0].Hostname)
	assert.False(t, stored[0].Primary)
}

func TestApplySetHostsIntentIgnoresNonRunningNodes(t *testing.T) {
	var changed bool
	orch, _, _, g := setupSetHostsTest(t, &changed)

	// Immich server is mid-flight (STARTING): must not be reset.
	require.NoError(t, g.SetActualStatus("apps-immich-server", graph.StatusStarting, ""))

	orch.applySetHostsIntent(NewSetHostsIntent(
		[]string{"localhost", "bloud.local", "example.com"}, "example.com"))

	assert.Equal(t, graph.StatusStarting, nodeStatus(t, g, "apps-immich-server"))
	assert.True(t, changed)
}
