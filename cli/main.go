// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorYellow = "\033[1;33m"
	colorCyan   = "\033[0;36m"
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
		"start":     func([]string) int { return cmdStart() },
		"stop":      func([]string) int { return cmdStop() },
		"status":    func([]string) int { return cmdStatus() },
		"logs":      func([]string) int { return cmdLogs() },
		"shell":     cmdShell,
		"install":   cmdInstall,
		"uninstall": cmdUninstall,
		"reset":     func([]string) int { return cmdReset() },
		"destroy":   func([]string) int { return cmdDestroy() },
		"services":  func([]string) int { return cmdServices() },
		"attach":    func([]string) int { return cmdAttach() },
		"rebuild":   func([]string) int { return cmdRebuild() },
		"dev":       func([]string) int { return cmdDev() },
		"e2e":       cmdE2E,
		"validate":  cmdValidate,
		"package":   cmdPackage,
		"depgraph":  func([]string) int { return cmdDepGraph() },
		"token":     func([]string) int { return cmdToken() },
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

func printUsage() {
	fmt.Println("Bloud CLI")
	fmt.Println()
	fmt.Println("Usage: ./bloud <command> [args]")
	fmt.Println()
	fmt.Println("Setup:")
	fmt.Println("  setup           Select runtime backend, check prerequisites, build CLI")
	fmt.Println()
	fmt.Println("Dev (VM):")
	fmt.Println("  dev             Build + deploy + run host-agent on the VM (Ctrl-C to stop)")
	fmt.Println("  start           Show dev environment quick-start instructions")
	fmt.Println("  stop            Stop host-agent running on the VM")
	fmt.Println("  status          Show VM and host-agent status")
	fmt.Println("  services        Show app container status on the VM")
	fmt.Println("  logs            Stream host-agent logs from the VM")
	fmt.Println("  attach          Open a shell on the VM")
	fmt.Println("  shell [cmd]     Run a command on the VM (or open a shell)")
	fmt.Println("  install <app>   Install an app via API (requires running host-agent)")
	fmt.Println("  uninstall <app> Uninstall an app via API")
	fmt.Println("  reset           Wipe all data in the VM and re-run setup (keeps VM)")
	fmt.Println("  destroy         Delete the VM")
	fmt.Println()
	fmt.Println("Validation:")
	fmt.Println("  validate [flags]     Run tiered validation (default: --tier changed)")
	fmt.Println("    --tier <t>         fast | changed | integration")
	fmt.Println("    --app <name>       Scope to a specific app")
	fmt.Println("    --dry-run          Show plan without executing")
	fmt.Println("    --explain          Print why each command was selected")
	fmt.Println("    --json             Output JSON ledger only")
	fmt.Println("    --since <ref>      Git ref for diff base (default: HEAD)")
	fmt.Println("  e2e lifecycle [flags] Run full lifecycle E2E")
	fmt.Println("  e2e affected [--since <ref>] [--json]")
	fmt.Println("                       Print the e2e projects a change set needs")
	fmt.Println()
	fmt.Println("Release:")
	fmt.Println("  package              Build the host-agent, frontend, and catalog into a .deb")
	fmt.Println("    --arch <a>         Package architecture (default: host arch)")
	fmt.Println("    --version <v>      Package version (default: git describe)")
	fmt.Println("    --out <dir>        Output directory (default: dist)")
	fmt.Println()
	fmt.Println("Other:")
	fmt.Println("  depgraph        Generate Mermaid dependency graph from app metadata")
	fmt.Println("  token           Print the host-agent API token (for ad-hoc curl / e2e)")
	fmt.Println()
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
	fmt.Println("  ./bloud dev")
}

func log(msg string) {
	fmt.Printf("%s==>%s %s\n", colorGreen, colorReset, msg)
}

func errorf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%sError:%s "+format+"\n", append([]any{colorRed, colorReset}, args...)...)
}
