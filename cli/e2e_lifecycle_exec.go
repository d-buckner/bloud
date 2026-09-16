// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"codeberg.org/d-buckner/bloud/cli/backend"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// checkPrerequisites verifies the remote host (HOME, preflight script),
// prepares a QEMU target if one is configured, and provisions the native
// runtime when running without a VM.
func (r *lifecycle) checkPrerequisites() error {
	r.step("Checking host prerequisites")
	home, err := r.remoteOutput("printf %s \"$HOME\"")
	if err != nil {
		return err
	}
	r.cfg.remoteHome = strings.TrimSpace(home)
	if r.cfg.remoteHome == "" {
		return fmt.Errorf("remote HOME is empty")
	}
	if err := r.remoteRun(remotePreflightScript, r.cfg.remoteDir); err != nil {
		return fmt.Errorf("host preflight: %w", err)
	}
	if err := r.prepareQEMUTarget(); err != nil {
		return err
	}
	if r.cfg.native {
		r.step("Provisioning native runtime")
		bk := backend.NewNativeBackend(r.cfg.root)
		if err := bk.Create(context.Background()); err != nil {
			return fmt.Errorf("native runtime provisioning failed: %w", err)
		}
	}
	return nil
}

// runInstallFlow installs Jellyfin through the host-local API in --host-only
// mode, or through the browser (ensure user + Playwright install/login flow)
// otherwise.

func renderLifecycleHostAgentUnit(cfg lifecycleConfig) string {
	var extraEnv strings.Builder
	if cfg.qemu != "" {
		fmt.Fprintf(&extraEnv, "Environment=BLOUD_TRUSTED_LOCAL_NETS=10.0.2.0/24\n")
	}
	return fmt.Sprintf(`[Unit]
Description=Bloud E2E host agent
After=network-online.target podman.socket
Wants=network-online.target podman.socket

[Service]
Type=simple
WorkingDirectory=%s/host-agent
Environment=BLOUD_DATA_DIR=%s/data
Environment=BLOUD_APPS_DIR=%s/apps
Environment=BLOUD_TRAEFIK_DYNAMIC_DIR=%s
Environment=BLOUD_SSO_ISSUER_URL=%s
%s
ExecStart=%s/host-agent/host-agent
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, cfg.remoteDir, cfg.remoteDir, cfg.remoteDir, cfg.traefikDir, ssoIssuerURL(), extraEnv.String(), cfg.remoteDir)
}

func (r *lifecycle) step(message string) {
	fmt.Printf("\n%s==>%s %s\n", colorGreen, colorReset, message)
}

// buildAndDeploy builds the host-agent binary and frontend, deploys them to
// the runtime, installs the systemd service, and waits for the API to come
// up. Shared by the lifecycle and app E2E runners.

func (r *lifecycle) localRun(dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func (r *lifecycle) remoteRun(script string, args ...string) error {
	cmd := r.remoteCommand(script, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = strings.NewReader("set -euo pipefail\n" + script)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("remote command failed: %w", err)
	}
	return nil
}

func (r *lifecycle) remoteOutput(script string, args ...string) (string, error) {
	cmd := r.remoteCommand(script, args...)
	cmd.Stdin = strings.NewReader("set -euo pipefail\n" + script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("remote command failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func (r *lifecycle) remoteCommand(_ string, args ...string) *exec.Cmd {
	commandArgs := []string{}
	if r.cfg.native {
		name := "bash"
		commandArgs = append(commandArgs, "-se", "--")
		commandArgs = append(commandArgs, args...)
		return exec.Command(name, commandArgs...)
	}
	if r.cfg.lima != "" {
		name := "limactl"
		commandArgs = append(commandArgs, "shell", "--start", r.cfg.lima, "bash", "-se", "--")
		commandArgs = append(commandArgs, args...)
		return exec.Command(name, commandArgs...)
	} else if r.cfg.qemu != "" {
		name := "ssh"
		commandArgs = append(commandArgs,
			"-i", r.cfg.sshKeyFile,
			"-p", "2222",
			"-o", "StrictHostKeyChecking=accept-new",
			"-o", "ConnectTimeout=5",
			r.cfg.sshTarget, "bash", "-se", "--")
		for _, arg := range args {
			commandArgs = append(commandArgs, shellQuote(arg))
		}
		return exec.Command(name, commandArgs...)
	} else {
		name := "ssh"
		commandArgs = append(commandArgs, r.cfg.sshTarget, "bash", "-se", "--")
		for _, arg := range args {
			commandArgs = append(commandArgs, shellQuote(arg))
		}
		return exec.Command(name, commandArgs...)
	}
}

func (r *lifecycle) remotePath(relative string) string {
	return filepath.Join(r.cfg.remoteDir, relative)
}

func (r *lifecycle) artifactDir() string {
	return filepath.Join(r.cfg.root, "e2e", "test-results", "runtime-lifecycle")
}

var remotePreflightScript = `command -v systemctl >/dev/null
command -v podman >/dev/null
command -v curl >/dev/null
test "$(uname -s)" = Linux
systemctl --user show-environment >/dev/null
if test -e "$1" && test ! -f "$1/.bloud-e2e-runtime"; then
  echo "refusing to use unowned runtime directory: $1" >&2
  exit 1
fi
mkdir -p "$1"
touch "$1/.bloud-e2e-runtime"
systemctl --user enable --now podman.socket
podman info >/dev/null`
