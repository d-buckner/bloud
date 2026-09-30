// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
)

// addr parses a public URL or fails the test. Every test that needs a specific
// address model should build it from a real origin string rather than from
// struct literals, so the fixture reads the way the setting reads in the UI.
func addr(t *testing.T, raw string) hostset.PublicURL {
	t.Helper()
	u, err := hostset.ParsePublicURL(raw)
	if err != nil {
		t.Fatalf("ParsePublicURL(%q) failed: %v", raw, err)
	}
	return u
}

// addrState builds a live host set around one origin with the given entrypoint
// port, which is the shape the orchestrator hands to the auth module.
func addrState(t *testing.T, raw string, servedPort int) *hostset.State {
	t.Helper()
	return hostset.NewState(hostset.New(addr(t, raw)).WithServedPort(servedPort))
}
