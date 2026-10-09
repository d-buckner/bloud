// SPDX-License-Identifier: AGPL-3.0-only

package container

import (
	"context"
	"errors"
	"os"
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
	// listed is the set of containers ListContainers returns.
	listed []podman.Container
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

func (f *fakePodmanClient) ListContainers(_ context.Context) ([]podman.Container, error) {
	return f.listed, nil
}

func (f *fakePodmanClient) EnsureNetwork(_ context.Context, name string) error {
	f.networks = append(f.networks, name)
	return nil
}

func (f *fakePodmanClient) Exec(_ context.Context, _ string, _ []string) ([]byte, error) {
	return nil, nil
}

func (f *fakePodmanClient) RemoveHostPath(_ context.Context, path string) error {
	f.record("remove-host-path")
	f.removed = append(f.removed, path)
	return nil
}

func TestPodmanRuntimeRemoveHostPathDelegates(t *testing.T) {
	client := &fakePodmanClient{}
	runtime := newPodmanRuntime(client)

	require.NoError(t, runtime.RemoveHostPath(context.Background(), "/var/tmp/bloud/data/affine"))
	assert.Equal(t, []string{"remove-host-path"}, client.events)
	assert.Equal(t, []string{"/var/tmp/bloud/data/affine"}, client.removed)
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
	assert.Equal(t, "true", client.created[0].Labels[ManagedLabel])

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

// The recreate path must honor the same ownership guard Remove does: calling the
// raw client directly would destroy any container that merely shares a name with
// a Bloud spec, unchecked.
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
			Labels: map[string]string{ManagedLabel: "true"},
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
			Labels: map[string]string{ManagedLabel: "true"},
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

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/env"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestPodmanRuntimeEnsureMergesEnvFileOverDeclaredEnvironment(t *testing.T) {
	envFile := writeEnvFile(t, "# generated\nCALDAV_PASSWORD='from-the-file'\nBEARER_TOKEN=bearer-from-file\n")
	client := &fakePodmanClient{}
	rt := newPodmanRuntime(client)

	_, err := rt.Ensure(context.Background(), Spec{
		Name:        "apps-dav-mcp",
		Image:       "ghcr.io/philflowio/dav-mcp:4.1.2",
		Environment: map[string]string{"PORT": "9333", "CALDAV_PASSWORD": "static-default"},
		EnvFile:     envFile,
	})
	require.NoError(t, err)
	require.Len(t, client.created, 1)

	env := client.created[0].Env
	// The file wins: it carries resolved truth and must not be shadowed by a
	// static default of the same name.
	assert.Equal(t, "from-the-file", env["CALDAV_PASSWORD"])
	assert.Equal(t, "bearer-from-file", env["BEARER_TOKEN"])
	// Declared environment survives the merge.
	assert.Equal(t, "9333", env["PORT"])
}

func TestPodmanRuntimeEnsureFailsOnMissingEnvFile(t *testing.T) {
	missing := t.TempDir() + "/absent-env"
	client := &fakePodmanClient{}
	rt := newPodmanRuntime(client)

	_, err := rt.Ensure(context.Background(), Spec{
		Name:    "apps-dav-mcp",
		Image:   "ghcr.io/philflowio/dav-mcp:4.1.2",
		EnvFile: missing,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read env file")
	// A container created without the file it asked for would start
	// half-configured, so nothing may have been created at all.
	assert.Empty(t, client.created)
}

func TestPodmanRuntimeEnvFileContentsDoNotMoveTheSpecRevision(t *testing.T) {
	envFile := writeEnvFile(t, "CALDAV_PASSWORD=first\n")
	spec := Spec{Name: "apps-dav-mcp", Image: "img", EnvFile: envFile}

	before, err := spec.Revision()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(envFile, []byte("CALDAV_PASSWORD=rotated\n"), 0o600))
	after, err := spec.Revision()
	require.NoError(t, err)

	// Rotating a credential in the file is not catalog spec drift. The
	// configurator reports the recreate; the renderer must not invent one, or
	// the catalog-update diff would reset nodes on every rotation.
	assert.Equal(t, before, after)

	// The path itself is in the revision: pointing at a different file is a
	// real spec change.
	other := spec
	other.EnvFile = writeEnvFile(t, "CALDAV_PASSWORD=first\n")
	otherRev, err := other.Revision()
	require.NoError(t, err)
	assert.NotEqual(t, before, otherRev)
}

func TestParseEnvFile(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    map[string]string
		wantErr string
	}{
		{
			name:    "comments and blanks ignored",
			content: "# comment\n\nA=1\n  \nB=2\n",
			want:    map[string]string{"A": "1", "B": "2"},
		},
		{
			name:    "single and double quotes stripped",
			content: "A='quoted'\nB=\"double\"\n",
			want:    map[string]string{"A": "quoted", "B": "double"},
		},
		{
			name:    "value containing equals signs",
			content: "TOKEN=abc=def=ghi\n",
			want:    map[string]string{"TOKEN": "abc=def=ghi"},
		},
		{
			name:    "empty value is kept",
			content: "EMPTY=\n",
			want:    map[string]string{"EMPTY": ""},
		},
		{
			name:    "line with no equals is rejected",
			content: "A=1\nNOT_AN_ASSIGNMENT\n",
			wantErr: "line 2: expected KEY=value",
		},
		{
			name:    "empty key is rejected",
			content: "=orphan\n",
			wantErr: "line 1: empty key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeEnvFile(t, tc.content)
			got, err := parseEnvFile(path)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseEnvFileMissingFile(t *testing.T) {
	_, err := parseEnvFile(t.TempDir() + "/nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read env file")
}

// TestPodmanRuntimeEnsureCarriesShmSize proves the size survives the trip from
// the runtime-neutral Spec into the create call, which is the only place it can
// take effect.
func TestPodmanRuntimeEnsureCarriesShmSize(t *testing.T) {
	client := &fakePodmanClient{}
	runtime := newPodmanRuntime(client)

	_, err := runtime.Ensure(context.Background(), Spec{
		Name: "apps-authentik-server", Image: "img", ShmSize: 256 << 20,
	})
	require.NoError(t, err)
	require.Len(t, client.created, 1)
	assert.Equal(t, int64(256<<20), client.created[0].ShmSize)
}

// TestShmSizeMovesTheSpecRevision pins that a size change reaches an install
// that already has the container. Ensure recreates on a moved revision and does
// nothing when the revision matches, so a field left out of the hash would leave
// every existing container on the size it was first created with, no matter
// what the catalog later asked for.
func TestShmSizeMovesTheSpecRevision(t *testing.T) {
	base := Spec{Name: "apps-x", Image: "img"}
	before, err := base.Revision()
	require.NoError(t, err)

	raised := base
	raised.ShmSize = 256 << 20
	after, err := raised.Revision()
	require.NoError(t, err)

	assert.NotEqual(t, before, after, "shm size is part of desired state")
}

func TestRuntimeRejectsNegativeShmSize(t *testing.T) {
	client := &fakePodmanClient{}
	runtime := newPodmanRuntime(client)

	_, err := runtime.Ensure(context.Background(), Spec{
		Name: "apps-x", Image: "img", ShmSize: -1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shm size must not be negative")
	assert.Empty(t, client.created, "a rejected spec must not create anything")
}
