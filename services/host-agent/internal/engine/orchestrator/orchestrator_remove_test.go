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
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/dirs"
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
// app free to write again as it shuts down (many images rewrite state files on
// SIGTERM), and those bytes are then unreachable to the host user.
func TestRemoveMultiContainerApp_ClearData_RemovesDataAfterContainers(t *testing.T) {
	mockRuntime := new(MockContainerRuntime)
	spy := &pathRemovalSpy{MockContainerRuntime: mockRuntime}
	orch, registry, dataDir := newRemoveTestOrchestrator(t, spy)

	pgName := "apps-affine-postgres"
	require.NoError(t, orch.graph.AddNode(pgName))
	registry.On("Get", pgName).Return(nil)

	// The app data volume, with content.
	pgData := filepath.Join(dirs.AppDataDir(dataDir, "affine"), "postgres")
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
		"remove-host-path:" + dirs.AppDataDir(dataDir, "affine"),
	}, spy.events, "the data directory is removed only after its containers are gone")
	_, err := os.Stat(dirs.AppDataDir(dataDir, "affine"))
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

	appData := dirs.AppDataDir(dataDir, "affine")
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

	appData := dirs.AppDataDir(dataDir, "affine")
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
	assert.Equal(t, []string{"remove-host-path:" + dirs.AppDataDir(dataDir, "myapp")}, spy.events,
		"only the app data directory is handed to the runtime")
	mockRuntime.AssertExpectations(t)
}

// newSharedDataRemoveOrchestrator wires an orchestrator with an app store, so
// the shared-tree ownership check has something to consult.
func newSharedDataRemoveOrchestrator(t *testing.T, runtime containerruntime.Runtime, appStore *FakeAppStore) (*Orchestrator, *MockCatalogCache, string) {
	t.Helper()
	dataDir := t.TempDir()
	registry := new(MockConfiguratorRegistry)
	catalogCache := new(MockCatalogCache)
	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		registry,
		catalogCache,
		dataDir,
		newTestLogger(),
		OrchestratorConfig{Containers: runtime, AppStore: appStore},
	)
	registry.On("Get", mock.Anything).Return(nil).Maybe()
	return orch, catalogCache, dataDir
}

func hermesCatalogApp() *catalog.App {
	return &catalog.App{
		CatalogID:      "hermes",
		OwnsSharedData: []string{"{{dataDir}}/hermes/home"},
		Containers: []catalog.ContainerDef{{
			Name:  "apps-hermes",
			Image: "nousresearch/hermes-agent:1",
			Volumes: []catalog.ContainerVolume{
				{Source: "{{dataDir}}/hermes/home", Destination: "/opt/data"},
			},
		}},
	}
}

// TestRemoveMultiContainerApp_ClearData_RemovesOwnedSharedTree covers the
// other half of the shared-tree rule: a tree the app declares it owns is not
// left stranded outside apps/<name>, where no later cleanup path looks and a
// reinstall would silently reuse it.
func TestRemoveMultiContainerApp_ClearData_RemovesOwnedSharedTree(t *testing.T) {
	spy := &pathRemovalSpy{MockContainerRuntime: new(MockContainerRuntime)}
	appStore := NewFakeAppStore()
	require.NoError(t, appStore.Install("hermes", "Hermes", "1.0", nil, nil))
	orch, cache, dataDir := newSharedDataRemoveOrchestrator(t, spy, appStore)
	cache.On("Get", "hermes").Return(hermesCatalogApp(), nil)

	home := dirs.HermesHomeDir(dataDir)
	require.NoError(t, os.MkdirAll(home, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.yaml"), []byte("mcp_servers: {}"), 0o644))
	spy.MockContainerRuntime.On("Remove", mock.Anything, "apps-hermes").Return(nil)

	require.NoError(t, orch.removeMultiContainerApp(context.Background(), "hermes", hermesCatalogApp().Containers, true))

	_, err := os.Stat(home)
	assert.True(t, os.IsNotExist(err), "the shared tree the app owns is cleared with it")
	assert.Equal(t, []string{
		"remove-host-path:" + dirs.AppDataDir(dataDir, "hermes"),
		"remove-host-path:" + home,
	}, spy.events, "the private tree goes first, then the shared tree it owns")
}

// TestRemoveMultiContainerApp_ClearData_LeavesSharedTreeStillMounted verifies
// the ownership claim is checked against reality: while another installed app
// mounts the same path, clearing this one must not take it.
func TestRemoveMultiContainerApp_ClearData_LeavesSharedTreeStillMounted(t *testing.T) {
	spy := &pathRemovalSpy{MockContainerRuntime: new(MockContainerRuntime)}
	appStore := NewFakeAppStore()
	require.NoError(t, appStore.Install("hermes", "Hermes", "1.0", nil, nil))
	require.NoError(t, appStore.Install("other", "Other", "1.0", nil, nil))
	orch, cache, dataDir := newSharedDataRemoveOrchestrator(t, spy, appStore)
	cache.On("Get", "hermes").Return(hermesCatalogApp(), nil)
	cache.On("Get", "other").Return(&catalog.App{
		CatalogID: "other",
		Containers: []catalog.ContainerDef{{
			Name:  "apps-other",
			Image: "other:1",
			Volumes: []catalog.ContainerVolume{
				{Source: "{{dataDir}}/hermes/home", Destination: "/mnt/brain"},
			},
		}},
	}, nil)

	home := dirs.HermesHomeDir(dataDir)
	require.NoError(t, os.MkdirAll(home, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.yaml"), []byte("keep me"), 0o644))
	spy.MockContainerRuntime.On("Remove", mock.Anything, "apps-hermes").Return(nil)

	require.NoError(t, orch.removeMultiContainerApp(context.Background(), "hermes", hermesCatalogApp().Containers, true))

	_, err := os.Stat(filepath.Join(home, "config.yaml"))
	assert.NoError(t, err, "a tree another installed app mounts survives the owner's clear-data")
	assert.Equal(t, []string{"remove-host-path:" + dirs.AppDataDir(dataDir, "hermes")}, spy.events,
		"only the private tree is handed to the runtime")
}

// TestRemoveMultiContainerApp_KeepData_NeverTouchesSharedTree pins that the
// shared tree is only ever in scope when clearData is set.
func TestRemoveMultiContainerApp_KeepData_NeverTouchesSharedTree(t *testing.T) {
	mockRuntime := new(MockContainerRuntime)
	appStore := NewFakeAppStore()
	require.NoError(t, appStore.Install("hermes", "Hermes", "1.0", nil, nil))
	orch, cache, dataDir := newSharedDataRemoveOrchestrator(t, mockRuntime, appStore)
	cache.On("Get", "hermes").Return(hermesCatalogApp(), nil)

	home := dirs.HermesHomeDir(dataDir)
	require.NoError(t, os.MkdirAll(home, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.yaml"), []byte("keep me"), 0o644))
	mockRuntime.On("Remove", mock.Anything, "apps-hermes").Return(nil)

	require.NoError(t, orch.removeMultiContainerApp(context.Background(), "hermes", hermesCatalogApp().Containers, false))

	_, err := os.Stat(filepath.Join(home, "config.yaml"))
	assert.NoError(t, err, "a keep-data uninstall never reaches the shared tree")
}
