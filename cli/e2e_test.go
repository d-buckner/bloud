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
	spec := filepath.Join(root, "e2e", "node_modules", "@playwright", "test")
	if err := os.MkdirAll(spec, 0o755); err != nil {
		t.Fatalf("seed the installed suite: %v", err)
	}

	if err := requireE2EDeps(root); err != nil {
		t.Errorf("requireE2EDeps failed with the suite installed: %v", err)
	}
}
