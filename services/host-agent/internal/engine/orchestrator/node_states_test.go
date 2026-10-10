// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
)

// TestNodeStates_PairsPhaseWithTarget pins what makes "current" readable without
// the reader knowing the phase vocabulary: the phase a node is in and the phase
// it is being driven to.
func TestNodeStates_PairsPhaseWithTarget(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("apps-immich-server"))
	require.NoError(t, to.g.SetActualStatus("apps-immich-server", graph.StatusStarting, ""))

	states := to.orch.NodeStates()
	require.Contains(t, states, "apps-immich-server")
	assert.Equal(t, "starting", states["apps-immich-server"].Phase)
	assert.Equal(t, "queued", states["apps-immich-server"].Target)
	assert.True(t, states["apps-immich-server"].InFlight, "not at target yet")
}

// An errored node is terminal until something resets it: nothing is working on
// it, so lighting it up as the current node would send the reader to a node
// nobody is touching.
func TestNodeStates_ErrorIsStalledNotInFlight(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("apps-immich-server"))
	require.NoError(t, to.g.SetActualStatus("apps-immich-server", graph.StatusError, "image pull failed"))

	state := to.orch.NodeStates()["apps-immich-server"]
	assert.Equal(t, "failed", state.Phase)
	assert.Equal(t, "image pull failed", state.Reason)
	assert.False(t, state.InFlight)
}

// A resync of a RUNNING node leaves both statuses at running while PreStart is
// in the middle of it, so the status pair alone cannot say which node the engine
// has in hand. The active set is what says it.
func TestNodeStates_ActiveNodeIsInFlightAtItsTarget(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("apps-immich-server"))
	require.NoError(t, to.g.SetActualStatus("apps-immich-server", graph.StatusRunning, ""))
	require.NoError(t, to.g.SetTargetStatus("apps-immich-server", graph.StatusRunning))
	require.False(t, to.orch.NodeStates()["apps-immich-server"].InFlight)

	done := to.orch.markNodeActive("apps-immich-server")
	assert.Equal(t, map[string]bool{"apps-immich-server": true}, to.orch.ActiveNodes())
	assert.True(t, to.orch.NodeStates()["apps-immich-server"].InFlight)

	done()
	assert.Empty(t, to.orch.ActiveNodes())
	assert.False(t, to.orch.NodeStates()["apps-immich-server"].InFlight)

	// A second release is a no-op, so a deferred call and an explicit one
	// cannot leave a node stuck as current.
	done()
	assert.Empty(t, to.orch.ActiveNodes())
}

// NodePhases is the projection the developer graph's status column reads; it
// stays consistent with NodeStates rather than being computed twice.
func TestNodeStates_ProjectionMatchesPhases(t *testing.T) {
	to := newTestOrchestrator()

	require.NoError(t, to.g.AddNode("apps-immich-server"))
	require.NoError(t, to.g.SetActualStatus("apps-immich-server", graph.StatusPostStartConfig, ""))

	states := to.orch.NodeStates()
	phases := to.orch.NodePhases()
	require.Len(t, phases, len(states))
	assert.Equal(t, states["apps-immich-server"].Phase, phases["apps-immich-server"])
}
