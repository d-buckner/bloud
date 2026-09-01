// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package backend abstracts provisioning and lifecycle management of runtime
// environments (Lima VM, native box, etc.).
package backend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/cli/executor"
)

const (
	// nativeRemoteDir is where the host-agent and app data live on a native
	// (non-VM) host. Mirrors the QEMU guest layout.
	nativeRemoteDir = "/var/tmp/bloud-native-runtime"
)

// NativeBackend provisions and manages a native (non-VM) runtime environment
// directly on the current machine. It is the Linux CI backend: the CLI runs
// on the same host as the host-agent, so there is no SSH, no rsync, and no
// VM lifecycle. The provisioning step ensures the user-level systemd manager
// is available (for GitHub Actions runners) and the podman API socket is
// enabled.
type NativeBackend struct {
	projectDir string
	runCmd     func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// NewNativeBackend returns a backend that manages the native runtime on the
// current machine. projectDir is the bloud repo root (the apps dir is
// referenced in place, no copy needed).
func NewNativeBackend(projectDir string) *NativeBackend {
	return &NativeBackend{projectDir: projectDir, runCmd: exec.CommandContext}
}

// Create ensures the native runtime environment is ready: the user-level
// systemd manager is available, the podman API socket is enabled, and the
// runtime directory exists. Idempotent — safe to call on every dev/e2e run.
func (b *NativeBackend) Create(ctx context.Context) error {
	// On GitHub Actions runners (and other headless systemd hosts) the user
	// session may not have a runtime dir yet. loginctl enable-linger makes
	// the user systemd manager available without a login session. This is a
	// no-op on interactive desktops where the user is already logged in.
	if _, err := b.run(ctx, "sh", "-c", "test -d \"$XDG_RUNTIME_DIR\" && test -S \"$XDG_RUNTIME_DIR/systemd/private\""); err != nil {
		// No user systemd yet — try to enable linger (needs root or the
		// user's own session). On GH runners the runner user can sudo.
		if _, err := b.run(ctx, "sudo", "-n", "loginctl", "enable-linger", currentUsername()); err != nil {
			// Linger may already be enabled or sudo unavailable; fall back
			// to checking if the socket exists now.
			if out2, err2 := b.run(ctx, "sh", "-c", "test -S \"$XDG_RUNTIME_DIR/systemd/private\""); err2 != nil {
				return fmt.Errorf("user-level systemd not available and could not enable linger: %w (check: %s)", err2, out2)
			}
		}
	}

	// Enable the podman API socket (rootless podman exposes its API over a
	// Unix socket; the host-agent's podman client needs it).
	if _, err := b.run(ctx, "systemctl", "--user", "enable", "--now", "podman.socket"); err != nil {
		return fmt.Errorf("enable podman.socket: %w", err)
	}

	// Create the runtime directory layout.
	if _, err := b.run(ctx, "mkdir", "-p", nativeRemoteDir+"/host-agent/web/build", nativeRemoteDir+"/data", nativeRemoteDir+"/apps"); err != nil {
		return fmt.Errorf("create runtime dirs: %w", err)
	}

	return nil
}

// Destroy tears down the native runtime: stops the host-agent, removes
// managed containers, and wipes the runtime directory. The host itself is
// untouched.
func (b *NativeBackend) Destroy(ctx context.Context) error {
	// Stop host-agent and any app systemd units.
	b.run(ctx, "sh", "-c", "pkill -f 'host-agent$' 2>/dev/null; systemctl --user stop 'apps-*.service' 'bloud-e2e-host-agent.service' 2>/dev/null; true")

	// Remove all containers.
	b.run(ctx, "sh", "-c", "podman rm -f $(podman ps -aq) 2>/dev/null || true; podman system prune -f 2>/dev/null || true")

	// Wipe the runtime directory.
	if err := os.RemoveAll(nativeRemoteDir); err != nil {
		return fmt.Errorf("remove runtime dir: %w", err)
	}
	return nil
}

// SyncProject is a no-op for the native backend — the project is already on
// the same filesystem, no copy needed.
func (b *NativeBackend) SyncProject(_ context.Context) error { return nil }

// Host returns the runtime host backed by the local machine.
func (b *NativeBackend) Host() executor.Host {
	return executor.NewLocalHost(
		&executor.LocalExecutor{},
		map[string]string{
			"host-agent":  "3000",
			"traefik":     "8080",
			"traefik-tls": "8443",
			"ldap":        "3389",
			"jellyfin":    "8096",
			"authentik":   "9001",
			"immich":      "2283",
			"navidrome":   "4533",
			"affine":      "3010",
			"appflowy":    "8480",
		},
		executor.DataDirs{
			HostAgentDir: nativeRemoteDir + "/host-agent",
			DataDir:      nativeRemoteDir + "/data",
			AppsDir:      filepath.Join(b.projectDir, "apps"),
		},
	)
}

func (b *NativeBackend) run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := b.runCmd(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// currentUsername returns the current user's username.
func currentUsername() string {
	return os.Getenv("USER")
}
