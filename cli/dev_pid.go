// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

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
