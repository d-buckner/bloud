// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
)

// newRemoveTestOrchestrator builds an orchestrator rooted at a temp data dir.
func newRemoveTestOrchestrator(t *testing.T, runtime containerruntime.Runtime) (*Orchestrator, *MockConfiguratorRegistry, string) {
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
		OrchestratorConfig{Containers: runtime},
	)
	return orch, registry, dataDir
}

// pathRemovalSpy wraps MockContainerRuntime with the runtime's optional
// PathRemover capability, recording the order in which the container removal
// and the data-directory removal happen.
type pathRemovalSpy struct {
	*MockContainerRuntime
	events []string
}

func (s *pathRemovalSpy) RemoveHostPath(_ context.Context, path string) error {
	s.events = append(s.events, "remove-host-path:"+path)
	return os.RemoveAll(path)
}

// TestRemoveMultiContainerApp_ClearData_RemovesDataAfterContainers covers the
// postgres-ownership case: the postgres image keeps its data as a non-root
// container user, leaving a mode-0700 volume directory on the host that the
// host-agent user cannot delete. The runtime removes the app data directory as
// the root of its user namespace, and it must do so only after the containers
// are gone: emptying the volumes while a container is still alive leaves the
// app free to write again as it shuts down (a live Manticore rewrites
// manticore.json on SIGTERM), and those bytes are then unreachable to the
// host user.
func TestRemoveMultiContainerApp_ClearData_RemovesDataAfterContainers(t *testing.T) {
	mockRuntime := new(MockContainerRuntime)
	spy := &pathRemovalSpy{MockContainerRuntime: mockRuntime}
	orch, registry, dataDir := newRemoveTestOrchestrator(t, spy)

	pgName := "apps-affine-postgres"
	require.NoError(t, orch.graph.AddNode(pgName))
	registry.On("Get", pgName).Return(nil)

	// The app data volume, with content.
	pgData := filepath.Join(dataDir, "affine", "postgres")
	require.NoError(t, os.MkdirAll(pgData, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(pgData, "PG_VERSION"), []byte("16"), 0o600))

	mockRuntime.On("Remove", mock.Anything, pgName).
		Run(func(mock.Arguments) { spy.events = append(spy.events, "remove-container") }).
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

	require.Equal(t, []string{
		"remove-container",
		"remove-host-path:" + filepath.Join(dataDir, "affine"),
	}, spy.events, "the data directory is removed only after its containers are gone")
	_, err := os.Stat(filepath.Join(dataDir, "affine"))
	assert.True(t, os.IsNotExist(err), "app data directory should be fully removed")
	mockRuntime.AssertExpectations(t)
}

// TestRemoveMultiContainerApp_ClearData_FallsBackToHostRemoval verifies a
// runtime without the PathRemover capability still gets the host-side
// removal, which is enough for every host-owned directory.
func TestRemoveMultiContainerApp_ClearData_FallsBackToHostRemoval(t *testing.T) {
	mockRuntime := new(MockContainerRuntime)
	orch, registry, dataDir := newRemoveTestOrchestrator(t, mockRuntime)

	pgName := "apps-affine-postgres"
	require.NoError(t, orch.graph.AddNode(pgName))
	registry.On("Get", pgName).Return(nil)

	appData := filepath.Join(dataDir, "affine")
	require.NoError(t, os.MkdirAll(appData, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appData, "config.json"), []byte("{}"), 0o600))

	mockRuntime.On("Remove", mock.Anything, pgName).Return(nil)

	defs := []catalog.ContainerDef{{Name: pgName, Image: "docker.io/pgvector/pgvector:pg16"}}

	require.NoError(t, orch.removeMultiContainerApp(context.Background(), "affine", defs, true))

	_, err := os.Stat(appData)
	assert.True(t, os.IsNotExist(err), "host-owned data should still be removed")
	mockRuntime.AssertExpectations(t)
}

// TestRemoveMultiContainerApp_KeepData_HoldsDirectory verifies clearData=false
// leaves the data directory alone.
func TestRemoveMultiContainerApp_KeepData_HoldsDirectory(t *testing.T) {
	mockRuntime := new(MockContainerRuntime)
	orch, registry, dataDir := newRemoveTestOrchestrator(t, mockRuntime)

	pgName := "apps-affine-postgres"
	require.NoError(t, orch.graph.AddNode(pgName))
	registry.On("Get", pgName).Return(nil)

	appData := filepath.Join(dataDir, "affine")
	require.NoError(t, os.MkdirAll(appData, 0o755))

	mockRuntime.On("Remove", mock.Anything, pgName).Return(nil)

	defs := []catalog.ContainerDef{{Name: pgName, Image: "docker.io/pgvector/pgvector:pg16"}}

	require.NoError(t, orch.removeMultiContainerApp(context.Background(), "affine", defs, false))

	_, err := os.Stat(appData)
	assert.NoError(t, err, "clearData=false must keep the data directory")
	mockRuntime.AssertExpectations(t)
}

// TestRemoveMultiContainerApp_ClearData_IgnoresForeignVolumes verifies
// volumes whose host source is outside the app data directory (e.g. shared
// media) are never touched: only the app data directory is removed.
func TestRemoveMultiContainerApp_ClearData_IgnoresForeignVolumes(t *testing.T) {
	mockRuntime := new(MockContainerRuntime)
	spy := &pathRemovalSpy{MockContainerRuntime: mockRuntime}
	orch, registry, dataDir := newRemoveTestOrchestrator(t, spy)

	mediaName := "apps-myapp"
	require.NoError(t, orch.graph.AddNode(mediaName))
	registry.On("Get", mediaName).Return(nil)

	mediaDir := filepath.Join(dataDir, "media", "movies")
	require.NoError(t, os.MkdirAll(mediaDir, 0o755))

	mockRuntime.On("Remove", mock.Anything, mediaName).Return(nil)

	defs := []catalog.ContainerDef{{
		Name:  mediaName,
		Image: "myapp:1.0",
		Volumes: []catalog.ContainerVolume{{
			Source:      "{{dataDir}}/media/movies",
			Destination: "/movies",
		}},
	}}

	require.NoError(t, orch.removeMultiContainerApp(context.Background(), "myapp", defs, true))

	_, err := os.Stat(mediaDir)
	assert.NoError(t, err, "foreign volume must not be deleted")
	assert.Equal(t, []string{"remove-host-path:" + filepath.Join(dataDir, "myapp")}, spy.events,
		"only the app data directory is handed to the runtime")
	mockRuntime.AssertExpectations(t)
}
