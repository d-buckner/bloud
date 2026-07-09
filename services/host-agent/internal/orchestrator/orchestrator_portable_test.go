package orchestrator

import (
	"context"
	"log/slog"
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

func TestPortableOrchestrator_ReconcileState(t *testing.T) {
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
			Image:         "docker.io/jellyfin/jellyfin:10.11.11",
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

	require.NoError(t, appStore.Install("jellyfin", "Jellyfin", "", nil, &store.InstallOptions{Port: 8096}))

	ctx := context.Background()

	// ReconcileState just regenerates routes; lifecycle is driven by the Orchestrator.
	orch.ReconcileState(ctx)
	assert.Empty(t, containers.ensured, "ReconcileState no longer re-ensures containers")
}
