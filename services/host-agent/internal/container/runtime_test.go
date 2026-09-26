// SPDX-License-Identifier: AGPL-3.0-only

package container

import (
	"context"
	"errors"
	"sync"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePodmanClient struct {
	current  *podman.ContainerDetails
	pulled   []string
	progress []podman.PullProgress
	created  []podman.ContainerConfig
	started  []string
	removed  []string
	networks []string

	// pullErr, when set, makes every pull fail. Used to prove a failed pull
	// cannot cost the container that is already running.
	pullErr error
	// events is the chronological log across all mutating calls, so a test
	// can pin ordering and not just the final set of calls.
	events []string
}

func (f *fakePodmanClient) record(event string) {
	f.events = append(f.events, event)
}

func (f *fakePodmanClient) PullImage(_ context.Context, image string) error {
	f.record("pull")
	if f.pullErr != nil {
		return f.pullErr
	}
	f.pulled = append(f.pulled, image)
	return nil
}

func (f *fakePodmanClient) PullImageWithProgress(_ context.Context, image string, onProgress func(podman.PullProgress)) error {
	f.record("pull")
	if f.pullErr != nil {
		return f.pullErr
	}
	f.pulled = append(f.pulled, image)
	onProgress(podman.PullProgress{Phase: "pulling", Percent: 50, Detail: "Copying blob"})
	onProgress(podman.PullProgress{Phase: "done"})
	f.progress = append(f.progress, podman.PullProgress{Phase: "done"})
	return nil
}

func (f *fakePodmanClient) CreateContainer(_ context.Context, config podman.ContainerConfig) (string, error) {
	f.record("create")
	f.created = append(f.created, config)
	f.current = &podman.ContainerDetails{
		ID: "created", Name: config.Name, State: "created", Labels: config.Labels,
	}
	return "created", nil
}

func (f *fakePodmanClient) StartContainer(_ context.Context, name string) error {
	f.record("start")
	f.started = append(f.started, name)
	f.current.State = "running"
	return nil
}

func (f *fakePodmanClient) RemoveContainer(_ context.Context, name string, _ bool) error {
	f.record("remove")
	f.removed = append(f.removed, name)
	f.current = nil
	return nil
}

func (f *fakePodmanClient) InspectContainer(_ context.Context, _ string) (*podman.ContainerDetails, error) {
	return f.current, nil
}

func (f *fakePodmanClient) EnsureNetwork(_ context.Context, name string) error {
	f.networks = append(f.networks, name)
	return nil
}

func (f *fakePodmanClient) Exec(_ context.Context, _ string, _ []string) ([]byte, error) {
	return nil, nil
}

func TestPodmanRuntimeEnsureIsIdempotentAndRecreatesChangedSpec(t *testing.T) {
	client := &fakePodmanClient{}
	runtime := newPodmanRuntime(client)
	spec := Spec{Name: "apps-jellyfin", Image: "jellyfin:1", RestartPolicy: "always"}

	first, err := runtime.Ensure(context.Background(), spec)
	require.NoError(t, err)
	assert.True(t, first.Created)
	assert.True(t, first.Started)
	require.Len(t, client.created, 1)
	assert.Equal(t, "true", client.created[0].Labels[managedLabel])

	second, err := runtime.Ensure(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, EnsureResult{}, second)
	assert.Len(t, client.created, 1)

	spec.Image = "jellyfin:2"
	third, err := runtime.Ensure(context.Background(), spec)
	require.NoError(t, err)
	assert.True(t, third.Recreated)
	assert.Equal(t, []string{"apps-jellyfin"}, client.removed)
	assert.Len(t, client.created, 2)
}

func TestPodmanRuntimeRemoveRefusesUnmanagedContainer(t *testing.T) {
	client := &fakePodmanClient{
		current: &podman.ContainerDetails{Name: "external", State: "running"},
	}
	runtime := newPodmanRuntime(client)

	err := runtime.Remove(context.Background(), "external")
	require.ErrorContains(t, err, "unmanaged")
	assert.Empty(t, client.removed)
}

// The recreate path must honour the same ownership guard Remove does. It
// used to call the raw client directly, so a container that merely shared
// a name with a Bloud spec was destroyed without any check.
func TestPodmanRuntimeEnsureRefusesUnmanagedContainer(t *testing.T) {
	client := &fakePodmanClient{
		current: &podman.ContainerDetails{Name: "apps-jellyfin", State: "running"},
	}
	runtime := newPodmanRuntime(client)

	_, err := runtime.Ensure(context.Background(), Spec{Name: "apps-jellyfin", Image: "jellyfin:2"})
	require.ErrorContains(t, err, "unmanaged")
	assert.Empty(t, client.removed, "an unmanaged container must never be removed")
	assert.Empty(t, client.created)
}

// A registry outage during a spec change must leave the running container
// exactly where it was. The old order removed first and pulled second, so
// this was the scenario that took the app down with no rollback.
func TestPodmanRuntimePullFailureLeavesExistingContainerUntouched(t *testing.T) {
	client := &fakePodmanClient{
		current: &podman.ContainerDetails{
			ID:     "old",
			Name:   "apps-jellyfin",
			State:  "running",
			Labels: map[string]string{managedLabel: "true"},
		},
		pullErr: errors.New("registry unreachable"),
	}
	runtime := newPodmanRuntime(client)

	_, err := runtime.Ensure(context.Background(), Spec{Name: "apps-jellyfin", Image: "jellyfin:2"})
	require.Error(t, err, "the pull failure must surface")
	assert.Empty(t, client.removed, "a failed pull must not cost the running container")
	assert.Empty(t, client.created)
	require.NotNil(t, client.current, "the existing container must still be there")
	assert.Equal(t, "running", client.current.State)
}

// The ordering itself is the contract, not just the absence of removal on
// failure: pull, then remove, then create, then start.
func TestPodmanRuntimeEnsurePullsBeforeRemoving(t *testing.T) {
	client := &fakePodmanClient{
		current: &podman.ContainerDetails{
			ID:     "old",
			Name:   "apps-jellyfin",
			State:  "running",
			Labels: map[string]string{managedLabel: "true"},
		},
	}
	runtime := newPodmanRuntime(client)

	result, err := runtime.Ensure(context.Background(), Spec{Name: "apps-jellyfin", Image: "jellyfin:2"})
	require.NoError(t, err)
	assert.True(t, result.Recreated)
	assert.Equal(t, []string{"pull", "remove", "create", "start"}, client.events)
}

func TestRuntimeRejectsUnsafeContainerName(t *testing.T) {
	runtime := newPodmanRuntime(&fakePodmanClient{})

	_, err := runtime.Ensure(context.Background(), Spec{Name: "../external", Image: "image"})
	require.ErrorContains(t, err, "invalid container name")
}

type pullUpdates struct {
	mu         sync.Mutex
	containers []string
	images     []string
	phases     []string
}

func (p *pullUpdates) record(container, image, phase string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.containers = append(p.containers, container)
	p.images = append(p.images, image)
	p.phases = append(p.phases, phase)
}

func TestPodmanRuntimeEnsureReportsPullProgress(t *testing.T) {
	client := &fakePodmanClient{}
	runtime := newPodmanRuntime(client)

	updates := &pullUpdates{}
	runtime.SetPullProgressReporter(func(containerName, image string, p PullProgress) {
		updates.record(containerName, image, p.Phase)
	})

	_, err := runtime.Ensure(context.Background(), Spec{Name: "apps-jellyfin", Image: "jellyfin:1"})
	require.NoError(t, err)

	updates.mu.Lock()
	defer updates.mu.Unlock()
	require.Equal(t, []string{"apps-jellyfin", "apps-jellyfin"}, updates.containers)
	require.Equal(t, []string{"jellyfin:1", "jellyfin:1"}, updates.images)
	assert.Equal(t, []string{"pulling", "done"}, updates.phases)
}

func TestPodmanRuntimeEnsureWithoutReporterUsesPlainPull(t *testing.T) {
	client := &fakePodmanClient{}
	runtime := newPodmanRuntime(client)

	_, err := runtime.Ensure(context.Background(), Spec{Name: "apps-jellyfin", Image: "jellyfin:1"})
	require.NoError(t, err)

	// No reporter registered: the plain pull path is used and no progress is
	// recorded.
	assert.Empty(t, client.progress)
}
