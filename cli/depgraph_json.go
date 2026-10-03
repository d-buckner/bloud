// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
)

// catalogGraphNode is one node of the catalog snapshot, in the shape the
// dashboard's developer graph consumes (see
// services/host-agent/internal/api/system_module.go). The browser renderer
// feeds this straight into the same layout and node components the live
// dashboard uses, so the README image and the dashboard cannot drift apart.
type catalogGraphNode struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
	IsSystem    bool   `json:"isSystem"`
	NodeType    string `json:"nodeType"` // "app" or "container"
	ParentID    string `json:"parentId,omitempty"`
	Category    string `json:"category,omitempty"`
}

// catalogGraphEdge is one edge of the catalog snapshot. Within-app
// dependsOn edges carry no label, the same way the live graph leaves them
// unlabeled inside a box.
type catalogGraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Label  string `json:"label,omitempty"`
}

// catalogGraph is the whole catalog as one graph: every app, every container
// each app declares, and every integration edge between them.
type catalogGraph struct {
	Nodes []catalogGraphNode `json:"nodes"`
	Edges []catalogGraphEdge `json:"edges"`
}

// buildCatalogGraph turns the catalog into the dashboard-shaped snapshot.
// It mirrors system_module's live graph builder, with two differences that
// follow from having no running instance: every node carries the neutral
// `catalog` status, and there are no connection or tunnel nodes.
func buildCatalogGraph(apps map[string]*AppMetadata) catalogGraph {
	nodes := make([]catalogGraphNode, 0, len(apps))
	edges := make([]catalogGraphEdge, 0)

	for _, appName := range sortedAppNames(apps) {
		app := apps[appName]
		nodes = append(nodes, catalogGraphNode{
			ID:          appName,
			DisplayName: appDisplayName(app),
			Status:      catalogNodeStatus,
			IsSystem:    app.IsSystem,
			NodeType:    "app",
			Category:    app.Category,
		})
		edges = append(edges, appContainerNodes(app, &nodes)...)
	}

	// Always present, whatever the catalog declares: see the note on
	// instanceProviderNodeID.
	nodes = append(nodes, catalogGraphNode{
		ID:          instanceProviderNodeID,
		DisplayName: instanceProviderLabel,
		Status:      instanceProviderStatus,
		IsSystem:    false,
		NodeType:    "service",
		Category:    instanceProviderCategory,
	})

	for _, edge := range integrationEdges(apps) {
		edges = append(edges, catalogGraphEdge{Source: edge.from, Target: edge.to, Label: edge.label})
	}
	return catalogGraph{Nodes: nodes, Edges: edges}
}

// appContainerNodes appends one node per container the app declares and
// returns the within-app dependsOn edges between them. An app that declares
// no containers stays a single flat app node, the way the live graph renders
// it. Edges to a container the app does not declare are dropped, so no arrow
// points at a node that is not in the box.
func appContainerNodes(app *AppMetadata, nodes *[]catalogGraphNode) []catalogGraphEdge {
	declared := make(map[string]bool, len(app.Containers))
	for _, container := range app.Containers {
		declared[container.Name] = true
		*nodes = append(*nodes, catalogGraphNode{
			ID:          container.Name,
			DisplayName: containerLabel(container.Name, app.Name),
			Status:      catalogNodeStatus,
			IsSystem:    app.IsSystem,
			NodeType:    "container",
			ParentID:    app.Name,
			Category:    app.Category,
		})
	}

	edges := make([]catalogGraphEdge, 0)
	for _, container := range app.Containers {
		for _, dep := range container.DependsOn {
			if !declared[dep] {
				continue
			}
			edges = append(edges, catalogGraphEdge{Source: container.Name, Target: dep})
		}
	}
	return edges
}

// renderCatalogGraphJSON encodes the catalog snapshot. The output is
// deterministic for a given catalog, so a CI render is reproducible.
func renderCatalogGraphJSON(apps map[string]*AppMetadata) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(buildCatalogGraph(apps)); err != nil {
		return "", err
	}
	return buf.String(), nil
}
