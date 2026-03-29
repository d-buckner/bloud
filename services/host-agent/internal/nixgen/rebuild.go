package nixgen

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// NixosSystemPath is the PATH required for NixOS system commands.
// Validated against the ISO: all binaries (nix, nixos-rebuild, systemctl,
// journalctl, podman, machinectl, sudo) live in these two directories.
// /usr/bin is empty on NixOS; /nix/var/nix/profiles/default/bin does not
// exist on this system. /bin contains only sh.
//
// Set via os.Setenv in main so all exec.Command calls resolve bare names,
// and passed explicitly via "sudo env" so child processes inherit it too
// (sudo resets the environment by default).
const NixosSystemPath = "/run/wrappers/bin:/run/current-system/sw/bin:/bin"

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
		impure:    true, // Enable by default for development
		useSudo:   true, // nixos-rebuild switch requires root
	}
}

// flakeURI returns the flake path with the path: URI scheme prefix when the
// path is absolute. This forces Nix to evaluate the flake rather than treating
// the path as an already-built store derivation. Without path:, Nix interprets
// /nix/store/hash/subdir as a store path and returns the store root directly —
// skipping flake evaluation entirely and producing the wrong build output.
func (r *Rebuilder) flakeURI() string {
	if strings.HasPrefix(r.flakePath, "/") && !strings.HasPrefix(r.flakePath, "path:") {
		return "path:" + r.flakePath
	}
	return r.flakePath
}

// RebuildResult contains the result of a rebuild operation
type RebuildResult struct {
	Success      bool
	Output       string
	ErrorMessage string
	Duration     time.Duration
	Changes      []string
}

// nixosRebuildCmd constructs a nixos-rebuild command with the correct sudo
// wrapping and _NIXOS_REBUILD_REEXEC=1 to skip the unnecessary re-exec step.
//
// nixos-rebuild normally builds $flake#$host.config.system.build.nixos-rebuild
// and re-execs from the result before switching. This is an optimization to use
// the latest nixos-rebuild script, but it adds an extra build step.
// _NIXOS_REBUILD_REEXEC=1 skips it safely since we're already using the correct
// nixos-rebuild version. It must be passed inline via `sudo env` because sudo
// strips environment variables by default.
func (r *Rebuilder) nixosRebuildCmd(ctx context.Context, args []string) *exec.Cmd {
	var cmd *exec.Cmd
	if r.useSudo {
		sudoArgs := []string{
			"env",
			"_NIXOS_REBUILD_REEXEC=1",
			"PATH=" + NixosSystemPath,
		}
		// Pass BLOUD_FLAKE_PATH through sudo so builtins.getEnv in host-agent.nix
		// can detect the deployed package root during --impure flake evaluation.
		if r.flakePath != "" {
			sudoArgs = append(sudoArgs, "BLOUD_FLAKE_PATH="+r.flakePath)
		}
		sudoArgs = append(sudoArgs, "nixos-rebuild")
		sudoArgs = append(sudoArgs, args...)
		cmd = exec.CommandContext(ctx, "sudo", sudoArgs...)
	} else {
		cmd = exec.CommandContext(ctx, "nixos-rebuild", args...)
		cmd.Env = append(os.Environ(), "_NIXOS_REBUILD_REEXEC=1")
	}
	// Must not run from a nix store path CWD: nix uses CWD as a cache hint
	// and returns the wrong derivation (the store root) even with path: URI.
	cmd.Dir = "/tmp"
	return cmd
}

// userSystemctlCmd constructs a systemctl --user command for the bloud user.
// When useSudo is true, it uses machinectl shell to run in the user's login
// session (required to reach the user's systemd instance from a root service).
func (r *Rebuilder) userSystemctlCmd(ctx context.Context, args []string) *exec.Cmd {
	if r.useSudo {
		// machinectl shell requires an absolute path to the binary being invoked.
		machinectlArgs := append([]string{
			"shell", "bloud@",
			"/run/current-system/sw/bin/systemctl", "--user",
		}, args...)
		return exec.CommandContext(ctx, "sudo", append([]string{"machinectl"}, machinectlArgs...)...)
	}
	return exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...)
}

// Switch performs a nixos-rebuild switch
func (r *Rebuilder) Switch(ctx context.Context) (*RebuildResult, error) {
	start := time.Now()

	result := &RebuildResult{
		Changes: []string{},
	}

	args := []string{"switch"}

	if r.flakePath != "" {
		args = append(args, "--flake", fmt.Sprintf("%s#%s", r.flakeURI(), r.hostname))
	}

	if r.impure {
		args = append(args, "--impure")
	}

	if r.dryRun {
		args = append(args, "--dry-run")
	}

	r.logger.Info("running nixos-rebuild", "args", args, "sudo", r.useSudo)

	cmd := r.nixosRebuildCmd(ctx, args)

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

// Test performs a nixos-rebuild test (applies config without touching bootloader)
func (r *Rebuilder) Test(ctx context.Context) (*RebuildResult, error) {
	start := time.Now()

	result := &RebuildResult{
		Changes: []string{},
	}

	args := []string{"test"}
	if r.flakePath != "" {
		args = append(args, "--flake", fmt.Sprintf("%s#%s", r.flakeURI(), r.hostname))
	}
	if r.impure {
		args = append(args, "--impure")
	}

	r.logger.Info("running nixos-rebuild test", "args", args, "sudo", r.useSudo)

	cmd := r.nixosRebuildCmd(ctx, args)
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
		args = append(args, "--flake", fmt.Sprintf("%s#%s", r.flakeURI(), r.hostname))
	}

	if r.impure {
		args = append(args, "--impure")
	}

	r.logger.Info("running nixos-rebuild (streaming)", "args", args, "sudo", r.useSudo)
	events <- RebuildEvent{Type: "output", Message: fmt.Sprintf("Running: nixos-rebuild %s", strings.Join(args, " "))}

	cmd := r.nixosRebuildCmd(ctx, args)

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

// StopUserService stops a systemd user service for an app
func (r *Rebuilder) StopUserService(ctx context.Context, appName string) error {
	serviceName := fmt.Sprintf("podman-%s.service", appName)
	r.logger.Info("stopping user service", "service", serviceName)

	output, err := r.userSystemctlCmd(ctx, []string{"stop", serviceName}).CombinedOutput()
	if err != nil {
		r.logger.Warn("failed to stop service", "service", serviceName, "error", err, "output", string(output))
		return fmt.Errorf("failed to stop %s: %w", serviceName, err)
	}

	r.logger.Info("service stopped", "service", serviceName)
	return nil
}

// ReloadAndRestartApps reloads systemd user daemon and restarts all bloud apps.
// Call after nixos-rebuild to pick up new/changed unit files and restart apps.
func (r *Rebuilder) ReloadAndRestartApps(ctx context.Context) error {
	r.logger.Info("reloading systemd user daemon and restarting apps")

	output, err := r.userSystemctlCmd(ctx, []string{"daemon-reload"}).CombinedOutput()
	if err != nil {
		r.logger.Error("failed to reload user daemon", "error", err, "output", string(output))
		return fmt.Errorf("daemon-reload failed: %w", err)
	}
	r.logger.Info("user daemon reloaded")

	output, err = r.userSystemctlCmd(ctx, []string{"restart", "bloud-apps.target"}).CombinedOutput()
	if err != nil {
		r.logger.Error("failed to restart bloud-apps.target", "error", err, "output", string(output))
		return fmt.Errorf("restart bloud-apps.target failed: %w", err)
	}
	r.logger.Info("bloud-apps.target restarted")

	return nil
}
