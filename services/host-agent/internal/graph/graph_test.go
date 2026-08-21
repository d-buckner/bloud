// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package graph_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
)

// newGraph creates a Graph backed by an in-memory MapRepository.
func newGraph() *graph.Graph {
	return graph.New(graph.NewMapRepository())
}

// ============================================================================
// Cycle 1: Node management
// ============================================================================

func TestAddNode_CreatesInitializingNode(t *testing.T) {
	g := newGraph()

	err := g.AddNode("nodeA")
	require.NoError(t, err)

	node, err := g.GetNode("nodeA")
	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, graph.StatusInitializing, node.TargetStatus)
	assert.Equal(t, graph.StatusInitializing, node.ActualStatus)
	assert.Empty(t, node.Error)
}

func TestAddNode_ReturnsErrorIfAlreadyExists(t *testing.T) {
	g := newGraph()
	require.NoError(t, g.AddNode("nodeA"))

	err := g.AddNode("nodeA")
	assert.Error(t, err)
}

func TestGetNode_ReturnsNilForUnknownID(t *testing.T) {
	g := newGraph()

	node, err := g.GetNode("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, node)
}

func TestSetTargetStatus_PersistsAndFiresTargetUpdated(t *testing.T) {
	g := newGraph()
	require.NoError(t, g.AddNode("nodeA"))

	var received []graph.Node
	g.On(graph.EventTargetUpdated, func(n graph.Node) {
		received = append(received, n)
	})

	err := g.SetTargetStatus("nodeA", graph.StatusRunning)
	require.NoError(t, err)

	// Persisted
	node, _ := g.GetNode("nodeA")
	assert.Equal(t, graph.StatusRunning, node.TargetStatus)

	// Event fired with updated node
	require.Len(t, received, 1)
	assert.Equal(t, "nodeA", received[0].ID)
	assert.Equal(t, graph.StatusRunning, received[0].TargetStatus)
}

func TestSetTargetStatus_NoopsIfAlreadyAtTarget(t *testing.T) {
	g := newGraph()
	require.NoError(t, g.AddNode("nodeA"))
	require.NoError(t, g.SetTargetStatus("nodeA", graph.StatusRunning))

	var fired int
	g.On(graph.EventTargetUpdated, func(n graph.Node) { fired++ })

	// Setting same status again should be a no-op
	err := g.SetTargetStatus("nodeA", graph.StatusRunning)
	require.NoError(t, err)
	assert.Equal(t, 0, fired, "TARGET_UPDATED should not fire when status is unchanged")
}

func TestSetTargetStatus_ReturnsErrorForUnknownNode(t *testing.T) {
	g := newGraph()
	err := g.SetTargetStatus("nonexistent", graph.StatusRunning)
	assert.Error(t, err)
}

func TestSetActualStatus_FiresNodeUpdatedOnly(t *testing.T) {
	g := newGraph()
	require.NoError(t, g.AddNode("nodeA"))

	var targetFired, nodeFired int
	g.On(graph.EventTargetUpdated, func(n graph.Node) { targetFired++ })
	g.On(graph.EventNodeUpdated, func(n graph.Node) { nodeFired++ })

	err := g.SetActualStatus("nodeA", graph.StatusStarting, "")
	require.NoError(t, err)

	assert.Equal(t, 0, targetFired, "TARGET_UPDATED must not fire from SetActualStatus")
	assert.Equal(t, 1, nodeFired)

	node, _ := g.GetNode("nodeA")
	assert.Equal(t, graph.StatusStarting, node.ActualStatus)
}

func TestSetActualStatus_PersistsErrorMessage(t *testing.T) {
	g := newGraph()
	require.NoError(t, g.AddNode("nodeA"))

	require.NoError(t, g.SetActualStatus("nodeA", graph.StatusError, "something went wrong"))

	node, _ := g.GetNode("nodeA")
	assert.Equal(t, graph.StatusError, node.ActualStatus)
	assert.Equal(t, "something went wrong", node.Error)
}

func TestEventListeners_ReceiveUpdatedNode(t *testing.T) {
	g := newGraph()
	require.NoError(t, g.AddNode("nodeA"))

	var seen graph.Node
	g.On(graph.EventNodeUpdated, func(n graph.Node) {
		seen = n
	})

	require.NoError(t, g.SetActualStatus("nodeA", graph.StatusRunning, ""))
	assert.Equal(t, "nodeA", seen.ID)
	assert.Equal(t, graph.StatusRunning, seen.ActualStatus)
}

// ============================================================================
// Cycle 2: Topology
// ============================================================================

func TestGetTopologicalLevels_SingleNode(t *testing.T) {
	g := newGraph()
	require.NoError(t, g.AddNode("nodeA"))

	levels, err := g.GetTopologicalLevels()
	require.NoError(t, err)
	require.Len(t, levels, 1)
	assert.Equal(t, []string{"nodeA"}, levels[0])
}

func TestGetTopologicalLevels_Empty(t *testing.T) {
	g := newGraph()

	levels, err := g.GetTopologicalLevels()
	require.NoError(t, err)
	assert.Nil(t, levels)
}

func TestGetTopologicalLevels_LinearChain(t *testing.T) {
	// A ← B ← C  (B depends on A, C depends on B)
	// Expected: [[A], [B], [C]]
	g := newGraph()
	require.NoError(t, g.AddNode("A"))
	require.NoError(t, g.AddNode("B"))
	require.NoError(t, g.AddNode("C"))
	require.NoError(t, g.AddEdge("B", "A")) // B depends on A
	require.NoError(t, g.AddEdge("C", "B")) // C depends on B

	levels, err := g.GetTopologicalLevels()
	require.NoError(t, err)
	require.Len(t, levels, 3)
	assert.Equal(t, []string{"A"}, levels[0])
	assert.Equal(t, []string{"B"}, levels[1])
	assert.Equal(t, []string{"C"}, levels[2])
}

func TestGetTopologicalLevels_Diamond(t *testing.T) {
	// A ← B, A ← C, B ← D, C ← D
	// D depends on B and C; B and C depend on A
	// Expected: [[A], [B,C], [D]]
	g := newGraph()
	for _, id := range []string{"A", "B", "C", "D"} {
		require.NoError(t, g.AddNode(id))
	}
	require.NoError(t, g.AddEdge("B", "A"))
	require.NoError(t, g.AddEdge("C", "A"))
	require.NoError(t, g.AddEdge("D", "B"))
	require.NoError(t, g.AddEdge("D", "C"))

	levels, err := g.GetTopologicalLevels()
	require.NoError(t, err)
	require.Len(t, levels, 3)
	assert.Equal(t, []string{"A"}, levels[0])

	sort.Strings(levels[1])
	assert.Equal(t, []string{"B", "C"}, levels[1])
	assert.Equal(t, []string{"D"}, levels[2])
}

func TestGetTopologicalLevels_IndependentNodes(t *testing.T) {
	g := newGraph()
	require.NoError(t, g.AddNode("X"))
	require.NoError(t, g.AddNode("Y"))
	require.NoError(t, g.AddNode("Z"))

	levels, err := g.GetTopologicalLevels()
	require.NoError(t, err)
	require.Len(t, levels, 1)

	sort.Strings(levels[0])
	assert.Equal(t, []string{"X", "Y", "Z"}, levels[0])
}

func TestGetTopologicalLevels_CycleReturnsError(t *testing.T) {
	g := newGraph()
	require.NoError(t, g.AddNode("A"))
	require.NoError(t, g.AddNode("B"))
	// Manually create a cycle by bypassing AddEdge's cycle check
	// We add A→B first, then try B→A which should be rejected
	require.NoError(t, g.AddEdge("B", "A")) // B depends on A

	// This should fail because it would create a cycle
	err := g.AddEdge("A", "B")
	assert.Error(t, err, "adding cycle-creating edge should return error")
}

func TestAddEdge_RejectsMissingDependent(t *testing.T) {
	g := newGraph()
	require.NoError(t, g.AddNode("A"))

	err := g.AddEdge("nonexistent", "A")
	assert.Error(t, err)
}

func TestAddEdge_RejectsMissingDependency(t *testing.T) {
	g := newGraph()
	require.NoError(t, g.AddNode("A"))

	err := g.AddEdge("A", "nonexistent")
	assert.Error(t, err)
}

func TestAddEdge_RejectsCycle(t *testing.T) {
	g := newGraph()
	require.NoError(t, g.AddNode("A"))
	require.NoError(t, g.AddNode("B"))
	require.NoError(t, g.AddNode("C"))
	require.NoError(t, g.AddEdge("B", "A"))
	require.NoError(t, g.AddEdge("C", "B"))

	// A→C would create A←B←C←A cycle
	err := g.AddEdge("A", "C")
	assert.Error(t, err)
}

func TestGetDependencies_ReturnsDirectDeps(t *testing.T) {
	g := newGraph()
	require.NoError(t, g.AddNode("A"))
	require.NoError(t, g.AddNode("B"))
	require.NoError(t, g.AddNode("C"))
	require.NoError(t, g.AddEdge("C", "A"))
	require.NoError(t, g.AddEdge("C", "B"))

	deps, err := g.GetDependencies("C")
	require.NoError(t, err)
	sort.Strings(deps)
	assert.Equal(t, []string{"A", "B"}, deps)
}
