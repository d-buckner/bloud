// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// Contract: the orchestrator converges on its own, not only when asked.
// A ReconcileIntent on an idle-based timer is the trigger, and the pass it
// runs must be free: no restart, no config write, no status transition when
// reality already matches intent. A once-a-minute pass that turned out to
// churn would replace a rare bug with a 60-second reboot cycle, so the
// no-op guarantee below is the load-bearing part of this feature.

// ── test doubles ──────────────────────────────────────────────────────

// countingRepo records how many times a node row was written. Every status
// change goes through SaveNode, so a zero count means the pass left the
// lifecycle record alone.
type countingRepo struct {
	*graph.MapRepository
	mu         sync.Mutex
	nodeWrites int
}

func (r *countingRepo) SaveNode(node graph.Node) error {
	r.mu.Lock()
	r.nodeWrites++
	r.mu.Unlock()
	return r.MapRepository.SaveNode(node)
}

func (r *countingRepo) writes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nodeWrites
}

func (r *countingRepo) reset() {
	r.mu.Lock()
	r.nodeWrites = 0
	r.mu.Unlock()
}

// liveRuntime is a container runtime that keeps its own truth, so a test can
// kill a container the way `podman kill` does: flip the flag, and the next
// Inspect reports what the orchestrator would really see.
type liveRuntime struct {
	mu       sync.Mutex
	states   map[string]containerruntime.State
	ensures  int
	removes  int
	inspects int
}

func newLiveRuntime() *liveRuntime {
	return &liveRuntime{states: map[string]containerruntime.State{}}
}

func (r *liveRuntime) set(name string, st containerruntime.State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[name] = st
}

func (r *liveRuntime) kill(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.states[name]
	st.Running = false
	r.states[name] = st
}

func (r *liveRuntime) isRunning(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.states[name]
	return ok && st.Running
}

func (r *liveRuntime) counts() (ensures, removes, inspects int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensures, r.removes, r.inspects
}

func (r *liveRuntime) resetCounts() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensures, r.removes, r.inspects = 0, 0, 0
}

func (r *liveRuntime) EnsureNetwork(_ context.Context, _ string) error { return nil }

func (r *liveRuntime) Ensure(_ context.Context, spec containerruntime.Spec) (containerruntime.EnsureResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensures++
	r.states[spec.Name] = containerruntime.State{Exists: true, Running: true}
	return containerruntime.EnsureResult{Created: true, Started: true}, nil
}

func (r *liveRuntime) Remove(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removes++
	delete(r.states, name)
	return nil
}

func (r *liveRuntime) Inspect(_ context.Context, name string) (containerruntime.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inspects++
	st, ok := r.states[name]
	if !ok {
		return containerruntime.State{Exists: false}, nil
	}
	return st, nil
}

func (r *liveRuntime) ListContainers(_ context.Context) ([]containerruntime.ContainerInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]containerruntime.ContainerInfo, 0, len(r.states))
	for name := range r.states {
		out = append(out, containerruntime.ContainerInfo{Name: name})
	}
	return out, nil
}

func (r *liveRuntime) Exec(_ context.Context, _ string, _ []string) error { return nil }

// passRecorder watches LastConverged and records every distinct value, which
// is an exact count of convergence passes. Polling at 2ms cannot merge two
// passes that are a whole self-heal interval apart.
type passRecorder struct {
	mu     sync.Mutex
	times  []time.Time
	cancel context.CancelFunc
}

func watchPasses(ctx context.Context, orch *Orchestrator) *passRecorder {
	rec := &passRecorder{}
	ctx, cancel := context.WithCancel(ctx)
	rec.cancel = cancel
	go func() {
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		// Start from the pass that is already recorded, so the boot pass is
		// never counted as a self-heal pass.
		last := orch.LastConverged()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if t := orch.LastConverged(); !t.IsZero() && !t.Equal(last) {
					last = t
					rec.mu.Lock()
					rec.times = append(rec.times, t)
					rec.mu.Unlock()
				}
			}
		}
	}()
	return rec
}

func (r *passRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.times)
}

func (r *passRecorder) stop() { r.cancel() }

// ── fixture ───────────────────────────────────────────────────────────

const (
	healApp       = "jellyfin"
	healContainer = "apps-jellyfin"
)

type healFixture struct {
	orch   *Orchestrator
	g      *graph.Graph
	repo   *countingRepo
	apps   *FakeAppStore
	cat    *FakeCatalogCache
	rt     *liveRuntime
	cfg    *MockConfigurator
	routes string
}

// newHealFixture builds a one-app stack that is already healthy: the app is
// installed and running, its container is up, and its node is at RUNNING
// with target RUNNING. That is the state a self-heal pass must cross without
// touching.
func newHealFixture(t *testing.T, interval time.Duration) *healFixture {
	t.Helper()

	repo := &countingRepo{MapRepository: graph.NewMapRepository()}
	g := graph.New(repo)
	require.NoError(t, g.AddNode(healContainer))
	require.NoError(t, g.SetTargetStatus(healContainer, graph.StatusRunning))
	require.NoError(t, g.SetActualStatus(healContainer, graph.StatusRunning, ""))
	repo.reset()

	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: healApp, DisplayName: "Jellyfin", Status: "running"})

	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{
		CatalogID:   healApp,
		DisplayName: "Jellyfin",
		Containers:  []catalog.ContainerDef{{Name: healContainer, Image: "docker.io/test/jellyfin:1.0"}},
	})

	rt := newLiveRuntime()
	rt.set(healContainer, containerruntime.State{Exists: true, Running: true})

	cfg := new(MockConfigurator)
	cfg.On("Name").Return(healContainer).Maybe()
	cfg.On("PreStart", mock.Anything, mock.Anything).Return(configurator.NoRestart(), nil).Maybe()
	cfg.On("PostStart", mock.Anything, mock.Anything).Return(nil).Maybe()
	registry := new(MockConfiguratorRegistry)
	registry.On("Get", mock.Anything).Return(cfg).Maybe()

	routesPath := t.TempDir() + "/apps-routes.yml"

	orch := NewOrchestrator(g, registry, cat, t.TempDir(), newTestLogger(), OrchestratorConfig{
		AppStore:         apps,
		Containers:       rt,
		TraefikGen:       traefikgen.NewGenerator(routesPath),
		SelfHealInterval: interval,
	})

	return &healFixture{orch: orch, g: g, repo: repo, apps: apps, cat: cat, rt: rt, cfg: cfg, routes: routesPath}
}

// converge runs one full pass exactly the way the intent loop runs one.
func (f *healFixture) converge(intents ...Intent) {
	f.orch.converge(context.Background(), intents)
}

func statRoutes(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err, "the convergence pass should have written the route file")
	return info
}

func healNode(t *testing.T, g *graph.Graph, id string) *graph.Node {
	t.Helper()
	node, err := g.GetNode(id)
	require.NoError(t, err)
	require.NotNil(t, node, "node %q should exist", id)
	return node
}

// countCalls counts how many times the shared configurator mock has had the
// named method called since it was created. The idle-pass test needs a running
// total rather than a per-pass one, because the fixture's mock is not reset
// between the establishing pass and the pass under test.
func countCalls(cfg *MockConfigurator, method string) int {
	n := 0
	for _, c := range cfg.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

// ── idempotence ───────────────────────────────────────────────────────

// The deliverable test from the issue: a full pass over a healthy stack must
// produce zero restarts, zero config writes, and zero graph transitions.
//
// It does re-run PostStart. RUNNING means the lifecycle phases completed once,
// not that the app's config still matches the outside world, and the periodic
// diff is a large part of what the idle pass is for. What it must not do is
// touch the container or move a node's status.
func TestSelfHeal_IdlePassChangesNothing(t *testing.T) {
	f := newHealFixture(t, 0)

	// First pass: establishes that the stack is converged and creates the
	// route file, so the second pass has something to leave alone.
	f.converge(NewReconcileIntent())
	require.Equal(t, graph.StatusRunning, healNode(t, f.g, healContainer).ActualStatus)
	_, _, inspects := f.rt.counts()
	require.Positive(t, inspects, "the pass must actually have looked at the container")
	require.Equal(t, 1, countCalls(f.cfg, "PostStart"),
		"a running node gets its PostStart diff on every pass")

	// Reset every counter, then run the pass that is supposed to be silent.
	f.repo.reset()
	f.rt.resetCounts()

	// Let the filesystem clock advance enough that a rewrite would be
	// visible in the route file's mtime.
	time.Sleep(30 * time.Millisecond)
	before := statRoutes(t, f.routes)

	f.converge(NewReconcileIntent())

	ensures, removes, _ := f.rt.counts()
	assert.Equal(t, 0, ensures, "a healthy container must not be created or recreated")
	assert.Equal(t, 0, removes, "a healthy container must not be removed")
	assert.Zero(t, f.repo.writes(), "a converged node must not take a status transition")
	// PreStart is the phase that can ask for a container recreate, so an idle
	// pass must never run it. PostStart is the diff, and it runs exactly once
	// more: the resync is one call per pass, not one per change.
	assert.Equal(t, 0, countCalls(f.cfg, "PreStart"),
		"an idle pass must not run PreStart: that is the phase that can restart the container")
	assert.Equal(t, 2, countCalls(f.cfg, "PostStart"),
		"an idle pass runs exactly one PostStart resync")

	after := statRoutes(t, f.routes)
	assert.True(t, before.ModTime().Equal(after.ModTime()),
		"routes that are already current must not be rewritten: Traefik reloads the file on every write")

	// And the pass must not have disturbed the app row either.
	row, err := f.apps.GetByCatalogID(healApp)
	require.NoError(t, err)
	assert.Equal(t, store.AppStatusRunning, row.Status)
}

// A pass over an empty instance (nothing installed at all) must complete and
// leave no trace behind.
func TestSelfHeal_NothingInstalledIsSilent(t *testing.T) {
	f := newHealFixture(t, 0)
	apps := NewFakeAppStore()
	f.orch.appStore = apps
	f.orch.config.AppStore = apps

	require.NotPanics(t, func() { f.converge(NewReconcileIntent()) })
	ensures, removes, _ := f.rt.counts()
	assert.Zero(t, ensures+removes, "an empty instance must not gain containers from a self-heal pass")
}

// ── drift repair through the timer ────────────────────────────────────

// A container killed out from under Bloud comes back on the next tick with
// no intent submitted by anyone. This is the whole point of the feature:
// nothing else in the system notices at 3am.
func TestSelfHeal_TimerRepairsDriftWithoutAnIntent(t *testing.T) {
	f := newHealFixture(t, 120*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.orch.Start(ctx)
	<-f.orch.Ready()

	require.True(t, f.rt.isRunning(healContainer))
	require.Equal(t, graph.StatusRunning, healNode(t, f.g, healContainer).ActualStatus)

	// Kill it the way an OOM or a `podman kill` would.
	f.rt.kill(healContainer)

	// Wait for the container to be back *and* for the graph to report RUNNING.
	// isRunning only proves the runtime restarted the container; the node is
	// promoted to RUNNING once POSTSTART has run, so sampling between the two
	// is what made this test flake. Poll the state under test.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.rt.isRunning(healContainer) &&
			healNode(t, f.g, healContainer).ActualStatus == graph.StatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	assert.True(t, f.rt.isRunning(healContainer),
		"the periodic pass must restart a container that died while the process stayed up")
	assert.Equal(t, graph.StatusRunning, healNode(t, f.g, healContainer).ActualStatus)
	ensures, _, _ := f.rt.counts()
	assert.Positive(t, ensures, "the repair must have gone through the container runtime")
}

// ── error recovery ────────────────────────────────────────────────────

// The self-heal pass is the consumer of `retryable: true` on an operation row: an
// app that failed, whose cause has been cleared, comes back on the next tick
// without a human submitting an install.
func TestSelfHeal_RetriesARetryableFailure(t *testing.T) {
	db := testdb.SetupTestDB(t)
	ops := store.NewOperationStore(db)
	f := newHealFixture(t, 0)
	f.orch.config.Operations = ops

	// Reproduce the reported state: node in ERROR, row says a retry may help.
	require.NoError(t, f.g.SetActualStatus(healContainer, graph.StatusError, "permission denied"))
	require.NoError(t, f.apps.UpdateStatus(healApp, "error"))
	require.NoError(t, ops.Start(healApp, "op-1", store.OpTypeInstall, store.OpPhasePrestart))
	require.NoError(t, ops.Fail(healApp, store.OpPhasePrestart, "permission denied", true))

	// The cause is cleared: the configurator now succeeds.
	f.converge(NewReconcileIntent())

	assert.Equal(t, graph.StatusRunning, healNode(t, f.g, healContainer).ActualStatus,
		"a retryable failure must converge once the cause is gone")
	op, err := ops.Get(healApp)
	require.NoError(t, err)
	assert.Equal(t, store.OpStatusDone, op.Status, "the retry must not leave the failure as the last word")
}

// A failure explicitly marked non-retryable stays terminal. Without the
// distinction the pass would hammer a permanently broken app forever on a
// cause no retry can fix.
func TestSelfHeal_LeavesNonRetryableFailureTerminal(t *testing.T) {
	db := testdb.SetupTestDB(t)
	ops := store.NewOperationStore(db)
	f := newHealFixture(t, 0)
	f.orch.config.Operations = ops

	require.NoError(t, f.g.SetActualStatus(healContainer, graph.StatusError, "unsupported license"))
	require.NoError(t, ops.Start(healApp, "op-1", store.OpTypeInstall, store.OpPhasePrestart))
	require.NoError(t, ops.Fail(healApp, store.OpPhasePrestart, "unsupported license", false))

	f.converge(NewReconcileIntent())

	assert.Equal(t, graph.StatusError, healNode(t, f.g, healContainer).ActualStatus,
		"a non-retryable failure must not be retried by the periodic pass")
}

// Re-driving a node whose app is on its way out would fight the removal the
// same pass is about to perform.
func TestSelfHeal_DoesNotResurrectAnUninstallingApp(t *testing.T) {
	db := testdb.SetupTestDB(t)
	ops := store.NewOperationStore(db)
	f := newHealFixture(t, 0)
	f.orch.config.Operations = ops

	require.NoError(t, f.g.SetActualStatus(healContainer, graph.StatusError, "boom"))
	require.NoError(t, f.apps.UpdateStatus(healApp, "uninstalling"))
	require.NoError(t, ops.Start(healApp, "op-1", store.OpTypeInstall, store.OpPhasePrestart))
	require.NoError(t, ops.Fail(healApp, store.OpPhasePrestart, "boom", true))

	f.orch.retryErroredNodes()

	assert.Equal(t, graph.StatusError, healNode(t, f.g, healContainer).ActualStatus,
		"an uninstalling app's node must stay put")
}

// Without an operation store there is nothing that says a retry may help, so
// the pass must not guess.
func TestSelfHeal_NoOperationsStoreIsANoOp(t *testing.T) {
	f := newHealFixture(t, 0)
	require.NoError(t, f.g.SetActualStatus(healContainer, graph.StatusError, "boom"))

	require.NotPanics(t, func() { f.orch.retryErroredNodes() })
	assert.Equal(t, graph.StatusError, healNode(t, f.g, healContainer).ActualStatus)
}

// ── queue interaction ─────────────────────────────────────────────────

// A timer tick that lands during a user burst must be handled by the same
// pass, not queued behind it as extra work.
func TestSelfHeal_CoalescesWithAUserBurst(t *testing.T) {
	q := NewIntentQueue(200 * time.Millisecond)
	q.Enqueue(NewInstallAppIntent("a"))
	q.Enqueue(NewInstallAppIntent("b"))
	q.Enqueue(NewReconcileIntent())

	batch, live := q.WaitAndDrain(context.Background())
	require.True(t, live)
	require.Len(t, batch, 3, "one pass must carry the user intents and the timer tick together")
	assert.IsType(t, ReconcileIntent{}, batch[2])
}

// The timer is idle-based, so continuous user activity keeps pushing it out
// instead of adding passes of its own on top. A plain ticker would fire
// three extra times over this window; the floor between passes is what makes
// "at most one reconcile per interval" true rather than aspirational.
func TestSelfHeal_UserActivityDefersThePeriodicPass(t *testing.T) {
	const (
		interval = 500 * time.Millisecond
		window   = 1500 * time.Millisecond
		every    = 100 * time.Millisecond
	)
	f := newHealFixture(t, interval)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.orch.Start(ctx)
	<-f.orch.Ready()

	rec := watchPasses(ctx, f.orch)
	start := time.Now()
	for time.Since(start) < window {
		f.orch.Enqueue(NewRenameAppIntent(healApp, "Jellyfin"))
		time.Sleep(every)
	}
	time.Sleep(interval)
	rec.stop()

	// Roughly window/every user-triggered passes, plus the ones the recorder
	// caught after Ready. The assertion is the ceiling: with continuous
	// activity the periodic pass must not add itself on top.
	userPasses := int(window / every)
	assert.LessOrEqual(t, rec.count(), userPasses+3,
		"continuous user activity must keep deferring the self-heal pass, not stack it on top")
}

// The debounce in the queue must not stretch the effective self-heal
// interval: with nothing else happening, passes arrive at about the
// configured cadence.
func TestSelfHeal_FiresOnScheduleWhenIdle(t *testing.T) {
	const (
		interval = 200 * time.Millisecond
		window   = 1200 * time.Millisecond
	)
	f := newHealFixture(t, interval)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.orch.Start(ctx)
	<-f.orch.Ready()

	rec := watchPasses(ctx, f.orch)
	time.Sleep(window)
	rec.stop()

	expected := int(window / interval)
	assert.GreaterOrEqual(t, rec.count(), expected-1,
		"the effective interval must not be inflated by the queue's debounce")
	assert.LessOrEqual(t, rec.count(), expected+2)
}

// A non-positive interval means no timer at all: the instance converges on
// intents only, exactly as it did before this feature.
func TestSelfHeal_DisabledIntervalNeverFires(t *testing.T) {
	f := newHealFixture(t, -1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.orch.Start(ctx)
	<-f.orch.Ready()

	rec := watchPasses(ctx, f.orch)
	time.Sleep(500 * time.Millisecond)
	rec.stop()

	assert.Zero(t, rec.count(), "a disabled interval must never submit a pass")
}

// ── drain arm ─────────────────────────────────────────────────────────

// The explicit arm in applyIntents exists so a timer tick cannot be read as
// an unhandled intent. A warning that fires every minute trains everyone to
// ignore the warning that means something.
func TestApplyIntents_ReconcileIntentIsNotUnhandled(t *testing.T) {
	var log bytes.Buffer
	f := newHealFixture(t, 0)
	f.orch.logger = slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo}))

	f.orch.applyIntents([]Intent{NewReconcileIntent()}, map[string]bool{})

	assert.NotContains(t, log.String(), "unhandled intent type")
	assert.Equal(t, "Reconcile", intentTypeName(NewReconcileIntent()))
}

// The pass must not mutate any store on its own: no install rows, no status
// writes, no uninstall flags.
func TestApplyIntents_ReconcileIntentWritesNoStores(t *testing.T) {
	f := newHealFixture(t, 0)
	before, err := f.apps.GetAll()
	require.NoError(t, err)

	pending := map[string]bool{}
	f.orch.applyIntents([]Intent{NewReconcileIntent()}, pending)

	after, err := f.apps.GetAll()
	require.NoError(t, err)
	require.Len(t, after, len(before))
	assert.Equal(t, store.AppStatusRunning, after[0].Status)
	assert.Empty(t, pending, "a reconcile intent must not queue any data clearing")
}

// A reconcile intent that cannot be retried must not error the drain: the
// failure of one node is never a reason to abandon the pass.
func TestApplyIntents_SurvivesAnOperationReadFailure(t *testing.T) {
	f := newHealFixture(t, 0)
	require.NoError(t, f.g.SetActualStatus(healContainer, graph.StatusError, "boom"))

	// No Operations store wired: the retry step is a no-op, not a crash.
	require.NotPanics(t, func() {
		f.orch.applyIntents([]Intent{NewReconcileIntent(), NewRenameAppIntent(healApp, "Media")}, map[string]bool{})
	})
	row, err := f.apps.GetByCatalogID(healApp)
	require.NoError(t, err)
	assert.Equal(t, "Media", row.DisplayName, "the other intent in the batch still applied")
}
