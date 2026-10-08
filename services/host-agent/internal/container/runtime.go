// SPDX-License-Identifier: AGPL-3.0-only

package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
)

const (
	// AppLabel names the owning catalog app on every managed container
	// (invariant 12).
	AppLabel = "io.bloud.app"
	// ManagedLabel marks a container Bloud created and therefore may
	// remove or recreate.
	ManagedLabel = "io.bloud.managed"
	// SpecRevisionLabel carries the revision of the spec a container was
	// created from, so a caller can compare a desired spec against a
	// running container without recreating it.
	SpecRevisionLabel = "io.bloud.spec-revision"
)

// Spec is the runtime-neutral desired state for one container.
type Spec struct {
	Name        string
	Image       string
	Environment map[string]string
	// EnvFile is a host path holding additional KEY=value lines merged into
	// Environment at create time, the file winning on a name clash. It is how a
	// resolved contract binding reaches an image that reads nothing but its
	// process environment. The revision hashes this path, not the file's
	// contents, so rotating a credential there is not catalog spec drift; the
	// configurator owns the recreate signal. See catalog.ContainerDef.EnvFile.
	EnvFile       string
	ExtraHosts    []string
	Ports         []Port
	Mounts        []Mount
	Labels        map[string]string
	Networks      []string
	Entrypoint    []string
	Command       []string
	RestartPolicy string
	// ShmSize is the size in bytes of the container's /dev/shm tmpfs. Zero
	// leaves the runtime default, which is 64MB under podman. It is a byte
	// count rather than the metadata's size string for two reasons: that is
	// what the create API takes, and this struct is what the spec revision
	// hashes, so canonicalising here makes "256m" and "268435456" the same
	// desired state instead of a spurious recreate.
	ShmSize int64
}

type Port struct {
	Host      int
	Container int
	Protocol  string
}

type Mount struct {
	Source      string
	Destination string
	Options     []string
}

type State struct {
	Exists  bool
	Running bool
}

type EnsureResult struct {
	Created   bool
	Recreated bool
	Started   bool
}

// ContainerInfo is the runtime-neutral view of one container the orchestrator
// needs to diff against the catalog: its name and its labels. The labels carry
// the owner (AppLabel) and the spec revision (SpecRevisionLabel).
type ContainerInfo struct {
	Name   string
	Labels map[string]string
}

// Runtime converges container desired state without exposing Podman-specific operations.
type Runtime interface {
	EnsureNetwork(ctx context.Context, name string) error
	Ensure(ctx context.Context, spec Spec) (EnsureResult, error)
	Remove(ctx context.Context, name string) error
	Inspect(ctx context.Context, name string) (State, error)
	// ListContainers returns every container with its name and labels, so
	// the orchestrator can diff the running set against the catalog.
	ListContainers(ctx context.Context) ([]ContainerInfo, error)
	// Exec runs a command inside a running container. Returns an error if the
	// command exits with a non-zero status, the container is not running, or
	// the context is canceled.
	Exec(ctx context.Context, name string, cmd []string) error
}

// PathRemover is implemented by runtimes that can delete a host path whose
// contents a container wrote as a mapped non-root user. A rootless runtime
// maps those container IDs into its user namespace, so the host-agent user
// cannot delete them with os.RemoveAll even though the directory sits in the
// agent's own data tree; the runtime deletes the path as the namespace root.
// It is an optional capability, type-asserted by callers that need it, so
// plain Runtime implementations and test fakes need no changes.
type PathRemover interface {
	RemoveHostPath(ctx context.Context, path string) error
}

// PullProgress reports one image pull update while a container's image is
// being downloaded (see podman.PullProgress for the field semantics).
type PullProgress = podman.PullProgress

// PullProgressFunc reports pull progress for a container's image.
// containerName is the container the image is being pulled for, which lets
// callers attribute the pull to its owning app.
type PullProgressFunc func(containerName string, image string, progress PullProgress)

// PullProgressReporter is implemented by runtimes that can report image pull
// progress. It is an optional capability: the orchestrator type-asserts to it
// so plain Runtime implementations need no changes.
type PullProgressReporter interface {
	SetPullProgressReporter(fn PullProgressFunc)
}

type podmanClient interface {
	PullImage(ctx context.Context, image string) error
	PullImageWithProgress(ctx context.Context, image string, onProgress func(podman.PullProgress)) error
	CreateContainer(ctx context.Context, config podman.ContainerConfig) (string, error)
	StartContainer(ctx context.Context, nameOrID string) error
	RemoveContainer(ctx context.Context, nameOrID string, force bool) error
	InspectContainer(ctx context.Context, nameOrID string) (*podman.ContainerDetails, error)
	ListContainers(ctx context.Context) ([]podman.Container, error)
	EnsureNetwork(ctx context.Context, name string) error
	Exec(ctx context.Context, containerName string, cmd []string) ([]byte, error)
	RemoveHostPath(ctx context.Context, path string) error
}

// PodmanRuntime implements Runtime using the Podman API.
type PodmanRuntime struct {
	client podmanClient

	mu           sync.Mutex
	pullReporter PullProgressFunc
}

func NewPodmanRuntime(client *podman.Client) *PodmanRuntime {
	return &PodmanRuntime{client: client}
}

func newPodmanRuntime(client podmanClient) *PodmanRuntime {
	return &PodmanRuntime{client: client}
}

// SetPullProgressReporter registers a callback that receives image pull
// progress while Ensure creates containers. Nil (or no reporter) keeps the
// plain no-progress pull path.
func (r *PodmanRuntime) SetPullProgressReporter(fn PullProgressFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pullReporter = fn
}

// pullImage pulls spec's image, reporting progress to the registered
// reporter (when any) so the orchestrator can broadcast pull events.
func (r *PodmanRuntime) pullImage(ctx context.Context, containerName, image string) error {
	r.mu.Lock()
	reporter := r.pullReporter
	r.mu.Unlock()
	if reporter == nil {
		return r.client.PullImage(ctx, image)
	}
	return r.client.PullImageWithProgress(ctx, image, func(p podman.PullProgress) {
		reporter(containerName, image, p)
	})
}

func (r *PodmanRuntime) Ensure(ctx context.Context, spec Spec) (EnsureResult, error) {
	if err := validateSpec(spec); err != nil {
		return EnsureResult{}, err
	}

	revision, err := specRevision(spec)
	if err != nil {
		return EnsureResult{}, err
	}

	current, err := r.client.InspectContainer(ctx, spec.Name)
	if err != nil {
		return EnsureResult{}, err
	}

	if current != nil && current.Labels[SpecRevisionLabel] == revision {
		if current.State == "running" {
			return EnsureResult{}, nil
		}
		if err := r.client.StartContainer(ctx, spec.Name); err != nil {
			return EnsureResult{}, err
		}
		return EnsureResult{Started: true}, nil
	}

	result := EnsureResult{Created: current == nil, Recreated: current != nil}
	if current != nil && !isManaged(current) {
		return EnsureResult{}, fmt.Errorf("refusing to recreate unmanaged container %q", spec.Name)
	}

	if err := r.replaceContainer(ctx, spec, revision, current != nil); err != nil {
		return EnsureResult{}, err
	}
	result.Started = true
	return result, nil
}

// replaceContainer pulls the image, removes the container being replaced, then
// creates and starts the new one. Split out of Ensure because the env-file merge
// is a decision point of its own and this path already carried most of that
// function's branches.
func (r *PodmanRuntime) replaceContainer(
	ctx context.Context, spec Spec, revision string, hadCurrent bool,
) error {
	// Pull before anything is destroyed. The old order removed the running
	// container and then pulled, so a registry outage, a rate limit or a
	// digest mismatch left the app with no container and no rollback.
	if err := r.pullImage(ctx, spec.Name, spec.Image); err != nil {
		return err
	}
	if hadCurrent {
		if err := r.client.RemoveContainer(ctx, spec.Name, true); err != nil {
			return err
		}
	}
	config := toPodmanConfig(spec, revision)
	if err := applyEnvFile(&config, spec.EnvFile); err != nil {
		return fmt.Errorf("container %q: %w", spec.Name, err)
	}
	if _, err := r.client.CreateContainer(ctx, config); err != nil {
		return err
	}
	return r.client.StartContainer(ctx, spec.Name)
}

func (r *PodmanRuntime) EnsureNetwork(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	return r.client.EnsureNetwork(ctx, name)
}

func (r *PodmanRuntime) Remove(ctx context.Context, name string) error {
	if !validContainerName(name) {
		return fmt.Errorf("invalid container name %q", name)
	}
	current, err := r.client.InspectContainer(ctx, name)
	if err != nil || current == nil {
		return err
	}
	if !isManaged(current) {
		return fmt.Errorf("refusing to remove unmanaged container %q", name)
	}
	return r.client.RemoveContainer(ctx, name, true)
}

// isManaged reports whether an inspected container carries Bloud's
// ownership label. Every destructive path checks it, so a name collision
// with a container Bloud did not create can never destroy it.
func isManaged(details *podman.ContainerDetails) bool {
	return details != nil && details.Labels[ManagedLabel] == "true"
}

func validateSpec(spec Spec) error {
	if !validContainerName(spec.Name) {
		return fmt.Errorf("invalid container name %q", spec.Name)
	}
	if spec.Image == "" {
		return fmt.Errorf("container image is required")
	}
	if spec.ShmSize < 0 {
		return fmt.Errorf("container %q: shm size must not be negative", spec.Name)
	}
	return nil
}

func validContainerName(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '-', char == '_', char == '.':
		default:
			return false
		}
	}
	return true
}

func (r *PodmanRuntime) Exec(ctx context.Context, name string, cmd []string) error {
	_, err := r.client.Exec(ctx, name, cmd)
	return err
}

// RemoveHostPath deletes a host path through the runtime's user namespace,
// which is the only vantage point that can remove files a container wrote as a
// non-root user.
func (r *PodmanRuntime) RemoveHostPath(ctx context.Context, path string) error {
	return r.client.RemoveHostPath(ctx, path)
}

func (r *PodmanRuntime) Inspect(ctx context.Context, name string) (State, error) {
	current, err := r.client.InspectContainer(ctx, name)
	if err != nil || current == nil {
		return State{}, err
	}
	return State{Exists: true, Running: current.State == "running"}, nil
}

// ListContainers returns every container with its name and labels, translating
// podman's leading-slash names into the bare names the rest of the runtime
// addresses (spec.Name is written without the slash).
func (r *PodmanRuntime) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	containers, err := r.client.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ContainerInfo, 0, len(containers))
	for _, c := range containers {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, ContainerInfo{Name: name, Labels: c.Labels})
	}
	return out, nil
}

// Revision returns the deterministic revision stored as SpecRevisionLabel on a
// container created from this spec. Callers compare it against a running
// container's label to detect a change without recreating the container.
func (s Spec) Revision() (string, error) {
	return specRevision(s)
}

func specRevision(spec Spec) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("marshal container spec: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func toPodmanConfig(spec Spec, revision string) podman.ContainerConfig {
	labels := make(map[string]string, len(spec.Labels)+2)
	for key, value := range spec.Labels {
		labels[key] = value
	}
	labels[ManagedLabel] = "true"
	labels[SpecRevisionLabel] = revision
	env := make(map[string]string, len(spec.Environment))
	for key, value := range spec.Environment {
		env[key] = value
	}
	config := podman.ContainerConfig{
		Name:          spec.Name,
		Image:         spec.Image,
		Env:           env,
		ExtraHosts:    spec.ExtraHosts,
		Labels:        labels,
		Networks:      spec.Networks,
		Entrypoint:    spec.Entrypoint,
		Command:       spec.Command,
		RestartPolicy: spec.RestartPolicy,
		ShmSize:       spec.ShmSize,
	}
	for _, port := range spec.Ports {
		config.Ports = append(config.Ports, podman.PortMapping{
			HostPort:      port.Host,
			ContainerPort: port.Container,
			Protocol:      port.Protocol,
		})
	}
	for _, mount := range spec.Mounts {
		config.Volumes = append(config.Volumes, podman.VolumeMount{
			Source:      mount.Source,
			Destination: mount.Destination,
			Type:        "bind",
			Options:     mount.Options,
		})
	}
	return config
}

// applyEnvFile merges the KEY=value lines in envFile into config.Env, the file
// taking precedence over entries the spec declared directly. An empty path is a
// no-op, since most containers declare no env file.
//
// A declared path that cannot be read is an error rather than a silent skip: a
// container created without the file it asked for starts half-configured and
// fails somewhere far from the cause.
func applyEnvFile(config *podman.ContainerConfig, envFile string) error {
	if envFile == "" {
		return nil
	}
	values, err := parseEnvFile(envFile)
	if err != nil {
		return err
	}
	if config.Env == nil {
		config.Env = map[string]string{}
	}
	for key, value := range values {
		config.Env[key] = value
	}
	return nil
}

// parseEnvFile reads a podman-style env file: one KEY=value per line, blank
// lines and `#` comments ignored, one layer of surrounding quotes stripped from
// the value. A line with no `=` is rejected rather than skipped, so a malformed
// generated file fails the create loudly instead of quietly dropping a variable
// the app then reads as unset.
func parseEnvFile(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read env file %s: %w", path, err)
	}
	out := make(map[string]string)
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("env file %s line %d: expected KEY=value, got %q", path, i+1, line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("env file %s line %d: empty key", path, i+1)
		}
		out[key] = unquoteEnvValue(value)
	}
	return out, nil
}

// unquoteEnvValue strips one layer of matching single or double quotes and
// returns the remainder. An unquoted value comes back as it went in.
func unquoteEnvValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`)) ||
			(strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'")) {
			return value[1 : len(value)-1]
		}
	}
	return value
}
