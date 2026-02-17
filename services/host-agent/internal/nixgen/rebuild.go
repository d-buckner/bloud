package nixgen

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// Rebuilder handles nixos-rebuild operations
type Rebuilder struct {
	flakePath string
	hostname  string
	logger    *slog.Logger
	dryRun    bool
	impure    bool // Allow impure evaluation (for runtime-generated config)
	useSudo   bool // Run nixos-rebuild with sudo
}

// NewRebuilder creates a nixos-rebuild wrapper
func NewRebuilder(flakePath, hostname string, logger *slog.Logger) *Rebuilder {
	return &Rebuilder{
		flakePath: flakePath,
		hostname:  hostname,
		logger:    logger,
		dryRun:    false,
		impure:    true, // Enable by default for development
		useSudo:   true, // nixos-rebuild switch requires root
	}
}

// RebuildResult contains the result of a rebuild operation
type RebuildResult struct {
	Success      bool
	Output       string
	ErrorMessage string
	Duration     time.Duration
	Changes      []string
}

// Switch performs a nixos-rebuild switch
func (r *Rebuilder) Switch(ctx context.Context) (*RebuildResult, error) {
	start := time.Now()

	result := &RebuildResult{
		Changes: []string{},
	}

	args := []string{"switch"}

	if r.flakePath != "" {
		args = append(args, "--flake", fmt.Sprintf("%s#%s", r.flakePath, r.hostname))
	}

	if r.impure {
		args = append(args, "--impure")
	}

	if r.dryRun {
		args = append(args, "--dry-run")
	}

	r.logger.Info("running nixos-rebuild", "args", args, "sudo", r.useSudo)

	var cmd *exec.Cmd
	if r.useSudo {
		sudoArgs := append([]string{"nixos-rebuild"}, args...)
		cmd = exec.CommandContext(ctx, "sudo", sudoArgs...)
	} else {
		cmd = exec.CommandContext(ctx, "nixos-rebuild", args...)
	}

	// Capture both stdout and stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start nixos-rebuild: %w", err)
	}

	// Stream output
	var outputLines []string
	outputDone := make(chan struct{})

	go func() {
		defer close(outputDone)
		r.streamOutput(stdout, stderr, &outputLines, result)
	}()

	// Wait for command to complete
	cmdErr := cmd.Wait()
	<-outputDone

	result.Duration = time.Since(start)
	result.Output = strings.Join(outputLines, "\n")

	if cmdErr != nil {
		result.Success = false
		result.ErrorMessage = cmdErr.Error()
		r.logger.Error("nixos-rebuild failed",
			"error", cmdErr,
			"duration", result.Duration,
		)
		return result, nil
	}

	result.Success = true
	r.logger.Info("nixos-rebuild completed successfully",
		"duration", result.Duration,
		"changes", len(result.Changes),
	)

	return result, nil
}

// streamOutput reads and logs output from nixos-rebuild
func (r *Rebuilder) streamOutput(stdout, stderr io.Reader, outputLines *[]string, result *RebuildResult) {
	// Read stdout
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			*outputLines = append(*outputLines, line)
			r.parseOutputLine(line, result)
			r.logger.Debug("nixos-rebuild", "output", line)
		}
	}()

	// Read stderr
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		*outputLines = append(*outputLines, line)
		r.logger.Debug("nixos-rebuild", "error", line)
	}
}

// parseOutputLine extracts useful information from rebuild output
func (r *Rebuilder) parseOutputLine(line string, result *RebuildResult) {
	// Look for service changes
	if strings.Contains(line, "starting") {
		result.Changes = append(result.Changes, line)
	}
	if strings.Contains(line, "stopping") {
		result.Changes = append(result.Changes, line)
	}
	if strings.Contains(line, "restarting") {
		result.Changes = append(result.Changes, line)
	}
	if strings.Contains(line, "reloading") {
		result.Changes = append(result.Changes, line)
	}
}

// Test performs a nixos-rebuild test (doesn't change bootloader)
func (r *Rebuilder) Test(ctx context.Context) (*RebuildResult, error) {
	// Similar to Switch but uses 'test' instead of 'switch'
	start := time.Now()

	result := &RebuildResult{
		Changes: []string{},
	}

	args := []string{"test"}
	if r.flakePath != "" {
		args = append(args, "--flake", fmt.Sprintf("%s#%s", r.flakePath, r.hostname))
	}

	r.logger.Info("running nixos-rebuild test", "args", args, "sudo", r.useSudo)

	var cmd *exec.Cmd
	if r.useSudo {
		sudoArgs := append([]string{"nixos-rebuild"}, args...)
		cmd = exec.CommandContext(ctx, "sudo", sudoArgs...)
	} else {
		cmd = exec.CommandContext(ctx, "nixos-rebuild", args...)
	}
	output, err := cmd.CombinedOutput()

	result.Duration = time.Since(start)
	result.Output = string(output)

	if err != nil {
		result.Success = false
		result.ErrorMessage = err.Error()
		return result, nil
	}

	result.Success = true
	return result, nil
}

// DryRun performs a dry-run to preview changes
func (r *Rebuilder) DryRun(ctx context.Context) (*RebuildResult, error) {
	oldDryRun := r.dryRun
	r.dryRun = true
	defer func() { r.dryRun = oldDryRun }()

	return r.Switch(ctx)
}

// RebuildEvent represents a streaming event during rebuild
type RebuildEvent struct {
	Type    string `json:"type"`    // "output", "error", "complete"
	Message string `json:"message"`
	Success bool   `json:"success,omitempty"`
}

// SwitchStream performs a nixos-rebuild switch with streaming output
func (r *Rebuilder) SwitchStream(ctx context.Context, events chan<- RebuildEvent) {
	defer close(events)

	args := []string{"switch"}

	if r.flakePath != "" {
		args = append(args, "--flake", fmt.Sprintf("%s#%s", r.flakePath, r.hostname))
	}

	if r.impure {
		args = append(args, "--impure")
	}

	r.logger.Info("running nixos-rebuild (streaming)", "args", args, "sudo", r.useSudo)
	events <- RebuildEvent{Type: "output", Message: fmt.Sprintf("Running: nixos-rebuild %s", strings.Join(args, " "))}

	var cmd *exec.Cmd
	if r.useSudo {
		sudoArgs := append([]string{"nixos-rebuild"}, args...)
		cmd = exec.CommandContext(ctx, "sudo", sudoArgs...)
	} else {
		cmd = exec.CommandContext(ctx, "nixos-rebuild", args...)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		events <- RebuildEvent{Type: "error", Message: fmt.Sprintf("Failed to get stdout pipe: %v", err)}
		events <- RebuildEvent{Type: "complete", Success: false}
		return
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		events <- RebuildEvent{Type: "error", Message: fmt.Sprintf("Failed to get stderr pipe: %v", err)}
		events <- RebuildEvent{Type: "complete", Success: false}
		return
	}

	if err := cmd.Start(); err != nil {
		events <- RebuildEvent{Type: "error", Message: fmt.Sprintf("Failed to start: %v", err)}
		events <- RebuildEvent{Type: "complete", Success: false}
		return
	}

	// Stream stdout
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			events <- RebuildEvent{Type: "output", Message: scanner.Text()}
		}
	}()

	// Stream stderr
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			events <- RebuildEvent{Type: "output", Message: scanner.Text()}
		}
	}()

	// Wait for command to complete
	if err := cmd.Wait(); err != nil {
		events <- RebuildEvent{Type: "error", Message: fmt.Sprintf("Rebuild failed: %v", err)}
		events <- RebuildEvent{Type: "complete", Success: false}
		return
	}

	events <- RebuildEvent{Type: "complete", Success: true, Message: "Rebuild completed successfully"}
}

// Rollback rolls back to the previous generation
func (r *Rebuilder) Rollback(ctx context.Context) (*RebuildResult, error) {
	start := time.Now()

	result := &RebuildResult{}

	r.logger.Info("rolling back nixos configuration", "sudo", r.useSudo)

	var cmd *exec.Cmd
	if r.useSudo {
		cmd = exec.CommandContext(ctx, "sudo", "nixos-rebuild", "switch", "--rollback")
	} else {
		cmd = exec.CommandContext(ctx, "nixos-rebuild", "switch", "--rollback")
	}
	output, err := cmd.CombinedOutput()

	result.Duration = time.Since(start)
	result.Output = string(output)

	if err != nil {
		result.Success = false
		result.ErrorMessage = err.Error()
		r.logger.Error("rollback failed", "error", err)
		return result, nil
	}

	result.Success = true
	r.logger.Info("rollback completed successfully")

	return result, nil
}

// StopUserService stops a systemd user service for an app
func (r *Rebuilder) StopUserService(ctx context.Context, appName string) error {
	// Map app names to service names
	serviceName := fmt.Sprintf("podman-%s.service", appName)

	r.logger.Info("stopping user service", "service", serviceName)

	// Use machinectl to run systemctl in the user's session
	// This is the same approach used in bloud-user-services
	var cmd *exec.Cmd
	if r.useSudo {
		cmd = exec.CommandContext(ctx, "sudo", "machinectl", "shell", "bloud@",
			"/run/current-system/sw/bin/systemctl", "--user", "stop", serviceName)
	} else {
		cmd = exec.CommandContext(ctx, "systemctl", "--user", "stop", serviceName)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		r.logger.Warn("failed to stop service", "service", serviceName, "error", err, "output", string(output))
		return fmt.Errorf("failed to stop %s: %w", serviceName, err)
	}

	r.logger.Info("service stopped", "service", serviceName)
	return nil
}


// ReloadAndRestartApps reloads systemd user daemon and restarts all bloud apps
// This should be called after nixos-rebuild to:
// 1. Reload unit files (daemon-reload) so systemd sees new/changed services
// 2. Restart bloud-apps.target which restarts all apps (via PartOf=)
func (r *Rebuilder) ReloadAndRestartApps(ctx context.Context) error {
	r.logger.Info("reloading systemd user daemon and restarting apps")

	// Step 1: daemon-reload to pick up new/changed unit files
	var reloadCmd *exec.Cmd
	if r.useSudo {
		reloadCmd = exec.CommandContext(ctx, "sudo", "machinectl", "shell", "bloud@",
			"/run/current-system/sw/bin/systemctl", "--user", "daemon-reload")
	} else {
		reloadCmd = exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload")
	}

	output, err := reloadCmd.CombinedOutput()
	if err != nil {
		r.logger.Error("failed to reload user daemon", "error", err, "output", string(output))
		return fmt.Errorf("daemon-reload failed: %w", err)
	}
	r.logger.Info("user daemon reloaded")

	// Step 2: restart bloud-apps.target to restart all apps
	// Using "try-restart" so it only restarts if already active
	var restartCmd *exec.Cmd
	if r.useSudo {
		restartCmd = exec.CommandContext(ctx, "sudo", "machinectl", "shell", "bloud@",
			"/run/current-system/sw/bin/systemctl", "--user", "restart", "bloud-apps.target")
	} else {
		restartCmd = exec.CommandContext(ctx, "systemctl", "--user", "restart", "bloud-apps.target")
	}

	output, err = restartCmd.CombinedOutput()
	if err != nil {
		r.logger.Error("failed to restart bloud-apps.target", "error", err, "output", string(output))
		return fmt.Errorf("restart bloud-apps.target failed: %w", err)
	}
	r.logger.Info("bloud-apps.target restarted")

	return nil
}
