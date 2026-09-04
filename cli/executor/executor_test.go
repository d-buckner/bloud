// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalRunSuccess(t *testing.T) {
	res, err := (&LocalExecutor{}).Run(context.Background(), RunSpec{
		Command: "echo",
		Args:    []string{"hello"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.Stdout != "hello\n" {
		t.Fatalf("Stdout = %q, want %q", res.Stdout, "hello\n")
	}
}

func TestLocalRunFailure(t *testing.T) {
	res, err := (&LocalExecutor{}).Run(context.Background(), RunSpec{
		Command: "sh",
		Args:    []string{"-c", "exit 3"},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil")
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}
}

func TestLocalRunEnv(t *testing.T) {
	res, err := (&LocalExecutor{}).Run(context.Background(), RunSpec{
		Command: "sh",
		Args:    []string{"-c", `printf '%s' "$FOO"`},
		Env:     map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Stdout != "bar" {
		t.Fatalf("Stdout = %q, want %q", res.Stdout, "bar")
	}
}

func TestLocalRunStdin(t *testing.T) {
	res, err := (&LocalExecutor{}).Run(context.Background(), RunSpec{
		Command: "cat",
		Stdin:   strings.NewReader("payload"),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Stdout != "payload" {
		t.Fatalf("Stdout = %q, want %q", res.Stdout, "payload")
	}
}

func TestLocalRunDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := (&LocalExecutor{}).Run(context.Background(), RunSpec{
		Command: "ls",
		Dir:     dir,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(res.Stdout, "marker") {
		t.Fatalf("Stdout = %q, want it to contain %q", res.Stdout, "marker")
	}
}

func TestLocalRunShellSyntax(t *testing.T) {
	res, err := (&LocalExecutor{}).Run(context.Background(), RunSpec{
		Command: `pkill -f 'no-such-process-xyz$' 2>/dev/null; echo done; true`,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(res.Stdout, "done") {
		t.Fatalf("Stdout = %q, want it to contain %q", res.Stdout, "done")
	}
}

func TestLocalCopyTo(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.txt")
	dst := filepath.Join(t.TempDir(), "dst.txt")
	if err := os.WriteFile(src, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := (&LocalExecutor{}).CopyTo(context.Background(), src, dst); err != nil {
		t.Fatalf("CopyTo() error = %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading dst: %v", err)
	}
	if string(got) != "data" {
		t.Fatalf("dst = %q, want %q", string(got), "data")
	}
}

func TestLocalCopyFrom(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.txt")
	dst := filepath.Join(t.TempDir(), "dst.txt")
	if err := os.WriteFile(src, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := (&LocalExecutor{}).CopyFrom(context.Background(), src, dst); err != nil {
		t.Fatalf("CopyFrom() error = %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading dst: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("dst = %q, want %q", string(got), "payload")
	}
}

func TestLocalCopyToDirectory(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "top.txt"), []byte("top"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(src, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "leaf.txt"), []byte("leaf"), 0600); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "dst")
	if err := (&LocalExecutor{}).CopyTo(context.Background(), src, dst); err != nil {
		t.Fatalf("CopyTo() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "top.txt"))
	if err != nil {
		t.Fatalf("reading dst/top.txt: %v", err)
	}
	if string(got) != "top" {
		t.Fatalf("dst/top.txt = %q, want %q", string(got), "top")
	}

	got, err = os.ReadFile(filepath.Join(dst, "nested", "deeper", "leaf.txt"))
	if err != nil {
		t.Fatalf("reading dst/nested/deeper/leaf.txt: %v", err)
	}
	if string(got) != "leaf" {
		t.Fatalf("dst/nested/deeper/leaf.txt = %q, want %q", string(got), "leaf")
	}
}

func TestLocalRunStream(t *testing.T) {
	var stdout, stderr strings.Builder
	err := (&LocalExecutor{}).RunStream(context.Background(),
		RunSpec{Command: "sh", Args: []string{"-c", `printf 'out'; printf 'err' >&2`}},
		&stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("RunStream() error = %v", err)
	}
	if stdout.String() != "out" {
		t.Fatalf("stdout = %q, want %q", stdout.String(), "out")
	}
	if stderr.String() != "err" {
		t.Fatalf("stderr = %q, want %q", stderr.String(), "err")
	}
}
