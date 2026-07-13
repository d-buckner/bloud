package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// cmdE2E runs either Playwright alone or the complete lifecycle E2E.
func cmdE2E(args []string) int {
	root, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	if len(args) > 0 && args[0] == "lifecycle" {
		if err := runLifecycle(root, args[1:]); err != nil {
			errorf("Lifecycle E2E failed: %v", err)
			return 1
		}
		return 0
	}

	playwrightArgs := []string{"playwright", "test"}
	playwrightArgs = append(playwrightArgs, args...)

	cmd := exec.Command("npx", playwrightArgs...)
	cmd.Dir = filepath.Join(root, "e2e")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Run(); err != nil {
		errorf("End-to-end tests failed: %v", err)
		return 1
	}
	return 0
}

func runPlaywright(root string) error {
	cmd := exec.Command("npx", "playwright", "test")
	cmd.Dir = filepath.Join(root, "e2e")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run Playwright tests: %w", err)
	}
	return nil
}
