package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

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

	// Step 1: Check Lima VM is running
	if !flags.json {
		fmt.Printf("%s==>%s Checking Lima VM 'bloud-dev'...\n", colorGreen, colorReset)
	}
	if !isLimaVMRunning("bloud-dev") {
		if !flags.json {
			fmt.Printf("%s==>%s Starting Lima VM 'bloud-dev'...\n", colorGreen, colorReset)
		}
		startCmd := exec.Command("limactl", "start", "bloud-dev")
		startCmd.Stdout = os.Stdout
		startCmd.Stderr = os.Stderr
		if err := startCmd.Run(); err != nil {
			errorf("failed to start Lima VM: %v", err)
			result.ExitCode = 1
			result.Confidence = "low"
			result.ConfidenceReason = "Lima VM failed to start"
			result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			writeLedger(root, result, flags)
			return 1
		}
	}

	quotedRoot := shellQuote(root)

	// Step 2: Verify the VM has the tools and setup files required by the tests.
	if !flags.json {
		fmt.Printf("%s==>%s Checking integration prerequisites...\n", colorGreen, colorReset)
	}
	preflightScript := fmt.Sprintf(
		"command -v podman >/dev/null && command -v podman-compose >/dev/null && command -v go >/dev/null && command -v ldapsearch >/dev/null && test -f %s/dev/host-agent.env",
		quotedRoot,
	)
	preflight := exec.Command("limactl", "shell", "bloud-dev", "bash", "-c", preflightScript)
	if out, err := preflight.CombinedOutput(); err != nil {
		errorf("integration prerequisites are missing; recreate/provision the VM and run dev/setup.sh: %v: %s", err, strings.TrimSpace(string(out)))
		result.ExitCode = 1
		result.Confidence = "low"
		result.ConfidenceReason = "integration prerequisites missing"
		result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		writeLedger(root, result, flags)
		return 1
	}

	// Step 3: Converge the compose stack and wait for required services.
	if !flags.json {
		fmt.Printf("%s==>%s Starting compose stack...\n", colorGreen, colorReset)
	}
	composeScript := fmt.Sprintf(`cd %s/dev
services="postgres redis authentik-server authentik-worker authentik-proxy authentik-ldap jellyfin"
for service in $services; do
  id=$(podman ps -aq --filter "label=com.docker.compose.project=dev" --filter "label=com.docker.compose.service=$service" | head -1)
  if [ -n "$id" ]; then
    podman start "$id" >/dev/null
  else
    podman-compose up -d --no-recreate "$service"
  fi
done`, quotedRoot)
	composeUp := exec.Command("limactl", "shell", "bloud-dev", "bash", "-c", composeScript)
	composeUp.Stdout = os.Stdout
	composeUp.Stderr = os.Stderr
	if err := composeUp.Run(); err != nil {
		errorf("failed to start compose stack: %v", err)
		result.ExitCode = 1
		result.Confidence = "low"
		result.ConfidenceReason = "compose stack failed to start"
		result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		writeLedger(root, result, flags)
		return 1
	}

	readinessScript := fmt.Sprintf(`cd %s/dev
services="postgres redis authentik-server authentik-worker authentik-proxy authentik-ldap jellyfin"
deadline=$((SECONDS + 180))
while [ "$SECONDS" -lt "$deadline" ]; do
  ready=true
  for service in $services; do
    id=$(podman ps -q --filter "label=com.docker.compose.project=dev" --filter "label=com.docker.compose.service=$service" | head -1)
    if [ -z "$id" ] || [ "$(podman inspect -f '{{.State.Running}}' "$id" 2>/dev/null)" != "true" ]; then
      ready=false
      break
    fi
    health=$(podman inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$id" 2>/dev/null)
    if [ -n "$health" ] && [ "$health" != "healthy" ]; then
      ready=false
      break
    fi
  done
  if [ "$ready" = true ]; then
    exit 0
  fi
  sleep 3
done
podman-compose ps
exit 1`, quotedRoot)
	readiness := exec.Command("limactl", "shell", "bloud-dev", "bash", "-c", readinessScript)
	readiness.Stdout = os.Stdout
	readiness.Stderr = os.Stderr
	if err := readiness.Run(); err != nil {
		errorf("compose stack did not become ready: %v", err)
		result.ExitCode = 1
		result.Confidence = "low"
		result.ConfidenceReason = "compose stack did not become ready"
		result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		writeLedger(root, result, flags)
		return 1
	}

	// Step 4: Build host-agent inside VM
	if !flags.json {
		fmt.Printf("%s==>%s Building host-agent...\n", colorGreen, colorReset)
	}
	buildCmd := exec.Command("limactl", "shell", "bloud-dev", "bash", "-c",
		fmt.Sprintf("cd %s/services/host-agent && go build -o /tmp/host-agent ./cmd/host-agent", quotedRoot))
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		errorf("failed to build host-agent: %v", err)
		result.ExitCode = 1
		result.Confidence = "low"
		result.ConfidenceReason = "host-agent build failed"
		result.Commands = append(result.Commands, CommandResult{
			ID: "build-host-agent", Command: "go build ./cmd/host-agent", Status: "fail", ExitCode: 1,
		})
		result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		writeLedger(root, result, flags)
		return 1
	}
	result.Commands = append(result.Commands, CommandResult{
		ID: "build-host-agent", Command: "go build ./cmd/host-agent", Status: "pass", ExitCode: 0,
	})

	// Step 5: Run integration tests inside VM
	if !flags.json {
		fmt.Printf("%s==>%s Running integration tests...\n", colorGreen, colorReset)
	}

	for _, cmd := range tier.Commands {
		// Source the host-agent.env file and run the test command inside the VM
		shellCmd := fmt.Sprintf(
			"set -a && source %s/dev/host-agent.env && set +a && cd %s/%s && %s",
			quotedRoot, quotedRoot, shellQuote(cmd.Cwd), cmd.Run,
		)
		testCmd := exec.Command("limactl", "shell", "bloud-dev", "bash", "-c", shellCmd)
		if !flags.json {
			testCmd.Stdout = os.Stdout
			testCmd.Stderr = os.Stderr
		}

		start := time.Now()
		err := testCmd.Run()
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

		if cmdExit != 0 {
			result.ExitCode = 1
			result.Confidence = "low"
			result.ConfidenceReason = fmt.Sprintf("integration test %s failed", cmd.ID)
			result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			writeLedger(root, result, flags)
			return 1
		}
	}

	result.ExitCode = 0
	result.Confidence = "high"
	result.ConfidenceReason = "integration tests passed against real services"
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	writeLedger(root, result, flags)
	return 0
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// isLimaVMRunning checks if a Lima VM is in Running status.
func isLimaVMRunning(name string) bool {
	cmd := exec.Command("limactl", "list", "--json")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	// limactl list --json outputs one JSON object per line
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var vm struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(line), &vm) == nil {
			if vm.Name == name && vm.Status == "Running" {
				return true
			}
		}
	}
	return false
}

// --- Helpers ---

// splitShellWords splits a command string into arguments, respecting single
// quotes, double quotes, and backslash escapes. It is a minimal shell-like
// tokenizer used to turn manifest command strings into exec.Command args so
// that quoted arguments containing spaces survive intact.
func splitShellWords(s string) []string {
	runes := []rune(s)
	var words []string
	var word []rune
	i := 0
	flush := func() {
		if len(word) > 0 {
			words = append(words, string(word))
			word = word[:0]
		}
	}
	for i < len(runes) {
		r := runes[i]
		switch r {
		case ' ', '\t', '\n', '\r':
			flush()
			i++
		case '\'':
			i++
			for i < len(runes) && runes[i] != '\'' {
				word = append(word, runes[i])
				i++
			}
			i++
		case '"':
			i++
			for i < len(runes) && runes[i] != '"' {
				if runes[i] == '\\' && i+1 < len(runes) {
					next := runes[i+1]
					switch next {
					case '"', '\\', '$', '`':
						word = append(word, next)
					default:
						word = append(word, runes[i], runes[i+1])
					}
					i += 2
				} else {
					word = append(word, runes[i])
					i++
				}
			}
			i++
		case '\\':
			if i+1 < len(runes) {
				word = append(word, runes[i+1])
				i += 2
			} else {
				i++
			}
		default:
			word = append(word, r)
			i++
		}
	}
	flush()
	return words
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
