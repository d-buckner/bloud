// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The guard has to fire when the suite is absent, and its message has to carry
// the command that fixes it. The failure it replaces arrives from Playwright's
// config loader as ERR_MODULE_NOT_FOUND with no instruction attached.
func TestRequireE2EDepsFailsWithTheFix(t *testing.T) {
	root := t.TempDir()

	err := requireE2EDeps(root)
	if err == nil {
		t.Fatal("requireE2EDeps passed with no e2e/node_modules at all")
	}
	if !strings.Contains(err.Error(), "npm --prefix e2e ci") {
		t.Errorf("error does not name the install command: %v", err)
	}
}

func TestRequireE2EDepsPassesWhenInstalled(t *testing.T) {
	root := t.TempDir()
	for _, spec := range []string{
		filepath.Join(root, "e2e", "node_modules", "@playwright", "test"),
		filepath.Join(root, "e2e", "node_modules", "@bloud", "browser-harness"),
	} {
		if err := os.MkdirAll(spec, 0o755); err != nil {
			t.Fatalf("seed the installed suite: %v", err)
		}
	}

	if err := requireE2EDeps(root); err != nil {
		t.Errorf("requireE2EDeps failed with the suite installed: %v", err)
	}
}

// An install from before the harness existed has @playwright/test but no link
// to @bloud/browser-harness. The guard has to name the same install command,
// because the failure it replaces is an ERR_MODULE_NOT_FOUND on the first
// harness import.
func TestRequireE2EDepsFailsWithoutTheHarnessLink(t *testing.T) {
	root := t.TempDir()
	spec := filepath.Join(root, "e2e", "node_modules", "@playwright", "test")
	if err := os.MkdirAll(spec, 0o755); err != nil {
		t.Fatalf("seed the installed suite: %v", err)
	}

	err := requireE2EDeps(root)
	if err == nil {
		t.Fatal("requireE2EDeps passed without the browser harness link")
	}
	if !strings.Contains(err.Error(), "npm --prefix e2e ci") {
		t.Errorf("error does not name the install command: %v", err)
	}
}

// TestSetAppE2EEnvReadsMetadata pins that the e2e app runner sets an app's dev
// switches from its metadata (e2e.env), not from a CLI or CI hardcode. It is
// the regression this change exists to prevent.
func TestSetAppE2EEnvReadsMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "vaultwarden"), 0o755); err != nil {
		t.Fatal(err)
	}
	md := "name: vaultwarden\ne2e:\n  env:\n    BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP: \"1\"\n"
	if err := os.WriteFile(filepath.Join(root, "apps", "vaultwarden", "metadata.yaml"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	key := "BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP"
	t.Setenv(key, "")
	if err := setAppE2EEnv(root, "vaultwarden"); err != nil {
		t.Fatalf("setAppE2EEnv: %v", err)
	}
	if got := os.Getenv(key); got != "1" {
		t.Errorf("setAppE2EEnv set %s=%q, want %q", key, got, "1")
	}
}
