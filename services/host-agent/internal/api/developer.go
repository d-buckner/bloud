package api

import (
	"net/http"
	"sort"
	"strings"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
)

type graphNode struct {
	ID          string       `json:"id"`
	DisplayName string       `json:"displayName"`
	Status      string       `json:"status"`
	IsSystem    bool         `json:"isSystem"`
	Sidecar     *sidecarInfo `json:"sidecar"`
}

type sidecarInfo struct {
	State  string `json:"state"`
	Status string `json:"status"`
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

func (s *Server) handleDeveloperGraph(w http.ResponseWriter, r *http.Request) {
	apps, err := s.appStore.GetAll()
	if err != nil {
		s.logger.Error("failed to get apps for developer graph", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to get apps")
		return
	}

	// Build sidecar lookup from podman containers: name prefix "ts-" → sidecar info
	sidecarMap := make(map[string]*sidecarInfo)
	if s.podmanClient != nil {
		containers, err := s.podmanClient.ListContainers(r.Context())
		if err != nil {
			s.logger.Warn("failed to list containers for sidecar status", "error", err)
		} else {
			for _, c := range containers {
				for _, name := range c.Names {
					if strings.HasPrefix(name, "ts-") {
						appName := strings.TrimPrefix(name, "ts-")
						sidecarMap[appName] = &sidecarInfo{
							State:  c.State,
							Status: c.Status,
						}
					}
				}
			}
		}
	}

	// Build catalog integration lookup from the app graph
	var graphDefs map[string]*catalog.AppDefinition
	if s.graph != nil {
		graphDefs = s.graph.GetApps()
	}

	nodes := make([]graphNode, 0, len(apps))
	edges := make([]graphEdge, 0)

	for _, app := range apps {
		node := graphNode{
			ID:          app.Name,
			DisplayName: app.DisplayName,
			Status:      app.Status,
			IsSystem:    app.IsSystem,
			Sidecar:     sidecarMap[app.Name],
		}
		nodes = append(nodes, node)

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
				// Check if user made a runtime choice
				if target, chosen := app.IntegrationConfig[label]; chosen {
					edges = append(edges, graphEdge{
						Source: app.Name,
						Target: target,
						Label:  label,
					})
					continue
				}
				// Fall back to default compatible app
				for _, compat := range integration.Compatible {
					if compat.Default {
						edges = append(edges, graphEdge{
							Source: app.Name,
							Target: compat.App,
							Label:  label,
						})
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
				edges = append(edges, graphEdge{
					Source: app.Name,
					Target: app.IntegrationConfig[label],
					Label:  label,
				})
			}
		}
	}

	respondJSON(w, http.StatusOK, developerGraph{
		Nodes: nodes,
		Edges: edges,
	})
}
