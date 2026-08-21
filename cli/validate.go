// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/cli/executor"

	"gopkg.in/yaml.v3"
)

// ValidateResult is the final ledger written after a validation run.
type ValidateResult struct {
	StartedAt        string           `json:"startedAt"`
	FinishedAt       string           `json:"finishedAt"`
	Tier             string           `json:"tier"`
	ExitCode         int              `json:"exitCode"`
	Apps             []string         `json:"apps"`
	ChangedFiles     []string         `json:"changedFiles"`
	RiskAreas        []string         `json:"riskAreas"`
	Confidence       string           `json:"confidence"`
	ConfidenceReason string           `json:"confidenceReason"`
	Commands         []CommandResult  `json:"commands"`
	Skipped          []SkippedCommand `json:"skipped"`
	UnmappedFiles    []string         `json:"unmappedFiles"`
	Artifacts        []string         `json:"artifacts"`
	NextRecommended  string           `json:"nextRecommendedTier"`
}

// CommandResult records the outcome of a single validation command.
type CommandResult struct {
	ID         string `json:"id"`
	Cwd        string `json:"cwd"`
	Command    string `json:"command"`
	Status     string `json:"status"`
	DurationMs int64  `json:"durationMs"`
	ExitCode   int    `json:"exitCode"`
}

// SkippedCommand records a command that was not run and why.
type SkippedCommand struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Manifest types for validation.yaml
type validationManifest struct {
	Tiers     map[string]manifestTier `yaml:"tiers"`
	Inference manifestInference       `yaml:"inference"`
	Apps      map[string]manifestApp  `yaml:"apps"`
}

type manifestTier struct {
	Commands []manifestCommand `yaml:"commands"`
}

type manifestCommand struct {
	ID  string `yaml:"id"`
	Cwd string `yaml:"cwd"`
	Run string `yaml:"run"`
}

type manifestInference struct {
	Paths []manifestPath `yaml:"paths"`
}

type manifestPath struct {
	Pattern   string   `yaml:"pattern"`
	Triggers  []string `yaml:"triggers"`
	RiskAreas []string `yaml:"riskAreas"`
}

type manifestApp struct {
	Auth            string   `yaml:"auth"`
	ValidationLevel string   `yaml:"validation-level"`
	Files           []string `yaml:"files"`
	E2EProject      string   `yaml:"e2e-project"`
}

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

func loadManifest(root string) (*validationManifest, error) {
	data, err := os.ReadFile(filepath.Join(root, "validation.yaml"))
	if err != nil {
		return nil, err
	}
	var m validationManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// --- Fast tier ---

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
	triggeredIDs := map[string]bool{}
	riskAreas := map[string]bool{}
	var unmapped []string

	for _, f := range changedFiles {
		matched := false
		for _, p := range manifest.Inference.Paths {
			if pathMatches(f, p.Pattern) {
				matched = true
				for _, t := range p.Triggers {
					triggeredIDs[t] = true
				}
				for _, r := range p.RiskAreas {
					riskAreas[r] = true
				}
			}
		}
		if !matched {
			unmapped = append(unmapped, f)
		}
	}

	result.UnmappedFiles = unmapped
	for r := range riskAreas {
		result.RiskAreas = append(result.RiskAreas, r)
	}
	sort.Strings(result.RiskAreas)

	// Determine confidence
	result.Confidence = "high"
	result.ConfidenceReason = "all changed files mapped to commands"
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
	appSet := map[string]bool{}
	for appName, appDef := range manifest.Apps {
		for _, f := range changedFiles {
			if appSet[appName] {
				break
			}
			for _, pattern := range appDef.Files {
				if pathMatches(f, pattern) {
					appSet[appName] = true
					break
				}
			}
		}
	}
	for a := range appSet {
		result.Apps = append(result.Apps, a)
	}
	sort.Strings(result.Apps)

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
				fmt.Printf("Risk areas detected: %s — consider running a higher tier.\n", strings.Join(result.RiskAreas, ", "))
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

// --- Integration tier ---

// integrationRuntimeDir is the guest-side home of the validation runtime: a
// self-contained host-agent deployment (binary, web build, app catalog,
// data) separate from the dev runtime. The tier deploys the current code
// here through the real product path (host-agent + orchestrator + catalog)
// and runs the tier's commands against it, so integration validation
// exercises the same install/reconcile flow as production.
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

func renderIntegrationHostAgentUnit(rt string, qemu bool) string {
	var extraEnv string
	if qemu {
		extraEnv = "Environment=BLOUD_TRUSTED_LOCAL_NETS=10.0.2.0/24\n"
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
	step("Provisioning " + vmLabel())
	bk, err := devBackend()
	if err != nil {
		errorf("could not set up backend: %v", err)
		return fail("backend setup failed")
	}
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
	qemu := backendName() == "qemu"
	rt := integrationRuntimeDir

	// Step 2: Guest preflight.
	step("Checking integration prerequisites")
	if res, err := ex.Run(ctx, executor.RunSpec{Command: integrationPreflightScript}); err != nil || res.ExitCode != 0 {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" && err != nil {
			detail = err.Error()
		}
		errorf("integration prerequisites missing (recreate the VM): %s", detail)
		return fail("integration prerequisites missing")
	}

	// Step 3: Stop any host-agent holding port 3000. The validation runtime
	// takes the port over for the duration of the tier; the dev runtime
	// state (data, containers) is untouched and ./bloud dev converges it
	// back afterwards.
	step("Stopping any running host-agent (validation runtime takes over port 3000)")
	if res, err := ex.Run(ctx, executor.RunSpec{Command: integrationStopAgentScript}); err != nil || res.ExitCode != 0 {
		errorf("failed to stop running host-agent: %v", err)
		return fail("failed to stop running host-agent")
	}

	// Step 4: Build artifacts locally.
	step("Building host-agent for linux/" + runtime.GOARCH)
	tmpDir, err := os.MkdirTemp("", "bloud-validate-build-*")
	if err != nil {
		errorf("failed to create build dir: %v", err)
		return fail("could not create build dir")
	}
	defer os.RemoveAll(tmpDir)

	hostAgentSrc := filepath.Join(root, "services", "host-agent")
	binaryPath := filepath.Join(tmpDir, "host-agent")
	buildCmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/host-agent")
	buildCmd.Dir = hostAgentSrc
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		errorf("host-agent build failed: %v", err)
		return fail("host-agent build failed")
	}

	step("Building frontend")
	webCmd := exec.Command("npm", "run", "build", "--workspace=@bloud/host-agent-web")
	webCmd.Dir = root
	webCmd.Stdout = os.Stdout
	webCmd.Stderr = os.Stderr
	if err := webCmd.Run(); err != nil {
		errorf("frontend build failed: %v", err)
		return fail("frontend build failed")
	}

	step("Building integration test binary")
	testBinary := filepath.Join(tmpDir, "bloud-integration.test")
	testBuild := exec.Command("go", "test", "-tags", "integration", "-c", "-o", testBinary, "./internal/e2e")
	testBuild.Dir = hostAgentSrc
	testBuild.Stdout = os.Stdout
	testBuild.Stderr = os.Stderr
	if err := testBuild.Run(); err != nil {
		errorf("integration test build failed: %v", err)
		return fail("integration test build failed")
	}

	// Step 5: Deploy to the validation runtime.
	step("Deploying to " + rt)
	if _, err := ex.Run(ctx, executor.RunSpec{
		Command: fmt.Sprintf("rm -rf %s/host-agent %s/apps && mkdir -p %s/host-agent/web/build %s/apps %s/data %s/bin", rt, rt, rt, rt, rt, rt),
	}); err != nil {
		errorf("failed to prepare runtime dir: %v", err)
		return fail("failed to prepare runtime dir")
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
			return fail("deployment failed")
		}
	}
	if _, err := ex.Run(ctx, executor.RunSpec{
		Command: fmt.Sprintf("chmod 755 %s/host-agent/host-agent %s/bin/bloud-integration.test", rt, rt),
	}); err != nil {
		errorf("failed to chmod deployed binaries: %v", err)
		return fail("deployment failed")
	}

	// Generate runtime secrets on first use (product command; idempotent).
	// The integration tests read the real values from secrets.json.
	if _, err := ex.Run(ctx, executor.RunSpec{
		Command: fmt.Sprintf("%s/host-agent/host-agent init-secrets %s/data", rt, rt),
	}); err != nil {
		errorf("failed to initialize runtime secrets: %v", err)
		return fail("secret initialization failed")
	}

	// Step 6: Install and start the host-agent systemd service.
	step("Installing and starting " + integrationHostAgentUnit)
	unit := renderIntegrationHostAgentUnit(rt, qemu)
	unitPath := filepath.Join(tmpDir, integrationHostAgentUnit)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		errorf("failed to write unit file: %v", err)
		return fail("failed to write unit file")
	}
	if err := ex.CopyTo(ctx, unitPath, "/tmp/"+integrationHostAgentUnit); err != nil {
		errorf("failed to copy unit file into guest: %v", err)
		return fail("failed to deploy unit file")
	}
	if res, err := ex.Run(ctx, executor.RunSpec{
		Command: fmt.Sprintf(`install -d "$HOME/.config/systemd/user"
install -m 644 /tmp/%[1]s "$HOME/.config/systemd/user/%[1]s"
rm -f /tmp/%[1]s
systemctl --user daemon-reload
systemctl --user enable --now %[1]s`, integrationHostAgentUnit),
	}); err != nil || res.ExitCode != 0 {
		errorf("failed to install host-agent service: %v", err)
		return fail("failed to install host-agent service")
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

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// --- Helpers ---

// splitShellWords splits a command string into arguments, respecting single
// quotes, double quotes, and backslash escapes. It is a minimal shell-like
// tokenizer used to turn manifest command strings into exec.Command args so
// that quoted arguments containing spaces survive intact.
func splitShellWords(s string) []string {
	var words []string
	for i := 0; i < len(s); {
		for i < len(s) && isShellSpace(s[i]) {
			i++
		}
		if i == len(s) {
			break
		}

		var word strings.Builder
		i = readShellWord(s, i, &word)
		if word.Len() > 0 {
			words = append(words, word.String())
		}
	}
	return words
}

// isShellSpace reports whether b is a word-separating whitespace byte.
func isShellSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// readShellWord reads one shell word starting at i (a non-space byte), writing
// its unquoted contents into word. It returns the index just past the word.
func readShellWord(s string, i int, word *strings.Builder) int {
	for i < len(s) {
		switch {
		case isShellSpace(s[i]):
			return i
		case s[i] == '\'':
			i = readSingleQuoted(s, i, word)
		case s[i] == '"':
			i = readDoubleQuoted(s, i, word)
		case s[i] == '\\':
			if i+1 < len(s) {
				word.WriteByte(s[i+1])
				i += 2
			} else {
				i++
			}
		default:
			word.WriteByte(s[i])
			i++
		}
	}
	return i
}

// readSingleQuoted copies the literal contents of a single-quoted section
// starting at the opening quote at i, and returns the index just past it. An
// unterminated quote consumes the rest of the string.
func readSingleQuoted(s string, i int, word *strings.Builder) int {
	close := strings.IndexByte(s[i+1:], '\'')
	if close < 0 {
		word.WriteString(s[i+1:])
		return len(s)
	}
	word.WriteString(s[i+1 : i+1+close])
	return i + close + 2
}

// readDoubleQuoted copies the contents of a double-quoted section starting at
// the opening quote at i, honoring backslash escapes, and returns the index
// just past it. An unterminated quote consumes the rest of the string.
func readDoubleQuoted(s string, i int, word *strings.Builder) int {
	for i++; i < len(s); i++ {
		switch {
		case s[i] == '"':
			return i + 1
		case s[i] == '\\' && i+1 < len(s):
			switch s[i+1] {
			case '"', '\\', '$', '`':
				word.WriteByte(s[i+1])
			default:
				word.WriteByte('\\')
				word.WriteByte(s[i+1])
			}
			i++
		default:
			word.WriteByte(s[i])
		}
	}
	return i
}

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

func getChangedFiles(root string, since string) ([]string, error) {
	// Get both staged and unstaged changes
	var args []string
	if since != "" {
		args = []string{"diff", "--name-only", since}
	} else {
		args = []string{"diff", "--name-only", "HEAD"}
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		// HEAD might not exist (initial commit), fall back to listing all tracked + untracked
		cmd2 := exec.Command("git", "status", "--porcelain")
		cmd2.Dir = root
		out2, err2 := cmd2.Output()
		if err2 != nil {
			return nil, err2
		}
		return parseStatusFiles(string(out2)), nil
	}

	files := splitLines(string(out))

	// Also get staged changes not yet committed
	cmd2 := exec.Command("git", "diff", "--name-only", "--cached")
	cmd2.Dir = root
	out2, err := cmd2.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --cached failed: %w", err)
	}
	staged := splitLines(string(out2))

	// Also get untracked files
	cmd3 := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd3.Dir = root
	out3, err := cmd3.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files --others failed: %w", err)
	}
	untracked := splitLines(string(out3))

	// Deduplicate
	seen := map[string]bool{}
	var result []string
	for _, list := range [][]string{files, staged, untracked} {
		for _, f := range list {
			if f != "" && !seen[f] {
				seen[f] = true
				result = append(result, f)
			}
		}
	}
	sort.Strings(result)
	return result, nil
}

func parseStatusFiles(status string) []string {
	var files []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		f := strings.TrimSpace(line[3:])
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

func splitLines(s string) []string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// pathMatches checks if a file path matches a glob-like pattern.
// Supports ** for recursive directory matching and * for single segment.
func pathMatches(file, pattern string) bool {
	// Convert glob pattern to a simple prefix + suffix check
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return strings.HasPrefix(file, prefix+"/") || file == prefix
	}
	if strings.Contains(pattern, "**") {
		// pattern like "a/**/b" — split and check prefix/suffix
		parts := strings.SplitN(pattern, "**", 2)
		return strings.HasPrefix(file, parts[0]) && strings.HasSuffix(file, strings.TrimPrefix(parts[1], "/"))
	}
	// Exact match or single-level glob
	matched, _ := filepath.Match(pattern, file)
	return matched
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

func writeLedger(root string, result *ValidateResult, flags validateFlags) {
	if flags.json {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
	}

	// Write to .bloud/validation/
	dir := filepath.Join(root, ".bloud", "validation")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return
	}

	// Write timestamped file
	ts := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	tsPath := filepath.Join(dir, ts+".json")
	os.WriteFile(tsPath, data, 0644)

	// Write latest.json
	latestPath := filepath.Join(dir, "latest.json")
	os.WriteFile(latestPath, data, 0644)

	// Prune old files (keep newest 20)
	pruneOldLedgers(dir, 20)
}

func pruneOldLedgers(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var jsonFiles []string
	for _, e := range entries {
		name := e.Name()
		if name == "latest.json" || !strings.HasSuffix(name, ".json") {
			continue
		}
		jsonFiles = append(jsonFiles, name)
	}

	if len(jsonFiles) <= keep {
		return
	}

	sort.Strings(jsonFiles)
	toRemove := jsonFiles[:len(jsonFiles)-keep]
	for _, name := range toRemove {
		os.Remove(filepath.Join(dir, name))
	}
}
