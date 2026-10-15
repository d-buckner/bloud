// SPDX-License-Identifier: AGPL-3.0-only

package managedfile

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestEnsureWritable_ModeIsTheWant covers the two outcomes a caller can act on:
// a path that lacks the permission is changed, and one that already carries it
// is left exactly as it was.
func TestEnsureWritable_ModeIsTheWant(t *testing.T) {
	dir := t.TempDir()

	dirPath := filepath.Join(dir, "shared")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := EnsureWritable(dirPath, 0o777); err != nil {
		t.Fatalf("EnsureWritable(dir, 0777) = %v, want nil", err)
	}
	info, err := os.Stat(dirPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o777 {
		t.Errorf("directory mode = %o, want 777", got)
	}

	filePath := filepath.Join(dir, "conf")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := EnsureWritable(filePath, 0o666); err != nil {
		t.Fatalf("EnsureWritable(file, 0666) = %v, want nil", err)
	}
	info, err = os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o666 {
		t.Errorf("file mode = %o, want 666", got)
	}
}

// TestEnsureWritable_AlreadyWritableIsUntouched pins the case the helper exists
// for: a path that a container has taken over already carries the permission, so
// it must be reported as done rather than chmodded. The chmod would fail with
// EPERM against the container's subordinate uid, and a PreStart that fails is
// terminal for the node.
//
// The ownership that makes the chmod fail cannot be reproduced unprivileged, so
// the decision on that failure is pinned in TestContainerOwned and this test
// pins the other half of the rule: a path that already has the permission is not
// chmodded at all, which is what keeps the failing call from happening.
// The sticky bit is the marker, because a chmod to 0o777 clears it.
func TestEnsureWritable_AlreadyWritableIsUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "container-owned")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(path, 0o777|os.ModeSticky); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if err := EnsureWritable(path, 0o777); err != nil {
		t.Fatalf("EnsureWritable(already writable, 0777) = %v, want nil", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode()&os.ModeSticky == 0 {
		t.Error("the sticky bit was cleared: a path that is already writable must not be chmodded")
	}
}

// TestEnsureWritable_MissingPathIsAnError keeps a genuinely absent path loud:
// every call site creates the path first, so a missing one is a bug there, not
// something to paper over.
func TestEnsureWritable_MissingPathIsAnError(t *testing.T) {
	if err := EnsureWritable(filepath.Join(t.TempDir(), "absent"), 0o777); err == nil {
		t.Error("EnsureWritable(absent path) = nil, want an error")
	}
}

// TestContainerOwned pins which chmod failures EnsureWritable accepts: one where
// a different uid owns the path and can write it. Both halves matter. A foreign
// owner is what makes the chmod fail with EPERM, and the owner's write bit is
// what makes accepting it honest, because the container's own user is the
// process that writes there. Reading only the failure would as happily accept a
// root-owned path the container cannot write either.
func TestContainerOwned(t *testing.T) {
	foreign := uint32(os.Getuid()) + 1
	cases := []struct {
		name string
		uid  uint32
		mode os.FileMode
		want bool
	}{
		{"a foreign owner that can write", foreign, 0o755, true},
		{"a foreign owner that cannot write", foreign, 0o444, false},
		{"our own uid", uint32(os.Getuid()), 0o755, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := fakeInfo{uid: tc.uid, mode: tc.mode}
			if got := containerOwned(info); got != tc.want {
				t.Errorf("containerOwned(uid=%d, mode=%o) = %v, want %v", tc.uid, tc.mode, got, tc.want)
			}
		})
	}
}

// fakeInfo carries only what containerOwned reads: the mode and the uid behind
// it. os.Stat cannot be talked into reporting a foreign owner without
// privileges, so the decision is tested directly.
type fakeInfo struct {
	fs.FileInfo
	uid  uint32
	mode os.FileMode
}

func (f fakeInfo) Mode() os.FileMode { return f.mode }
func (f fakeInfo) Sys() any          { return &syscall.Stat_t{Uid: f.uid} }
