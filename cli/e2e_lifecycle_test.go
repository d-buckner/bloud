// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseLifecycleConfig(t *testing.T) {
	values := map[string]string{
		"BLOUD_E2E_SSH_TARGET": "bloud@test-host",
	}

	cfg, help, err := parseLifecycleConfig("/repo", []string{"--host-only", "--keep"}, func(key string) string {
		return values[key]
	}, "lima")
	if err != nil {
		t.Fatal(err)
	}
	if help {
		t.Fatal("unexpected help result")
	}
	if !cfg.hostOnly || !cfg.keep {
		t.Fatalf("flags not parsed: %+v", cfg)
	}
	if cfg.remoteDir != "/var/tmp/bloud-e2e-runtime" || cfg.goarch != runtime.GOARCH {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if cfg.traefikDir != "/var/tmp/bloud-e2e-runtime/data/traefik/dynamic" {
		t.Fatalf("traefik directory default not applied: %+v", cfg)
	}
}

func TestParseLifecycleConfigRequiresBrowserURL(t *testing.T) {
	_, _, err := parseLifecycleConfig("/repo", nil, func(key string) string {
		switch key {
		case "BLOUD_E2E_SSH_TARGET":
			return "test-host"
		default:
			return ""
		}
	}, "lima")
	if err == nil || !strings.Contains(err.Error(), "BLOUD_URL") {
		t.Fatalf("expected missing BLOUD_URL error, got %v", err)
	}
}

func TestParseLifecycleConfigRejectsRootRuntimeDirectory(t *testing.T) {

	_, _, err := parseLifecycleConfig("/repo", []string{"--host-only"}, func(key string) string {
		switch key {
		case "BLOUD_E2E_SSH_TARGET":
			return "test-host"
		case "BLOUD_E2E_RUNTIME_DIR":
			return "/"
		default:
			return ""
		}
	}, "lima")
	if err == nil || !strings.Contains(err.Error(), "non-root absolute path") {
		t.Fatalf("expected unsafe runtime directory error, got %v", err)
	}
}

func TestParseLifecycleConfigRejectsBroadRuntimeDirectory(t *testing.T) {

	_, _, err := parseLifecycleConfig("/repo", []string{"--host-only"}, func(key string) string {
		switch key {
		case "BLOUD_E2E_SSH_TARGET":
			return "test-host"
		case "BLOUD_E2E_RUNTIME_DIR":
			return "/tmp"
		default:
			return ""
		}
	}, "lima")
	if err == nil || !strings.Contains(err.Error(), "dedicated child directory") {
		t.Fatalf("expected broad runtime directory error, got %v", err)
	}
}

func TestRenderLifecycleHostAgentUnit(t *testing.T) {
	unit := renderLifecycleHostAgentUnit(lifecycleConfig{
		remoteDir:  "/tmp/bloud-e2e",
		traefikDir: "/srv/traefik/dynamic",
	})

	for _, expected := range []string{
		"Environment=BLOUD_DATA_DIR=/tmp/bloud-e2e/data",
		"Environment=BLOUD_TRAEFIK_DYNAMIC_DIR=/srv/traefik/dynamic",
		"ExecStart=/tmp/bloud-e2e/host-agent/host-agent",
	} {
		if !strings.Contains(unit, expected) {
			t.Fatalf("unit missing %q:\n%s", expected, unit)
		}
	}
	for _, removed := range []string{"BLOUD_SYSTEMD_SCOPE", "BLOUD_QUADLET_DIR"} {
		if strings.Contains(unit, removed) {
			t.Fatalf("unit unexpectedly contains %q:\n%s", removed, unit)
		}
	}
}

func TestLifecycleRemoteScriptsAreValidBash(t *testing.T) {
	scripts := []string{
		remotePreflightScript,
		remoteInstallHostAgentScript,
		remoteWaitForHostAgentScript,
		remoteResetJellyfinScript,
		remoteInstallJellyfinScript,
		remoteEnsureUserScript,
		remoteAssertInstalledScript,
		remoteRestartScript,
		remoteUninstallScript,
	}
	for _, script := range scripts {
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader("set -euo pipefail\n" + script)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("invalid remote script: %v\n%s\n%s", err, output, script)
		}
	}
}

func TestRemoteCommandQuotesArguments(t *testing.T) {
	runner := &lifecycle{cfg: lifecycleConfig{sshTarget: "test-host"}}
	cmd := runner.remoteCommand("ignored", `{"username":"test user"}`)
	got := strings.Join(cmd.Args, " ")
	if !strings.Contains(got, `'{"username":"test user"}'`) {
		t.Fatalf("remote argument was not shell quoted: %s", got)
	}
}

func TestLifecycleDefaultsToLima(t *testing.T) {
	root := t.TempDir()
	cfg, _, err := parseLifecycleConfig(root, []string{"--host-only"}, func(string) string { return "" }, "lima")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.lima != "bloud-dev" {
		t.Fatalf("expected bloud-dev Lima default, got %+v", cfg)
	}
}

func TestLifecycleDefaultsToQEMU(t *testing.T) {
	root := t.TempDir()
	cfg, _, err := parseLifecycleConfig(root, []string{"--host-only"}, func(string) string { return "" }, "qemu")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.qemu != "bloud-qemu" {
		t.Fatalf("expected bloud-qemu QEMU default, got %+v", cfg)
	}
	if cfg.sshTarget != "bloud@127.0.0.1" || !strings.HasSuffix(cfg.sshKeyFile, filepath.Join(".bloud", "qemu", "bloud-qemu", "id_ed25519")) {
		t.Fatalf("expected derived SSH target/key, got %+v", cfg)
	}
}

func TestLifecycleNativeBackend(t *testing.T) {
	root := t.TempDir()
	cfg, _, err := parseLifecycleConfig(root, []string{"--host-only"}, func(string) string { return "" }, "native")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.native || cfg.lima != "" || cfg.qemu != "" || cfg.sshTarget != "" {
		t.Fatalf("native config should have no VM instance, got %+v", cfg)
	}
	if cfg.baseURL != "http://localhost:3000" {
		t.Fatalf("native default base URL = %q", cfg.baseURL)
	}
}

func TestLifecycleNativeBackendRejectsExplicitInstance(t *testing.T) {
	root := t.TempDir()
	_, _, err := parseLifecycleConfig(root, []string{"--host-only"}, func(key string) string {
		if key == "BLOUD_E2E_LIMA_INSTANCE" {
			return "bloud-dev"
		}
		return ""
	}, "native")
	if err == nil || !strings.Contains(err.Error(), "native backend") {
		t.Fatalf("expected native+instance rejection, got %v", err)
	}
}
