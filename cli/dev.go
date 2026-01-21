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
	devVMName       = "bloud"
	devTmuxSession  = "bloud-dev"
	devProjectInVM  = "/home/bloud.linux/bloud"
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

	// Try to find project root by looking for lima/nixos.yaml
	for dir := cwd; dir != "/"; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "lima", "nixos.yaml")); err == nil {
			return dir, nil
		}
	}

	return "", fmt.Errorf("could not find project root (looking for lima/nixos.yaml)")
}

func getDevConfigPath() (string, error) {
	root, err := getProjectRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "lima", "nixos.yaml"), nil
}

func cmdStart() int {
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
	if !vm.IsRunning(devVMName) {
		errorf("VM is not running. Start with: ./bloud start")
		return 1
	}

	var command string
	if len(args) > 0 {
		command = strings.Join(args, " ")
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
	if !vm.IsRunning(vmName) {
		errorf("VM is not running. Start with: ./bloud start")
		return 1
	}

	log(fmt.Sprintf("Installing %s...", appName))

	// Call the host-agent API
	cmd := fmt.Sprintf(`curl -s -X POST -w "\n%%{http_code}" http://localhost:%d/api/apps/%s/install`, apiPort, appName)
	output, err := vm.Exec(vmName, cmd)
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
	if !vm.IsRunning(vmName) {
		errorf("VM is not running. Start with: ./bloud start")
		return 1
	}

	log(fmt.Sprintf("Uninstalling %s...", appName))

	// Call the host-agent API
	cmd := fmt.Sprintf(`curl -s -X POST -w "\n%%{http_code}" http://localhost:%d/api/apps/%s/uninstall`, apiPort, appName)
	output, err := vm.Exec(vmName, cmd)
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
