package vm

import (
	"fmt"
	"os/exec"
)

// LocalExec runs a command locally and returns the output
func LocalExec(command string) (string, error) {
	cmd := exec.Command("bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("command failed: %w", err)
	}
	return string(output), nil
}
