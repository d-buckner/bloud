// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

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

func newSyncOrchestrator(apps *FakeAppStore, cat catalog.CacheInterface, rt containerruntime.Runtime) *Orchestrator {
	return NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		new(MockConfiguratorRegistry),
		cat,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{AppStore: apps, Containers: rt},
	)
}

// An installed row whose catalog entry is gone (the app directory was
// removed or renamed, so the loader never produced a metadata.yaml for it)
// must not dereference the nil catalog result. There is no recover()
// anywhere in host-agent, so the old code killed the daemon on the next
// convergence pass; SyncContainerState runs on every pass.
func TestSyncContainerState_CatalogMissDoesNotPanic(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "ghost", DisplayName: "Ghost", Status: "running"})
	rt := new(MockContainerRuntime)
	orch := newSyncOrchestrator(apps, NewFakeCatalogCache(), rt)

	require.NotPanics(t, func() {
		orch.SyncContainerState(context.Background())
	})

	// Nothing was inspectable: the missing app is skipped before the runtime.
	rt.AssertNotCalled(t, "Inspect", mock.Anything, mock.Anything)
	row, err := apps.GetByCatalogID("ghost")
	require.NoError(t, err)
	assert.Equal(t, "running", row.Status, "a catalog miss must not alter the stored status")
}

// The repair path itself, pinned alongside the guard: a single-container app
// whose container is gone is marked stopped so a later pass can re-create
// it.
func TestSyncContainerState_SingleContainerGoneMarksStopped(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "jellyfin", DisplayName: "Jellyfin", Status: "running"})
	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{
		CatalogID:  "jellyfin",
		Containers: []catalog.ContainerDef{{Name: "apps-jellyfin"}},
	})
	rt := new(MockContainerRuntime)
	rt.On("Inspect", mock.Anything, "apps-jellyfin").
		Return(containerruntime.State{Exists: false}, nil)
	orch := newSyncOrchestrator(apps, cat, rt)

	orch.SyncContainerState(context.Background())

	row, err := apps.GetByCatalogID("jellyfin")
	require.NoError(t, err)
	assert.Equal(t, "stopped", row.Status, "a vanished container must be re-driveable")
}

// A multi-container app is still out of scope for this path (its lifecycle
// is tracked via graph events), and the catalog hit must not be mistaken
// for a miss: no status change happens without inspection.
func TestSyncContainerState_MultiContainerSkipped(t *testing.T) {
	apps := NewFakeAppStore()
	apps.AddApp(&store.InstalledApp{CatalogID: "authentik", DisplayName: "Authentik", Status: "running"})
	cat := NewFakeCatalogCache()
	cat.AddApp(&catalog.App{
		CatalogID: "authentik",
		Containers: []catalog.ContainerDef{
			{Name: "apps-authentik-server"},
			{Name: "apps-authentik-worker"},
		},
	})
	rt := new(MockContainerRuntime)
	orch := newSyncOrchestrator(apps, cat, rt)

	orch.SyncContainerState(context.Background())

	rt.AssertNotCalled(t, "Inspect", mock.Anything, mock.Anything)
	row, err := apps.GetByCatalogID("authentik")
	require.NoError(t, err)
	assert.Equal(t, "running", row.Status)
}
