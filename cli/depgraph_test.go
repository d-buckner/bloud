// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
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
	// The proxy edge is drawn from the proxy to the app it routes, and the
	// required marker is kept.
	if !strings.Contains(rendered, "app_traefik -->|proxy*| app_widget_app") {
		t.Errorf("missing reversed required proxy edge:\n%s", rendered)
	}
	if strings.Contains(rendered, "app_widget_app -->|proxy*| app_traefik") {
		t.Errorf("proxy edge left in the app-to-proxy direction:\n%s", rendered)
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

func TestSpliceGraphBlockKeepsTheRestOfTheDocument(t *testing.T) {
	original := "# Doc\n\nIntro text.\n\n" + graphBeginMarker + "\nold\n" + graphEndMarker + "\n\nOutro.\n"
	generated := graphBeginMarker + "\nfresh content\n" + graphEndMarker + "\n"

	updated, ok := spliceGraphBlock(original, generated)
	if !ok {
		t.Fatal("splice reported no block, but the markers are present")
	}
	// Everything outside the markers is preserved byte for byte, including
	// the blank lines the document already had after the end marker.
	want := "# Doc\n\nIntro text.\n\n" + generated + "\n\nOutro.\n"
	if updated != want {
		t.Errorf("splice changed more than the block:\ngot:\n%s\nwant:\n%s", updated, want)
	}

	if _, ok := spliceGraphBlock("# nothing here\n", generated); ok {
		t.Error("splice succeeded on a document with no markers")
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

// The committed README must carry the graph the current catalog produces.
// This is the same invariant `./bloud depgraph --check` enforces in the fast
// tier, asserted here too so a `go test ./...` catches it.
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
		t.Errorf("README.md dependency graph is stale: run ./bloud depgraph --write and commit it")
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
