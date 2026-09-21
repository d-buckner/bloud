// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"codeberg.org/d-buckner/bloud/cli/executor"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// integrationRuntimeDir is the guest-side home of the validation runtime: a
// self-contained host-agent deployment (binary, web build, app catalog,
// data) separate from the dev runtime. The tier deploys the current code
// here through the real product path (host-agent + orchestrator + catalog)
// and runs the tier's commands against it, so integration validation
// exercises the same install/reconcile flow as production.
const integrationRuntimeDir = "/var/tmp/bloud-validate-runtime"

// integrationHostAgentUnit supervises the validation runtime's host-agent.

// integrationHostAgentUnit supervises the validation runtime's host-agent.
const integrationHostAgentUnit = "bloud-validate-host-agent.service"

// integrationPreflightScript verifies the guest has everything the
// validation runtime needs. Go is not required in the guest: artifacts are
// built locally and copied in.

// integrationPreflightScript verifies the guest has everything the
// validation runtime needs. Go is not required in the guest: artifacts are
// built locally and copied in.
var integrationPreflightScript = `set -euo pipefail
command -v podman >/dev/null
command -v curl >/dev/null
command -v ldapsearch >/dev/null
test "$(uname -s)" = Linux
systemctl --user show-environment >/dev/null
systemctl --user enable --now podman.socket
podman info >/dev/null`

// integrationStopAgentScript stops whatever holds port 3000 so the
// validation unit can bind: the dev host-agent (foreground under ./bloud
// dev, or the legacy bloud-host-agent.service unit on VMs provisioned by
// older CLI versions) and any prior lifecycle or validation unit. The
// units are disabled so a Restart=on-failure cannot resurrect an agent
// that races the validation unit for the port after the fuser kill.

// integrationStopAgentScript stops whatever holds port 3000 so the
// validation unit can bind: the dev host-agent (foreground under ./bloud
// dev, or the legacy bloud-host-agent.service unit on VMs provisioned by
// older CLI versions) and any prior lifecycle or validation unit. The
// units are disabled so a Restart=on-failure cannot resurrect an agent
// that races the validation unit for the port after the fuser kill.
var integrationStopAgentScript = `for unit in bloud-host-agent.service bloud-e2e-host-agent.service bloud-validate-host-agent.service; do
  systemctl --user disable --now "$unit" >/dev/null 2>&1 || true
done
fuser -k 3000/tcp 2>/dev/null || true
sleep 1`

// integrationWaitAgentScript waits for the validation host-agent API. First
// boot pulls images and converges the system apps before the listener opens,
// so this doubles as the bootstrap convergence gate.

// integrationWaitAgentScript waits for the validation host-agent API. First
// boot pulls images and converges the system apps before the listener opens,
// so this doubles as the bootstrap convergence gate.
var integrationWaitAgentScript = `deadline=$((SECONDS + 1200))
until curl -fsS http://localhost:3000/api/health >/dev/null 2>&1; do
  if ((SECONDS >= deadline)); then
    journalctl --user -u bloud-validate-host-agent.service --no-pager -n 80 || true
    exit 1
  fi
  sleep 5
done`

func renderIntegrationHostAgentUnit(rt, backend string) string {
	var extraEnv string
	if backend == "qemu" {
		extraEnv = "Environment=BLOUD_TRUSTED_LOCAL_NETS=10.0.2.0/24\n"
	}
	if port := traefikPortEnv(backend); port != "" {
		extraEnv += "Environment=BLOUD_TRAEFIK_PORT=" + port + "\n"
	}
	return fmt.Sprintf(`[Unit]
Description=Bloud integration validation host agent
After=network-online.target podman.socket
Wants=network-online.target podman.socket

[Service]
Type=simple
WorkingDirectory=%s/host-agent
Environment=BLOUD_DATA_DIR=%s/data
Environment=BLOUD_APPS_DIR=%s/apps
Environment=BLOUD_TRAEFIK_DYNAMIC_DIR=%s/data/traefik/dynamic
Environment=BLOUD_SSO_ISSUER_URL=%s
%sExecStart=%s/host-agent/host-agent
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, rt, rt, rt, rt, ssoIssuerURL(), extraEnv, rt)
}

// integrationPrepareGuest verifies the guest has everything the tier needs
// and stops any host-agent holding port 3000. Returns a ledger fail reason,
// or "" when the guest is ready.
func integrationPrepareGuest(ctx context.Context, ex executor.Executor, step func(string)) string {
	step("Checking integration prerequisites")
	if res, err := ex.Run(ctx, executor.RunSpec{Command: integrationPreflightScript}); err != nil || res.ExitCode != 0 {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" && err != nil {
			detail = err.Error()
		}
		errorf("integration prerequisites missing (recreate the VM): %s", detail)
		return "integration prerequisites missing"
	}
	step("Stopping any running host-agent (validation runtime takes over port 3000)")
	if res, err := ex.Run(ctx, executor.RunSpec{Command: integrationStopAgentScript}); err != nil || res.ExitCode != 0 {
		errorf("failed to stop running host-agent: %v", err)
		return "failed to stop running host-agent"
	}
	return ""
}

// integrationBuildArtifacts builds the host-agent binary, the frontend, and
// the integration test binary locally into tmpDir. The error message is the
// ledger confidence reason for the failing build.

// integrationBuildArtifacts builds the host-agent binary, the frontend, and
// the integration test binary locally into tmpDir. The error message is the
// ledger confidence reason for the failing build.
func integrationBuildArtifacts(root, hostAgentSrc, tmpDir string, step func(string)) (string, string, error) {
	step("Building host-agent for linux/" + runtime.GOARCH)
	binaryPath := filepath.Join(tmpDir, "host-agent")
	buildCmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/host-agent")
	buildCmd.Dir = hostAgentSrc
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		errorf("host-agent build failed: %v", err)
		return "", "", errors.New("host-agent build failed")
	}

	step("Building frontend")
	webCmd := exec.Command("npm", "run", "build", "--workspace=@bloud/host-agent-web")
	webCmd.Dir = root
	webCmd.Stdout = os.Stdout
	webCmd.Stderr = os.Stderr
	if err := webCmd.Run(); err != nil {
		errorf("frontend build failed: %v", err)
		return "", "", errors.New("frontend build failed")
	}

	step("Building integration test binary")
	testBinary := filepath.Join(tmpDir, "bloud-integration.test")
	testBuild := exec.Command("go", "test", "-tags", "integration", "-c", "-o", testBinary, "./internal/e2e")
	testBuild.Dir = hostAgentSrc
	testBuild.Stdout = os.Stdout
	testBuild.Stderr = os.Stderr
	if err := testBuild.Run(); err != nil {
		errorf("integration test build failed: %v", err)
		return "", "", errors.New("integration test build failed")
	}
	return binaryPath, testBinary, nil
}

// integrationDeploy copies the built artifacts into the guest validation
// runtime dir and initializes runtime secrets (idempotent product command;
// the tests read the real values from secrets.json).

// integrationDeploy copies the built artifacts into the guest validation
// runtime dir and initializes runtime secrets (idempotent product command;
// the tests read the real values from secrets.json).
func integrationDeploy(ctx context.Context, ex executor.Executor, root, hostAgentSrc, rt, binaryPath, testBinary string) error {
	if _, err := ex.Run(ctx, executor.RunSpec{
		Command: fmt.Sprintf("rm -rf %s/host-agent %s/apps && mkdir -p %s/host-agent/web/build %s/apps %s/data %s/bin", rt, rt, rt, rt, rt, rt),
	}); err != nil {
		errorf("failed to prepare runtime dir: %v", err)
		return errors.New("failed to prepare runtime dir")
	}
	deployments := []struct {
		from string
		to   string
	}{
		{binaryPath, rt + "/host-agent/host-agent"},
		{filepath.Join(hostAgentSrc, "web", "build"), rt + "/host-agent/web/build"},
		{filepath.Join(root, "apps"), rt + "/apps"},
		{testBinary, rt + "/bin/bloud-integration.test"},
	}
	for _, d := range deployments {
		if err := ex.CopyTo(ctx, d.from, d.to); err != nil {
			errorf("failed to copy %s into guest: %v", d.from, err)
			return errors.New("deployment failed")
		}
	}
	if _, err := ex.Run(ctx, executor.RunSpec{
		Command: fmt.Sprintf("chmod 755 %s/host-agent/host-agent %s/bin/bloud-integration.test", rt, rt),
	}); err != nil {
		errorf("failed to chmod deployed binaries: %v", err)
		return errors.New("deployment failed")
	}
	if _, err := ex.Run(ctx, executor.RunSpec{
		Command: fmt.Sprintf("%s/host-agent/host-agent init-secrets %s/data", rt, rt),
	}); err != nil {
		errorf("failed to initialize runtime secrets: %v", err)
		return errors.New("secret initialization failed")
	}
	return nil
}

// integrationInstallService installs the validation host-agent systemd user
// unit in the guest and starts it.

// integrationInstallService installs the validation host-agent systemd user
// unit in the guest and starts it.
func integrationInstallService(ctx context.Context, ex executor.Executor, rt, backend, tmpDir string) error {
	unit := renderIntegrationHostAgentUnit(rt, backend)
	unitPath := filepath.Join(tmpDir, integrationHostAgentUnit)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		errorf("failed to write unit file: %v", err)
		return errors.New("failed to write unit file")
	}
	if err := ex.CopyTo(ctx, unitPath, "/tmp/"+integrationHostAgentUnit); err != nil {
		errorf("failed to copy unit file into guest: %v", err)
		return errors.New("failed to deploy unit file")
	}
	if res, err := ex.Run(ctx, executor.RunSpec{
		Command: fmt.Sprintf(`install -d "$HOME/.config/systemd/user"
install -m 644 /tmp/%[1]s "$HOME/.config/systemd/user/%[1]s"
rm -f /tmp/%[1]s
systemctl --user daemon-reload
systemctl --user enable --now %[1]s`, integrationHostAgentUnit),
	}); err != nil || res.ExitCode != 0 {
		errorf("failed to install host-agent service: %v", err)
		return errors.New("failed to install host-agent service")
	}
	return nil
}

// integrationRunTests runs the tier's commands against the deployed
// runtime and records each result. Returns 0 when every command passed,
// 1 on the first failure (remaining commands are skipped).

// integrationRunTests runs the tier's commands against the deployed
// runtime and records each result. Returns 0 when every command passed,
// 1 on the first failure (remaining commands are skipped).
func integrationRunTests(ctx context.Context, ex executor.Executor, tier manifestTier, rt string, result *ValidateResult, flags validateFlags) int {
	testEnv := map[string]string{
		"BLOUD_DATA_DIR":            rt + "/data",
		"BLOUD_TRAEFIK_DYNAMIC_DIR": rt + "/data/traefik/dynamic",
		"BLOUD_E2E_HOST_AGENT_UNIT": integrationHostAgentUnit,
	}
	exitCode := 0
	for _, cmd := range tier.Commands {
		if flags.explain && !flags.json {
			fmt.Printf("    %s->%s %s: %s (cwd %s)\n", colorCyan, colorReset, cmd.ID, cmd.Run, cmd.Cwd)
		}
		spec := executor.RunSpec{
			Command: cmd.Run,
			Dir:     cmd.Cwd,
			Env:     testEnv,
		}
		start := time.Now()
		var (
			cmdExit int
			runErr  error
		)
		if flags.json {
			res, err := ex.Run(ctx, spec)
			cmdExit = res.ExitCode
			runErr = err
		} else {
			runErr = ex.RunStream(ctx, spec, os.Stdout, os.Stderr)
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				cmdExit = exitErr.ExitCode()
			} else if runErr != nil {
				cmdExit = 1
			}
		}
		dur := time.Since(start)

		status := "pass"
		if runErr != nil {
			status = "fail"
			exitCode = 1
		}
		result.Commands = append(result.Commands, CommandResult{
			ID:         cmd.ID,
			Cwd:        cmd.Cwd,
			Command:    cmd.Run,
			Status:     status,
			DurationMs: dur.Milliseconds(),
			ExitCode:   cmdExit,
		})

		if !flags.json {
			icon := colorGreen + "✓" + colorReset
			if status == "fail" {
				icon = colorRed + "✗" + colorReset
			}
			fmt.Printf("%s %s (%dms)\n", icon, cmd.ID, dur.Milliseconds())
		}

		if runErr != nil {
			break
		}
	}
	return exitCode
}
