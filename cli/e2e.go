// SPDX-License-Identifier: AGPL-3.0-only

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

	// The Playwright suite is its own npm package rather than a workspace, so
	// nothing installs it as a side effect of the root install. Check before
	// provisioning anything: without the check the failure is an
	// ERR_MODULE_NOT_FOUND from inside Playwright's config loader minutes
	// into a run that has already built and deployed a runtime, and it names
	// neither the missing package nor the command that installs it. `affected`
	// is exempt because it only reads validation.yaml and prints a list.
	if len(args) == 0 || args[0] != "affected" {
		if err := requireE2EDeps(root); err != nil {
			errorf("%v", err)
			return 1
		}
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

	if len(args) > 0 && args[0] == "affected" {
		if err := runE2EAffected(root, args[1:]); err != nil {
			errorf("Could not resolve affected e2e apps: %v", err)
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

// requireE2EDeps checks that the Playwright suite's own package is installed.
// e2e/ is a standalone npm package, not one of the root workspaces, so the
// root install does not cover it. The check exists because the failure it
// prevents is opaque: Playwright resolves the config, the config's import of
// @playwright/test misses, and the run dies with ERR_MODULE_NOT_FOUND after
// the caller has already built and deployed a runtime.
func requireE2EDeps(root string) error {
	spec := filepath.Join(root, "e2e", "node_modules", "@playwright", "test")
	if _, err := os.Stat(spec); err != nil {
		return fmt.Errorf("the Playwright suite is not installed: run `npm --prefix e2e ci`")
	}
	return nil
}

func runPlaywright(root, username, password, apiToken string) error {
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
	// The API helpers authenticate admin calls with the runtime credential. The
	// caller reads it from the runtime it deployed (see lifecycle.apiTokenPath);
	// e2e/lib/api.ts keeps its own fallback chain for manual runs.
	if apiToken != "" {
		env = append(env, "BLOUD_API_TOKEN="+apiToken)
	}
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run Playwright tests: %w", err)
	}
	return nil
}
