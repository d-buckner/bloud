// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"testing"
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
