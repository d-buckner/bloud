// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestCrashRecoveryViaReconcile simulates a container crash, then restarts
// only the host-agent process. Recovery must come from the host-agent's
// startup convergence (graph reconciliation + idempotent PreStart/PostStart),
// not from the supervisor or any direct container manipulation.
func TestCrashRecoveryViaReconcile(t *testing.T) {
	unit := os.Getenv("BLOUD_E2E_HOST_AGENT_UNIT")
	if unit == "" {
		t.Skip("BLOUD_E2E_HOST_AGENT_UNIT not set; skipping crash recovery test")
	}

	// Install the app this test crashes, rather than waiting for another test
	// to install it: Go runs tests in file order, and
	// crash_recovery_test.go sorts before jellyfin_test.go, so the wait used
	// to run against an app that was not installed yet and time out with an
	// empty status.
	postJSON(t, hostAgentURL+"/api/apps/jellyfin/install", `{}`, http.StatusAccepted)
	waitAppRunning(t, "jellyfin", 5*time.Minute)
	waitHTTPOrFatal(t, 60*time.Second, jellyfinURL+"/health")

	// Fault injection: simulate the Jellyfin container crashing.
	out, err := exec.Command("podman", "stop", "apps-jellyfin").CombinedOutput()
	if err != nil {
		t.Fatalf("podman stop apps-jellyfin (fault injection): %v\n%s", err, out)
	}
	t.Log("fault injected: apps-jellyfin stopped")

	// Restart only the host-agent process. The supervisor brings the process
	// back; the host-agent's startup convergence brings the container back.
	out, err = exec.Command("systemctl", "--user", "restart", unit).CombinedOutput()
	if err != nil {
		t.Fatalf("systemctl --user restart %s: %v\n%s", unit, err, out)
	}
	waitHTTPOrFatal(t, 120*time.Second, hostAgentURL+"/api/health")

	// The host-agent must recover the container through the graph.
	waitAppRunning(t, "jellyfin", 5*time.Minute)
	running := runCmd(t, "podman", "inspect", "-f", `{{.State.Running}}`, "apps-jellyfin")
	if running != "true" {
		t.Fatalf("apps-jellyfin not running after recovery (State.Running=%s)", running)
	}
	waitHTTPOrFatal(t, 60*time.Second, jellyfinURL+"/health")

	// PostStart re-ran during recovery and must have been idempotent:
	// wizard still complete, SSO chain still intact.
	info := getJellyfinSystemInfo(t)
	if !info.StartupWizardCompleted {
		t.Error("StartupWizardCompleted should still be true after recovery")
	}
	jellyfinLDAPLoginAs(t, "admin", readSecrets(t).AuthentikBootstrapPassword)
	t.Log("crash recovery complete: host-agent reconciled the container back")
}

// --- Uninstall through the real path ---

// TestJellyfinUninstallCleanup uninstalls Jellyfin through the API and asserts
// the full cleanup: store entry, container, data directory, and routes.
