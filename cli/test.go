package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"codeberg.org/d-buckner/bloud-v3/cli/vm"
)

const (
	testVMName      = "bloud-test"
	testTmuxSession = "bloud-test"
)

var testPorts = []vm.PortForward{
	{LocalPort: 3001, RemotePort: 3001},
	{LocalPort: 5174, RemotePort: 5174},
	{LocalPort: 8081, RemotePort: 8081},
}

func getTestConfigPath() (string, error) {
	root, err := getProjectRoot()
	if err != nil {
		return "", err
	}

	templatePath := filepath.Join(root, "lima", "test-nixos.yaml.template")
	outputPath := filepath.Join(root, "lima", "test-nixos.yaml")

	// Generate config from template
	if err := generateTestConfig(templatePath, outputPath, root); err != nil {
		return "", err
	}

	return outputPath, nil
}

func generateTestConfig(templatePath, outputPath, projectRoot string) error {
	tmplContent, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("failed to read template: %w", err)
	}

	tmpl, err := template.New("config").Parse(string(tmplContent))
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	// Get main repo path (for git worktree support)
	mainRepo := os.Getenv("BLOUD_MAIN_REPO")
	if mainRepo == "" {
		home, _ := os.UserHomeDir()
		mainRepo = filepath.Join(home, "Projects", "bloud-v3")
	}

	data := map[string]string{
		"PROJECT_ROOT":    projectRoot,
		"BLOUD_MAIN_REPO": mainRepo,
	}

	// Write output
	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output: %w", err)
	}
	defer f.Close()

	// Template uses __PROJECT_ROOT__ and __BLOUD_MAIN_REPO__ placeholders
	// We need to do simple string replacement instead of Go templates
	content := string(tmplContent)
	content = strings.ReplaceAll(content, "__PROJECT_ROOT__", projectRoot)
	content = strings.ReplaceAll(content, "__BLOUD_MAIN_REPO__", mainRepo)

	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	_ = tmpl // unused but kept for potential future template use
	_ = data

	return nil
}

func testStart() int {
	ctx := context.Background()

	configPath, err := getTestConfigPath()
	if err != nil {
		errorf("Could not generate config: %v", err)
		return 1
	}

	projectRoot, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	// Create temp directory for test VM
	if err := os.MkdirAll("/tmp/lima-test", 0755); err != nil {
		warn(fmt.Sprintf("Could not create temp dir: %v", err))
	}

	// Delete existing VM if it exists (ephemeral)
	if vm.Exists(testVMName) {
		log("Removing existing test VM...")
		if err := vm.Delete(testVMName); err != nil {
			errorf("Failed to delete existing VM: %v", err)
			return 1
		}
	}

	// Create fresh VM
	log(fmt.Sprintf("Creating fresh test VM '%s'...", testVMName))
	if err := vm.Create(testVMName, configPath); err != nil {
		errorf("Failed to create VM: %v", err)
		return 1
	}

	// Wait for SSH
	log("Waiting for test VM to boot...")
	time.Sleep(5 * time.Second)

	if err := vm.WaitForSSH(ctx, testVMName, 5*time.Minute); err != nil {
		errorf("Failed to wait for SSH: %v", err)
		return 1
	}

	log("Test VM created successfully")

	// Get main repo path for mounts
	mainRepo := os.Getenv("BLOUD_MAIN_REPO")
	if mainRepo == "" {
		home, _ := os.UserHomeDir()
		mainRepo = filepath.Join(home, "Projects", "bloud-v3")
	}

	// Create mount directories and mount filesystems
	log("Mounting shared directories...")
	_, _ = vm.Exec(testVMName, fmt.Sprintf("sudo mkdir -p %s %s/.git /tmp/lima", projectRoot, mainRepo))

	mounts := []vm.Mount{
		{Tag: "mount0", MountPath: projectRoot},
		{Tag: "mount1", MountPath: mainRepo + "/.git", ReadOnly: true},
		{Tag: "mount2", MountPath: "/tmp/lima"},
	}
	if err := vm.MountFilesystems(testVMName, mounts); err != nil {
		warn(fmt.Sprintf("Mount warning: %v", err))
	}

	// Fix git safe.directory
	_, _ = vm.Exec(testVMName, "sudo git config --system --add safe.directory '*'")
	_, _ = vm.Exec(testVMName, fmt.Sprintf("sudo git config --system --add safe.directory '%s'", projectRoot))
	_, _ = vm.Exec(testVMName, fmt.Sprintf("sudo git config --system --add safe.directory '%s'", mainRepo))

	// Build host-agent binary BEFORE nixos-rebuild so prestart/poststart hooks work
	// NOTE: In production, this should be a systemd service dependency (bloud-host-agent.service)
	// that ensures the binary exists before app services start. For dev, we build it here.
	// We use nix-shell to get Go because the base NixOS image doesn't have Go installed yet
	// (it's only added after nixos-rebuild switch applies our configuration).
	log("Building host-agent binary (required for service hooks)...")
	buildScript := fmt.Sprintf(`
		set -e
		LOCAL_SRC="/tmp/bloud-test-src"
		rm -rf "$LOCAL_SRC"
		mkdir -p "$LOCAL_SRC"
		cp -r "%s/services/host-agent" "$LOCAL_SRC/"
		cp -r "%s/apps" "$LOCAL_SRC/"
		# Fix go.mod replace paths for VM directory structure
		sed -i 's|=> ../../apps|=> ../apps|g' "$LOCAL_SRC/host-agent/go.mod" 2>/dev/null || true
		sed -i 's|=> ../services/host-agent|=> ../host-agent|g' "$LOCAL_SRC/apps/go.mod" 2>/dev/null || true
		cd "$LOCAL_SRC/host-agent"
		# Use nix-shell to get Go temporarily (base image doesn't have Go yet)
		nix-shell -p go --run "go build -o /tmp/host-agent-test ./cmd/host-agent"
		echo "Host-agent binary built at /tmp/host-agent-test"
	`, projectRoot, projectRoot)
	if err := vm.ExecStream(testVMName, buildScript); err != nil {
		errorf("Failed to build host-agent binary: %v", err)
		return 1
	}

	// Apply NixOS configuration
	log("Applying NixOS configuration...")
	if err := vm.ExecStream(testVMName, fmt.Sprintf("sudo nixos-rebuild switch --flake %s#vm-test --impure", projectRoot)); err != nil {
		errorf("Failed to apply NixOS config: %v", err)
		return 1
	}

	// Fix home directory ownership
	_, _ = vm.Exec(testVMName, "sudo chown -R bloud:users /home/bloud/.local")

	// Start port forwarding
	log("Starting port forwarding (test ports)...")
	if _, err := vm.StartPortForwarding(testVMName, testPorts); err != nil {
		warn(fmt.Sprintf("Port forwarding warning: %v", err))
	}
	time.Sleep(2 * time.Second)

	// Start test services
	log("Starting test services...")
	if _, err := vm.Exec(testVMName, fmt.Sprintf("bash %s/lima/start-test.sh '%s'", projectRoot, projectRoot)); err != nil {
		if !strings.Contains(err.Error(), "already running") {
			errorf("Failed to start test services: %v", err)
			return 1
		}
	}

	fmt.Println()
	fmt.Println("Test VM is ready!")
	fmt.Println()
	fmt.Println("Test URLs:")
	fmt.Println("  http://localhost:8081     Web UI (via Traefik)")
	fmt.Println("  http://localhost:3001     Go API")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  ./bloud test logs     View server output")
	fmt.Println("  ./bloud test attach   Attach to tmux session")
	fmt.Println("  ./bloud test stop     Stop and destroy test VM")

	return 0
}

func testStop() int {
	if !vm.Exists(testVMName) {
		log("Test VM does not exist")
		return 0
	}

	log("Stopping test services...")

	// Kill port forwarding first
	_ = vm.KillPortForwarding(testVMName, testPorts[0].LocalPort)

	// If VM is running, kill tmux session
	if vm.IsRunning(testVMName) {
		_, _ = vm.Exec(testVMName, fmt.Sprintf("tmux kill-session -t %s 2>/dev/null || true", testTmuxSession))
		_, _ = vm.Exec(testVMName, `pkill -f "air" 2>/dev/null || true; pkill -f "vite" 2>/dev/null || true`)
	}

	// Delete VM (ephemeral)
	log("Destroying test VM...")
	if err := vm.Delete(testVMName); err != nil {
		errorf("Failed to delete VM: %v", err)
		return 1
	}

	// Clean up temp directory
	if err := os.RemoveAll("/tmp/lima-test"); err != nil {
		warn(fmt.Sprintf("Could not clean up temp dir: %v", err))
	}

	log("Test VM destroyed")
	return 0
}

func testStatus() int {
	fmt.Println()
	log("Test VM Status")
	fmt.Println()

	// Check if VM exists
	if !vm.Exists(testVMName) {
		fmt.Printf("  VM:           %sNot created%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Run './bloud test start' to create the test VM")
		return 0
	}

	// Check if VM is running
	if !vm.IsRunning(testVMName) {
		fmt.Printf("  VM:           %sStopped%s\n", colorYellow, colorReset)
		fmt.Println()
		fmt.Println("  Run './bloud test start' to create a fresh test VM")
		return 0
	}

	fmt.Printf("  VM:           %sRunning%s\n", colorGreen, colorReset)

	// Check tmux session
	output, err := vm.Exec(testVMName, fmt.Sprintf("tmux has-session -t %s 2>/dev/null && echo running || echo stopped", testTmuxSession))
	if err == nil && strings.TrimSpace(output) == "running" {
		fmt.Printf("  Tmux Session: %sRunning%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Tmux Session: %sNot running%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Run './bloud test start' to create a fresh test VM")
		return 0
	}

	// Check host-agent on test port
	output, _ = vm.Exec(testVMName, `curl -s http://localhost:3001/api/health 2>/dev/null`)
	if strings.Contains(output, "ok") {
		fmt.Printf("  Host Agent:   %sRunning%s (http://localhost:3001)\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Host Agent:   %sStarting...%s\n", colorYellow, colorReset)
	}

	// Check web UI on test port
	output, _ = vm.Exec(testVMName, `curl -s http://localhost:8081 2>/dev/null`)
	if strings.Contains(output, "html") {
		fmt.Printf("  Web UI:       %sRunning%s (http://localhost:8081)\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Web UI:       %sStarting...%s\n", colorYellow, colorReset)
	}

	// Check port forwarding
	if isTestPortForwardingRunning() {
		fmt.Printf("  Port Forward: %sActive%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Port Forward: %sNot running%s\n", colorRed, colorReset)
	}

	fmt.Println()
	return 0
}

func testLogs() int {
	if !vm.IsRunning(testVMName) {
		errorf("Test VM is not running. Start with: ./bloud test start")
		return 1
	}

	// Check if tmux session exists
	output, err := vm.Exec(testVMName, fmt.Sprintf("tmux has-session -t %s 2>/dev/null && echo running || echo stopped", testTmuxSession))
	if err != nil || strings.TrimSpace(output) != "running" {
		errorf("Test services not running. Start with: ./bloud test start")
		return 1
	}

	log("Capturing output from test tmux...")
	fmt.Println()

	fmt.Printf("%s=== Go API (port 3001) ===%s\n", colorCyan, colorReset)
	output, _ = vm.Exec(testVMName, fmt.Sprintf("tmux capture-pane -t %s:test.0 -p -S -50", testTmuxSession))
	fmt.Println(output)

	fmt.Printf("%s=== Vite (port 5174) ===%s\n", colorCyan, colorReset)
	output, _ = vm.Exec(testVMName, fmt.Sprintf("tmux capture-pane -t %s:test.1 -p -S -50", testTmuxSession))
	fmt.Println(output)

	return 0
}

func testAttach() int {
	if !vm.IsRunning(testVMName) {
		errorf("Test VM is not running. Start with: ./bloud test start")
		return 1
	}

	// Check if tmux session exists
	output, err := vm.Exec(testVMName, fmt.Sprintf("tmux has-session -t %s 2>/dev/null && echo running || echo stopped", testTmuxSession))
	if err != nil || strings.TrimSpace(output) != "running" {
		errorf("Test services not running. Start with: ./bloud test start")
		return 1
	}

	log("Attaching to test session (Ctrl-B D to detach)...")
	if err := vm.InteractiveShell(testVMName, fmt.Sprintf("tmux attach -t %s", testTmuxSession)); err != nil {
		errorf("Failed to attach: %v", err)
		return 1
	}

	return 0
}

func testShell(args []string) int {
	if !vm.IsRunning(testVMName) {
		errorf("Test VM is not running. Start with: ./bloud test start")
		return 1
	}

	var command string
	if len(args) > 0 {
		command = strings.Join(args, " ")
	}

	if err := vm.InteractiveShell(testVMName, command); err != nil {
		if command == "" {
			return 0
		}
		errorf("Command failed: %v", err)
		return 1
	}

	return 0
}

func testRebuild() int {
	if !vm.IsRunning(testVMName) {
		errorf("Test VM is not running. Start with: ./bloud test start")
		return 1
	}

	projectRoot, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	log("Rebuilding NixOS configuration for test VM...")
	cmd := fmt.Sprintf("sudo nixos-rebuild switch --flake %s#vm-test --impure", projectRoot)
	if err := vm.ExecStream(testVMName, cmd); err != nil {
		errorf("Rebuild failed: %v", err)
		return 1
	}

	return 0
}

func isTestPortForwardingRunning() bool {
	pattern := fmt.Sprintf("ssh.*-L %d:localhost:%d.*bloud@", testPorts[0].LocalPort, testPorts[0].RemotePort)
	cmd := localExec("pgrep", "-f", pattern)
	output, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(output)) != ""
}

func testServices() int {
	if !vm.IsRunning(testVMName) {
		errorf("Test VM is not running. Start with: ./bloud test start")
		return 1
	}

	output, err := vm.Exec(testVMName, "systemctl --user list-units 'podman-*' --all --no-pager")
	if err != nil {
		errorf("Failed to get services: %v", err)
		return 1
	}

	fmt.Println(output)
	return 0
}
