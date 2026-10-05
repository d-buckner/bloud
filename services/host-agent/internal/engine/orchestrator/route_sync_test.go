// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// Contract (docs/operations/tech-debt.md, "route-generation side
// effects"): route generation is pure with respect to the runtime. It
// starts nothing and mutates no proxies; the installed set is the only
// input, and the config write is the only effect.

// orderTracker records cross-fake call order. The SyncRoutes path is a
// synchronous single-goroutine call by contract, so no locking is needed.
type orderTracker struct {
	calls []string
}

func (tr *orderTracker) add(name string) { tr.calls = append(tr.calls, name) }

type orderGenerator struct {
	tr *orderTracker
}

func (g *orderGenerator) Generate(_ []*catalog.App) error {
	g.tr.add("generate")
	return nil
}
func (g *orderGenerator) SetAuthentikEnabled(_ bool)      {}
func (g *orderGenerator) Preview(_ []*catalog.App) string { return "" }

// SyncRoutes starts nothing and mutates nothing but the route config. It
// reads the installed set from the store and hands it to the generator.
func TestRoutePurity_SyncRoutesStartsNoRuntime(t *testing.T) {
	tr := &orderTracker{}

	appStore := NewFakeAppStore()
	_ = appStore.Install("jellyfin", "Jellyfin", "1.0", nil, &store.InstallOptions{Port: 8096})
	catalogCache := NewFakeCatalogCache()
	catalogCache.AddApp(&catalog.App{
		CatalogID: "jellyfin", DisplayName: "Jellyfin", Version: "1.0.0", Port: 8096,
	})

	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		new(MockConfiguratorRegistry),
		catalogCache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{
			Stores:  StoresConfig{AppStore: appStore},
			Runtime: RuntimeConfig{TraefikGen: &orderGenerator{tr: tr}},
		},
	)

	require.NoError(t, orch.SyncRoutes())
	assert.Equal(t, []string{"generate"}, tr.calls,
		"the only effect of a route sync is the config write")
}

// Routes are a function of the installed set, not of any configurator's
// PostStart. After an uninstall the removed app must stop being routed in the
// same step that deletes its store row, not at the end of Reconcile after the
// (now per-pass) PostStart resync has run: the integration tier's
// uninstall-cleanup assertions read apps-routes.yml as soon as the app leaves
// the installed list.
func TestConvergeUninstalls_RegeneratesRoutesBeforeResync(t *testing.T) {
	tr := &orderTracker{}
	appStore := NewFakeAppStore()
	require.NoError(t, appStore.Install("jellyfin", "Jellyfin", "1.0", nil, &store.InstallOptions{Port: 8096}))
	require.NoError(t, appStore.UpdateStatus("jellyfin", "uninstalling"))

	g := graph.New(graph.NewMapRepository())
	require.NoError(t, g.AddNode("jellyfin"))

	registry := new(MockConfiguratorRegistry)
	registry.On("Get", "jellyfin").Return(nil)

	orch := NewOrchestrator(
		g,
		registry,
		NewFakeCatalogCache(),
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{Runtime: RuntimeConfig{TraefikGen: &orderGenerator{tr: tr}}, Stores: StoresConfig{AppStore: appStore}},
	)

	apps, err := appStore.GetAll()
	require.NoError(t, err)
	appMap := map[string]*store.InstalledApp{"jellyfin": apps[0]}
	orch.convergeUninstalls(context.Background(), apps, appMap, map[string]bool{"jellyfin": true})

	assert.Contains(t, tr.calls, "generate",
		"routes are regenerated in the uninstall step, before the resync runs")

	ids, err := appStore.GetInstalledCatalogIDs()
	require.NoError(t, err)
	assert.NotContains(t, ids, "jellyfin",
		"store row is gone before route generation reads the installed set")
}
