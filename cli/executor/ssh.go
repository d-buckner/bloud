// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// cmdFn builds an exec.Cmd; kept injectable so tests can fake command execution.
type cmdFn func(ctx context.Context, name string, args ...string) *exec.Cmd

// Transport reaches a runtime guest (Lima VM via limactl, QEMU guest via ssh).
// SSHExecutor implements Transport.
type Transport interface {
	Executor
	// InteractiveShell opens a login shell on the guest with the terminal attached.
	InteractiveShell(ctx context.Context, stdout, stderr io.Writer, stdin io.Reader) error
	// Ready reports whether the guest is running and reachable.
	Ready() bool
}

// SSHConn describes how to reach a guest over real SSH (QEMU hostfwd, etc.).
type SSHConn struct {
	Host    string // e.g. 127.0.0.1
	Port    int    // host port forwarded to guest :22 (e.g. 2222)
	User    string // guest user (e.g. bloud)
	KeyFile string // private key used to authenticate
}

// SSHExecutor runs commands and transfers files to a runtime host over a
// Transport: either a Lima VM (via `limactl`) or a QEMU/SSH guest (via ssh+rsync).
type SSHExecutor struct {
	runCmd   func(ctx context.Context, spec RunSpec) *exec.Cmd
	copyTo   func(ctx context.Context, from, to string, recursive bool) *exec.Cmd
	copyFrom func(ctx context.Context, from, to string) *exec.Cmd
	inter    func(ctx context.Context) *exec.Cmd
	ready    func() bool
}

// NewLimactlExecutor returns an executor that targets the given Lima instance.
func NewLimactlExecutor(instance string) *SSHExecutor {
	return newExecutor(limactlStrategies(exec.CommandContext, instance))
}

// NewSSHExecutor returns an executor that targets a guest over real SSH.
func NewSSHExecutor(conn SSHConn) *SSHExecutor {
	return newExecutor(sshStrategies(exec.CommandContext, conn))
}

func newExecutor(s strategySet) *SSHExecutor {
	return &SSHExecutor{
		runCmd:   s.run,
		copyTo:   s.copyTo,
		copyFrom: s.copyFrom,
		inter:    s.inter,
		ready:    s.ready,
	}
}

// Run executes a command on the host, capturing stdout and stderr.
func (e *SSHExecutor) Run(ctx context.Context, spec RunSpec) (ExecResult, error) {
	cmd := e.runCmd(ctx, spec)
	cmd.Stdin = spec.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return result(stdout.String(), stderr.String(), err)
}

// RunStream executes a command on the host, streaming output to the writers.
func (e *SSHExecutor) RunStream(ctx context.Context, spec RunSpec, stdout, stderr io.Writer) error {
	cmd := e.runCmd(ctx, spec)
	if spec.Stdin != nil {
		cmd.Stdin = spec.Stdin
	} else {
		cmd.Stdin = os.Stdin
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// InteractiveShell opens a login shell on the host with the terminal attached.
func (e *SSHExecutor) InteractiveShell(ctx context.Context, stdout, stderr io.Writer, stdin io.Reader) error {
	cmd := e.inter(ctx)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// CopyTo uploads a local file or directory to the host. Directories are copied recursively.
func (e *SSHExecutor) CopyTo(ctx context.Context, from, to string) error {
	cmd := e.copyTo(ctx, from, to, isDirectory(from))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// CopyFrom downloads a file or directory from the host to a local path.
func (e *SSHExecutor) CopyFrom(ctx context.Context, from, to string) error {
	cmd := e.copyFrom(ctx, from, to)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Ready reports whether the guest is running and reachable.
func (e *SSHExecutor) Ready() bool { return e.ready() }

// strategySet bundles the command-building closures for one transport.
type strategySet struct {
	run      func(ctx context.Context, spec RunSpec) *exec.Cmd
	copyTo   func(ctx context.Context, from, to string, recursive bool) *exec.Cmd
	copyFrom func(ctx context.Context, from, to string) *exec.Cmd
	inter    func(ctx context.Context) *exec.Cmd
	ready    func() bool
}

// limactlStrategies shells into a Lima VM via `limactl`.
func limactlStrategies(newCmd cmdFn, instance string) strategySet {
	return strategySet{
		run: func(ctx context.Context, spec RunSpec) *exec.Cmd {
			return newCmd(ctx, "limactl", "shell", "--start", instance, "bash", "-c", BuildRemoteScript(spec))
		},
		copyTo: func(ctx context.Context, from, to string, recursive bool) *exec.Cmd {
			args := []string{"copy"}
			if recursive {
				args = append(args, "-r")
			}
			args = append(args, from, instance+":"+to)
			return newCmd(ctx, "limactl", args...)
		},
		copyFrom: func(ctx context.Context, from, to string) *exec.Cmd {
			return newCmd(ctx, "limactl", "copy", instance+":"+from, to)
		},
		inter: func(ctx context.Context) *exec.Cmd {
			return newCmd(ctx, "limactl", "shell", instance)
		},
		ready: func() bool {
			cmd := newCmd(context.Background(), "limactl", "list", "--json")
			out, err := cmd.Output()
			if err != nil {
				return false
			}
			return IsVMNameRunning(string(out), instance)
		},
	}
}

// sshStrategies shells into a guest over real SSH.
func sshStrategies(newCmd cmdFn, conn SSHConn) strategySet {
	port := strconv.Itoa(conn.Port)
	base := []string{"-p", port, "-i", conn.KeyFile}
	target := conn.User + "@" + conn.Host

	return strategySet{
		run: func(ctx context.Context, spec RunSpec) *exec.Cmd {
			// OpenSSH joins every argv after the host with spaces and does NOT
			// re-quote them, so passing the script as a bare argv element would
			// split `bash -c <script>` on whitespace and bash -c would only see
			// the script's first word. Single-quote the whole script so the
			// joined remote command is `bash -c '<script>'`.
			args := append(base, "-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes", target, "bash", "-c", shellQuote(BuildRemoteScript(spec)))
			return newCmd(ctx, "ssh", args...)
		},
		copyTo: func(ctx context.Context, from, to string, recursive bool) *exec.Cmd {
			args := []string{"-a"}
			if recursive {
				args = append(args, "-r")
				// Trailing slash makes rsync copy the directory's CONTENTS to
				// `to` (creating `to` if missing), matching `limactl copy -r`
				// semantics. Without it, rsync nests the source basename inside
				// the destination (web/build -> web/build/build).
				if !strings.HasSuffix(from, "/") {
					from += "/"
				}
			}
			args = append(args, "-e", "ssh -p "+port+" -i "+conn.KeyFile, from, target+":"+to)
			return newCmd(ctx, "rsync", args...)
		},
		copyFrom: func(ctx context.Context, from, to string) *exec.Cmd {
			return newCmd(ctx, "rsync", "-a", "-e", "ssh -p "+port+" -i "+conn.KeyFile, target+":"+from, to)
		},
		inter: func(ctx context.Context) *exec.Cmd {
			args := append(base, "-t", target)
			return newCmd(ctx, "ssh", args...)
		},
		ready: func() bool {
			args := append(base, "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=5", target, "true")
			return newCmd(context.Background(), "ssh", args...).Run() == nil
		},
	}
}

// BuildRemoteScript renders a RunSpec into a bash script executed on the host.
// Env vars are exported in sorted order for deterministic output.
func BuildRemoteScript(spec RunSpec) string {
	var b strings.Builder
	for _, k := range sortedEnvKeys(spec.Env) {
		fmt.Fprintf(&b, "export %s=%s; ", k, shellQuote(spec.Env[k]))
	}
	if spec.Dir != "" {
		fmt.Fprintf(&b, "cd %s; ", shellQuote(spec.Dir))
	}
	if spec.AsRoot {
		b.WriteString("sudo ")
	}
	b.WriteString(spec.Command)
	if len(spec.Args) > 0 {
		b.WriteString(" " + strings.Join(spec.Args, " "))
	}
	return b.String()
}

func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
