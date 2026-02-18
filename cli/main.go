package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/cli/vm"
)

const (
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorYellow = "\033[1;33m"
	colorCyan   = "\033[0;36m"
	colorReset  = "\033[0m"
)

// loadDotEnv reads a .env file from the project root and sets any variables
// not already present in the environment. This lets users configure BLOUD_PVE_HOST
// and other settings without needing to export them from their shell profile.
func loadDotEnv() {
	root, err := getProjectRoot()
	if err != nil {
		return
	}
	f, err := os.Open(filepath.Join(root, ".env"))
	if err != nil {
		return
	}
	defer f.Close()

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
			os.Setenv(key, value)
		}
	}
}

func main() {
	loadDotEnv()
	vm.DetectRuntime()

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

	// For Lima mode, ensure SSH is available
	if !vm.IsNative() && !isPVEMode() {
		if err := vm.EnsureSSHAvailable(); err != nil {
			fmt.Fprintf(os.Stderr, "%sError:%s %v\n", colorRed, colorReset, err)
			fmt.Fprintf(os.Stderr, "\nRun './bloud setup' to check all prerequisites.\n")
			os.Exit(1)
		}
	}

	var exitCode int

	switch cmd {
	case "start":
		if isPVEMode() {
			exitCode = cmdStartPVE(args)
		} else {
			exitCode = cmdStart()
		}
	case "stop":
		if isPVEMode() {
			exitCode = cmdStopPVE()
		} else {
			exitCode = cmdStop()
		}
	case "status":
		if isPVEMode() {
			exitCode = cmdStatusPVE()
		} else {
			exitCode = cmdStatus()
		}
	case "logs":
		if isPVEMode() {
			exitCode = cmdLogsPVE()
		} else {
			exitCode = cmdLogs()
		}
	case "shell":
		if isPVEMode() {
			exitCode = cmdShellPVE(args)
		} else {
			exitCode = cmdShell(args)
		}
	case "install":
		if isPVEMode() {
			exitCode = cmdInstallPVE(args)
		} else {
			exitCode = cmdInstall(args)
		}
	case "uninstall":
		if isPVEMode() {
			exitCode = cmdUninstallPVE(args)
		} else {
			exitCode = cmdUninstall(args)
		}
	case "destroy":
		if isPVEMode() {
			exitCode = cmdDestroyPVE()
		} else {
			exitCode = cmdDestroy()
		}
	case "checks":
		if isPVEMode() {
			exitCode = cmdChecksPVE()
		} else {
			fmt.Fprintf(os.Stderr, "%sError:%s 'checks' is only available in Proxmox mode (set BLOUD_PVE_HOST)\n", colorRed, colorReset)
			exitCode = 1
		}
	// Lima-only commands
	case "services":
		exitCode = cmdServices()
	case "attach":
		exitCode = cmdAttach()
	case "rebuild":
		exitCode = cmdRebuild()
	case "depgraph":
		exitCode = cmdDepGraph()

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

func printUsage() {
	fmt.Println("Bloud CLI")
	fmt.Println()

	if isPVEMode() {
		fmt.Printf("  Backend: %sProxmox%s (%s)\n", colorCyan, colorReset, os.Getenv("BLOUD_PVE_HOST"))
		fmt.Println()
		fmt.Println("Usage: ./bloud <command> [args]")
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("  start [iso] [flags]   Deploy ISO → create VM → boot → check (VM stays running)")
		fmt.Println("    --skip-deploy       Reuse existing VM (skip ISO upload + VM create)")
		fmt.Println("    --pve-host <host>   Override Proxmox SSH target")
		fmt.Println("    --vmid <id>         Override VM ID")
		fmt.Println("  stop                  Stop VM")
		fmt.Println("  destroy               Destroy VM completely")
		fmt.Println("  status                Show VM and service status")
		fmt.Println("  logs                  Stream VM journalctl")
		fmt.Println("  shell [cmd]           SSH into VM")
		fmt.Println("  checks                Run health checks against running VM")
		fmt.Println("  install <app>         Install an app via API")
		fmt.Println("  uninstall <app>       Uninstall an app via API")
		fmt.Println()
		fmt.Println("Environment:")
		fmt.Println("  BLOUD_PVE_HOST        Proxmox SSH target (e.g. root@192.168.0.62)")
		fmt.Println("  BLOUD_PVE_VMID        VM ID (default: 9999)")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  ./bloud start                         # test latest GitHub release")
		fmt.Println("  ./bloud start ./bloud.iso             # test local ISO")
		fmt.Println("  ./bloud start --skip-deploy           # re-run checks on existing VM")
		return
	}

	if vm.IsNative() {
		fmt.Println("  Backend: Native NixOS")
	} else {
		fmt.Println("  Backend: Lima VM")
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
		fmt.Println("Commands:")
		fmt.Println("  start           Start dev environment")
	} else {
		fmt.Println("Commands (persistent Lima VM, ports 8080/3000/5173):")
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
	fmt.Println("URLs (after start):")
	fmt.Println("  http://localhost:8080     Web UI (via Traefik)")
	fmt.Println("  http://localhost:3000     Go API")
	fmt.Println()
	fmt.Println("Proxmox mode: set BLOUD_PVE_HOST to switch to ISO testing against Proxmox")
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
