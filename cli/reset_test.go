// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/cli/backend"
	"codeberg.org/d-buckner/bloud/cli/executor"
)

func TestParseYesFlag(t *testing.T) {
	cases := []struct {
		args    []string
		want    bool
		wantErr bool
	}{
		// The prompt is the default: a destructive command never runs
		// unattended unless the caller said so.
		{nil, false, false},
		{[]string{}, false, false},
		{[]string{"-y"}, true, false},
		{[]string{"--yes"}, true, false},
		{[]string{"--yes", "-y"}, true, false},
		// Unknown flags error rather than being ignored: accepting them would
		// let a typo read as "wipe without asking" when it did nothing.
		{[]string{"-Y"}, false, true},
		{[]string{"--force"}, false, true},
		{[]string{"jellyfin"}, false, true},
	}
	for _, tc := range cases {
		got, err := parseYesFlag("reset", tc.args)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseYesFlag(reset, %v) = %v, want an error", tc.args, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseYesFlag(reset, %v) returned unexpected error: %v", tc.args, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseYesFlag(reset, %v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// recordingExecutor captures the commands a reset was asked to run instead of
// running them, so the wipe's shape is testable without a real runtime.
type recordingExecutor struct {
	cmds []string
	err  error
}

func (r *recordingExecutor) Run(context.Context, executor.RunSpec) (executor.ExecResult, error) {
	return executor.ExecResult{}, r.err
}

func (r *recordingExecutor) RunStream(_ context.Context, spec executor.RunSpec, _, _ io.Writer) error {
	r.cmds = append(r.cmds, spec.Command)
	return r.err
}

func (r *recordingExecutor) CopyTo(context.Context, string, string) error   { return nil }
func (r *recordingExecutor) CopyFrom(context.Context, string, string) error { return nil }

type recordingHost struct {
	ex   executor.Executor
	dirs executor.DataDirs
}

func (h recordingHost) Executor() executor.Executor { return h.ex }
func (h recordingHost) Ports() map[string]string    { return nil }
func (h recordingHost) DataDirs() executor.DataDirs { return h.dirs }
func (h recordingHost) Ready() bool                 { return true }

type recordingBackend struct{ host executor.Host }

func (b recordingBackend) Create(context.Context) error      { return nil }
func (b recordingBackend) Destroy(context.Context) error     { return nil }
func (b recordingBackend) Host() executor.Host               { return b.host }
func (b recordingBackend) SyncProject(context.Context) error { return nil }

func newRecordingBackend() (backend.Backend, *recordingExecutor) {
	ex := &recordingExecutor{}
	return recordingBackend{host: recordingHost{
		ex:   ex,
		dirs: executor.DataDirs{HostAgentDir: "/rt/host-agent", DataDir: "/rt/data", AppsDir: "/rt/apps"},
	}}, ex
}

func TestResetRuntimeSteps(t *testing.T) {
	bk, ex := newRecordingBackend()
	if err := resetRuntime(bk, "lima"); err != nil {
		t.Fatalf("resetRuntime: %v", err)
	}
	if len(ex.cmds) != 3 {
		t.Fatalf("reset ran %d commands, want 3: %v", len(ex.cmds), ex.cmds)
	}
	for i, want := range []string{"host-agent", "podman rm -f", "/rt/data"} {
		if !strings.Contains(ex.cmds[i], want) {
			t.Errorf("step %d = %q, want it to mention %q", i, ex.cmds[i], want)
		}
	}
	// A VM-backed reset also clears the guest-side bloud state dir.
	if !strings.Contains(ex.cmds[2], ".local/share/bloud") {
		t.Errorf("lima wipe = %q, want it to include $HOME/.local/share/bloud", ex.cmds[2])
	}
}

func TestResetRuntimeKeepsNativeHomeDir(t *testing.T) {
	// native runs on the developer's own machine: $HOME/.local/share/bloud is
	// a real path there, so the reset must not touch it.
	bk, ex := newRecordingBackend()
	if err := resetRuntime(bk, "native"); err != nil {
		t.Fatalf("resetRuntime: %v", err)
	}
	if len(ex.cmds) != 3 {
		t.Fatalf("reset ran %d commands, want 3: %v", len(ex.cmds), ex.cmds)
	}
	if strings.Contains(ex.cmds[2], ".local/share/bloud") {
		t.Errorf("native wipe = %q, want no $HOME/.local/share/bloud removal", ex.cmds[2])
	}
}

func TestResetRuntimeStopsOnFirstFailure(t *testing.T) {
	ex := &recordingExecutor{err: errors.New("ssh went away")}
	bk := recordingBackend{host: recordingHost{ex: ex, dirs: executor.DataDirs{DataDir: "/rt/data"}}}
	err := resetRuntime(bk, "lima")
	if err == nil {
		t.Fatal("resetRuntime = nil, want the executor error")
	}
	if !strings.Contains(err.Error(), "ssh went away") {
		t.Errorf("error = %v, want it to wrap the executor failure", err)
	}
	if len(ex.cmds) != 1 {
		t.Errorf("reset continued with %d commands after a failure, want 1", len(ex.cmds))
	}
}

func TestResetIfRequested(t *testing.T) {
	bk, ex := newRecordingBackend()

	// Without --reset the dev loop must not touch anything: hot reload relies
	// on the running stack surviving the invocation.
	if err := resetIfRequested(devFlags{watch: true}, bk, "lima"); err != nil {
		t.Fatalf("resetIfRequested without reset: %v", err)
	}
	if len(ex.cmds) != 0 {
		t.Fatalf("ran %d commands without --reset: %v", len(ex.cmds), ex.cmds)
	}

	if err := resetIfRequested(devFlags{watch: true, reset: true}, bk, "lima"); err != nil {
		t.Fatalf("resetIfRequested with reset: %v", err)
	}
	if len(ex.cmds) != 3 {
		t.Fatalf("ran %d commands with --reset, want the full 3-step wipe: %v", len(ex.cmds), ex.cmds)
	}
}

func TestConfirmDestructive(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"bare yes", "y\n", true},
		{"uppercase yes", "Y\n", true},
		{"yes with padding", "  y  \n", true},
		{"default no", "\n", false},
		{"explicit no", "n\n", false},
		{"anything else", "reset\n", false},
		// A non-interactive stdin reads EOF with no answer: that aborts, so
		// piping into a destructive command never wipes by accident.
		{"closed stdin", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if got := confirmDestructive(&out, bufio.NewReader(strings.NewReader(tc.input)), "Continue? [y/N] "); got != tc.want {
				t.Errorf("confirmDestructive(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
