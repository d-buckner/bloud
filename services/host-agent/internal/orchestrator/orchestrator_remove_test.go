// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
)

// newRemoveTestOrchestrator builds an orchestrator with a mock runtime
// rooted at a temp data dir.
func newRemoveTestOrchestrator(t *testing.T, mockRuntime *MockContainerRuntime) (*Orchestrator, *MockConfiguratorRegistry, string) {
	t.Helper()
	dataDir := t.TempDir()
	g := graph.New(graph.NewMapRepository())
	registry := new(MockConfiguratorRegistry)
	catalogCache := new(MockCatalogCache)
	orch := NewOrchestrator(
		g,
		registry,
		catalogCache,
		dataDir,
		newTestLogger(),
		OrchestratorConfig{Containers: mockRuntime},
	)
	return orch, registry, dataDir
}

// TestRemoveMultiContainerApp_ClearData_ExecutesInContainerCleanup covers
// the postgres-ownership case: the postgres image keeps its data as a
// non-root container user, leaving a mode-0700 volume directory on the
// host that the host-agent user cannot delete. The orchestrator must
// therefore empty the volume from inside the container (Exec) before the
// container is removed, so the host-side RemoveAll can finish the job.
func TestRemoveMultiContainerApp_ClearData_ExecutesInContainerCleanup(t *testing.T) {
	mockRuntime := new(MockContainerRuntime)
	orch, registry, dataDir := newRemoveTestOrchestrator(t, mockRuntime)

	pgName := "apps-affine-postgres"
	require.NoError(t, orch.graph.AddNode(pgName))
	orch.registerContainerOwner(pgName, "affine")
	registry.On("Get", pgName).Return(nil)

	// The app data volume, with content.
	pgData := filepath.Join(dataDir, "affine", "postgres")
	require.NoError(t, os.MkdirAll(pgData, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(pgData, "PG_VERSION"), []byte("16"), 0o600))

	var callOrder []string
	mockRuntime.On("Inspect", mock.Anything, pgName).
		Return(containerruntime.State{Exists: true, Running: true}, nil)
	mockRuntime.On("Exec", mock.Anything, pgName, mock.Anything).
		Run(func(args mock.Arguments) {
			callOrder = append(callOrder, "exec")
			// Emulate the in-container `find -delete`: empty the volume.
			entries, _ := os.ReadDir(pgData)
			for _, e := range entries {
				require.NoError(t, os.Remove(filepath.Join(pgData, e.Name())))
			}
		}).
		Return(nil)
	mockRuntime.On("Remove", mock.Anything, pgName).
		Run(func(_ mock.Arguments) { callOrder = append(callOrder, "remove") }).
		Return(nil)

	defs := []catalog.ContainerDef{{
		Name:  pgName,
		Image: "docker.io/pgvector/pgvector:pg16",
		Volumes: []catalog.ContainerVolume{{
			Source:      "{{appDataDir}}/postgres",
			Destination: "/var/lib/postgresql/data",
		}},
	}}

	require.NoError(t, orch.removeMultiContainerApp(context.Background(), "affine", defs, true))

	require.Equal(t, []string{"exec", "remove"}, callOrder,
		"in-container cleanup must happen before container removal")
	// With the volume emptied, the host-side RemoveAll deletes everything.
	_, err := os.Stat(filepath.Join(dataDir, "affine"))
	assert.True(t, os.IsNotExist(err), "app data directory should be fully removed")
	mockRuntime.AssertExpectations(t)
}

// TestRemoveMultiContainerApp_ClearData_ExecUsesVolumeDestination verifies
// the cleanup command targets the container-side mount point.
func TestRemoveMultiContainerApp_ClearData_ExecUsesVolumeDestination(t *testing.T) {
	mockRuntime := new(MockContainerRuntime)
	orch, registry, _ := newRemoveTestOrchestrator(t, mockRuntime)

	pgName := "apps-affine-postgres"
	require.NoError(t, orch.graph.AddNode(pgName))
	registry.On("Get", pgName).Return(nil)

	var execCmd []string
	mockRuntime.On("Inspect", mock.Anything, pgName).
		Return(containerruntime.State{Exists: true, Running: true}, nil)
	mockRuntime.On("Exec", mock.Anything, pgName, mock.Anything).
		Run(func(args mock.Arguments) { execCmd = args.Get(2).([]string) }).
		Return(nil)
	mockRuntime.On("Remove", mock.Anything, pgName).Return(nil)

	defs := []catalog.ContainerDef{{
		Name:  pgName,
		Image: "docker.io/pgvector/pgvector:pg16",
		Volumes: []catalog.ContainerVolume{{
			Source:      "{{appDataDir}}/postgres",
			Destination: "/var/lib/postgresql/data",
		}},
	}}

	require.NoError(t, orch.removeMultiContainerApp(context.Background(), "affine", defs, true))

	require.NotEmpty(t, execCmd)
	joined := strings.Join(execCmd, " ")
	assert.Contains(t, joined, "/var/lib/postgresql/data")
	assert.Contains(t, joined, "-mindepth 1 -delete")
}

// TestRemoveMultiContainerApp_ClearData_SkipsExecWhenContainerNotRunning
// verifies the cleanup is best-effort: a stopped container is simply left
// for the host-side RemoveAll (no exec into a dead container).
func TestRemoveMultiContainerApp_ClearData_SkipsExecWhenContainerNotRunning(t *testing.T) {
	mockRuntime := new(MockContainerRuntime)
	orch, registry, dataDir := newRemoveTestOrchestrator(t, mockRuntime)

	pgName := "apps-affine-postgres"
	require.NoError(t, orch.graph.AddNode(pgName))
	registry.On("Get", pgName).Return(nil)

	// Host-owned volume: deletable without in-container help.
	pgData := filepath.Join(dataDir, "affine", "postgres")
	require.NoError(t, os.MkdirAll(pgData, 0o755))

	mockRuntime.On("Inspect", mock.Anything, pgName).
		Return(containerruntime.State{Exists: true, Running: false}, nil)
	mockRuntime.On("Remove", mock.Anything, pgName).Return(nil)

	defs := []catalog.ContainerDef{{
		Name:  pgName,
		Image: "docker.io/pgvector/pgvector:pg16",
		Volumes: []catalog.ContainerVolume{{
			Source:      "{{appDataDir}}/postgres",
			Destination: "/var/lib/postgresql/data",
		}},
	}}

	require.NoError(t, orch.removeMultiContainerApp(context.Background(), "affine", defs, true))

	mockRuntime.AssertNotCalled(t, "Exec", mock.Anything, mock.Anything, mock.Anything)
	_, err := os.Stat(filepath.Join(dataDir, "affine"))
	assert.True(t, os.IsNotExist(err), "host-owned data should still be removed")
}

// TestRemoveMultiContainerApp_ClearData_IgnoresForeignVolumes verifies
// volumes whose host source is outside the app data directory (e.g. shared
// media) are never touched by the in-container cleanup.
func TestRemoveMultiContainerApp_ClearData_IgnoresForeignVolumes(t *testing.T) {
	mockRuntime := new(MockContainerRuntime)
	orch, registry, dataDir := newRemoveTestOrchestrator(t, mockRuntime)

	mediaName := "apps-myapp"
	require.NoError(t, orch.graph.AddNode(mediaName))
	registry.On("Get", mediaName).Return(nil)

	mediaDir := filepath.Join(dataDir, "media", "movies")
	require.NoError(t, os.MkdirAll(mediaDir, 0o755))

	mockRuntime.On("Inspect", mock.Anything, mediaName).
		Return(containerruntime.State{Exists: true, Running: true}, nil)
	mockRuntime.On("Remove", mock.Anything, mediaName).Return(nil)

	defs := []catalog.ContainerDef{{
		Name:  mediaName,
		Image: "myapp:latest",
		Volumes: []catalog.ContainerVolume{{
			Source:      "{{dataDir}}/media/movies",
			Destination: "/movies",
		}},
	}}

	require.NoError(t, orch.removeMultiContainerApp(context.Background(), "myapp", defs, true))

	mockRuntime.AssertNotCalled(t, "Exec", mock.Anything, mock.Anything, mock.Anything)
	_, err := os.Stat(mediaDir)
	assert.NoError(t, err, "foreign volume must not be deleted")
}
