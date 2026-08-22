// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

func newSubmitTestOrchestrator(t *testing.T) (*Orchestrator, *FakeAppStore, *FakeCatalogCache) {
	t.Helper()
	g := graph.New(graph.NewMapRepository())
	fakeStore := NewFakeAppStore()
	fakeCatalog := NewFakeCatalogCache()
	orch := NewOrchestrator(
		g,
		new(MockConfiguratorRegistry),
		fakeCatalog,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{AppStore: fakeStore},
	)
	return orch, fakeStore, fakeCatalog
}

func TestOrchestrator_SubmitInstall_RecordsRowBeforeEnqueue(t *testing.T) {
	orch, fakeStore, fakeCatalog := newSubmitTestOrchestrator(t)
	fakeCatalog.apps["jellyfin"] = &catalog.App{
		CatalogID:   "jellyfin",
		DisplayName: "Jellyfin",
		Version:     "10.11.11",
		Port:        8096,
	}

	orch.Submit(NewInstallAppIntent("jellyfin"))

	// The row exists (status installing) before reconciliation runs, so the
	// API can include it in the 202 response.
	app, err := fakeStore.GetByCatalogID("jellyfin")
	require.NoError(t, err)
	require.NotNil(t, app)
	assert.Equal(t, "installing", app.Status)
	assert.Equal(t, "Jellyfin", app.DisplayName)
	assert.Equal(t, "10.11.11", app.Version)
	assert.Equal(t, 8096, app.Port)
	assert.Equal(t, 1, orch.queue.PendingCount())
}

func TestOrchestrator_SubmitInstall_ReinstallIsIdempotent(t *testing.T) {
	orch, fakeStore, fakeCatalog := newSubmitTestOrchestrator(t)
	fakeCatalog.apps["jellyfin"] = &catalog.App{CatalogID: "jellyfin", DisplayName: "Jellyfin"}

	// A previously failed install: the row exists with a stored error.
	fakeStore.AddApp(&store.InstalledApp{
		CatalogID: "jellyfin", DisplayName: "Jellyfin", Status: "error", LastError: "boom",
	})

	orch.Submit(NewInstallAppIntent("jellyfin"))
	orch.Submit(NewInstallAppIntent("jellyfin"))

	all, err := fakeStore.GetAll()
	require.NoError(t, err)
	require.Len(t, all, 1, "re-submit upserts, never duplicates")
	assert.Equal(t, "installing", all[0].Status)
	assert.Empty(t, all[0].LastError, "reinstall clears last_error")
	assert.Equal(t, 2, orch.queue.PendingCount())
}

func TestOrchestrator_SubmitNonInstall_DoesNotWriteStore(t *testing.T) {
	orch, fakeStore, _ := newSubmitTestOrchestrator(t)

	orch.Submit(NewUninstallAppIntent("jellyfin", false))

	apps, err := fakeStore.GetAll()
	require.NoError(t, err)
	assert.Empty(t, apps)
	assert.Equal(t, 1, orch.queue.PendingCount())
}

func TestOrchestrator_SubmitInstall_AppNotInCatalog_StillEnqueues(t *testing.T) {
	orch, fakeStore, _ := newSubmitTestOrchestrator(t)

	// Defensive path: the API validates catalog membership before calling,
	// but Submit must not fail the enqueue if it can't find the app.
	orch.Submit(NewInstallAppIntent("ghost"))

	apps, err := fakeStore.GetAll()
	require.NoError(t, err)
	assert.Empty(t, apps)
	assert.Equal(t, 1, orch.queue.PendingCount())
}
