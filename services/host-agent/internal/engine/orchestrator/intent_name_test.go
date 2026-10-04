// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import "testing"

// TestIntentTypeName_AllTypes pins the human-readable name of every intent
// type. intentTypeName derives the name from the concrete type, so this test
// is the exhaustive check that a newly added intent type is acknowledged and
// gets the expected name rather than silently reporting "Unknown".
func TestIntentTypeName_AllTypes(t *testing.T) {
	cases := []struct {
		intent Intent
		want   string
	}{
		{NewInstallAppIntent("x"), "InstallApp"},
		{NewUninstallAppIntent("x", false), "UninstallApp"},
		{NewRenameAppIntent("x", "y"), "RenameApp"},
		{NewClearAppDataIntent("x"), "ClearAppData"},
		{NewSetPublicURLIntent("http://x"), "SetPublicURL"},
		{NewSetInferenceIntent("[]", "", nil), "SetInference"},
		{NewReconcileIntent(), "Reconcile"},
	}
	for _, c := range cases {
		if got := intentTypeName(c.intent); got != c.want {
			t.Errorf("intentTypeName(%T) = %q, want %q", c.intent, got, c.want)
		}
	}
}
