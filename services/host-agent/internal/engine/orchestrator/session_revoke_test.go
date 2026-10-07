// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRevokeTrackerFiresExactlyOnce pins the consume-once semantics.
//
// A revoke that stayed pending would log every user out on every resync, which
// turns a one-time control into a recurring denial of service against the app's
// own users. take() removing the entry is the whole guarantee, so it is
// asserted directly rather than trusted.
func TestRevokeTrackerFiresExactlyOnce(t *testing.T) {
	tracker := newRevokeTracker()

	assert.False(t, tracker.take("hermes-webui"), "nothing requested yet")

	tracker.request("hermes-webui")
	assert.True(t, tracker.take("hermes-webui"), "the request must fire once")
	assert.False(t, tracker.take("hermes-webui"), "and only once")
}

// TestRevokeTrackerIsPerApp proves one app's revoke cannot be consumed by
// another, which would log out the wrong app's users.
func TestRevokeTrackerIsPerApp(t *testing.T) {
	tracker := newRevokeTracker()
	tracker.request("hermes-webui")

	assert.False(t, tracker.take("affine"), "a revoke for one app is not a revoke for another")
	assert.True(t, tracker.take("hermes-webui"))
}

// TestRevokeTrackerIgnoresEmptyNames keeps a malformed intent from creating an
// entry keyed on nothing.
func TestRevokeTrackerIgnoresEmptyNames(t *testing.T) {
	tracker := newRevokeTracker()
	tracker.request("")
	assert.False(t, tracker.take(""))
}

// TestRevokeTrackerNilSafe matches the nil-tolerance the rest of the engine
// assumes for optional collaborators.
func TestRevokeTrackerNilSafe(t *testing.T) {
	var tracker *revokeTracker
	require.NotPanics(t, func() {
		tracker.request("hermes-webui")
		assert.False(t, tracker.take("hermes-webui"))
	})
}

// TestApplyRevokeIntentRecordsTheRequest covers the drain-phase entry point.
func TestApplyRevokeIntentRecordsTheRequest(t *testing.T) {
	o := &Orchestrator{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	o.applyRevokeClientSessionsIntent(NewRevokeClientSessionsIntent("hermes-webui"))
	require.NotNil(t, o.sessionRevokes)
	assert.True(t, o.sessionRevokes.take("hermes-webui"))

	// An empty app name records nothing rather than a wildcard entry.
	empty := &Orchestrator{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	empty.applyRevokeClientSessionsIntent(NewRevokeClientSessionsIntent(""))
	assert.Nil(t, empty.sessionRevokes)
}
