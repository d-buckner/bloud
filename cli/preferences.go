// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Preferences holds operator choices persisted per checkout at
// .bloud/preferences.yaml (gitignored).
type Preferences struct {
	// Backend is the runtime backend: "lima" (macOS VM), "qemu" (Linux VM),
	// or "native" (Linux, no VM — runs directly on the host).
	Backend string `yaml:"backend,omitempty"`
}

// preferencesFile is the per-checkout preferences path.
func preferencesFile(root string) string {
	return filepath.Join(root, ".bloud", "preferences.yaml")
}

// loadPreferences reads the stored preferences; a missing file yields the
// zero value.
func loadPreferences(root string) (Preferences, error) {
	var p Preferences
	data, err := os.ReadFile(preferencesFile(root))
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return p, fmt.Errorf("read %s: %w", preferencesFile(root), err)
	}
	if err := yaml.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("parse %s: %w", preferencesFile(root), err)
	}
	return p, nil
}

// savePreferences writes the stored preferences (creating .bloud/ if needed).
func savePreferences(root string, p Preferences) error {
	path := preferencesFile(root)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(&p)
	if err != nil {
		return err
	}
	header := "# Bloud CLI preferences (per checkout, gitignored)\n" +
		"# Backend runtime: lima (macOS VM) | qemu (Linux VM) | native (Linux, no VM)\n"
	return os.WriteFile(path, []byte(header+string(data)), 0644)
}

// availableBackendsFor returns the runtime backends applicable to a host OS:
// macOS only supports Lima, Linux supports the QEMU VM and the native (no-VM)
// backend. Other hosts fall back to the historical default.
func availableBackendsFor(goos string) []string {
	if goos == "linux" {
		return []string{"qemu", "native"}
	}
	return []string{"lima"}
}

// availableBackends returns the backends applicable to this host.
func availableBackends() []string {
	return availableBackendsFor(runtime.GOOS)
}

// backendDescription is the human-facing one-liner for a backend.
func backendDescription(name string) string {
	switch name {
	case "lima":
		return "Lima VM (macOS)"
	case "qemu":
		return "QEMU VM"
	case "native":
		return "run directly on this host (no VM; used by CI)"
	default:
		return name
	}
}

// storedBackend returns the preference file's backend when it is set and
// applicable to this host; a stale value (e.g. copied from another OS) is
// ignored.
func storedBackend(root string) string {
	p, err := loadPreferences(root)
	if err != nil || p.Backend == "" {
		return ""
	}
	for _, name := range availableBackends() {
		if p.Backend == name {
			return p.Backend
		}
	}
	return ""
}

// stdinIsTerminal reports whether stdin is an interactive terminal.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// stdinReader is the single buffered reader behind all interactive prompts.
// Sharing one reader keeps bufio from reading ahead input meant for a later
// prompt (e.g. the reset confirmation typed after the backend question).
var stdinReader = bufio.NewReader(os.Stdin)

// promptBackend interactively selects one of the options. The first option
// is the default (empty answer). Accepts a 1-based index or a backend name.
// path is the preferences file the choice will be stored in (displayed to
// the user).
func promptBackend(r *bufio.Reader, path string, options []string) (string, error) {
	fmt.Println()
	fmt.Println("  No backend preference set — pick a runtime for this machine")
	fmt.Printf("  (stored in %s):\n\n", path)
	for i, name := range options {
		fmt.Printf("   %d) %-8s %s\n", i+1, name, backendDescription(name))
	}
	for {
		fmt.Printf("  Choice [%d]: ", 1)
		line, readErr := r.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return "", fmt.Errorf("reading backend selection: %w", readErr)
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer == "" {
			if readErr == io.EOF {
				return "", noPreferenceError(options)
			}
			answer = "1"
		}
		if idx, err := strconv.Atoi(answer); err == nil && idx >= 1 && idx <= len(options) {
			return options[idx-1], nil
		}
		for _, name := range options {
			if answer == name {
				return name, nil
			}
		}
		if readErr == io.EOF {
			return "", noPreferenceError(options)
		}
		fmt.Printf("  %sInvalid choice %q — enter 1-%d or a backend name%s\n", colorYellow, answer, len(options), colorReset)
	}
}

// noPreferenceError is the user-facing error for "no backend known and none
// could be asked for".
func noPreferenceError(options []string) error {
	return fmt.Errorf("no backend preference set — run './bloud setup' or set BLOUD_BACKEND (%s)", strings.Join(options, " | "))
}

// resolveBackend determines the runtime backend for a checkout rooted at
// root:
//  1. BLOUD_BACKEND (explicit override; CI relies on it)
//  2. the stored preference (.bloud/preferences.yaml)
//  3. the single backend available on this host (macOS: lima)
//  4. an interactive prompt, whose answer is persisted to the preference file
//
// It errors when no preference can be determined without asking.
func resolveBackend(root string, getenv func(string) string, interactive bool, ask func(options []string) (string, error)) (string, error) {
	if v := getenv("BLOUD_BACKEND"); v != "" {
		return v, nil
	}
	if b := storedBackend(root); b != "" {
		return b, nil
	}
	available := availableBackends()
	if len(available) == 1 {
		return available[0], nil
	}
	if !interactive {
		return "", noPreferenceError(available)
	}
	name, err := ask(available)
	if err != nil {
		return "", err
	}
	if err := savePreferences(root, Preferences{Backend: name}); err != nil {
		return "", fmt.Errorf("saving backend preference: %w", err)
	}
	return name, nil
}

// backendName returns the selected runtime backend: "lima" (macOS VM),
// "qemu" (Linux VM), or "native" (Linux, no VM). Resolution order:
// BLOUD_BACKEND, .bloud/preferences.yaml, the host default, then an
// interactive prompt (which persists its answer). It errors when the backend
// cannot be determined without asking.
func backendName() (string, error) {
	if v := os.Getenv("BLOUD_BACKEND"); v != "" {
		return v, nil
	}
	root, err := getProjectRoot()
	if err != nil {
		return "", err
	}
	return resolveBackend(root, os.Getenv, stdinIsTerminal(), func(options []string) (string, error) {
		return promptBackend(stdinReader, preferencesFile(root), options)
	})
}

// usageBackend resolves the backend for display only (e.g. usage text):
// env override, stored preference, or the host default. It never prompts and
// returns "" when undetermined.
func usageBackend() string {
	if v := os.Getenv("BLOUD_BACKEND"); v != "" {
		return v
	}
	root, err := getProjectRoot()
	if err != nil {
		return ""
	}
	if b := storedBackend(root); b != "" {
		return b
	}
	if available := availableBackends(); len(available) == 1 {
		return available[0]
	}
	return ""
}

// setupBackend selects and persists the runtime backend during './bloud
// setup': an explicit BLOUD_BACKEND wins, an existing stored preference is
// kept, macOS (Lima-only) is chosen automatically, otherwise the user picks
// interactively.
func setupBackend(root string) (string, error) {
	if v := os.Getenv("BLOUD_BACKEND"); v != "" {
		fmt.Printf("  Backend: %s (from BLOUD_BACKEND)\n", v)
		return v, nil
	}
	if b := storedBackend(root); b != "" {
		fmt.Printf("  Backend: %s (stored in %s)\n", b, preferencesFile(root))
		return b, nil
	}
	available := availableBackends()
	if len(available) == 1 {
		if err := savePreferences(root, Preferences{Backend: available[0]}); err != nil {
			return "", fmt.Errorf("saving backend preference: %w", err)
		}
		fmt.Printf("  Backend: %s (automatic — the only backend for this host)\n", available[0])
		return available[0], nil
	}
	name, err := promptBackend(stdinReader, preferencesFile(root), available)
	if err != nil {
		return "", err
	}
	if err := savePreferences(root, Preferences{Backend: name}); err != nil {
		return "", fmt.Errorf("saving backend preference: %w", err)
	}
	fmt.Printf("  Backend: %s (stored in %s)\n", name, preferencesFile(root))
	return name, nil
}
