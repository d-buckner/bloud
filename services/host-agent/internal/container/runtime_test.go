// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package container

import (
	"context"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePodmanClient struct {
	current  *podman.ContainerDetails
	pulled   []string
	created  []podman.ContainerConfig
	started  []string
	removed  []string
	networks []string
}

func (f *fakePodmanClient) PullImage(_ context.Context, image string) error {
	f.pulled = append(f.pulled, image)
	return nil
}

func (f *fakePodmanClient) CreateContainer(_ context.Context, config podman.ContainerConfig) (string, error) {
	f.created = append(f.created, config)
	f.current = &podman.ContainerDetails{
		ID: "created", Name: config.Name, State: "created", Labels: config.Labels,
	}
	return "created", nil
}

func (f *fakePodmanClient) StartContainer(_ context.Context, name string) error {
	f.started = append(f.started, name)
	f.current.State = "running"
	return nil
}

func (f *fakePodmanClient) RemoveContainer(_ context.Context, name string, _ bool) error {
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

func TestRuntimeRejectsUnsafeContainerName(t *testing.T) {
	runtime := newPodmanRuntime(&fakePodmanClient{})

	_, err := runtime.Ensure(context.Background(), Spec{Name: "../external", Image: "image"})
	require.ErrorContains(t, err, "invalid container name")
}
