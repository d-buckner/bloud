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

// fakeLimactlExecutor builds a limactl-backed SSHExecutor with a faked command factory.
func fakeLimactlExecutor(recorded *[]string, script string) *SSHExecutor {
	nc := fakeExec(recorded, script)
	return newExecutor(limactlStrategies(nc, "bloud-dev"))
}

// fakeSSHExecutor builds an ssh-backed SSHExecutor with a faked command factory.
func fakeSSHExecutor(recorded *[]string, script string, conn SSHConn) *SSHExecutor {
	nc := fakeExec(recorded, script)
	return newExecutor(sshStrategies(nc, conn))
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

func TestLimactlExecutorRunInvocation(t *testing.T) {
	var recorded []string
	ex := fakeLimactlExecutor(&recorded, "echo fake-out")
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

func TestLimactlExecutorRunScriptReflectsSpec(t *testing.T) {
	var recorded []string
	ex := fakeLimactlExecutor(&recorded, "echo ok")
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

func TestLimactlExecutorRunFailure(t *testing.T) {
	var recorded []string
	ex := fakeLimactlExecutor(&recorded, "echo fake-err >&2; exit 3")
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

func TestLimactlExecutorCopyToFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(src, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	var recorded []string
	ex := fakeLimactlExecutor(&recorded, "true")
	if err := ex.CopyTo(context.Background(), src, "/runtime/host-agent/host-agent"); err != nil {
		t.Fatalf("CopyTo() error = %v", err)
	}
	want := []string{"limactl", "copy", src, "bloud-dev:/runtime/host-agent/host-agent"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestLimactlExecutorCopyToDirIsRecursive(t *testing.T) {
	dir := t.TempDir()
	var recorded []string
	ex := fakeLimactlExecutor(&recorded, "true")
	if err := ex.CopyTo(context.Background(), dir, "/runtime/web/build"); err != nil {
		t.Fatalf("CopyTo() error = %v", err)
	}
	want := []string{"limactl", "copy", "-r", dir, "bloud-dev:/runtime/web/build"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestLimactlExecutorCopyFrom(t *testing.T) {
	var recorded []string
	ex := fakeLimactlExecutor(&recorded, "true")
	if err := ex.CopyFrom(context.Background(), "/runtime/logs.txt", "/tmp/logs.txt"); err != nil {
		t.Fatalf("CopyFrom() error = %v", err)
	}
	want := []string{"limactl", "copy", "bloud-dev:/runtime/logs.txt", "/tmp/logs.txt"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestLimactlExecutorRunStream(t *testing.T) {
	var recorded []string
	ex := fakeLimactlExecutor(&recorded, "echo streamed")
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

func TestLimactlExecutorInteractiveShell(t *testing.T) {
	var recorded []string
	ex := fakeLimactlExecutor(&recorded, "exit")
	var stdout, stderr strings.Builder
	if err := ex.InteractiveShell(context.Background(), &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("InteractiveShell() error = %v", err)
	}
	want := []string{"limactl", "shell", "bloud-dev"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestLimactlExecutorReady(t *testing.T) {
	var recorded []string
	ex := fakeLimactlExecutor(&recorded, `echo '{"name":"bloud-dev","status":"Running"}'`)
	if !ex.Ready() {
		t.Error("Ready() = false, want true")
	}
	want := []string{"limactl", "list", "--json"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestLimactlExecutorReadyNotRunning(t *testing.T) {
	var recorded []string
	ex := fakeLimactlExecutor(&recorded, `echo '{"name":"bloud-dev","status":"Stopped"}'`)
	if ex.Ready() {
		t.Error("Ready() = true, want false")
	}
}

func TestSSHExecutorRunInvocation(t *testing.T) {
	conn := SSHConn{Host: "127.0.0.1", Port: 2222, User: "bloud", KeyFile: "/key"}
	var recorded []string
	ex := fakeSSHExecutor(&recorded, "echo fake-out", conn)
	res, err := ex.Run(context.Background(), RunSpec{Command: "echo", Args: []string{"hi"}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"ssh", "-p", "2222", "-i", "/key", "-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes", "bloud@127.0.0.1", "bash", "-c", "'echo hi'"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
	if res.Stdout != "fake-out\n" {
		t.Fatalf("Stdout = %q, want %q", res.Stdout, "fake-out\n")
	}
}

func TestSSHExecutorRunScriptReflectsSpec(t *testing.T) {
	conn := SSHConn{Host: "127.0.0.1", Port: 2222, User: "bloud", KeyFile: "/key"}
	var recorded []string
	ex := fakeSSHExecutor(&recorded, "echo ok", conn)
	if _, err := ex.Run(context.Background(), RunSpec{
		Command: "./host-agent",
		Dir:     "/runtime",
		Env:     map[string]string{"BLOUD_DATA_DIR": "/runtime/data"},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []string{"ssh", "-p", "2222", "-i", "/key", "-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes", "bloud@127.0.0.1", "bash", "-c", "'export BLOUD_DATA_DIR='\"'\"'/runtime/data'\"'\"'; cd '\"'\"'/runtime'\"'\"'; ./host-agent'"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestSSHExecutorCopyToDirIsRecursive(t *testing.T) {
	conn := SSHConn{Host: "127.0.0.1", Port: 2222, User: "bloud", KeyFile: "/key"}
	dir := t.TempDir()
	var recorded []string
	ex := fakeSSHExecutor(&recorded, "true", conn)
	if err := ex.CopyTo(context.Background(), dir, "/runtime/web/build"); err != nil {
		t.Fatalf("CopyTo() error = %v", err)
	}
	// Directory sources get a trailing slash so rsync copies the contents to
	// the destination instead of nesting the source basename inside it.
	want := []string{"rsync", "-a", "-r", "-e", "ssh -p 2222 -i /key", dir + "/", "bloud@127.0.0.1:/runtime/web/build"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestSSHExecutorCopyFrom(t *testing.T) {
	conn := SSHConn{Host: "127.0.0.1", Port: 2222, User: "bloud", KeyFile: "/key"}
	var recorded []string
	ex := fakeSSHExecutor(&recorded, "true", conn)
	if err := ex.CopyFrom(context.Background(), "/runtime/logs.txt", "/tmp/logs.txt"); err != nil {
		t.Fatalf("CopyFrom() error = %v", err)
	}
	want := []string{"rsync", "-a", "-e", "ssh -p 2222 -i /key", "bloud@127.0.0.1:/runtime/logs.txt", "/tmp/logs.txt"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestSSHExecutorInteractiveShell(t *testing.T) {
	conn := SSHConn{Host: "127.0.0.1", Port: 2222, User: "bloud", KeyFile: "/key"}
	var recorded []string
	ex := fakeSSHExecutor(&recorded, "exit", conn)
	var stdout, stderr strings.Builder
	if err := ex.InteractiveShell(context.Background(), &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("InteractiveShell() error = %v", err)
	}
	want := []string{"ssh", "-p", "2222", "-i", "/key", "-t", "bloud@127.0.0.1"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestSSHExecutorReady(t *testing.T) {
	conn := SSHConn{Host: "127.0.0.1", Port: 2222, User: "bloud", KeyFile: "/key"}
	var recorded []string
	ex := fakeSSHExecutor(&recorded, "true", conn)
	if !ex.Ready() {
		t.Error("Ready() = false, want true")
	}
	want := []string{"ssh", "-p", "2222", "-i", "/key", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=5", "bloud@127.0.0.1", "true"}
	if !slices.Equal(recorded, want) {
		t.Fatalf("invocation = %q, want %q", recorded, want)
	}
}

func TestSSHExecutorReadyFails(t *testing.T) {
	conn := SSHConn{Host: "127.0.0.1", Port: 2222, User: "bloud", KeyFile: "/key"}
	var recorded []string
	ex := fakeSSHExecutor(&recorded, "exit 1", conn)
	if ex.Ready() {
		t.Error("Ready() = true, want false")
	}
}
