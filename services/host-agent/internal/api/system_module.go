// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
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
	appStore store.AppStoreInterface
	// catalog is the disk-driven catalog cache, and the source the developer
	// graph draws its integration edges from: it is the live view of
	// apps/*/metadata.yaml, refreshed by POST /api/apps/refresh-catalog, so a
	// display never needs a second copy of the declarations kept in sync by
	// hand. It is also what ssoEdgeLabel reads.
	catalog catalog.CacheInterface
	orch    orchestratorStatusCaller
	// healthCheck reports whether the system is actually working (database
	// reachable, orchestrator intent loop alive). Wired by the router; nil
	// leaves the endpoint as a liveness echo.
	healthCheck func() error
	// aiSettings is the store behind Settings -> AI. The developer graph
	// reads it to decide whether the instance provides an AI model at all:
	// with no enabled upstream there is nothing to wire, so the AI Model node
	// stays out of the picture instead of showing a provider that answers
	// nothing. Wired by the router; nil means never shown.
	aiSettings store.SettingsStoreInterface
	// dnsDiagnostics answers /system/diagnostics. Wired by the router; nil
	// omits the endpoint.
	dnsDiagnostics *system.DNSDiagnostics
	logger         *slog.Logger
}

func NewSystemModule(
	appStore store.AppStoreInterface,
	catalog catalog.CacheInterface,
	orch orchestratorStatusCaller,
	logger *slog.Logger,
) *systemModule {
	return &systemModule{
		appStore: appStore,
		catalog:  catalog,
		orch:     orch,
		logger:   logger,
	}
}

// ---- Health ----

// SetHealthCheck wires the system health check behind the health endpoint, so
// the HTTP answer and Server.CheckSystemHealth come from one implementation.
func (m *systemModule) SetHealthCheck(check func() error) {
	m.healthCheck = check
}

// SetAISettings wires the settings store behind Settings -> AI, which is what
// the developer graph reads to decide whether the instance provides an AI
// model. Unwired, the graph never shows the AI Model node.
func (m *systemModule) SetAISettings(settingsStore store.SettingsStoreInterface) {
	m.aiSettings = settingsStore
}

// SetDNSDiagnostics wires the container DNS diagnostic behind
// /system/diagnostics. Unwired, the endpoint answers 503.
func (m *systemModule) SetDNSDiagnostics(d *system.DNSDiagnostics) {
	m.dnsDiagnostics = d
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

// DiagnosticsHandler answers the container DNS diagnostic: whether the
// configured public host resolves inside a managed container the way it does
// on the host.
func (m *systemModule) DiagnosticsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m.dnsDiagnostics == nil {
			respondError(w, http.StatusServiceUnavailable, "diagnostics unavailable")
			return
		}
		respondJSON(w, http.StatusOK, m.dnsDiagnostics.Check(r.Context()))
	}
}

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

// SystemStatusStreamHandler streams system stats via SSE.
func (m *systemModule) SystemStatusStreamHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		ctx := r.Context()
		m.logger.Info("SSE client connected for system stats")

		for {
			select {
			case <-ctx.Done():
				m.logger.Info("SSE client disconnected")
				return
			case <-ticker.C:
				stats, err := system.GetStats()
				if err != nil {
					m.logger.Error("failed to get system stats for SSE", "error", err)
					continue
				}
				data, err := json.Marshal(stats)
				if err != nil {
					m.logger.Error("failed to marshal stats for SSE", "error", err)
					continue
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
					// Client disconnected mid-stream.
					return
				}
				flusher.Flush()
			}
		}
	}
}

// ---- Storage ----

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

// The AI Model node: the instance's own Settings -> AI endpoint, drawn as a
// provider so an inference consumer's edge has somewhere to land. It is not
// an app. Nothing installs it, no container backs it, and the orchestrator
// never gains a lifecycle node for it; this is a display of a wiring that is
// real but has no installed shape.
//
// The live graph shows it only while an upstream is enabled. The catalog
// snapshot the README image renders from always shows it, because that
// picture is the full view of what Bloud can wire rather than of what this
// instance happens to have configured.
const (
	AINodeID    = "ai:instance"
	AINodeLabel = "AI Model"
	// AINodeStatus is what the node reads as. "external" rather than a
	// lifecycle word: the instance never health-checks someone else's server,
	// so "running" would claim a liveness nobody verified, and it is not a
	// Bloud-run workload either. It is deliberately absent from the status
	// color table so the dot falls back to the same neutral gray every other
	// unprobed status gets.
	AINodeStatus = "external"
)

type graphNode struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
	IsSystem    bool   `json:"isSystem"`
	NodeType    string `json:"nodeType"` // "app", "container", "connection", or "service"
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
	Nodes        []graphNode                      `json:"nodes"`
	Edges        []graphEdge                      `json:"edges"`
	Orchestrator *orchestrator.OrchestratorStatus `json:"orchestrator,omitempty"`
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

// integrationTarget is one (contract, provider node) pair an app is wired to.
type integrationTarget struct {
	label string
	node  string
}

// buildGraphEdges derives the integration edges for a single app, keeping only
// those whose provider is actually a node in the payload.
//
// The catalog declaration drives it, through the same set rule the
// orchestrator uses to bind providers (see integrationTargets): every
// declared compatible provider. No provider is ever "chosen"; whether an edge
// draws is whether the provider is installed, which is exactly what `present`
// answers below.
//
// The instance's own AI provider reaches the graph without a `default: true`
// anywhere: an app that declares `source: setting` declares a wiring that
// exists the moment the setting is populated, and a display keyed on the
// default flag would hide it.
//
// Every candidate then passes through `present`, so an edge only ever names a
// node the browser was also given.
func (m *systemModule) buildGraphEdges(app *store.InstalledApp, present map[string]bool) []graphEdge {
	targets := m.integrationTargets(app)

	edges := make([]graphEdge, 0, len(targets))
	for _, target := range targets {
		if !present[target.node] {
			continue
		}
		edgeLabel := target.label
		if target.label == "sso" {
			edgeLabel = m.ssoEdgeLabel(app.CatalogID)
		}
		edge := graphEdge{Source: app.CatalogID, Target: target.node, Label: edgeLabel}
		if target.label == "proxy" {
			edge.Source, edge.Target = edge.Target, edge.Source
		}
		edges = append(edges, edge)
	}
	return edges
}

// integrationTargets lists the provider nodes each of the app's declared
// integrations resolves to, deduplicated, in a deterministic order.
//
// The rule is the orchestrator's, read from catalog.DeclaredProviders rather
// than restated here: every declared compatible provider. No provider is ever
// chosen. Whether an edge actually draws is decided later, in buildGraphEdges,
// against the set of nodes the payload contains, which is the installed set
// plus the AI Model node when the instance provides one.
//
// This is what #233 was about: the old code treated the value recorded in
// IntegrationConfig as the chosen provider for every contract, so Hermes
// recorded `mcp: affine-mcp` when that was the only MCP provider and the
// graph kept drawing one edge after `dav-mcp` was installed and wired.
// The recorded value is now write-only provenance, and nothing here reads it.
//
// An app the catalog does not describe falls back to what its install
// recorded, which is the only wiring the display can name for an app whose
// declaration is gone.
func (m *systemModule) integrationTargets(app *store.InstalledApp) []integrationTarget {
	var targets []integrationTarget
	seen := make(map[string]bool)
	add := func(label, node string) {
		key := label + "\x00" + node
		if seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, integrationTarget{label: label, node: node})
	}

	def := m.catalogDefinition(app.CatalogID)
	if def == nil {
		for label, node := range app.IntegrationConfig {
			add(label, node)
		}
		return targets
	}

	labels := make([]string, 0, len(def.Integrations))
	for label := range def.Integrations {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	for _, label := range labels {
		for _, provider := range catalog.DeclaredProviders(def.Integrations[label]) {
			add(label, providerNodeID(provider))
		}
	}
	return targets
}

// catalogDefinition returns the catalog entry for an installed app, or nil when
// no cache is wired or the catalog does not list it. A missing entry is not an
// error here: the display falls back to the recorded config rather than
// inventing declarations for an app the catalog does not know.
func (m *systemModule) catalogDefinition(catalogID string) *catalog.App {
	if m.catalog == nil {
		return nil
	}
	def, err := m.catalog.Get(catalogID)
	if err != nil {
		return nil
	}
	return def
}

// providerNodes is the set of node IDs an integration edge may point at:
// every installed app, plus the AI Model node while the instance provides one.
//
// The display filters on this rather than trusting the catalog's compatible
// list outright because an edge naming a node that is not in the payload still
// reaches the browser, which draws an arrow into empty space. A compatible
// entry whose provider is not installed is a possibility the metadata allows,
// not a wiring that exists.
func providerNodes(apps []*store.InstalledApp, aiShown bool) map[string]bool {
	present := make(map[string]bool, len(apps)+1)
	for _, app := range apps {
		present[app.CatalogID] = true
	}
	if aiShown {
		present[AINodeID] = true
	}
	return present
}

// providerNodeID maps one provider to the graph node that stands for it. A
// catalog app is its own node; the instance provider is not an app, so it
// maps to the AI Model node the instance provides.
func providerNodeID(provider catalog.BoundProvider) string {
	if provider.Source != "" {
		return AINodeID
	}
	return provider.App
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

		respondJSON(w, http.StatusOK, m.buildDeveloperGraph(apps))
	}
}

// buildDeveloperGraph assembles the developer dashboard graph from the
// installed apps, their catalog definitions, and the live LAN connection
// node.
func (m *systemModule) buildDeveloperGraph(
	apps []*store.InstalledApp,
) developerGraph {
	// Whether the instance provides an AI model is resolved before the edges,
	// not after: it decides whether an inference edge has anywhere to point.
	aiShown := m.aiConfigured()

	nodes, edges, hasTraefik := m.appNodes(apps, providerNodes(apps, aiShown))
	if hasTraefik {
		nodes = append(nodes, graphNode{
			ID:          "conn:local",
			DisplayName: "LAN",
			Status:      "active",
			NodeType:    "connection",
		})
		edges = append(edges, graphEdge{Source: "conn:local", Target: "traefik", Label: "route"})
	}

	nodes = append(nodes, aiNode(aiShown)...)

	var orchStatus *orchestrator.OrchestratorStatus
	if m.orch != nil {
		status := m.orch.Status()
		orchStatus = &status
	}

	return developerGraph{
		Nodes:        nodes,
		Edges:        edges,
		Orchestrator: orchStatus,
	}
}

// aiNode returns the AI Model node when the instance provides one, and nothing
// otherwise.
//
// The node is the display's stand-in for a provider with no installed shape:
// nothing installs it, no container backs it, and the orchestrator never gains
// a lifecycle node for it. Whether it is in the payload is also what gates the
// edges that point at it (see providerNodes), so no edge can ever name a node
// the browser was not given.
func aiNode(shown bool) []graphNode {
	if !shown {
		return nil
	}
	return []graphNode{{
		ID:          AINodeID,
		DisplayName: AINodeLabel,
		Status:      AINodeStatus,
		// Not flagged system: that would add a "system" chip on top of
		// "external" and a dashed border, and the point of this node is that
		// it reads as one plain thing the instance points at.
		IsSystem: false,
		NodeType: "service",
	}}
}

// aiConfigured reports whether Settings -> AI has an enabled upstream. A
// store that fails to answer reads as unconfigured: the graph is a display,
// and a settings read error is not a reason to invent a provider.
func (m *systemModule) aiConfigured() bool {
	if m.aiSettings == nil {
		return false
	}
	upstreamsJSON, err := m.aiSettings.Get(inference.SettingUpstreams)
	if err != nil {
		return false
	}
	settings, err := inference.DecodeSettings(upstreamsJSON, "")
	if err != nil {
		return false
	}
	_, ok := settings.ActiveUpstream()
	return ok
}

// appNodes builds one node per installed app, plus one child node per
// container that app declares, and reports whether traefik is among them
// (the LAN connection node only means something with a proxy to attach to)
// alongside each app's integration edges.
func (m *systemModule) appNodes(
	apps []*store.InstalledApp,
	present map[string]bool,
) ([]graphNode, []graphEdge, bool) {
	nodes := make([]graphNode, 0, len(apps))
	edges := make([]graphEdge, 0)
	hasTraefik := false
	phases := m.nodePhases()

	for _, app := range apps {
		nodes = append(nodes, graphNode{
			ID:          app.CatalogID,
			DisplayName: app.DisplayName,
			Status:      string(app.Status),
			IsSystem:    app.IsSystem,
			NodeType:    "app",
		})

		containerNodes, containerEdges := m.containerNodes(app, phases)
		nodes = append(nodes, containerNodes...)
		edges = append(edges, containerEdges...)

		if app.CatalogID == "traefik" {
			hasTraefik = true
		}

		edges = append(edges, m.buildGraphEdges(app, present)...)
	}

	return nodes, edges, hasTraefik
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
			Status:      string(app.Status),
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
			status = string(app.Status)
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

// ---- Router ----

// NewSystemRouter registers all system-related routes on the given router.
// /system/diagnostics is registered on the authenticated router, not here (see
// registerRoutes): the other system routes are public info, but resolver
// upstreams are operator-only.
func NewSystemRouter(mod *systemModule, r chi.Router) {
	r.Get("/health", mod.HealthHandler())
	r.Get("/system/status", mod.SystemStatusHandler())
	r.Get("/system/storage", mod.StorageHandler())
	r.Get("/system/developer", mod.DeveloperGraphHandler())
}
