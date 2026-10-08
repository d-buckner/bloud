// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testCatalog is a two-app catalog: one system app with a dependency chain,
// one user app that consumes the proxy and the identity provider.
func testCatalog() map[string]*AppMetadata {
	return map[string]*AppMetadata{
		"traefik": {
			Name:        "traefik",
			DisplayName: "Traefik",
			Category:    "network",
			IsSystem:    true,
			Containers:  []ContainerMetadata{{Name: "apps-traefik"}},
		},
		"authentik": {
			Name:        "authentik",
			DisplayName: "Authentik",
			Category:    "security",
			IsSystem:    true,
			Integrations: map[string]Integration{
				"proxy": {Required: true, Compatible: []CompatibleApp{{App: "traefik", Default: true}}},
			},
			Containers: []ContainerMetadata{
				{Name: "apps-authentik-postgres"},
				{Name: "apps-authentik-server", DependsOn: []string{"apps-authentik-postgres"}},
				{Name: "apps-authentik-ldap", DependsOn: []string{"apps-authentik-server"}},
			},
		},
		"widget-app": {
			Name:        "widget-app",
			DisplayName: "Widget App",
			Category:    "media",
			SSO:         SSOConfig{Strategy: "forward-auth"},
			Integrations: map[string]Integration{
				"proxy": {Required: true, Compatible: []CompatibleApp{{App: "traefik", Default: true}}},
				"sso":   {Required: false, Compatible: []CompatibleApp{{App: "authentik", Default: true}}},
			},
			Containers: []ContainerMetadata{{Name: "apps-widget-app"}},
		},
	}
}

// linesBetween returns the block of the diagram from the line containing
// `start` up to the next `end`, so assertions can scope to one app's box.
func linesBetween(t *testing.T, rendered, start string) []string {
	t.Helper()
	lines := strings.Split(rendered, "\n")
	begin := -1
	for i, line := range lines {
		if strings.Contains(line, start) {
			begin = i
			break
		}
	}
	if begin < 0 {
		t.Fatalf("no line containing %q in:\n%s", start, rendered)
	}
	var out []string
	for _, line := range lines[begin:] {
		if strings.TrimSpace(line) == "end" {
			return out
		}
		out = append(out, line)
	}
	t.Fatalf("no `end` after %q in:\n%s", start, rendered)
	return nil
}

func TestRenderGraphBoxesEveryApp(t *testing.T) {
	rendered := renderDependencyGraph(testCatalog())

	wants := []string{
		`subgraph app_traefik["Traefik (system)"]`,
		`subgraph app_authentik["Authentik (system)"]`,
		`subgraph app_widget_app["Widget App"]`,
	}
	for _, want := range wants {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing box %q\n%s", want, rendered)
		}
	}
}

func TestRenderGraphContainersInsideTheirOwnBox(t *testing.T) {
	rendered := renderDependencyGraph(testCatalog())

	box := strings.Join(linesBetween(t, rendered, `subgraph app_authentik`), "\n")
	for _, want := range []string{
		`c_authentik_postgres["postgres"]`,
		`c_authentik_server["server"]`,
		`c_authentik_ldap["ldap"]`,
	} {
		if !strings.Contains(box, want) {
			t.Errorf("authentik box missing %s:\n%s", want, box)
		}
	}

	// A container belongs to exactly one box: the widget box must not hold
	// another app's node.
	widgetBox := strings.Join(linesBetween(t, rendered, `subgraph app_widget_app`), "\n")
	if strings.Contains(widgetBox, "c_authentik") {
		t.Errorf("widget box leaked authentik nodes:\n%s", widgetBox)
	}
}

func TestRenderGraphWithinAppDependsOnEdges(t *testing.T) {
	rendered := renderDependencyGraph(testCatalog())
	box := strings.Join(linesBetween(t, rendered, `subgraph app_authentik`), "\n")

	for _, want := range []string{
		"c_authentik_server --> c_authentik_postgres",
		"c_authentik_ldap --> c_authentik_server",
	} {
		if !strings.Contains(box, want) {
			t.Errorf("missing within-app edge %q:\n%s", want, box)
		}
	}
}

// A dependsOn that points outside the app would draw an arrow to a node that
// is not in the box, so it is dropped rather than rendered as a dangling edge.
func TestRenderGraphDropsUnknownDependsOn(t *testing.T) {
	apps := map[string]*AppMetadata{
		"lonely": {
			Name: "lonely",
			Containers: []ContainerMetadata{
				{Name: "apps-lonely", DependsOn: []string{"apps-someone-elses-db"}},
			},
		},
	}
	rendered := renderDependencyGraph(apps)
	if strings.Contains(rendered, "someone_elses_db") {
		t.Errorf("edge drawn to a container the app does not declare:\n%s", rendered)
	}
}

func TestRenderGraphIntegrationEdgeLabels(t *testing.T) {
	rendered := renderDependencyGraph(testCatalog())

	// The sso edge carries the app's strategy, not the literal "sso".
	if !strings.Contains(rendered, "app_widget_app -->|forward-auth| app_authentik") {
		t.Errorf("missing strategy-labeled sso edge:\n%s", rendered)
	}
	if strings.Contains(rendered, "app_widget_app -->|sso|") {
		t.Errorf("sso edge kept the generic label:\n%s", rendered)
	}
	// The proxy edge is drawn from the proxy to the app it routes.
	if !strings.Contains(rendered, "app_traefik -->|proxy| app_widget_app") {
		t.Errorf("missing reversed proxy edge:\n%s", rendered)
	}
	if strings.Contains(rendered, "app_widget_app -->|proxy| app_traefik") {
		t.Errorf("proxy edge left in the app-to-proxy direction:\n%s", rendered)
	}
	// Required integrations carry no marker: the star notation is gone, so no
	// edge label in the diagram body carries one. (Scoped to the fence: the
	// "apps/*/metadata.yaml" glob in the generated note is not a marker.)
	fence := rendered[strings.Index(rendered, "```mermaid"):strings.LastIndex(rendered, "```")]
	if strings.Contains(fence, "*") {
		t.Errorf("a required-integration marker leaked into the diagram:\n%s", fence)
	}
}

// An sso integration with no declared strategy falls back to the generic
// label rather than rendering an empty edge annotation.
func TestRenderGraphSSOEdgeFallsBackWithoutStrategy(t *testing.T) {
	apps := map[string]*AppMetadata{
		"provider": {Name: "provider"},
		"consumer": {
			Name: "consumer",
			Integrations: map[string]Integration{
				"sso": {Compatible: []CompatibleApp{{App: "provider", Default: true}}},
			},
		},
	}
	rendered := renderDependencyGraph(apps)
	if !strings.Contains(rendered, "app_consumer -->|sso| app_provider") {
		t.Errorf("missing fallback sso edge:\n%s", rendered)
	}
}

// The generated block is committed, so two runs over the same catalog must
// produce byte-identical output. Go randomizes map iteration order on every
// pass, so rendering the same catalog repeatedly exercises the sorting
// rather than the luck of one ordering.
func TestRenderGraphIsDeterministic(t *testing.T) {
	first := renderDependencyGraph(testCatalog())
	for i := 0; i < 8; i++ {
		if got := renderDependencyGraph(testCatalog()); got != first {
			t.Fatalf("render %d differs from the first render of the same catalog", i+1)
		}
	}
}

func TestRenderGraphSubgraphsAreBalanced(t *testing.T) {
	rendered := renderDependencyGraph(testCatalog())
	open := strings.Count(rendered, "\n    subgraph ")
	closed := strings.Count(rendered, "\n    end\n")
	if open != closed {
		t.Errorf("%d subgraph openings but %d `end` closings:\n%s", open, closed, rendered)
	}
}

func TestRenderGraphIsFencedMermaid(t *testing.T) {
	rendered := renderDependencyGraph(testCatalog())
	if !strings.HasPrefix(rendered, graphBeginMarker) {
		t.Errorf("block does not start with the begin marker: %q", rendered[:40])
	}
	if !strings.HasSuffix(strings.TrimSpace(rendered), graphEndMarker) {
		t.Error("block does not end with the end marker")
	}
	if !strings.Contains(rendered, "```mermaid\nflowchart TD") {
		t.Error("block has no mermaid fence")
	}
	if strings.Count(rendered, "```") != 2 {
		t.Errorf("expected exactly one fenced diagram, found %d fences", strings.Count(rendered, "```"))
	}
}

// An app that declares no containers is still a box with one node, the way
// the lifecycle graph represents it.
func TestRenderGraphAppWithoutContainers(t *testing.T) {
	apps := map[string]*AppMetadata{
		"flat": {Name: "flat", DisplayName: "Flat"},
	}
	rendered := renderDependencyGraph(apps)
	box := strings.Join(linesBetween(t, rendered, `subgraph app_flat`), "\n")
	if !strings.Contains(box, `c_flat["flat"]`) {
		t.Errorf("containerless app rendered without a node:\n%s", box)
	}
}

func TestContainerLabel(t *testing.T) {
	cases := []struct {
		container string
		app       string
		want      string
	}{
		{"apps-immich-postgres", "immich", "postgres"},
		{"apps-authentik-ldap", "authentik", "ldap"},
		{"apps-paperless-ngx-gotenberg", "paperless-ngx", "gotenberg"},
		{"apps-jellyfin", "jellyfin", "jellyfin"},
		{"apps-paperless-ngx", "paperless-ngx", "paperless-ngx"},
		{"custom-name", "other", "custom-name"},
	}
	for _, c := range cases {
		if got := containerLabel(c.container, c.app); got != c.want {
			t.Errorf("containerLabel(%q, %q) = %q, want %q", c.container, c.app, got, c.want)
		}
	}
}

// Two app names that sanitize to the same id part must not collapse two
// containers onto one node: the second one gets a suffix instead.
func TestAssignContainerIDSplitsCollisions(t *testing.T) {
	apps := map[string]*AppMetadata{
		"a":   {Name: "a", Containers: []ContainerMetadata{{Name: "apps-a-b"}}},
		"a_b": {Name: "a_b", Containers: []ContainerMetadata{{Name: "apps-a_b"}}},
	}

	ids := assignContainerIDs(apps)
	first := ids.get("a", "apps-a-b")
	second := ids.get("a_b", "apps-a_b")
	if first == "" || second == "" {
		t.Fatalf("missing ids: %q / %q", first, second)
	}
	if first == second {
		t.Fatalf("two different containers share node id %q", first)
	}

	rendered := renderDependencyGraph(apps)
	if !strings.Contains(rendered, `["b"]`) || !strings.Contains(rendered, `["a_b"]`) {
		t.Errorf("both containers should still render:\n%s", rendered)
	}
}

func TestDefaultProviderPrefersDefaultFlag(t *testing.T) {
	integration := Integration{Compatible: []CompatibleApp{{App: "alt"}, {App: "chosen", Default: true}}}
	if got := defaultProvider(integration); got != "chosen" {
		t.Errorf("defaultProvider = %q, want chosen", got)
	}
	fallback := Integration{Compatible: []CompatibleApp{{App: "first"}, {App: "second"}}}
	if got := defaultProvider(fallback); got != "first" {
		t.Errorf("defaultProvider without a default flag = %q, want first", got)
	}
	if got := defaultProvider(Integration{}); got != "" {
		t.Errorf("defaultProvider with no compatible apps = %q, want empty", got)
	}
}

// A `multi: true` integration binds every compatible provider the resolver
// can find, so the graph has to draw all of them. Drawing only the default
// made Radicale's icsFeed contract look like it synced Radarr alone when it
// syncs Sonarr too, and hid the calendar MCP from a reader trying to see how
// Hermes gets its tools.
func TestDrawnProvidersExpandsMultiIntegrations(t *testing.T) {
	multi := Integration{
		Multi:      true,
		Compatible: []CompatibleApp{{App: "radarr"}, {App: "sonarr"}},
	}
	got := drawnProviders(multi)
	want := []string{"radarr", "sonarr"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("drawnProviders(multi) = %v, want %v", got, want)
	}
}

// A single-provider integration must not fan out. Drawing every compatible
// alternative would show wiring that never happens, which is worse than
// showing nothing.
func TestDrawnProvidersKeepsSingleIntegrationsNarrow(t *testing.T) {
	single := Integration{
		Compatible: []CompatibleApp{{App: "alt"}, {App: "chosen", Default: true}},
	}
	got := drawnProviders(single)
	if len(got) != 1 || got[0] != "chosen" {
		t.Errorf("drawnProviders(single) = %v, want [chosen]", got)
	}
	if got := drawnProviders(Integration{}); len(got) != 0 {
		t.Errorf("drawnProviders(empty) = %v, want none", got)
	}
}

// A multi integration pointing at the instance's own settings still maps to
// the AI node rather than to an empty app name.
func TestDrawnProvidersMapsInstanceSource(t *testing.T) {
	multi := Integration{
		Multi:      true,
		Compatible: []CompatibleApp{{Source: "setting"}, {App: "real"}},
	}
	got := drawnProviders(multi)
	if len(got) != 2 || got[0] != settingProviderNodeID || got[1] != "real" {
		t.Errorf("drawnProviders(instance+app) = %v, want [%s real]", got, settingProviderNodeID)
	}
}

// The browser renderer feeds this JSON into the dashboard's own graph
// components, so the shape has to match what /api/system/developer returns:
// app nodes keyed by catalog id, container nodes keyed by container name and
// parented to their app, integration edges between app ids.
func TestBuildCatalogGraphShape(t *testing.T) {
	graph := buildCatalogGraph(testCatalog())

	byID := make(map[string]catalogGraphNode, len(graph.Nodes))
	for _, node := range graph.Nodes {
		byID[node.ID] = node
	}

	traefik := byID["traefik"]
	if traefik.ID == "" || !traefik.IsSystem || traefik.NodeType != "app" {
		t.Errorf("traefik node wrong: %+v", traefik)
	}
	if traefik.Status != catalogNodeStatus {
		t.Errorf("catalog node status = %q, want %q", traefik.Status, catalogNodeStatus)
	}

	server := byID["apps-authentik-server"]
	if server.NodeType != "container" || server.ParentID != "authentik" {
		t.Errorf("container node wrong: %+v", server)
	}
	if server.DisplayName != "server" {
		t.Errorf("container display name = %q, want the component name", server.DisplayName)
	}
	if !server.IsSystem {
		t.Error("a system app's container lost the system flag")
	}

	widget := byID["widget-app"]
	if widget.DisplayName != "Widget App" {
		t.Errorf("display name = %q, want the catalog display name", widget.DisplayName)
	}
	if widget.Category != "media" {
		t.Errorf("category = %q, want media", widget.Category)
	}
}

func TestBuildCatalogGraphEdges(t *testing.T) {
	graph := buildCatalogGraph(testCatalog())

	has := func(source, target, label string) bool {
		for _, e := range graph.Edges {
			if e.Source == source && e.Target == target && e.Label == label {
				return true
			}
		}
		return false
	}

	// Within-app dependsOn: drawn between container names, unlabeled, the
	// same way the live graph leaves box-internal edges bare.
	if !has("apps-authentik-server", "apps-authentik-postgres", "") {
		t.Errorf("missing unlabeled within-app edge: %+v", graph.Edges)
	}
	// The sso edge carries the strategy, and the proxy edge points from the
	// proxy to the app it routes.
	if !has("widget-app", "authentik", "forward-auth") {
		t.Errorf("missing strategy-labeled sso edge: %+v", graph.Edges)
	}
	if !has("traefik", "widget-app", "proxy") {
		t.Errorf("missing reversed proxy edge: %+v", graph.Edges)
	}
	if has("widget-app", "traefik", "proxy") {
		t.Errorf("proxy edge left in the app-to-proxy direction: %+v", graph.Edges)
	}
	// A dependsOn on a container the app does not declare draws nothing.
	if has("apps-widget-app", "apps-someone-elses-db", "") {
		t.Errorf("edge drawn to a container the app does not declare: %+v", graph.Edges)
	}
}

// An app that declares no containers is one flat app node: emitting a
// container node with the app's own id would collide with the app node.
func TestBuildCatalogGraphContainerlessApp(t *testing.T) {
	graph := buildCatalogGraph(map[string]*AppMetadata{
		"flat": {Name: "flat", DisplayName: "Flat"},
	})
	if len(graph.Nodes) != 2 || graph.Nodes[0].NodeType != "app" || graph.Nodes[0].ID != "flat" {
		t.Errorf("containerless app rendered as %+v", graph.Nodes)
	}
	// The AI Model node rides along with every snapshot; see the note on
	// settingProviderNodeID.
	if graph.Nodes[1].ID != settingProviderNodeID || graph.Nodes[1].NodeType != "service" {
		t.Errorf("expected the AI provider node second, got %+v", graph.Nodes[1])
	}
}

// The instance's AI provider is in every snapshot, whatever the catalog
// declares. The generated picture is the full view of what Bloud can wire,
// not of what happens to have a consumer this week.
func TestBuildCatalogGraphAlwaysCarriesAIProvider(t *testing.T) {
	graph := buildCatalogGraph(map[string]*AppMetadata{
		"lonely": {Name: "lonely", DisplayName: "Lonely"},
	})
	found := false
	for _, node := range graph.Nodes {
		if node.ID == settingProviderNodeID {
			found = true
			if node.DisplayName != settingProviderLabel || node.NodeType != "service" {
				t.Errorf("AI provider node rendered as %+v", node)
			}
		}
	}
	if !found {
		t.Errorf("snapshot has no AI provider node: %+v", graph.Nodes)
	}
}

// An app whose only inference provider is `source: setting` gets an edge to
// the AI Model node. Before the instance source mapped to a node this edge
// resolved to an empty app name and vanished, so the wiring the app declares
// was invisible.
func TestBuildCatalogGraphInferenceEdgeToAIProvider(t *testing.T) {
	graph := buildCatalogGraph(map[string]*AppMetadata{
		"agent": {
			Name:        "agent",
			DisplayName: "Agent",
			Integrations: map[string]Integration{
				"inference": {Compatible: []CompatibleApp{{Source: "setting", Default: true}}},
			},
		},
	})
	want := catalogGraphEdge{Source: "agent", Target: settingProviderNodeID, Label: "inference"}
	if !containsEdge(graph.Edges, want) {
		t.Errorf("no inference edge to the AI provider; edges: %+v", graph.Edges)
	}
}

func containsEdge(edges []catalogGraphEdge, want catalogGraphEdge) bool {
	for _, edge := range edges {
		if edge == want {
			return true
		}
	}
	return false
}

// A compatible entry naming an app that is not in the catalog still draws
// nothing: the AI node is the instance's, not a catch-all for a dangling
// provider reference.
func TestBuildCatalogGraphUnknownProviderStillDropped(t *testing.T) {
	graph := buildCatalogGraph(map[string]*AppMetadata{
		"consumer": {
			Name:        "consumer",
			DisplayName: "Consumer",
			Integrations: map[string]Integration{
				"pvr": {Compatible: []CompatibleApp{{App: "not-shipped", Default: true}}},
			},
		},
	})
	for _, edge := range graph.Edges {
		if edge.Target == "not-shipped" || edge.Source == "not-shipped" {
			t.Errorf("edge to an app the catalog does not have: %+v", edge)
		}
	}
}

func TestDefaultProviderMapsInstanceSourceToAINode(t *testing.T) {
	integration := Integration{Compatible: []CompatibleApp{{Source: "setting", Default: true}}}
	if got := defaultProvider(integration); got != settingProviderNodeID {
		t.Errorf("defaultProvider = %q, want %q", got, settingProviderNodeID)
	}
}

func TestRenderGraphAIProviderOutsideEveryBox(t *testing.T) {
	apps := testCatalog()
	apps["agent"] = &AppMetadata{
		Name:        "agent",
		DisplayName: "Agent",
		Integrations: map[string]Integration{
			"inference": {Compatible: []CompatibleApp{{Source: "setting", Default: true}}},
		},
	}
	rendered := renderDependencyGraph(apps)

	if !strings.Contains(rendered, `ai_model["AI Model"]`) {
		t.Errorf("no AI Model node in:\n%s", rendered)
	}
	if !strings.Contains(rendered, "app_agent -->|inference| ai_model") {
		t.Errorf("no inference edge to the AI Model node in:\n%s", rendered)
	}
	// The node is drawn, never boxed: it is not an app, so it must not open
	// or sit inside a subgraph.
	if strings.Contains(rendered, "subgraph ai_model") {
		t.Errorf("the AI provider must not be a subgraph:\n%s", rendered)
	}
	if lines := strings.Split(rendered, "\n"); countSubgraphDepthAtAI(lines) != 0 {
		t.Errorf("AI Model node is nested inside a subgraph")
	}
}

// countSubgraphDepthAtAI reports how many open subgraphs surround the line
// that declares the AI Model node.
func countSubgraphDepthAtAI(lines []string) int {
	depth := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "subgraph ") {
			depth++
			continue
		}
		if trimmed == "end" {
			depth--
			continue
		}
		if strings.HasPrefix(trimmed, settingProviderMermaid+"[") {
			return depth
		}
	}
	return -1
}

func TestRenderCatalogGraphJSON(t *testing.T) {
	encoded, err := renderCatalogGraphJSON(testCatalog())
	if err != nil {
		t.Fatalf("renderCatalogGraphJSON: %v", err)
	}

	var round catalogGraph
	if err := json.Unmarshal([]byte(encoded), &round); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, encoded)
	}
	// testCatalog: 3 app nodes + 5 container nodes + the AI provider node.
	if len(round.Nodes) != 9 || len(round.Edges) == 0 {
		t.Errorf("unexpected round-trip size: %d nodes, %d edges\n%s", len(round.Nodes), len(round.Edges), encoded)
	}

	// The renderer is what CI runs, and a non-deterministic snapshot would
	// make every generated image differ from the last.
	again, err := renderCatalogGraphJSON(testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if again != encoded {
		t.Error("two renders of the same catalog differ")
	}
}

// The committed image must describe the catalog that is actually in the
// tree, so every app directory shows up in the snapshot the image is
// rendered from, with its containers.
func TestRepoCatalogJSONCoversEveryApp(t *testing.T) {
	root, err := getProjectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	appsDir := filepath.Join(root, "apps")
	apps, err := loadAppMetadata(appsDir)
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	graph := buildCatalogGraph(apps)
	appIDs := map[string]bool{}
	containersByApp := map[string]int{}
	for _, node := range graph.Nodes {
		if node.NodeType == "app" {
			appIDs[node.ID] = true
		}
		if node.NodeType == "container" {
			containersByApp[node.ParentID]++
		}
	}

	entries, err := os.ReadDir(appsDir)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta := filepath.Join(appsDir, entry.Name(), "metadata.yaml")
		if _, err := os.Stat(meta); err != nil {
			continue
		}
		checked++
		if !appIDs[entry.Name()] {
			t.Errorf("app %s is missing from the catalog snapshot", entry.Name())
			continue
		}
		if want := len(apps[entry.Name()].Containers); want > 0 && containersByApp[entry.Name()] != want {
			t.Errorf("app %s snapshot has %d container nodes, metadata declares %d",
				entry.Name(), containersByApp[entry.Name()], want)
		}
	}
	if checked == 0 {
		t.Fatal("no app metadata found to check")
	}
}

func TestSpliceGraphBlockKeepsTheRestOfTheDocument(t *testing.T) {
	original := "# Doc\n\nIntro text.\n\n" + graphBeginMarker + "\nold\n" + graphEndMarker + "\n\nOutro.\n"
	generated := graphBeginMarker + "\nfresh content\n" + graphEndMarker + "\n"

	updated, ok := spliceGraphBlock(original, generated)
	if !ok {
		t.Fatal("splice reported no block, but the markers are present")
	}
	// Everything outside the markers is preserved byte for byte, including
	// the blank lines the document already had after the end marker. The
	// block itself contributes no newline of its own at the seam: the one
	// that terminated the end-marker line is the document's, so the blank
	// line after the marker is still exactly one blank line.
	want := "# Doc\n\nIntro text.\n\n" + strings.TrimRight(generated, "\n") + "\n\nOutro.\n"
	if updated != want {
		t.Errorf("splice changed more than the block:\ngot:\n%s\nwant:\n%s", updated, want)
	}

	if _, ok := spliceGraphBlock("# nothing here\n", generated); ok {
		t.Error("splice succeeded on a document with no markers")
	}
}

// TestSpliceGraphBlockIsIdempotent pins the property the merge-to-main
// refresh depends on. That job runs --write on every merge and commits
// only when the file changed, so a writer that adds a blank line on each
// pass makes the guard always fire: it shipped whitespace-only commits and
// a tail of blank lines that grew with every catalog change.
func TestSpliceGraphBlockIsIdempotent(t *testing.T) {
	generated := graphBeginMarker + "\nfresh content\n" + graphEndMarker + "\n"

	cases := map[string]string{
		"block ends the file with blank lines": "# Doc\n\n" + graphBeginMarker + "\nold\n" + graphEndMarker + "\n\n\n",
		"block ends the file cleanly":          "# Doc\n\n" + graphBeginMarker + "\nold\n" + graphEndMarker + "\n",
		"block is followed by more prose":      "# Doc\n\n" + graphBeginMarker + "\nold\n" + graphEndMarker + "\n\nOutro.\n",
	}

	for name, original := range cases {
		current := original
		var first string
		for pass := 1; pass <= 3; pass++ {
			var ok bool
			current, ok = spliceGraphBlock(current, generated)
			if !ok {
				t.Fatalf("%s: splice pass %d reported no block", name, pass)
			}
			if pass == 1 {
				first = current
				// The document's own ending is the ending: the block
				// does not bring a second newline to the party.
				if strings.Count(current, "\n") != strings.Count(original, "\n") {
					t.Errorf("%s: first write changed the line count %d -> %d:\n%q",
						name, strings.Count(original, "\n"), strings.Count(current, "\n"), current)
				}
				continue
			}
			if current != first {
				t.Errorf("%s: pass %d changed the file again:\nfirst:\n%q\nafter:\n%q", name, pass, first, current)
			}
		}
	}
}

// TestWriteGraphBlockIsIdempotentOnDisk covers the same property through
// the mode CI actually runs: a second --write over a file that is already
// current must report current, not rewrite a longer file.
func TestWriteGraphBlockIsIdempotentOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")
	original := "# Doc\n\nIntro.\n\n" + graphBeginMarker + "\nstale\n" + graphEndMarker + "\n\nOutro.\n"
	generated := graphBeginMarker + "\nfresh\n" + graphEndMarker + "\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if code := writeGraphBlock(dir, "README.md", generated); code != 0 {
		t.Fatalf("first writeGraphBlock = %d, want 0", code)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code := writeGraphBlock(dir, "README.md", generated); code != 0 {
		t.Fatalf("second writeGraphBlock = %d, want 0", code)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("second write changed the file (%d -> %d bytes):\n%s", len(first), len(second), second)
	}
}

func TestExtractGraphBlock(t *testing.T) {
	content := "before " + graphBeginMarker + "\ninner\n" + graphEndMarker + " after"
	block, ok := extractGraphBlock(content)
	if !ok {
		t.Fatal("extract failed on a document with both markers")
	}
	if !strings.HasPrefix(block, graphBeginMarker) || !strings.HasSuffix(block, graphEndMarker) {
		t.Errorf("extracted block is not marker-delimited: %q", block)
	}
	if !strings.Contains(block, "inner") {
		t.Errorf("extracted block lost the inner content: %q", block)
	}

	if _, ok := extractGraphBlock("only " + graphBeginMarker); ok {
		t.Error("extract succeeded with no end marker")
	}
}

// writeGraphBlock and checkGraphBlock are the two modes CI and the fast tier
// run, so both are exercised against a real file.
func TestWriteAndCheckGraphBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")
	original := "# Doc\n\nIntro.\n\n" + graphBeginMarker + "\nstale\n" + graphEndMarker + "\n\nOutro.\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	generated := graphBeginMarker + "\nfresh\n" + graphEndMarker + "\n"

	if code := writeGraphBlock(dir, "README.md", generated); code != 0 {
		t.Fatalf("writeGraphBlock = %d, want 0", code)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "fresh") || strings.Contains(string(written), "stale") {
		t.Errorf("block not replaced:\n%s", written)
	}
	if !strings.HasPrefix(string(written), "# Doc\n\nIntro.\n\n") || !strings.HasSuffix(string(written), "\n\nOutro.\n") {
		t.Errorf("surrounding document changed:\n%s", written)
	}

	if code := checkGraphBlock(dir, "README.md", generated); code != 0 {
		t.Errorf("checkGraphBlock on a fresh block = %d, want 0", code)
	}
	if code := checkGraphBlock(dir, "README.md", graphBeginMarker+"\nother\n"+graphEndMarker+"\n"); code != 1 {
		t.Errorf("checkGraphBlock on a stale block = %d, want 1", code)
	}

	// A target with no markers fails instead of appending a second diagram.
	plain := filepath.Join(dir, "plain.md")
	if err := os.WriteFile(plain, []byte("# plain\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if code := writeGraphBlock(dir, "plain.md", generated); code != 1 {
		t.Errorf("writeGraphBlock without markers = %d, want 1 (it must not append)", code)
	}
	if code := checkGraphBlock(dir, "plain.md", generated); code != 1 {
		t.Errorf("checkGraphBlock without markers = %d, want 1", code)
	}
}

// The committed README must carry the graph the current catalog produces. This
// is the same invariant `./bloud depgraph --check` enforces in the fast tier,
// asserted here too so a `go test ./...` catches it. The README is the only
// home of the diagram now, so a stale README is a stale graph.
func TestRepoREADMEGraphIsCurrent(t *testing.T) {
	root, err := getProjectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	apps, err := loadAppMetadata(filepath.Join(root, "apps"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if code := checkGraphBlock(root, graphDefaultFile, renderDependencyGraph(apps)); code != 0 {
		t.Errorf("%s is stale: run ./bloud depgraph --write and commit it", graphDefaultFile)
	}
}

// The README draws the graph with the Mermaid block itself rather than with a
// rendered picture. GitHub renders the fence inline, so there is no image
// artifact between the catalog and what a reader sees, and nothing is left
// behind pointing at the PNG that used to be committed.
func TestRepoREADMEDiagramIsMermaidNotAnImage(t *testing.T) {
	root, err := getProjectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(root, graphDefaultFile))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	body := string(readme)

	block, ok := extractGraphBlock(body)
	if !ok {
		t.Fatalf("README has no generated dependency graph block: run ./bloud depgraph --write")
	}
	if !strings.Contains(block, "```mermaid") {
		t.Error("the generated block in the README is not a Mermaid diagram")
	}
	if strings.Contains(body, "dependency-graph.png") {
		t.Error("README still points at the rendered graph image; the Mermaid block replaced it")
	}
	if strings.Contains(body, "graph:image") {
		t.Error("README still tells readers to run the removed image renderer (npm run graph:image)")
	}
}

// Every app directory in the catalog must appear in the diagram: an app that
// silently dropped out would make the README look complete while lying about
// the graph.
func TestRepoCatalogEveryAppIsRendered(t *testing.T) {
	root, err := getProjectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	appsDir := filepath.Join(root, "apps")
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		t.Fatal(err)
	}

	apps, err := loadAppMetadata(appsDir)
	if err != nil {
		t.Fatal(err)
	}
	rendered := renderDependencyGraph(apps)

	boxes := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(appsDir, entry.Name(), "metadata.yaml")); err != nil {
			continue
		}
		boxes++
		if !strings.Contains(rendered, "subgraph "+appBoxID(entry.Name())) {
			t.Errorf("app %s has no box in the generated graph", entry.Name())
		}
	}
	if boxes == 0 {
		t.Fatal("no app metadata found to check")
	}
	if got := strings.Count(rendered, "\n    subgraph "); got != boxes {
		t.Errorf("rendered %d boxes for %d catalog apps", got, boxes)
	}
}
