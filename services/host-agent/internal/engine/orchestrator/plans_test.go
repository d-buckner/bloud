// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func planGraph() *catalog.AppGraph {
	g := catalog.NewGraph([]*catalog.App{
		{CatalogID: "qbittorrent"},
		{CatalogID: "deluge"},
		{CatalogID: "radarr", Integrations: map[string]catalog.Integration{
			"downloadClient": {
				Required:   true,
				Compatible: []catalog.CompatibleApp{{App: "qbittorrent", Default: true}, {App: "deluge"}},
			},
		}},
	})
	g.SetInstalled([]string{"qbittorrent", "radarr"})
	return g
}

func newPlanTestOrchestrator(t *testing.T, catalogGraph catalog.AppGraphInterface) *Orchestrator {
	t.Helper()
	return NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		new(MockConfiguratorRegistry),
		NewFakeCatalogCache(),
		t.TempDir(),
		newTestLogger(),
		OrchestratorConfig{CatalogGraph: catalogGraph},
	)
}

func TestPlanInstall_DelegatesToTheLiveGraph(t *testing.T) {
	o := newPlanTestOrchestrator(t, planGraph())

	plan, err := o.PlanInstall("radarr")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Equal(t, "radarr", plan.App)
	assert.Empty(t, plan.Choices, "qbittorrent is installed, so the required slot resolves without a choice")
	require.Len(t, plan.AutoConfig, 1)
	assert.Equal(t, "qbittorrent", plan.AutoConfig[0].Source)
}

func TestPlanRemove_DelegatesToTheLiveGraph(t *testing.T) {
	o := newPlanTestOrchestrator(t, planGraph())

	plan, err := o.PlanRemove("qbittorrent")
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Equal(t, "qbittorrent", plan.App)
	// radarr requires a download client and deluge is not installed, so the
	// planner blocks the removal. The permitted-with-alternative case is
	// covered at the HTTP layer in apps_plan_test.go.
	assert.False(t, plan.CanRemove)
	require.Len(t, plan.Blockers, 1)
	assert.Contains(t, plan.Blockers[0], "radarr")
}

// TestPlans_NoGraphIsNotAnEmptyPlan pins the error identity the API maps to
// 503. Returning a zero-value plan here would be indistinguishable from a
// real plan that found nothing to do.
func TestPlans_NoGraphIsNotAnEmptyPlan(t *testing.T) {
	o := newPlanTestOrchestrator(t, nil)

	installPlan, err := o.PlanInstall("radarr")
	assert.Nil(t, installPlan)
	assert.ErrorIs(t, err, ErrPlanUnavailable)

	removePlan, err := o.PlanRemove("radarr")
	assert.Nil(t, removePlan)
	assert.ErrorIs(t, err, ErrPlanUnavailable)
}

// TestPlans_ReflectLaterInstalls guards the property the pre-flight dialog
// depends on: the plan reads the same graph the convergence pass refreshes,
// so a plan asked for after an install reflects it.
func TestPlans_ReflectLaterInstalls(t *testing.T) {
	g := catalog.NewGraph([]*catalog.App{
		{CatalogID: "qbittorrent"},
		{CatalogID: "radarr", Integrations: map[string]catalog.Integration{
			"downloadClient": {
				Required:   true,
				Compatible: []catalog.CompatibleApp{{App: "qbittorrent", Default: true}},
			},
		}},
	})
	o := newPlanTestOrchestrator(t, g)

	before, err := o.PlanInstall("radarr")
	require.NoError(t, err)
	require.Len(t, before.Choices, 1, "no provider installed yet, so the operator must choose")

	g.SetInstalled([]string{"qbittorrent"})
	after, err := o.PlanInstall("radarr")
	require.NoError(t, err)
	assert.Empty(t, after.Choices, "the provider is installed now, so the choice is gone")
	require.Len(t, after.AutoConfig, 1)
}
