// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/system"
	"github.com/go-chi/chi/v5"
)

// orchestratorStatusCaller is the minimal interface needed for the system
// module: it extends orchestratorCaller with a Status() method and the
// per-node lifecycle phases the developer graph renders.
type orchestratorStatusCaller interface {
	Enqueue(intent orchestrator.Intent)
	Status() orchestrator.OrchestratorStatus
	NodePhases() map[string]string
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
	// healthCheck reports whether the system is actually working (database
	// reachable, orchestrator intent loop alive). Wired by the router; nil
	// leaves the endpoint as a liveness echo.
	healthCheck func() error
	logger      *slog.Logger
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

// SetHealthCheck wires the system health check behind the health endpoint, so
// the HTTP answer and Server.CheckSystemHealth come from one implementation.
func (m *systemModule) SetHealthCheck(check func() error) {
	m.healthCheck = check
}

// HealthHandler answers the health probe. 200 means the instance is up and
// reconciling. 503 with status "unhealthy" means the process answers but
// something it depends on is broken; the reason goes to the log, not the wire,
// because this endpoint is public and a driver error can name a path.
//
// That body is also what separates this 503 from the bootstrap gate's
// 503 {"error":"starting"}: "starting" means still coming up, "unhealthy"
// means up and broken. The bootstrap page keys off the difference.
func (m *systemModule) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.healthCheck == nil {
			respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		if err := m.healthCheck(); err != nil {
			m.logger.Warn("health check failed", "error", err)
			respondJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
			return
		}
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
	NodeType    string `json:"nodeType"` // "app", "container", or "connection"
	// ParentID is the owning app's node ID for container nodes; the dashboard
	// draws those inside the app's box.
	ParentID string `json:"parentId,omitempty"`
}

type graphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Label  string `json:"label"`
}

type developerGraph struct {
	Nodes         []graphNode                      `json:"nodes"`
	Edges         []graphEdge                      `json:"edges"`
	TailnetDomain string                           `json:"tailnetDomain,omitempty"`
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

		var graphDefs map[string]*catalog.AppDefinition
		if m.graph != nil {
			graphDefs = m.graph.GetApps()
		}

		respondJSON(w, http.StatusOK, m.buildDeveloperGraph(r.Context(), apps, graphDefs))
	}
}

// tailnetNodeInfo carries the per-app fields needed to render a tunnel node.
type tailnetNodeInfo struct {
	appName     string
	displayName string
	tailnetID   string
	status      string
}

// buildDeveloperGraph assembles the developer dashboard graph from the
// installed apps, their catalog definitions, and the live gateway state.
func (m *systemModule) buildDeveloperGraph(
	ctx context.Context,
	apps []*store.InstalledApp,
	graphDefs map[string]*catalog.AppDefinition,
) developerGraph {
	nodes, edges, tailnetApps, tailnetIDs, hasTraefik := m.appNodes(apps, graphDefs)
	domain := m.resolveTailnetDomain(ctx, tailnetIDs)

	tunnelNodes, tunnelEdges := m.tunnelNodes(tailnetApps, tailnetIDs, domain, hasTraefik)
	nodes = append(nodes, tunnelNodes...)
	edges = append(edges, tunnelEdges...)

	gwNodes, gwEdges := m.gatewayAndLocalNodes(ctx, tailnetIDs, hasTraefik)
	nodes = append(nodes, gwNodes...)
	edges = append(edges, gwEdges...)

	var orchStatus *orchestrator.OrchestratorStatus
	if m.orch != nil {
		status := m.orch.Status()
		orchStatus = &status
	}

	return developerGraph{
		Nodes:         nodes,
		Edges:         edges,
		TailnetDomain: domain,
		Orchestrator:  orchStatus,
	}
}

// appNodes builds one node per installed app, plus one child node per
// container that app declares, and collects the tailnet/apps bookkeeping
// (unique tailnet IDs, the tunnel-node list, traefik presence) plus each
// app's integration edges.
func (m *systemModule) appNodes(
	apps []*store.InstalledApp,
	graphDefs map[string]*catalog.AppDefinition,
) ([]graphNode, []graphEdge, []tailnetNodeInfo, map[string]bool, bool) {
	nodes := make([]graphNode, 0, len(apps))
	edges := make([]graphEdge, 0)
	tailnetIDs := make(map[string]bool)
	hasTraefik := false
	var tailnetApps []tailnetNodeInfo
	phases := m.nodePhases()

	for _, app := range apps {
		nodes = append(nodes, graphNode{
			ID:          app.CatalogID,
			DisplayName: app.DisplayName,
			Status:      app.Status,
			IsSystem:    app.IsSystem,
			NodeType:    "app",
		})

		containerNodes, containerEdges := m.containerNodes(app, phases)
		nodes = append(nodes, containerNodes...)
		edges = append(edges, containerEdges...)

		if app.CatalogID == "traefik" {
			hasTraefik = true
		}

		if app.TailnetID != "" {
			tailnetIDs[app.TailnetID] = true
			tailnetApps = append(tailnetApps, tailnetNodeInfo{
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

	return nodes, edges, tailnetApps, tailnetIDs, hasTraefik
}

// nodePhases returns the live lifecycle phase of every graph node, keyed by
// node ID. Nil when no orchestrator is wired (containers then fall back to
// their app's stored status).
func (m *systemModule) nodePhases() map[string]string {
	if m.orch == nil {
		return nil
	}
	return m.orch.NodePhases()
}

// containerNodes builds the container child nodes for an installed app plus
// the within-app dependsOn edges between them. The app's catalog entry is the
// source; an app that declares no containers (legacy entry, or a cache miss)
// is represented by a single node keyed by the catalog ID, mirroring how the
// lifecycle graph names it.
func (m *systemModule) containerNodes(app *store.InstalledApp, phases map[string]string) ([]graphNode, []graphEdge) {
	defs := m.containerDefs(app.CatalogID)
	if len(defs) == 0 {
		return []graphNode{{
			ID:          app.CatalogID,
			DisplayName: app.DisplayName,
			Status:      app.Status,
			IsSystem:    app.IsSystem,
			NodeType:    "container",
			ParentID:    app.CatalogID,
		}}, nil
	}

	nodes := make([]graphNode, 0, len(defs))
	edges := make([]graphEdge, 0)
	for _, def := range defs {
		status := phases[def.Name]
		if status == "" {
			status = app.Status
		}
		nodes = append(nodes, graphNode{
			ID:          def.Name,
			DisplayName: containerLabel(def.Name, app.CatalogID),
			Status:      status,
			IsSystem:    app.IsSystem,
			NodeType:    "container",
			ParentID:    app.CatalogID,
		})
		for _, dep := range def.DependsOn {
			// Unlabeled: the box scopes the edge to one app, and every
			// within-app edge is a dependsOn (dependency drawn below).
			edges = append(edges, graphEdge{Source: def.Name, Target: dep})
		}
	}
	return nodes, edges
}

// containerDefs returns the container definitions an app declares in the
// catalog, or nil when the cache has no entry for it.
func (m *systemModule) containerDefs(appName string) []catalog.ContainerDef {
	if m.catalog == nil {
		return nil
	}
	app, err := m.catalog.Get(appName)
	if err != nil || app == nil {
		return nil
	}
	return app.ContainerDefs()
}

// containerLabel shortens a runtime container name ("apps-immich-postgres")
// to the part naming the component ("postgres"), falling back to the app name
// for the app's own single container ("apps-traefik").
func containerLabel(containerName, appID string) string {
	short := strings.TrimPrefix(strings.TrimPrefix(containerName, "apps-"+appID), "-")
	short = strings.TrimPrefix(short, "apps-")
	if short == "" {
		return appID
	}
	return short
}

// resolveTailnetDomain asks the gateway for the active tailnet domain, if
// any tailnet-connected apps exist.
func (m *systemModule) resolveTailnetDomain(ctx context.Context, tailnetIDs map[string]bool) string {
	if m.gateway == nil || len(tailnetIDs) == 0 {
		return ""
	}
	domain, err := m.gateway.GetTailnetDomain(ctx)
	if err != nil {
		return ""
	}
	return domain
}

// tunnelNodes builds the per-app tunnel nodes and their tailnet connection
// nodes (and the tunnel→traefik route edges when traefik is present).
func (m *systemModule) tunnelNodes(
	tailnetApps []tailnetNodeInfo,
	tailnetIDs map[string]bool,
	tailnetDomain string,
	hasTraefik bool,
) ([]graphNode, []graphEdge) {
	nodes := make([]graphNode, 0)
	edges := make([]graphEdge, 0)

	for _, tn := range tailnetApps {
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

	return nodes, edges
}

// gatewayAndLocalNodes builds the tailnet gateway node (with its connection
// edges) and the local LAN connection node.
func (m *systemModule) gatewayAndLocalNodes(
	ctx context.Context,
	tailnetIDs map[string]bool,
	hasTraefik bool,
) ([]graphNode, []graphEdge) {
	nodes := make([]graphNode, 0)
	edges := make([]graphEdge, 0)

	if m.gateway != nil && len(tailnetIDs) > 0 {
		gwStatus := "stopped"
		if m.gateway.IsRunning(ctx) {
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

	return nodes, edges
}

// ---- Router ----

// NewSystemRouter registers all system-related routes on the given router.
func NewSystemRouter(mod *systemModule, r chi.Router) {
	r.Get("/health", mod.HealthHandler())
	r.Get("/system/status", mod.SystemStatusHandler())
	r.Get("/system/storage", mod.StorageHandler())
	r.Get("/system/developer", mod.DeveloperGraphHandler())
}
