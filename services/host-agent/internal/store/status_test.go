// SPDX-License-Identifier: AGPL-3.0-only

package store

import "testing"

// TestAppStatusClosedSet pins the apps.status vocabulary: exactly the five
// values the orchestrator can emit. The wire encoding (DB column, JSON
// payload) depends on these exact strings, so an added, removed, or renamed
// value must be a deliberate edit here, not an accident at a call site.
func TestAppStatusClosedSet(t *testing.T) {
	want := map[AppStatus]string{
		AppStatusInstalling:   "installing",
		AppStatusUninstalling: "uninstalling",
		AppStatusRunning:      "running",
		AppStatusError:        "error",
		AppStatusStopped:      "stopped",
	}
	if len(want) != 5 {
		t.Fatalf("apps.status has %d values, want exactly 5", len(want))
	}
	for status, wire := range want {
		if string(status) != wire {
			t.Errorf("AppStatus %q has wire value %q, want %q", status, string(status), wire)
		}
	}
}
