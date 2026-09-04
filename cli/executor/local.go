// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// LocalExecutor runs commands on the local machine.
type LocalExecutor struct{}

// Run executes a command locally, capturing stdout and stderr.
func (e *LocalExecutor) Run(ctx context.Context, spec RunSpec) (ExecResult, error) {
	cmd := buildLocalCommand(ctx, spec)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return result(stdout.String(), stderr.String(), err)
}

// RunStream executes a command locally, streaming output to the writers.
func (e *LocalExecutor) RunStream(ctx context.Context, spec RunSpec, stdout, stderr io.Writer) error {
	cmd := buildLocalCommand(ctx, spec)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// CopyTo copies a local file or directory to another local path. Directories
// are copied recursively, matching SSHExecutor's rsync-based semantics: the
// contents of `from` land at `to` (which is created if missing), rather than
// nesting `from`'s basename inside it.
func (e *LocalExecutor) CopyTo(_ context.Context, from, to string) error {
	if isDirectory(from) {
		return copyDir(from, to)
	}
	return copyFile(from, to)
}

// CopyFrom copies a local file or directory from another local path. See CopyTo.
func (e *LocalExecutor) CopyFrom(_ context.Context, from, to string) error {
	if isDirectory(from) {
		return copyDir(from, to)
	}
	return copyFile(from, to)
}

func buildLocalCommand(ctx context.Context, spec RunSpec) *exec.Cmd {
	var cmd *exec.Cmd
	switch {
	case len(spec.Args) > 0:
		// Command names a program and Args are its literal arguments — exec
		// it directly, no shell involved.
		name, args := spec.Command, spec.Args
		if spec.AsRoot {
			name, args = "sudo", append([]string{spec.Command}, spec.Args...)
		}
		cmd = exec.CommandContext(ctx, name, args...)
	case spec.AsRoot:
		cmd = exec.CommandContext(ctx, "sudo", "sh", "-c", spec.Command)
	default:
		// Command is a full shell command line (pipes, redirects, quoting,
		// &&/;, etc.), matching the contract of the SSH/Lima executors, which
		// hand it to a remote shell verbatim. Run it through "sh -c" here too,
		// rather than treating spec.Command as a literal executable name.
		cmd = exec.CommandContext(ctx, "sh", "-c", spec.Command)
	}
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), envSlice(spec.Env)...)
	}
	cmd.Dir = spec.Dir
	cmd.Stdin = spec.Stdin
	return cmd
}

// result normalizes the outcome of cmd.Run() into an ExecResult.
func result(stdout, stderr string, err error) (ExecResult, error) {
	res := ExecResult{Stdout: stdout, Stderr: stderr}
	res.ExitCode = 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
		}
	}
	return res, err
}

func envSlice(env map[string]string) []string {
	var out []string
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("open %s: %w", from, err)
	}
	defer in.Close()

	out, err := os.Create(to)
	if err != nil {
		return fmt.Errorf("create %s: %w", to, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy %s -> %s: %w", from, to, err)
	}
	return out.Close()
}

// copyDir recursively copies the contents of the directory `from` into `to`,
// creating `to` as needed.
func copyDir(from, to string) error {
	if err := os.MkdirAll(to, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", to, err)
	}
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}
