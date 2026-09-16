// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

//go:build integration

package e2e

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestJellyfinUninstallCleanup uninstalls Jellyfin through the API and asserts
// the full cleanup: store entry, container, data directory, and routes.
func TestJellyfinUninstallCleanup(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/jellyfin/uninstall",
		`{"clearData":true}`, http.StatusAccepted)

	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if appStatus(t, "jellyfin") == "" {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if status := appStatus(t, "jellyfin"); status != "" {
		t.Fatalf("jellyfin still listed as installed (status %q)", status)
	}

	// Container must be gone (the orchestrator removes it, not the harness).
	if _, err := exec.Command("podman", "container", "exists", "apps-jellyfin").CombinedOutput(); err == nil {
		t.Fatal("apps-jellyfin container still exists after uninstall")
	}

	// clearData must remove the app's data directory (asserted when the
	// deployer provides the data directory location).
	if os.Getenv("BLOUD_DATA_DIR") != "" {
		dataPath := filepath.Join(dataDir(), "jellyfin")
		if _, err := os.Stat(dataPath); err == nil {
			t.Errorf("data directory %s still exists after clearData uninstall", dataPath)
		}
	}

	// Routes must be regenerated without Jellyfin.
	traefikDir := os.Getenv("BLOUD_TRAEFIK_DYNAMIC_DIR")
	if traefikDir != "" {
		routesPath := filepath.Join(traefikDir, "apps-routes.yml")
		routes, err := os.ReadFile(routesPath)
		if err != nil {
			t.Fatalf("reading %s: %v", routesPath, err)
		}
		if strings.Contains(string(routes), "jellyfin") {
			t.Errorf("apps-routes.yml still references jellyfin after uninstall")
		}
	}
	t.Log("jellyfin fully uninstalled: store, container, data, and routes cleaned up")
}

// --- AFFiNE (native-oidc, own postgres + redis) ---
