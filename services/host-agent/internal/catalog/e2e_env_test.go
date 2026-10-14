// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import "testing"

// TestValidateE2EEnv pins that an app's e2e.env names only the dev switches the
// host-agent actually reads. A typo would otherwise be forwarded and silently
// dropped, which reads as "the switch does nothing" rather than "the name is
// wrong".
func TestValidateE2EEnv(t *testing.T) {
	known := &App{E2E: E2EConfig{Env: map[string]string{"BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP": "1"}}}
	if err := validateE2EEnv(known); err != nil {
		t.Fatalf("validateE2EEnv rejected a known key: %v", err)
	}

	unknown := &App{E2E: E2EConfig{Env: map[string]string{"BLOUD_DEV_TYPO_SWITCH": "1"}}}
	if err := validateE2EEnv(unknown); err == nil {
		t.Fatal("validateE2EEnv accepted an unknown e2e.env key")
	}

	empty := &App{}
	if err := validateE2EEnv(empty); err != nil {
		t.Fatalf("validateE2EEnv rejected an absent e2e block: %v", err)
	}
}
