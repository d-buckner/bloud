// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"codeberg.org/d-buckner/bloud/cli/backend"
	"codeberg.org/d-buckner/bloud/cli/executor"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type validateFlags struct {
	tier    string
	app     string
	json    bool
	explain bool
	dryRun  bool
	since   string
	verbose bool
}

func cmdValidate(args []string) int {
	flags := parseValidateFlags(args)

	root, err := getProjectRoot()
	if err != nil {
		errorf("cannot find project root: %v", err)
		return 1
	}

	manifest, err := loadManifest(root)
	if err != nil {
		errorf("cannot load validation.yaml: %v", err)
		return 1
	}

	switch flags.tier {
	case "fast":
		return runFastTier(root, manifest, flags)
	case "changed":
		return runChangedTier(root, manifest, flags)
	case "integration":
		return runIntegrationTier(root, manifest, flags)
	default:
		errorf("unknown tier: %s (available: fast, changed, integration)", flags.tier)
		return 1
	}
}

func parseValidateFlags(args []string) validateFlags {
	f := validateFlags{tier: "changed"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tier":
			if i+1 < len(args) {
				i++
				f.tier = args[i]
			}
		case "--app":
			if i+1 < len(args) {
				i++
				f.app = args[i]
			}
		case "--json":
			f.json = true
		case "--explain":
			f.explain = true
		case "--dry-run":
			f.dryRun = true
		case "--since":
			if i+1 < len(args) {
				i++
				f.since = args[i]
			}
		case "--verbose", "-v":
			f.verbose = true
		}
	}
	if os.Getenv("BLOUD_VALIDATE_VERBOSE") == "1" {
		f.verbose = true
	}
	return f
}

func runFastTier(root string, manifest *validationManifest, flags validateFlags) int {
	tier, ok := manifest.Tiers["fast"]
	if !ok {
		errorf("no 'fast' tier defined in validation.yaml")
		return 1
	}

	result := &ValidateResult{
		StartedAt:        time.Now().UTC().Format(time.RFC3339),
		Tier:             "fast",
		Confidence:       "high",
		ConfidenceReason: "all fast-tier commands executed",
	}

	if flags.dryRun {
		printDryRun("fast", tier.Commands, nil, nil, flags)
		return 0
	}

	exitCode := runCommands(root, tier.Commands, result, flags)

	result.ExitCode = exitCode
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	writeLedger(root, result, flags)
	return exitCode
}

// --- Changed tier ---

func runChangedTier(root string, manifest *validationManifest, flags validateFlags) int {
	result := &ValidateResult{
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Tier:      "changed",
	}

	changedFiles, err := getChangedFiles(root, flags.since)
	if err != nil {
		errorf("cannot determine changed files: %v", err)
		return 1
	}
	result.ChangedFiles = changedFiles

	if len(changedFiles) == 0 {
		if !flags.json {
			fmt.Println("No changed files detected. Nothing to validate.")
		}
		result.Confidence = "high"
		result.ConfidenceReason = "no changes detected"
		return finishResult(root, result, flags, 0)
	}

	// Infer commands and risk areas from changed files
	triggeredIDs, riskAreas, unmapped := inferTriggers(changedFiles, manifest)
	result.UnmappedFiles = unmapped
	result.RiskAreas = riskAreas
	result.Confidence, result.ConfidenceReason = changedConfidence(unmapped)
	result.Apps = detectAffectedApps(changedFiles, manifest)

	// Only the fast tier's commands are inferable from a path; the higher
	// tiers are chosen, not derived.
	commands := triggeredCommands(manifest.Tiers["fast"], triggeredIDs)

	if flags.dryRun {
		printDryRun("changed", commands, result.RiskAreas, changedFiles, flags)
		return 0
	}
	printChangedPlan(flags, result, commands, changedFiles)

	if len(commands) == 0 {
		if !flags.json {
			fmt.Println("No testable commands triggered by changed files.")
			if len(result.RiskAreas) > 0 {
				fmt.Printf("Risk areas detected: %s. Consider running a higher tier.\n", strings.Join(result.RiskAreas, ", "))
			}
		}
		return finishResult(root, result, flags, 0)
	}

	return finishResult(root, result, flags, runCommands(root, commands, result, flags))
}

// finishResult stamps the ledger with the end time and the exit code, then
// returns that code, so every tier ends the run the same way.
func finishResult(root string, result *ValidateResult, flags validateFlags, exitCode int) int {
	result.ExitCode = exitCode
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	writeLedger(root, result, flags)
	return exitCode
}

// changedConfidence is what the changed tier's coverage is worth: a file no
// glob in validation.yaml claims is a file no command looked at.
func changedConfidence(unmapped []string) (string, string) {
	if len(unmapped) > 0 {
		return "medium", fmt.Sprintf("%d file(s) not mapped to any validation command", len(unmapped))
	}
	return "high", "all changed files mapped to validation commands"
}

// triggeredCommands keeps the tier's commands the changed files selected.
func triggeredCommands(tier manifestTier, triggeredIDs map[string]bool) []manifestCommand {
	var commands []manifestCommand
	for _, cmd := range tier.Commands {
		if triggeredIDs[cmd.ID] {
			commands = append(commands, cmd)
		}
	}
	return commands
}

// printChangedPlan states what the inference picked, unless the caller asked
// for the JSON ledger only.
func printChangedPlan(flags validateFlags, result *ValidateResult, commands []manifestCommand, changedFiles []string) {
	if flags.json || len(commands) == 0 {
		return
	}
	fmt.Printf("%s==>%s Inferred %d command(s) from %d changed file(s)\n", colorGreen, colorReset, len(commands), len(changedFiles))
	if len(result.RiskAreas) > 0 {
		fmt.Printf("    Risk areas: %s\n", strings.Join(result.RiskAreas, ", "))
	}
	if len(result.Apps) > 0 {
		fmt.Printf("    Affected apps: %s\n", strings.Join(result.Apps, ", "))
	}
	fmt.Println()
}

func runIntegrationTier(root string, manifest *validationManifest, flags validateFlags) int {
	tier, ok := manifest.Tiers["integration"]
	if !ok {
		errorf("no 'integration' tier defined in validation.yaml")
		return 1
	}

	result := &ValidateResult{
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Tier:      "integration",
	}

	if flags.dryRun {
		printDryRun("integration", tier.Commands, nil, nil, flags)
		return 0
	}

	exitCode, reason := runIntegrationRuntime(root, tier, result, flags)
	if reason != "" {
		result.Confidence = "low"
		result.ConfidenceReason = reason
		return finishResult(root, result, flags, 1)
	}

	result.Confidence = "high"
	result.ConfidenceReason = "integration tests passed against the real dependency-graph path"
	return finishResult(root, result, flags, exitCode)
}

// runIntegrationRuntime brings the validation runtime up, runs the tier's
// commands against it, and takes the unit back down. It returns the exit code
// with an empty reason, or 1 with the reason the run could not complete, so the
// caller only has to stamp the ledger once.
func runIntegrationRuntime(root string, tier manifestTier, result *ValidateResult, flags validateFlags) (int, string) {
	ctx := context.Background()
	step := func(msg string) {
		if !flags.json {
			fmt.Printf("%s==>%s %s\n", colorGreen, colorReset, msg)
		}
	}

	// Step 1: Provision the VM (no-op if it is already running).
	bk, name, err := integrationProvisionVM(ctx, step)
	if err != nil {
		errorf("%v", err)
		return 1, err.Error()
	}
	ex := bk.Host().Executor()
	rt := integrationRuntimeDir

	// Steps 2-3: guest preflight + take over port 3000. The validation
	// runtime takes the port over for the duration of the tier; the dev
	// runtime state (data, containers) is untouched and ./bloud dev
	// converges it back afterwards.
	if reason := integrationPrepareGuest(ctx, ex, step); reason != "" {
		return 1, reason
	}

	// Step 4: Build artifacts locally.
	tmpDir, err := os.MkdirTemp("", "bloud-validate-build-*")
	if err != nil {
		errorf("failed to create build dir: %v", err)
		return 1, "could not create build dir"
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	hostAgentSrc := filepath.Join(root, "services", "host-agent")
	binaryPath, testBinary, err := integrationBuildArtifacts(root, hostAgentSrc, tmpDir, step)
	if err != nil {
		return 1, err.Error()
	}

	// Step 5: Deploy to the validation runtime.
	step("Deploying to " + rt)
	if err := integrationDeploy(ctx, ex, root, hostAgentSrc, rt, binaryPath, testBinary); err != nil {
		return 1, err.Error()
	}

	// Step 6: Install and start the host-agent systemd service.
	step("Installing and starting " + integrationHostAgentUnit)
	if err := integrationInstallService(ctx, ex, rt, name, tmpDir); err != nil {
		return 1, err.Error()
	}

	// Step 7: Wait for the API (first boot converges the system apps).
	step("Waiting for host-agent (first boot pulls images and converges system apps; may take a while)")
	if err := integrationWaitForAgent(ctx, ex); err != nil {
		return 1, err.Error()
	}

	// Step 8: Run the tier's commands against the deployed runtime.
	step("Running integration tests")
	exitCode := integrationRunTests(ctx, ex, tier, rt, result, flags)

	integrationStopService(ctx, ex)

	if exitCode != 0 {
		return 1, "integration tests failed"
	}
	if !flags.json {
		fmt.Printf("\n%s==>%s Validation runtime remains at %s (guest). Re-run %s%s%s to restore the dev runtime state.\n",
			colorGreen, colorReset, rt, colorCyan, "./bloud dev", colorReset)
	}
	return 0, ""
}

// integrationStopService stops the validation unit. The runtime dir and its
// containers are left in place for inspection; ./bloud dev re-converges the
// dev state.
func integrationStopService(ctx context.Context, ex executor.Executor) {
	if _, err := ex.Run(ctx, executor.RunSpec{
		Command: "systemctl --user disable --now " + integrationHostAgentUnit + " >/dev/null 2>&1 || true",
	}); err != nil {
		errorf("failed to stop validation host-agent: %v", err)
	}
}

// integrationProvisionVM resolves the dev backend and brings the VM up, which
// is a no-op when it is already running.
func integrationProvisionVM(ctx context.Context, step func(string)) (backend.Backend, string, error) {
	bk, name, err := devBackend()
	if err != nil {
		return nil, "", fmt.Errorf("could not set up backend: %w", err)
	}
	step("Provisioning " + vmLabel(name))
	if err := bk.Create(ctx); err != nil {
		return nil, name, fmt.Errorf("failed to provision VM: %w", err)
	}
	if !bk.Host().Ready() {
		return nil, name, fmt.Errorf("VM is not reachable after provisioning")
	}
	return bk, name, nil
}

// integrationWaitForAgent blocks until the validation host-agent reports
// healthy, echoing whatever it said on stderr when it did not.
func integrationWaitForAgent(ctx context.Context, ex executor.Executor) error {
	res, err := ex.Run(ctx, executor.RunSpec{Command: integrationWaitAgentScript})
	if err == nil && res.ExitCode == 0 {
		return nil
	}
	if detail := strings.TrimSpace(res.Stderr); detail != "" {
		fmt.Fprintln(os.Stderr, detail)
	}
	return fmt.Errorf("validation host-agent did not become healthy")
}

// ranCommand is one executed fast-tier command and everything the console
// needs to report it: the captured output, how long it took, and whether it
// failed. The output is captured rather than streamed because both `go test`
// and the generators write freely to stdout, including deliberate error-path
// text from tests that pass. Streaming it buries the verdict; the captured log
// keeps the evidence, and only a failing command's output reaches the console.
type ranCommand struct {
	cmd      manifestCommand
	output   string
	duration time.Duration
	exitCode int
	failed   bool
}

func runCommands(root string, commands []manifestCommand, result *ValidateResult, flags validateFlags) int {
	exitCode := 0
	var ran []ranCommand

	if !flags.json {
		fmt.Printf("  %s\n", strings.Repeat("-", 60))
	}

	for _, cmd := range commands {
		if flags.explain && !flags.json {
			fmt.Printf("    %s→%s %s: %s\n", colorCyan, colorReset, cmd.ID, cmd.Run)
		}
		r := runManifestCommand(root, cmd, flags)
		ran = append(ran, r)
		result.Commands = append(result.Commands, r.result())
		if r.failed {
			exitCode = 1
		}
	}

	logPath := writeValidateLog(root, result.Tier, ran)

	if !flags.json {
		printValidateSummary(result.Tier, ran, logPath)
	}
	return exitCode
}

// printValidateSummary is the signal the tier owes the reader: did it pass or
// fail, how long it took, and, when it failed, the output of only the failing
// commands. The per-command verdicts were already printed as they ran.
func printValidateSummary(tier string, ran []ranCommand, logPath string) {
	var failed []ranCommand
	var total time.Duration
	for _, r := range ran {
		total += r.duration
		if r.failed {
			failed = append(failed, r)
		}
	}

	for _, r := range failed {
		fmt.Printf("\n%s=== %s failed (exit %d, %s) ===%s\n%s\n",
			colorRed, r.cmd.ID, r.exitCode, r.duration.Round(time.Millisecond), colorReset,
			indentOutput(r.output))
	}

	fmt.Printf("  %s\n", strings.Repeat("-", 60))
	if len(failed) == 0 {
		fmt.Printf("validate: %s tier passed, %d/%d checks in %s\n",
			tier, len(ran), len(ran), total.Round(time.Millisecond))
	} else {
		names := make([]string, 0, len(failed))
		for _, r := range failed {
			names = append(names, r.cmd.ID)
		}
		fmt.Printf("validate: %s tier failed, %d/%d checks in %s; failed: %s\n",
			tier, len(ran)-len(failed), len(ran), total.Round(time.Millisecond), strings.Join(names, ", "))
	}
	if logPath != "" {
		fmt.Printf("full output: %s\n", logPath)
	}
}

// indentOutput prefixes every line so the dumped failure output is visually
// scoped to the check that produced it.
func indentOutput(out string) string {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return "    (no output)"
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

// writeValidateLog mirrors every command's full output to .bloud/logs so the
// console can stay quiet without losing evidence. Best effort: a read-only
// checkout must not fail the tier.
func writeValidateLog(root, tier string, ran []ranCommand) string {
	dir := filepath.Join(root, ".bloud", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	path := filepath.Join(dir, "validate-"+tier+".log")
	var b strings.Builder
	fmt.Fprintf(&b, "validate %s tier, %s\n", tier, time.Now().UTC().Format(time.RFC3339))
	for _, r := range ran {
		status := "PASS"
		if r.failed {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "\n===== [%s] %s (cd %s && %s) %s =====\n%s\n",
			status, r.cmd.ID, r.cmd.Cwd, r.cmd.Run, r.duration.Round(time.Millisecond), r.output)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return ""
	}
	if rel, err := filepath.Rel(root, path); err == nil {
		return rel
	}
	return path
}

// runManifestCommand runs one validation command, prints its result line, and
// returns everything the log and the summary need. The output is captured
// rather than streamed: `go test` and the generators write freely to stdout,
// including deliberate error-path text from tests that pass, and streaming it
// buries the verdict. --verbose streams it live and still logs it; a failing
// command's output is dumped by printValidateSummary.
//
// An empty command string is a manifest error rather than a spawn failure, so
// it is recorded without starting a process.
func runManifestCommand(root string, cmd manifestCommand, flags validateFlags) ranCommand {
	cwd := root
	if cmd.Cwd != "." {
		cwd = filepath.Join(root, cmd.Cwd)
	}

	parts := splitShellWords(cmd.Run)
	if len(parts) == 0 {
		reportCommand(flags, cmd.ID, "fail", 0, "(empty command)")
		return ranCommand{cmd: cmd, output: "empty command\n", exitCode: 1, failed: true}
	}

	var buf bytes.Buffer
	c := exec.Command(parts[0], parts[1:]...)
	c.Dir = cwd
	c.Stdin = os.Stdin
	switch {
	case flags.json:
		// JSON mode owns stdout: the command's output is not part of the
		// payload, so send it to /dev/null rather than the console.
		c.Stdout = nil
		c.Stderr = nil
	case flags.verbose:
		// Stream it live and keep a copy for the log.
		w := io.MultiWriter(os.Stdout, &buf)
		c.Stdout = w
		c.Stderr = w
	default:
		// Quiet by default: capture, and surface it only if the command
		// fails (or in the log file).
		c.Stdout = &buf
		c.Stderr = &buf
	}

	start := time.Now()
	err := c.Run()
	dur := time.Since(start)

	exitCode := 0
	failed := err != nil
	if failed {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	status := "pass"
	if failed {
		status = "fail"
	}
	reportCommand(flags, cmd.ID, status, dur.Milliseconds(), "")
	return ranCommand{cmd: cmd, output: buf.String(), duration: dur, exitCode: exitCode, failed: failed}
}

// reportCommand prints the one-line console result for a validation command.
// A command that never spawned has no duration worth reporting, so `note`
// carries the reason instead.
func reportCommand(flags validateFlags, id, status string, ms int64, note string) {
	if flags.json {
		return
	}
	icon := colorGreen + "✓" + colorReset
	if status == "fail" {
		icon = colorRed + "✗" + colorReset
	}
	if note != "" {
		fmt.Printf("  %s %s %s\n", icon, id, note)
		return
	}
	fmt.Printf("  %s %-24s %6.1fs\n", icon, id, float64(ms)/1000)
}

// result is the ledger row for a finished command.
func (r ranCommand) result() CommandResult {
	status := "pass"
	if r.failed {
		status = "fail"
	}
	return CommandResult{
		ID:         r.cmd.ID,
		Cwd:        r.cmd.Cwd,
		Command:    r.cmd.Run,
		Status:     status,
		DurationMs: r.duration.Milliseconds(),
		ExitCode:   r.exitCode,
	}
}

func printDryRun(tier string, commands []manifestCommand, riskAreas []string, changedFiles []string, flags validateFlags) {
	fmt.Printf("Tier: %s (dry-run)\n", tier)
	if len(changedFiles) > 0 {
		fmt.Printf("Changed files: %d\n", len(changedFiles))
		for _, f := range changedFiles {
			fmt.Printf("  %s\n", f)
		}
		fmt.Println()
	}
	if len(riskAreas) > 0 {
		fmt.Printf("Risk areas: %s\n", strings.Join(riskAreas, ", "))
	}
	fmt.Printf("Commands (%d):\n", len(commands))
	for _, cmd := range commands {
		fmt.Printf("  [%s] cd %s && %s\n", cmd.ID, cmd.Cwd, cmd.Run)
	}
}
