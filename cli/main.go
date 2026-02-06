package main

import (
	"fmt"
	"os"

	"codeberg.org/d-buckner/bloud/cli/vm"
)

const (
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorYellow = "\033[1;33m"
	colorCyan   = "\033[0;36m"
	colorReset  = "\033[0m"
)

func main() {
	vm.DetectRuntime()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	// Handle setup command before any other checks
	// (setup needs to work even when dependencies are missing)
	if cmd == "setup" {
		os.Exit(cmdSetup())
	}

	// For Lima mode, ensure SSH is available
	if !vm.IsNative() {
		if err := vm.EnsureSSHAvailable(); err != nil {
			fmt.Fprintf(os.Stderr, "%sError:%s %v\n", colorRed, colorReset, err)
			fmt.Fprintf(os.Stderr, "\nRun './bloud setup' to check all prerequisites.\n")
			os.Exit(1)
		}
	}

	var exitCode int

	switch cmd {
	// Dev commands (top-level)
	case "start":
		exitCode = cmdStart()
	case "stop":
		exitCode = cmdStop()
	case "status":
		exitCode = cmdStatus()
	case "services":
		exitCode = cmdServices()
	case "logs":
		exitCode = cmdLogs()
	case "attach":
		exitCode = cmdAttach()
	case "shell":
		exitCode = cmdShell(args)
	case "rebuild":
		exitCode = cmdRebuild()
	case "install":
		exitCode = cmdInstall(args)
	case "uninstall":
		exitCode = cmdUninstall(args)
	case "depgraph":
		exitCode = cmdDepGraph()
	case "destroy":
		exitCode = cmdDestroy()

	// Test commands (subcommand)
	case "test":
		exitCode = handleTest(args)

	case "help", "--help", "-h":
		printUsage()
		exitCode = 0

	default:
		fmt.Fprintf(os.Stderr, "%sError:%s Unknown command: %s\n", colorRed, colorReset, cmd)
		printUsage()
		exitCode = 1
	}

	os.Exit(exitCode)
}

func handleTest(args []string) int {
	if vm.IsNative() {
		fmt.Println("Test commands are not available on native NixOS.")
		fmt.Println("Test isolation requires Lima VMs (macOS/non-NixOS Linux).")
		return 1
	}

	if len(args) < 1 {
		printTestUsage()
		return 0
	}

	cmd := args[0]
	testArgs := args[1:]

	switch cmd {
	case "start":
		return testStart()
	case "stop":
		return testStop()
	case "reset":
		return testReset()
	case "destroy":
		return testDestroy()
	case "status":
		return testStatus()
	case "logs":
		return testLogs()
	case "attach":
		return testAttach()
	case "shell":
		return testShell(testArgs)
	case "rebuild":
		return testRebuild()
	case "services":
		return testServices()
	case "install":
		return testInstall(testArgs)
	case "uninstall":
		return testUninstall(testArgs)
	case "help", "--help", "-h":
		printTestUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "%sError:%s Unknown test command: %s\n", colorRed, colorReset, cmd)
		printTestUsage()
		return 1
	}
}

func printUsage() {
	fmt.Println("Bloud Development CLI")

	if vm.IsNative() {
		fmt.Println("  Runtime: Native NixOS")
	} else {
		fmt.Println("  Runtime: Lima VM")
	}

	fmt.Println()
	fmt.Println("Usage: ./bloud <command> [args]")
	fmt.Println()
	fmt.Println("Setup:")
	if vm.IsNative() {
		fmt.Println("  setup           Check prerequisites and apply NixOS configuration")
	} else {
		fmt.Println("  setup           Check prerequisites and download VM image")
	}
	fmt.Println()

	if vm.IsNative() {
		fmt.Println("Dev Commands (ports 8080/3000/5173):")
		fmt.Println("  start           Start dev environment")
	} else {
		fmt.Println("Dev Commands (persistent environment, ports 8080/3000/5173):")
		fmt.Println("  start           Start dev environment (auto-starts VM if needed)")
	}
	fmt.Println("  stop            Stop dev services")
	fmt.Println("  status          Show dev environment status")
	fmt.Println("  services        Show podman service status")
	fmt.Println("  logs            Show logs from dev services")
	fmt.Println("  attach          Attach to tmux session (Ctrl-B D to detach)")
	if vm.IsNative() {
		fmt.Println("  shell [cmd]     Run a command (or open a shell)")
	} else {
		fmt.Println("  shell [cmd]     Shell into VM (or run a command)")
	}
	fmt.Println("  rebuild         Rebuild NixOS configuration")
	fmt.Println("  install <app>   Install an app")
	fmt.Println("  uninstall <app> Uninstall an app")
	fmt.Println("  depgraph        Generate Mermaid dependency graph from app metadata")
	if !vm.IsNative() {
		fmt.Println("  destroy         Destroy the dev VM completely")
	}
	fmt.Println()

	if !vm.IsNative() {
		fmt.Println("Test Commands (isolated environment, ports 8081/3001/5174):")
		fmt.Println("  test start      Start test VM (warm start from snapshot if available)")
		fmt.Println("  test stop       Stop test VM (preserves snapshot for fast restart)")
		fmt.Println("  test reset      Reset to clean state from snapshot (fast)")
		fmt.Println("  test destroy    Completely destroy test VM and snapshot")
		fmt.Println("  test status     Show test environment status")
		fmt.Println("  test logs       Show logs from test services")
		fmt.Println("  test attach     Attach to test tmux session")
		fmt.Println("  test shell      Shell into test VM")
		fmt.Println("  test rebuild    Rebuild test VM NixOS config")
		fmt.Println("  test install    Install an app in test VM")
		fmt.Println("  test uninstall  Uninstall an app from test VM")
		fmt.Println()
	}

	fmt.Println("URLs (after start):")
	fmt.Println("  http://localhost:8080     Dev - Web UI (via Traefik)")
	fmt.Println("  http://localhost:3000     Dev - Go API")
	if !vm.IsNative() {
		fmt.Println("  http://localhost:8081     Test - Web UI (via Traefik)")
		fmt.Println("  http://localhost:3001     Test - Go API")
	}
}

func printTestUsage() {
	fmt.Println("Bloud Test Environment")
	fmt.Println()
	fmt.Println("Usage: ./bloud test <command> [args]")
	fmt.Println()
	fmt.Println("The test environment uses snapshots for fast startup:")
	fmt.Println("  - First 'test start' does a cold start (creates VM + snapshot)")
	fmt.Println("  - Subsequent 'test start' does a warm start from snapshot (fast!)")
	fmt.Println("  - 'test reset' restores to clean state without rebuilding")
	fmt.Println("  - 'test destroy' removes VM and snapshot completely")
	fmt.Println()
	fmt.Println("It runs on different ports (8081/3001/5174) so it can run alongside dev.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  start           Start test VM (warm start if snapshot exists)")
	fmt.Println("  stop            Stop test VM (preserves snapshot)")
	fmt.Println("  reset           Reset to clean state from snapshot (fast)")
	fmt.Println("  destroy         Completely destroy test VM and snapshot")
	fmt.Println("  status          Show test environment status")
	fmt.Println("  services        Show podman service status")
	fmt.Println("  logs            Show logs from test services")
	fmt.Println("  attach          Attach to tmux session (Ctrl-B D to detach)")
	fmt.Println("  shell [cmd]     Shell into VM (or run a command)")
	fmt.Println("  rebuild         Rebuild NixOS configuration")
	fmt.Println("  install <app>   Install an app")
	fmt.Println("  uninstall <app> Uninstall an app")
}

func log(msg string) {
	fmt.Printf("%s==>%s %s\n", colorGreen, colorReset, msg)
}

func warn(msg string) {
	fmt.Printf("%sWarning:%s %s\n", colorYellow, colorReset, msg)
}

func errorf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%sError:%s "+format+"\n", append([]any{colorRed, colorReset}, args...)...)
}
