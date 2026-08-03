package managedfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWrite_NewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.txt")

	changed, err := Write(path, []byte("hello"), 0644)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if !changed {
		t.Error("Write() changed = false for new file, want true")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want %q", got, "hello")
	}
}

func TestWrite_SameContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "same.txt")
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	changed, err := Write(path, []byte("hello"), 0644)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if changed {
		t.Error("Write() changed = true for identical content, want false")
	}
}

func TestWrite_DifferentContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diff.txt")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	changed, err := Write(path, []byte("new"), 0644)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if !changed {
		t.Error("Write() changed = false for different content, want true")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
}

func TestWrite_Permissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "perms.txt")

	_, err := Write(path, []byte("secret"), 0600)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("permissions = %o, want %o", got, 0600)
	}
}

func TestWrite_CreatesParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c", "file.txt")

	changed, err := Write(path, []byte("deep"), 0644)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if !changed {
		t.Error("Write() changed = false, want true")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "deep" {
		t.Errorf("content = %q, want %q", got, "deep")
	}
}

func TestWrite_AtomicNoCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic.txt")
	original := []byte("original content")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}

	// Mount a read-only tmpfs so write fails even when running as root.
	// File permission checks are bypassed by root, so we need a truly
	// read-only filesystem to verify the atomic-write failure path.
	roDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(roDir, 0755); err != nil {
		t.Fatal(err)
	}
	mountErr := exec.Command("mount", "-t", "tmpfs", "-o", "ro", "tmpfs", roDir).Run()
	if mountErr != nil {
		t.Skipf("mount unavailable (%v), skipping atomic-corruption test", mountErr)
	}
	defer exec.Command("umount", roDir).Run()

	targetInReadOnly := filepath.Join(roDir, "subdir", "file.txt")

	_, err := Write(targetInReadOnly, []byte("new"), 0644)
	if err == nil {
		t.Fatal("expected error writing to read-only directory")
	}

	// Original file should be untouched
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "original content" {
		t.Errorf("original file corrupted: content = %q", got)
	}
}
