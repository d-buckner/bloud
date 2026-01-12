package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud-v3/cli/vm"
)

// localExec runs a command on the host machine (not in VM)
func localExec(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

const (
	devVMName       = "bloud"
	devTmuxSession  = "bloud-dev"
	devProjectInVM  = "/home/bloud.linux/bloud-v3"
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

	configPath, err := getDevConfigPath()
	if err != nil {
		errorf("Could not find config: %v", err)
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

	log("Rebuilding NixOS configuration...")
	cmd := fmt.Sprintf("sudo nixos-rebuild switch --flake %s#vm-dev --impure", devProjectInVM)
	if err := vm.ExecStream(devVMName, cmd); err != nil {
		errorf("Rebuild failed: %v", err)
		return 1
	}

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
