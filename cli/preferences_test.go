// SPDX-License-Identifier: AGPL-3.0-only

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
	// A non-Fedora Linux host keeps both backends. Fedora drops native; see
	// TestAvailableBackendsForFedoraDropsNative in distro_test.go.
	useRelease(t, archOSRelease)
	if got := strings.Join(availableBackendsFor("darwin", Release{}), ","); got != "lima" {
		t.Fatalf("darwin backends = %q, want lima only", got)
	}
	if got := strings.Join(availableBackendsFor("linux", Release{ID: "arch"}), ","); got != "qemu,native" {
		t.Fatalf("linux backends = %q, want qemu,native", got)
	}
	if got := strings.Join(availableBackendsFor("windows", Release{}), ","); got != "lima" {
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

// noEnv is a getenv that reports nothing set.
func noEnv(string) string { return "" }

// firstOption is a chooser that always picks the first offered backend.
func firstOption(opts []string) (string, error) { return opts[0], nil }

// askFor returns a chooser that asserts the offered option list and picks
// `want`.
func askFor(t *testing.T, available []string, want string) func([]string) (string, error) {
	return func(opts []string) (string, error) {
		if strings.Join(opts, ",") != strings.Join(available, ",") {
			t.Fatalf("ask options = %v, want %v", opts, available)
		}
		return want, nil
	}
}

// precedenceRelease pins a host where every backend is available, so these
// cases exercise the resolution order rather than the Fedora refusal. Without
// this the test would fail or pass by accident depending on the machine it
// runs on.
func precedenceRelease(t *testing.T) []string {
	t.Helper()
	useRelease(t, archOSRelease)
	// The stored value has to be applicable to this host, or storedBackend
	// discards it as stale (TestStoredBackendIgnoresStaleValue): "qemu" is
	// stale on macOS and "lima" on Linux. Derive it from this host's
	// available backends instead of hardcoding one.
	return availableBackends()
}

func TestResolveBackendEnvOverrideBeatsStored(t *testing.T) {
	available := precedenceRelease(t)
	envSet := func(string) string { return "native" }
	root := t.TempDir()
	if err := savePreferences(root, Preferences{Backend: available[0]}); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveBackend(root, envSet, false, firstOption); err != nil || got != "native" {
		t.Fatalf("env override = %q, %v; want native", got, err)
	}
}

func TestResolveBackendStoredPreferenceWins(t *testing.T) {
	available := precedenceRelease(t)
	root := t.TempDir()
	if err := savePreferences(root, Preferences{Backend: available[0]}); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveBackend(root, noEnv, false, firstOption); err != nil || got != available[0] {
		t.Fatalf("stored preference = %q, %v; want %s", got, err, available[0])
	}
}

// TestResolveBackendNoPreferenceNonInteractive: a single-option host
// auto-resolves; a multi-option host refuses until it is interactive.
func TestResolveBackendNoPreferenceNonInteractive(t *testing.T) {
	available := precedenceRelease(t)
	got, err := resolveBackend(t.TempDir(), noEnv, false, firstOption)
	if len(available) == 1 {
		if err != nil || got != available[0] {
			t.Fatalf("auto resolve = %q, %v; want %s", got, err, available[0])
		}
		return
	}
	if err == nil {
		t.Fatalf("expected non-interactive error, got %q", got)
	}
	if !strings.Contains(err.Error(), "./bloud setup") {
		t.Fatalf("error should point at ./bloud setup: %v", err)
	}
}

func TestResolveBackendInteractiveAnswerPersisted(t *testing.T) {
	available := precedenceRelease(t)
	if len(available) < 2 {
		t.Skip("single-backend host has nothing to choose")
	}
	empty := t.TempDir()
	want := available[1]
	got, err := resolveBackend(empty, noEnv, true, askFor(t, available, want))
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

func TestBackendNameEnvOverride(t *testing.T) {
	useRelease(t, archOSRelease)
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
		{"\n", "qemu"},         // empty = default
		{"  2 \n", "native"},   // padded
		{"native\n", "native"}, // by name
		{"NATIVE\n", "native"}, // case-insensitive
		{"bogus\n1\n", "qemu"}, // invalid, then valid
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
