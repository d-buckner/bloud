// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// newEventTestOrchestrator builds an orchestrator with a fake app store and a
// live event bus so tests can observe status-sync and event publishing.
func newEventTestOrchestrator(t *testing.T) (*Orchestrator, *graph.Graph, *FakeAppStore, *eventbus.Bus) {
	t.Helper()
	g := graph.New(graph.NewMapRepository())
	fakeStore := NewFakeAppStore()
	bus := eventbus.New()
	orch := NewOrchestrator(
		g,
		new(MockConfiguratorRegistry),
		nil,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{
			AppStore: fakeStore,
			Events:   bus,
		},
	)
	return orch, g, fakeStore, bus
}

func TestOrchestrator_NodeError_SetsLastErrorAndPublishesNodeEvent(t *testing.T) {
	_, g, fakeStore, bus := newEventTestOrchestrator(t)
	events, cancel := bus.Subscribe()
	defer cancel()

	fakeStore.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", Status: "installing"})
	require.NoError(t, g.AddNode("jellyfin"))

	require.NoError(t, g.SetActualStatus("jellyfin", graph.StatusError, "image pull timed out"))

	app, err := fakeStore.GetByCatalogID("jellyfin")
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.Equal(t, "error", app.Status)
	assert.Equal(t, "image pull timed out", app.LastError)

	// The node transition must be published with the user-facing phase.
	select {
	case evt := <-events:
		require.Equal(t, eventbus.TypeNode, evt.Type)
		require.NotNil(t, evt.Node)
		assert.Equal(t, "jellyfin", evt.Node.App)
		assert.Equal(t, "jellyfin", evt.Node.Container)
		assert.Equal(t, "failed", evt.Node.Phase)
		assert.Equal(t, "image pull timed out", evt.Node.Error)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for node event")
	}
}

func TestOrchestrator_NodeRunning_ClearsLastError(t *testing.T) {
	_, g, fakeStore, _ := newEventTestOrchestrator(t)

	fakeStore.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", Status: "error", LastError: "boom"})
	require.NoError(t, g.AddNode("jellyfin"))

	require.NoError(t, g.SetActualStatus("jellyfin", graph.StatusRunning, ""))

	app, err := fakeStore.GetByCatalogID("jellyfin")
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.Equal(t, "running", app.Status)
	assert.Empty(t, app.LastError)
}

func TestOrchestrator_RecordActivity_PublishesActivityEvent(t *testing.T) {
	orch, _, _, bus := newEventTestOrchestrator(t)
	events, cancel := bus.Subscribe()
	defer cancel()

	orch.recordActivity("intent_enqueued", "InstallAppIntent")

	select {
	case evt := <-events:
		require.Equal(t, eventbus.TypeActivity, evt.Type)
		require.NotNil(t, evt.Activity)
		assert.Equal(t, "intent_enqueued", evt.Activity.Event)
		assert.Equal(t, "InstallAppIntent", evt.Activity.Detail)
		assert.False(t, evt.Activity.Time.IsZero())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for activity event")
	}
}

func TestOrchestrator_NoBus_NoPanic(t *testing.T) {
	// Without an event bus configured, transitions must still work.
	to := newTestOrchestrator()
	require.NoError(t, to.g.AddNode("app1"))
	require.NoError(t, to.g.SetActualStatus("app1", graph.StatusError, "x"))
}

// pullTestRuntime is a no-op container runtime that implements
// containerruntime.PullProgressReporter so tests can capture the pull
// progress callback the orchestrator registers.
type pullTestRuntime struct {
	mu       sync.Mutex
	reporter containerruntime.PullProgressFunc
}

func (r *pullTestRuntime) SetPullProgressReporter(fn containerruntime.PullProgressFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reporter = fn
}

func (r *pullTestRuntime) report(containerName, image string, p containerruntime.PullProgress) {
	r.mu.Lock()
	fn := r.reporter
	r.mu.Unlock()
	if fn != nil {
		fn(containerName, image, p)
	}
}

func (r *pullTestRuntime) EnsureNetwork(context.Context, string) error { return nil }
func (r *pullTestRuntime) Ensure(context.Context, containerruntime.Spec) (containerruntime.EnsureResult, error) {
	return containerruntime.EnsureResult{}, nil
}
func (r *pullTestRuntime) Remove(context.Context, string) error { return nil }
func (r *pullTestRuntime) Inspect(context.Context, string) (containerruntime.State, error) {
	return containerruntime.State{}, nil
}
func (r *pullTestRuntime) Exec(context.Context, string, []string) error { return nil }

func TestOrchestrator_PullProgressPublishedWithOwningApp(t *testing.T) {
	g := graph.New(graph.NewMapRepository())
	bus := eventbus.New()
	runtime := &pullTestRuntime{}
	orch := NewOrchestrator(
		g,
		new(MockConfiguratorRegistry),
		nil,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{
			AppStore:   NewFakeAppStore(),
			Containers: runtime,
			Events:     bus,
		},
	)

	// Multi-container node: the pull is attributed to the owning app, not the
	// container name.
	orch.registerContainerOwner("apps-immich-server", "immich")

	events, cancel := bus.Subscribe()
	defer cancel()

	runtime.report("apps-immich-server", "ghcr.io/immich-app/immich-server:v1.126.2",
		containerruntime.PullProgress{Phase: "pulling", Percent: 34, Current: 356515806, Total: 1073741824})

	select {
	case evt := <-events:
		require.Equal(t, eventbus.TypePull, evt.Type)
		require.NotNil(t, evt.Pull)
		assert.Equal(t, "immich", evt.Pull.App)
		assert.Equal(t, "ghcr.io/immich-app/immich-server:v1.126.2", evt.Pull.Image)
		assert.Equal(t, "pulling", evt.Pull.Phase)
		assert.Equal(t, "34% — 340.0 MiB of 1.0 GiB", evt.Pull.Detail)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pull event")
	}
}

func TestOrchestrator_PullProgressUnknownContainerFallsBackToNodeID(t *testing.T) {
	g := graph.New(graph.NewMapRepository())
	bus := eventbus.New()
	runtime := &pullTestRuntime{}
	NewOrchestrator(
		g,
		new(MockConfiguratorRegistry),
		nil,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{AppStore: NewFakeAppStore(), Containers: runtime, Events: bus},
	)

	events, cancel := bus.Subscribe()
	defer cancel()

	runtime.report("jellyfin", "docker.io/jellyfin/jellyfin:10.11.11",
		containerruntime.PullProgress{Phase: "done"})

	select {
	case evt := <-events:
		require.Equal(t, eventbus.TypePull, evt.Type)
		require.NotNil(t, evt.Pull)
		assert.Equal(t, "jellyfin", evt.Pull.App)
		assert.Equal(t, "done", evt.Pull.Phase)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pull event")
	}
}

func TestPullDetail(t *testing.T) {
	assert.Equal(t, "34% — 340.0 MiB of 1.0 GiB",
		pullDetail(containerruntime.PullProgress{Percent: 34, Current: 356515806, Total: 1073741824}))
	assert.Equal(t, "Copying blob sha256:abc",
		pullDetail(containerruntime.PullProgress{Detail: "Copying blob sha256:abc"}))
	assert.Empty(t, pullDetail(containerruntime.PullProgress{}))
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:           "512 B",
		1024:          "1.0 KiB",
		356515806:     "340.0 MiB",
		1073741824:    "1.0 GiB",
		1099511627776: "1.0 TiB",
	}
	for n, want := range cases {
		assert.Equal(t, want, humanBytes(n), "humanBytes(%d)", n)
	}
}

// TestPhaseForStatus covers the graph-state → dashboard-phase mapping.
func TestPhaseForStatus(t *testing.T) {
	cases := map[graph.NodeStatus]string{
		graph.StatusInitializing:    "queued",
		graph.StatusPreStartConfig:  "configuring",
		graph.StatusStarting:        "starting",
		graph.StatusPostStartConfig: "finalizing",
		graph.StatusRunning:         "running",
		graph.StatusError:           "failed",
	}
	for status, want := range cases {
		assert.Equal(t, want, phaseForStatus(status), "phaseForStatus(%s)", status)
	}
	assert.Equal(t, "WEIRD", phaseForStatus(graph.NodeStatus("WEIRD")))
}
