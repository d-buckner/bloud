// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

// The resync is the path every already-RUNNING node takes on every pass: it
// re-runs PreStart and PostStart, and recreates the container only when
// PreStart reports that the config actually changed. These cover that path and
// the watchdog that makes a non-converging diff loud without ever withholding
// a restart that was legitimately asked for.

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

// newResyncHarness builds the harness. warnAt of 0 leaves the default warning
// threshold.
func newResyncHarness(t *testing.T, warnAt int) *resyncHarness {
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
		Tuning:  TuningConfig{HealthCheckTimeout: 100 * time.Millisecond, ResyncRestartWarnAt: warnAt},
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

// Nothing withholds a restart. A configurator that reports a change every pass
// gets a restart every pass, however long that goes on, because the engine
// cannot tell a non-converging diff from a burst of legitimate changes and the
// cost of guessing wrong is a container serving config that disagrees with the
// file on disk. This is the property that replaced a cap, and the calendar
// aggregation test is what forced it: three consecutive real changes to
// Radicale's sharing.csv, each of which genuinely required a restart.
func TestResync_RestartsAreNeverWithheld(t *testing.T) {
	h := newResyncHarness(t, 2)
	h.cfg.setPrestart(configurator.MustRestart("config rewritten"), nil)

	for i := 1; i <= 4; i++ {
		h.rt.resetCounts()
		h.pass(t)
		_, removes, _ := h.rt.counts()
		assert.Equal(t, 1, removes, "pass %d still restarts: the watchdog observes, it does not decide", i)
		assert.Equal(t, graph.StatusRunning, h.status(t), "pass %d leaves the node RUNNING", i)
	}
}

// The watchdog raises a signal once the consecutive count crosses the
// threshold, and keeps raising it every threshold after, because the condition
// it watches for does not stop on its own.
func TestResync_WatchdogSignalsAtThreshold(t *testing.T) {
	h := newResyncHarness(t, 2)
	h.cfg.setPrestart(configurator.MustRestart("config rewritten"), nil)

	h.pass(t)
	assert.Empty(t, h.orch.ResyncRestartSignals(), "one restart is what a change costs; nothing to say")

	h.pass(t)
	signals := h.orch.ResyncRestartSignals()
	require.Len(t, signals, 1, "the second consecutive restart crosses the threshold")
	assert.Equal(t, resyncNode, signals[0].Node)
	assert.Equal(t, "config rewritten", signals[0].Reason)
	assert.Equal(t, 2, signals[0].Restarts)

	h.pass(t)
	require.Len(t, h.orch.ResyncRestartSignals(), 1)
	assert.Equal(t, 2, h.orch.ResyncRestartSignals()[0].Restarts, "no new crossing at three")

	h.pass(t)
	require.Len(t, h.orch.ResyncRestartSignals(), 1)
	assert.Equal(t, 4, h.orch.ResyncRestartSignals()[0].Restarts, "the boundary repeats, so a runaway loop keeps announcing itself")
}

// A pass that converges clears the accounting, so a node that was flagged while
// thrashing does not stay flagged once it settles.
func TestResync_ConvergedPassClearsWatch(t *testing.T) {
	h := newResyncHarness(t, 2)
	h.cfg.setPrestart(configurator.MustRestart("config rewritten"), nil)

	h.pass(t)
	h.pass(t)
	require.Len(t, h.orch.ResyncRestartSignals(), 1)

	h.cfg.setPrestart(configurator.NoRestart(), nil)
	h.pass(t)
	assert.Empty(t, h.orch.ResyncRestartSignals(), "a converged pass clears the signal")

	h.rt.resetCounts()
	h.cfg.setPrestart(configurator.MustRestart("config rewritten again"), nil)
	h.pass(t)
	_, removes, _ := h.rt.counts()
	assert.Equal(t, 1, removes, "a later real change still restarts")
	assert.Empty(t, h.orch.ResyncRestartSignals(), "and the count restarted from zero, not from four")
}

// A full lifecycle drive is an intentional event: an install, a reboot, a
// crash recovery, or an explicit reset. It clears the accounting on the way
// in, so the resync count never carries a drive's own restarts into the
// watchdog's view of the node.
func TestResync_FullDriveClearsWatch(t *testing.T) {
	h := newResyncHarness(t, 2)
	h.cfg.setPrestart(configurator.MustRestart("config rewritten"), nil)

	h.pass(t)
	h.pass(t)
	require.Len(t, h.orch.ResyncRestartSignals(), 1)

	// Simulate the node dropping out of RUNNING, which is what an install
	// retry or a reboot does, and let the pass drive it fully.
	require.NoError(t, h.g.SetActualStatus(resyncNode, graph.StatusInitializing, ""))
	h.rt.resetCounts()
	h.pass(t)
	_, removes, _ := h.rt.counts()
	require.Equal(t, 1, removes, "the drive restarts as it always has")
	require.Empty(t, h.orch.ResyncRestartSignals(), "the drive reset the accounting")

	// Back at RUNNING, the resync count starts from zero again.
	require.NoError(t, h.g.SetActualStatus(resyncNode, graph.StatusRunning, ""))
	h.rt.resetCounts()
	h.pass(t)
	_, removes, _ = h.rt.counts()
	assert.Equal(t, 1, removes, "the resync still restarts after the drive")
	assert.Empty(t, h.orch.ResyncRestartSignals(), "the count began again at one, below the threshold")
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
	assert.Empty(t, orch.ResyncRestartSignals(), "nothing was restarted, so nothing is counted")
}

// The threshold is configurable, so an operator can raise the alarm sooner on
// a box where a restart is expensive, or later on one where bursts are normal.
func TestResync_WarnAtIsConfigurable(t *testing.T) {
	h := newResyncHarness(t, 1)
	h.cfg.setPrestart(configurator.MustRestart("config rewritten"), nil)

	h.pass(t)
	_, removes, _ := h.rt.counts()
	require.Equal(t, 1, removes, "a threshold of one still restarts")
	require.Len(t, h.orch.ResyncRestartSignals(), 1, "and signals on the very first restart")
	assert.Equal(t, 1, h.orch.ResyncRestartSignals()[0].Restarts)
}

// The signal has to survive being read from the status snapshot, since that is
// the surface an operator sees.
func TestResync_SignalsSurfaceInStatus(t *testing.T) {
	h := newResyncHarness(t, 1)
	h.cfg.setPrestart(configurator.MustRestart("config rewritten"), nil)

	h.pass(t)
	h.pass(t)

	status := h.orch.Status()
	require.Len(t, status.ResyncRestartSignals, 1, "one record per node, holding the latest crossing")
	assert.Equal(t, resyncNode, status.ResyncRestartSignals[0].Node)
	assert.Equal(t, 2, status.ResyncRestartSignals[0].Restarts)
	assert.Contains(t, status.RecentActivity[len(status.RecentActivity)-1].Detail, resyncNode)
}
