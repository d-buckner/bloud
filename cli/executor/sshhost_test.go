package executor

import (
	"context"
	"errors"
	"io"
	"testing"
)

type stubExecutor struct {
	runResult ExecResult
	runErr    error
	runCalled bool
	runSpec   RunSpec
}

func (s *stubExecutor) Run(_ context.Context, spec RunSpec) (ExecResult, error) {
	s.runCalled = true
	s.runSpec = spec
	return s.runResult, s.runErr
}

func (s *stubExecutor) RunStream(_ context.Context, _ RunSpec, _, _ io.Writer) error { return nil }
func (s *stubExecutor) CopyTo(_ context.Context, _, _ string) error                  { return nil }
func (s *stubExecutor) CopyFrom(_ context.Context, _, _ string) error                { return nil }

func TestIsVMNameRunning(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{name: "matching running instance", out: `{"name":"bloud-dev","status":"Running"}` + "\n", want: true},
		{name: "matching stopped instance", out: `{"name":"bloud-dev","status":"Stopped"}` + "\n", want: false},
		{name: "other instances running", out: `{"name":"other","status":"Running"}` + "\n", want: false},
		{name: "empty output", out: "", want: false},
		{name: "malformed line skipped", out: `not json` + "\n" + `{"name":"bloud-dev","status":"Running"}`, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsVMNameRunning(tt.out, "bloud-dev"); got != tt.want {
				t.Errorf("IsVMNameRunning(%q) = %v, want %v", tt.out, got, tt.want)
			}
		})
	}
}

func TestIsVMNamePresent(t *testing.T) {
	out := `{"name":"other","status":"Running"}` + "\n" + `{"name":"bloud-dev","status":"Stopped"}`
	if !IsVMNamePresent(out, "bloud-dev") {
		t.Error("IsVMNamePresent() = false, want true")
	}
	if IsVMNamePresent(out, "missing") {
		t.Error("IsVMNamePresent() = true, want false")
	}
}

func TestSSHHostReadyRunning(t *testing.T) {
	local := &stubExecutor{runResult: ExecResult{Stdout: `{"name":"bloud-dev","status":"Running"}` + "\n"}}
	host := NewSSHHost("bloud-dev", &stubExecutor{}, local, nil, DataDirs{})
	if !host.Ready() {
		t.Error("Ready() = false, want true")
	}
	if !local.runCalled {
		t.Fatal("local executor was not invoked")
	}
	if local.runSpec.Command != "limactl" || len(local.runSpec.Args) != 2 || local.runSpec.Args[0] != "list" || local.runSpec.Args[1] != "--json" {
		t.Fatalf("ready check invoked %q %q, want limactl list --json", local.runSpec.Command, local.runSpec.Args)
	}
}

func TestSSHHostReadyStopped(t *testing.T) {
	local := &stubExecutor{runResult: ExecResult{Stdout: `{"name":"bloud-dev","status":"Stopped"}` + "\n"}}
	host := NewSSHHost("bloud-dev", &stubExecutor{}, local, nil, DataDirs{})
	if host.Ready() {
		t.Error("Ready() = true, want false")
	}
}

func TestSSHHostReadyError(t *testing.T) {
	local := &stubExecutor{runErr: errors.New("limactl not found")}
	host := NewSSHHost("bloud-dev", &stubExecutor{}, local, nil, DataDirs{})
	if host.Ready() {
		t.Error("Ready() = true, want false")
	}
}

func TestSSHHostDescription(t *testing.T) {
	remote := &stubExecutor{}
	ports := map[string]string{"host-agent": "3000", "traefik": "8080"}
	dirs := DataDirs{HostAgentDir: "/runtime/host-agent", DataDir: "/runtime/data", AppsDir: "/repo/apps"}
	host := NewSSHHost("bloud-dev", remote, &stubExecutor{}, ports, dirs)

	if host.Executor() != remote {
		t.Error("Executor() did not return the remote executor")
	}
	if len(host.Ports()) != 2 || host.Ports()["traefik"] != "8080" {
		t.Fatalf("Ports() = %v, want traefik:8080", host.Ports())
	}
	if host.DataDirs() != dirs {
		t.Fatalf("DataDirs() = %v, want %v", host.DataDirs(), dirs)
	}
}
