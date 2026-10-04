// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"codeberg.org/d-buckner/bloud/cli/backend"
	"codeberg.org/d-buckner/bloud/cli/executor"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// integrationRuntimeDir is the guest-side home of the validation runtime: a
// self-contained host-agent deployment (binary, app catalog, data) separate
// from the dev runtime. The tier deploys the current code here through the
// real product path (host-agent + orchestrator + catalog) and runs the
// tier's commands against it, so integration validation exercises the same
// install/reconcile flow as production.
//
// The dashboard is deliberately not part of that deployment. The tier ships
// no `web/build`, so host-agent serves its documented missing-build
// fallback page (invariant 11) and the tier spends its time on the
// install/reconcile path it exists to test. The production frontend build
// is gated by the fast tier's `web-build` command, which runs on every
// push; rebuilding the same artifact here again would add a minute of
// vite's chunk listing to the log and prove nothing twice.
const integrationRuntimeDir = "/var/tmp/bloud-validate-runtime"

// integrationHostAgentUnit supervises the validation runtime's host-agent.
const integrationHostAgentUnit = "bloud-validate-host-agent.service"

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
var integrationStopAgentScript = `for unit in bloud-host-agent.service bloud-e2e-host-agent.service bloud-validate-host-agent.service; do
  systemctl --user disable --now "$unit" >/dev/null 2>&1 || true
done
fuser -k 3000/tcp 2>/dev/null || true
sleep 1`

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

// integrationTranscript is the integration tier's console/log split, the
// same one the fast tier uses. The tier has four producers worth quieting:
// the Go build, the deploy copies, the first-boot wait, and `go test
// -test.v`. None of it is signal while the run is going well, and all of it
// is the whole message the moment a phase fails.
//
// So each phase is one line on the terminal -- id, verdict, duration --
// every phase's raw bytes go to .bloud/logs/validate-integration.log, and
// only a failing phase's output is replayed on the console. --verbose
// streams live as well, for the case where waiting for the end is not good
// enough.
type integrationTranscript struct {
	root   string
	flags  validateFlags
	phases []ranCommand
	start  time.Time
}

func newIntegrationTranscript(root string, flags validateFlags) *integrationTranscript {
	return &integrationTranscript{root: root, flags: flags, start: time.Now()}
}

// phase runs one step with its output captured. It returns the finished
// phase record alongside the step's error, so a caller that has to record
// the row itself (the test commands, which land in the ledger) does not
// have to reach back into the transcript.
func (t *integrationTranscript) phase(cmd manifestCommand, run func(out io.Writer) (int, error)) (ranCommand, error) {
	start := time.Now()
	var buf bytes.Buffer
	w := io.Writer(&buf)
	if t.flags.verbose {
		w = io.MultiWriter(os.Stdout, &buf)
	}

	code, err := run(w)
	phase := ranCommand{
		cmd:      cmd,
		output:   buf.String(),
		duration: time.Since(start),
		exitCode: code,
		failed:   err != nil || code != 0,
	}
	t.phases = append(t.phases, phase)

	status := "pass"
	if phase.failed {
		status = "fail"
	}
	reportCommand(t.flags, cmd.ID, status, phase.duration.Milliseconds(), "")
	if phase.failed && !t.flags.json && phase.output != "" {
		fmt.Printf("\n%s=== %s failed (exit %d, %s) ===%s\n%s\n",
			colorRed, cmd.ID, code, phase.duration.Round(time.Millisecond), colorReset,
			indentOutput(phase.output))
	}
	if phase.failed {
		return phase, errors.New(cmd.ID + " failed")
	}
	return phase, nil
}

// bringUpPhase describes a bring-up step for the ledger. These run in the
// CLI's own directory rather than in the guest, so `detail` is a
// description of what the step did rather than a shell line.
func bringUpPhase(id, detail string) manifestCommand {
	return manifestCommand{ID: id, Cwd: ".", Run: detail}
}

// finish writes the transcript and prints the tier summary through the fast
// tier's reporter, so both tiers leave the same shape on the terminal and in
// .bloud/logs.
func (t *integrationTranscript) finish() {
	logPath := writeValidateLog(t.root, "integration", t.phases)
	if !t.flags.json {
		printValidateSummary("integration", t.phases, logPath, time.Since(t.start))
	}
}

// captureRun runs a spec through the executor and folds whatever it produced
// into the phase's transcript. Verbose streams live; otherwise the bytes are
// collected and surface only if the phase failed.
func captureRun(ctx context.Context, ex executor.Executor, spec executor.RunSpec, out io.Writer, verbose bool) (int, error) {
	if verbose {
		err := ex.RunStream(ctx, spec, out, out)
		return exitCodeOf(err), err
	}
	res, err := ex.Run(ctx, spec)
	if res.Stdout != "" {
		_, _ = io.WriteString(out, res.Stdout)
	}
	if res.Stderr != "" {
		_, _ = io.WriteString(out, res.Stderr)
	}
	if err != nil {
		return exitCodeOf(err), err
	}
	if res.ExitCode != 0 {
		return res.ExitCode, fmt.Errorf("command exited with code %d", res.ExitCode)
	}
	return 0, nil
}

// exitCodeOf is the exit code carried by an exec error, or 1 for any other
// failure. A phase that never spawned still failed, and 1 says so.
func exitCodeOf(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}

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

// note writes a diagnostic line into the phase's transcript. The transcript
// is a best-effort record of what the tier saw, so a write failure has
// nowhere more useful to go than nowhere.
func note(out io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(out, format+"\n", args...)
}

// integrationPrepareGuest verifies the guest has everything the tier needs
// and stops any host-agent holding port 3000.
func integrationPrepareGuest(ctx context.Context, ex executor.Executor, out io.Writer) error {
	if res, err := ex.Run(ctx, executor.RunSpec{Command: integrationPreflightScript}); err != nil || res.ExitCode != 0 {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" && err != nil {
			detail = err.Error()
		}
		note(out, "integration prerequisites missing (recreate the VM): %s", detail)
		return errors.New("integration prerequisites missing")
	}
	if res, err := ex.Run(ctx, executor.RunSpec{Command: integrationStopAgentScript}); err != nil || res.ExitCode != 0 {
		note(out, "failed to stop running host-agent: %v", err)
		return errors.New("failed to stop running host-agent")
	}
	return nil
}

// The local build lands in a temp dir under these names, and the deploy
// copies them into the guest under the names host-agent and the test runner
// expect. One pair of constants so the two sides cannot drift.
const (
	integrationHostAgentBinaryName = "host-agent"
	integrationTestBinaryName      = "bloud-integration.test"
)

// integrationBuildArtifacts builds the host-agent binary and the
// integration test binary locally into tmpDir, writing the toolchain's own
// output to `out` rather than to the console.
//
// The frontend is not built here. See integrationArtifacts: the fast tier
// already gates the production bundle, and the validation runtime does not
// serve a dashboard.
func integrationBuildArtifacts(hostAgentSrc, tmpDir string, out io.Writer) error {
	binaryPath := filepath.Join(tmpDir, integrationHostAgentBinaryName)
	buildCmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/host-agent")
	buildCmd.Dir = hostAgentSrc
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	buildCmd.Stdout = out
	buildCmd.Stderr = out
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("host-agent build failed: %w", err)
	}

	testBinary := filepath.Join(tmpDir, integrationTestBinaryName)
	testBuild := exec.Command("go", "test", "-tags", "integration", "-c", "-o", testBinary, "./internal/e2e")
	testBuild.Dir = hostAgentSrc
	// The binary runs *in the guest*, so it needs the same target as the
	// host-agent build above. Without it Go builds for the host (darwin/arm64
	// on a macOS developer machine), the deployment succeeds, and the test run
	// dies with "cannot execute binary file: Exec format error".
	testBuild.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	testBuild.Stdout = out
	testBuild.Stderr = out
	if err := testBuild.Run(); err != nil {
		return fmt.Errorf("integration test build failed: %w", err)
	}
	return nil
}

// integrationArtifact is one thing the tier copies into the guest runtime.
type integrationArtifact struct {
	from string
	to   string
}

// integrationArtifacts lists what the tier deploys. It is a function rather
// than an inline literal so a test can assert what is in the deployment:
// the frontend bundle is deliberately absent, and the day someone adds
// `web/build` back the test asks why. The fast tier's `web-build` command
// already gates the production bundle, and the validation runtime serves
// host-agent's missing-build fallback page instead (invariant 11).
func integrationArtifacts(root, tmpDir, rt string) []integrationArtifact {
	return []integrationArtifact{
		{filepath.Join(tmpDir, integrationHostAgentBinaryName), rt + "/host-agent/host-agent"},
		{filepath.Join(root, "apps"), rt + "/apps"},
		{filepath.Join(tmpDir, integrationTestBinaryName), rt + "/bin/bloud-integration.test"},
	}
}

// integrationDeploy copies the built artifacts into the guest validation
// runtime dir and initializes runtime secrets (idempotent product command;
// the tests read the real values from secrets.json).
func integrationDeploy(ctx context.Context, ex executor.Executor, root, tmpDir, rt string) error {
	if _, err := ex.Run(ctx, executor.RunSpec{
		Command: fmt.Sprintf("rm -rf %s/host-agent %s/apps && mkdir -p %s/host-agent %s/apps %s/data %s/bin", rt, rt, rt, rt, rt, rt),
	}); err != nil {
		return fmt.Errorf("failed to prepare runtime dir: %w", err)
	}
	for _, d := range integrationArtifacts(root, tmpDir, rt) {
		if err := ex.CopyTo(ctx, d.from, d.to); err != nil {
			return fmt.Errorf("failed to copy %s into guest: %w", d.from, err)
		}
	}
	if _, err := ex.Run(ctx, executor.RunSpec{
		Command: fmt.Sprintf("chmod 755 %s/host-agent/host-agent %s/bin/bloud-integration.test", rt, rt),
	}); err != nil {
		return fmt.Errorf("failed to chmod deployed binaries: %w", err)
	}
	if _, err := ex.Run(ctx, executor.RunSpec{
		Command: fmt.Sprintf("%s/host-agent/host-agent init-secrets %s/data", rt, rt),
	}); err != nil {
		return fmt.Errorf("failed to initialize runtime secrets: %w", err)
	}
	return nil
}

// integrationInstallService installs the validation host-agent systemd user
// unit in the guest and starts it.
func integrationInstallService(ctx context.Context, ex executor.Executor, rt, backend, tmpDir string) error {
	unit := renderIntegrationHostAgentUnit(rt, backend)
	unitPath := filepath.Join(tmpDir, integrationHostAgentUnit)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("failed to write unit file: %w", err)
	}
	if err := ex.CopyTo(ctx, unitPath, "/tmp/"+integrationHostAgentUnit); err != nil {
		return fmt.Errorf("failed to copy unit file into guest: %w", err)
	}
	if res, err := ex.Run(ctx, executor.RunSpec{
		Command: fmt.Sprintf(`install -d "$HOME/.config/systemd/user"
install -m 644 /tmp/%[1]s "$HOME/.config/systemd/user/%[1]s"
rm -f /tmp/%[1]s
systemctl --user daemon-reload
systemctl --user enable --now %[1]s`, integrationHostAgentUnit),
	}); err != nil || res.ExitCode != 0 {
		return fmt.Errorf("failed to install host-agent service: %v", err)
	}
	return nil
}

// integrationRunTests runs the tier's commands against the deployed
// runtime, each as one transcript phase. It returns 0 when every command
// passed, 1 on the first failure (remaining commands are skipped).
//
// A --test-run filter narrows the suite to one app group, so the integration
// tier can be fanned out across a CI matrix (one runner per group) instead of
// running every app's install/reconcile journey serially in a single 15-minute
// binary.
func integrationRunTests(ctx context.Context, ex executor.Executor, tier manifestTier, rt string, result *ValidateResult, t *integrationTranscript) int {
	testEnv := map[string]string{
		"BLOUD_DATA_DIR":            rt + "/data",
		"BLOUD_APPS_DIR":            rt + "/apps",
		"BLOUD_TRAEFIK_DYNAMIC_DIR": rt + "/data/traefik/dynamic",
		"BLOUD_E2E_HOST_AGENT_UNIT": integrationHostAgentUnit,
	}
	for _, cmd := range tier.Commands {
		run := withTestRunFilter(cmd.Run, t.flags.testRun)
		if t.flags.explain && !t.flags.json {
			fmt.Printf("    %s->%s %s: %s (cwd %s)\n", colorCyan, colorReset, cmd.ID, run, cmd.Cwd)
		}
		phase, err := t.phase(cmd, func(out io.Writer) (int, error) {
			return captureRun(ctx, ex, executor.RunSpec{Command: run, Dir: cmd.Cwd, Env: testEnv}, out, t.flags.verbose)
		})
		result.Commands = append(result.Commands, phase.result())
		if err != nil {
			return 1
		}
	}
	return 0
}

// withTestRunFilter appends a -test.run regex to a test command, single-quoted
// so the shell does not treat the `|` alternation as a pipe. An empty filter
// leaves the command untouched (run the whole suite).
func withTestRunFilter(run, filter string) string {
	if filter == "" {
		return run
	}
	return run + " -test.run '" + strings.ReplaceAll(filter, "'", "'\\''") + "'"
}

func runIntegrationRuntime(root string, tier manifestTier, result *ValidateResult, flags validateFlags) (int, string) {
	ctx := context.Background()
	t := newIntegrationTranscript(root, flags)
	if !flags.json {
		fmt.Printf("%s==>%s Integration tier\n", colorGreen, colorReset)
	}

	exitCode, reason := bringUpAndTest(ctx, root, tier, result, t)
	t.finish()
	if reason != "" {
		return 1, reason
	}
	if exitCode != 0 {
		return 1, "integration tests failed"
	}
	if !flags.json {
		fmt.Printf("\n%s==>%s Validation runtime remains at %s (guest). Re-run %s%s%s to restore the dev runtime state.\n",
			colorGreen, colorReset, integrationRuntimeDir, colorCyan, "./bloud dev", colorReset)
	}
	return 0, ""
}

// bringUpAndTest runs the tier's phases in order and stops at the first one
// that fails, returning its exit code with an empty reason, or 1 with the
// reason the run could not complete. Every phase's output is already in the
// transcript by the time it returns, so a bail is never a bail without the
// evidence that caused it.
func bringUpAndTest(ctx context.Context, root string, tier manifestTier, result *ValidateResult, t *integrationTranscript) (int, string) {
	rt := integrationRuntimeDir
	hostAgentSrc := filepath.Join(root, "services", "host-agent")

	bk, name, err := integrationProvisionVM(ctx, t)
	if err != nil {
		return 1, err.Error()
	}
	ex := bk.Host().Executor()

	// The validation runtime takes port 3000 over for the duration of the
	// tier; the dev runtime state (data, containers) is untouched and
	// ./bloud dev converges it back afterwards. The unit comes down whether
	// the tests ran or the bring-up bailed halfway, or the tier would leave
	// an agent holding the port for the next run to fight.
	defer integrationStopService(ctx, ex)

	if _, err := t.phase(bringUpPhase("preflight", "verify podman, ldapsearch, and user systemd in the guest"),
		onlyErr(func(out io.Writer) error { return integrationPrepareGuest(ctx, ex, out) })); err != nil {
		return 1, err.Error()
	}

	tmpDir, err := os.MkdirTemp("", "bloud-validate-build-*")
	if err != nil {
		return 1, "could not create build dir"
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if _, err := t.phase(bringUpPhase("build-host-agent", "go build ./cmd/host-agent and the integration test binary (linux/"+runtime.GOARCH+")"),
		onlyErr(func(out io.Writer) error { return integrationBuildArtifacts(hostAgentSrc, tmpDir, out) })); err != nil {
		return 1, err.Error()
	}

	if _, err := t.phase(bringUpPhase("deploy-runtime", "copy "+rt+": host-agent, the app catalog, the test binary, and init-secrets"),
		onlyErr(func(io.Writer) error { return integrationDeploy(ctx, ex, root, tmpDir, rt) })); err != nil {
		return 1, err.Error()
	}

	if _, err := t.phase(bringUpPhase("start-host-agent", "install and start "+integrationHostAgentUnit),
		onlyErr(func(io.Writer) error { return integrationInstallService(ctx, ex, rt, name, tmpDir) })); err != nil {
		return 1, err.Error()
	}

	// First boot converges the system apps before the listener opens, so
	// this is also the bootstrap gate.
	if _, err := t.phase(bringUpPhase("wait-for-convergence", "poll GET :3000/api/health until the first pass ends"),
		onlyErr(func(out io.Writer) error { return integrationWaitForAgent(ctx, ex, out) })); err != nil {
		return 1, err.Error()
	}

	return integrationRunTests(ctx, ex, tier, rt, result, t), ""
}

// onlyErr adapts a step that only reports an error to a phase's
// (exit code, error) signature: nil is exit 0, anything else is exit 1.
func onlyErr(step func(io.Writer) error) func(io.Writer) (int, error) {
	return func(out io.Writer) (int, error) {
		if err := step(out); err != nil {
			return 1, err
		}
		return 0, nil
	}
}

// integrationProvisionVM resolves the dev backend and brings the VM up, which
// is a no-op when it is already running.
func integrationProvisionVM(ctx context.Context, t *integrationTranscript) (backend.Backend, string, error) {
	bk, name, err := devBackend()
	if err != nil {
		return nil, "", fmt.Errorf("could not set up backend: %w", err)
	}
	_, provisionErr := t.phase(bringUpPhase("provision-host", "ensure the "+vmLabel(name)+" runtime exists"), func(out io.Writer) (int, error) {
		if err := bk.Create(ctx); err != nil {
			note(out, "failed to provision VM: %v", err)
			return 1, err
		}
		if !bk.Host().Ready() {
			note(out, "VM is not reachable after provisioning")
			return 1, errors.New("VM is not reachable after provisioning")
		}
		return 0, nil
	})
	if provisionErr != nil {
		return nil, name, errors.New("failed to provision the runtime host")
	}
	return bk, name, nil
}

// integrationWaitForAgent blocks until the validation host-agent reports
// healthy, writing whatever the wait script said on failure into the
// transcript so the timeout is not silent.
func integrationWaitForAgent(ctx context.Context, ex executor.Executor, out io.Writer) error {
	res, err := ex.Run(ctx, executor.RunSpec{Command: integrationWaitAgentScript})
	if err == nil && res.ExitCode == 0 {
		return nil
	}
	if detail := strings.TrimSpace(res.Stdout); detail != "" {
		_, _ = io.WriteString(out, detail+"\n")
	}
	if detail := strings.TrimSpace(res.Stderr); detail != "" {
		_, _ = io.WriteString(out, detail+"\n")
	}
	return fmt.Errorf("validation host-agent did not become healthy")
}

// integrationStopService stops the validation unit. The runtime dir and its
// containers are left in place for inspection; ./bloud dev re-converges
// the dev state.
func integrationStopService(ctx context.Context, ex executor.Executor) {
	if _, err := ex.Run(ctx, executor.RunSpec{
		Command: "systemctl --user disable --now " + integrationHostAgentUnit + " >/dev/null 2>&1 || true",
	}); err != nil {
		errorf("failed to stop validation host-agent: %v", err)
	}
}
