// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
)

const (
	lifecycleHostAgentUnit = "bloud-e2e-host-agent.service"
)

type lifecycleConfig struct {
	root       string
	lima       string
	qemu       string // QEMU instance name (auto-provisioned)
	sshTarget  string
	native     bool   // run natively on the current machine (no VM)
	sshKeyFile string // SSH key file for QEMU (auto-derived)
	baseURL    string
	remoteDir  string
	goarch     string
	username   string
	password   string
	traefikDir string
	hostOnly   bool
	keep       bool
	remoteHome string
}

type lifecycle struct {
	cfg      lifecycleConfig
	buildDir string
	failed   bool
}

var lifecycleRemotePath = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

func runLifecycle(root string, args []string) error {
	if wantsHelp(args) {
		printLifecycleUsage(os.Stdout)
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
		printLifecycleUsage(os.Stdout)
		return nil
	}

	runner := &lifecycle{cfg: cfg, failed: true}
	return runner.run()
}

// wantsHelp reports whether args ask for usage text; checked before backend
// resolution so '--help' never triggers the backend prompt.

// wantsHelp reports whether args ask for usage text; checked before backend
// resolution so '--help' never triggers the backend prompt.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// parseLifecycleConfig builds the lifecycle config from args and the
// environment. backendName is the resolved runtime backend (see
// backendName); explicit BLOUD_E2E_* instance variables still override it.

// parseLifecycleConfig builds the lifecycle config from args and the
// environment. backendName is the resolved runtime backend (see
// backendName); explicit BLOUD_E2E_* instance variables still override it.
func parseLifecycleConfig(root string, args []string, getenv func(string) string, backendName string) (lifecycleConfig, bool, error) {
	cfg := lifecycleConfig{
		root:       root,
		lima:       getenv("BLOUD_E2E_LIMA_INSTANCE"),
		qemu:       getenv("BLOUD_E2E_QEMU_INSTANCE"),
		sshTarget:  getenv("BLOUD_E2E_SSH_TARGET"),
		native:     backendName == "native",
		baseURL:    getenv("BLOUD_URL"),
		remoteDir:  getenv("BLOUD_E2E_RUNTIME_DIR"),
		goarch:     getenv("BLOUD_E2E_GOARCH"),
		username:   getenv("BLOUD_E2E_USERNAME"),
		password:   getenv("BLOUD_E2E_PASSWORD"),
		traefikDir: getenv("BLOUD_E2E_TRAEFIK_DYNAMIC_DIR"),
	}
	applyLifecycleDefaults(&cfg, backendName)

	flags := flag.NewFlagSet("e2e lifecycle", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&cfg.hostOnly, "host-only", false, "skip Playwright browser tests")
	// Default from env so CI (which sets BLOUD_E2E_KEEP=1) gets the same
	// behavior as passing --keep on the command line. The flag can still
	// override the env default.
	keepDefault := getenv("BLOUD_E2E_KEEP") == "1"
	flags.BoolVar(&cfg.keep, "keep", keepDefault, "leave the host-agent service running")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return cfg, true, nil
		}
		return cfg, false, err
	}
	if err := validateLifecycleConfig(&cfg); err != nil {
		return cfg, false, err
	}
	return cfg, false, nil
}

// applyLifecycleDefaults fills unset config fields with backend-aware
// defaults (instance names, URLs, credentials, derived paths).

// applyLifecycleDefaults fills unset config fields with backend-aware
// defaults (instance names, URLs, credentials, derived paths).
func applyLifecycleDefaults(cfg *lifecycleConfig, backendName string) {
	if cfg.remoteDir == "" {
		cfg.remoteDir = "/var/tmp/bloud-e2e-runtime"
	}
	// Default the VM instance from the backend when nothing is explicit.
	switch {
	case cfg.native:
		// Runs on the current machine; no VM instance.
	case cfg.lima == "" && cfg.qemu == "" && cfg.sshTarget == "" && backendName == "qemu":
		cfg.qemu = "bloud-qemu"
	case cfg.lima == "" && cfg.qemu == "" && cfg.sshTarget == "":
		cfg.lima = "bloud-dev"
	}
	if cfg.qemu != "" && cfg.sshTarget == "" {
		// Derive SSH target and key from QEMU instance
		cfg.sshTarget = "bloud@127.0.0.1"
		cfg.sshKeyFile = filepath.Join(cfg.root, ".bloud", "qemu", cfg.qemu, "id_ed25519")
	}
	if cfg.baseURL == "" && (cfg.lima != "" || cfg.native) {
		cfg.baseURL = "http://localhost:3000"
	}
	if cfg.goarch == "" {
		cfg.goarch = runtime.GOARCH
	}
	if cfg.username == "" {
		cfg.username = "e2etest"
	}
	if cfg.password == "" {
		cfg.password = "e2etest123"
	}
	if cfg.traefikDir == "" {
		cfg.traefikDir = filepath.Join(cfg.remoteDir, "data", "traefik", "dynamic")
	}
}

// validateLifecycleConfig rejects combinations of instance/SSH/runtime
// settings that cannot describe one coherent deployment target.

// validateLifecycleConfig rejects combinations of instance/SSH/runtime
// settings that cannot describe one coherent deployment target.
func validateLifecycleConfig(cfg *lifecycleConfig) error {
	if err := validateInstanceSelection(cfg); err != nil {
		return err
	}
	if !cfg.hostOnly && cfg.baseURL == "" {
		return fmt.Errorf("BLOUD_URL is required unless --host-only is used")
	}
	if !filepath.IsAbs(cfg.remoteDir) || filepath.Clean(cfg.remoteDir) == "/" || !lifecycleRemotePath.MatchString(cfg.remoteDir) {
		return fmt.Errorf("BLOUD_E2E_RUNTIME_DIR must be a non-root absolute path")
	}
	if lifecycleReservedDir(cfg.remoteDir) {
		return fmt.Errorf("BLOUD_E2E_RUNTIME_DIR must identify a dedicated child directory")
	}
	if !filepath.IsAbs(cfg.traefikDir) || !lifecycleRemotePath.MatchString(cfg.traefikDir) {
		return fmt.Errorf("BLOUD_E2E_TRAEFIK_DYNAMIC_DIR must be an absolute path containing only letters, numbers, '.', '_', '-', and '/'")
	}
	switch cfg.goarch {
	case "amd64", "arm64":
	default:
		return fmt.Errorf("unsupported BLOUD_E2E_GOARCH %q", cfg.goarch)
	}
	return nil
}

// validateInstanceSelection enforces that at most one VM/SSH target is
// configured and that it is compatible with the chosen backend.

// validateInstanceSelection enforces that at most one VM/SSH target is
// configured and that it is compatible with the chosen backend.
func validateInstanceSelection(cfg *lifecycleConfig) error {
	if cfg.native && (cfg.lima != "" || cfg.qemu != "" || cfg.sshTarget != "") {
		return fmt.Errorf("native backend cannot be combined with BLOUD_E2E_LIMA_INSTANCE, BLOUD_E2E_QEMU_INSTANCE, or BLOUD_E2E_SSH_TARGET")
	}
	if cfg.lima != "" && (cfg.qemu != "" || cfg.sshTarget != "") {
		return fmt.Errorf("set only one of BLOUD_E2E_LIMA_INSTANCE, BLOUD_E2E_QEMU_INSTANCE, or BLOUD_E2E_SSH_TARGET")
	}
	if cfg.qemu != "" && cfg.sshTarget != "" && cfg.sshKeyFile == "" {
		// sshTarget was set manually, not derived from QEMU
		return fmt.Errorf("BLOUD_E2E_QEMU_INSTANCE and BLOUD_E2E_SSH_TARGET cannot be used together")
	}
	return nil
}

// lifecycleReservedDir reports whether a runtime dir is (or is inside) a
// system directory that a dedicated validation runtime must never occupy.

// lifecycleReservedDir reports whether a runtime dir is (or is inside) a
// system directory that a dedicated validation runtime must never occupy.
func lifecycleReservedDir(dir string) bool {
	switch filepath.Clean(dir) {
	case "/bin", "/boot", "/dev", "/etc", "/home", "/opt", "/run", "/srv", "/tmp", "/usr", "/var":
		return true
	}
	return false
}

func printLifecycleUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `Usage: ./bloud e2e lifecycle [--host-only] [--keep]

Required environment:
  None — the VM instance defaults from the backend preference
  (.bloud/preferences.yaml, set by ./bloud setup).

Optional environment:
  BLOUD_BACKEND            Override the stored backend preference
                           (e.g. native = run on the current machine, no VM)
  BLOUD_E2E_LIMA_INSTANCE
                         Lima instance name (default: bloud-dev)
  BLOUD_E2E_SSH_TARGET   Use a generic SSH target instead of Lima
  BLOUD_URL              Browser-accessible ingress URL
  BLOUD_E2E_RUNTIME_DIR  Remote deployment/data directory (default: /var/tmp/bloud-e2e-runtime)
  BLOUD_E2E_GOARCH       Linux target architecture: amd64 or arm64
  BLOUD_E2E_USERNAME     Browser test user (default: e2etest)
  BLOUD_E2E_PASSWORD     Browser test password (default: e2etest123)
  BLOUD_E2E_TRAEFIK_DYNAMIC_DIR
                         Directory watched by the target ingress

Flags:
  --host-only            Skip Playwright; verify host lifecycle behavior only
  --keep                 Leave the host-agent deployment running after the test`)
}

func (r *lifecycle) run() (runErr error) {
	defer func() {
		if r.failed {
			fmt.Fprintf(os.Stderr, "Collecting failure logs in %s\n", r.artifactDir())
			r.collectLogs()
		}
		if !r.cfg.keep && r.cfg.remoteHome != "" {
			r.cleanupRemoteDeployment()
		}
	}()

	if err := r.checkPrerequisites(); err != nil {
		return err
	}
	if err := r.buildAndDeploy(); err != nil {
		return err
	}

	r.step("Resetting prior managed Jellyfin state")
	if err := r.remoteRun(remoteResetJellyfinScript, r.cfg.remoteDir); err != nil {
		return err
	}

	if err := r.runInstallFlow(); err != nil {
		return err
	}

	r.step("Asserting installed Jellyfin host state")
	if err := r.remoteRun(remoteAssertInstalledScript, r.cfg.remoteDir, r.cfg.traefikDir); err != nil {
		return err
	}

	if err := r.verifyAfterRestart(); err != nil {
		return err
	}

	r.step("Uninstalling Jellyfin and asserting cleanup")
	if err := r.remoteRun(remoteUninstallScript, r.cfg.remoteDir, r.cfg.traefikDir); err != nil {
		return err
	}

	r.failed = false
	r.step("Jellyfin lifecycle passed")
	return nil
}

// checkPrerequisites verifies the remote host (HOME, preflight script),
// prepares a QEMU target if one is configured, and provisions the native
// runtime when running without a VM.
