package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// SSHExecutor runs commands and transfers files to a Lima VM via `limactl`.
type SSHExecutor struct {
	instance string
	newCmd   func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// NewSSHExecutor returns an executor that targets the given Lima instance.
func NewSSHExecutor(instance string) *SSHExecutor {
	return &SSHExecutor{instance: instance, newCmd: exec.CommandContext}
}

// Run executes a command inside the VM, capturing stdout and stderr.
func (e *SSHExecutor) Run(ctx context.Context, spec RunSpec) (ExecResult, error) {
	cmd := e.buildShellCommand(ctx, spec)
	cmd.Stdin = spec.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return result(stdout.String(), stderr.String(), err)
}

// RunStream executes a command inside the VM, streaming output to the writers.
func (e *SSHExecutor) RunStream(ctx context.Context, spec RunSpec, stdout, stderr io.Writer) error {
	cmd := e.buildShellCommand(ctx, spec)
	if spec.Stdin != nil {
		cmd.Stdin = spec.Stdin
	} else {
		cmd.Stdin = os.Stdin
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// InteractiveShell opens a login shell on the VM with the terminal attached.
func (e *SSHExecutor) InteractiveShell(ctx context.Context, stdout, stderr io.Writer, stdin io.Reader) error {
	cmd := e.newCmd(ctx, "limactl", "shell", e.instance)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// CopyTo uploads a local file or directory into the VM. Directories are copied recursively.
func (e *SSHExecutor) CopyTo(ctx context.Context, from, to string) error {
	args := []string{"copy"}
	if isDirectory(from) {
		args = append(args, "-r")
	}
	args = append(args, from, e.instance+":"+to)
	cmd := e.newCmd(ctx, "limactl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// CopyFrom downloads a file or directory from the VM to a local path.
func (e *SSHExecutor) CopyFrom(ctx context.Context, from, to string) error {
	cmd := e.newCmd(ctx, "limactl", "copy", e.instance+":"+from, to)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (e *SSHExecutor) buildShellCommand(ctx context.Context, spec RunSpec) *exec.Cmd {
	return e.newCmd(ctx, "limactl", "shell", "--start", e.instance, "bash", "-c", BuildRemoteScript(spec))
}

// BuildRemoteScript renders a RunSpec into a bash script executed inside the VM.
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
