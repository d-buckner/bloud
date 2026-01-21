package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"codeberg.org/d-buckner/bloud/cli/vm"
)

const (
	testVMName       = "bloud-test"
	testTmuxSession  = "bloud-test"
	testSnapshotName = "ready" // Snapshot taken after NixOS is configured
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
		mainRepo = filepath.Join(home, "Projects", "bloud")
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

	projectRoot, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	// Run pre-flight checks before attempting to start
	preflightResult := vm.RunPreflightChecks(projectRoot)
	if preflightResult.HasErrors() {
		vm.PrintPreflightErrors(preflightResult)
		return 1
	}

	// Create temp directory for test VM
	if err := os.MkdirAll("/tmp/lima-test", 0755); err != nil {
		warn(fmt.Sprintf("Could not create temp dir: %v", err))
	}

	// Check if we can do a warm start (VM exists with snapshot)
	if vm.Exists(testVMName) && vm.SnapshotExists(testVMName, testSnapshotName) {
		return testWarmStart(ctx, projectRoot)
	}

	// Cold start: create VM from scratch and create snapshot
	return testColdStart(ctx, projectRoot)
}

// testColdStart creates a fresh VM, configures it, and creates a snapshot for future warm starts
func testColdStart(ctx context.Context, projectRoot string) int {
	configPath, err := getTestConfigPath()
	if err != nil {
		errorf("Could not generate config: %v", err)
		return 1
	}

	// Delete existing VM if it exists (starting fresh)
	if vm.Exists(testVMName) {
		log("Removing existing test VM (no snapshot found)...")
		if err := vm.Delete(testVMName); err != nil {
			errorf("Failed to delete existing VM: %v", err)
			return 1
		}
	}

	// Create fresh VM
	log(fmt.Sprintf("Creating fresh test VM '%s' (cold start)...", testVMName))
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
		mainRepo = filepath.Join(home, "Projects", "bloud")
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

	// Initialize secrets BEFORE nixos-rebuild so NixOS can read them
	log("Initializing secrets...")
	if err := vm.ExecStream(testVMName, "/tmp/host-agent-test init-secrets /home/bloud/.local/share/bloud"); err != nil {
		errorf("Failed to initialize secrets: %v", err)
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

	// Create snapshot for future warm starts
	log("Creating snapshot for future warm starts...")
	if err := vm.Stop(testVMName); err != nil {
		warn(fmt.Sprintf("Could not stop VM for snapshot: %v", err))
	} else {
		if err := vm.SnapshotCreate(testVMName, testSnapshotName); err != nil {
			warn(fmt.Sprintf("Could not create snapshot: %v", err))
		} else {
			log("Snapshot created successfully!")
		}
		// Start VM again
		if err := vm.Start(testVMName); err != nil {
			errorf("Failed to start VM after snapshot: %v", err)
			return 1
		}
		time.Sleep(3 * time.Second)
		if err := vm.WaitForSSH(ctx, testVMName, 2*time.Minute); err != nil {
			errorf("Failed to wait for SSH after snapshot: %v", err)
			return 1
		}
		// Re-mount filesystems after restart
		if err := vm.MountFilesystems(testVMName, mounts); err != nil {
			warn(fmt.Sprintf("Mount warning after restart: %v", err))
		}
	}

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

	printTestReady()
	return 0
}

// testWarmStart restores from snapshot for fast startup
func testWarmStart(ctx context.Context, projectRoot string) int {
	log("Warm start: restoring from snapshot...")

	// Stop VM if running (required for snapshot apply)
	if vm.IsRunning(testVMName) {
		log("Stopping VM to apply snapshot...")
		_ = vm.KillPortForwarding(testVMName, testPorts[0].LocalPort)
		if err := vm.Stop(testVMName); err != nil {
			errorf("Failed to stop VM: %v", err)
			return 1
		}
	}

	// Apply snapshot
	log("Applying snapshot...")
	if err := vm.SnapshotApply(testVMName, testSnapshotName); err != nil {
		errorf("Failed to apply snapshot: %v", err)
		return 1
	}

	// Start VM
	log("Starting VM from snapshot...")
	if err := vm.Start(testVMName); err != nil {
		errorf("Failed to start VM: %v", err)
		return 1
	}

	// Wait for SSH (should be fast since VM is already configured)
	time.Sleep(3 * time.Second)
	if err := vm.WaitForSSH(ctx, testVMName, 2*time.Minute); err != nil {
		errorf("Failed to wait for SSH: %v", err)
		return 1
	}

	// Get main repo path for mounts
	mainRepo := os.Getenv("BLOUD_MAIN_REPO")
	if mainRepo == "" {
		home, _ := os.UserHomeDir()
		mainRepo = filepath.Join(home, "Projects", "bloud")
	}

	// Re-mount filesystems (9p mounts don't persist across reboot)
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

	// Start port forwarding
	log("Starting port forwarding (test ports)...")
	if _, err := vm.StartPortForwarding(testVMName, testPorts); err != nil {
		warn(fmt.Sprintf("Port forwarding warning: %v", err))
	}
	time.Sleep(1 * time.Second)

	// Start test services
	log("Starting test services...")
	if _, err := vm.Exec(testVMName, fmt.Sprintf("bash %s/lima/start-test.sh '%s'", projectRoot, projectRoot)); err != nil {
		if !strings.Contains(err.Error(), "already running") {
			errorf("Failed to start test services: %v", err)
			return 1
		}
	}

	printTestReady()
	return 0
}

func printTestReady() {
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
	fmt.Println("  ./bloud test reset    Reset to clean state (fast)")
	fmt.Println("  ./bloud test stop     Stop test VM (preserves snapshot)")
}

func testStop() int {
	if !vm.Exists(testVMName) {
		log("Test VM does not exist")
		return 0
	}

	log("Stopping test services...")

	// Kill port forwarding first
	_ = vm.KillPortForwarding(testVMName, testPorts[0].LocalPort)

	// If VM is running, kill tmux session and stop VM
	if vm.IsRunning(testVMName) {
		_, _ = vm.Exec(testVMName, fmt.Sprintf("tmux kill-session -t %s 2>/dev/null || true", testTmuxSession))
		_, _ = vm.Exec(testVMName, `pkill -f "air" 2>/dev/null || true; pkill -f "vite" 2>/dev/null || true`)

		log("Stopping test VM (preserving snapshot for fast restart)...")
		if err := vm.Stop(testVMName); err != nil {
			errorf("Failed to stop VM: %v", err)
			return 1
		}
	}

	hasSnapshot := vm.SnapshotExists(testVMName, testSnapshotName)
	if hasSnapshot {
		log("Test VM stopped. Snapshot preserved for fast restart.")
		fmt.Println()
		fmt.Println("Next './bloud test start' will be fast (warm start from snapshot)")
		fmt.Println("To fully destroy: './bloud test destroy'")
	} else {
		log("Test VM stopped (no snapshot)")
	}

	return 0
}

// testReset resets the test environment to clean state (from snapshot)
func testReset() int {
	if !vm.Exists(testVMName) {
		errorf("Test VM does not exist. Run './bloud test start' first.")
		return 1
	}

	if !vm.SnapshotExists(testVMName, testSnapshotName) {
		errorf("No snapshot found. Run './bloud test start' to create one.")
		return 1
	}

	ctx := context.Background()

	projectRoot, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	log("Resetting test environment to clean state...")

	// Kill port forwarding
	_ = vm.KillPortForwarding(testVMName, testPorts[0].LocalPort)

	// Stop VM if running
	if vm.IsRunning(testVMName) {
		_, _ = vm.Exec(testVMName, fmt.Sprintf("tmux kill-session -t %s 2>/dev/null || true", testTmuxSession))
		if err := vm.Stop(testVMName); err != nil {
			errorf("Failed to stop VM: %v", err)
			return 1
		}
	}

	// Apply snapshot
	log("Restoring from snapshot...")
	if err := vm.SnapshotApply(testVMName, testSnapshotName); err != nil {
		errorf("Failed to apply snapshot: %v", err)
		return 1
	}

	// Start VM
	log("Starting VM...")
	if err := vm.Start(testVMName); err != nil {
		errorf("Failed to start VM: %v", err)
		return 1
	}

	time.Sleep(3 * time.Second)
	if err := vm.WaitForSSH(ctx, testVMName, 2*time.Minute); err != nil {
		errorf("Failed to wait for SSH: %v", err)
		return 1
	}

	// Get main repo path for mounts
	mainRepo := os.Getenv("BLOUD_MAIN_REPO")
	if mainRepo == "" {
		home, _ := os.UserHomeDir()
		mainRepo = filepath.Join(home, "Projects", "bloud")
	}

	// Re-mount filesystems
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

	// Start port forwarding
	log("Starting port forwarding...")
	if _, err := vm.StartPortForwarding(testVMName, testPorts); err != nil {
		warn(fmt.Sprintf("Port forwarding warning: %v", err))
	}
	time.Sleep(1 * time.Second)

	// Start test services
	log("Starting test services...")
	if _, err := vm.Exec(testVMName, fmt.Sprintf("bash %s/lima/start-test.sh '%s'", projectRoot, projectRoot)); err != nil {
		if !strings.Contains(err.Error(), "already running") {
			errorf("Failed to start test services: %v", err)
			return 1
		}
	}

	fmt.Println()
	log("Test environment reset to clean state!")
	return 0
}

// testDestroy completely removes the test VM and snapshot
func testDestroy() int {
	if !vm.Exists(testVMName) {
		log("Test VM does not exist")
		return 0
	}

	log("Destroying test VM completely...")

	// Kill port forwarding first
	_ = vm.KillPortForwarding(testVMName, testPorts[0].LocalPort)

	// If VM is running, kill tmux session
	if vm.IsRunning(testVMName) {
		_, _ = vm.Exec(testVMName, fmt.Sprintf("tmux kill-session -t %s 2>/dev/null || true", testTmuxSession))
		_, _ = vm.Exec(testVMName, `pkill -f "air" 2>/dev/null || true; pkill -f "vite" 2>/dev/null || true`)
	}

	// Delete VM (this also removes snapshots)
	if err := vm.Delete(testVMName); err != nil {
		errorf("Failed to delete VM: %v", err)
		return 1
	}

	// Clean up temp directory
	if err := os.RemoveAll("/tmp/lima-test"); err != nil {
		warn(fmt.Sprintf("Could not clean up temp dir: %v", err))
	}

	log("Test VM destroyed completely")
	return 0
}

func testStatus() int {
	fmt.Println()
	log("Test VM Status")
	fmt.Println()

	// Check if VM exists
	if !vm.Exists(testVMName) {
		fmt.Printf("  VM:           %sNot created%s\n", colorRed, colorReset)
		fmt.Printf("  Snapshot:     %sNone%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Run './bloud test start' to create the test VM (cold start)")
		return 0
	}

	// Check snapshot status
	hasSnapshot := vm.SnapshotExists(testVMName, testSnapshotName)
	if hasSnapshot {
		fmt.Printf("  Snapshot:     %sAvailable%s (fast restart enabled)\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Snapshot:     %sNone%s\n", colorYellow, colorReset)
	}

	// Check if VM is running
	if !vm.IsRunning(testVMName) {
		fmt.Printf("  VM:           %sStopped%s\n", colorYellow, colorReset)
		fmt.Println()
		if hasSnapshot {
			fmt.Println("  Run './bloud test start' for warm start from snapshot")
		} else {
			fmt.Println("  Run './bloud test start' for cold start")
		}
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

func testInstall(args []string) int {
	if len(args) < 1 {
		errorf("Usage: ./bloud test install <app-name>")
		return 1
	}

	appName := args[0]
	return installApp(testVMName, 3001, appName)
}

func testUninstall(args []string) int {
	if len(args) < 1 {
		errorf("Usage: ./bloud test uninstall <app-name>")
		return 1
	}

	appName := args[0]
	return uninstallApp(testVMName, 3001, appName)
}
