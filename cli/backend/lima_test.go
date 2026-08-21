// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package backend

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

func fakeLimaBackend(t *testing.T, recorded *[][]string, listOutputs []string) *LimaBackend {
	t.Helper()
	var call int
	return &LimaBackend{
		instance:   "bloud-dev",
		projectDir: "/repo",
		newCmd: func(_ context.Context, name string, args ...string) *exec.Cmd {
			*recorded = append(*recorded, append([]string{name}, args...))
			if name == "limactl" && len(args) == 2 && args[0] == "list" && args[1] == "--json" {
				f, err := os.CreateTemp(t.TempDir(), "limactl-list-*")
				if err != nil {
					return exec.Command("false")
				}
				if len(listOutputs) > 0 {
					idx := call
					if idx >= len(listOutputs) {
						idx = len(listOutputs) - 1
					}
					f.WriteString(listOutputs[idx])
				}
				f.Close()
				call++
				return exec.Command("cat", f.Name())
			}
			return exec.Command("true")
		},
	}
}

func TestLimaBackendCreateAlreadyRunning(t *testing.T) {
	var recorded [][]string
	b := fakeLimaBackend(t, &recorded, []string{`{"name":"bloud-dev","status":"Running"}` + "\n"})
	if err := b.Create(context.Background()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	want := [][]string{{"limactl", "list", "--json"}, {"limactl", "list", "--json"}}
	if !reflect.DeepEqual(recorded, want) {
		t.Fatalf("invocations = %v, want %v", recorded, want)
	}
}

func TestLimaBackendCreateStoppedStartsVM(t *testing.T) {
	var recorded [][]string
	b := fakeLimaBackend(t, &recorded, []string{
		`{"name":"bloud-dev","status":"Stopped"}` + "\n",
		`{"name":"bloud-dev","status":"Running"}` + "\n",
	})
	if err := b.Create(context.Background()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	want := [][]string{
		{"limactl", "list", "--json"},
		{"limactl", "start", "bloud-dev"},
		{"limactl", "list", "--json"},
	}
	if !reflect.DeepEqual(recorded, want) {
		t.Fatalf("invocations = %v, want %v", recorded, want)
	}
}

func TestLimaBackendCreateMissingCreatesVM(t *testing.T) {
	var recorded [][]string
	b := fakeLimaBackend(t, &recorded, []string{
		"",
		`{"name":"bloud-dev","status":"Running"}` + "\n",
	})
	if err := b.Create(context.Background()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	want := [][]string{
		{"limactl", "list", "--json"},
		{"limactl", "create", "--name=bloud-dev", "/repo/dev/lima.yaml"},
		{"limactl", "start", "bloud-dev"},
		{"limactl", "list", "--json"},
	}
	if !reflect.DeepEqual(recorded, want) {
		t.Fatalf("invocations = %v, want %v", recorded, want)
	}
}

func TestLimaBackendCreateVerifyFails(t *testing.T) {
	var recorded [][]string
	b := fakeLimaBackend(t, &recorded, []string{
		`{"name":"bloud-dev","status":"Running"}` + "\n",
		`{"name":"bloud-dev","status":"Stopped"}` + "\n",
	})
	err := b.Create(context.Background())
	if err == nil {
		t.Fatal("Create() error = nil, want non-nil")
	}
}

func TestLimaBackendCreateListFails(t *testing.T) {
	var recorded [][]string
	b := fakeLimaBackend(t, &recorded, nil)
	b.newCmd = func(_ context.Context, name string, args ...string) *exec.Cmd {
		recorded = append(recorded, append([]string{name}, args...))
		return exec.Command("sh", "-c", "exit 1")
	}
	if err := b.Create(context.Background()); err == nil {
		t.Fatal("Create() error = nil, want non-nil")
	}
}

func TestLimaBackendDestroy(t *testing.T) {
	var recorded [][]string
	b := fakeLimaBackend(t, &recorded, nil)
	if err := b.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	want := [][]string{{"limactl", "delete", "--force", "bloud-dev"}}
	if !reflect.DeepEqual(recorded, want) {
		t.Fatalf("invocations = %v, want %v", recorded, want)
	}
}

func TestLimaBackendHost(t *testing.T) {
	var recorded [][]string
	b := fakeLimaBackend(t, &recorded, nil)
	h := b.Host()
	if h.Executor() == nil {
		t.Error("Host().Executor() = nil")
	}
	if got := h.Ports()["traefik"]; got != "8080" {
		t.Errorf("Ports()[traefik] = %q, want 8080", got)
	}
	if got := h.Ports()["host-agent"]; got != "3000" {
		t.Errorf("Ports()[host-agent] = %q, want 3000", got)
	}
	dirs := h.DataDirs()
	if dirs.HostAgentDir != "/var/tmp/bloud-dev-runtime/host-agent" {
		t.Errorf("HostAgentDir = %q, want /var/tmp/bloud-dev-runtime/host-agent", dirs.HostAgentDir)
	}
	if dirs.DataDir != "/var/tmp/bloud-dev-runtime/data" {
		t.Errorf("DataDir = %q, want /var/tmp/bloud-dev-runtime/data", dirs.DataDir)
	}
	if dirs.AppsDir != "/repo/apps" {
		t.Errorf("AppsDir = %q, want /repo/apps", dirs.AppsDir)
	}
}
