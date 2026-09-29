// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// treeScript spawns one grandchild that holds an exclusive flock for its whole
// life and records its pid. This is the shape that leaked: the dev loop's
// children are wrappers (npm -> sh -> vite), and a signal sent only to the
// wrapper leaves the grandchild holding the dev-server port.
//
// The pid is written from inside the lock, and the holder then execs into
// `sleep`, so the recorded pid is the lock holder and the pid file cannot
// appear before the lock is held. A `flock ... & echo $!` pair has no such
// guarantee: the parent records the pid while the child is still racing to
// acquire, which is exactly how this test flaked in CI.
const treeScript = `flock -x %s -c 'echo $$ > %s; exec sleep 300' &
wait
`

func requireFlock(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock is not installed; cannot build a wrapper/grandchild tree")
	}
}

// startTree starts a wrapper/grandchild tree through restartableCmd and returns
// the command plus the grandchild pid.
func startTree(t *testing.T, dir string) (*restartableCmd, int) {
	t.Helper()
	lockPath := filepath.Join(dir, "tree.lock")
	pidPath := filepath.Join(dir, "child.pid")
	c := newRestartableCmd("tree", func() (*exec.Cmd, error) {
		return exec.Command("sh", "-c", fmt.Sprintf(treeScript, lockPath, pidPath)), nil
	})
	if _, err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := waitForFile(pidPath, 5*time.Second); err != nil {
		t.Fatalf("grandchild never reported its pid: %v", err)
	}
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse pid %q: %v", raw, err)
	}
	waitUntil(t, 5*time.Second, fmt.Sprintf("the grandchild (pid %d) holds %s", pid, lockPath), func() bool {
		return lockHeld(lockPath)
	})
	return c, pid
}

func TestRestartableCmdChildRunsInItsOwnProcessGroup(t *testing.T) {
	c := newRestartableCmd("sleeper", func() (*exec.Cmd, error) {
		return exec.Command("sleep", "30"), nil
	})
	if _, err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := c.cmd.Process.Pid

	selfGroup, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("own pgid: %v", err)
	}
	childGroup, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("child pgid: %v", err)
	}
	if childGroup != pid {
		t.Errorf("child pgid = %d, want %d (its own group)", childGroup, pid)
	}
	if childGroup == selfGroup {
		t.Errorf("child shares the CLI process group (%d); a group signal would hit the CLI too", childGroup)
	}

	if err := c.Stop(5 * time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if c.Running() {
		t.Error("still Running() after Stop")
	}
	waitProcessGone(t, pid)
}

func TestRestartableCmdStopKillsWholeTree(t *testing.T) {
	requireFlock(t)
	dir := t.TempDir()
	c, grandchild := startTree(t, dir)

	if err := c.Stop(5 * time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitProcessGone(t, grandchild)
	waitUntil(t, 5*time.Second, "the flock is released", func() bool {
		return !lockHeld(filepath.Join(dir, "tree.lock"))
	})
}

func TestRestartableCmdForceKillsWholeTree(t *testing.T) {
	requireFlock(t)
	dir := t.TempDir()
	c, grandchild := startTree(t, dir)

	c.Force()

	waitProcessGone(t, grandchild)
	waitUntil(t, 5*time.Second, "the flock is released", func() bool {
		return !lockHeld(filepath.Join(dir, "tree.lock"))
	})
	if err := c.Stop(2 * time.Second); err != nil {
		t.Fatalf("stop after force: %v", err)
	}
}

// lockHeld reports whether some other process holds an exclusive flock on path.
func TestEscalateOnSecondSignalForcesBothDevServers(t *testing.T) {
	requireFlock(t)
	signals := make(chan os.Signal, 4)
	child, childGrand := startTree(t, t.TempDir())
	vite, viteGrand := startTree(t, t.TempDir())
	loop := &reloadLoop{out: io.Discard, child: child, vite: vite, signals: signals}
	loop.escalateOnSecondSignal()

	// The first signal only announces: the graceful stop is the caller's job
	// (the context it cancelled), so nothing gets killed here.
	signals <- syscall.SIGTERM
	time.Sleep(300 * time.Millisecond)
	if !processAlive(childGrand) || !processAlive(viteGrand) {
		t.Fatal("first signal killed the trees; only the second one should")
	}

	signals <- syscall.SIGTERM
	waitProcessGone(t, childGrand)
	waitProcessGone(t, viteGrand)
	close(signals)
}

func lockHeld(path string) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return true
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// processAlive reports whether pid is a live process. A zombie still answers
// signal 0 but is already dead for every purpose here, so it does not count.
func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return errors.Is(err, syscall.EPERM)
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	// comm is parenthesised and may contain spaces: parse after the last ')'.
	idx := bytes.LastIndexByte(stat, ')')
	if idx < 0 {
		return true
	}
	fields := strings.Fields(string(stat[idx+1:]))
	if len(fields) > 0 && fields[0] == "Z" {
		return false
	}
	return true
}

func waitProcessGone(t *testing.T, pid int) {
	t.Helper()
	waitUntil(t, 5*time.Second, fmt.Sprintf("pid %d is gone", pid), func() bool {
		return !processAlive(pid)
	})
}

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
