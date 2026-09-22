// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// AppMetadata represents the relevant fields from metadata.yaml for dependency graphing
type AppMetadata struct {
	Name         string                 `yaml:"name"`
	DisplayName  string                 `yaml:"displayName"`
	Category     string                 `yaml:"category"`
	IsSystem     bool                   `yaml:"isSystem"`
	Integrations map[string]Integration `yaml:"integrations"`
	SSO          SSOConfig              `yaml:"sso"`
	Containers   []ContainerMetadata    `yaml:"containers"`
}

// SSOConfig represents SSO configuration
type SSOConfig struct {
	Strategy string `yaml:"strategy"`
}

// Integration defines how an app connects to other apps
type Integration struct {
	Required   bool            `yaml:"required"`
	Compatible []CompatibleApp `yaml:"compatible"`
}

// CompatibleApp defines a specific app that can fulfill an integration
type CompatibleApp struct {
	App     string `yaml:"app"`
	Default bool   `yaml:"default"`
}

// ContainerMetadata is one container an app declares. The graph reads the
// node name and the within-app dependencies; everything else in the
// container spec is irrelevant to the topology.
type ContainerMetadata struct {
	Name      string   `yaml:"name"`
	DependsOn []string `yaml:"dependsOn"`
}

// The generated block in the target document. Everything between these two
// markers is replaced by `bloud depgraph --write` and compared by
// `--check`, so the rest of the file is untouched.
const (
	graphBeginMarker = "<!-- BEGIN GENERATED DEPENDENCY GRAPH -->"
	graphEndMarker   = "<!-- END GENERATED DEPENDENCY GRAPH -->"
	graphDefaultFile = "README.md"
)

// graphMode is what a `bloud depgraph` run does with the rendered diagram.
type graphMode int

const (
	graphModePrint graphMode = iota
	graphModeWrite
	graphModeCheck
)

func cmdDepGraph(args []string) int {
	root, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	mode := graphModePrint
	target := graphDefaultFile
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--write":
			mode = graphModeWrite
		case "--check":
			mode = graphModeCheck
		case "--target":
			if i+1 >= len(args) {
				errorf("--target needs a path (usage: bloud depgraph [--write | --check] [--target FILE])")
				return 1
			}
			i++
			target = args[i]
		case "--help", "-h":
			printDepGraphUsage()
			return 0
		default:
			errorf("Unknown flag %q (usage: bloud depgraph [--write | --check] [--target FILE])", args[i])
			return 1
		}
	}

	apps, err := loadAppMetadata(filepath.Join(root, "apps"))
	if err != nil {
		errorf("Failed to load app metadata: %v", err)
		return 1
	}
	if len(apps) == 0 {
		errorf("No apps with a metadata.yaml found under %s", filepath.Join(root, "apps"))
		return 1
	}

	generated := renderDependencyGraph(apps)

	switch mode {
	case graphModeWrite:
		return writeGraphBlock(root, target, generated)
	case graphModeCheck:
		return checkGraphBlock(root, target, generated)
	default:
		fmt.Print(generated)
		return 0
	}
}

// printDepGraphUsage documents the graph command's modes.
func printDepGraphUsage() {
	fmt.Println("Usage: bloud depgraph [--write | --check] [--target FILE]")
	fmt.Println()
	fmt.Println("  (no flags)   Print the full Mermaid dependency graph to stdout")
	fmt.Println("  --write      Replace the generated block in the target file")
	fmt.Println("  --check      Exit 1 when the target file's block is not what the")
	fmt.Println("               catalog produces right now (the PR-time gate)")
	fmt.Println("  --target     File to write or check (default: README.md)")
}

// graphTargetPath resolves --target against the repo root.
func graphTargetPath(root, target string) string {
	if filepath.IsAbs(target) {
		return target
	}
	return filepath.Join(root, target)
}

// writeGraphBlock replaces the generated block in the target file. It fails
// rather than appending when the markers are missing, so a typo in the target
// can never produce a second copy of the diagram.
func writeGraphBlock(root, target, generated string) int {
	path := graphTargetPath(root, target)
	content, err := os.ReadFile(path)
	if err != nil {
		errorf("Could not read %s: %v", path, err)
		return 1
	}

	updated, ok := spliceGraphBlock(string(content), generated)
	if !ok {
		errorf("%s has no generated dependency graph block. Add these two lines where the diagram belongs:\n  %s\n  %s",
			path, graphBeginMarker, graphEndMarker)
		return 1
	}
	if updated == string(content) {
		log(relOrAbs(root, path) + " dependency graph is already current")
		return 0
	}
	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		errorf("Could not write %s: %v", path, err)
		return 1
	}
	log("Wrote the dependency graph to " + relOrAbs(root, path))
	return 0
}

// checkGraphBlock compares the target file's block against what the catalog
// produces now. A stale README describes a graph that no longer exists, so
// this is the gate that keeps the two together.
func checkGraphBlock(root, target, generated string) int {
	path := graphTargetPath(root, target)
	content, err := os.ReadFile(path)
	if err != nil {
		errorf("Could not read %s: %v", path, err)
		return 1
	}

	existing, ok := extractGraphBlock(string(content))
	if !ok {
		errorf("%s has no generated dependency graph block. Run: ./bloud depgraph --write", relOrAbs(root, path))
		return 1
	}
	if normalizeGraph(existing) == normalizeGraph(generated) {
		log(relOrAbs(root, path) + " dependency graph is up to date")
		return 0
	}

	errorf("%s dependency graph is stale. Run: ./bloud depgraph --write (then commit the result)", relOrAbs(root, path))
	reportFirstGraphDifference(normalizeGraph(existing), normalizeGraph(generated))
	return 1
}

// relOrAbs labels a path for output: relative to the repo root when it is
// inside it, absolute otherwise.
func relOrAbs(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// extractGraphBlock returns the generated block, markers included.
func extractGraphBlock(content string) (string, bool) {
	begin := strings.Index(content, graphBeginMarker)
	if begin < 0 {
		return "", false
	}
	relEnd := strings.Index(content[begin:], graphEndMarker)
	if relEnd < 0 {
		return "", false
	}
	return content[begin : begin+relEnd+len(graphEndMarker)], true
}

// spliceGraphBlock swaps the target's generated block for a new one and
// leaves the rest of the document byte-for-byte alone.
func spliceGraphBlock(content, generated string) (string, bool) {
	begin := strings.Index(content, graphBeginMarker)
	if begin < 0 {
		return content, false
	}
	relEnd := strings.Index(content[begin:], graphEndMarker)
	if relEnd < 0 {
		return content, false
	}
	absEnd := begin + relEnd + len(graphEndMarker)
	return content[:begin] + generated + content[absEnd:], true
}

// normalizeGraph trims the trailing newline and surrounding blank lines so a
// comparison is about content, not about how the file ends.
func normalizeGraph(s string) string {
	return strings.TrimSpace(s)
}

// reportFirstGraphDifference prints the first line where the committed
// diagram and the current one diverge, which is usually all it takes to see
// what a metadata change did.
func reportFirstGraphDifference(existing, generated string) {
	existingLines := strings.Split(existing, "\n")
	generatedLines := strings.Split(generated, "\n")
	limit := len(existingLines)
	if len(generatedLines) < limit {
		limit = len(generatedLines)
	}
	for i := 0; i < limit; i++ {
		if existingLines[i] != generatedLines[i] {
			fmt.Fprintf(os.Stderr, "  first difference at block line %d:\n    committed: %s\n    expected:  %s\n",
				i+1, strings.TrimSpace(existingLines[i]), strings.TrimSpace(generatedLines[i]))
			return
		}
	}
	fmt.Fprintf(os.Stderr, "  block length differs: committed %d lines, expected %d lines\n",
		len(existingLines), len(generatedLines))
}

// loadAppMetadata reads every apps/<name>/metadata.yaml into a map keyed by
// the app's declared name.
func loadAppMetadata(appsDir string) (map[string]*AppMetadata, error) {
	apps := make(map[string]*AppMetadata)

	entries, err := os.ReadDir(appsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read apps directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		metadataPath := filepath.Join(appsDir, entry.Name(), "metadata.yaml")
		if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
			continue
		}

		data, err := os.ReadFile(metadataPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", metadataPath, err)
		}

		var app AppMetadata
		if err := yaml.Unmarshal(data, &app); err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", metadataPath, err)
		}

		if app.Name == "" {
			continue
		}

		apps[app.Name] = &app
	}

	return apps, nil
}

// renderDependencyGraph renders the whole catalog as one mermaid diagram: a
// box per app, one node per container inside that box, the within-app
// dependsOn arrows, and the cross-app integration arrows.
//
// It mirrors the developer dashboard's graph (the developer graph in
// internal/api/system_module.go), which draws the same app boxes holding
// their container nodes with the same edge labels, so the README and the
// dashboard describe one topology rather than two that can drift.
func renderDependencyGraph(apps map[string]*AppMetadata) string {
	var sb strings.Builder

	sb.WriteString(graphBeginMarker + "\n")
	sb.WriteString("<!-- Generated by `./bloud depgraph --write` from `apps/*/metadata.yaml`. Do not edit by hand. -->\n")
	sb.WriteString("\n")
	sb.WriteString("```mermaid\n")
	sb.WriteString("flowchart TD\n")

	ids := assignContainerIDs(apps)
	for _, appName := range sortedAppNames(apps) {
		sb.WriteString(renderAppBox(apps[appName], ids))
	}

	sb.WriteString("\n    %% Cross-app integration edges\n")
	for _, edge := range integrationEdges(apps) {
		fmt.Fprintf(&sb, "    %s -->|%s| %s\n", edge.from, edge.label, edge.to)
	}

	sb.WriteString("```\n")
	sb.WriteString("\n" + graphLegend + "\n")
	sb.WriteString(graphEndMarker + "\n")
	return sb.String()
}

// graphLegend explains the notation, because the diagram is generated and no
// prose around it is written by whoever changed the metadata.
const graphLegend = "_Each box is one app; the nodes inside it are that app's containers, with an arrow from a container to every container it depends on. " +
	"Arrows between boxes are integrations: a `proxy` arrow is drawn from the proxy to the apps it routes, and an SSO arrow is labeled with the app's strategy (`ldap`, `forward-auth`, `native-oidc`)._"

// renderAppBox renders one app: its titled box, a node per container, and
// the dependsOn arrows between them.
func renderAppBox(app *AppMetadata, ids *containerIDs) string {
	var sb strings.Builder

	title := app.DisplayName
	if title == "" {
		title = app.Name
	}
	if app.IsSystem {
		title += " (system)"
	}
	fmt.Fprintf(&sb, "\n    subgraph %s[\"%s\"]\n", appBoxID(app.Name), title)

	if len(app.Containers) == 0 {
		// An app that declares no containers is one node, the way the
		// lifecycle graph names it after its catalog entry.
		fmt.Fprintf(&sb, "        %s[\"%s\"]\n", ids.get(app.Name, app.Name), app.Name)
		sb.WriteString("    end\n")
		return sb.String()
	}

	for _, container := range app.Containers {
		fmt.Fprintf(&sb, "        %s[\"%s\"]\n",
			ids.get(app.Name, container.Name), containerLabel(container.Name, app.Name))
	}
	for _, container := range app.Containers {
		for _, dep := range container.DependsOn {
			if !appDeclaresContainer(app, dep) {
				continue
			}
			fmt.Fprintf(&sb, "        %s --> %s\n",
				ids.get(app.Name, container.Name), ids.get(app.Name, dep))
		}
	}
	sb.WriteString("    end\n")
	return sb.String()
}

// containerIDs maps "app + container" to the mermaid node id assigned to it.
type containerIDs struct {
	byContainer map[string]string
}

func containerKey(appName, containerName string) string {
	return appName + "\x00" + containerName
}

// assignContainerIDs hands every container in the catalog a node id. The id
// is built from the app and the component the container names, which keeps
// the generated source readable, and the assignment is recorded, so an id
// that would collide with another app's node takes a numeric suffix instead
// of silently merging two containers into one box.
func assignContainerIDs(apps map[string]*AppMetadata) *containerIDs {
	ids := &containerIDs{byContainer: make(map[string]string)}
	owner := make(map[string]string)

	for _, appName := range sortedAppNames(apps) {
		app := apps[appName]
		if len(app.Containers) == 0 {
			ids.assign(owner, appName, appName, "c_"+idPart(appName))
			continue
		}
		for _, container := range app.Containers {
			ids.assign(owner, appName, container.Name, containerNodeID(appName, container.Name))
		}
	}
	return ids
}

// assign records one node id, suffixing it if the base id is taken.
func (c *containerIDs) assign(owner map[string]string, appName, containerName, base string) {
	key := containerKey(appName, containerName)
	id := base
	for n := 2; owner[id] != "" && owner[id] != key; n++ {
		id = fmt.Sprintf("%s_%d", base, n)
	}
	owner[id] = key
	c.byContainer[key] = id
}

// get returns the node id assigned to one container.
func (c *containerIDs) get(appName, containerName string) string {
	return c.byContainer[containerKey(appName, containerName)]
}

// graphEdge is one rendered cross-app edge between two app boxes.
type graphEdge struct {
	from  string
	to    string
	label string
}

// integrationEdges derives the cross-app edges from every app's integrations
// block. The direction and the labels follow the developer graph: an sso
// edge carries the app's SSO strategy rather than the literal "sso", and a
// proxy edge points from the proxy to the app it routes.
func integrationEdges(apps map[string]*AppMetadata) []graphEdge {
	seen := make(map[string]bool)
	var edges []graphEdge

	for _, appName := range sortedKeys(apps) {
		app := apps[appName]
		for _, integrationName := range sortedKeys(app.Integrations) {
			integration := app.Integrations[integrationName]
			provider := defaultProvider(integration)
			if provider == "" || provider == appName || apps[provider] == nil {
				continue
			}

			label := integrationName
			if integrationName == "sso" {
				label = ssoEdgeLabel(app)
			}

			from, to := appBoxID(appName), appBoxID(provider)
			if integrationName == "proxy" {
				from, to = to, from
			}

			key := from + "->" + to + "|" + label
			if seen[key] {
				continue
			}
			seen[key] = true
			edges = append(edges, graphEdge{from: from, to: to, label: label})
		}
	}

	sort.Slice(edges, func(i, j int) bool {
		if edges[i].from != edges[j].from {
			return edges[i].from < edges[j].from
		}
		if edges[i].to != edges[j].to {
			return edges[i].to < edges[j].to
		}
		return edges[i].label < edges[j].label
	})
	return edges
}

// defaultProvider returns the app an integration resolves to by default:
// the entry flagged default, or the first compatible app when none is.
func defaultProvider(integration Integration) string {
	for _, compat := range integration.Compatible {
		if compat.Default {
			return compat.App
		}
	}
	if len(integration.Compatible) > 0 {
		return integration.Compatible[0].App
	}
	return ""
}

// ssoEdgeLabel is the strategy name for an sso edge, falling back to "sso"
// when the app declares no strategy.
func ssoEdgeLabel(app *AppMetadata) string {
	if app.SSO.Strategy == "" {
		return "sso"
	}
	return app.SSO.Strategy
}

// appDeclaresContainer reports whether the app declares a container by name,
// so a dependsOn on something outside the app never draws a dangling arrow.
func appDeclaresContainer(app *AppMetadata, name string) bool {
	for _, container := range app.Containers {
		if container.Name == name {
			return true
		}
	}
	return false
}

// nonIDChar matches anything mermaid cannot use inside an identifier.
var nonIDChar = regexp.MustCompile(`[^A-Za-z0-9_]`)

// idPart turns an app or container name into a mermaid-safe identifier part.
func idPart(s string) string {
	return nonIDChar.ReplaceAllString(s, "_")
}

// appBoxID is the subgraph id for an app.
func appBoxID(appName string) string {
	return "app_" + idPart(appName)
}

// containerNodeID is the node id for one container of an app. The app's own
// single container keeps just the app name; a component gets the app name
// and its own.
func containerNodeID(appName, containerName string) string {
	label := containerLabel(containerName, appName)
	if label == appName {
		return "c_" + idPart(appName)
	}
	return "c_" + idPart(appName) + "_" + idPart(label)
}

// containerLabel shortens a runtime container name ("apps-immich-postgres")
// to the component it names ("postgres"), the same way the developer graph
// labels it. The app's own single container keeps the app's name.
func containerLabel(containerName, appID string) string {
	short := strings.TrimPrefix(strings.TrimPrefix(containerName, "apps-"+appID), "-")
	short = strings.TrimPrefix(short, "apps-")
	if short == "" {
		return appID
	}
	return short
}

// sortedAppNames orders apps for the diagram: system apps first, then user
// apps, each alphabetically. That puts the infrastructure the rest depends
// on at the top of the rendered graph.
func sortedAppNames(apps map[string]*AppMetadata) []string {
	var systemApps, userApps []string
	for name, app := range apps {
		if app.IsSystem {
			systemApps = append(systemApps, name)
		} else {
			userApps = append(userApps, name)
		}
	}
	sort.Strings(systemApps)
	sort.Strings(userApps)
	return append(systemApps, userApps...)
}

// sortedKeys returns a map's keys sorted for deterministic output.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
