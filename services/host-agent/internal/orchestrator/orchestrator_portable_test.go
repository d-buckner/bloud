package orchestrator

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeContainerRuntime struct {
	ensured  []containerruntime.Spec
	removed  []string
	networks []string
}

func (f *fakeContainerRuntime) EnsureNetwork(_ context.Context, name string) error {
	f.networks = append(f.networks, name)
	return nil
}

func (f *fakeContainerRuntime) Ensure(_ context.Context, spec containerruntime.Spec) (containerruntime.EnsureResult, error) {
	f.ensured = append(f.ensured, spec)
	return containerruntime.EnsureResult{Created: true, Started: true}, nil
}

func (f *fakeContainerRuntime) Remove(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}

func (f *fakeContainerRuntime) Inspect(_ context.Context, _ string) (containerruntime.State, error) {
	return containerruntime.State{}, nil
}

func TestPortableOrchestratorInstallsAndRemovesFromCatalogTopology(t *testing.T) {
	dataDir := t.TempDir()
	graph := NewFakeAppGraph()
	cache := NewFakeCatalogCache()
	appStore := NewFakeAppStore()
	containers := &fakeContainerRuntime{}
	cache.AddApp(&catalog.App{
		CatalogID:   "jellyfin",
		DisplayName: "Jellyfin",
		Port:        8096,
		Container: &catalog.ContainerSpec{
			Name:          "apps-jellyfin",
			Image:         "docker.io/jellyfin/jellyfin:10.11.7",
			Network:       "apps-net",
			RestartPolicy: "always",
			Volumes: []catalog.ContainerVolume{{
				Source: "{{appDataDir}}/config", Destination: "/config",
			}},
		},
	})

	orch := NewPortable(PortableConfig{
		Graph: graph, CatalogCache: cache, AppStore: appStore, Containers: containers,
		DataDir: dataDir, Logger: slog.Default(),
	})

	// Record in store (as the reconciler would) then run sub-step lifecycle.
	require.NoError(t, appStore.Install("jellyfin", "Jellyfin", "", nil, &store.InstallOptions{Port: 8096}))

	ctx := context.Background()
	require.NoError(t, orch.PreStartApp(ctx, "jellyfin"))
	require.NoError(t, orch.EnsureContainer(ctx, "jellyfin"))
	require.Len(t, containers.ensured, 1)
	assert.Equal(t, []string{"apps-net"}, containers.networks)
	assert.Equal(t, filepath.Join(dataDir, "jellyfin", "config"), containers.ensured[0].Mounts[0].Source)
	app, err := appStore.GetByCatalogID("jellyfin")
	require.NoError(t, err)
	assert.Equal(t, "starting", app.Status, "EnsureContainer sets status to starting")
	require.NoError(t, orch.HealthCheckApp(ctx, "jellyfin"))
	require.NoError(t, orch.PostStartApp(ctx, "jellyfin"))
	require.NoError(t, orch.ProvisionSSO(ctx, "jellyfin"))

	// ReconcileState re-ensures the app.
	orch.ReconcileState(context.Background())
	assert.Len(t, containers.ensured, 2)

	// RemoveApp removes container and store entry.
	err = orch.RemoveApp(context.Background(), "jellyfin", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"apps-jellyfin"}, containers.removed)
	installed, err := appStore.IsInstalled("jellyfin")
	require.NoError(t, err)
	assert.False(t, installed)
}
