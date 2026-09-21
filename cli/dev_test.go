// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// repoRootFromCwd resolves the repo root from the test's original working
// directory (the cli package dir) without using getProjectRoot itself, so the
// tests assert against an independently derived expectation.
func repoRootFromCwd(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Dir(cwd)
	if _, err := os.Stat(filepath.Join(root, "validation.yaml")); err != nil {
		t.Fatalf("expected %s to be the repo root: %v", root, err)
	}
	return root
}

func TestGetProjectRootFromRepoSubdir(t *testing.T) {
	root := repoRootFromCwd(t)

	// A nested directory with no local markers must still resolve by walking up.
	nested := filepath.Join(root, "services", "host-agent", "internal")
	if _, err := os.Stat(nested); err != nil {
		t.Skipf("missing %s", nested)
	}
	t.Chdir(nested)

	got, err := getProjectRoot()
	if err != nil {
		t.Fatalf("getProjectRoot from %s: %v", nested, err)
	}
	if got != root {
		t.Fatalf("getProjectRoot = %q, want %q", got, root)
	}
}

func TestGetProjectRootFromCliDir(t *testing.T) {
	root := repoRootFromCwd(t)
	t.Chdir(filepath.Join(root, "cli"))

	got, err := getProjectRoot()
	if err != nil {
		t.Fatalf("getProjectRoot from cli dir: %v", err)
	}
	if got != root {
		t.Fatalf("getProjectRoot = %q, want %q", got, root)
	}
}

func TestGetProjectRootOutsideRepo(t *testing.T) {
	dir := t.TempDir()
	// Guard against a TMPDIR that lives inside the repo, which would make the
	// walk-up legitimately succeed.
	repoRoot := repoRootFromCwd(t)
	rel, err := filepath.Rel(repoRoot, dir)
	if err == nil && len(rel) > 0 && rel[0] != '.' && !filepath.IsAbs(rel) {
		t.Skipf("temp dir %s is inside the repo", dir)
	}

	t.Chdir(dir)
	if got, err := getProjectRoot(); err == nil {
		t.Fatalf("getProjectRoot outside repo = %q, want error", got)
	}
}

// TestRootMarkersExist pins the markers themselves: if one is moved or deleted,
// root resolution silently loses a signal, so the failure should be loud here.
func TestRootMarkersExist(t *testing.T) {
	root := repoRootFromCwd(t)
	for _, marker := range rootMarkers {
		if _, err := os.Stat(filepath.Join(root, marker)); err != nil {
			t.Errorf("root marker %q missing from %s", marker, root)
		}
	}
}

// TestStopPreviousHostAgentCommandFreesBusyBinary reproduces the redeploy
// failure: overwriting a binary that is still executing fails with "text file
// busy". The stop command must release it so the deploy copy can proceed.
func TestStopPreviousHostAgentCommandFreesBusyBinary(t *testing.T) {
	if _, err := exec.LookPath("fuser"); err != nil {
		t.Skip("fuser not installed")
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not found")
	}
	src, err := os.ReadFile(sleepPath)
	if err != nil {
		t.Skipf("cannot read %s: %v", sleepPath, err)
	}

	bin := filepath.Join(t.TempDir(), "host-agent")
	if err := os.WriteFile(bin, src, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "300")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot execute a copied binary here: %v", err)
	}
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// Precondition: the copy really is blocked while the process runs.
	if f, err := os.OpenFile(bin, os.O_WRONLY|os.O_TRUNC, 0); err == nil {
		_ = f.Close()
		t.Skip("overwriting a running binary is allowed here; nothing to reproduce")
	} else if !errors.Is(err, syscall.ETXTBSY) {
		t.Fatalf("precondition: opening a running binary for write = %v, want ETXTBSY", err)
	}

	// A port nothing listens on, so the port half of the command is a no-op and
	// this test cannot kill a developer's real host-agent on :3000.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()

	out, err := exec.Command("sh", "-c", stopPreviousHostAgentCommand(port, bin)).CombinedOutput()
	if err != nil {
		t.Fatalf("stop command failed: %v\n%s", err, out)
	}

	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("previous host-agent still running after the stop command")
	}
	f, err := os.OpenFile(bin, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatalf("binary still busy after the stop command: %v", err)
	}
	_ = f.Close()
}

// The stop command must be a harmless no-op when nothing is deployed or
// running (first run), since cmdDev treats its failure as fatal.
func TestStopPreviousHostAgentCommandNoopOnFirstRun(t *testing.T) {
	if _, err := exec.LookPath("fuser"); err != nil {
		t.Skip("fuser not installed")
	}
	missing := filepath.Join(t.TempDir(), "host-agent")
	if out, err := exec.Command("sh", "-c", stopPreviousHostAgentCommand("65501", missing)).CombinedOutput(); err != nil {
		t.Fatalf("stop command failed with nothing to stop: %v\n%s", err, out)
	}
}
