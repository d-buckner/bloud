package api

import (
	"net/http"
	"sort"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
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
	Nodes         []graphNode                   `json:"nodes"`
	Edges         []graphEdge                   `json:"edges"`
	TailnetDomain string                        `json:"tailnetDomain,omitempty"`
	Orchestrator  *orchestrator.OrchestratorStatus `json:"orchestrator,omitempty"`
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

	// Track apps with tailnet nodes for post-loop edge generation
	type tailnetNodeInfo struct {
		appName     string
		displayName string
		tailnetID   string
		status      string
	}
	var tailnetNodeApps []tailnetNodeInfo

	for _, app := range apps {
		node := graphNode{
			ID:          app.CatalogID,
			DisplayName: app.DisplayName,
			Status:      app.Status,
			IsSystem:    app.IsSystem,
			NodeType:    "app",
		}
		nodes = append(nodes, node)

		if app.CatalogID == "traefik" {
			hasTraefik = true
		}

		// Track tailnet connection and tailnet node for this app.
		// Edges are generated after the loop so we know whether Traefik exists.
		if app.TailnetID != "" {
			tailnetIDs[app.TailnetID] = true
			tailnetNodeApps = append(tailnetNodeApps, tailnetNodeInfo{
				appName:     app.CatalogID,
				displayName: app.DisplayName,
				tailnetID:   app.TailnetID,
				status:      app.Status,
			})
		}

		// Derive edges: use runtime IntegrationConfig first, fall back to catalog defaults.
		// Sort integration labels for deterministic edge ordering.
		if def, ok := graphDefs[app.CatalogID]; ok {
			labels := make([]string, 0, len(def.Integrations))
			for label := range def.Integrations {
				labels = append(labels, label)
			}
			sort.Strings(labels)

			for _, label := range labels {
				integration := def.Integrations[label]
				edgeLabel := label
				if label == "sso" {
					edgeLabel = s.ssoEdgeLabel(app.CatalogID)
				}

				// Check if user made a runtime choice
				if target, chosen := app.IntegrationConfig[label]; chosen {
					edge := graphEdge{Source: app.CatalogID, Target: target, Label: edgeLabel}
					if label == "proxy" {
						edge.Source, edge.Target = edge.Target, edge.Source
					}
					edges = append(edges, edge)
					continue
				}
				// Fall back to default compatible app
				for _, compat := range integration.Compatible {
					if compat.Default {
						edge := graphEdge{Source: app.CatalogID, Target: compat.App, Label: edgeLabel}
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
					edgeLabel = s.ssoEdgeLabel(app.CatalogID)
				}
				edge := graphEdge{Source: app.CatalogID, Target: app.IntegrationConfig[label], Label: edgeLabel}
				if label == "proxy" {
					edge.Source, edge.Target = edge.Target, edge.Source
				}
				edges = append(edges, edge)
			}
		}
	}

	// Add tailnet node containers (ts-{appName}) that sit between the tailnet
	// connection and Traefik. Each shared app gets its own Tailscale container
	// that joins the tailnet and proxies to Traefik via TS_SERVE_CONFIG.
	for _, tn := range tailnetNodeApps {
		tsNodeID := "ts:" + tn.appName
		nodes = append(nodes, graphNode{
			ID:          tsNodeID,
			DisplayName: tn.displayName + " Tunnel",
			Status:      tn.status, // mirrors the app — systemd lifecycle is coupled
			IsSystem:    true,
			NodeType:    "app",
		})
		edges = append(edges, graphEdge{
			Source: "conn:tailnet:" + tn.tailnetID,
			Target: tsNodeID,
			Label:  "tailnet",
		})
		if hasTraefik {
			edges = append(edges, graphEdge{
				Source: tsNodeID,
				Target: "traefik",
				Label:  "route",
			})
		}
	}

	// Discover tailnet domain from the gateway (best-effort).
	var tailnetDomain string
	if s.gateway != nil && len(tailnetIDs) > 0 {
		if domain, err := s.gateway.GetTailnetDomain(r.Context()); err == nil {
			tailnetDomain = domain
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
		if tailnetDomain != "" {
			displayName = "bloud." + tailnetDomain
		}
		nodes = append(nodes, graphNode{
			ID:          "conn:tailnet:" + tailnetID,
			DisplayName: displayName,
			Status:      status,
			NodeType:    "connection",
		})
	}

	// Add gateway node when a tailnet connection is active.
	// The gateway joins the tailnet and exposes a SOCKS5 proxy so that remote
	// apps (shared from other hosts) can be proxied through Traefik to the LAN.
	// It is separate from tailnet nodes, which handle outbound app sharing.
	if s.gateway != nil && len(tailnetIDs) > 0 {
		gwStatus := "stopped"
		if s.gateway.IsRunning(r.Context()) {
			gwStatus = "running"
		}
		nodes = append(nodes, graphNode{
			ID:          "sys:gateway",
			DisplayName: "Tailnet Gateway",
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
			DisplayName: "LAN",
			Status:      "active",
			NodeType:    "connection",
		})
		edges = append(edges, graphEdge{
			Source: "conn:local",
			Target: "traefik",
			Label:  "route",
		})
	}

	var orchStatus *orchestrator.OrchestratorStatus
	if s.orch != nil {
		status := s.orch.Status()
		orchStatus = &status
	}

	resp := developerGraph{
		Nodes:         nodes,
		Edges:         edges,
		TailnetDomain: tailnetDomain,
		Orchestrator:  orchStatus,
	}

	respondJSON(w, http.StatusOK, resp)
}
