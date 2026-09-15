// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package graph

import (
	"fmt"
	"sync"
)

// GraphRepository persists graph nodes and edges.
type GraphRepository interface {
	SaveNode(node Node) error
	GetNode(id string) (*Node, error)
	GetNodes() ([]Node, error)
	DeleteNode(id string) error
	SaveEdge(dependentID, dependencyID string) error
	GetDependencies(nodeID string) ([]string, error)
	GetDependents(nodeID string) ([]string, error)
	DeleteEdge(dependentID, dependencyID string) error
}

// MapRepository is a thread-safe in-memory implementation of GraphRepository.
// Used in tests; not suitable for production (no persistence).
type MapRepository struct {
	mu    sync.RWMutex
	nodes map[string]Node
	// edges[dependent] = set of dependencies
	edges map[string]map[string]struct{}
}

// NewMapRepository creates a new in-memory repository.
func NewMapRepository() *MapRepository {
	return &MapRepository{
		nodes: make(map[string]Node),
		edges: make(map[string]map[string]struct{}),
	}
}

func (r *MapRepository) SaveNode(node Node) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[node.ID] = node
	return nil
}

func (r *MapRepository) GetNode(id string) (*Node, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.nodes[id]
	if !ok {
		return nil, nil
	}
	return &n, nil
}

func (r *MapRepository) GetNodes() ([]Node, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	nodes := make([]Node, 0, len(r.nodes))
	for _, n := range r.nodes {
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (r *MapRepository) DeleteNode(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nodes[id]; !ok {
		return fmt.Errorf("node %q not found", id)
	}
	delete(r.nodes, id)
	delete(r.edges, id)
	for dep := range r.edges {
		delete(r.edges[dep], id)
	}
	return nil
}

func (r *MapRepository) SaveEdge(dependentID, dependencyID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.edges[dependentID] == nil {
		r.edges[dependentID] = make(map[string]struct{})
	}
	r.edges[dependentID][dependencyID] = struct{}{}
	return nil
}

func (r *MapRepository) GetDependencies(nodeID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	deps := r.edges[nodeID]
	result := make([]string, 0, len(deps))
	for dep := range deps {
		result = append(result, dep)
	}
	return result, nil
}

func (r *MapRepository) GetDependents(nodeID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []string
	for dependent, deps := range r.edges {
		if _, ok := deps[nodeID]; ok {
			result = append(result, dependent)
		}
	}
	return result, nil
}

func (r *MapRepository) DeleteEdge(dependentID, dependencyID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.edges[dependentID] == nil {
		return fmt.Errorf("edge %q → %q not found", dependentID, dependencyID)
	}
	if _, ok := r.edges[dependentID][dependencyID]; !ok {
		return fmt.Errorf("edge %q → %q not found", dependentID, dependencyID)
	}
	delete(r.edges[dependentID], dependencyID)
	return nil
}
