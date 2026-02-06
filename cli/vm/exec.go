package vm

import (
	"fmt"
	"os"
	"os/exec"
)

// Run executes a command either in the VM (via SSH) or locally, returning output
func Run(vmName, command string) (string, error) {
	if IsNative() {
		return LocalExec(command)
	}
	return Exec(vmName, command)
}

// RunStream executes a command with stdout/stderr streamed to the terminal
func RunStream(vmName, command string) error {
	if IsNative() {
		return LocalExecStream(command)
	}
	return ExecStream(vmName, command)
}

// RunInteractive executes a command with full stdin/stdout/stderr attached
func RunInteractive(vmName, command string) error {
	if IsNative() {
		return LocalInteractive(command)
	}
	return InteractiveShell(vmName, command)
}

// LocalExec runs a command locally and returns the output
func LocalExec(command string) (string, error) {
	cmd := exec.Command("bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("command failed: %w", err)
	}
	return string(output), nil
}

// LocalExecStream runs a command locally with stdout/stderr piped to the terminal
func LocalExecStream(command string) error {
	cmd := exec.Command("bash", "-c", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// LocalInteractive runs a command locally with full stdin/stdout/stderr attached
func LocalInteractive(command string) error {
	if command == "" {
		command = "bash"
	}
	cmd := exec.Command("bash", "-c", command)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
