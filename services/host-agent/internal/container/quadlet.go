package container

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/podman"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/systemd"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/managedfile"
)

// QuadletRuntime persists desired container state as Quadlet units supervised by systemd.
type QuadletRuntime struct {
	client   podmanClient
	systemd  systemd.Manager
	unitDir  string
	wantedBy string
}

func NewQuadletRuntime(client *podman.Client, manager systemd.Manager, unitDir, wantedBy string) *QuadletRuntime {
	return newQuadletRuntime(client, manager, unitDir, wantedBy)
}

func newQuadletRuntime(client podmanClient, manager systemd.Manager, unitDir, wantedBy string) *QuadletRuntime {
	return &QuadletRuntime{
		client: client, systemd: manager, unitDir: unitDir, wantedBy: wantedBy,
	}
}

func (r *QuadletRuntime) EnsureNetwork(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	return r.client.EnsureNetwork(ctx, name)
}

func (r *QuadletRuntime) Ensure(ctx context.Context, spec Spec) (EnsureResult, error) {
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
	if current != nil && current.Labels[managedLabel] != "true" {
		return EnsureResult{}, fmt.Errorf("refusing to adopt unmanaged container %q", spec.Name)
	}
	content, err := renderQuadlet(spec, revision, r.wantedBy)
	if err != nil {
		return EnsureResult{}, err
	}
	changed, err := managedfile.Write(r.unitPath(spec.Name), content, 0644)
	if err != nil {
		return EnsureResult{}, err
	}
	// Reload when the container is absent so a prior partial failure can recover
	// even when the desired Quadlet file was already written.
	if changed || current == nil {
		if err := r.systemd.Reload(ctx); err != nil {
			return EnsureResult{}, err
		}
	}

	runtimeDrift := current != nil && current.Labels[revisionLabel] != revision
	recreate := current != nil && (changed || runtimeDrift)
	if current == nil || recreate {
		if err := r.client.PullImage(ctx, spec.Image); err != nil {
			return EnsureResult{}, err
		}
	}
	start := current == nil || current.State != "running" || recreate
	if start {
		if err := r.systemd.EnsureRunning(ctx, r.unitName(spec.Name), recreate); err != nil {
			return EnsureResult{}, err
		}
	}
	return EnsureResult{
		Created: current == nil, Recreated: recreate, Started: start,
	}, nil
}

func (r *QuadletRuntime) Remove(ctx context.Context, name string) error {
	if !validContainerName(name) {
		return fmt.Errorf("invalid container name %q", name)
	}
	current, err := r.client.InspectContainer(ctx, name)
	if err != nil {
		return err
	}
	unitPath := r.unitPath(name)
	_, unitErr := os.Stat(unitPath)
	unitExists := unitErr == nil
	if current == nil && !unitExists {
		return nil
	}
	if current != nil && current.Labels[managedLabel] != "true" {
		return fmt.Errorf("refusing to remove unmanaged container %q", name)
	}
	if unitExists && current != nil {
		if err := r.systemd.Stop(ctx, r.unitName(name)); err != nil {
			return err
		}
	}
	if current != nil {
		if err := r.client.RemoveContainer(ctx, name, true); err != nil {
			return err
		}
	}
	if unitExists {
		if err := os.Remove(unitPath); err != nil {
			return fmt.Errorf("remove quadlet unit: %w", err)
		}
		return r.systemd.Reload(ctx)
	}
	return nil
}

func (r *QuadletRuntime) Inspect(ctx context.Context, name string) (State, error) {
	current, err := r.client.InspectContainer(ctx, name)
	if err != nil || current == nil {
		return State{}, err
	}
	return State{Exists: true, Running: current.State == "running"}, nil
}

func (r *QuadletRuntime) unitPath(name string) string {
	return filepath.Join(r.unitDir, name+".container")
}

func (r *QuadletRuntime) unitName(name string) string {
	return name + ".service"
}

func renderQuadlet(spec Spec, revision, wantedBy string) ([]byte, error) {
	if strings.ContainsAny(spec.Name+spec.Image+spec.Network+wantedBy, "\r\n") {
		return nil, fmt.Errorf("quadlet fields may not contain newlines")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "[Unit]\nDescription=Bloud container %s\n", spec.Name)
	if spec.DependsOn != "" {
		fmt.Fprintf(&out, "After=%s\nBindsTo=%s\n", spec.DependsOn, spec.DependsOn)
	}
	fmt.Fprintf(&out, "\n[Container]\n")
	fmt.Fprintf(&out, "ContainerName=%s\nImage=%s\nPull=never\nCgroupsMode=disabled\n", spec.Name, spec.Image)
	if spec.Network != "" {
		fmt.Fprintf(&out, "Network=%s\n", spec.Network)
	}
	for _, port := range spec.Ports {
		protocol := port.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		fmt.Fprintf(&out, "PublishPort=%d:%d/%s\n", port.Host, port.Container, protocol)
	}
	for _, mount := range spec.Mounts {
		value := mount.Source + ":" + mount.Destination
		if len(mount.Options) > 0 {
			value += ":" + strings.Join(mount.Options, ",")
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("quadlet volume may not contain newlines")
		}
		fmt.Fprintf(&out, "Volume=%s\n", value)
	}
	envKeys := sortedKeys(spec.Environment)
	for _, key := range envKeys {
		fmt.Fprintf(&out, "Environment=%s\n", strconv.Quote(key+"="+spec.Environment[key]))
	}
	labels := make(map[string]string, len(spec.Labels)+2)
	for key, value := range spec.Labels {
		labels[key] = value
	}
	labels[managedLabel] = "true"
	labels[revisionLabel] = revision
	for _, key := range sortedKeys(labels) {
		fmt.Fprintf(&out, "Label=%s\n", strconv.Quote(key+"="+labels[key]))
	}
	if len(spec.Command) > 0 {
		quoted := make([]string, 0, len(spec.Command))
		for _, arg := range spec.Command {
			if strings.ContainsAny(arg, "\r\n") {
				return nil, fmt.Errorf("quadlet command may not contain newlines")
			}
			quoted = append(quoted, strconv.Quote(arg))
		}
		fmt.Fprintf(&out, "Exec=%s\n", strings.Join(quoted, " "))
	}
	fmt.Fprintln(&out, "\n[Service]")
	if spec.RestartPolicy != "" {
		fmt.Fprintf(&out, "Restart=%s\n", spec.RestartPolicy)
	}
	if wantedBy != "" {
		fmt.Fprintf(&out, "\n[Install]\nWantedBy=%s\n", wantedBy)
	}
	return []byte(out.String()), nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

