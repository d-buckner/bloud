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
	case "setup-builder":
		if isPVEMode() {
			exitCode = cmdSetupBuilderPVE()
		} else {
			fmt.Fprintf(os.Stderr, "%sError:%s 'setup-builder' is only available in Proxmox mode (set BLOUD_PVE_HOST)\n", colorRed, colorReset)
			exitCode = 1
		}
	case "services":
		exitCode = cmdServices()
	case "attach":
		exitCode = cmdAttach()
	case "rebuild":
		if isPVEMode() {
			exitCode = cmdRebuildPVE()
		} else {
			exitCode = cmdRebuild()
		}
	case "push":
		if isPVEMode() {
			exitCode = cmdPushPVE()
		} else {
			fmt.Fprintf(os.Stderr, "%sError:%s 'push' is only available in Proxmox mode (set BLOUD_PVE_HOST)\n", colorRed, colorReset)
			exitCode = 1
		}
	case "snapshot":
		if isPVEMode() {
			exitCode = cmdSnapshotPVE(args)
		} else {
			fmt.Fprintf(os.Stderr, "%sError:%s 'snapshot' is only available in Proxmox mode (set BLOUD_PVE_HOST)\n", colorRed, colorReset)
			exitCode = 1
		}
	case "smoke":
		if isPVEMode() {
			exitCode = cmdSmokePVE(args)
		} else {
			fmt.Fprintf(os.Stderr, "%sError:%s 'smoke' is only available in Proxmox mode (set BLOUD_PVE_HOST)\n", colorRed, colorReset)
			exitCode = 1
		}
	case "validate":
		exitCode = cmdValidate(args)
	case "depgraph":
		exitCode = cmdDepGraph()
	case "installer":
		if len(args) > 0 && args[0] == "stop" {
			exitCode = cmdInstallerStop()
		} else {
			exitCode = cmdInstaller()
		}

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
		fmt.Println("Fast iteration (no ISO rebuild required):")
		fmt.Println("  push                  Cross-compile + hot-swap binary via drop-in (~30s)")
		fmt.Println("  rebuild               rsync NixOS config + nixos-rebuild switch (~2-3 min)")
		fmt.Println("  snapshot save [name]  Save VM snapshot (default: base-installed)")
		fmt.Println("  snapshot restore [n]  Restore snapshot and start VM (~15s)")
		fmt.Println("  snapshot list         List all snapshots for the test VM")
		fmt.Println()
		fmt.Println("Full ISO cycle:")
		fmt.Println("  start [iso] [flags]   Deploy ISO → create VM → boot live ISO")
		fmt.Println("    --install           Auto-install via API, then run health checks")
		fmt.Println("    --build             Build ISO locally via build VM instead of downloading")
		fmt.Println("    --skip-deploy       Reuse existing VM (skip ISO upload + VM create)")
		fmt.Println("    --pve-host <host>   Override Proxmox SSH target")
		fmt.Println("    --vmid <id>         Override VM ID")
		fmt.Println()
		fmt.Println("Change validation:")
		fmt.Println("  validate [flags]            Run tiered validation (default: --tier changed)")
		fmt.Println("    --tier <t>                fast | changed | vm | clean | full")
		fmt.Println("    --app <name>              Scope to a specific app")
		fmt.Println("    --dry-run                 Show plan without executing")
		fmt.Println("    --explain                 Print why each command was selected")
		fmt.Println("    --json                    Output JSON ledger only")
		fmt.Println("    --since <ref>             Git ref for diff base (default: HEAD)")
		fmt.Println("  smoke [flags]               Run Playwright smoke tests against existing VM")
		fmt.Println("    --build                   Build ISO + deploy VM + run full install before tests")
		fmt.Println("    --apps <app1> <app2>      Run only specified app tests (default: all apps)")
		fmt.Println("    --update-snapshots        Refresh committed baseline screenshots")
		fmt.Println()
		fmt.Println("VM management:")
		fmt.Println("  stop                  Stop VM")
		fmt.Println("  destroy               Destroy VM completely")
		fmt.Println("  status                Show VM and service status")
		fmt.Println("  logs                  Stream VM journalctl")
		fmt.Println("  shell [cmd]           SSH into VM")
		fmt.Println("  checks                Run health checks against running VM")
		fmt.Println("  install <app>         Install an app via API")
		fmt.Println("  uninstall <app>       Uninstall an app via API")
		fmt.Println("  setup-builder         Provision BLOUD_BUILDER_HOST with Go + Node via Nix")
		fmt.Println()
		fmt.Println("Environment:")
		fmt.Println("  BLOUD_PVE_HOST        Proxmox SSH target (e.g. root@10.0.0.165)")
		fmt.Println("  BLOUD_PVE_VMID        VM ID (default: 9999)")
		fmt.Println("  BLOUD_BUILDER_HOST    SSH target for local ISO builds (e.g. builder@192.168.0.105)")
		fmt.Println()
		fmt.Println("Typical workflow:")
		fmt.Println("  ./bloud start --install               # full install (first time)")
		fmt.Println("  ./bloud snapshot save                 # save clean state")
		fmt.Println("  # --- iterate ---")
		fmt.Println("  ./bloud push                          # test Go code change (~30s)")
		fmt.Println("  ./bloud rebuild                       # test NixOS config change (~2-3 min)")
		fmt.Println("  ./bloud snapshot restore              # reset to clean state")
		fmt.Println()
		fmt.Println("Other examples:")
		fmt.Println("  ./bloud start                         # boot live ISO (manual install)")
		fmt.Println("  ./bloud start ./bloud.iso --install   # boot local ISO + auto-install")
		fmt.Println("  ./bloud start --build                 # build ISO on BLOUD_BUILDER_HOST and boot")
		fmt.Println("  ./bloud start --skip-deploy           # re-run checks on existing VM")
		return
	}

	fmt.Println("  Backend: Native NixOS")
	fmt.Println()
	fmt.Println("Usage: ./bloud <command> [args]")
	fmt.Println()
	fmt.Println("Setup:")
	fmt.Println("  setup           Check prerequisites and apply NixOS configuration")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  start           Start dev environment")
	fmt.Println("  stop            Stop dev services")
	fmt.Println("  status          Show dev environment status")
	fmt.Println("  services        Show podman service status")
	fmt.Println("  logs            Show logs from dev services")
	fmt.Println("  attach          Attach to tmux session (Ctrl-B D to detach)")
	fmt.Println("  shell [cmd]     Run a command (or open a shell)")
	fmt.Println("  rebuild         Rebuild NixOS configuration")
	fmt.Println("  install <app>   Install an app")
	fmt.Println("  uninstall <app> Uninstall an app")
	fmt.Println("  validate [flags] Run validation (default: --tier changed)")
	fmt.Println("    --tier <t>    fast | changed | vm | clean | full")
	fmt.Println("    --app <name>  Scope to a specific app")
	fmt.Println("    --dry-run     Show plan without executing")
	fmt.Println("    --explain     Print why each command was selected")
	fmt.Println("    --json        Output JSON ledger only")
	fmt.Println("    --since <ref> Git ref for diff base (default: HEAD)")
	fmt.Println("    --no-vm       Disable VM tier even if PVE available")
	fmt.Println("  depgraph        Generate Mermaid dependency graph from app metadata")
	fmt.Println("  installer       Start installer UI in mock mode (http://localhost:5174)")
	fmt.Println("  installer stop  Stop the installer dev server")
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
