package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fakeExec(recorded *[]string, script string) func(context.Context, string, ...string) *exec.Cmd {
	return func(_ context.Context, name string, args ...string) *exec.Cmd {
		*recorded = append(*recorded, append([]string{name}, args...)...)
		return exec.Command("sh", "-c", script)
	}
}

func TestBuildRemoteScript(t *testing.T) {
	tests := []struct {
		name string
		spec RunSpec
		want string
	}{
		{
			name: "simple command",
			spec: RunSpec{Command: "echo", Args: []string{"hi"}},
			want: "echo hi",
		},
		{
			name: "no args",
			spec: RunSpec{Command: "hostname"},
			want: "hostname",
		},
		{
			name: "env exported in sorted order",
			spec: RunSpec{
				Command: "echo",
				Args:    []string{"hi"},
				Env:     map[string]string{"B": "2", "A": "1"},
			},
			want: "export A='1'; export B='2'; echo hi",
		},
		{
			name: "env value with single quote",
			spec: RunSpec{
				Command: "echo",
				Env:     map[string]string{"A": "it's"},
			},
			want: "export A='it'\"'\"'s'; echo",
		},
		{
			name: "working directory",
			spec: RunSpec{Command: "pwd", Dir: "/tmp"},
			want: "cd '/tmp'; pwd",
		},
		{
			name: "as root",
			spec: RunSpec{Command: "podman", Args: []string{"ps"}, AsRoot: true},
			want: "sudo podman ps",
		},
		{
			name: "env, dir, and as root combined",
			spec: RunSpec{
				Command: "ls",
				Env:     map[string]string{"A": "1"},
				Dir:     "/x",
				AsRoot:  true,
			},
			want: "export A='1'; cd '/x'; sudo ls",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildRemoteScript(tt.spec)
			if got != tt.want {
				t.Errorf("BuildRemoteScript(%+v) = %q, want %q", tt.spec, got, tt.want)
			}
		})
	}
}

func TestSSHExecutorRunInvocation(t *testing.T) {
	var recorded []string
	ex := &SSHExecutor{instance: "bloud-dev", newCmd: fakeExec(&recorded, "echo fake-out")}
	res, err := ex.Run(context.Background(), RunSpec{Command: "echo", Args: []string{"hi"}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"limactl", "shell", "--start", "bloud-dev", "bash", "-c", "echo hi"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.Stdout != "fake-out\n" {
		t.Fatalf("Stdout = %q, want %q", res.Stdout, "fake-out\n")
	}
}

func TestSSHExecutorRunScriptReflectsSpec(t *testing.T) {
	var recorded []string
	ex := &SSHExecutor{instance: "bloud-dev", newCmd: fakeExec(&recorded, "echo ok")}
	if _, err := ex.Run(context.Background(), RunSpec{
		Command: "./host-agent",
		Dir:     "/runtime",
		Env:     map[string]string{"BLOUD_DATA_DIR": "/runtime/data"},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{
		"limactl", "shell", "--start", "bloud-dev", "bash", "-c",
		"export BLOUD_DATA_DIR='/runtime/data'; cd '/runtime'; ./host-agent",
	}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestSSHExecutorRunFailure(t *testing.T) {
	var recorded []string
	ex := &SSHExecutor{instance: "bloud-dev", newCmd: fakeExec(&recorded, "echo fake-err >&2; exit 3")}
	res, err := ex.Run(context.Background(), RunSpec{Command: "false"})
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil")
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}
	if res.Stderr != "fake-err\n" {
		t.Fatalf("Stderr = %q, want %q", res.Stderr, "fake-err\n")
	}
}

func TestSSHExecutorCopyToFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(src, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	var recorded []string
	ex := &SSHExecutor{instance: "bloud-dev", newCmd: fakeExec(&recorded, "true")}
	if err := ex.CopyTo(context.Background(), src, "/runtime/host-agent/host-agent"); err != nil {
		t.Fatalf("CopyTo() error = %v", err)
	}
	want := []string{"limactl", "copy", src, "bloud-dev:/runtime/host-agent/host-agent"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestSSHExecutorCopyToDirIsRecursive(t *testing.T) {
	dir := t.TempDir()
	var recorded []string
	ex := &SSHExecutor{instance: "bloud-dev", newCmd: fakeExec(&recorded, "true")}
	if err := ex.CopyTo(context.Background(), dir, "/runtime/web/build"); err != nil {
		t.Fatalf("CopyTo() error = %v", err)
	}
	want := []string{"limactl", "copy", "-r", dir, "bloud-dev:/runtime/web/build"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestSSHExecutorCopyFrom(t *testing.T) {
	var recorded []string
	ex := &SSHExecutor{instance: "bloud-dev", newCmd: fakeExec(&recorded, "true")}
	if err := ex.CopyFrom(context.Background(), "/runtime/logs.txt", "/tmp/logs.txt"); err != nil {
		t.Fatalf("CopyFrom() error = %v", err)
	}
	want := []string{"limactl", "copy", "bloud-dev:/runtime/logs.txt", "/tmp/logs.txt"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestSSHExecutorRunStream(t *testing.T) {
	var recorded []string
	ex := &SSHExecutor{instance: "bloud-dev", newCmd: fakeExec(&recorded, "echo streamed")}
	var stdout, stderr strings.Builder
	if err := ex.RunStream(context.Background(),
		RunSpec{Command: "hostname"}, &stdout, &stderr); err != nil {
		t.Fatalf("RunStream() error = %v", err)
	}
	want := []string{"limactl", "shell", "--start", "bloud-dev", "bash", "-c", "hostname"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
	if stdout.String() != "streamed\n" {
		t.Fatalf("stdout = %q, want %q", stdout.String(), "streamed\n")
	}
}

func TestSSHExecutorInteractiveShell(t *testing.T) {
	var recorded []string
	ex := &SSHExecutor{instance: "bloud-dev", newCmd: fakeExec(&recorded, "exit")}
	var stdout, stderr strings.Builder
	if err := ex.InteractiveShell(context.Background(), &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("InteractiveShell() error = %v", err)
	}
	want := []string{"limactl", "shell", "bloud-dev"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}
