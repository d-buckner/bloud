// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package api

import (
	"log/slog"
	"net/http"
	"sort"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/system"
	"github.com/go-chi/chi/v5"
)

// orchestratorStatusCaller is the minimal interface needed for the system
// module — it extends orchestratorCaller with a Status() method.
type orchestratorStatusCaller interface {
	Enqueue(intent orchestrator.Intent)
	Status() orchestrator.OrchestratorStatus
}

// SystemModule encapsulates system-level operations: health check, system
// status, storage stats, and the developer lifecycle graph.
type systemModule struct {
	appStore     store.AppStoreInterface
	catalog      catalog.CacheInterface
	graph        catalog.AppGraphInterface
	gateway      sharing.GatewayManagerInterface
	tailnetStore store.TailnetStoreInterface
	orch         orchestratorStatusCaller
	logger       *slog.Logger
}

// NewSystemModule creates a new SystemModule.
func NewSystemModule(
	appStore store.AppStoreInterface,
	catalog catalog.CacheInterface,
	graph catalog.AppGraphInterface,
	gateway sharing.GatewayManagerInterface,
	tailnetStore store.TailnetStoreInterface,
	orch orchestratorStatusCaller,
	logger *slog.Logger,
) *systemModule {
	return &systemModule{
		appStore:     appStore,
		catalog:      catalog,
		graph:        graph,
		gateway:      gateway,
		tailnetStore: tailnetStore,
		orch:         orch,
		logger:       logger,
	}
}

// ---- Health ----

// HealthHandler returns the basic health check.
func (m *systemModule) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ---- System Status ----

// SystemStatusHandler returns system stats (CPU, memory, disk).
func (m *systemModule) SystemStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats, err := system.GetStats()
		if err != nil {
			m.logger.Error("failed to get system stats", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to get system stats")
			return
		}
		respondJSON(w, http.StatusOK, stats)
	}
}

// ---- Storage ----

// StorageHandler returns storage statistics.
func (m *systemModule) StorageHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		storage, err := system.GetStorageStats()
		if err != nil {
			m.logger.Error("failed to get storage stats", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to get storage stats")
			return
		}
		respondJSON(w, http.StatusOK, storage)
	}
}

// ---- Types ----

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
	Nodes         []graphNode                    `json:"nodes"`
	Edges         []graphEdge                    `json:"edges"`
	TailnetDomain string                         `json:"tailnetDomain,omitempty"`
	Orchestrator  *orchestrator.OrchestratorStatus `json:"orchestrator,omitempty"`
}

// ssoEdgeLabel returns the SSO strategy (e.g. "forward-auth", "ldap") for an app,
// falling back to "sso" if the catalog entry or strategy is unavailable.
func (m *systemModule) ssoEdgeLabel(appName string) string {
	if m.catalog == nil {
		return "sso"
	}
	app, err := m.catalog.Get(appName)
	if err != nil || app.SSO.Strategy == "" {
		return "sso"
	}
	return app.SSO.Strategy
}

// buildGraphEdges derives the integration edges for a single app. It prefers
// the runtime integration config for each catalog-defined integration, falling
// back to the integration's default compatible app. When no catalog definition
// is available, it emits edges directly from the runtime integration config.
func (m *systemModule) buildGraphEdges(app *store.InstalledApp, def *catalog.AppDefinition) []graphEdge {
	targets := make(map[string]string)
	if def != nil {
		for label, integration := range def.Integrations {
			if target, chosen := app.IntegrationConfig[label]; chosen {
				targets[label] = target
				continue
			}
			for _, compat := range integration.Compatible {
				if compat.Default {
					targets[label] = compat.App
					break
				}
			}
		}
	} else {
		for label, target := range app.IntegrationConfig {
			targets[label] = target
		}
	}

	labels := make([]string, 0, len(targets))
	for label := range targets {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	edges := make([]graphEdge, 0, len(labels))
	for _, label := range labels {
		edgeLabel := label
		if label == "sso" {
			edgeLabel = m.ssoEdgeLabel(app.CatalogID)
		}
		edge := graphEdge{Source: app.CatalogID, Target: targets[label], Label: edgeLabel}
		if label == "proxy" {
			edge.Source, edge.Target = edge.Target, edge.Source
		}
		edges = append(edges, edge)
	}
	return edges
}

// DeveloperGraphHandler returns the lifecycle graph for the developer dashboard.
func (m *systemModule) DeveloperGraphHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apps, err := m.appStore.GetAll()
		if err != nil {
			m.logger.Error("failed to get apps for developer graph", "error", err)
			respondError(w, http.StatusInternalServerError, "failed to get apps")
			return
		}

		// Build catalog integration lookup from the app graph
		var graphDefs map[string]*catalog.AppDefinition
		if m.graph != nil {
			graphDefs = m.graph.GetApps()
		}

		nodes := make([]graphNode, 0, len(apps))
		edges := make([]graphEdge, 0)

		// Track unique tailnet IDs to create connection nodes
		tailnetIDs := make(map[string]bool)
		hasTraefik := false

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

			if app.TailnetID != "" {
				tailnetIDs[app.TailnetID] = true
				tailnetNodeApps = append(tailnetNodeApps, tailnetNodeInfo{
					appName:     app.CatalogID,
					displayName: app.DisplayName,
					tailnetID:   app.TailnetID,
					status:      app.Status,
				})
			}

			var def *catalog.AppDefinition
			if graphDefs != nil {
				def = graphDefs[app.CatalogID]
			}
			edges = append(edges, m.buildGraphEdges(app, def)...)
		}

		// Add tailnet node containers
		for _, tn := range tailnetNodeApps {
			tsNodeID := "ts:" + tn.appName
			nodes = append(nodes, graphNode{
				ID:          tsNodeID,
				DisplayName: tn.displayName + " Tunnel",
				Status:      tn.status,
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

		// Discover tailnet domain from the gateway
		var tailnetDomain string
		if m.gateway != nil && len(tailnetIDs) > 0 {
			if domain, err := m.gateway.GetTailnetDomain(r.Context()); err == nil {
				tailnetDomain = domain
			}
		}

		// Add tailnet connection nodes
		for tailnetID := range tailnetIDs {
			displayName := "Tailnet"
			status := "unknown"
			if conn, err := m.tailnetStore.GetByID(tailnetID); err == nil && conn != nil {
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

		// Add gateway node
		if m.gateway != nil && len(tailnetIDs) > 0 {
			gwStatus := "stopped"
			if m.gateway.IsRunning(r.Context()) {
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

		// Add local connection node
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
		if m.orch != nil {
			status := m.orch.Status()
			orchStatus = &status
		}

		respondJSON(w, http.StatusOK, developerGraph{
			Nodes:         nodes,
			Edges:         edges,
			TailnetDomain: tailnetDomain,
			Orchestrator:  orchStatus,
		})
	}
}

// ---- Router ----

// NewSystemRouter registers all system-related routes on the given router.
func NewSystemRouter(mod *systemModule, r chi.Router) {
	r.Get("/health", mod.HealthHandler())
	r.Get("/system/status", mod.SystemStatusHandler())
	r.Get("/system/storage", mod.StorageHandler())
	r.Get("/system/developer", mod.DeveloperGraphHandler())
}
