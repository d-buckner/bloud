// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAvailableBackendsFor(t *testing.T) {
	if got := strings.Join(availableBackendsFor("darwin"), ","); got != "lima" {
		t.Fatalf("darwin backends = %q, want lima only", got)
	}
	if got := strings.Join(availableBackendsFor("linux"), ","); got != "qemu,native" {
		t.Fatalf("linux backends = %q, want qemu,native", got)
	}
	if got := strings.Join(availableBackendsFor("windows"), ","); got != "lima" {
		t.Fatalf("windows backends = %q, want lima (historical default)", got)
	}
}

func TestPreferencesRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := savePreferences(root, Preferences{Backend: "qemu"}); err != nil {
		t.Fatal(err)
	}
	p, err := loadPreferences(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.Backend != "qemu" {
		t.Fatalf("backend = %q, want qemu", p.Backend)
	}
	data, err := os.ReadFile(preferencesFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "backend: qemu") {
		t.Fatalf("file missing 'backend: qemu': %s", data)
	}
}

func TestLoadPreferencesMissingFile(t *testing.T) {
	p, err := loadPreferences(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if p.Backend != "" {
		t.Fatalf("backend = %q, want empty", p.Backend)
	}
}

func TestStoredBackendIgnoresStaleValue(t *testing.T) {
	root := t.TempDir()
	// A backend that does not apply to this host (e.g. a preference file
	// copied from another OS) must be ignored, not honored.
	stale := "qemu"
	if runtime.GOOS == "linux" {
		stale = "lima"
	}
	if err := os.MkdirAll(filepath.Join(root, ".bloud"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preferencesFile(root), []byte("backend: "+stale+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := storedBackend(root); got != "" {
		t.Fatalf("storedBackend = %q, want empty (stale value)", got)
	}
}

func TestResolveBackendPrecedence(t *testing.T) {
	envSet := func(string) string { return "native" }
	getenv := func(string) string { return "" }
	ask := func(opts []string) (string, error) { return opts[0], nil }

	// 1. BLOUD_BACKEND overrides the stored preference.
	root := t.TempDir()
	if err := savePreferences(root, Preferences{Backend: "qemu"}); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveBackend(root, envSet, false, ask); err != nil || got != "native" {
		t.Fatalf("env override = %q, %v; want native", got, err)
	}
	// 2. The stored preference wins when no env override is set.
	if got, err := resolveBackend(root, getenv, false, ask); err != nil || got != "qemu" {
		t.Fatalf("stored preference = %q, %v; want qemu", got, err)
	}
	// 3. No preference: single-option hosts auto-resolve, multi-option
	// hosts error when not interactive.
	empty := t.TempDir()
	available := availableBackends()
	got, err := resolveBackend(empty, getenv, false, ask)
	if len(available) == 1 {
		if err != nil || got != available[0] {
			t.Fatalf("auto resolve = %q, %v; want %s", got, err, available[0])
		}
	} else {
		if err == nil {
			t.Fatalf("expected non-interactive error, got %q", got)
		}
		if !strings.Contains(err.Error(), "./bloud setup") {
			t.Fatalf("error should point at ./bloud setup: %v", err)
		}
	}
	// 4. Interactive answer is persisted.
	if len(available) > 1 {
		want := available[1]
		got, err := resolveBackend(empty, getenv, true, func(opts []string) (string, error) {
			if strings.Join(opts, ",") != strings.Join(available, ",") {
				t.Fatalf("ask options = %v, want %v", opts, available)
			}
			return want, nil
		})
		if err != nil || got != want {
			t.Fatalf("interactive = %q, %v; want %s", got, err, want)
		}
		p, err := loadPreferences(empty)
		if err != nil {
			t.Fatal(err)
		}
		if p.Backend != want {
			t.Fatalf("preference not persisted: %q", p.Backend)
		}
	}
}

func TestBackendNameEnvOverride(t *testing.T) {
	t.Setenv("BLOUD_BACKEND", "native")
	got, err := backendName()
	if err != nil || got != "native" {
		t.Fatalf("backendName = %q, %v; want native", got, err)
	}
}

func TestPromptBackendParsesInput(t *testing.T) {
	options := []string{"qemu", "native"}
	cases := []struct {
		input string
		want  string
	}{
		{"1\n", "qemu"},
		{"2\n", "native"},
		{"\n", "qemu"},             // empty = default
		{"  2 \n", "native"},       // padded
		{"native\n", "native"},     // by name
		{"NATIVE\n", "native"},     // case-insensitive
		{"bogus\n1\n", "qemu"},     // invalid, then valid
	}
	for _, tc := range cases {
		got, err := promptBackend(bufio.NewReader(strings.NewReader(tc.input)), "/tmp/preferences.yaml", options)
		if err != nil {
			t.Fatalf("input %q: %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("input %q = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestPromptBackendEOFWithoutAnswer(t *testing.T) {
	got, err := promptBackend(bufio.NewReader(strings.NewReader("")), "/tmp/preferences.yaml", []string{"qemu", "native"})
	if err == nil {
		t.Fatalf("expected EOF error, got %q", got)
	}
	if !strings.Contains(err.Error(), "./bloud setup") {
		t.Fatalf("error should point at ./bloud setup: %v", err)
	}
}

func TestPromptBackendEOFWithAnswer(t *testing.T) {
	// An answer without a trailing newline (Ctrl-D after typing) still
	// selects.
	got, err := promptBackend(bufio.NewReader(strings.NewReader("native")), "/tmp/preferences.yaml", []string{"qemu", "native"})
	if err != nil || got != "native" {
		t.Fatalf("got %q, %v; want native", got, err)
	}
}

func TestPreferencesFilePath(t *testing.T) {
	if got := preferencesFile("/repo"); got != filepath.Join("/repo", ".bloud", "preferences.yaml") {
		t.Fatalf("preferencesFile = %q", got)
	}
}
