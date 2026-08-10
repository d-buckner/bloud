package backend

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/cli/executor"
)

// devRemoteDir is where the host-agent and app data live inside the VM.
const devRemoteDir = "/var/tmp/bloud-dev-runtime"

// LimaBackend provisions and manages a Lima VM via `limactl`.
type LimaBackend struct {
	instance   string
	projectDir string
	newCmd     func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// NewLimaBackend returns a backend that manages the named Lima VM. projectDir
// is the bloud repo root on the host (mounted into the VM).
func NewLimaBackend(instance, projectDir string) *LimaBackend {
	return &LimaBackend{instance: instance, projectDir: projectDir, newCmd: exec.CommandContext}
}

// Create ensures the Lima VM exists and is running.
func (b *LimaBackend) Create(ctx context.Context) error {
	out, err := b.run(ctx, "limactl", "list", "--json")
	if err != nil {
		return fmt.Errorf("failed to list Lima VMs: %w", err)
	}

	if !executor.IsVMNamePresent(out, b.instance) {
		if _, err := b.run(ctx, "limactl", "create", "--name="+b.instance,
			filepath.Join(b.projectDir, "dev", "lima.yaml")); err != nil {
			return fmt.Errorf("failed to create Lima VM: %w", err)
		}
	}
	if !executor.IsVMNameRunning(out, b.instance) {
		if _, err := b.run(ctx, "limactl", "start", b.instance); err != nil {
			return fmt.Errorf("failed to start Lima VM: %w", err)
		}
	}

	out, err = b.run(ctx, "limactl", "list", "--json")
	if err != nil {
		return fmt.Errorf("failed to verify Lima VM: %w", err)
	}
	if !executor.IsVMNameRunning(out, b.instance) {
		return fmt.Errorf("Lima VM %q did not become ready", b.instance)
	}
	return nil
}

// Destroy deletes the Lima VM.
func (b *LimaBackend) Destroy(ctx context.Context) error {
	if _, err := b.run(ctx, "limactl", "delete", "--force", b.instance); err != nil {
		return fmt.Errorf("failed to delete Lima VM: %w", err)
	}
	return nil
}

// Host returns the runtime host backed by the Lima VM.
func (b *LimaBackend) Host() executor.Host {
	return executor.NewSSHHost(
		b.instance,
		executor.NewSSHExecutor(b.instance),
		&executor.LocalExecutor{},
		map[string]string{
			"host-agent": "3000",
			"postgres":   "5432",
			"traefik":    "8080",
			"jellyfin":   "8096",
			"authentik":  "9000",
		},
		executor.DataDirs{
			HostAgentDir: devRemoteDir + "/host-agent",
			DataDir:      devRemoteDir + "/data",
			AppsDir:      filepath.Join(b.projectDir, "apps"),
		},
	)
}

func (b *LimaBackend) run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := b.newCmd(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s: %s: %w", name, strings.TrimSpace(stderr.String()), err)
	}
	return stdout.String(), nil
}
