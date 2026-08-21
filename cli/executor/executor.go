// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package executor abstracts command execution and file transfer to a
// runtime host (local machine, SSH/Lima VM, etc.).
package executor

import (
	"context"
	"io"
)

// Executor runs commands and transfers files to/from a runtime host.
type Executor interface {
	// Run executes a command and captures its output.
	Run(ctx context.Context, spec RunSpec) (ExecResult, error)
	// RunStream executes a command, streaming stdout/stderr to the given writers.
	RunStream(ctx context.Context, spec RunSpec, stdout, stderr io.Writer) error
	// CopyTo uploads a local file or directory to a remote path.
	CopyTo(ctx context.Context, from, to string) error
	// CopyFrom downloads a remote file or directory to a local path.
	CopyFrom(ctx context.Context, from, to string) error
}

// RunSpec describes a single command execution.
type RunSpec struct {
	Command string
	Args    []string
	Env     map[string]string
	Stdin   io.Reader
	Dir     string
	AsRoot  bool
}

// ExecResult captures the outcome of a Run call.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}
