// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"sort"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/inference"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// orchestratorStatusCaller is the minimal interface needed for the system
// module: it extends orchestratorCaller with a Status() method and the
// per-node lifecycle state the developer graph renders.
type orchestratorStatusCaller interface {
	Enqueue(intent orchestrator.Intent)
	Status() orchestrator.OrchestratorStatus
	NodeStates() map[string]orchestrator.NodeState
}

// entryPointLabel is the address the instance is reached through, as the
// developer graph names its ingress node: the configured public host, with
// the port only when it is not the scheme default.
//
// The stored address is the answer rather than the Host header of the request
// that asked for the graph. The graph describes the deployment, and the same
// deployment is read from the machine it runs on, from another room, and from
// the CLI against the loopback API, where the header would say `localhost:3000`
// and describe nothing.
func (m *systemModule) entryPointLabel() string {
	if m.hostState == nil {
		return hostset.Default().Public().HostPort()
	}
	return m.hostState.Get().Public().HostPort()
}

// ---- Types ----

// External provider nodes.
//
// An external provider is a wiring that is real but has no installed shape:
// nothing installs it, no container backs it, and the orchestrator never gains
// a lifecycle node for it. It renders in the same row as the old "AI Model"
// node, which was the single special case this generalizes.
//
// The node ID is keyed on the record's own ID rather than on its contract, so
// two upstreams or two off-host PVRs draw as the two separate things they are
// instead of collapsing into one box that cannot say which it means.
func externalNodeID(recordID string) string {
	return "external:" + recordID
}

// externalNodeStatus is what an external node reads as. "external" rather
// than a lifecycle word: the instance never health-checks someone else's
// server, so "running" would claim a liveness nobody verified. It is
// deliberately absent from the status color table so the dot falls back to the
// same neutral gray every other unprobed status gets.
const externalNodeStatus = "external"

// externalIndex is the provider registry read once per graph build, indexed
// the three ways the display needs it.
//
// nodes is one per provider record. byContract maps a contract name to the
// nodes that fill it, which is what a consumer's `source: setting` edge
// resolves to. byApp maps a catalog ID to the node standing in for a remote
// install of it, which is what an `app:` edge resolves to when the app is not
// installed locally.
type externalIndex struct {
	nodes      []graphNode
	byContract map[string][]string
	byApp      map[string]string
	// inference indexes positions in nodes: the entries that fill the
	// instance's own AI contract, which are labelled by what they provide
	// rather than by the name typed into the Settings form.
	inference []int
}

func (e externalIndex) ids() map[string]bool {
	out := make(map[string]bool, len(e.nodes))
	for _, n := range e.nodes {
		out[n.ID] = true
	}
	return out
}

// externalRecordActive reports whether a provider record represents wiring the
// display should show as live.
//
// Only the inference contract has an on/off switch today: the AI Settings page
// keeps a disabled upstream so the toggle is reversible, and the resolver will
// not hand it to anyone. Drawing an edge to a provider nothing dials would be
// the graph claiming a wiring that does not exist, so a disabled upstream gets
// no node. Every other contract has no such flag, and a record of one is live
// by being there.
func externalRecordActive(rec *store.ExternalApp) bool {
	kind, ref, ok := store.ParseExternalAppSource(rec.Source)
	if !ok || kind != store.ExternalAppSourceKindContract || ref != inference.ContractName {
		return true
	}
	return rec.Value(inference.ContractName, inference.ValueEnabled) == "true"
}

func (m *systemModule) buildExternalIndex() externalIndex {
	idx := externalIndex{
		byContract: map[string][]string{},
		byApp:      map[string]string{},
	}
	if m.externalApps == nil {
		return idx
	}
	records, err := m.externalApps.GetAll()
	if err != nil {
		m.logger.Warn("cannot read the external provider registry; the graph omits external providers",
			"error", err)
		return idx
	}
	for _, rec := range records {
		if rec.Kind != string(store.ExternalAppKindProvider) || !externalRecordActive(rec) {
			continue
		}
		idx.nodes = append(idx.nodes, graphNode{
			ID:          externalNodeID(rec.ID),
			DisplayName: rec.Name,
			Status:      externalNodeStatus,
			NodeType:    "service",
		})
		kind, ref, ok := store.ParseExternalAppSource(rec.Source)
		if !ok {
			continue
		}
		node := externalNodeID(rec.ID)
		switch kind {
		case store.ExternalAppSourceKindContract:
			idx.byContract[ref] = append(idx.byContract[ref], node)
			if ref == inference.ContractName {
				idx.inference = append(idx.inference, len(idx.nodes)-1)
			}
		case store.ExternalAppSourceKindApp:
			idx.byApp[ref] = node
		}
	}
	idx.labelInferenceProviders()
	return idx
}

// inferenceLabel is what the graph calls the instance's own AI upstream. The
// record behind it is named whatever was typed into Settings -> AI ("Default"
// for the one the page creates), which describes the form field, not the thing.
// The CLI's catalog graph already calls this node "AI Model" (see
// settingProviderLabel in cli/depgraph.go); this is the same name in the live
// picture, so the two views of one wiring say the same word.
const inferenceLabel = "AI Model"

// labelInferenceProviders renames the nodes that fill the inference contract.
//
// With the one upstream the page creates today, the contract is the whole
// answer and the node reads as "AI Model". With more than one, a name that
// repeats itself across two boxes says nothing, so the typed name comes back as
// the qualifier: the reader can still tell which upstream an edge points at.
func (e externalIndex) labelInferenceProviders() {
	if len(e.inference) == 0 {
		return
	}
	if len(e.inference) == 1 {
		e.nodes[e.inference[0]].DisplayName = inferenceLabel
		return
	}
	for _, i := range e.inference {
		node := &e.nodes[i]
		if node.DisplayName == "" {
			node.DisplayName = inferenceLabel
			continue
		}
		node.DisplayName = inferenceLabel + " (" + node.DisplayName + ")"
	}
}

// providerNodeIDs maps one declared provider of one contract to the graph
// nodes an edge may point at. A setting provider fans out to every record
// filling that contract, because the consumer named the role and the operator
// may have filled it more than once.
func (idx externalIndex) providerNodeIDs(contract string, provider catalog.BoundProvider) []string {
	if provider.IsSetting() {
		return idx.byContract[contract]
	}
	if node, remote := idx.byApp[provider.App]; remote {
		return []string{node}
	}
	return []string{provider.App}
}

type graphNode struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
	IsSystem    bool   `json:"isSystem"`
	NodeType    string `json:"nodeType"` // "app", "container", "connection", or "service"
	// ParentID is the owning app's node ID for container nodes; the dashboard
	// draws those inside the app's box.
	ParentID string `json:"parentId,omitempty"`
	// Phase is the live lifecycle phase the engine reports for this node,
	// empty for a node the engine does not track (an external provider, the
	// ingress). Status is the stored app status and answers a different
	// question, so both ship: the display reads the live one when it exists
	// and falls back to the stored one when it does not.
	Phase string `json:"phase,omitempty"`
	// Reason is the failure text the engine recorded on the node. It is the
	// answer to "why is this one stuck" that does not require reading a log.
	Reason string `json:"reason,omitempty"`
	// InFlight says a goroutine is working on this node right now, which is
	// what lets the graph point at the current node rather than only at the
	// pass.
	InFlight bool `json:"inFlight,omitempty"`
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
func (m *systemModule) buildGraphEdges(app *store.InstalledApp, present map[string]bool, external externalIndex) []graphEdge {
	targets := m.integrationTargets(app, external)

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
func (m *systemModule) integrationTargets(app *store.InstalledApp, external externalIndex) []integrationTarget {
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
			for _, node := range external.providerNodeIDs(label, provider) {
				add(label, node)
			}
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
// every installed app, plus every external provider node.
//
// The display filters on this rather than trusting the catalog's compatible
// list outright because an edge naming a node that is not in the payload still
// reaches the browser, which draws an arrow into empty space. A compatible
// entry whose provider is neither installed nor registered is a possibility the
// metadata allows, not a wiring that exists.
func providerNodes(apps []*store.InstalledApp, external externalIndex) map[string]bool {
	present := make(map[string]bool, len(apps)+len(external.nodes))
	for _, app := range apps {
		present[app.CatalogID] = true
	}
	for id := range external.ids() {
		present[id] = true
	}
	return present
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
// installed apps, their catalog definitions, and the ingress node carrying
// the address the instance is reached through.
func (m *systemModule) buildDeveloperGraph(
	apps []*store.InstalledApp,
) developerGraph {
	external := m.buildExternalIndex()

	nodes, edges, hasTraefik := m.appNodes(apps, external)
	if hasTraefik {
		nodes = append(nodes, graphNode{
			ID:          "conn:local",
			DisplayName: m.entryPointLabel(),
			Status:      "active",
			NodeType:    "connection",
		})
		edges = append(edges, graphEdge{Source: "conn:local", Target: "traefik", Label: "route"})
	}

	nodes = append(nodes, external.nodes...)

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

// appNodes builds one node per installed app, plus one child node per
// container that app declares, and reports whether traefik is among them (the
// ingress node only means something with a proxy to attach to) alongside each
// app's integration edges.
func (m *systemModule) appNodes(
	apps []*store.InstalledApp,
	external externalIndex,
) ([]graphNode, []graphEdge, bool) {
	present := providerNodes(apps, external)
	nodes := make([]graphNode, 0, len(apps))
	edges := make([]graphEdge, 0)
	hasTraefik := false
	states := m.nodeStates()

	for _, app := range apps {
		containers, containerEdges := m.containerNodes(app, states)
		phase, reason, inFlight := rollup(containers)
		nodes = append(nodes, graphNode{
			ID:          app.CatalogID,
			DisplayName: app.DisplayName,
			Status:      string(app.Status),
			IsSystem:    app.IsSystem,
			NodeType:    "app",
			Phase:       phase,
			Reason:      reason,
			InFlight:    inFlight,
		})

		nodes = append(nodes, containers...)
		edges = append(edges, containerEdges...)

		if app.CatalogID == "traefik" {
			hasTraefik = true
		}

		edges = append(edges, m.buildGraphEdges(app, present, external)...)
	}

	return nodes, edges, hasTraefik
}

// nodeStates returns the live lifecycle state of every graph node, keyed by
// node ID. Nil when no orchestrator is wired (nodes then fall back to their
// app's stored status and report no phase of their own).
func (m *systemModule) nodeStates() map[string]orchestrator.NodeState {
	if m.orch == nil {
		return nil
	}
	return m.orch.NodeStates()
}

// phaseRank orders the user-facing phases from least to most advanced, so an
// app's rollup can name whichever container is furthest behind. A word the
// engine does not emit ranks with queued: an unfamiliar phase is unfinished
// business, and saying so is better than claiming a node is running.
func phaseRank(phase string) int {
	switch phase {
	case "failed":
		return -1
	case "queued":
		return 0
	case "configuring":
		return 1
	case "starting":
		return 2
	case "finalizing":
		return 3
	case "running":
		return 4
	default:
		return 0
	}
}

// rollup folds one app's container nodes into what the app's own node reports.
//
// The app is not a node the engine drives; its containers are. So the app's
// phase is whichever container is furthest behind, its reason the reason that
// container gave, and it counts as in flight while any of them is. An app whose
// containers carry no phase (no engine wired) rolls up to nothing and keeps its
// stored status.
func rollup(containers []graphNode) (phase, reason string, inFlight bool) {
	rank, first := 0, true
	for _, c := range containers {
		if c.Phase == "" {
			continue
		}
		if first || phaseRank(c.Phase) < rank {
			rank, first = phaseRank(c.Phase), false
			phase, reason = c.Phase, c.Reason
		}
		inFlight = inFlight || c.InFlight
	}
	return phase, reason, inFlight
}

// containerNodes builds the container child nodes for an installed app plus
// the within-app dependsOn edges between them. The app's catalog entry is the
// source; an app that declares no containers (legacy entry, or a cache miss)
// is represented by a single node keyed by the catalog ID, mirroring how the
// lifecycle graph names it.
func (m *systemModule) containerNodes(
	app *store.InstalledApp,
	states map[string]orchestrator.NodeState,
) ([]graphNode, []graphEdge) {
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
		state := states[def.Name]
		status := state.Phase
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
			Phase:       state.Phase,
			Reason:      state.Reason,
			InFlight:    state.InFlight,
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
