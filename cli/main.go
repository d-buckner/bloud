// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorYellow = "\033[1;33m"
	colorCyan   = "\033[0;36m"
	colorDim    = "\033[2m"
	colorReset  = "\033[0m"
)

// loadDotEnv reads a .env file from the project root and sets any variables
// not already present in the environment.
func loadDotEnv() {
	root, err := getProjectRoot()
	if err != nil {
		return
	}
	f, err := os.Open(filepath.Join(root, ".env"))
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// Strip optional surrounding quotes
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		// Only set if not already in environment
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}

func main() {
	loadDotEnv()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	// Handle setup command before any other checks
	if cmd == "setup" {
		os.Exit(cmdSetup())
	}

	os.Exit(dispatch(cmd, args))
}

// dispatch routes a CLI command to its handler; unknown commands print usage.
func dispatch(cmd string, args []string) int {
	handlers := map[string]func([]string) int{
		"start":      func([]string) int { return cmdStart() },
		"stop":       func([]string) int { return cmdStop() },
		"status":     func([]string) int { return cmdStatus() },
		"logs":       func([]string) int { return cmdLogs() },
		"shell":      cmdShell,
		"install":    cmdInstall,
		"uninstall":  cmdUninstall,
		"reset":      cmdReset,
		"destroy":    func([]string) int { return cmdDestroy() },
		"services":   func([]string) int { return cmdServices() },
		"attach":     func([]string) int { return cmdAttach() },
		"rebuild":    func([]string) int { return cmdRebuild() },
		"dev":        cmdDev,
		"e2e":        cmdE2E,
		"validate":   cmdValidate,
		"package":    cmdPackage,
		"depgraph":   cmdDepGraph,
		"catalogdoc": cmdCatalogDoc,
		"token":      func([]string) int { return cmdToken() },
	}
	if h, ok := handlers[cmd]; ok {
		return h(args)
	}
	switch cmd {
	case "help", "--help", "-h":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "%sError:%s Unknown command: %s\n", colorRed, colorReset, cmd)
		printUsage()
		return 1
	}
}

// usageLines is the CLI help screen as data: one entry per printed line. The
// text is the contract with whoever reads `./bloud` with no arguments, so it
// lives in one block that can be read top to bottom.
var usageLines = []string{
	"Bloud CLI",
	"",
	"Usage: ./bloud <command> [args]",
	"",
	"Setup:",
	"  setup           Select runtime backend, check prerequisites, build CLI",
	"",
	"Dev (VM):",
	"  dev             Build + deploy + run host-agent on the VM (Ctrl-C to stop)",
	"    --reset        Wipe the runtime first (same as reset -y, no prompt)",
	"    --no-watch     Run the one-shot build/deploy loop instead of hot reload",
	"    -v | --verbose  Stream raw subprocess output, not just warnings",
	"                    (all of it is mirrored to .bloud/logs/dev.log either way)",
	"  start           Show dev environment quick-start instructions",
	"  stop            Stop host-agent running on the VM",
	"  status          Show VM and host-agent status",
	"  services        Show app container status on the VM",
	"  logs            Stream host-agent logs from the VM",
	"  attach          Open a shell on the VM",
	"  shell [cmd]     Run a command on the VM (or open a shell)",
	"  install <app>   Install an app via API (requires running host-agent)",
	"  uninstall <app> Uninstall an app via API",
	"  reset           Wipe all data in the VM and re-run setup (keeps VM)",
	"    -y | --yes     Skip the confirmation prompt",
	"  destroy         Delete the VM",
	"",
	"Validation:",
	"  validate [flags]     Run tiered validation (default: --tier changed)",
	"    --tier <t>         fast | changed | integration",
	"    --app <name>       Scope to a specific app",
	"    --dry-run          Show plan without executing",
	"    --explain          Print why each command was selected",
	"    --json             Output JSON ledger only",
	"    --verbose | -v     Stream each command's raw output as it runs",
	"                       (also BLOUD_VALIDATE_VERBOSE=1)",
	"    --since <ref>      Git ref for diff base (default: HEAD)",
	"  e2e lifecycle [flags] Run full lifecycle E2E",
	"  e2e affected [--base <ref> | --since <ref>] [--json]",
	"                       Print the e2e projects a change set needs",
	"                       (--base diffs from the merge base: the whole PR)",
	"",
	"Release:",
	"  package              Build the host-agent, frontend, and catalog into a .deb",
	"    --arch <a>         Package architecture (default: host arch)",
	"    --version <v>      Package version (default: git describe)",
	"    --out <dir>        Output directory (default: dist)",
	"",
	"Other:",
	"  depgraph        Generate the full Mermaid dependency graph from app metadata",
	"    --write          Embed it in README.md (between the generated markers)",
	"    --check          Exit 1 when README.md's graph is stale",
	"  catalogdoc      Generate the README's catalog list and one-login table",
	"    --write          Replace both generated blocks in README.md",
	"    --check          Exit 1 when either block is stale",
	"    --target FILE    File to write or check (default: README.md)",
	"  token           Print the host-agent API token (for ad-hoc curl / e2e)",
}

func printUsage() {
	for _, line := range usageLines {
		fmt.Println(line)
	}
	fmt.Println()
	printBackendQuickStart()
	fmt.Println("  ./bloud dev")
}

// printBackendQuickStart prints the first-run lines for whichever runtime
// backend is selected, or the pointer at `setup` when there is none yet.
func printBackendQuickStart() {
	switch usageBackend() {
	case "qemu":
		fmt.Println("QEMU VM quick-start:")
		fmt.Println("  ./bloud dev   # provisions .bloud/qemu/bloud-qemu, boots VM")
	case "native":
		fmt.Println("Native quick-start:")
		fmt.Println("  ./bloud dev   # runs directly on this host (no VM)")
	case "lima":
		fmt.Println("Lima VM quick-start:")
		fmt.Println("  limactl create --name=bloud-dev dev/lima.yaml")
		fmt.Println("  limactl start bloud-dev")
	default:
		fmt.Println("First time? Pick a runtime backend:")
		fmt.Println("  ./bloud setup")
	}
}

func log(msg string) {
	fprintLog(os.Stdout, msg)
}

// fprintLog writes a "==>" progress line to w.
func fprintLog(w io.Writer, msg string) {
	_, _ = fmt.Fprintf(w, "%s==>%s %s\n", colorGreen, colorReset, msg)
}

func errorf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%sError:%s "+format+"\n", append([]any{colorRed, colorReset}, args...)...)
}
