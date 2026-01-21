package vm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	vmUser     = "bloud"
	vmPassword = "bloud"
)

// sshMethod tracks which SSH method is available
type sshMethod int

const (
	sshMethodUnknown sshMethod = iota
	sshMethodKey               // SSH key auth via Lima's config
	sshMethodPassword          // Password auth via sshpass
)

// cachedSSHMethod stores the detected SSH method to avoid repeated checks
var cachedSSHMethod = sshMethodUnknown

// detectSSHMethod determines the best available SSH method
func detectSSHMethod(vmName string) sshMethod {
	if cachedSSHMethod != sshMethodUnknown {
		return cachedSSHMethod
	}

	port, err := GetSSHPort(vmName)
	if err != nil || port == 0 {
		return sshMethodUnknown
	}

	// Try key-based SSH first (preferred - no sshpass needed)
	if tryKeySSH(port) {
		cachedSSHMethod = sshMethodKey
		return sshMethodKey
	}

	// Fall back to password auth if sshpass is available
	if hasSshpass() && tryPasswordSSH(port) {
		cachedSSHMethod = sshMethodPassword
		return sshMethodPassword
	}

	return sshMethodUnknown
}

// tryKeySSH tests if key-based SSH works
func tryKeySSH(port int) bool {
	cmd := exec.Command("ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes", // Fail instead of prompting for password
		"-o", "ConnectTimeout=2",
		"-o", "LogLevel=ERROR",
		"-p", strconv.Itoa(port),
		vmUser+"@127.0.0.1",
		"true",
	)
	return cmd.Run() == nil
}

// tryPasswordSSH tests if password-based SSH works
func tryPasswordSSH(port int) bool {
	cmd := exec.Command("sshpass", "-p", vmPassword,
		"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "PreferredAuthentications=password",
		"-o", "PubkeyAuthentication=no",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=2",
		"-p", strconv.Itoa(port),
		vmUser+"@127.0.0.1",
		"true",
	)
	return cmd.Run() == nil
}

// hasSshpass checks if sshpass is installed
func hasSshpass() bool {
	_, err := exec.LookPath("sshpass")
	return err == nil
}

// ResetSSHMethod clears the cached SSH method (useful after VM restart)
func ResetSSHMethod() {
	cachedSSHMethod = sshMethodUnknown
}

// VMStatus represents the status of a Lima VM
type VMStatus string

const (
	StatusRunning VMStatus = "Running"
	StatusStopped VMStatus = "Stopped"
	StatusUnknown VMStatus = "Unknown"
)

// Mount represents a 9p filesystem mount
type Mount struct {
	Tag       string
	MountPath string
	ReadOnly  bool
}

// PortForward represents a port to forward
type PortForward struct {
	LocalPort  int
	RemotePort int
}

// limaVM represents a Lima VM entry from limactl list --json
type limaVM struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	SSHLocalPort int    `json:"sshLocalPort"`
}

// GetStatus returns the current status of a VM
func GetStatus(vmName string) VMStatus {
	cmd := exec.Command("limactl", "list", "--format", "json")
	output, err := cmd.Output()
	if err != nil {
		return StatusUnknown
	}

	var vms []limaVM
	// limactl outputs one JSON object per line, not an array
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		var vm limaVM
		if err := json.Unmarshal(scanner.Bytes(), &vm); err != nil {
			continue
		}
		vms = append(vms, vm)
	}

	for _, vm := range vms {
		if vm.Name == vmName {
			switch vm.Status {
			case "Running":
				return StatusRunning
			case "Stopped":
				return StatusStopped
			default:
				return StatusUnknown
			}
		}
	}

	return StatusUnknown
}

// Exists checks if a VM exists
func Exists(vmName string) bool {
	status := GetStatus(vmName)
	return status != StatusUnknown
}

// IsRunning checks if a VM is currently running
func IsRunning(vmName string) bool {
	return GetStatus(vmName) == StatusRunning
}

// GetSSHPort returns the SSH port for a running VM
func GetSSHPort(vmName string) (int, error) {
	cmd := exec.Command("limactl", "list", "--format", "json")
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to list VMs: %w", err)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		var vm limaVM
		if err := json.Unmarshal(scanner.Bytes(), &vm); err != nil {
			continue
		}
		if vm.Name == vmName {
			if vm.SSHLocalPort == 0 {
				return 0, fmt.Errorf("VM %s has no SSH port", vmName)
			}
			return vm.SSHLocalPort, nil
		}
	}

	return 0, fmt.Errorf("VM %s not found", vmName)
}

// Start starts an existing stopped VM
func Start(vmName string) error {
	if !Exists(vmName) {
		return fmt.Errorf("VM %s does not exist", vmName)
	}

	if IsRunning(vmName) {
		return nil // Already running
	}

	// Start in background - Lima's SSH check won't work with password auth
	cmd := exec.Command("limactl", "start", vmName, "--tty=false")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	// We'll kill this process after SSH is ready
	go func() {
		_ = cmd.Wait()
	}()

	return nil
}

// Create creates and starts a new VM from a config file
func Create(vmName, configPath string) error {
	if Exists(vmName) {
		return fmt.Errorf("VM %s already exists", vmName)
	}

	cmd := exec.Command("limactl", "start", "--name="+vmName, configPath, "--tty=false")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	// We'll kill this process after SSH is ready
	go func() {
		_ = cmd.Wait()
	}()

	return nil
}

// Stop stops a running VM
func Stop(vmName string) error {
	if !IsRunning(vmName) {
		return nil // Already stopped
	}

	cmd := exec.Command("limactl", "stop", vmName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to stop VM: %w", err)
	}

	return nil
}

// Delete deletes a VM (stops it first if running)
func Delete(vmName string) error {
	if !Exists(vmName) {
		return nil // Already doesn't exist
	}

	// Stop first if running
	if IsRunning(vmName) {
		if err := Stop(vmName); err != nil {
			return err
		}
	}

	cmd := exec.Command("limactl", "delete", vmName, "--force")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete VM: %w", err)
	}

	return nil
}

// WaitForSSH waits for SSH to become available on a VM
func WaitForSSH(ctx context.Context, vmName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	attempt := 0

	for time.Now().Before(deadline) {
		attempt++
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		port, err := GetSSHPort(vmName)
		if err != nil || port == 0 {
			fmt.Printf("\r  Waiting for SSH... (%d)", attempt)
			time.Sleep(2 * time.Second)
			continue
		}

		// Try to connect
		if testSSH(port) {
			fmt.Println()
			return nil
		}

		fmt.Printf("\r  Waiting for SSH... (%d)", attempt)
		time.Sleep(2 * time.Second)
	}

	fmt.Println()
	return fmt.Errorf("SSH not available after %v", timeout)
}

// testSSH attempts an SSH connection to verify it works
func testSSH(port int) bool {
	// Try key-based first
	if tryKeySSH(port) {
		return true
	}
	// Fall back to password if sshpass available
	if hasSshpass() {
		return tryPasswordSSH(port)
	}
	return false
}

// Exec runs a command in the VM and returns the output
func Exec(vmName string, command string) (string, error) {
	port, err := GetSSHPort(vmName)
	if err != nil {
		return "", err
	}

	cmd := buildSSHCommand(vmName, port, false, command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("command failed: %w", err)
	}

	return string(output), nil
}

// buildSSHCommand creates an SSH command using the best available auth method
func buildSSHCommand(vmName string, port int, interactive bool, command string) *exec.Cmd {
	method := detectSSHMethod(vmName)

	var args []string

	if method == sshMethodPassword && hasSshpass() {
		// Use sshpass for password auth
		args = append(args, "-p", vmPassword, "ssh")
		args = append(args,
			"-o", "PreferredAuthentications=password",
			"-o", "PubkeyAuthentication=no",
		)
	} else {
		// Use key-based auth (default)
		args = []string{}
	}

	// Common SSH options
	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
	}

	if interactive {
		sshArgs = append(sshArgs, "-t")
	}

	sshArgs = append(sshArgs,
		"-p", strconv.Itoa(port),
		vmUser+"@127.0.0.1",
	)

	if command != "" {
		sshArgs = append(sshArgs, command)
	}

	if method == sshMethodPassword && hasSshpass() {
		args = append(args, sshArgs...)
		return exec.Command("sshpass", args...)
	}

	return exec.Command("ssh", sshArgs...)
}

// ExecStream runs a command in the VM and streams output to stdout/stderr
func ExecStream(vmName string, command string) error {
	port, err := GetSSHPort(vmName)
	if err != nil {
		return err
	}

	cmd := buildSSHCommand(vmName, port, false, command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// InteractiveShell opens an interactive SSH session to the VM
func InteractiveShell(vmName string, command string) error {
	port, err := GetSSHPort(vmName)
	if err != nil {
		return err
	}

	// For interactive, if no command specified, use bash
	if command == "" {
		command = "bash"
	}

	cmd := buildSSHCommand(vmName, port, true, command)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// MountFilesystems mounts 9p filesystems in the VM
func MountFilesystems(vmName string, mounts []Mount) error {
	for _, m := range mounts {
		// Create mount point if it doesn't exist
		mkdirCmd := fmt.Sprintf("sudo mkdir -p %s", m.MountPath)
		if _, err := Exec(vmName, mkdirCmd); err != nil {
			return fmt.Errorf("failed to create mount point %s: %w", m.MountPath, err)
		}

		// Build mount options
		opts := "trans=virtio,version=9p2000.L,msize=131072,cache=none,access=any"
		if m.ReadOnly {
			opts += ",ro"
		}

		// Mount the filesystem
		mountCmd := fmt.Sprintf("sudo mount -t 9p -o %s %s %s 2>/dev/null || true", opts, m.Tag, m.MountPath)
		if _, err := Exec(vmName, mountCmd); err != nil {
			return fmt.Errorf("failed to mount %s: %w", m.Tag, err)
		}
	}

	return nil
}

// StartPortForwarding starts SSH port forwarding in the background
// Returns a function to stop the forwarding
func StartPortForwarding(vmName string, ports []PortForward) (func(), error) {
	port, err := GetSSHPort(vmName)
	if err != nil {
		return nil, err
	}

	method := detectSSHMethod(vmName)

	var args []string
	var cmdName string

	if method == sshMethodPassword && hasSshpass() {
		cmdName = "sshpass"
		args = []string{"-p", vmPassword, "ssh",
			"-o", "PreferredAuthentications=password",
			"-o", "PubkeyAuthentication=no",
		}
	} else {
		cmdName = "ssh"
		args = []string{}
	}

	args = append(args,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ServerAliveInterval=60",
		"-o", "ExitOnForwardFailure=yes",
		"-N",
	)

	for _, p := range ports {
		args = append(args, "-L", fmt.Sprintf("%d:localhost:%d", p.LocalPort, p.RemotePort))
	}

	args = append(args, "-p", strconv.Itoa(port), vmUser+"@127.0.0.1")

	cmd := exec.Command(cmdName, args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start port forwarding: %w", err)
	}

	stop := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = cmd.Wait()
		}
	}

	return stop, nil
}

// KillPortForwarding kills any existing port forwarding for a VM
func KillPortForwarding(vmName string, firstPort int) error {
	// Use pkill to find and kill the SSH process
	pattern := fmt.Sprintf("ssh.*-L %d:localhost:%d.*%s@", firstPort, firstPort, vmUser)
	cmd := exec.Command("pkill", "-f", pattern)
	_ = cmd.Run() // Ignore errors - process might not exist
	return nil
}

// EnsureRunning ensures a VM is running, starting or creating it if necessary
func EnsureRunning(ctx context.Context, vmName, configPath string) error {
	status := GetStatus(vmName)

	switch status {
	case StatusRunning:
		return nil // Already running

	case StatusStopped:
		fmt.Printf("==> Starting VM '%s'...\n", vmName)
		if err := Start(vmName); err != nil {
			return err
		}

	case StatusUnknown:
		fmt.Printf("==> Creating VM '%s'...\n", vmName)
		if err := Create(vmName, configPath); err != nil {
			return err
		}
	}

	// Wait for SSH
	fmt.Println("==> Waiting for VM to boot...")
	time.Sleep(5 * time.Second) // Give QEMU time to start

	if err := WaitForSSH(ctx, vmName, 2*time.Minute); err != nil {
		return err
	}

	return nil
}

// EnsureSshpass checks that sshpass is installed (only needed for password auth fallback)
func EnsureSshpass() error {
	if hasSshpass() {
		return nil
	}
	return fmt.Errorf("sshpass not installed (only needed if SSH key auth fails). Install with: brew install hudochenkov/sshpass/sshpass")
}

// EnsureSSHAvailable checks that some form of SSH auth will work
// Returns an error only if neither key auth nor password auth is possible
func EnsureSSHAvailable() error {
	// Check if SSH client is available
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh client not found")
	}

	// sshpass is optional - we prefer key auth
	// We'll detect the right method when we actually connect
	return nil
}

// SnapshotCreate creates a new snapshot of a VM
// The VM must be stopped before creating a snapshot
func SnapshotCreate(vmName, snapshotName string) error {
	if IsRunning(vmName) {
		return fmt.Errorf("VM must be stopped before creating a snapshot")
	}

	cmd := exec.Command("limactl", "snapshot", "create", vmName, "--tag", snapshotName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create snapshot: %s: %w", string(output), err)
	}

	return nil
}

// SnapshotApply restores a VM to a snapshot state
// The VM must be stopped before applying a snapshot
func SnapshotApply(vmName, snapshotName string) error {
	if IsRunning(vmName) {
		return fmt.Errorf("VM must be stopped before applying a snapshot")
	}

	cmd := exec.Command("limactl", "snapshot", "apply", vmName, "--tag", snapshotName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to apply snapshot: %s: %w", string(output), err)
	}

	return nil
}

// SnapshotDelete deletes a snapshot from a VM
func SnapshotDelete(vmName, snapshotName string) error {
	cmd := exec.Command("limactl", "snapshot", "delete", vmName, "--tag", snapshotName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete snapshot: %s: %w", string(output), err)
	}

	return nil
}

// SnapshotList lists all snapshots for a VM
func SnapshotList(vmName string) ([]string, error) {
	cmd := exec.Command("limactl", "snapshot", "list", vmName, "--quiet")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// No snapshots returns error, treat as empty list
		return []string{}, nil
	}

	var snapshots []string
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			snapshots = append(snapshots, line)
		}
	}

	return snapshots, nil
}

// SnapshotExists checks if a snapshot exists for a VM
func SnapshotExists(vmName, snapshotName string) bool {
	snapshots, err := SnapshotList(vmName)
	if err != nil {
		return false
	}

	for _, s := range snapshots {
		if s == snapshotName {
			return true
		}
	}

	return false
}
