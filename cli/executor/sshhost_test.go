// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package executor

import (
	"context"
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

// stubTransport implements the Transport interface for host-level tests.
type stubTransport struct {
	stubExecutor
	ready bool
}

func (s *stubTransport) InteractiveShell(_ context.Context, _, _ io.Writer, _ io.Reader) error {
	return nil
}
func (s *stubTransport) Ready() bool { return s.ready }

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
	remote := &stubTransport{ready: true}
	host := NewSSHHost(remote, nil, DataDirs{})
	if !host.Ready() {
		t.Error("Ready() = false, want true")
	}
}

func TestSSHHostReadyStopped(t *testing.T) {
	remote := &stubTransport{ready: false}
	host := NewSSHHost(remote, nil, DataDirs{})
	if host.Ready() {
		t.Error("Ready() = true, want false")
	}
}

func TestSSHHostDescription(t *testing.T) {
	remote := &stubTransport{}
	ports := map[string]string{"host-agent": "3000", "traefik": "8080"}
	dirs := DataDirs{HostAgentDir: "/runtime/host-agent", DataDir: "/runtime/data", AppsDir: "/repo/apps"}
	host := NewSSHHost(remote, ports, dirs)

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
