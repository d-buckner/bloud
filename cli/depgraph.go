// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// AppMetadata represents the relevant fields from metadata.yaml for dependency graphing
type AppMetadata struct {
	Name         string                 `yaml:"name"`
	DisplayName  string                 `yaml:"displayName"`
	Description  string                 `yaml:"description"`
	Category     string                 `yaml:"category"`
	IsSystem     bool                   `yaml:"isSystem"`
	Integrations map[string]Integration `yaml:"integrations"`
	SSO          SSOConfig              `yaml:"sso"`
	Containers   []ContainerMetadata    `yaml:"containers"`
}

type SSOConfig struct {
	Strategy string `yaml:"strategy"`
}

// Integration defines how an app connects to other apps
type Integration struct {
	Required   bool            `yaml:"required"`
	Compatible []CompatibleApp `yaml:"compatible"`
}

// CompatibleApp defines a specific provider that can fulfill an integration.
// Exactly one of App and Source names it: App is a catalog app, Source is the
// instance's own settings (`instance`), which is not an app at all.
type CompatibleApp struct {
	App     string `yaml:"app"`
	Source  string `yaml:"source"`
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
// `--check`, so the rest of the file is untouched. The default target is the
// README: the diagram is Mermaid, which GitHub renders inline, so the bytes
// the generator produce are the picture readers see. Nothing renders it
// separately, so there is no committed image that can lag the catalog and no
// browser in the loop to keep working.
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
	// graphModeJSON emits the catalog in the shape the dashboard's
	// developer graph consumes.
	graphModeJSON
)

// catalogNodeStatus is the status a catalog snapshot carries. Nothing in the
// snapshot is running, so the dashboard's status color falls back to the
// neutral gray rather than implying a live state.
const catalogNodeStatus = "catalog"

// The instance's own AI settings get a graph node. Nothing in apps/
// metadata.yaml declares it: there is no install, no container, and no
// lifecycle for the orchestrator to order. It is the one node the instance
// itself provides, and an inference consumer's edge has to point somewhere.
//
// The catalog snapshot always carries it. The generated picture is the full
// view of everything Bloud can wire, configured or not, so the node is part
// of the topology a reader learns rather than something that appears only
// after someone fills in a form. The live dashboard graph applies its own
// rule and shows the node only once Settings -> AI has an upstream.
const (
	instanceProviderNodeID   = "ai:instance"
	instanceProviderLabel    = "AI Model"
	instanceProviderMermaid  = "ai_model"
	instanceProviderCategory = "ai"
	// instanceProviderStatus is what the node reads as: "external", not the
	// snapshot-wide "catalog". It names what the node is rather than that it
	// happens to be part of a catalog dump, and it is deliberately absent
	// from the dashboard's status color table so the dot stays the same
	// neutral gray as every other unprobed status.
	instanceProviderStatus = "external"
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
		case "--json":
			mode = graphModeJSON
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

	apps, ok := loadCatalog(root)
	if !ok {
		return 1
	}

	generated := renderDependencyGraph(apps)

	switch mode {
	case graphModeWrite:
		return writeGraphBlock(root, target, generated)
	case graphModeCheck:
		return checkGraphBlock(root, target, generated)
	case graphModeJSON:
		return printCatalogGraphJSON(apps)
	default:
		fmt.Print(generated)
		return 0
	}
}

// printCatalogGraphJSON emits the catalog in the shape the dashboard's own
// developer graph consumes: nodes and edges, not a Mermaid string.
func printCatalogGraphJSON(apps map[string]*AppMetadata) int {
	encoded, err := renderCatalogGraphJSON(apps)
	if err != nil {
		errorf("Failed to encode the catalog graph: %v", err)
		return 1
	}
	fmt.Print(encoded)
	return 0
}

// printDepGraphUsage documents the graph command's modes.
func printDepGraphUsage() {
	fmt.Println("Usage: bloud depgraph [--write | --check] [--target FILE]")
	fmt.Println()
	fmt.Println("  (no flags)   Print the full Mermaid dependency graph to stdout")
	fmt.Println("  --write      Replace the generated block in the target file")
	fmt.Println("  --check      Exit 1 when the target file's block is not what the")
	fmt.Println("               catalog produces right now (the PR-time gate)")
	fmt.Println("  --json       Print the whole catalog as the developer-graph JSON")
	fmt.Println("               the dashboard consumes (nodes + edges)")
	fmt.Println("  --target     File to write or check (default: " + graphDefaultFile + ")")
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
