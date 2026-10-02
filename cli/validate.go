// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"codeberg.org/d-buckner/bloud/cli/backend"
	"codeberg.org/d-buckner/bloud/cli/executor"
	"context"
	"fmt"
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
		}
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

func runCommands(root string, commands []manifestCommand, result *ValidateResult, flags validateFlags) int {
	exitCode := 0
	for _, cmd := range commands {
		if flags.explain && !flags.json {
			fmt.Printf("    %s→%s %s: %s\n", colorCyan, colorReset, cmd.ID, cmd.Run)
		}
		cr := runManifestCommand(root, cmd, flags)
		result.Commands = append(result.Commands, cr)
		if cr.Status == "fail" {
			exitCode = 1
		}
	}
	return exitCode
}

// runManifestCommand runs one validation command, prints its result line, and
// returns the ledger row. An empty command string is a manifest error rather
// than a spawn failure, so it is recorded without starting a process.
func runManifestCommand(root string, cmd manifestCommand, flags validateFlags) CommandResult {
	cwd := root
	if cmd.Cwd != "." {
		cwd = filepath.Join(root, cmd.Cwd)
	}

	parts := splitShellWords(cmd.Run)
	if len(parts) == 0 {
		reportCommand(flags, cmd.ID, "fail", 0, "(empty command)")
		return CommandResult{ID: cmd.ID, Cwd: cmd.Cwd, Command: cmd.Run, Status: "fail", ExitCode: 1}
	}

	c := exec.Command(parts[0], parts[1:]...)
	c.Dir = cwd
	if !flags.json {
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
	}

	start := time.Now()
	err := c.Run()
	dur := time.Since(start)

	status, code := "pass", 0
	if err != nil {
		status = "fail"
		code = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
	}

	reportCommand(flags, cmd.ID, status, dur.Milliseconds(), "")
	return CommandResult{
		ID:         cmd.ID,
		Cwd:        cmd.Cwd,
		Command:    cmd.Run,
		Status:     status,
		DurationMs: dur.Milliseconds(),
		ExitCode:   code,
	}
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
		fmt.Printf("%s %s %s\n", icon, id, note)
		return
	}
	fmt.Printf("%s %s (%dms)\n", icon, id, ms)
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
