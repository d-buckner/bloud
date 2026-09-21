// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"codeberg.org/d-buckner/bloud/cli/backend"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// buildAndDeploy builds the host-agent binary and frontend, deploys them to
// the runtime, installs the systemd service, and waits for the API to come
// up. Shared by the lifecycle and app E2E runners.
func (r *lifecycle) buildAndDeploy() error {
	r.step("Building host-agent artifacts")
	buildDir, err := os.MkdirTemp("", "bloud-e2e-build-*")
	if err != nil {
		return err
	}
	r.buildDir = buildDir
	defer func() { _ = os.RemoveAll(buildDir) }()

	if err := r.localRun(r.cfg.root, nil, "npm", "run", "build", "--workspace=@bloud/host-agent-web"); err != nil {
		return err
	}
	hostAgentDir := filepath.Join(r.cfg.root, "services", "host-agent")
	buildEnv := append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+r.cfg.goarch)
	if err := r.localRun(hostAgentDir, buildEnv, "go", "build", "-o", filepath.Join(buildDir, "host-agent"), "./cmd/host-agent"); err != nil {
		return err
	}

	r.step("Deploying host-agent, frontend, and app catalog")
	if err := r.remoteRun("mkdir -p \"$1/host-agent/web/build\" \"$1/apps\"", r.cfg.remoteDir); err != nil {
		return err
	}
	if err := r.copyDirectory(filepath.Join(r.cfg.root, "apps"), r.remotePath("apps")); err != nil {
		return err
	}
	if err := r.copyDirectory(filepath.Join(hostAgentDir, "web", "build"), r.remotePath("host-agent/web/build")); err != nil {
		return err
	}
	if err := r.copyFile(filepath.Join(buildDir, "host-agent"), r.remotePath("host-agent/host-agent")); err != nil {
		return err
	}
	if err := r.remoteRun("chmod 755 \"$1/host-agent/host-agent\"", r.cfg.remoteDir); err != nil {
		return err
	}

	r.step("Installing host-agent systemd service")
	unit := renderLifecycleHostAgentUnit(r.cfg)
	unitPath := filepath.Join(buildDir, lifecycleHostAgentUnit)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return err
	}
	if err := r.remoteRun("mkdir -p \"$1/.config/systemd/user\"", r.cfg.remoteHome); err != nil {
		return err
	}
	if err := r.copyFile(unitPath, filepath.Join(r.cfg.remoteHome, ".config", "systemd", "user", lifecycleHostAgentUnit)); err != nil {
		return err
	}
	if err := r.remoteRun(remoteInstallHostAgentScript, lifecycleHostAgentUnit); err != nil {
		return err
	}
	if err := r.remoteRun(remoteWaitForHostAgentScript); err != nil {
		return fmt.Errorf("wait for host-agent API: %w", err)
	}
	return nil
}

func (r *lifecycle) copyDirectory(source, destination string) error {
	if r.cfg.native {
		return r.localRun(r.cfg.root, os.Environ(), "cp", "-a", source+"/.", destination)
	}
	if r.cfg.lima != "" {
		return r.remoteRun(`rm -rf "$2"
mkdir -p "$2"
cp -a "$1/." "$2/"`, source, destination)
	} else if r.cfg.qemu != "" {
		sshCmd := fmt.Sprintf("ssh -i %s -p 2222 -o StrictHostKeyChecking=accept-new", shellQuote(r.cfg.sshKeyFile))
		args := []string{"-a", "--delete", "-e", sshCmd, source + string(os.PathSeparator), r.cfg.sshTarget + ":" + destination + "/"}
		return r.localRun(r.cfg.root, os.Environ(), "rsync", args...)
	} else {
		args := []string{"-a", "--delete", source + string(os.PathSeparator), r.cfg.sshTarget + ":" + shellQuote(destination) + "/"}
		return r.localRun(r.cfg.root, os.Environ(), "rsync", args...)
	}
}

func (r *lifecycle) copyFile(source, destination string) error {
	if r.cfg.native {
		return r.localRun(r.cfg.root, os.Environ(), "cp", source, destination)
	}
	if r.cfg.lima != "" {
		return r.localRun(r.cfg.root, os.Environ(), "limactl", "copy", source, r.cfg.lima+":"+destination)
	} else if r.cfg.qemu != "" {
		sshCmd := fmt.Sprintf("ssh -i %s -p 2222 -o StrictHostKeyChecking=accept-new", shellQuote(r.cfg.sshKeyFile))
		return r.localRun(r.cfg.root, os.Environ(), "rsync", "-a", "-e", sshCmd, source, r.cfg.sshTarget+":"+destination)
	} else {
		return r.localRun(r.cfg.root, os.Environ(), "rsync", "-a", source, r.cfg.sshTarget+":"+shellQuote(destination))
	}
}

func (r *lifecycle) prepareQEMUTarget() error {
	if r.cfg.qemu == "" {
		return nil
	}
	r.step("Provisioning QEMU VM")
	bk := backend.NewQEMUBackend(r.cfg.qemu, r.cfg.root)
	if err := bk.Create(context.Background()); err != nil {
		return fmt.Errorf("QEMU VM provisioning failed: %w", err)
	}
	if err := bk.SyncProject(context.Background()); err != nil {
		return fmt.Errorf("QEMU project sync failed: %w", err)
	}
	return nil
}

var remoteInstallHostAgentScript = `unit="$1"
systemctl --user daemon-reload
systemctl --user enable "$unit"
systemctl --user restart "$unit"`

var remoteWaitForHostAgentScript = `deadline=$((SECONDS + 300))
until curl -fsS http://localhost:3000/api/health >/dev/null; do
  if ((SECONDS >= deadline)); then exit 1; fi
  sleep 2
done`

var remoteEnsureUserScript = `payload="$1"
status="$(curl -fsS http://localhost:3000/api/setup/status)"
if printf '%s' "$status" | grep -q '"setupRequired":true'; then
  deadline=$((SECONDS + 180))
  until curl -fsS http://localhost:3000/api/setup/status | grep -q '"authentikReady":true'; do
    if ((SECONDS >= deadline)); then exit 1; fi
    sleep 3
  done
  curl -fsS -X POST -H 'Content-Type: application/json' -d "$payload" http://localhost:3000/api/setup/create-user | grep -q '"success":true'
fi`

// ingressCanonicalPort is the port Traefik serves the public surface on: 80 on
// the VMs, 8080 on native (unprivileged, so it cannot bind :80).
func (r *lifecycle) ingressCanonicalPort() string {
	if r.cfg.native {
		return "8080"
	}
	return "80"
}

// assertIngressEntrypoints proves the deployed ingress serves on its canonical
// entrypoint and on the always-present 8080 compat entrypoint that app
// containers use to resolve `sso.localhost` for OIDC discovery. The 200 from
// /api/health on each proves requests are routed through to host-agent, not
// just that Traefik is alive. Runs after buildAndDeploy, which waits for the
// API, so the system apps have converged.
func (r *lifecycle) assertIngressEntrypoints() error {
	r.step("Asserting ingress entrypoints (" + r.ingressCanonicalPort() + " canonical, 8080 compat)")
	return r.remoteRun(remoteAssertIngressScript, r.ingressCanonicalPort())
}

var remoteAssertIngressScript = `for port in "$1" 8080; do
  curl -fsS "http://localhost:$port/ping" | grep -qi ok
  test "$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:$port/api/health")" = 200
done`
