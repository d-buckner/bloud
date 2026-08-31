// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

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

	if len(args) > 0 && args[0] == "app" {
		if err := runAppE2E(root, args[1:]); err != nil {
			errorf("App E2E failed: %v", err)
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

func runPlaywright(root, username, password string) error {
	args := []string{"playwright", "test"}
	if filter := os.Getenv("BLOUD_E2E_PLAYWRIGHT_FILTER"); filter != "" {
		args = append(args, "--grep", filter)
	}
	cmd := exec.Command("npx", args...)
	cmd.Dir = filepath.Join(root, "e2e")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	// Propagate the resolved E2E credentials to the Playwright subprocess.
	// The lifecycle/app runners create an Authentik user with these values
	// (defaults: e2etest/e2etest123). Without this, TEST_CREDS in constants.ts
	// falls back to admin/password and the login fails.
	env := os.Environ()
	env = append(env,
		"BLOUD_E2E_USERNAME="+username,
		"BLOUD_E2E_PASSWORD="+password,
	)
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run Playwright tests: %w", err)
	}
	return nil
}
