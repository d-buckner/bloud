package main

import (
	"bufio"
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
)

const (
	lifecycleHostAgentUnit = "bloud-e2e-host-agent.service"
)

type lifecycleConfig struct {
	root       string
	lima       string
	sshTarget  string
	envFile    string
	baseURL    string
	remoteDir  string
	goarch     string
	username   string
	password   string
	traefikDir string
	redisAddr  string
	hostOnly   bool
	keep       bool
	remoteHome string
	quadletDir string
}

type lifecycle struct {
	cfg                    lifecycleConfig
	buildDir               string
	failed                 bool
	stoppedComposeJellyfin string
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
		sshTarget:  getenv("BLOUD_E2E_SSH_TARGET"),
		envFile:    getenv("BLOUD_E2E_ENV_FILE"),
		baseURL:    getenv("BLOUD_URL"),
		remoteDir:  getenv("BLOUD_E2E_RUNTIME_DIR"),
		goarch:     getenv("BLOUD_E2E_GOARCH"),
		username:   getenv("BLOUD_E2E_USERNAME"),
		password:   getenv("BLOUD_E2E_PASSWORD"),
		traefikDir: getenv("BLOUD_E2E_TRAEFIK_DYNAMIC_DIR"),
		redisAddr:  getenv("BLOUD_E2E_REDIS_ADDR"),
	}
	if cfg.remoteDir == "" {
		cfg.remoteDir = "/var/tmp/bloud-e2e-runtime"
	}
	if cfg.lima == "" && cfg.sshTarget == "" {
		cfg.lima = "bloud-dev"
	}
	if cfg.envFile == "" && cfg.lima != "" {
		cfg.envFile = filepath.Join(root, "dev", "host-agent.env")
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
	if cfg.lima != "" && cfg.sshTarget != "" {
		return cfg, false, fmt.Errorf("set only one of BLOUD_E2E_LIMA_INSTANCE or BLOUD_E2E_SSH_TARGET")
	}
	if cfg.envFile == "" {
		return cfg, false, fmt.Errorf("BLOUD_E2E_ENV_FILE is required")
	}
	if info, err := os.Stat(cfg.envFile); err != nil || info.IsDir() {
		return cfg, false, fmt.Errorf("BLOUD_E2E_ENV_FILE must point to a readable file")
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
  None for the default Lima target. It uses bloud-dev and dev/host-agent.env.

Optional environment:
  BLOUD_E2E_LIMA_INSTANCE
                         Lima instance name (default: bloud-dev)
  BLOUD_E2E_SSH_TARGET   Use a generic SSH target instead of Lima
  BLOUD_E2E_ENV_FILE     Host-agent environment file for provisioned core services
  BLOUD_URL              Browser-accessible ingress URL
  BLOUD_E2E_RUNTIME_DIR  Remote deployment/data directory (default: /var/tmp/bloud-e2e-runtime)
  BLOUD_E2E_GOARCH       Linux target architecture: amd64 or arm64
  BLOUD_E2E_USERNAME     Browser test user (default: e2etest)
  BLOUD_E2E_PASSWORD     Browser test password (default: e2etest123)
  BLOUD_E2E_REDIS_ADDR   Redis address for host-agent; auto-discovered on Lima
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
		if !r.cfg.keep {
			r.restoreLimaComposeJellyfin()
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
	r.cfg.quadletDir = filepath.Join(r.cfg.remoteHome, ".config", "containers", "systemd")
	if err := r.remoteRun(remotePreflightScript, r.cfg.remoteDir); err != nil {
		return fmt.Errorf("host preflight: %w", err)
	}
	if err := r.prepareLimaTarget(); err != nil {
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
	sanitizedEnv := filepath.Join(r.buildDir, "host-agent.env")
	if err := writeSanitizedLifecycleEnvFile(r.cfg.envFile, sanitizedEnv); err != nil {
		return err
	}
	if err := r.copyFile(sanitizedEnv, r.remotePath("host-agent.env")); err != nil {
		return err
	}
	if err := r.remoteRun("chmod 600 \"$1/host-agent.env\"; chmod 755 \"$1/host-agent/host-agent\"", r.cfg.remoteDir); err != nil {
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
	if err := r.remoteRun(remoteResetJellyfinScript, r.cfg.quadletDir); err != nil {
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
	if err := r.remoteRun(remoteAssertInstalledScript, r.cfg.quadletDir, r.cfg.traefikDir); err != nil {
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
	if err := r.remoteRun(remoteUninstallScript, r.cfg.quadletDir, r.cfg.remoteDir, r.cfg.traefikDir); err != nil {
		return err
	}

	r.failed = false
	r.step("Jellyfin lifecycle passed")
	return nil
}

func renderLifecycleHostAgentUnit(cfg lifecycleConfig) string {
	var extraEnv strings.Builder
	if cfg.redisAddr != "" {
		fmt.Fprintf(&extraEnv, "Environment=BLOUD_REDIS_ADDR=%s\n", cfg.redisAddr)
	}
	return fmt.Sprintf(`[Unit]
Description=Bloud E2E host agent
After=network-online.target podman.socket
Wants=network-online.target podman.socket

[Service]
Type=simple
WorkingDirectory=%s/host-agent
EnvironmentFile=%s/host-agent.env
Environment=BLOUD_SYSTEMD_SCOPE=user
Environment=BLOUD_QUADLET_DIR=%s
Environment=BLOUD_DATA_DIR=%s/data
Environment=BLOUD_APPS_DIR=%s/apps
Environment=BLOUD_TRAEFIK_DYNAMIC_DIR=%s
%s
ExecStart=%s/host-agent/host-agent
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, cfg.remoteDir, cfg.remoteDir, cfg.quadletDir, cfg.remoteDir, cfg.remoteDir, cfg.traefikDir, extraEnv.String(), cfg.remoteDir)
}

func writeSanitizedLifecycleEnvFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer output.Close()

	owned := map[string]struct{}{
		"BLOUD_APPS_DIR":                {},
		"BLOUD_DATA_DIR":                {},
		"BLOUD_PODMAN_SOCKET":           {},
		"BLOUD_QUADLET_DIR":             {},
		"BLOUD_SYSTEMD_SCOPE":           {},
		"BLOUD_TRAEFIK_DYNAMIC_DIR":     {},
		"BLOUD_E2E_LIMA_INSTANCE":       {},
		"BLOUD_E2E_RUNTIME_DIR":         {},
		"BLOUD_E2E_SSH_TARGET":          {},
		"BLOUD_E2E_TRAEFIK_DYNAMIC_DIR": {},
	}
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			fmt.Fprintln(output, line)
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if ok {
			key = strings.TrimSpace(key)
			if _, skip := owned[key]; skip {
				continue
			}
		}
		fmt.Fprintln(output, line)
	}
	return scanner.Err()
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
	name := "ssh"
	if r.cfg.lima != "" {
		name = "limactl"
		commandArgs = append(commandArgs, "shell", "--start", r.cfg.lima, "bash", "-se", "--")
	} else {
		commandArgs = append(commandArgs, r.cfg.sshTarget, "bash", "-se", "--")
	}
	for _, arg := range args {
		if r.cfg.lima != "" {
			commandArgs = append(commandArgs, arg)
		} else {
			commandArgs = append(commandArgs, shellQuote(arg))
		}
	}
	return exec.Command(name, commandArgs...)
}

func (r *lifecycle) copyDirectory(source, destination string) error {
	if r.cfg.lima != "" {
		return r.remoteRun(`rm -rf "$2"
mkdir -p "$2"
cp -a "$1/." "$2/"`, source, destination)
	}
	args := []string{"-a", "--delete", source + string(os.PathSeparator), r.cfg.sshTarget + ":" + shellQuote(destination) + "/"}
	return r.localRun(r.cfg.root, os.Environ(), "rsync", args...)
}

func (r *lifecycle) copyFile(source, destination string) error {
	if r.cfg.lima != "" {
		return r.localRun(r.cfg.root, os.Environ(), "limactl", "copy", source, r.cfg.lima+":"+destination)
	}
	return r.localRun(r.cfg.root, os.Environ(), "rsync", "-a", source, r.cfg.sshTarget+":"+shellQuote(destination))
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
	_ = os.MkdirAll(r.artifactDir(), 0755)
	logs := map[string]string{
		"journal.log": `journalctl --user -u bloud-e2e-host-agent.service -u apps-jellyfin.service --no-pager -n 500 || true`,
		"podman.log":  `podman ps -a; podman inspect apps-jellyfin 2>&1 || true`,
		"quadlet.log": `test -f "$1/apps-jellyfin.container" && cat "$1/apps-jellyfin.container"; true`,
		"routes.log":  `test -f "$1/apps-routes.yml" && cat "$1/apps-routes.yml"; true`,
	}
	for name, script := range logs {
		args := []string{}
		if name == "quadlet.log" {
			args = append(args, r.cfg.quadletDir)
		}
		if name == "routes.log" {
			args = append(args, r.cfg.traefikDir)
		}
		output, err := r.remoteOutput(script, args...)
		if err != nil {
			output += "\n" + err.Error()
		}
		_ = os.WriteFile(filepath.Join(r.artifactDir(), name), []byte(output), 0644)
	}
}

func (r *lifecycle) cleanupRemoteDeployment() {
	script := `curl -fsS -X POST -H 'Content-Type: application/json' -d '{"clearData":true}' http://localhost:3000/api/apps/jellyfin/uninstall >/dev/null 2>&1 || true
systemctl --user stop apps-jellyfin.service >/dev/null 2>&1 || true
podman rm -f apps-jellyfin >/dev/null 2>&1 || true
podman rm -f bloud-e2e-redis >/dev/null 2>&1 || true
rm -f "$3/apps-jellyfin.container"
systemctl --user disable --now "$1" >/dev/null 2>&1 || true
rm -f "$2/.config/systemd/user/$1"
if test -f "$4/.bloud-e2e-runtime"; then
  rm -rf "$4"
fi
systemctl --user daemon-reload >/dev/null 2>&1 || true`
	_ = r.remoteRun(script, lifecycleHostAgentUnit, r.cfg.remoteHome, r.cfg.quadletDir, r.cfg.remoteDir)
}

func (r *lifecycle) prepareLimaTarget() error {
	if r.cfg.lima == "" {
		return nil
	}
	name, err := r.remoteOutput(`podman ps --filter label=com.docker.compose.service=jellyfin --format '{{.Names}}' | head -1`)
	if err != nil {
		return err
	}
	r.stoppedComposeJellyfin = strings.TrimSpace(name)
	if r.cfg.redisAddr == "" {
		if err := r.remoteRun(`if ! timeout 2 bash -c '</dev/tcp/127.0.0.1/6379' 2>/dev/null; then
  podman rm -f bloud-e2e-redis >/dev/null 2>&1 || true
  podman run -d --name bloud-e2e-redis -p 127.0.0.1:6379:6379 docker.io/redis:7-alpine >/dev/null
fi`, r.cfg.root); err != nil {
			return fmt.Errorf("prepare Lima Redis port: %w", err)
		}
		r.cfg.redisAddr = "127.0.0.1:6379"
	}
	if r.stoppedComposeJellyfin == "" {
		return nil
	}
	r.step("Stopping legacy compose-managed Jellyfin")
	return r.remoteRun(`podman stop "$1" >/dev/null`, r.stoppedComposeJellyfin)
}

func (r *lifecycle) restoreLimaComposeJellyfin() {
	if r.stoppedComposeJellyfin == "" {
		return
	}
	fmt.Printf("\n%s==>%s Restoring legacy compose-managed Jellyfin\n", colorGreen, colorReset)
	_ = r.remoteRun(`podman start "$1" >/dev/null`, r.stoppedComposeJellyfin)
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
systemctl --user stop apps-jellyfin.service >/dev/null 2>&1 || true
podman rm -f apps-jellyfin >/dev/null 2>&1 || true
rm -f "$1/apps-jellyfin.container"
systemctl --user daemon-reload
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
test -f "$1/apps-jellyfin.container"
systemctl --user is-active --quiet apps-jellyfin.service
curl -fsS http://localhost:8096/health >/dev/null
curl -fsS http://localhost:3000/api/apps/installed | grep -q '"name":"jellyfin"'
grep -q 'jellyfin-backend' "$2/apps-routes.yml"`

var remoteRestartScript = `systemctl --user restart apps-jellyfin.service
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
systemctl --user is-active --quiet apps-jellyfin.service
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
test ! -e "$1/apps-jellyfin.container"
! systemctl --user is-active --quiet apps-jellyfin.service
test ! -e "$2/data/jellyfin"
! grep -q 'jellyfin-backend' "$3/apps-routes.yml"`
