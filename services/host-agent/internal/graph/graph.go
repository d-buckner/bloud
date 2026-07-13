// Package graph manages the lifecycle state graph for installed apps.
// Each node tracks a targetStatus (desired) and actualStatus (current),
// and fires events when either changes. Topological ordering ensures
// dependencies are always processed before dependents.
package graph

import (
	"fmt"
	"sync"
)

// NodeStatus represents a lifecycle phase for an app node.
type NodeStatus string

const (
	StatusInitializing    NodeStatus = "INITIALIZING"
	StatusPreStartConfig  NodeStatus = "PRESTART_CONFIG"
	StatusStarting        NodeStatus = "STARTING"
	StatusPostStartConfig NodeStatus = "POSTSTART_CONFIG"
	StatusRunning         NodeStatus = "RUNNING"
	StatusError           NodeStatus = "ERROR"
)

// EventType identifies the kind of graph event.
type EventType string

const (
	// EventTargetUpdated fires when a node's target status changes.
	EventTargetUpdated EventType = "TARGET_UPDATED"

	// EventNodeUpdated fires when a node's actual status changes.
	EventNodeUpdated EventType = "NODE_UPDATED"
)

// Node is the state record for a single app in the lifecycle graph.
type Node struct {
	ID           string
	TargetStatus NodeStatus
	ActualStatus NodeStatus
	Error        string
}

// EventHandler is called when a graph event fires.
type EventHandler func(node Node)

// Graph is a directed acyclic graph of app lifecycle nodes.
// It is safe for concurrent use.
type Graph struct {
	mu        sync.RWMutex
	repo      GraphRepository
	listeners map[EventType][]EventHandler
}

// New creates a Graph backed by the provided repository.
func New(repo GraphRepository) *Graph {
	return &Graph{
		repo:      repo,
		listeners: make(map[EventType][]EventHandler),
	}
}

// On registers an event handler. Handlers are called synchronously in the
// goroutine that triggers the event; they must not block for long.
func (g *Graph) On(event EventType, handler EventHandler) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.listeners[event] = append(g.listeners[event], handler)
}

// AddNode inserts a new node with INITIALIZING/INITIALIZING status.
// Returns an error if the node already exists.
func (g *Graph) AddNode(id string) error {
	existing, err := g.repo.GetNode(id)
	if err != nil {
		return err
	}
	if existing != nil {
		return fmt.Errorf("node %q already exists", id)
	}
	return g.repo.SaveNode(Node{
		ID:           id,
		TargetStatus: StatusInitializing,
		ActualStatus: StatusInitializing,
	})
}

// AddEdge records that dependentID depends on dependencyID (dependency runs first).
// Returns an error if either node is missing or if adding the edge would create a cycle.
func (g *Graph) AddEdge(dependentID, dependencyID string) error {
	dep, err := g.repo.GetNode(dependentID)
	if err != nil {
		return err
	}
	if dep == nil {
		return fmt.Errorf("node %q not found", dependentID)
	}
	parent, err := g.repo.GetNode(dependencyID)
	if err != nil {
		return err
	}
	if parent == nil {
		return fmt.Errorf("node %q not found", dependencyID)
	}
	if err := g.wouldCycle(dependentID, dependencyID); err != nil {
		return err
	}
	return g.repo.SaveEdge(dependentID, dependencyID)
}

// GetNode returns the node with the given ID, or nil if it doesn't exist.
func (g *Graph) GetNode(id string) (*Node, error) {
	return g.repo.GetNode(id)
}

// GetDependencies returns the IDs of all direct dependencies of the given node.
func (g *Graph) GetDependencies(nodeID string) ([]string, error) {
	return g.repo.GetDependencies(nodeID)
}

// DeleteNode removes a node and all its edges from the graph.
// Returns an error if the node does not exist.
func (g *Graph) DeleteNode(id string) error {
	return g.repo.DeleteNode(id)
}

// SetTargetStatus changes what the Orchestrator should drive the node toward.
// Fires EventTargetUpdated. No-ops (without firing) if status is already the target.
func (g *Graph) SetTargetStatus(id string, status NodeStatus) error {
	node, err := g.repo.GetNode(id)
	if err != nil {
		return err
	}
	if node == nil {
		return fmt.Errorf("node %q not found", id)
	}
	if node.TargetStatus == status {
		return nil
	}
	node.TargetStatus = status
	if err := g.repo.SaveNode(*node); err != nil {
		return err
	}
	g.emit(EventTargetUpdated, *node)
	return nil
}

// SetActualStatus records the node's current lifecycle phase.
// Fires EventNodeUpdated. Never fires EventTargetUpdated.
func (g *Graph) SetActualStatus(id string, status NodeStatus, errMsg string) error {
	node, err := g.repo.GetNode(id)
	if err != nil {
		return err
	}
	if node == nil {
		return fmt.Errorf("node %q not found", id)
	}
	node.ActualStatus = status
	node.Error = errMsg
	if err := g.repo.SaveNode(*node); err != nil {
		return err
	}
	g.emit(EventNodeUpdated, *node)
	return nil
}

// GetTopologicalLevels returns nodes grouped by topological level using Kahn's algorithm.
// Level 0 contains nodes with no dependencies; each subsequent level depends only on
// earlier levels. Returns an error if the graph contains a cycle.
func (g *Graph) GetTopologicalLevels() ([][]string, error) {
	nodes, err := g.repo.GetNodes()
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, nil
	}

	// inDegree[n] = number of dependencies n has among current nodes
	inDegree := make(map[string]int, len(nodes))
	// dependents[dep] = list of nodes that depend on dep
	dependents := make(map[string][]string, len(nodes))

	for _, node := range nodes {
		if _, ok := inDegree[node.ID]; !ok {
			inDegree[node.ID] = 0
		}
		deps, err := g.repo.GetDependencies(node.ID)
		if err != nil {
			return nil, err
		}
		for _, dep := range deps {
			inDegree[node.ID]++
			dependents[dep] = append(dependents[dep], node.ID)
		}
	}

	// Collect all nodes with zero in-degree into the initial queue.
	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	var result [][]string
	processed := 0

	for len(queue) > 0 {
		level := make([]string, len(queue))
		copy(level, queue)
		result = append(result, level)
		queue = queue[:0]

		for _, id := range level {
			processed++
			for _, dependent := range dependents[id] {
				inDegree[dependent]--
				if inDegree[dependent] == 0 {
					queue = append(queue, dependent)
				}
			}
		}
	}

	if processed != len(nodes) {
		return nil, fmt.Errorf("cycle detected in graph")
	}

	return result, nil
}

// wouldCycle checks whether adding dependentID → dependencyID would create a cycle.
// It does so by walking the existing dependency graph from dependencyID to see if
// it can reach dependentID.
func (g *Graph) wouldCycle(dependentID, dependencyID string) error {
	visited := make(map[string]bool)
	var dfs func(id string) bool
	dfs = func(id string) bool {
		if id == dependentID {
			return true
		}
		if visited[id] {
			return false
		}
		visited[id] = true
		deps, _ := g.repo.GetDependencies(id)
		for _, dep := range deps {
			if dfs(dep) {
				return true
			}
		}
		return false
	}
	if dfs(dependencyID) {
		return fmt.Errorf("adding edge %q → %q would create a cycle", dependentID, dependencyID)
	}
	return nil
}

func (g *Graph) emit(event EventType, node Node) {
	g.mu.RLock()
	handlers := make([]EventHandler, len(g.listeners[event]))
	copy(handlers, g.listeners[event])
	g.mu.RUnlock()
	for _, h := range handlers {
		h(node)
	}
}
