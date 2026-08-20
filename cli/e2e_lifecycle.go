// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"codeberg.org/d-buckner/bloud/cli/backend"
)

const (
	lifecycleHostAgentUnit = "bloud-e2e-host-agent.service"
)

type lifecycleConfig struct {
	root       string
	lima       string
	qemu       string // QEMU instance name (auto-provisioned)
	sshTarget  string
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
	cfg, help, err := parseLifecycleConfig(root, args, os.Getenv)
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

func parseLifecycleConfig(root string, args []string, getenv func(string) string) (lifecycleConfig, bool, error) {
	cfg := lifecycleConfig{
		root:       root,
		lima:       getenv("BLOUD_E2E_LIMA_INSTANCE"),
		qemu:       getenv("BLOUD_E2E_QEMU_INSTANCE"),
		sshTarget:  getenv("BLOUD_E2E_SSH_TARGET"),
		baseURL:    getenv("BLOUD_URL"),
		remoteDir:  getenv("BLOUD_E2E_RUNTIME_DIR"),
		goarch:     getenv("BLOUD_E2E_GOARCH"),
		username:   getenv("BLOUD_E2E_USERNAME"),
		password:   getenv("BLOUD_E2E_PASSWORD"),
		traefikDir: getenv("BLOUD_E2E_TRAEFIK_DYNAMIC_DIR"),
	}
	if cfg.remoteDir == "" {
		cfg.remoteDir = "/var/tmp/bloud-e2e-runtime"
	}
	if cfg.lima == "" && cfg.qemu == "" && cfg.sshTarget == "" {
		cfg.lima = "bloud-dev"
	}
	if cfg.qemu != "" && cfg.sshTarget == "" {
		// Derive SSH target and key from QEMU instance
		cfg.sshTarget = "bloud@127.0.0.1"
		cfg.sshKeyFile = filepath.Join(root, ".bloud", "qemu", cfg.qemu, "id_ed25519")
	}
	if cfg.baseURL == "" && cfg.lima != "" {
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

	flags := flag.NewFlagSet("e2e lifecycle", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&cfg.hostOnly, "host-only", false, "skip Playwright browser tests")
	flags.BoolVar(&cfg.keep, "keep", false, "leave the host-agent service running")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return cfg, true, nil
		}
		return cfg, false, err
	}
	if flags.NArg() != 0 {
		return cfg, false, fmt.Errorf("unexpected lifecycle arguments: %s", strings.Join(flags.Args(), " "))
	}
	if cfg.lima != "" && (cfg.qemu != "" || cfg.sshTarget != "") {
		return cfg, false, fmt.Errorf("set only one of BLOUD_E2E_LIMA_INSTANCE, BLOUD_E2E_QEMU_INSTANCE, or BLOUD_E2E_SSH_TARGET")
	}
	if cfg.qemu != "" && cfg.sshTarget != "" && cfg.sshKeyFile == "" {
		// sshTarget was set manually, not derived from QEMU
		return cfg, false, fmt.Errorf("BLOUD_E2E_QEMU_INSTANCE and BLOUD_E2E_SSH_TARGET cannot be used together")
	}
	if !cfg.hostOnly && cfg.baseURL == "" {
		return cfg, false, fmt.Errorf("BLOUD_URL is required unless --host-only is used")
	}
	if !filepath.IsAbs(cfg.remoteDir) || filepath.Clean(cfg.remoteDir) == "/" || !lifecycleRemotePath.MatchString(cfg.remoteDir) {
		return cfg, false, fmt.Errorf("BLOUD_E2E_RUNTIME_DIR must be a non-root absolute path")
	}
	switch filepath.Clean(cfg.remoteDir) {
	case "/bin", "/boot", "/dev", "/etc", "/home", "/opt", "/run", "/srv", "/tmp", "/usr", "/var":
		return cfg, false, fmt.Errorf("BLOUD_E2E_RUNTIME_DIR must identify a dedicated child directory")
	}
	if !filepath.IsAbs(cfg.traefikDir) || !lifecycleRemotePath.MatchString(cfg.traefikDir) {
		return cfg, false, fmt.Errorf("BLOUD_E2E_TRAEFIK_DYNAMIC_DIR must be an absolute path containing only letters, numbers, '.', '_', '-', and '/'")
	}
	switch cfg.goarch {
	case "amd64", "arm64":
	default:
		return cfg, false, fmt.Errorf("unsupported BLOUD_E2E_GOARCH %q", cfg.goarch)
	}
	return cfg, false, nil
}

func printLifecycleUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage: ./bloud e2e lifecycle [--host-only] [--keep]

Required environment:
  None for the default Lima target. It uses bloud-dev.

Optional environment:
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
	buildDir, err := os.MkdirTemp("", "bloud-e2e-build-*")
	if err != nil {
		return err
	}
	r.buildDir = buildDir
	defer func() {
		if r.failed {
			fmt.Fprintf(os.Stderr, "Collecting failure logs in %s\n", r.artifactDir())
			r.collectLogs()
		}
		if !r.cfg.keep && r.cfg.remoteHome != "" {
			r.cleanupRemoteDeployment()
		}
		os.RemoveAll(r.buildDir)
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

	r.step("Building host-agent artifacts")
	if err := r.localRun(r.cfg.root, nil, "npm", "run", "build", "--workspace=@bloud/host-agent-web"); err != nil {
		return err
	}
	hostAgentDir := filepath.Join(r.cfg.root, "services", "host-agent")
	buildEnv := append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+r.cfg.goarch)
	if err := r.localRun(hostAgentDir, buildEnv, "go", "build", "-o", filepath.Join(r.buildDir, "host-agent"), "./cmd/host-agent"); err != nil {
		return err
	}

	r.step("Deploying host-agent, frontend, and app catalog")
	if err := r.remoteRun("mkdir -p \"$1/host-agent/web/build\" \"$1/apps\"", r.cfg.remoteDir); err != nil {
		return err
	}
	if err := r.copyDirectory(filepath.Join(r.cfg.root, "apps"), r.remotePath("apps")); err != nil {
		return err
	}
	if err := r.copyDirectory(filepath.Join(hostAgentDir, "web", "build"), r.remotePath("host-agent/web/build")); err != nil {
		return err
	}
	if err := r.copyFile(filepath.Join(r.buildDir, "host-agent"), r.remotePath("host-agent/host-agent")); err != nil {
		return err
	}
	if err := r.remoteRun("chmod 755 \"$1/host-agent/host-agent\"", r.cfg.remoteDir); err != nil {
		return err
	}

	r.step("Installing host-agent systemd service")
	unit := renderLifecycleHostAgentUnit(r.cfg)
	unitPath := filepath.Join(r.buildDir, lifecycleHostAgentUnit)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return err
	}
	if err := r.remoteRun("mkdir -p \"$1/.config/systemd/user\"", r.cfg.remoteHome); err != nil {
		return err
	}
	if err := r.copyFile(unitPath, filepath.Join(r.cfg.remoteHome, ".config", "systemd", "user", lifecycleHostAgentUnit)); err != nil {
		return err
	}
	if err := r.remoteRun(remoteInstallHostAgentScript, lifecycleHostAgentUnit); err != nil {
		return err
	}
	if err := r.remoteRun(remoteWaitForHostAgentScript); err != nil {
		return fmt.Errorf("wait for host-agent API: %w", err)
	}

	r.step("Resetting prior managed Jellyfin state")
	if err := r.remoteRun(remoteResetJellyfinScript); err != nil {
		return err
	}

	if r.cfg.hostOnly {
		r.step("Installing Jellyfin through the host-local API")
		if err := r.remoteRun(remoteInstallJellyfinScript); err != nil {
			return err
		}
	} else {
		r.step("Ensuring the E2E user exists")
		payload, err := json.Marshal(map[string]string{"username": r.cfg.username, "password": r.cfg.password})
		if err != nil {
			return err
		}
		if err := r.remoteRun(remoteEnsureUserScript, string(payload)); err != nil {
			return err
		}
		r.step("Running Jellyfin browser install and login flow")
		if err := runPlaywright(r.cfg.root); err != nil {
			return err
		}
	}

	r.step("Asserting installed Jellyfin host state")
	if err := r.remoteRun(remoteAssertInstalledScript, r.cfg.traefikDir); err != nil {
		return err
	}

	r.step("Restarting Jellyfin and host-agent")
	if err := r.remoteRun(remoteRestartScript, lifecycleHostAgentUnit); err != nil {
		return err
	}
	if !r.cfg.hostOnly {
		r.step("Verifying browser flow after service restarts")
		if err := runPlaywright(r.cfg.root); err != nil {
			return err
		}
	}

	r.step("Uninstalling Jellyfin and asserting cleanup")
	if err := r.remoteRun(remoteUninstallScript, r.cfg.remoteDir, r.cfg.traefikDir); err != nil {
		return err
	}

	r.failed = false
	r.step("Jellyfin lifecycle passed")
	return nil
}

func renderLifecycleHostAgentUnit(cfg lifecycleConfig) string {
	var extraEnv strings.Builder
	if cfg.qemu != "" {
		fmt.Fprintf(&extraEnv, "Environment=BLOUD_TRUSTED_LOCAL_NETS=10.0.2.0/24\n")
	}
	return fmt.Sprintf(`[Unit]
Description=Bloud E2E host agent
After=network-online.target podman.socket
Wants=network-online.target podman.socket

[Service]
Type=simple
WorkingDirectory=%s/host-agent
Environment=BLOUD_DATA_DIR=%s/data
Environment=BLOUD_APPS_DIR=%s/apps
Environment=BLOUD_TRAEFIK_DYNAMIC_DIR=%s
Environment=BLOUD_SSO_ISSUER_URL=%s
%s
ExecStart=%s/host-agent/host-agent
Restart=on-failure
RestartSec=2

[Install]
`, cfg.remoteDir, cfg.remoteDir, cfg.remoteDir, cfg.traefikDir, ssoIssuerURL(), extraEnv.String(), cfg.remoteDir)
}

func (r *lifecycle) step(message string) {
	fmt.Printf("\n%s==>%s %s\n", colorGreen, colorReset, message)
}

func (r *lifecycle) localRun(dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func (r *lifecycle) remoteRun(script string, args ...string) error {
	cmd := r.remoteCommand(script, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = strings.NewReader("set -euo pipefail\n" + script)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("remote command failed: %w", err)
	}
	return nil
}

func (r *lifecycle) remoteOutput(script string, args ...string) (string, error) {
	cmd := r.remoteCommand(script, args...)
	cmd.Stdin = strings.NewReader("set -euo pipefail\n" + script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("remote command failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func (r *lifecycle) remoteCommand(_ string, args ...string) *exec.Cmd {
	commandArgs := []string{}
	if r.cfg.lima != "" {
		name := "limactl"
		commandArgs = append(commandArgs, "shell", "--start", r.cfg.lima, "bash", "-se", "--")
		for _, arg := range args {
			commandArgs = append(commandArgs, arg)
		}
		return exec.Command(name, commandArgs...)
	} else if r.cfg.qemu != "" {
		name := "ssh"
		commandArgs = append(commandArgs,
			"-i", r.cfg.sshKeyFile,
			"-p", "2222",
			"-o", "StrictHostKeyChecking=accept-new",
			"-o", "ConnectTimeout=5",
			r.cfg.sshTarget, "bash", "-se", "--")
		for _, arg := range args {
			commandArgs = append(commandArgs, shellQuote(arg))
		}
		return exec.Command(name, commandArgs...)
	} else {
		name := "ssh"
		commandArgs = append(commandArgs, r.cfg.sshTarget, "bash", "-se", "--")
		for _, arg := range args {
			commandArgs = append(commandArgs, shellQuote(arg))
		}
		return exec.Command(name, commandArgs...)
	}
}

func (r *lifecycle) copyDirectory(source, destination string) error {
	if r.cfg.lima != "" {
		return r.remoteRun(`rm -rf "$2"
mkdir -p "$2"
cp -a "$1/." "$2/"`, source, destination)
	} else if r.cfg.qemu != "" {
		sshCmd := fmt.Sprintf("ssh -i %s -p 2222 -o StrictHostKeyChecking=accept-new", shellQuote(r.cfg.sshKeyFile))
		args := []string{"-a", "--delete", "-e", sshCmd, source + string(os.PathSeparator), r.cfg.sshTarget + ":" + shellQuote(destination) + "/"}
		return r.localRun(r.cfg.root, os.Environ(), "rsync", args...)
	} else {
		args := []string{"-a", "--delete", source + string(os.PathSeparator), r.cfg.sshTarget + ":" + shellQuote(destination) + "/"}
		return r.localRun(r.cfg.root, os.Environ(), "rsync", args...)
	}
}

func (r *lifecycle) copyFile(source, destination string) error {
	if r.cfg.lima != "" {
		return r.localRun(r.cfg.root, os.Environ(), "limactl", "copy", source, r.cfg.lima+":"+destination)
	} else if r.cfg.qemu != "" {
		sshCmd := fmt.Sprintf("ssh -i %s -p 2222 -o StrictHostKeyChecking=accept-new", shellQuote(r.cfg.sshKeyFile))
		return r.localRun(r.cfg.root, os.Environ(), "rsync", "-a", "-e", sshCmd, source, r.cfg.sshTarget+":"+shellQuote(destination))
	} else {
		return r.localRun(r.cfg.root, os.Environ(), "rsync", "-a", source, r.cfg.sshTarget+":"+shellQuote(destination))
	}
}

func (r *lifecycle) remotePath(relative string) string {
	return filepath.Join(r.cfg.remoteDir, relative)
}

func (r *lifecycle) artifactDir() string {
	return filepath.Join(r.cfg.root, "e2e", "test-results", "runtime-lifecycle")
}

func (r *lifecycle) collectLogs() {
	if r.cfg.remoteHome == "" {
		return
	}
	if err := os.MkdirAll(r.artifactDir(), 0755); err != nil {
		errorf("failed to create artifact dir: %v", err)
		return
	}
	logs := map[string]string{
		"journal.log": `journalctl --user -u bloud-e2e-host-agent.service --no-pager -n 500 || true`,
		"podman.log":  `podman ps -a; podman inspect apps-jellyfin 2>&1 || true`,
		"routes.log":  `test -f "$1/apps-routes.yml" && cat "$1/apps-routes.yml"; true`,
	}
	for name, script := range logs {
		args := []string{}
		if name == "routes.log" {
			args = append(args, r.cfg.traefikDir)
		}
		output, err := r.remoteOutput(script, args...)
		if err != nil {
			output += "\n" + err.Error()
		}
		if err := os.WriteFile(filepath.Join(r.artifactDir(), name), []byte(output), 0644); err != nil {
			errorf("failed to write %s artifact: %v", name, err)
		}
	}
}

func (r *lifecycle) cleanupRemoteDeployment() {
	script := `curl -fsS -X POST -H 'Content-Type: application/json' -d '{"clearData":true}' http://localhost:3000/api/apps/jellyfin/uninstall >/dev/null 2>&1 || true
podman rm -f apps-jellyfin >/dev/null 2>&1 || true
systemctl --user disable --now "$1" >/dev/null 2>&1 || true
rm -f "$2/.config/systemd/user/$1"
if test -f "$3/.bloud-e2e-runtime"; then
  rm -rf "$3"
fi
systemctl --user daemon-reload >/dev/null 2>&1 || true`
	if err := r.remoteRun(script, lifecycleHostAgentUnit, r.cfg.remoteHome, r.cfg.remoteDir); err != nil {
		errorf("failed to clean up remote deployment: %v", err)
	}
}

func (r *lifecycle) prepareQEMUTarget() error {
	if r.cfg.qemu == "" {
		return nil
	}
	r.step("Provisioning QEMU VM")
	bk := backend.NewQEMUBackend(r.cfg.qemu, r.cfg.root)
	if err := bk.Create(context.Background()); err != nil {
		return fmt.Errorf("QEMU VM provisioning failed: %w", err)
	}
	if err := bk.SyncProject(context.Background()); err != nil {
		return fmt.Errorf("QEMU project sync failed: %w", err)
	}
	return nil
}

var remotePreflightScript = `command -v systemctl >/dev/null
command -v podman >/dev/null
command -v curl >/dev/null
test "$(uname -s)" = Linux
systemctl --user show-environment >/dev/null
if test -e "$1" && test ! -f "$1/.bloud-e2e-runtime"; then
  echo "refusing to use unowned runtime directory: $1" >&2
  exit 1
fi
mkdir -p "$1"
touch "$1/.bloud-e2e-runtime"
systemctl --user enable --now podman.socket
podman info >/dev/null`

var remoteInstallHostAgentScript = `unit="$1"
systemctl --user daemon-reload
systemctl --user enable "$unit"
systemctl --user restart "$unit"`

var remoteWaitForHostAgentScript = `deadline=$((SECONDS + 90))
until curl -fsS http://localhost:3000/api/health >/dev/null; do
  if ((SECONDS >= deadline)); then exit 1; fi
  sleep 2
done`

var remoteResetJellyfinScript = `installed="$(curl -sS http://localhost:3000/api/apps/installed || printf '[]')"
if printf '%s' "$installed" | grep -q '"name":"jellyfin"'; then
  curl -sS -X POST -H 'Content-Type: application/json' -d '{"clearData":true}' http://localhost:3000/api/apps/jellyfin/uninstall >/dev/null || true
  deadline=$((SECONDS + 120))
  until ! curl -sS http://localhost:3000/api/apps/installed | grep -q '"name":"jellyfin"'; do
    if ((SECONDS >= deadline)); then exit 1; fi
    sleep 2
  done
fi
podman rm -f apps-jellyfin >/dev/null 2>&1 || true
installed="$(curl -sS http://localhost:3000/api/apps/installed || printf '[]')"
! printf '%s' "$installed" | grep -q '"name":"jellyfin"'`

var remoteInstallJellyfinScript = `http_code="$(curl -sS -o /dev/null -w '%%{http_code}' -X POST -H 'Content-Type: application/json' -d '{}' http://localhost:3000/api/apps/jellyfin/install)"
printf 'install response: %%s\n' "$http_code"
test "$http_code" -ge 200 && test "$http_code" -lt 300
deadline=$((SECONDS + 120))
until curl -sS http://localhost:3000/api/apps/installed | grep -q '"status":"running".*"name":"jellyfin"\|"name":"jellyfin".*"status":"running"'; do
  if ((SECONDS >= deadline)); then echo "timed out waiting for jellyfin to reach running"; exit 1; fi
  sleep 3
done
printf 'jellyfin is running\n'`

var remoteEnsureUserScript = `payload="$1"
status="$(curl -fsS http://localhost:3000/api/setup/status)"
if printf '%s' "$status" | grep -q '"setupRequired":true'; then
  deadline=$((SECONDS + 180))
  until curl -fsS http://localhost:3000/api/setup/status | grep -q '"authentikReady":true'; do
    if ((SECONDS >= deadline)); then exit 1; fi
    sleep 3
  done
  curl -fsS -X POST -H 'Content-Type: application/json' -d "$payload" http://localhost:3000/api/setup/create-user | grep -q '"success":true'
fi`

var remoteAssertInstalledScript = `test "$(podman inspect -f '{{ index .Config.Labels "io.bloud.managed" }}' apps-jellyfin)" = true
test "$(podman inspect -f '{{ index .Config.Labels "io.bloud.app" }}' apps-jellyfin)" = jellyfin
test "$(podman inspect -f '{{ .State.Running }}' apps-jellyfin)" = true
curl -fsS http://localhost:8096/health >/dev/null
curl -fsS http://localhost:3000/api/apps/installed | grep -q '"name":"jellyfin"'
grep -q 'jellyfin-backend' "$1/apps-routes.yml"`

var remoteRestartScript = `podman restart apps-jellyfin
deadline=$((SECONDS + 120))
until curl -fsS http://localhost:8096/health >/dev/null; do
  if ((SECONDS >= deadline)); then exit 1; fi
  sleep 2
done
systemctl --user restart "$1"
deadline=$((SECONDS + 90))
until curl -fsS http://localhost:3000/api/health >/dev/null; do
  if ((SECONDS >= deadline)); then exit 1; fi
  sleep 2
done
test "$(podman inspect -f '{{ .State.Running }}' apps-jellyfin)" = true
curl -fsS http://localhost:3000/api/apps/installed | grep -q '"name":"jellyfin"'`

var remoteUninstallScript = `http_code="$(curl -sS -o /dev/null -w '%%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"clearData":true}' http://localhost:3000/api/apps/jellyfin/uninstall)"
printf 'uninstall response: %%s\n' "$http_code"
test "$http_code" -ge 200 && test "$http_code" -lt 300
deadline=$((SECONDS + 120))
until ! curl -sS http://localhost:3000/api/apps/installed | grep -q '"name":"jellyfin"'; do
  if ((SECONDS >= deadline)); then echo "timed out waiting for jellyfin removal"; exit 1; fi
  sleep 2
done
! podman container exists apps-jellyfin
test ! -e "$1/data/jellyfin"
! grep -q 'jellyfin-backend' "$2/apps-routes.yml"`
