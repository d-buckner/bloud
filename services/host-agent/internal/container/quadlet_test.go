package container

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/podman"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSystemdManager struct {
	reloads  int
	ensured  []string
	restarts []bool
	stopped  []string
}

func (f *fakeSystemdManager) Reload(_ context.Context) error {
	f.reloads++
	return nil
}

func (f *fakeSystemdManager) EnsureRunning(_ context.Context, unit string, restart bool) error {
	f.ensured = append(f.ensured, unit)
	f.restarts = append(f.restarts, restart)
	return nil
}

func (f *fakeSystemdManager) Stop(_ context.Context, unit string) error {
	f.stopped = append(f.stopped, unit)
	return nil
}

func TestQuadletRuntimeWritesAndConvergesManagedUnit(t *testing.T) {
	client := &fakePodmanClient{}
	manager := &fakeSystemdManager{}
	unitDir := t.TempDir()
	runtime := newQuadletRuntime(client, manager, unitDir, "default.target")
	spec := Spec{
		Name: "apps-jellyfin", Image: "jellyfin:1", Network: "apps-net", RestartPolicy: "always",
		Environment: map[string]string{"TZ": "Etc/UTC"},
		Ports:       []Port{{Host: 8096, Container: 8096}},
		Mounts:      []Mount{{Source: "/data/jellyfin/config", Destination: "/config"}},
	}

	result, err := runtime.Ensure(context.Background(), spec)
	require.NoError(t, err)
	assert.True(t, result.Created)
	assert.Equal(t, 1, manager.reloads)
	assert.Equal(t, []string{"apps-jellyfin.service"}, manager.ensured)
	assert.Equal(t, []bool{false}, manager.restarts)
	assert.Equal(t, []string{"jellyfin:1"}, client.pulled)

	content, err := os.ReadFile(filepath.Join(unitDir, "apps-jellyfin.container"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "ContainerName=apps-jellyfin")
	assert.Contains(t, string(content), "CgroupsMode=disabled")
	assert.NotContains(t, string(content), "PodmanArgs=--replace")
	assert.Contains(t, string(content), "Network=apps-net")
	assert.Contains(t, string(content), "PublishPort=8096:8096/tcp")
	assert.Contains(t, string(content), `Label="io.bloud.managed=true"`)
	assert.Contains(t, string(content), "WantedBy=default.target")

	client.current = nil
	second, err := runtime.Ensure(context.Background(), spec)
	require.NoError(t, err)
	assert.True(t, second.Created)
	assert.Equal(t, 2, manager.reloads, "absent container should reload systemd to recover partial failures")
	assert.Len(t, manager.ensured, 2)
}

func TestQuadletRuntimeRestartsObservedRuntimeDrift(t *testing.T) {
	client := &fakePodmanClient{}
	manager := &fakeSystemdManager{}
	runtime := newQuadletRuntime(client, manager, t.TempDir(), "default.target")
	spec := Spec{Name: "apps-jellyfin", Image: "jellyfin:1"}

	_, err := runtime.Ensure(context.Background(), spec)
	require.NoError(t, err)
	client.current = &podman.ContainerDetails{
		Name: "apps-jellyfin", State: "running",
		Labels: map[string]string{managedLabel: "true", revisionLabel: "stale"},
	}

	result, err := runtime.Ensure(context.Background(), spec)
	require.NoError(t, err)
	assert.True(t, result.Recreated)
	assert.Equal(t, true, manager.restarts[len(manager.restarts)-1])
	assert.Equal(t, 1, manager.reloads, "runtime drift should not rewrite an unchanged unit")
}

func TestQuadletRuntimeRestartsChangedManagedContainerAndRemovesUnit(t *testing.T) {
	client := &fakePodmanClient{}
	manager := &fakeSystemdManager{}
	unitDir := t.TempDir()
	runtime := newQuadletRuntime(client, manager, unitDir, "multi-user.target")
	spec := Spec{Name: "apps-jellyfin", Image: "jellyfin:1"}

	_, err := runtime.Ensure(context.Background(), spec)
	require.NoError(t, err)
	revision, err := specRevision(spec)
	require.NoError(t, err)
	client.current = &podman.ContainerDetails{
		Name: "apps-jellyfin", State: "running",
		Labels: map[string]string{managedLabel: "true", revisionLabel: revision},
	}

	spec.Image = "jellyfin:2"
	result, err := runtime.Ensure(context.Background(), spec)
	require.NoError(t, err)
	assert.True(t, result.Recreated)
	assert.Equal(t, true, manager.restarts[len(manager.restarts)-1])

	require.NoError(t, runtime.Remove(context.Background(), "apps-jellyfin"))
	assert.Equal(t, []string{"apps-jellyfin.service"}, manager.stopped)
	assert.Equal(t, []string{"apps-jellyfin"}, client.removed)
	_, err = os.Stat(filepath.Join(unitDir, "apps-jellyfin.container"))
	assert.True(t, os.IsNotExist(err))
}

func TestQuadletRendersDependsOn(t *testing.T) {
	spec := Spec{
		Name: "ts-jellyfin", Image: "tailscale:latest", Network: "apps-net",
		DependsOn: "apps-jellyfin.service",
	}
	content, err := renderQuadlet(spec, "rev", "default.target")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "After=apps-jellyfin.service")
	assert.Contains(t, s, "BindsTo=apps-jellyfin.service")
}

func TestQuadletOmitsDependsOnWhenEmpty(t *testing.T) {
	spec := Spec{Name: "apps-jellyfin", Image: "jellyfin:1"}
	content, err := renderQuadlet(spec, "rev", "default.target")
	require.NoError(t, err)
	s := string(content)
	assert.NotContains(t, s, "After=")
	assert.NotContains(t, s, "BindsTo=")
}

func TestQuadletRuntimeRemovesUnloadedUnit(t *testing.T) {
	client := &fakePodmanClient{}
	manager := &fakeSystemdManager{}
	unitDir := t.TempDir()
	runtime := newQuadletRuntime(client, manager, unitDir, "default.target")
	unitPath := filepath.Join(unitDir, "apps-jellyfin.container")
	require.NoError(t, os.WriteFile(unitPath, []byte("[Container]\n"), 0644))

	require.NoError(t, runtime.Remove(context.Background(), "apps-jellyfin"))

	assert.Empty(t, manager.stopped)
	assert.Equal(t, 1, manager.reloads)
	_, err := os.Stat(unitPath)
	assert.True(t, os.IsNotExist(err))
}
