// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"codeberg.org/d-buckner/bloud/cli/backend"
	"codeberg.org/d-buckner/bloud/cli/executor"
)

// localExec runs a command on the host machine
func localExec(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// isSignalExit reports whether err came from a process killed by a signal
// (e.g. Ctrl-C), which interactive commands treat as a clean stop.
func isSignalExit(err error) bool {
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == -1 {
		return true
	}
	return false
}

// rootMarkers are files whose presence identifies the Bloud repo root. They are
// deliberately root-level and stable: keying off a single documentation file
// breaks root resolution every time the docs tree is reorganized.
var rootMarkers = []string{"validation.yaml", "AGENTS.md", "docs/specs/spec.md"}

func getProjectRoot() (string, error) {
	// Find project root by looking for cli/main.go relative to executable or cwd
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// Check if we're in the project root
	if _, err := os.Stat(filepath.Join(cwd, "cli", "main.go")); err == nil {
		return cwd, nil
	}

	// Check if we're in cli directory
	if _, err := os.Stat(filepath.Join(cwd, "main.go")); err == nil {
		return filepath.Dir(cwd), nil
	}

	// Walk up looking for a root marker.
	for dir := cwd; dir != "/"; dir = filepath.Dir(dir) {
		for _, marker := range rootMarkers {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir, nil
			}
		}
	}

	return "", fmt.Errorf("could not find project root (looking for %s)", strings.Join(rootMarkers, ", "))
}

func limaInstance() string {
	if v := os.Getenv("BLOUD_E2E_LIMA_INSTANCE"); v != "" {
		return v
	}
	return "bloud-dev"
}

func qemuInstance() string {
	if v := os.Getenv("BLOUD_QEMU_INSTANCE"); v != "" {
		return v
	}
	return "bloud-qemu"
}

// vmInstance returns the instance name for a backend. Native backends have
// no instance; the label is "native".
func vmInstance(name string) string {
	switch name {
	case "qemu":
		return qemuInstance()
	case "native":
		return "native"
	default:
		return limaInstance()
	}
}

// trustedLocalNetsEnv returns the BLOUD_TRUSTED_LOCAL_NETS value for the
// host-agent. QEMU slirp NAT presents host-forwarded connections from the
// gateway (10.0.2.2), not loopback, so the host-agent must trust that subnet
// for host-side API calls (e.g. ./bloud install, e2e). Lima forwards to
// loopback, so no trusted nets are needed.
func trustedLocalNetsEnv(name string) string {
	if name == "qemu" {
		return "10.0.2.0/24"
	}
	return ""
}

// traefikPortEnv returns the BLOUD_TRAEFIK_PORT override for a backend. Traefik
// defaults to its canonical :80 entrypoint; the native backend runs unprivileged
// (no root, and port 80 may be occupied on the developer machine), so it keeps
// the always-bound 8080 compat entrypoint. The VM backends serve :80 and expose
// it on the host as 8080, so they leave the default in place.
func traefikPortEnv(name string) string {
	if name == "native" {
		return "8080"
	}
	return ""
}

// ssoIssuerURL is the OIDC issuer base URL reachable from inside app
// containers: the SSO subdomain of the base domain, mapped to the host
// gateway via per-app extraHosts. An explicit BLOUD_SSO_ISSUER_URL wins.
func ssoIssuerURL() string {
	if v := os.Getenv("BLOUD_SSO_ISSUER_URL"); v != "" {
		return v
	}
	baseDomain := os.Getenv("BLOUD_BASE_DOMAIN")
	if baseDomain == "" {
		baseDomain = "localhost"
	}
	return fmt.Sprintf("http://sso.%s:8080", baseDomain)
}

// vmLabel is the human-readable backend name for a backend.
func vmLabel(name string) string {
	switch name {
	case "qemu":
		return "QEMU VM"
	case "native":
		return "native host"
	default:
		return "Lima VM"
	}
}

// cmdStart prints usage guidance: the real dev loop is ./bloud dev.
func cmdStart() int {
	name, err := backendName()
	if err != nil {
		errorf("No runtime backend: %v", err)
		return 1
	}

	fmt.Println("Start the dev environment:")
	fmt.Println()
	fmt.Println("  ./bloud dev          Build, deploy to runtime VM, and run host-agent (Ctrl-C to stop)")
	fmt.Println()
	switch name {
	case "qemu":
		fmt.Println("Prerequisites (QEMU backend):")
		fmt.Println("  ./bloud dev   # provisions .bloud/qemu/bloud-qemu, boots VM")
	case "native":
		fmt.Println("Prerequisites (native backend):")
		fmt.Println("  ./bloud setup  # checks prerequisites; ./bloud dev runs on this host")
	default:
		fmt.Println("Prerequisites (Lima backend):")
		fmt.Println("  limactl create --name=bloud-dev dev/lima.yaml")
		fmt.Println("  limactl start bloud-dev")
	}
	return 0
}

func cmdStop() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	log("Stopping host-agent on " + inst)
	err = bk.Host().Executor().RunStream(context.Background(), executor.RunSpec{
		Command: `pkill -f 'host-agent$' 2>/dev/null; systemctl --user stop apps-*.service 2>/dev/null; true`,
	}, os.Stdout, os.Stderr)
	if err != nil && !isSignalExit(err) {
		errorf("Failed to stop host-agent: %v", err)
		return 1
	}
	log("Stopped")
	return 0
}

func cmdStatus() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	fmt.Println()
	fmt.Printf("  %s:  %s\n", vmLabel(name), inst)

	host := bk.Host()

	// Check if VM is running
	if host.Ready() {
		fmt.Printf("  VM status: %sRunning%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  VM status: %sStopped%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Start the VM with: ./bloud dev")
		return 0
	}

	// Check host-agent
	res, err := host.Executor().Run(context.Background(), executor.RunSpec{
		Command: `curl -sf http://localhost:3000/api/health 2>/dev/null && echo ok || echo down`,
	})
	if err == nil && strings.Contains(res.Stdout, "ok") {
		fmt.Printf("  Host agent: %sRunning%s (localhost:3000)\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Host agent: %sNot running%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Run: ./bloud dev")
	}

	fmt.Println()
	return 0
}

func cmdLogs() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	log("Streaming host-agent logs from " + inst + " (Ctrl-C to stop)...")
	err = bk.Host().Executor().RunStream(context.Background(), executor.RunSpec{
		Command: `journalctl --user -u host-agent -f 2>/dev/null || journalctl -f 2>/dev/null`,
	}, os.Stdout, os.Stderr)
	if err != nil && !isSignalExit(err) {
		errorf("Failed to stream logs: %v", err)
		return 1
	}
	return 0
}

func cmdAttach() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	sshex, ok := bk.Host().Executor().(*executor.SSHExecutor)
	if !ok {
		errorf("Backend host does not support interactive shells")
		return 1
	}
	log("Opening shell on " + inst + " (type 'exit' to leave)...")
	if err := sshex.InteractiveShell(context.Background(), os.Stdout, os.Stderr, os.Stdin); err != nil && !isSignalExit(err) {
		errorf("Failed to open shell on "+inst+": %v", err)
		return 1
	}
	return 0
}

func cmdShell(args []string) int {
	if len(args) == 0 {
		return cmdAttach()
	}
	bk, _, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	command := strings.Join(args, " ")
	if err := bk.Host().Executor().RunStream(context.Background(), executor.RunSpec{
		Command: command,
	}, os.Stdout, os.Stderr); err != nil && !isSignalExit(err) {
		errorf("Command failed: %v", err)
		return 1
	}
	return 0
}

func cmdRebuild() int {
	fmt.Println("'rebuild' is not supported (Nix runtime was removed).")
	fmt.Println()
	fmt.Println("To pick up code changes, re-run: ./bloud dev")
	return 0
}

func cmdServices() int {
	bk, _, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	err = bk.Host().Executor().RunStream(context.Background(), executor.RunSpec{
		Command: `podman ps --all --filter 'name=^apps-' --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'`,
	}, os.Stdout, os.Stderr)
	if err != nil && !isSignalExit(err) {
		errorf("Failed to list services: %v", err)
		return 1
	}
	return 0
}

// parseYesFlag reads the confirmation-skipping -y/--yes flag out of a
// destructive command's args. Anything else is a typo, not a value to ignore:
// silently accepting an unknown flag on a command that deletes data means the
// flag you thought you passed (and the safety it was meant to carry) never
// took effect.
func parseYesFlag(cmdName string, args []string) (bool, error) {
	yes := false
	for _, arg := range args {
		switch arg {
		case "-y", "--yes":
			yes = true
		default:
			return false, fmt.Errorf("unknown %s flag %q (expected -y or --yes)", cmdName, arg)
		}
	}
	return yes, nil
}

// confirmDestructive asks a yes/no question before something destructive and
// reads the answer from r. Only a bare "y" (any case) confirms: an empty
// answer, a typo, or EOF (a non-interactive stdin) all abort, so a scripted
// run cannot wipe a runtime by default just because nobody was there to
// answer. Use -y/--yes where the answer is known in advance.
func confirmDestructive(w io.Writer, r *bufio.Reader, question string) bool {
	_, _ = fmt.Fprint(w, question)
	resp, _ := r.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(resp), "y")
}

func cmdReset(args []string) int {
	yes, err := parseYesFlag("reset", args)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}

	if !yes {
		prompt := fmt.Sprintf("This will stop all services and wipe all app data in '%s'.\n"+
			"The VM itself is kept: only data, containers, and the database are removed.\n"+
			"Continue? [y/N] ", vmInstance(name))
		if !confirmDestructive(os.Stdout, stdinReader, prompt) {
			fmt.Println("Aborted.")
			return 0
		}
	}

	if err := resetRuntime(bk, name); err != nil {
		errorf("%v", err)
		return 1
	}

	log("Reset complete: run ./bloud dev to start fresh")
	return 0
}

// resetRuntime wipes the dev runtime: host-agent, app units, every container,
// and the data dir that holds the database. The VM itself is kept.
//
// Consent is the caller's job. cmdReset prompts; --reset on ./bloud dev is
// already an explicit request, so the same wipe runs unattended there.
func resetRuntime(bk backend.Backend, name string) error {
	host := bk.Host()
	ex := host.Executor()
	dirs := host.DataDirs()
	ctx := context.Background()

	// 1. Kill host-agent and any app systemd units
	log("Stopping host-agent")
	if err := ex.RunStream(ctx, executor.RunSpec{
		Command: `pkill -f 'host-agent$' 2>/dev/null; systemctl --user stop 'apps-*.service' 'bloud-e2e-host-agent.service' 2>/dev/null; true`,
	}, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("failed to stop host-agent: %w", err)
	}

	// 2. Remove all containers
	log("Removing containers")
	if err := ex.RunStream(ctx, executor.RunSpec{
		Command: `
set -e
podman rm -f $(podman ps -aq) 2>/dev/null || true
podman system prune -f 2>/dev/null || true
`,
	}, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("failed to stop services: %w", err)
	}

	// 3. Wipe data directories and database. The runtime data dir is
	// backend-specific (Lima: /var/tmp/bloud-dev-runtime/data, QEMU:
	// /var/tmp/bloud-qemu-runtime/data); the bloud.db database lives inside
	// it (BLOUD_DATA_DIR). Use podman unshare for dirs with container-owned
	// files (e.g. postgres).
	//
	// $HOME/.local/share/bloud is a real, non-disposable path on native.
	homeWipe := ""
	if name != "native" {
		homeWipe = "podman unshare rm -rf \"$HOME/.local/share/bloud\"\n"
	}
	wipe := fmt.Sprintf("set -e\n%spodman unshare rm -rf %s\nrm -f %s/bloud.db\n", homeWipe, dirs.DataDir, dirs.DataDir)
	log("Wiping data")
	if err := ex.RunStream(ctx, executor.RunSpec{
		Command: wipe,
	}, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("failed to wipe data: %w", err)
	}

	return nil
}

func cmdDestroy() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	fmt.Printf("This will stop and delete the %s '%s'.\n", vmLabel(name), inst)
	if !confirmDestructive(os.Stdout, stdinReader, "Continue? [y/N] ") {
		fmt.Println("Aborted.")
		return 0
	}
	if err := bk.Destroy(context.Background()); err != nil {
		errorf("Failed to delete VM: %v", err)
		return 1
	}
	log("VM deleted")
	return 0
}

func cmdInstall(args []string) int {
	if len(args) < 1 {
		errorf("Usage: ./bloud install <app-name>")
		return 1
	}
	return installApp(3000, args[0])
}

func cmdUninstall(args []string) int {
	if len(args) < 1 {
		errorf("Usage: ./bloud uninstall <app-name>")
		return 1
	}
	return uninstallApp(3000, args[0])
}

// installApp calls the host-agent API to install an app
func installApp(apiPort int, appName string) int {
	log(fmt.Sprintf("Installing %s...", appName))

	token, err := readAPIToken(context.Background())
	if err != nil {
		errorf("Could not read the host-agent API token: %v", err)
		return 1
	}

	curlCmd := fmt.Sprintf(`curl -s -X POST %s -w "\n%%{http_code}" http://localhost:%d/api/apps/%s/install`,
		authHeader(token), apiPort, appName)
	output, err := LocalExec(curlCmd)
	if err != nil {
		errorf("Failed to call install API: %v", err)
		return 1
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 1 {
		errorf("Empty response from API")
		return 1
	}

	httpCode := lines[len(lines)-1]
	responseBody := strings.Join(lines[:len(lines)-1], "\n")

	if httpCode != "200" && httpCode != "201" && httpCode != "202" {
		errorf("Install failed (HTTP %s): %s", httpCode, responseBody)
		return 1
	}

	log(fmt.Sprintf("Successfully installed %s", appName))
	fmt.Println(responseBody)
	return 0
}

// devBackend builds the selected backend for the current project and
// returns its name.
func devBackend() (backend.Backend, string, error) {
	root, err := getProjectRoot()
	if err != nil {
		return nil, "", err
	}
	name, err := backendName()
	if err != nil {
		return nil, "", err
	}
	switch name {
	case "qemu":
		return backend.NewQEMUBackend(qemuInstance(), root), name, nil
	case "native":
		return backend.NewNativeBackend(root), name, nil
	default:
		return backend.NewLimaBackend(limaInstance(), root), name, nil
	}
}

// stopPreviousHostAgentCommand returns a shell command that stops a previous
// dev host-agent: whatever holds the API port, and whatever is executing the
// deployed binary. The binary match is what prevents "text file busy" when the
// new build is copied over it, and unlike a name-based pkill (host-agent runs
// as "./host-agent") it cannot hit an unrelated process. It waits, bounded,
// for the binary to be released. Every step is best-effort: nothing running is
// the normal first-run case.
func stopPreviousHostAgentCommand(apiPort, binaryPath string) string {
	return "fuser -k " + apiPort + "/tcp >/dev/null 2>&1 || true; " +
		"fuser -k " + binaryPath + " >/dev/null 2>&1 || true; " +
		"for i in 1 2 3 4 5 6 7 8 9 10; do fuser -s " + binaryPath + " >/dev/null 2>&1 || break; sleep 0.5; done; " +
		"sleep 0.5"
}

// stopPreviousViteCommand returns a shell command that frees the dashboard's
// dev-server port before a new vite is started.
//
// Without this, a vite left behind by an earlier run keeps the port and the
// new one silently moves to the next port number, while the host-agent keeps
// proxying to the port it was told about. The developer then browses the
// stale server: a process from an hour ago, whose working directory may no
// longer exist, answering 404 for every route on the dashboard. That is a
// routing failure that reads as a broken frontend, so the sweep happens
// before anything is started rather than being left to the shutdown path.
//
// TERM first, then KILL: vite holds no state worth a graceful exit, but a
// TERM gives it the chance to unbind the port on its own, which is faster
// than waiting out the grace window. Every step is best-effort; a free port
// is the normal first-run case and must not be an error.
func stopPreviousViteCommand(port int) string {
	p := strconv.Itoa(port)
	return "fuser -k -TERM " + p + "/tcp >/dev/null 2>&1 || true; " +
		"for i in 1 2 3 4 5 6 7 8 9 10; do fuser -s " + p + "/tcp >/dev/null 2>&1 || break; sleep 0.5; done; " +
		"fuser -k -KILL " + p + "/tcp >/dev/null 2>&1 || true; " +
		"sleep 0.5"
}

// runLocalCommand runs a shell command on the developer's own machine and
// streams its output. The dev-loop pre-flight checks are local-process
// operations, not guest operations, so they do not go through the backend's
// executor.
func runLocalCommand(ctx context.Context, script string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// devLoopPIDFile names the file a hot-reload loop records its own pid in.
const devLoopPIDFile = "bloud-dev.pid"

// devLoopPIDPath is where a running hot-reload loop leaves its pid.
func devLoopPIDPath(runDir string) string {
	return filepath.Join(runDir, devLoopPIDFile)
}

// takeoverPreviousDevLoop stops a hot-reload loop left running by an earlier
// `./bloud dev`, so a second invocation replaces the first instead of
// stacking beside it. The bool reports whether there was a live loop to stop.
//
// The loop is stopped by pid rather than by port because it is the loop that
// owns the vite and host-agent children this run is about to start: a TERM
// lands on the loop's own shutdown path, which takes its children down in
// order. Killing the port holders alone would leave the old loop alive and
// watching, and its crash-restart handler would immediately bring a second
// vite back on the next free port. The port sweep still runs afterwards to
// catch orphans that outlived a loop which was killed too hard to clean up.
//
// A pid is only killed when it verifiably belongs to a `bloud dev` process.
// Pids get reused, and this one was read from a file written by a run that
// may have died at any point.
func takeoverPreviousDevLoop(pidPath string) (bool, error) {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", pidPath, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		_ = os.Remove(pidPath)
		return false, nil
	}
	if !isBloudDevProcess(pid) {
		_ = os.Remove(pidPath)
		return false, nil
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			_ = os.Remove(pidPath)
			return false, nil
		}
		return false, fmt.Errorf("signal previous dev loop %d: %w", pid, err)
	}

	for i := 0; i < 20; i++ {
		if syscall.Kill(pid, 0) != nil {
			_ = os.Remove(pidPath)
			return true, nil
		}
		time.Sleep(250 * time.Millisecond)
	}

	_ = syscall.Kill(pid, syscall.SIGKILL)
	_ = os.Remove(pidPath)
	return true, nil
}

// isBloudDevProcess reports whether pid is a bloud CLI running the dev
// command.
//
// The argv scan rather than argv[0] alone because the kernel prepends the
// interpreter when a script is executed: a `bloud` shell wrapper shows up as
// `/bin/sh ./bloud dev`, and the real compiled CLI as `./bloud dev`. Both
// are the loop we mean, so the test is "an argument whose basename is `bloud`
// is immediately followed by `dev`".
//
// An unreadable cmdline is answered as "not ours": refusing to kill an
// unidentified process is the safe direction to fail, and the port sweep
// covers the case this misses.
func isBloudDevProcess(pid int) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	args := strings.Split(string(raw), "\x00")
	for i := 0; i+1 < len(args); i++ {
		if filepath.Base(args[i]) == "bloud" && args[i+1] == "dev" {
			return true
		}
	}
	return false
}

// writeDevLoopPID records this process as the active dev loop, so the next
// invocation can find and replace it.
func writeDevLoopPID(pidPath string) error {
	return os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// devFlags are the options the dev loop understands.
type devFlags struct {
	// watch selects the hot-reload loop. It is the default because the
	// one-shot loop is the slow and destructive one; --no-watch asks for it
	// explicitly.
	watch bool
	// reset wipes the runtime (the same wipe as `./bloud reset -y`) before
	// the dev loop starts, so the stack comes up from empty data.
	reset bool
	// verbose streams the raw subprocess output to the console instead of
	// quieting it to the dev log. It is the escape hatch for "the summary is
	// not enough", and it changes nothing about what gets logged.
	verbose bool
}

// parseDevFlags parses the dev command's flags. Unknown flags are an error:
// --reset is destructive, so a typo that silently failed to wipe would leave
// the caller believing they were starting from a clean runtime.
func parseDevFlags(args []string) (devFlags, error) {
	flags := devFlags{watch: true}
	for _, arg := range args {
		switch arg {
		case "--watch":
			flags.watch = true
		case "--no-watch":
			flags.watch = false
		case "--reset":
			flags.reset = true
		case "-v", "--verbose":
			flags.verbose = true
		default:
			return flags, fmt.Errorf("unknown dev flag %q (expected --watch, --no-watch, --reset, or --verbose)", arg)
		}
	}
	if v := os.Getenv("BLOUD_DEV_VERBOSE"); v == "1" || strings.EqualFold(v, "true") {
		flags.verbose = true
	}
	return flags, nil
}

// cmdDev is the whole dev loop. By default it hot-reloads: the host-agent is
// rebuilt and restarted on save while the containers it manages are left
// running, and the dashboard is served by a vite dev server that hot-reloads
// on save. --no-watch runs the old one-shot build-deploy-foreground instead.
// --reset wipes the runtime first (the same wipe as ./bloud reset -y, run
// without a prompt because the flag is the consent) and then starts the loop.
func cmdDev(args []string) int {
	flags, err := parseDevFlags(args)
	if err != nil {
		errorf("%v", err)
		return 1
	}

	root, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}

	// The console is built here rather than inside each loop so both paths
	// share one log file, one header, and one close.
	c, err := newDevConsole(os.Stdout, devLogPath(root), flags.verbose)
	if err != nil {
		errorf("could not open the dev log: %v", err)
		return 1
	}
	defer func() { _ = c.Close() }()
	c.Header(vmLabel(name), fmt.Sprintf("http://localhost:%s", portOr(bk.Host().Ports(), "traefik", defaultTraefikPort)))

	if flags.watch {
		// Hot reload needs the CLI to own the host-agent as a child process it
		// can stop and start. That is only true on the native backend, where
		// the CLI and the host-agent share a machine; over SSH the same loop
		// means remote process supervision and a file copy per reload, which is
		// not what was wired. Say so and fall through rather than silently
		// running something different from what was asked for.
		if name == "native" {
			return runDevWatch(root, bk, flags, c)
		}
		c.Note("hot reload is wired for the native backend only; running the one-shot loop")
	}

	return runDevOnce(root, bk, name, flags, c)
}

// resetIfRequested runs the full runtime wipe when --reset was passed. The
// loops call it after provisioning, not before: on a machine whose runtime
// does not exist yet, the wipe's commands have nowhere to execute, so
// resetting first would turn `./bloud dev --reset` into a failure instead of
// a fresh start.
func resetIfRequested(flags devFlags, bk backend.Backend, name string) error {
	if !flags.reset {
		return nil
	}
	log("--reset: wiping the runtime before starting")
	if err := resetRuntime(bk, name); err != nil {
		return fmt.Errorf("reset before dev: %w", err)
	}
	return nil
}

// runDevWatch brings up the hot-reload loop on a native runtime.
func runDevWatch(root string, bk backend.Backend, flags devFlags, c *devConsole) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	host := bk.Host()
	ex := host.Executor()
	// Create is a no-op when the runtime is already up, so the step is quick
	// on every launch after the first.
	provStart := time.Now()
	if err := bk.Create(ctx); err != nil {
		c.StepFailed("provision runtime", "", time.Since(provStart), err)
		return 1
	}
	c.Step("provision runtime", "", time.Since(provStart))

	if flags.reset {
		resetStart := time.Now()
		if err := resetRuntime(bk, "native"); err != nil {
			c.StepFailed("wipe runtime", "--reset", time.Since(resetStart), err)
			return 1
		}
		c.Step("wipe runtime", "--reset", time.Since(resetStart))
	}

	dirs := host.DataDirs()
	if !clearDevLoopConflicts(ctx, ex, dirs, c) {
		return 1
	}

	env := devRunEnv(dirs, "native")
	env["BLOUD_DEV_VITE_URL"] = fmt.Sprintf("http://%s:%d", devViteHost, devVitePort)
	env["BLOUD_DEV_FAST_GATE"] = "1"

	pidPath := devLoopPIDPath(dirs.HostAgentDir)
	if err := writeDevLoopPID(pidPath); err != nil {
		c.StepFailed("record the dev loop pid", "", 0, err)
		return 1
	}
	defer func() { _ = os.Remove(pidPath) }()

	// The API opens only once the host-agent is listening, which is the one
	// bring-up fact the steps above cannot report. Watching for it from the
	// side is what turns "host-agent started" into "open your browser".
	go announceHostAgentReady(ctx, ex, c, host.Ports(), readyPollInterval, devQuietProgressFirst)

	return runHotReload(ctx, hotReloadOptions{
		root:       root,
		binaryPath: dirs.HostAgentDir + "/host-agent",
		runDir:     dirs.HostAgentDir,
		env:        env,
		console:    c,
	})
}

// clearDevLoopConflicts removes everything a previous run leaves in this
// loop's way. It reports on the console itself and returns false rather than
// an error: each step carries its own label, and the caller only ever stops.
func clearDevLoopConflicts(ctx context.Context, ex executor.Executor, dirs executor.DataDirs, c *devConsole) bool {
	pidPath := devLoopPIDPath(dirs.HostAgentDir)
	// A reload must never wipe the stack it is reloading, so the managed-
	// container sweep the one-shot loop runs is deliberately absent here.
	// Only the pre-catalog legacy names are cleared: nothing reconciles those,
	// so a leftover from an old compose stack would squat on a port forever.
	// Housekeeping like this runs on every launch and is invisible unless it
	// fails, so it writes to the log rather than the console.
	quiet := c.QuietStream()
	if err := ex.RunStream(ctx, executor.RunSpec{
		Command: `podman rm -f bloud-dev-postgres bloud-dev-redis dev_authentik-worker_1 dev_authentik-proxy_1 2>/dev/null; true`,
	}, quiet, quiet); err != nil && ctx.Err() == nil {
		c.StepFailed("clear pre-catalog legacy containers", "", 0, err)
		return false
	}

	// A previous loop owns the vite and host-agent children this run is about
	// to start. Take it over first so its shutdown path releases those in
	// order, rather than leaving a second loop watching the same ports and
	// respawning a competing dev server.
	tookOver, err := takeoverPreviousDevLoop(pidPath)
	if err != nil {
		c.StepFailed("take over the previous dev loop", "", 0, err)
		return false
	}
	if tookOver {
		c.Note("replacing the dev loop already running (pid file: " + pidPath + ")")
	}

	// Stop a host-agent left behind by an earlier run, or the new one cannot
	// bind its port and the two fight over the same runtime dir.
	if err := ex.RunStream(ctx, executor.RunSpec{
		Command: stopPreviousHostAgentCommand("3000", dirs.HostAgentDir+"/host-agent"),
	}, quiet, quiet); err != nil && ctx.Err() == nil {
		c.StepFailed("stop the previous host-agent", "", 0, err)
		return false
	}

	// vite runs on the developer's machine, outside the loop's runtime dir,
	// so a previous run's vite can outlive it and keep the dev-server port.
	// Clear the port before starting ours: the host-agent is told to proxy the
	// dashboard at a fixed port, and a stale server sitting on it serves the
	// dashboard from a checkout that may no longer exist.
	if err := runLocalCommand(ctx, stopPreviousViteCommand(devVitePort)); err != nil && ctx.Err() == nil {
		c.StepFailed("free the dashboard dev-server port", strconv.Itoa(devVitePort), 0, err)
		return false
	}
	return true
}

// devRunEnv is the host-agent's dev environment. The same map feeds the
// one-shot and the hot-reload paths, so the two cannot drift into running with
// different configuration.
func devRunEnv(dirs executor.DataDirs, name string) map[string]string {
	env := map[string]string{
		"BLOUD_DATA_DIR":            dirs.DataDir,
		"BLOUD_APPS_DIR":            dirs.AppsDir,
		"BLOUD_TRAEFIK_DYNAMIC_DIR": dirs.DataDir + "/traefik/dynamic",
		"BLOUD_TRAEFIK_PORT":        traefikPortEnv(name),
		"BLOUD_TRUSTED_LOCAL_NETS":  trustedLocalNetsEnv(name),
		"BLOUD_SSO_ISSUER_URL":      ssoIssuerURL(),
	}
	for k, v := range devPassthrough(os.Getenv) {
		env[k] = v
	}
	return env
}

// runDevOnce is the one-shot loop: build, deploy, run in the foreground.
func runDevOnce(root string, bk backend.Backend, name string, flags devFlags, c *devConsole) int {
	host := bk.Host()
	ex := host.Executor()
	dirs := host.DataDirs()
	goarch := runtime.GOARCH
	// Most of this loop is routine work whose output only matters when it
	// fails, so it is mirrored to the dev log rather than the console.
	quiet := c.QuietStream()
	ctx := context.Background()

	if err := devProvision(ctx, bk, name, flags, c); err != nil {
		return 1
	}
	if err := devClearContainers(ctx, ex, c); err != nil {
		return 1
	}

	tmpDir, err := os.MkdirTemp("", "bloud-dev-build-*")
	if err != nil {
		c.StepFailed("create the build dir", "", 0, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	binaryPath, err := devBuildHostAgent(root, tmpDir, goarch, c)
	if err != nil {
		return 1
	}
	if err := devBuildDashboard(root, c); err != nil {
		return 1
	}

	// Stop any previous host-agent before deploying: copying over a running
	// binary fails with "text file busy" (and by now its containers are gone).
	if err := ex.RunStream(ctx, executor.RunSpec{
		Command: stopPreviousHostAgentCommand("3000", dirs.HostAgentDir+"/host-agent"),
	}, quiet, quiet); err != nil {
		c.StepFailed("stop the previous host-agent", "", 0, err)
		return 1
	}

	if err := devDeployHostAgent(ctx, ex, dirs, binaryPath, c); err != nil {
		return 1
	}
	if err := devDeployDashboard(ctx, ex, dirs, filepath.Join(root, devWebBuildRel), c); err != nil {
		return 1
	}

	c.Blank()
	c.Note("running in the foreground · Ctrl-C to stop")
	c.HintLogPath()
	c.Blank()

	if err := devRunForeground(ex, host, dirs, name, c); err != nil {
		return 1
	}
	return 0
}

// devRunForeground runs the host-agent in the foreground until it exits or a
// signal arrives. The SSO issuer URL is derived (see ssoIssuerURL); all other
// configuration resolves through env vars, secrets.json, and the host-agent's
// dev fallbacks.
func devRunForeground(ex executor.Executor, host executor.Host, dirs executor.DataDirs, name string, c *devConsole) error {
	agentTee := c.HostAgentStream()
	// The host-agent opens its API only after every installed app is up, so
	// watch for that and say so instead of leaving the terminal silent.
	readyCtx, stopReadyWatch := context.WithCancel(context.Background())
	go announceHostAgentReady(readyCtx, ex, c, host.Ports(), readyPollInterval, devQuietProgressFirst)
	runErr := ex.RunStream(context.Background(), executor.RunSpec{
		Command: "unset DATABASE_URL; exec ./host-agent",
		Dir:     dirs.HostAgentDir,
		Env:     devRunEnv(dirs, name),
	}, agentTee, agentTee)
	stopReadyWatch()
	if runErr != nil && !isSignalExit(runErr) {
		c.StepFailed("host-agent", "", 0, runErr)
		return runErr
	}
	return nil
}

// devWebBuildRel is where the dashboard build lands, relative to the repo root.
const devWebBuildRel = "services/host-agent/web/build"

// devProvision brings the runtime up and wipes it if asked. Provisioning is a
// no-op when the guest is already running (Lima: created and started; QEMU:
// image and seed present and the guest reachable), so it is safe for both
// backends on every launch.
func devProvision(ctx context.Context, bk backend.Backend, name string, flags devFlags, c *devConsole) error {
	start := time.Now()
	if err := bk.Create(ctx); err != nil {
		c.StepFailed("provision runtime", "", time.Since(start), err)
		return err
	}
	c.Step("provision runtime", "", time.Since(start))

	if !flags.reset {
		return nil
	}
	resetStart := time.Now()
	if err := resetIfRequested(flags, bk, name); err != nil {
		c.StepFailed("wipe runtime", "--reset", time.Since(resetStart), err)
		return err
	}
	c.Step("wipe runtime", "--reset", time.Since(resetStart))
	return nil
}

// devClearContainers gives the host-agent a clean slate before it takes over.
// It also removes the stale legacy dev containers (bloud-dev-postgres,
// bloud-dev-redis, dev_* compose names) that predate the host-agent's
// self-bootstrap. There is no shared postgres/redis compose stack anymore: apps
// own their infra containers (e.g. apps-authentik-postgres) through their
// metadata.yaml containers blocks, so the host-agent is the single manager.
// apps-traefik is included because it uses host network and holds port 80.
func devClearContainers(ctx context.Context, ex executor.Executor, c *devConsole) error {
	quiet := c.QuietStream()
	err := ex.RunStream(ctx, executor.RunSpec{
		Command: `podman rm -f bloud-dev-postgres bloud-dev-redis apps-traefik dev_authentik-worker_1 dev_authentik-proxy_1 apps-authentik-ldap apps-authentik-server 2>/dev/null; podman ps -a --filter label=io.bloud.managed=true -q | xargs -r podman rm -f -t 2 2>/dev/null; true`,
	}, quiet, quiet)
	if err != nil {
		c.StepFailed("stop managed app containers", "", 0, err)
	}
	return err
}

// devBuildHostAgent cross-compiles the host-agent for the guest into tmpDir and
// returns the binary's path.
func devBuildHostAgent(root, tmpDir, goarch string, c *devConsole) (string, error) {
	binaryPath := filepath.Join(tmpDir, "host-agent")
	tee := c.QuietStream()
	start := time.Now()
	cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/host-agent")
	cmd.Dir = filepath.Join(root, "services", "host-agent")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch)
	cmd.Stdout = tee
	cmd.Stderr = tee
	if err := cmd.Run(); err != nil {
		c.StepFailed("build host-agent", "linux/"+goarch, time.Since(start), err)
		c.PrintTail(tee.Tail(devTailLines))
		return "", err
	}
	c.Step("build host-agent", "linux/"+goarch, time.Since(start))
	return binaryPath, nil
}

// devBuildDashboard produces the static bundle host-agent serves.
func devBuildDashboard(root string, c *devConsole) error {
	tee := c.QuietStream()
	start := time.Now()
	cmd := exec.Command("npm", "run", "build", "--workspace=@bloud/host-agent-web")
	cmd.Dir = root
	cmd.Stdout = tee
	cmd.Stderr = tee
	if err := cmd.Run(); err != nil {
		c.StepFailed("build dashboard", "", time.Since(start), err)
		c.PrintTail(tee.Tail(devTailLines))
		return err
	}
	c.Step("build dashboard", "", time.Since(start))
	return nil
}

// devDeployHostAgent puts the binary in the runtime dir and marks it runnable.
func devDeployHostAgent(ctx context.Context, ex executor.Executor, dirs executor.DataDirs, binaryPath string, c *devConsole) error {
	quiet := c.QuietStream()
	start := time.Now()
	fail := func(err error) error {
		c.StepFailed("deploy host-agent", dirs.HostAgentDir, time.Since(start), err)
		return err
	}
	if err := ex.RunStream(ctx, executor.RunSpec{Command: "mkdir -p " + dirs.HostAgentDir}, quiet, quiet); err != nil {
		return fail(err)
	}
	if err := ex.CopyTo(ctx, binaryPath, dirs.HostAgentDir+"/host-agent"); err != nil {
		return fail(err)
	}
	if err := ex.RunStream(ctx, executor.RunSpec{Command: "chmod 755 " + dirs.HostAgentDir + "/host-agent"}, quiet, quiet); err != nil {
		return fail(err)
	}
	c.Step("deploy host-agent", dirs.HostAgentDir, time.Since(start))
	return nil
}

// devDeployDashboard replaces the bundle the runtime serves. A build that was
// never produced is not a failure here: host-agent falls back to its embedded
// placeholder page, which is the honest thing to show when there is no bundle.
func devDeployDashboard(ctx context.Context, ex executor.Executor, dirs executor.DataDirs, webBuildDir string, c *devConsole) error {
	if _, statErr := os.Stat(webBuildDir); statErr != nil {
		return nil
	}
	quiet := c.QuietStream()
	start := time.Now()
	fail := func(err error) error {
		c.StepFailed("deploy dashboard", "", time.Since(start), err)
		return err
	}
	if err := ex.RunStream(ctx, executor.RunSpec{Command: "rm -rf " + dirs.HostAgentDir + "/web/build"}, quiet, quiet); err != nil {
		return fail(err)
	}
	if err := ex.RunStream(ctx, executor.RunSpec{Command: "mkdir -p " + dirs.HostAgentDir + "/web"}, quiet, quiet); err != nil {
		return fail(err)
	}
	if err := ex.CopyTo(ctx, webBuildDir, dirs.HostAgentDir+"/web/build"); err != nil {
		return fail(err)
	}
	c.Step("deploy dashboard", "", time.Since(start))
	return nil
}

// uninstallApp calls the host-agent API to uninstall an app
func uninstallApp(apiPort int, appName string) int {
	log(fmt.Sprintf("Uninstalling %s...", appName))

	token, err := readAPIToken(context.Background())
	if err != nil {
		errorf("Could not read the host-agent API token: %v", err)
		return 1
	}

	curlCmd := fmt.Sprintf(`curl -s -X POST %s -w "\n%%{http_code}" http://localhost:%d/api/apps/%s/uninstall`,
		authHeader(token), apiPort, appName)
	output, err := LocalExec(curlCmd)
	if err != nil {
		errorf("Failed to call uninstall API: %v", err)
		return 1
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 1 {
		errorf("Empty response from API")
		return 1
	}

	httpCode := lines[len(lines)-1]
	responseBody := strings.Join(lines[:len(lines)-1], "\n")

	if httpCode != "200" && httpCode != "202" {
		errorf("Uninstall failed (HTTP %s): %s", httpCode, responseBody)
		return 1
	}

	log(fmt.Sprintf("Successfully uninstalled %s", appName))
	fmt.Println(responseBody)
	return 0
}
