package main

import (
	"fmt"
	"os"

	"codeberg.org/d-buckner/bloud-v3/cli/vm"
)

const (
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorYellow = "\033[1;33m"
	colorCyan   = "\033[0;36m"
	colorReset  = "\033[0m"
)

func main() {
	// Ensure sshpass is available
	if err := vm.EnsureSshpass(); err != nil {
		fmt.Fprintf(os.Stderr, "%sError:%s %v\n", colorRed, colorReset, err)
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var exitCode int

	switch cmd {
	// Dev commands (top-level)
	case "start":
		exitCode = cmdStart()
	case "stop":
		exitCode = cmdStop()
	case "status":
		exitCode = cmdStatus()
	case "logs":
		exitCode = cmdLogs()
	case "attach":
		exitCode = cmdAttach()
	case "shell":
		exitCode = cmdShell(args)
	case "rebuild":
		exitCode = cmdRebuild()

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
	fmt.Println()
	fmt.Println("Usage: ./bloud <command> [args]")
	fmt.Println()
	fmt.Println("Dev Commands (persistent environment, ports 8080/3000/5173):")
	fmt.Println("  start           Start dev environment (auto-starts VM if needed)")
	fmt.Println("  stop            Stop dev services")
	fmt.Println("  status          Show dev environment status")
	fmt.Println("  logs            Show logs from dev services")
	fmt.Println("  attach          Attach to tmux session (Ctrl-B D to detach)")
	fmt.Println("  shell [cmd]     Shell into VM (or run a command)")
	fmt.Println("  rebuild         Rebuild NixOS configuration")
	fmt.Println()
	fmt.Println("Test Commands (ephemeral environment, ports 8081/3001/5174):")
	fmt.Println("  test start      Create fresh test VM and start services")
	fmt.Println("  test stop       Stop services and destroy test VM")
	fmt.Println("  test status     Show test environment status")
	fmt.Println("  test logs       Show logs from test services")
	fmt.Println("  test attach     Attach to test tmux session")
	fmt.Println("  test shell      Shell into test VM")
	fmt.Println("  test rebuild    Rebuild test VM NixOS config")
	fmt.Println()
	fmt.Println("URLs (after start):")
	fmt.Println("  http://localhost:8080     Dev - Web UI (via Traefik)")
	fmt.Println("  http://localhost:3000     Dev - Go API")
	fmt.Println("  http://localhost:8081     Test - Web UI (via Traefik)")
	fmt.Println("  http://localhost:3001     Test - Go API")
}

func printTestUsage() {
	fmt.Println("Bloud Test Environment")
	fmt.Println()
	fmt.Println("Usage: ./bloud test <command> [args]")
	fmt.Println()
	fmt.Println("The test environment is ephemeral - it auto-destroys on stop.")
	fmt.Println("It runs on different ports (8081/3001/5174) so it can run alongside dev.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  start           Create fresh test VM and start services")
	fmt.Println("  stop            Stop services and destroy test VM")
	fmt.Println("  status          Show test environment status")
	fmt.Println("  logs            Show logs from test services")
	fmt.Println("  attach          Attach to tmux session (Ctrl-B D to detach)")
	fmt.Println("  shell [cmd]     Shell into VM (or run a command)")
	fmt.Println("  rebuild         Rebuild NixOS configuration")
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
