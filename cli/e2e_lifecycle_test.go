package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseLifecycleConfig(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "host-agent.env")
	if err := os.WriteFile(envFile, []byte("DATABASE_URL=test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"BLOUD_E2E_SSH_TARGET": "bloud@test-host",
		"BLOUD_E2E_ENV_FILE":   envFile,
	}

	cfg, help, err := parseLifecycleConfig("/repo", []string{"--host-only", "--keep"}, func(key string) string {
		return values[key]
	})
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
	envFile := filepath.Join(t.TempDir(), "host-agent.env")
	if err := os.WriteFile(envFile, nil, 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := parseLifecycleConfig("/repo", nil, func(key string) string {
		switch key {
		case "BLOUD_E2E_SSH_TARGET":
			return "test-host"
		case "BLOUD_E2E_ENV_FILE":
			return envFile
		default:
			return ""
		}
	})
	if err == nil || !strings.Contains(err.Error(), "BLOUD_URL") {
		t.Fatalf("expected missing BLOUD_URL error, got %v", err)
	}
}

func TestParseLifecycleConfigRejectsRootRuntimeDirectory(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "host-agent.env")
	if err := os.WriteFile(envFile, nil, 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := parseLifecycleConfig("/repo", []string{"--host-only"}, func(key string) string {
		switch key {
		case "BLOUD_E2E_SSH_TARGET":
			return "test-host"
		case "BLOUD_E2E_ENV_FILE":
			return envFile
		case "BLOUD_E2E_RUNTIME_DIR":
			return "/"
		default:
			return ""
		}
	})
	if err == nil || !strings.Contains(err.Error(), "non-root absolute path") {
		t.Fatalf("expected unsafe runtime directory error, got %v", err)
	}
}

func TestParseLifecycleConfigRejectsBroadRuntimeDirectory(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "host-agent.env")
	if err := os.WriteFile(envFile, nil, 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := parseLifecycleConfig("/repo", []string{"--host-only"}, func(key string) string {
		switch key {
		case "BLOUD_E2E_SSH_TARGET":
			return "test-host"
		case "BLOUD_E2E_ENV_FILE":
			return envFile
		case "BLOUD_E2E_RUNTIME_DIR":
			return "/tmp"
		default:
			return ""
		}
	})
	if err == nil || !strings.Contains(err.Error(), "dedicated child directory") {
		t.Fatalf("expected broad runtime directory error, got %v", err)
	}
}

func TestRenderLifecycleHostAgentUnit(t *testing.T) {
	unit := renderLifecycleHostAgentUnit(lifecycleConfig{
		remoteDir:  "/tmp/bloud-e2e",
		traefikDir: "/srv/traefik/dynamic",
		redisAddr:  "10.89.0.2:6379",
	})

	for _, expected := range []string{
		"Environment=BLOUD_DATA_DIR=/tmp/bloud-e2e/data",
		"Environment=BLOUD_TRAEFIK_DYNAMIC_DIR=/srv/traefik/dynamic",
		"Environment=BLOUD_REDIS_ADDR=10.89.0.2:6379",
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

func TestWriteSanitizedLifecycleEnvFileRemovesRunnerOwnedSettings(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.env")
	destination := filepath.Join(dir, "destination.env")
	err := os.WriteFile(source, []byte(strings.Join([]string{
		"BLOUD_DATA_DIR=/home/daniel.guest/.local/share/bloud",
		"BLOUD_APPS_DIR=/Users/daniel/Projects/bloud/apps",
		"DATABASE_URL=postgres://apps:test@localhost/bloud",
		"BLOUD_LDAP_HOST=apps-authentik-ldap",
		"",
	}, "\n")), 0600)
	if err != nil {
		t.Fatal(err)
	}

	if err := writeSanitizedLifecycleEnvFile(source, destination); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, removed := range []string{"BLOUD_DATA_DIR", "BLOUD_APPS_DIR"} {
		if strings.Contains(text, removed) {
			t.Fatalf("expected %s to be removed from sanitized env:\n%s", removed, text)
		}
	}
	if !strings.Contains(text, "DATABASE_URL=") || !strings.Contains(text, "BLOUD_LDAP_HOST=") {
		t.Fatalf("expected core service settings to remain:\n%s", text)
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
	devDir := filepath.Join(root, "dev")
	if err := os.MkdirAll(devDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devDir, "host-agent.env"), nil, 0600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := parseLifecycleConfig(root, []string{"--host-only"}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.lima != "bloud-dev" {
		t.Fatalf("expected bloud-dev Lima default, got %+v", cfg)
	}
	if cfg.envFile != filepath.Join(root, "dev", "host-agent.env") {
		t.Fatalf("expected default Lima env file, got %+v", cfg)
	}
}
