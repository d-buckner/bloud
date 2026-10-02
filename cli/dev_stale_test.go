// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestPortHolderHelper is not a test: it is the child process that the port
// sweep tests re-exec in order to own a TCP port. It exits the test binary's
// normal run unless BLOUD_TEST_BIND_PORT names the port to hold.
func TestPortHolderHelper(t *testing.T) {
	port := os.Getenv("BLOUD_TEST_BIND_PORT")
	if port == "" {
		t.Skip("helper process; only runs when BLOUD_TEST_BIND_PORT is set")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("bind 127.0.0.1:%s: %v", port, err)
	}
	defer func() { _ = ln.Close() }()
	fmt.Println("bound")
	select {}
}

// startPortHolder re-execs this test binary as a process holding the given
// port, and waits until the port really answers, so a sweep run after it is
// racing a live listener and not a process that has not bound yet.
func startPortHolder(t *testing.T, port string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestPortHolderHelper")
	cmd.Env = append(os.Environ(), "BLOUD_TEST_BIND_PORT="+port)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start port holder: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_, _ = cmd.Process.Wait()
	})
	if err := waitForPortListening(port, 10*time.Second); err != nil {
		t.Fatalf("port %s never came up: %v", port, err)
	}
	return cmd
}

func waitForPortListening(port string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("nothing listening on 127.0.0.1:%s after %s", port, timeout)
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	return port
}

func portInUse(port string) bool {
	out, err := exec.Command("sh", "-c", "fuser -s "+port+"/tcp >/dev/null 2>&1; echo $?").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "0"
}

// The dev loop must clear the dashboard dev-server port before starting its
// own vite. A stale vite left on that port keeps the host-agent's fixed proxy
// target, so the browser gets the dashboard served by a process from a
// previous run, whose working directory may already be gone.
func TestStopPreviousViteCommandFreesPort(t *testing.T) {
	if _, err := exec.LookPath("fuser"); err != nil {
		t.Skip("fuser not installed")
	}
	port := freePort(t)
	holder := startPortHolder(t, port)
	defer holder.Process.Kill() //nolint:errcheck // best-effort if the sweep missed

	if out, err := exec.Command("sh", "-c", stopPreviousViteCommand(portInt(t, port))).CombinedOutput(); err != nil {
		t.Fatalf("sweep failed: %v\n%s", err, out)
	}

	if portInUse(port) {
		t.Fatalf("port %s still held after the sweep", port)
	}
}

// A free port is the normal first-run case and must not be an error, because
// cmdDev treats a failed pre-flight as fatal.
func TestStopPreviousViteCommandNoopWhenPortFree(t *testing.T) {
	if _, err := exec.LookPath("fuser"); err != nil {
		t.Skip("fuser not installed")
	}
	port := freePort(t)
	if out, err := exec.Command("sh", "-c", stopPreviousViteCommand(portInt(t, port))).CombinedOutput(); err != nil {
		t.Fatalf("sweep failed with nothing to stop: %v\n%s", err, out)
	}
}

func portInt(t *testing.T, port string) int {
	t.Helper()
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("bad port %q: %v", port, err)
	}
	return n
}

// writeFakeDevLoop drops an executable script named `bloud` and runs it with
// the `dev` argument, so /proc/<pid>/cmdline looks exactly like a real
// `./bloud dev` invocation. It stays in sh rather than exec'ing away, which
// is what preserves argv for the identity check.
func writeFakeDevLoop(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bloud")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write fake bloud: %v", err)
	}
	cmd := exec.Command(path, "dev")
	// Its own process group: a loop killed with SIGKILL leaves its `sleep` children
	// behind, and the cleanup has to be able to reach them.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake dev loop: %v", err)
	}
	// Wait until the process is recognizable before returning. cmd.Start can
	// return while /proc/<pid>/cmdline is still unset, and a caller that reads
	// the identity immediately (takeoverPreviousDevLoop) would see "not a
	// bloud dev loop" for a live one and skip the kill. That is the same exec
	// race TestIsBloudDevProcess documents; settling it here means every
	// caller starts from a process the production check agrees is ours.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !isBloudDevProcess(cmd.Process.Pid) {
		time.Sleep(20 * time.Millisecond)
	}
	if !isBloudDevProcess(cmd.Process.Pid) {
		t.Fatalf("fake dev loop pid %d never became recognizable", cmd.Process.Pid)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	})
	return cmd
}

// alive reports whether pid is a live process. A child that has been killed
// but not yet reaped is a zombie: it still occupies the pid table and still
// answers signal 0, but it is dead, so the zombie state counts as gone.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	st, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	s := string(st)
	// The comm field is parenthesised and may itself contain spaces and
	// parentheses, so split on the last ')' rather than counting fields.
	i := strings.LastIndex(s, ")")
	if i < 0 || i+2 >= len(s) {
		return true
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) == 0 {
		return true
	}
	return fields[0] != "Z"
}

func TestTakeoverPreviousDevLoopNoPidFile(t *testing.T) {
	got, err := takeoverPreviousDevLoop(filepath.Join(t.TempDir(), "absent.pid"))
	if err != nil {
		t.Fatalf("missing pid file must not be an error: %v", err)
	}
	if got {
		t.Fatal("reported a takeover with no pid file")
	}
}

// A stale loop gets terminated and the pid file is cleared.
func TestTakeoverPreviousDevLoopKillsStaleLoop(t *testing.T) {
	stale := writeFakeDevLoop(t, "sleep 300 & wait")
	pidPath := filepath.Join(t.TempDir(), "bloud-dev.pid")
	if err := writeDevLoopPIDTo(pidPath, stale.Process.Pid); err != nil {
		t.Fatalf("seed pid file: %v", err)
	}

	got, err := takeoverPreviousDevLoop(pidPath)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if !got {
		t.Fatal("expected a takeover of the stale loop")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("pid file should be cleared, stat err = %v", err)
	}
	waitForGone(t, stale.Process.Pid)
}

// A pid that ignores TERM is escalated to KILL rather than left running.
func TestTakeoverPreviousDevLoopEscalatesToKill(t *testing.T) {
	stale := writeFakeDevLoop(t, "trap '' TERM; sleep 300 & wait")
	pidPath := filepath.Join(t.TempDir(), "bloud-dev.pid")
	if err := writeDevLoopPIDTo(pidPath, stale.Process.Pid); err != nil {
		t.Fatalf("seed pid file: %v", err)
	}
	if _, err := takeoverPreviousDevLoop(pidPath); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	waitForGone(t, stale.Process.Pid)
}

// The pid came from a file written by a run that could have died at any
// point, so the number may now belong to something unrelated. Only a real
// `bloud dev` process may be signaled.
func TestTakeoverPreviousDevLoopSparesForeignProcess(t *testing.T) {
	foreign := exec.Command("sleep", "300")
	if err := foreign.Start(); err != nil {
		t.Fatalf("start foreign process: %v", err)
	}
	t.Cleanup(func() {
		if foreign.Process != nil {
			_ = foreign.Process.Kill()
		}
	})

	pidPath := filepath.Join(t.TempDir(), "bloud-dev.pid")
	if err := writeDevLoopPIDTo(pidPath, foreign.Process.Pid); err != nil {
		t.Fatalf("seed pid file: %v", err)
	}

	got, err := takeoverPreviousDevLoop(pidPath)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if got {
		t.Fatal("claimed a takeover for a non-bloud process")
	}
	if !alive(foreign.Process.Pid) {
		t.Fatal("killed a process that is not a bloud dev loop")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("stale pid file should be cleared, stat err = %v", err)
	}
}

// A pid file that does not hold a number must not take the run down.
func TestTakeoverPreviousDevLoopGarbagePid(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "bloud-dev.pid")
	if err := os.WriteFile(pidPath, []byte("not-a-pid\n"), 0o644); err != nil {
		t.Fatalf("seed pid file: %v", err)
	}
	got, err := takeoverPreviousDevLoop(pidPath)
	if err != nil {
		t.Fatalf("garbage pid must not be fatal: %v", err)
	}
	if got {
		t.Fatal("reported a takeover for a garbage pid")
	}
}

func TestIsBloudDevProcess(t *testing.T) {
	loop := writeFakeDevLoop(t, "sleep 300 & wait")
	defer loop.Process.Kill() //nolint:errcheck

	// Poll rather than assert once. exec.Cmd.Start returns when the child has
	// forked and reported its setup error, which is before execve has necessarily
	// finished, so /proc/<pid>/cmdline can still read empty or pre-exec for a
	// few milliseconds. A single read there is a race with the kernel, not with
	// the code under test: it failed roughly one run in three.
	//
	// Only the positive case needs this. The negative assertions below hold while
	// the cmdline is unsettled too, so waiting on them would hide nothing.
	recognized := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if isBloudDevProcess(loop.Process.Pid) {
			recognized = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !recognized {
		raw, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", loop.Process.Pid))
		t.Fatalf("a `bloud dev` process was not recognized: pid=%d cmdline=%q readErr=%v alive=%v",
			loop.Process.Pid, string(raw), readErr, alive(loop.Process.Pid))
	}

	foreign := exec.Command("sleep", "300")
	if err := foreign.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer foreign.Process.Kill() //nolint:errcheck
	if isBloudDevProcess(foreign.Process.Pid) {
		t.Fatal("a bare sleep was mistaken for a dev loop")
	}

	if isBloudDevProcess(0) {
		t.Fatal("pid 0 is not a dev loop")
	}
}

func waitForGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive", pid)
}

// writeDevLoopPIDTo is writeDevLoopPID with an explicit pid, so a test can
// record a process other than itself.
func writeDevLoopPIDTo(pidPath string, pid int) error {
	return os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o644)
}

// The dashboard proxy target must name an IPv4 loopback. `localhost` resolves
// ::1 first on a dual-stack host, so a vite bound to the other family would
// silently decide which server the dashboard is served from.
func TestDevViteProxyTargetIsIPv4Loopback(t *testing.T) {
	if devViteHost != "127.0.0.1" {
		t.Fatalf("devViteHost = %q, want 127.0.0.1; a resolvable name lets the resolver pick a different stack's vite", devViteHost)
	}
	url := fmt.Sprintf("http://%s:%d", devViteHost, devVitePort)
	if want := "http://127.0.0.1:5173"; url != want {
		t.Fatalf("BLOUD_DEV_VITE_URL would be %q, want %q", url, want)
	}
}

// The pid file has to live in the runtime dir the loop already owns, and the
// name must not collide with the deployed binary layout.
func TestDevLoopPIDPath(t *testing.T) {
	got := devLoopPIDPath("/var/tmp/bloud-native-runtime/host-agent")
	want := filepath.Join("/var/tmp/bloud-native-runtime/host-agent", "bloud-dev.pid")
	if got != want {
		t.Fatalf("devLoopPIDPath = %q, want %q", got, want)
	}
}
