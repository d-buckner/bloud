package api

import (
	"net/http"
	"sort"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
)

type graphNode struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
	IsSystem    bool   `json:"isSystem"`
	NodeType    string `json:"nodeType"` // "app" or "connection"
}

type graphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Label  string `json:"label"`
}

type developerGraph struct {
	Nodes []graphNode `json:"nodes"`
	Edges []graphEdge `json:"edges"`
}

// ssoEdgeLabel returns the SSO strategy (e.g. "forward-auth", "ldap") for an app,
// falling back to "sso" if the catalog entry or strategy is unavailable.
func (s *Server) ssoEdgeLabel(appName string) string {
	if s.catalog == nil {
		return "sso"
	}
	app, err := s.catalog.Get(appName)
	if err != nil || app.SSO.Strategy == "" {
		return "sso"
	}
	return app.SSO.Strategy
}

func (s *Server) handleDeveloperGraph(w http.ResponseWriter, r *http.Request) {
	apps, err := s.appStore.GetAll()
	if err != nil {
		s.logger.Error("failed to get apps for developer graph", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get apps")
		return
	}

	// Build catalog integration lookup from the app graph
	var graphDefs map[string]*catalog.AppDefinition
	if s.graph != nil {
		graphDefs = s.graph.GetApps()
	}

	nodes := make([]graphNode, 0, len(apps))
	edges := make([]graphEdge, 0)

	// Track unique tailnet IDs to create connection nodes
	tailnetIDs := make(map[string]bool)
	hasTraefik := false

	for _, app := range apps {
		node := graphNode{
			ID:          app.Name,
			DisplayName: app.DisplayName,
			Status:      app.Status,
			IsSystem:    app.IsSystem,
			NodeType:    "app",
		}
		nodes = append(nodes, node)

		if app.Name == "traefik" {
			hasTraefik = true
		}

		// Track tailnet connection for this app
		if app.TailnetID != "" {
			tailnetIDs[app.TailnetID] = true
			edges = append(edges, graphEdge{
				Source: "conn:tailnet:" + app.TailnetID,
				Target: app.Name,
				Label:  "tailnet",
			})
		}

		// Derive edges: use runtime IntegrationConfig first, fall back to catalog defaults.
		// Sort integration labels for deterministic edge ordering.
		if def, ok := graphDefs[app.Name]; ok {
			labels := make([]string, 0, len(def.Integrations))
			for label := range def.Integrations {
				labels = append(labels, label)
			}
			sort.Strings(labels)

			for _, label := range labels {
				integration := def.Integrations[label]
				edgeLabel := label
				if label == "sso" {
					edgeLabel = s.ssoEdgeLabel(app.Name)
				}

				// Check if user made a runtime choice
				if target, chosen := app.IntegrationConfig[label]; chosen {
					edge := graphEdge{Source: app.Name, Target: target, Label: edgeLabel}
					if label == "proxy" {
						edge.Source, edge.Target = edge.Target, edge.Source
					}
					edges = append(edges, edge)
					continue
				}
				// Fall back to default compatible app
				for _, compat := range integration.Compatible {
					if compat.Default {
						edge := graphEdge{Source: app.Name, Target: compat.App, Label: edgeLabel}
						if label == "proxy" {
							edge.Source, edge.Target = edge.Target, edge.Source
						}
						edges = append(edges, edge)
						break
					}
				}
			}
		} else {
			// No catalog entry — use IntegrationConfig directly
			labels := make([]string, 0, len(app.IntegrationConfig))
			for label := range app.IntegrationConfig {
				labels = append(labels, label)
			}
			sort.Strings(labels)

			for _, label := range labels {
				edgeLabel := label
				if label == "sso" {
					edgeLabel = s.ssoEdgeLabel(app.Name)
				}
				edge := graphEdge{Source: app.Name, Target: app.IntegrationConfig[label], Label: edgeLabel}
				if label == "proxy" {
					edge.Source, edge.Target = edge.Target, edge.Source
				}
				edges = append(edges, edge)
			}
		}
	}

	// Add tailnet connection nodes and gateway
	for tailnetID := range tailnetIDs {
		displayName := "Tailnet"
		status := "unknown"
		if conn, err := s.tailnetStore.GetByID(tailnetID); err == nil && conn != nil {
			displayName = conn.Name
			status = conn.Status
		}
		nodes = append(nodes, graphNode{
			ID:          "conn:tailnet:" + tailnetID,
			DisplayName: displayName,
			Status:      status,
			NodeType:    "connection",
		})
	}

	// Add gateway node when a tailnet connection is active.
	// The gateway is the owner's remote access point — it joins the tailnet
	// and proxies to local Traefik so the owner can reach their Bloud instance
	// remotely. It is separate from sidecars, which handle app sharing.
	if s.gateway != nil && len(tailnetIDs) > 0 {
		gwStatus := "stopped"
		if s.gateway.IsRunning(r.Context()) {
			gwStatus = "running"
		}
		nodes = append(nodes, graphNode{
			ID:          "sys:gateway",
			DisplayName: "TS Gateway",
			Status:      gwStatus,
			IsSystem:    true,
			NodeType:    "app",
		})
		for tailnetID := range tailnetIDs {
			edges = append(edges, graphEdge{
				Source: "conn:tailnet:" + tailnetID,
				Target: "sys:gateway",
				Label:  "tailnet",
			})
		}
		if hasTraefik {
			edges = append(edges, graphEdge{
				Source: "sys:gateway",
				Target: "traefik",
				Label:  "proxy",
			})
		}
	}

	// Add local connection node (routes through traefik)
	if hasTraefik {
		nodes = append(nodes, graphNode{
			ID:          "conn:local",
			DisplayName: "", // frontend fills from window.location.hostname
			Status:      "active",
			NodeType:    "connection",
		})
		edges = append(edges, graphEdge{
			Source: "conn:local",
			Target: "traefik",
			Label:  "route",
		})
	}

	respondJSON(w, http.StatusOK, developerGraph{
		Nodes: nodes,
		Edges: edges,
	})
}
