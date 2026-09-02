// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"codeberg.org/d-buckner/bloud/cli/backend"
)

// appE2ERunner runs a single app's E2E flow on a self-contained runtime:
// provision -> deploy -> wait for system convergence -> ensure user ->
// run the app's Playwright spec.
//
// Unlike the lifecycle runner (which exercises install/restart/uninstall
// transitions for Jellyfin specifically), this runner exists so CI can give
// each app its own runtime in parallel: image pulls and convergence happen
// concurrently across runners instead of sequentially on one.
//
// It embeds *lifecycle to reuse the provisioning, deployment, and remote
// execution helpers.
type appE2ERunner struct {
	*lifecycle
	app string
}

func runAppE2E(root string, args []string) error {
	if wantsHelp(args) {
		printAppE2EUsage(os.Stdout)
		return nil
	}
	name, err := backendName()
	if err != nil {
		return err
	}

	cfg, help, err := parseLifecycleConfig(root, args, os.Getenv, name)
	if err != nil {
		return err
	}
	if help {
		printAppE2EUsage(os.Stdout)
		return nil
	}

	app := os.Getenv("BLOUD_E2E_APP")
	if app == "" {
		return fmt.Errorf("BLOUD_E2E_APP is required (e.g. jellyfin, navidrome, immich, affine, appflowy, install-streaming)")
	}

	runner := &appE2ERunner{
		lifecycle: &lifecycle{cfg: cfg, failed: true},
		app:       app,
	}
	return runner.run()
}

func printAppE2EUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage: ./bloud e2e app

Provisions a self-contained runtime, installs one app through the
host-agent API, and runs that app's Playwright spec.

Required environment:
  BLOUD_E2E_APP            App to test: jellyfin | navidrome | immich | affine | appflowy | install-streaming

Optional environment (same as "e2e lifecycle"):
  BLOUD_BACKEND            Override the stored backend preference
                           (e.g. native = run on the current machine, no VM)
  BLOUD_URL                Browser-accessible ingress URL
  BLOUD_E2E_PLAYWRIGHT_FILTER
                           Playwright --grep filter (default: the app name)
  BLOUD_E2E_USERNAME       Browser test user (default: e2etest)
  BLOUD_E2E_PASSWORD       Browser test password (default: e2etest123)
  BLOUD_E2E_RUNTIME_DIR    Remote deployment/data directory (default: /var/tmp/bloud-e2e-runtime)

Flags:
  --keep                 Leave the host-agent deployment running after the test`)
}

func (r *appE2ERunner) run() (runErr error) {
	defer func() {
		if !r.cfg.keep && r.cfg.remoteHome != "" {
			r.cleanupAppDeployment()
		}
	}()

	r.step("Checking host prerequisites")
	home, err := r.remoteOutput("printf %s \"$HOME\"")
	if err != nil {
		return err
	}
	r.cfg.remoteHome = strings.TrimSpace(home)
	if r.cfg.remoteHome == "" {
		return fmt.Errorf("remote HOME is empty")
	}
	if err := r.remoteRun(remotePreflightScript, r.cfg.remoteDir); err != nil {
		return fmt.Errorf("host preflight: %w", err)
	}
	if err := r.prepareQEMUTarget(); err != nil {
		return err
	}
	if r.cfg.native {
		r.step("Provisioning native runtime")
		bk := backend.NewNativeBackend(r.cfg.root)
		if err := bk.Create(context.Background()); err != nil {
			return fmt.Errorf("native runtime provisioning failed: %w", err)
		}
	}

	if err := r.buildAndDeploy(); err != nil {
		return err
	}

	r.step("Ensuring the E2E user exists")
	payload, err := json.Marshal(map[string]string{"username": r.cfg.username, "password": r.cfg.password})
	if err != nil {
		return err
	}
	if err := r.remoteRun(remoteEnsureUserScript, string(payload)); err != nil {
		return err
	}

	r.step(fmt.Sprintf("Running %s E2E (browser)", r.app))
	if os.Getenv("BLOUD_E2E_PLAYWRIGHT_FILTER") == "" {
		os.Setenv("BLOUD_E2E_PLAYWRIGHT_FILTER", r.app)
	}
	if err := runPlaywright(r.cfg.root, r.cfg.username, r.cfg.password); err != nil {
		return err
	}

	r.failed = false
	r.step(fmt.Sprintf("%s E2E passed", r.app))
	return nil
}

// cleanupAppDeployment stops and removes the host-agent deployment without
// touching app state (the spec's own teardown handles uninstall).
func (r *appE2ERunner) cleanupAppDeployment() {
	script := `systemctl --user disable --now "$1" >/dev/null 2>&1 || true
rm -f "$2/.config/systemd/user/$1"
systemctl --user daemon-reload >/dev/null 2>&1 || true`
	if err := r.remoteRun(script, lifecycleHostAgentUnit, r.cfg.remoteHome); err != nil {
		errorf("failed to clean up app E2E deployment: %v", err)
	}
}
