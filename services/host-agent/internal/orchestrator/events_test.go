// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
