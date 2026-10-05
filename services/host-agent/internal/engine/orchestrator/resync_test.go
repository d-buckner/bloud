// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

// The resync is the path every already-RUNNING node takes on every pass: it
// re-runs PreStart and PostStart, and recreates the container only when
// PreStart reports that the config actually changed. These cover that path and
// the breaker that stops it when a config diff never converges.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// resyncNode is the container node the resync fixture reconciles.
const resyncNode = "apps-resync-target"

// resyncApp is the catalog ID that owns resyncNode.
const resyncApp = "resync-target"

// switchableConfigurator is a NodeLifecycle whose PreStart answer the test
// controls between passes. A testify mock cannot vary a return value per call
// without a fresh expectation each pass, and the whole subject of these tests is
// what happens across passes.
type switchableConfigurator struct {
	mu            sync.Mutex
	node          string
	prestart      configurator.PreStartResult
	prestartErr   error
	postStartErr  error
	preStartCalls int
	postStartCall int
}

func (c *switchableConfigurator) Name() string { return c.node }

func (c *switchableConfigurator) PreStart(_ context.Context, _ *configurator.AppState) (configurator.PreStartResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.preStartCalls++
	return c.prestart, c.prestartErr
}

func (c *switchableConfigurator) PostStart(_ context.Context, _ *configurator.AppState) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.postStartCall++
	return c.postStartErr
}

func (c *switchableConfigurator) setPrestart(res configurator.PreStartResult, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prestart = res
	c.prestartErr = err
}

func (c *switchableConfigurator) counts() (preStarts, postStarts int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.preStartCalls, c.postStartCall
}

// resyncHarness is one RUNNING node with a live container behind it, which is
// the state a working install sits in between passes.
type resyncHarness struct {
	orch *Orchestrator
	g    *graph.Graph
	cfg  *switchableConfigurator
	rt   *liveRuntime
}

// newResyncHarness builds the harness. restartCap of 0 leaves the default cap.
func newResyncHarness(t *testing.T, restartCap int) *resyncHarness {
	t.Helper()

	g := graph.New(graph.NewMapRepository())
	require.NoError(t, g.AddNode(resyncNode))
	require.NoError(t, g.SetTargetStatus(resyncNode, graph.StatusRunning))
	// The node is already converged: this is a resync, not a first drive.
	require.NoError(t, g.SetActualStatus(resyncNode, graph.StatusRunning, ""))

	apps := NewFakeAppStore()
	require.NoError(t, apps.Install(resyncApp, "Resync Target", "1", nil, nil))

	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{
		CatalogID:  resyncApp,
		Containers: []catalog.ContainerDef{{Name: resyncNode, Image: "example/resync:1.0"}},
	})

	rt := newLiveRuntime()
	rt.set(resyncNode, containerruntime.State{Exists: true, Running: true})

	cfg := &switchableConfigurator{node: resyncNode}
	registry := new(MockConfiguratorRegistry)
	registry.On("Get", mock.Anything).Return(cfg).Maybe()

	orch := NewOrchestrator(g, registry, cat, t.TempDir(), newTestLogger(), OrchestratorConfig{
		Tuning:  TuningConfig{HealthCheckTimeout: 100 * time.Millisecond, ResyncRestartCap: restartCap},
		Runtime: RuntimeConfig{Containers: rt},
		Stores:  StoresConfig{AppStore: apps},
	})
	orch.registerContainerOwner(resyncNode, resyncApp)

	return &resyncHarness{orch: orch, g: g, cfg: cfg, rt: rt}
}

func (h *resyncHarness) pass(t *testing.T) {
	t.Helper()
	require.NoError(t, h.orch.Reconcile(context.Background()))
}

func (h *resyncHarness) status(t *testing.T) graph.NodeStatus {
	t.Helper()
	node, err := h.g.GetNode(resyncNode)
	require.NoError(t, err)
	require.NotNil(t, node)
	return node.ActualStatus
}

// A node that converged on a previous pass is not assumed to still match the
// catalog or its providers. The resync runs PreStart so the configurator can
// diff the files it manages, which is the only way a provider installed after
// this app came up reaches them.
func TestResync_RunsPreStartForRunningNode(t *testing.T) {
	h := newResyncHarness(t, 0)

	h.pass(t)
	h.pass(t)

	preStarts, postStarts := h.cfg.counts()
	assert.Equal(t, 2, preStarts, "every pass re-runs PreStart for a running node")
	assert.Equal(t, 2, postStarts, "every pass re-runs PostStart for a running node")
}

// Running PreStart is not the same as disturbing the app. With nothing changed,
// the pass must not create, recreate, or remove a container.
func TestResync_NoChangeLeavesContainerAlone(t *testing.T) {
	h := newResyncHarness(t, 0)
	h.cfg.setPrestart(configurator.NoRestart(), nil)

	h.pass(t)

	ensures, removes, _ := h.rt.counts()
	assert.Equal(t, 0, ensures, "a resync that found no change must not create a container")
	assert.Equal(t, 0, removes, "a resync that found no change must not remove a container")
	assert.Equal(t, graph.StatusRunning, h.status(t))
}

// When PreStart reports a change, the resync recreates the container the same
// way a full drive does, and the node comes back to RUNNING in the same pass.
// Returning the restart to the pass matters: the node left RUNNING on the way
// through the recreate, so without promotion it would sit at STARTING until a
// later pass re-drove it.
func TestResync_ChangeRecreatesContainerAndPromotesNode(t *testing.T) {
	h := newResyncHarness(t, 0)
	h.cfg.setPrestart(configurator.MustRestart("config rewritten"), nil)

	h.pass(t)

	ensures, removes, _ := h.rt.counts()
	assert.Equal(t, 1, removes, "a reported config change removes the container")
	assert.Equal(t, 1, ensures, "and creates the replacement")
	assert.Equal(t, graph.StatusRunning, h.status(t), "the node is promoted back to RUNNING in the same pass")

	preStarts, postStarts := h.cfg.counts()
	assert.Equal(t, 1, preStarts)
	assert.Equal(t, 1, postStarts, "PostStart still runs after the restart")
}

// The breaker is what makes the resync safe to run at all. A configurator that
// reports a change every pass would otherwise restart its container once a
// minute forever, and the shape that produces is an app that rewrites the file
// Bloud manages while it runs.
func TestResync_BreakerCapsConsecutiveRestarts(t *testing.T) {
	h := newResyncHarness(t, 0)
	h.cfg.setPrestart(configurator.MustRestart("config rewritten"), nil)

	h.pass(t)
	h.pass(t)
	firstEnsures, removes, _ := h.rt.counts()
	require.Equal(t, 2, removes, "the cap grants two consecutive restarts")
	require.Equal(t, 2, firstEnsures, "each remove was paired with the create that replaced it")

	h.rt.resetCounts()
	h.pass(t)

	ensures, removes, _ := h.rt.counts()
	assert.Equal(t, 0, removes, "the third consecutive restart is refused")
	assert.Equal(t, 0, ensures, "and nothing is created in its place")
	assert.Equal(t, graph.StatusRunning, h.status(t), "a refused restart leaves the running app alone")

	breakers := h.orch.ResyncBreakers()
	require.Len(t, breakers, 1, "the trip is surfaced")
	assert.Equal(t, resyncNode, breakers[0].Node)
	assert.Equal(t, "config rewritten", breakers[0].Reason)
	assert.Equal(t, 2, breakers[0].Restarts)
}

// A pass that converges clears the accounting, so a breaker that tripped while
// an app was thrashing does not permanently disable restarts for an app that
// later settles.
func TestResync_ConvergedPassClearsBreaker(t *testing.T) {
	h := newResyncHarness(t, 0)
	h.cfg.setPrestart(configurator.MustRestart("config rewritten"), nil)

	h.pass(t)
	h.pass(t)
	h.pass(t)
	require.Len(t, h.orch.ResyncBreakers(), 1)

	h.cfg.setPrestart(configurator.NoRestart(), nil)
	h.pass(t)
	assert.Empty(t, h.orch.ResyncBreakers(), "a converged pass clears the trip")

	h.rt.resetCounts()
	h.cfg.setPrestart(configurator.MustRestart("config rewritten again"), nil)
	h.pass(t)
	_, removes, _ := h.rt.counts()
	assert.Equal(t, 1, removes, "a later real change is granted again after the clear")
}

// A full lifecycle drive is an intentional event: an install, a reboot, a
// crash recovery, or an explicit reset. It clears the breaker on the way in,
// so the resync accounting never suppresses a restart a drive itself asked for.
func TestResync_FullDriveClearsBreaker(t *testing.T) {
	h := newResyncHarness(t, 0)
	h.cfg.setPrestart(configurator.MustRestart("config rewritten"), nil)

	h.pass(t)
	h.pass(t)
	require.Equal(t, 2, func() int { _, r, _ := h.rt.counts(); return r }())

	// Simulate the node dropping out of RUNNING, which is what an install
	// retry or a reboot does, and let the pass drive it fully.
	require.NoError(t, h.g.SetActualStatus(resyncNode, graph.StatusInitializing, ""))
	h.rt.resetCounts()
	h.pass(t)
	_, removes, _ := h.rt.counts()
	require.Equal(t, 1, removes, "the drive restarts as it always has")
	require.Empty(t, h.orch.ResyncBreakers(), "the drive reset the accounting")

	// Back at RUNNING, the resync has its full allowance again.
	require.NoError(t, h.g.SetActualStatus(resyncNode, graph.StatusRunning, ""))
	h.rt.resetCounts()
	h.pass(t)
	_, removes, _ = h.rt.counts()
	assert.Equal(t, 1, removes, "the resync restart allowance was reset by the drive")
}

// A failed PreStart on the resync path is not a reason to take a serving app
// down. The full-lifecycle path parks the node in ERROR because the container
// must not start on a config that could not be written; here the container is
// already up, so the failure is recorded and retried next pass.
func TestResync_PreStartFailureKeepsNodeRunning(t *testing.T) {
	h := newResyncHarness(t, 0)
	h.cfg.setPrestart(configurator.NoRestart(), errors.New("cannot write config"))

	h.pass(t)

	assert.Equal(t, graph.StatusRunning, h.status(t), "a failed config diff does not error a running app")
	ensures, removes, _ := h.rt.counts()
	assert.Equal(t, 0, ensures)
	assert.Equal(t, 0, removes)

	_, postStarts := h.cfg.counts()
	assert.Equal(t, 0, postStarts, "PostStart is not run when the config phase failed")
}

// A node with no container def has nothing to recreate. The config change is
// still on disk and PostStart still gets its diff; the restart request is
// dropped rather than counted, because nothing was restarted.
func TestResync_RestartRequestWithoutContainerDef(t *testing.T) {
	g := graph.New(graph.NewMapRepository())
	require.NoError(t, g.AddNode("plain-app"))
	require.NoError(t, g.SetTargetStatus("plain-app", graph.StatusRunning))
	require.NoError(t, g.SetActualStatus("plain-app", graph.StatusRunning, ""))

	cfg := &switchableConfigurator{node: "plain-app"}
	cfg.setPrestart(configurator.MustRestart("config rewritten"), nil)
	registry := new(MockConfiguratorRegistry)
	registry.On("Get", mock.Anything).Return(cfg).Maybe()

	rt := newLiveRuntime()
	orch := NewOrchestrator(g, registry, NewFakeCatalogCache(), t.TempDir(), newTestLogger(), OrchestratorConfig{
		Tuning:  TuningConfig{HealthCheckTimeout: 100 * time.Millisecond},
		Runtime: RuntimeConfig{Containers: rt},
	})

	require.NoError(t, orch.Reconcile(context.Background()))

	_, removes, _ := rt.counts()
	assert.Equal(t, 0, removes, "there is no container to remove")
	_, postStarts := cfg.counts()
	assert.Equal(t, 1, postStarts, "PostStart still runs")
	assert.Empty(t, orch.ResyncBreakers(), "nothing was restarted, so nothing is counted")
}

// The cap is configurable, so an operator can catch a non-converging diff
// sooner or tolerate a burst of legitimate restarts.
func TestResync_CapIsConfigurable(t *testing.T) {
	h := newResyncHarness(t, 1)
	h.cfg.setPrestart(configurator.MustRestart("config rewritten"), nil)

	h.pass(t)
	_, removes, _ := h.rt.counts()
	require.Equal(t, 1, removes, "a cap of one grants exactly one restart")

	h.rt.resetCounts()
	h.pass(t)
	_, removes, _ = h.rt.counts()
	assert.Equal(t, 0, removes, "the second is refused")
	require.Len(t, h.orch.ResyncBreakers(), 1)
}

// The breaker record has to survive being read from the status snapshot, since
// that is the surface an operator sees.
func TestResync_BreakerSurfacesInStatus(t *testing.T) {
	h := newResyncHarness(t, 1)
	h.cfg.setPrestart(configurator.MustRestart("config rewritten"), nil)

	h.pass(t)
	h.pass(t)

	status := h.orch.Status()
	require.Len(t, status.ResyncBreakers, 1)
	assert.Equal(t, resyncNode, status.ResyncBreakers[0].Node)
	assert.Contains(t, status.RecentActivity[len(status.RecentActivity)-1].Detail, resyncNode)
}
