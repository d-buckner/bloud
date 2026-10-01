// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIsBackendSourceFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"services/host-agent/internal/api/server.go", true},
		{"apps/jellyfin/configurator.go", true},
		{"apps/jellyfin/metadata.yaml", true},
		// Test files are not compiled into the binary, so restarting for one
		// would be pure noise.
		{"services/host-agent/internal/api/server_test.go", false},
		{"apps/jellyfin/configurator_test.go", false},
		// Frontend files belong to the vite dev server, not the backend build.
		{"services/host-agent/web/src/routes/+page.svelte", false},
		{"services/host-agent/web/vite.config.ts", false},
		// Docs and validation config are not compiled in.
		{"README.md", false},
		{"validation.yaml", false},
		{"docs/architecture/overview.md", false},
	}
	for _, tc := range cases {
		if got := isBackendSourceFile(tc.path); got != tc.want {
			t.Errorf("isBackendSourceFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestTakeSnapshotSkipsVendorTrees(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a/service.go")
	write("node_modules/pkg/thing.go")
	write("build/generated.go")

	snap, err := takeSnapshot(root, isBackendSourceFile)
	if err != nil {
		t.Fatalf("takeSnapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("snapshot = %d files, want 1 (vendor trees must be skipped): %v", len(snap), keysOf(snap))
	}
	if _, ok := snap[filepath.Join(root, "a/service.go")]; !ok {
		t.Fatalf("expected the real source file in the snapshot, got %v", keysOf(snap))
	}
}

func TestDiffSnapshots(t *testing.T) {
	stamp := func(sec int64, size int64) fileStamp {
		return fileStamp{modTime: time.Unix(sec, 0), size: size}
	}

	t.Run("added, changed, and removed all count", func(t *testing.T) {
		prev := sourceSnapshot{"/a.go": stamp(1, 10), "/b.go": stamp(1, 10)}
		next := sourceSnapshot{"/a.go": stamp(2, 10), "/c.go": stamp(1, 10)}
		got := diffSnapshots(prev, next)
		want := []string{"/a.go", "/b.go", "/c.go"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("diff = %v, want %v", got, want)
		}
	})

	t.Run("identical snapshots produce nothing", func(t *testing.T) {
		prev := sourceSnapshot{"/a.go": stamp(1, 10)}
		next := sourceSnapshot{"/a.go": stamp(1, 10)}
		if got := diffSnapshots(prev, next); len(got) != 0 {
			t.Fatalf("diff = %v, want empty", got)
		}
	})

	t.Run("size catches a write the mtime clock missed", func(t *testing.T) {
		// A coarse-precision filesystem can report the same timestamp before
		// and after a write; the size difference still has to register.
		prev := sourceSnapshot{"/a.go": stamp(1, 10)}
		next := sourceSnapshot{"/a.go": stamp(1, 40)}
		if got := diffSnapshots(prev, next); !reflect.DeepEqual(got, []string{"/a.go"}) {
			t.Fatalf("diff = %v, want [/a.go]", got)
		}
	})
}

func TestParseDevFlags(t *testing.T) {
	cases := []struct {
		args      []string
		wantWatch bool
		wantReset bool
		wantErr   bool
	}{
		// Hot reload is the default: the one-shot loop is the slow,
		// destructive one, so it should never be what you get by accident.
		{nil, true, false, false},
		{[]string{}, true, false, false},
		{[]string{"--watch"}, true, false, false},
		{[]string{"--no-watch"}, false, false, false},
		// --reset is orthogonal to the loop shape: it wipes first, then
		// runs whichever loop the other flags selected.
		{[]string{"--reset"}, true, true, false},
		{[]string{"--reset", "--no-watch"}, false, true, false},
		{[]string{"--no-watch", "--reset"}, false, true, false},
		// An unknown flag is an error, not a silently ignored option: a typo
		// in a flag that wipes data must not pass unnoticed.
		{[]string{"--bogus"}, false, false, true},
		{[]string{"--reseet"}, false, false, true},
		{[]string{"-y"}, false, false, true},
	}
	for _, tc := range cases {
		got, err := parseDevFlags(tc.args)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseDevFlags(%v) = %+v, want an error", tc.args, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDevFlags(%v) returned unexpected error: %v", tc.args, err)
			continue
		}
		if got.watch != tc.wantWatch {
			t.Errorf("parseDevFlags(%v).watch = %v, want %v", tc.args, got.watch, tc.wantWatch)
		}
		if got.reset != tc.wantReset {
			t.Errorf("parseDevFlags(%v).reset = %v, want %v", tc.args, got.reset, tc.wantReset)
		}
	}
}

func TestDescribeChangeBatch(t *testing.T) {
	single := describeChangeBatch([]string{"/home/dev/bloud/services/host-agent/internal/api/server.go"})
	if single == "" {
		t.Fatal("describeChangeBatch returned an empty string")
	}
	if want := "services/host-agent/internal/api/server.go"; !strings.Contains(single, want) {
		t.Errorf("describeChangeBatch = %q, want it to name %q", single, want)
	}

	multi := describeChangeBatch([]string{
		"/home/dev/bloud/services/host-agent/internal/api/server.go",
		"/home/dev/bloud/apps/jellyfin/configurator.go",
	})
	if multi == "" {
		t.Fatal("describeChangeBatch returned an empty string for a multi-file batch")
	}
	// A long batch is summarised rather than dumped in full.
	long := describeChangeBatch([]string{
		"/a/services/host-agent/one.go",
		"/a/services/host-agent/two.go",
		"/a/apps/immich/three.go",
		"/a/apps/affine/four.go",
		"/a/apps/hermes/five.go",
		"/a/apps/radarr/six.go",
	})
	if len(long) > 200 {
		t.Errorf("a long batch should be summarised, got %d chars: %q", len(long), long)
	}
}

func keysOf(m sourceSnapshot) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
