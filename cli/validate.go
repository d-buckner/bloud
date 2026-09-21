// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
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
		result.ExitCode = 0
		result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		writeLedger(root, result, flags)
		return 0
	}

	// Infer commands and risk areas from changed files
	triggeredIDs, riskAreas, unmapped := inferTriggers(changedFiles, manifest)

	result.UnmappedFiles = unmapped
	result.RiskAreas = riskAreas

	// Determine confidence
	result.Confidence = "high"
	result.ConfidenceReason = "all changed files mapped to validation commands"
	if len(unmapped) > 0 {
		result.Confidence = "medium"
		result.ConfidenceReason = fmt.Sprintf("%d file(s) not mapped to any validation command", len(unmapped))
	}

	// Collect commands to run
	fastTier := manifest.Tiers["fast"]
	var commands []manifestCommand
	for _, cmd := range fastTier.Commands {
		if triggeredIDs[cmd.ID] {
			commands = append(commands, cmd)
		}
	}

	// Detect affected apps
	result.Apps = detectAffectedApps(changedFiles, manifest)

	if flags.dryRun {
		printDryRun("changed", commands, result.RiskAreas, changedFiles, flags)
		return 0
	}

	if !flags.json && len(commands) > 0 {
		fmt.Printf("%s==>%s Inferred %d command(s) from %d changed file(s)\n", colorGreen, colorReset, len(commands), len(changedFiles))
		if len(result.RiskAreas) > 0 {
			fmt.Printf("    Risk areas: %s\n", strings.Join(result.RiskAreas, ", "))
		}
		if len(result.Apps) > 0 {
			fmt.Printf("    Affected apps: %s\n", strings.Join(result.Apps, ", "))
		}
		fmt.Println()
	}

	if len(commands) == 0 {
		if !flags.json {
			fmt.Println("No testable commands triggered by changed files.")
			if len(result.RiskAreas) > 0 {
				fmt.Printf("Risk areas detected: %s. Consider running a higher tier.\n", strings.Join(result.RiskAreas, ", "))
			}
		}
		result.ExitCode = 0
		result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		writeLedger(root, result, flags)
		return 0
	}

	exitCode := runCommands(root, commands, result, flags)

	result.ExitCode = exitCode
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	writeLedger(root, result, flags)
	return exitCode
}

// inferTriggers maps changed files through the manifest's inference globs:
// which validation command IDs are triggered, which risk areas are hit,
// and which files matched no pattern at all.

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

	ctx := context.Background()
	fail := func(reason string) int {
		result.ExitCode = 1
		result.Confidence = "low"
		result.ConfidenceReason = reason
		result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		writeLedger(root, result, flags)
		return 1
	}
	step := func(msg string) {
		if !flags.json {
			fmt.Printf("%s==>%s %s\n", colorGreen, colorReset, msg)
		}
	}

	// Step 1: Provision the VM (no-op if it is already running).
	bk, name, err := devBackend()
	if err != nil {
		errorf("could not set up backend: %v", err)
		return fail("backend setup failed")
	}
	step("Provisioning " + vmLabel(name))
	if err := bk.Create(ctx); err != nil {
		errorf("failed to provision VM: %v", err)
		return fail("VM provisioning failed")
	}
	host := bk.Host()
	if !host.Ready() {
		errorf("VM is not reachable after provisioning")
		return fail("VM not reachable")
	}
	ex := host.Executor()
	rt := integrationRuntimeDir

	// Steps 2-3: guest preflight + take over port 3000. The validation
	// runtime takes the port over for the duration of the tier; the dev
	// runtime state (data, containers) is untouched and ./bloud dev
	// converges it back afterwards.
	if reason := integrationPrepareGuest(ctx, ex, step); reason != "" {
		return fail(reason)
	}

	// Step 4: Build artifacts locally.
	tmpDir, err := os.MkdirTemp("", "bloud-validate-build-*")
	if err != nil {
		errorf("failed to create build dir: %v", err)
		return fail("could not create build dir")
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	hostAgentSrc := filepath.Join(root, "services", "host-agent")
	binaryPath, testBinary, err := integrationBuildArtifacts(root, hostAgentSrc, tmpDir, step)
	if err != nil {
		return fail(err.Error())
	}

	// Step 5: Deploy to the validation runtime.
	step("Deploying to " + rt)
	if err := integrationDeploy(ctx, ex, root, hostAgentSrc, rt, binaryPath, testBinary); err != nil {
		return fail(err.Error())
	}

	// Step 6: Install and start the host-agent systemd service.
	step("Installing and starting " + integrationHostAgentUnit)
	if err := integrationInstallService(ctx, ex, rt, name, tmpDir); err != nil {
		return fail(err.Error())
	}

	// Step 7: Wait for the API (first boot converges the system apps).
	step("Waiting for host-agent (first boot pulls images and converges system apps; may take a while)")
	if res, err := ex.Run(ctx, executor.RunSpec{Command: integrationWaitAgentScript}); err != nil || res.ExitCode != 0 {
		if detail := strings.TrimSpace(res.Stderr); detail != "" {
			fmt.Fprintln(os.Stderr, detail)
		}
		errorf("validation host-agent did not become healthy")
		return fail("host-agent did not become healthy")
	}

	// Step 8: Run the tier's commands against the deployed runtime.
	step("Running integration tests")
	exitCode := integrationRunTests(ctx, ex, tier, rt, result, flags)

	// Stop the validation unit. The runtime dir and containers are left in
	// place for inspection; ./bloud dev re-converges the dev state.
	if _, err := ex.Run(ctx, executor.RunSpec{
		Command: "systemctl --user disable --now " + integrationHostAgentUnit + " >/dev/null 2>&1 || true",
	}); err != nil {
		errorf("failed to stop validation host-agent: %v", err)
	}

	if exitCode != 0 {
		result.ExitCode = 1
		result.Confidence = "low"
		result.ConfidenceReason = "integration tests failed"
		result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		writeLedger(root, result, flags)
		return 1
	}

	result.ExitCode = 0
	result.Confidence = "high"
	result.ConfidenceReason = "integration tests passed against the real dependency-graph path"
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if !flags.json {
		fmt.Printf("\n%s==>%s Validation runtime remains at %s (guest). Re-run %s%s%s to restore the dev runtime state.\n",
			colorGreen, colorReset, rt, colorCyan, "./bloud dev", colorReset)
	}
	writeLedger(root, result, flags)
	return 0
}

// integrationPrepareGuest verifies the guest has everything the tier needs
// and stops any host-agent holding port 3000. Returns a ledger fail reason,
// or "" when the guest is ready.

func runCommands(root string, commands []manifestCommand, result *ValidateResult, flags validateFlags) int {
	exitCode := 0
	for _, cmd := range commands {
		if flags.explain && !flags.json {
			fmt.Printf("    %s→%s %s: %s\n", colorCyan, colorReset, cmd.ID, cmd.Run)
		}

		cwd := root
		if cmd.Cwd != "." {
			cwd = filepath.Join(root, cmd.Cwd)
		}

		parts := splitShellWords(cmd.Run)
		if len(parts) == 0 {
			result.Commands = append(result.Commands, CommandResult{
				ID:         cmd.ID,
				Cwd:        cmd.Cwd,
				Command:    cmd.Run,
				Status:     "fail",
				DurationMs: 0,
				ExitCode:   1,
			})
			if !flags.json {
				fmt.Printf("%s✗%s %s (empty command)\n", colorRed, colorReset, cmd.ID)
			}
			exitCode = 1
			continue
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

		cmdExit := 0
		status := "pass"
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				cmdExit = exitErr.ExitCode()
			} else {
				cmdExit = 1
			}
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
	}
	return exitCode
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
