package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/cli/vm"
)

// localExec runs a command on the host machine (not in VM)
func localExec(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

const (
	devVMName      = "bloud"
	devTmuxSession = "bloud-dev"
	devProjectInVM = "/home/bloud.linux/bloud"
)

var devPorts = []vm.PortForward{
	{LocalPort: 3000, RemotePort: 3000},
	{LocalPort: 5173, RemotePort: 5173},
	{LocalPort: 8080, RemotePort: 8080},
	{LocalPort: 8085, RemotePort: 8085},
	{LocalPort: 9001, RemotePort: 9001},
	{LocalPort: 5006, RemotePort: 5006},
	{LocalPort: 3080, RemotePort: 3080},
}

func getProjectRoot() (string, error) {
	// Find project root by looking for cli/main.go relative to executable or cwd
	// First try relative to current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// Check if we're in the project root
	if _, err := os.Stat(filepath.Join(cwd, "cli", "main.go")); err == nil {
		return cwd, nil
	}

	// Check if we're in cli directory
	if _, err := os.Stat(filepath.Join(cwd, "main.go")); err == nil {
		return filepath.Dir(cwd), nil
	}

	// Try to find project root by looking for lima/nixos.yaml or flake.nix
	for dir := cwd; dir != "/"; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "lima", "nixos.yaml")); err == nil {
			return dir, nil
		}
		if _, err := os.Stat(filepath.Join(dir, "flake.nix")); err == nil {
			return dir, nil
		}
	}

	return "", fmt.Errorf("could not find project root (looking for lima/nixos.yaml or flake.nix)")
}

func getDevConfigPath() (string, error) {
	root, err := getProjectRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "lima", "nixos.yaml"), nil
}

func cmdStart() int {
	if vm.IsNative() {
		return cmdStartNative()
	}

	return cmdStartLima()
}

func cmdStartLima() int {
	ctx := context.Background()

	projectRoot, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	configPath := filepath.Join(projectRoot, "lima", "nixos.yaml")

	// Run pre-flight checks before attempting to start
	preflightResult := vm.RunPreflightChecks(projectRoot)
	if preflightResult.HasErrors() {
		vm.PrintPreflightErrors(preflightResult)
		return 1
	}

	// Ensure VM is running
	if err := vm.EnsureRunning(ctx, devVMName, configPath); err != nil {
		errorf("Failed to start VM: %v", err)
		return 1
	}

	// Mount filesystems if VM was just started
	log("Mounting shared directories...")
	mounts := []vm.Mount{
		{Tag: "mount0", MountPath: devProjectInVM},
		{Tag: "mount1", MountPath: "/tmp/lima"},
	}
	if err := vm.MountFilesystems(devVMName, mounts); err != nil {
		warn(fmt.Sprintf("Mount warning: %v", err))
	}

	// Start port forwarding if not already running
	if !isPortForwardingRunning(devPorts[0].LocalPort) {
		log("Starting port forwarding...")
		if _, err := vm.StartPortForwarding(devVMName, devPorts); err != nil {
			warn(fmt.Sprintf("Port forwarding warning: %v", err))
		}
		time.Sleep(2 * time.Second)
	} else {
		log("Port forwarding already running")
	}

	// Start dev environment in VM
	log("Starting hot reload dev environment...")
	if _, err := vm.Exec(devVMName, fmt.Sprintf("bash %s/lima/start-dev.sh", devProjectInVM)); err != nil {
		// Check if it's just because session already exists
		if !strings.Contains(err.Error(), "already running") {
			errorf("Failed to start dev environment: %v", err)
			return 1
		}
	}

	return 0
}

func cmdStop() int {
	if vm.IsNative() {
		return cmdStopNative()
	}

	if !vm.IsRunning(devVMName) {
		log("VM is not running")
		return 0
	}

	log("Stopping dev services...")

	// Kill tmux session
	_, _ = vm.Exec(devVMName, fmt.Sprintf("tmux kill-session -t %s 2>/dev/null || true", devTmuxSession))

	// Kill stray processes
	_, _ = vm.Exec(devVMName, `pkill -f "air" 2>/dev/null || true; pkill -f "vite" 2>/dev/null || true`)

	// Kill port forwarding
	_ = vm.KillPortForwarding(devVMName, devPorts[0].LocalPort)

	log("Dev services stopped")
	return 0
}

func cmdStatus() int {
	if vm.IsNative() {
		return cmdStatusNative()
	}

	fmt.Println()

	// Check VM status
	status := vm.GetStatus(devVMName)
	switch status {
	case vm.StatusRunning:
		fmt.Printf("  VM:           %sRunning%s\n", colorGreen, colorReset)
	case vm.StatusStopped:
		fmt.Printf("  VM:           %sStopped%s\n", colorYellow, colorReset)
		fmt.Println()
		fmt.Println("  Run './bloud start' to start the dev environment")
		return 0
	default:
		fmt.Printf("  VM:           %sNot created%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Run './bloud start' to create and start the dev environment")
		return 0
	}

	// Check tmux session
	output, err := vm.Exec(devVMName, fmt.Sprintf("tmux has-session -t %s 2>/dev/null && echo running || echo stopped", devTmuxSession))
	if err == nil && strings.TrimSpace(output) == "running" {
		fmt.Printf("  Tmux Session: %sRunning%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Tmux Session: %sNot running%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Run './bloud start' to start the dev environment")
		return 0
	}

	// Check host-agent
	output, _ = vm.Exec(devVMName, `curl -s http://localhost:3000/api/health 2>/dev/null`)
	if strings.Contains(output, "ok") {
		fmt.Printf("  Host Agent:   %sRunning%s (http://localhost:3000)\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Host Agent:   %sStarting...%s\n", colorYellow, colorReset)
	}

	// Check web UI
	output, _ = vm.Exec(devVMName, `curl -s http://localhost:8080 2>/dev/null`)
	if strings.Contains(output, "html") {
		fmt.Printf("  Web UI:       %sRunning%s (http://localhost:8080)\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Web UI:       %sStarting...%s\n", colorYellow, colorReset)
	}

	// Check port forwarding
	if isPortForwardingRunning(devPorts[0].LocalPort) {
		fmt.Printf("  Port Forward: %sActive%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Port Forward: %sNot running%s\n", colorRed, colorReset)
	}

	// Check podman containers
	fmt.Println()
	log("Podman containers:")
	output, _ = vm.Exec(devVMName, `podman ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"`)
	if strings.TrimSpace(output) == "" {
		fmt.Println("  (none running)")
	} else {
		fmt.Println(output)
	}

	return 0
}

func cmdLogs() int {
	if vm.IsNative() {
		return cmdLogsNative()
	}

	if !vm.IsRunning(devVMName) {
		errorf("VM is not running. Start with: ./bloud start")
		return 1
	}

	// Check if tmux session exists
	output, err := vm.Exec(devVMName, fmt.Sprintf("tmux has-session -t %s 2>/dev/null && echo running || echo stopped", devTmuxSession))
	if err != nil || strings.TrimSpace(output) != "running" {
		errorf("Dev environment not running. Start with: ./bloud start")
		return 1
	}

	log("Capturing output from tmux...")
	fmt.Println()

	fmt.Printf("%s=== Go (hot reload) ===%s\n", colorCyan, colorReset)
	output, _ = vm.Exec(devVMName, fmt.Sprintf("tmux capture-pane -t %s:dev.0 -p -S -50", devTmuxSession))
	fmt.Println(output)

	fmt.Printf("%s=== Web (vite) ===%s\n", colorCyan, colorReset)
	output, _ = vm.Exec(devVMName, fmt.Sprintf("tmux capture-pane -t %s:dev.1 -p -S -50", devTmuxSession))
	fmt.Println(output)

	return 0
}

func cmdAttach() int {
	if vm.IsNative() {
		return cmdAttachNative()
	}

	if !vm.IsRunning(devVMName) {
		errorf("VM is not running. Start with: ./bloud start")
		return 1
	}

	// Check if tmux session exists
	output, err := vm.Exec(devVMName, fmt.Sprintf("tmux has-session -t %s 2>/dev/null && echo running || echo stopped", devTmuxSession))
	if err != nil || strings.TrimSpace(output) != "running" {
		errorf("Dev environment not running. Start with: ./bloud start")
		return 1
	}

	log("Attaching to dev session (Ctrl-B D to detach)...")
	if err := vm.InteractiveShell(devVMName, fmt.Sprintf("tmux attach -t %s", devTmuxSession)); err != nil {
		errorf("Failed to attach: %v", err)
		return 1
	}

	return 0
}

func cmdShell(args []string) int {
	var command string
	if len(args) > 0 {
		command = strings.Join(args, " ")
	}

	if vm.IsNative() {
		if err := vm.LocalInteractive(command); err != nil {
			if command == "" {
				return 0
			}
			errorf("Command failed: %v", err)
			return 1
		}
		return 0
	}

	if !vm.IsRunning(devVMName) {
		errorf("VM is not running. Start with: ./bloud start")
		return 1
	}

	if err := vm.InteractiveShell(devVMName, command); err != nil {
		// Don't print error for normal shell exit
		if command == "" {
			return 0
		}
		errorf("Command failed: %v", err)
		return 1
	}

	return 0
}

func cmdRebuild() int {
	if vm.IsNative() {
		return cmdRebuildNative()
	}

	if !vm.IsRunning(devVMName) {
		errorf("VM is not running. Start with: ./bloud start")
		return 1
	}

	// Initialize secrets before rebuild to ensure NixOS can read them
	log("Ensuring secrets are initialized...")
	if _, err := vm.Exec(devVMName, "/tmp/host-agent init-secrets /home/bloud/.local/share/bloud"); err != nil {
		warn(fmt.Sprintf("Failed to initialize secrets: %v (continuing anyway)", err))
	}

	log("Rebuilding NixOS configuration...")
	cmd := fmt.Sprintf("sudo nixos-rebuild switch --flake %s#vm-dev --impure", devProjectInVM)
	if err := vm.ExecStream(devVMName, cmd); err != nil {
		errorf("Rebuild failed: %v", err)
		return 1
	}

	return 0
}

func cmdServices() int {
	if vm.IsNative() {
		output, err := vm.LocalExec("systemctl --user list-units 'podman-*' --all --no-pager")
		if err != nil {
			errorf("Failed to get services: %v", err)
			return 1
		}
		fmt.Println(output)
		return 0
	}

	if !vm.IsRunning(devVMName) {
		errorf("VM is not running. Start with: ./bloud start")
		return 1
	}

	output, err := vm.Exec(devVMName, "systemctl --user list-units 'podman-*' --all --no-pager")
	if err != nil {
		errorf("Failed to get services: %v", err)
		return 1
	}

	fmt.Println(output)
	return 0
}

func cmdDestroy() int {
	if vm.IsNative() {
		fmt.Println("Destroy is not applicable on native NixOS.")
		fmt.Println("To reset state, use: bloud-reset")
		return 0
	}

	if !vm.Exists(devVMName) {
		log("VM does not exist")
		return 0
	}

	// Kill port forwarding first
	_ = vm.KillPortForwarding(devVMName, devPorts[0].LocalPort)

	log("Destroying dev VM...")
	if err := vm.Delete(devVMName); err != nil {
		errorf("Failed to destroy VM: %v", err)
		return 1
	}

	log("Dev VM destroyed")
	return 0
}

// isPortForwardingRunning checks if port forwarding is running for a port
func isPortForwardingRunning(port int) bool {
	// Check for local SSH process doing port forwarding
	pattern := fmt.Sprintf("ssh.*-L %d:localhost:%d.*bloud@", port, port)
	cmd := localExec("pgrep", "-f", pattern)
	output, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(output)) != ""
}

func cmdInstall(args []string) int {
	if len(args) < 1 {
		errorf("Usage: ./bloud install <app-name>")
		return 1
	}

	appName := args[0]
	return installApp(devVMName, 3000, appName)
}

func cmdUninstall(args []string) int {
	if len(args) < 1 {
		errorf("Usage: ./bloud uninstall <app-name>")
		return 1
	}

	appName := args[0]
	return uninstallApp(devVMName, 3000, appName)
}

// installApp calls the host-agent API to install an app
func installApp(vmName string, apiPort int, appName string) int {
	if !vm.IsNative() && !vm.IsRunning(vmName) {
		errorf("VM is not running. Start with: ./bloud start")
		return 1
	}

	log(fmt.Sprintf("Installing %s...", appName))

	// Call the host-agent API
	curlCmd := fmt.Sprintf(`curl -s -X POST -w "\n%%{http_code}" http://localhost:%d/api/apps/%s/install`, apiPort, appName)
	output, err := vm.Run(vmName, curlCmd)
	if err != nil {
		errorf("Failed to call install API: %v", err)
		return 1
	}

	// Parse response - last line is HTTP status code
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 1 {
		errorf("Empty response from API")
		return 1
	}

	httpCode := lines[len(lines)-1]
	responseBody := strings.Join(lines[:len(lines)-1], "\n")

	if httpCode != "200" && httpCode != "201" {
		errorf("Install failed (HTTP %s): %s", httpCode, responseBody)
		return 1
	}

	log(fmt.Sprintf("Successfully installed %s", appName))
	fmt.Println(responseBody)
	return 0
}

// uninstallApp calls the host-agent API to uninstall an app
func uninstallApp(vmName string, apiPort int, appName string) int {
	if !vm.IsNative() && !vm.IsRunning(vmName) {
		errorf("VM is not running. Start with: ./bloud start")
		return 1
	}

	log(fmt.Sprintf("Uninstalling %s...", appName))

	// Call the host-agent API
	curlCmd := fmt.Sprintf(`curl -s -X POST -w "\n%%{http_code}" http://localhost:%d/api/apps/%s/uninstall`, apiPort, appName)
	output, err := vm.Run(vmName, curlCmd)
	if err != nil {
		errorf("Failed to call uninstall API: %v", err)
		return 1
	}

	// Parse response - last line is HTTP status code
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 1 {
		errorf("Empty response from API")
		return 1
	}

	httpCode := lines[len(lines)-1]
	responseBody := strings.Join(lines[:len(lines)-1], "\n")

	if httpCode != "200" {
		errorf("Uninstall failed (HTTP %s): %s", httpCode, responseBody)
		return 1
	}

	log(fmt.Sprintf("Successfully uninstalled %s", appName))
	fmt.Println(responseBody)
	return 0
}

// --- Native NixOS command implementations ---

const nativeDataDir = "/home/bloud/.local/share/bloud"

func cmdStartNative() int {
	projectRoot, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	// Run native preflight checks
	preflightResult := vm.RunNativePreflightChecks()
	if preflightResult.HasErrors() {
		vm.PrintPreflightErrors(preflightResult)
		return 1
	}

	// Create data directories
	log("Ensuring data directories exist...")
	for _, dir := range []string{
		nativeDataDir + "/nix",
		nativeDataDir + "/traefik/dynamic",
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			errorf("Failed to create directory %s: %v", dir, err)
			return 1
		}
	}

	// Ensure podman network exists
	log("Ensuring podman network...")
	_, _ = vm.LocalExec("podman network create apps-net 2>/dev/null || true")

	// Build host-agent binary
	log("Building host-agent binary...")
	buildCmd := fmt.Sprintf("cd %s/services/host-agent && go build -o /tmp/host-agent ./cmd/host-agent", projectRoot)
	if err := vm.LocalExecStream(buildCmd); err != nil {
		errorf("Failed to build host-agent: %v", err)
		return 1
	}

	// Initialize secrets
	log("Initializing secrets...")
	if _, err := vm.LocalExec(fmt.Sprintf("/tmp/host-agent init-secrets %s", nativeDataDir)); err != nil {
		warn(fmt.Sprintf("Failed to initialize secrets: %v (continuing anyway)", err))
	}

	// Restart any failed services
	log("Checking for failed services...")
	_, _ = vm.LocalExec("systemctl --user reset-failed 2>/dev/null || true")
	services := []string{
		"podman-apps-postgres", "podman-apps-redis", "bloud-db-init",
		"authentik-db-init", "podman-apps-authentik-server",
		"podman-apps-authentik-worker", "podman-apps-authentik-proxy",
	}
	for _, svc := range services {
		output, _ := vm.LocalExec(fmt.Sprintf("systemctl --user is-failed %s.service 2>/dev/null && echo failed || echo ok", svc))
		if strings.TrimSpace(output) == "failed" {
			log(fmt.Sprintf("Restarting failed service: %s", svc))
			_, _ = vm.LocalExec(fmt.Sprintf("systemctl --user restart %s.service 2>/dev/null || true", svc))
		}
	}

	// Install npm dependencies if needed
	webDir := filepath.Join(projectRoot, "services", "host-agent", "web")
	nodeModules := filepath.Join(webDir, "node_modules")
	if _, err := os.Stat(nodeModules); os.IsNotExist(err) {
		log("Installing npm dependencies...")
		if err := vm.LocalExecStream(fmt.Sprintf("cd %s && npm install", webDir)); err != nil {
			warn(fmt.Sprintf("npm install failed: %v", err))
		}
	}

	// Start tmux session
	log("Starting hot reload dev environment...")
	if err := startNativeTmux(projectRoot); err != nil {
		if strings.Contains(err.Error(), "already") {
			log("Dev environment already running!")
			fmt.Println()
			fmt.Println("  To view: ./bloud attach")
			fmt.Println("  To stop: ./bloud stop")
			return 0
		}
		errorf("Failed to start dev environment: %v", err)
		return 1
	}

	fmt.Println()
	fmt.Println("=== Development Environment Started ===")
	fmt.Println()
	fmt.Println("Services (with hot reload):")
	fmt.Println("  Go API:  http://localhost:3000  (rebuilding on *.go changes)")
	fmt.Println("  Web UI:  http://localhost:5173  (vite HMR)")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  ./bloud attach   - View tmux session (Ctrl-B D to detach)")
	fmt.Println("  ./bloud logs     - View server output")
	fmt.Println("  ./bloud stop     - Stop dev servers")
	fmt.Println("  ./bloud status   - Check service status")
	fmt.Println()

	return 0
}

func startNativeTmux(projectRoot string) error {
	// Check if session already exists
	output, _ := vm.LocalExec(fmt.Sprintf("tmux has-session -t %s 2>/dev/null && echo running || echo stopped", devTmuxSession))
	if strings.TrimSpace(output) == "running" {
		return fmt.Errorf("session already running")
	}

	hostAgentDir := filepath.Join(projectRoot, "services", "host-agent")
	webDir := filepath.Join(hostAgentDir, "web")

	// Write go-watch script (no file sync needed on native)
	goWatchScript := fmt.Sprintf(`#!/usr/bin/env bash
# Go hot reload - native (no file sync needed)
cd %s

export BLOUD_PORT=3000
export BLOUD_DATA_DIR="%s"
export BLOUD_APPS_DIR="%s/apps"
export BLOUD_NIX_CONFIG_DIR="%s/nix"
export BLOUD_FLAKE_PATH="%s"
export BLOUD_FLAKE_TARGET="dev-server"
export BLOUD_NIXOS_PATH="%s/nixos"

BIN="/tmp/host-agent"
LAST_HASH=""
PID=""

cleanup() {
    [ -n "$PID" ] && kill "$PID" 2>/dev/null
    exit 0
}
trap cleanup SIGINT SIGTERM

build_and_run() {
    echo "[$(date +%%H:%%M:%%S)] Building..."
    if go build -o "$BIN" ./cmd/host-agent 2>&1; then
        echo "[$(date +%%H:%%M:%%S)] Build successful, starting server..."
        [ -n "$PID" ] && kill "$PID" 2>/dev/null && sleep 1
        "$BIN" &
        PID=$!
        echo "[$(date +%%H:%%M:%%S)] Server started (PID: $PID)"
    else
        echo "[$(date +%%H:%%M:%%S)] Build failed!"
    fi
}

build_and_run

echo "[$(date +%%H:%%M:%%S)] Watching for changes (polling every 2s)..."
while true; do
    sleep 2
    HASH=$(find %s -name '*.go' -not -path '*/web/*' -exec stat -c '%%Y' {} \; 2>/dev/null | sort | md5sum)
    if [ "$HASH" != "$LAST_HASH" ]; then
        LAST_HASH="$HASH"
        echo ""
        echo "[$(date +%%H:%%M:%%S)] Change detected!"
        build_and_run
    fi
done
`, hostAgentDir, nativeDataDir, projectRoot, nativeDataDir, projectRoot, projectRoot, hostAgentDir)

	goWatchPath := "/tmp/go-watch-native.sh"
	if err := os.WriteFile(goWatchPath, []byte(goWatchScript), 0755); err != nil {
		return fmt.Errorf("failed to write go-watch script: %w", err)
	}

	// Write vite script
	viteScript := fmt.Sprintf(`#!/usr/bin/env bash
cd %s
npx vite dev --port 5173
`, webDir)

	vitePath := "/tmp/run-vite-native.sh"
	if err := os.WriteFile(vitePath, []byte(viteScript), 0755); err != nil {
		return fmt.Errorf("failed to write vite script: %w", err)
	}

	// Create tmux session with two panes
	if _, err := vm.LocalExec(fmt.Sprintf("tmux new-session -d -s %s -n dev", devTmuxSession)); err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	// First pane: Go hot reload
	_, _ = vm.LocalExec(fmt.Sprintf("tmux send-keys -t %s:dev '%s' Enter", devTmuxSession, goWatchPath))

	// Split horizontally for second pane
	_, _ = vm.LocalExec(fmt.Sprintf("tmux split-window -h -t %s:dev", devTmuxSession))

	// Second pane: Vite dev server
	_, _ = vm.LocalExec(fmt.Sprintf("tmux send-keys -t %s:dev.1 '%s' Enter", devTmuxSession, vitePath))

	// Set equal pane sizes
	_, _ = vm.LocalExec(fmt.Sprintf("tmux select-layout -t %s:dev even-horizontal", devTmuxSession))

	return nil
}

func cmdStopNative() int {
	log("Stopping dev services...")

	// Kill tmux session
	_, _ = vm.LocalExec(fmt.Sprintf("tmux kill-session -t %s 2>/dev/null || true", devTmuxSession))

	// Kill stray processes
	_, _ = vm.LocalExec(`pkill -f "go-watch-native" 2>/dev/null || true`)
	_, _ = vm.LocalExec(`pkill -f "run-vite-native" 2>/dev/null || true`)

	log("Dev services stopped")
	return 0
}

func cmdStatusNative() int {
	fmt.Println()
	fmt.Printf("  Runtime:      %sNative NixOS%s\n", colorGreen, colorReset)

	// Check tmux session
	output, _ := vm.LocalExec(fmt.Sprintf("tmux has-session -t %s 2>/dev/null && echo running || echo stopped", devTmuxSession))
	if strings.TrimSpace(output) == "running" {
		fmt.Printf("  Tmux Session: %sRunning%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Tmux Session: %sNot running%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Run './bloud start' to start the dev environment")
		return 0
	}

	// Check host-agent
	output, _ = vm.LocalExec("curl -s http://localhost:3000/api/health 2>/dev/null")
	if strings.Contains(output, "ok") {
		fmt.Printf("  Host Agent:   %sRunning%s (http://localhost:3000)\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Host Agent:   %sStarting...%s\n", colorYellow, colorReset)
	}

	// Check web UI
	output, _ = vm.LocalExec("curl -s http://localhost:8080 2>/dev/null")
	if strings.Contains(output, "html") {
		fmt.Printf("  Web UI:       %sRunning%s (http://localhost:8080)\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Web UI:       %sStarting...%s\n", colorYellow, colorReset)
	}

	// Check podman containers
	fmt.Println()
	log("Podman containers:")
	output, _ = vm.LocalExec(`podman ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"`)
	if strings.TrimSpace(output) == "" {
		fmt.Println("  (none running)")
	} else {
		fmt.Println(output)
	}

	return 0
}

func cmdLogsNative() int {
	// Check if tmux session exists
	output, _ := vm.LocalExec(fmt.Sprintf("tmux has-session -t %s 2>/dev/null && echo running || echo stopped", devTmuxSession))
	if strings.TrimSpace(output) != "running" {
		errorf("Dev environment not running. Start with: ./bloud start")
		return 1
	}

	log("Capturing output from tmux...")
	fmt.Println()

	fmt.Printf("%s=== Go (hot reload) ===%s\n", colorCyan, colorReset)
	output, _ = vm.LocalExec(fmt.Sprintf("tmux capture-pane -t %s:dev.0 -p -S -50", devTmuxSession))
	fmt.Println(output)

	fmt.Printf("%s=== Web (vite) ===%s\n", colorCyan, colorReset)
	output, _ = vm.LocalExec(fmt.Sprintf("tmux capture-pane -t %s:dev.1 -p -S -50", devTmuxSession))
	fmt.Println(output)

	return 0
}

func cmdAttachNative() int {
	// Check if tmux session exists
	output, _ := vm.LocalExec(fmt.Sprintf("tmux has-session -t %s 2>/dev/null && echo running || echo stopped", devTmuxSession))
	if strings.TrimSpace(output) != "running" {
		errorf("Dev environment not running. Start with: ./bloud start")
		return 1
	}

	log("Attaching to dev session (Ctrl-B D to detach)...")
	if err := vm.LocalInteractive(fmt.Sprintf("tmux attach -t %s", devTmuxSession)); err != nil {
		errorf("Failed to attach: %v", err)
		return 1
	}

	return 0
}

func cmdRebuildNative() int {
	projectRoot, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	// Initialize secrets before rebuild
	log("Ensuring secrets are initialized...")
	if _, err := vm.LocalExec(fmt.Sprintf("/tmp/host-agent init-secrets %s", nativeDataDir)); err != nil {
		warn(fmt.Sprintf("Failed to initialize secrets: %v (continuing anyway)", err))
	}

	log("Rebuilding NixOS configuration...")
	cmd := fmt.Sprintf("sudo nixos-rebuild switch --flake %s#dev-server --impure", projectRoot)
	if err := vm.LocalExecStream(cmd); err != nil {
		errorf("Rebuild failed: %v", err)
		return 1
	}

	return 0
}
