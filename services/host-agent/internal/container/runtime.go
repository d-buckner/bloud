package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/podman"
)

const (
	managedLabel  = "io.bloud.managed"
	revisionLabel = "io.bloud.spec-revision"
)

// Spec is the runtime-neutral desired state for one container.
type Spec struct {
	Name          string
	Image         string
	Environment   map[string]string
	Ports         []Port
	Mounts        []Mount
	Labels        map[string]string
	Network       string
	Command       []string
	RestartPolicy string
	DependsOn     string // systemd unit to bind lifecycle to (e.g. "apps-jellyfin.service")
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

// Runtime converges container desired state without exposing Podman-specific operations.
type Runtime interface {
	EnsureNetwork(ctx context.Context, name string) error
	Ensure(ctx context.Context, spec Spec) (EnsureResult, error)
	Remove(ctx context.Context, name string) error
	Inspect(ctx context.Context, name string) (State, error)
}

type podmanClient interface {
	PullImage(ctx context.Context, image string) error
	CreateContainer(ctx context.Context, config podman.ContainerConfig) (string, error)
	StartContainer(ctx context.Context, nameOrID string) error
	RemoveContainer(ctx context.Context, nameOrID string, force bool) error
	InspectContainer(ctx context.Context, nameOrID string) (*podman.ContainerDetails, error)
	EnsureNetwork(ctx context.Context, name string) error
}

// PodmanRuntime implements Runtime using the Podman API.
type PodmanRuntime struct {
	client podmanClient
}

func NewPodmanRuntime(client *podman.Client) *PodmanRuntime {
	return &PodmanRuntime{client: client}
}

func newPodmanRuntime(client podmanClient) *PodmanRuntime {
	return &PodmanRuntime{client: client}
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

	if current != nil && current.Labels[revisionLabel] == revision {
		if current.State == "running" {
			return EnsureResult{}, nil
		}
		if err := r.client.StartContainer(ctx, spec.Name); err != nil {
			return EnsureResult{}, err
		}
		return EnsureResult{Started: true}, nil
	}

	result := EnsureResult{Created: current == nil, Recreated: current != nil}
	if current != nil {
		if err := r.client.RemoveContainer(ctx, spec.Name, true); err != nil {
			return EnsureResult{}, err
		}
	}
	if err := r.client.PullImage(ctx, spec.Image); err != nil {
		return EnsureResult{}, err
	}
	if _, err := r.client.CreateContainer(ctx, toPodmanConfig(spec, revision)); err != nil {
		return EnsureResult{}, err
	}
	if err := r.client.StartContainer(ctx, spec.Name); err != nil {
		return EnsureResult{}, err
	}
	result.Started = true
	return result, nil
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
	if current.Labels[managedLabel] != "true" {
		return fmt.Errorf("refusing to remove unmanaged container %q", name)
	}
	return r.client.RemoveContainer(ctx, name, true)
}

func validateSpec(spec Spec) error {
	if !validContainerName(spec.Name) {
		return fmt.Errorf("invalid container name %q", spec.Name)
	}
	if spec.Image == "" {
		return fmt.Errorf("container image is required")
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

func (r *PodmanRuntime) Inspect(ctx context.Context, name string) (State, error) {
	current, err := r.client.InspectContainer(ctx, name)
	if err != nil || current == nil {
		return State{}, err
	}
	return State{Exists: true, Running: current.State == "running"}, nil
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
	labels[managedLabel] = "true"
	labels[revisionLabel] = revision

	config := podman.ContainerConfig{
		Name:          spec.Name,
		Image:         spec.Image,
		Env:           spec.Environment,
		Labels:        labels,
		Network:       spec.Network,
		Command:       spec.Command,
		RestartPolicy: spec.RestartPolicy,
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
