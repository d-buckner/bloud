// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"path/filepath"
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

func cmdDepGraph() int {
	root, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	appsDir := filepath.Join(root, "apps")
	apps, err := loadAppMetadata(appsDir)
	if err != nil {
		errorf("Failed to load app metadata: %v", err)
		return 1
	}

	mermaid := generateMermaid(apps)
	fmt.Println(mermaid)
	return 0
}

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

func generateMermaid(apps map[string]*AppMetadata) string {
	var sb strings.Builder

	sb.WriteString("```mermaid\n")
	sb.WriteString("flowchart TD\n")

	// Collect all apps and sort for consistent output
	var systemApps []string
	var userApps []string
	for name := range apps {
		if apps[name].IsSystem {
			systemApps = append(systemApps, name)
		} else {
			userApps = append(userApps, name)
		}
	}
	sort.Strings(systemApps)
	sort.Strings(userApps)

	// Write user apps subgraph (above system)
	if len(userApps) > 0 {
		sb.WriteString("    subgraph Apps\n")
		for _, appName := range userApps {
			fmt.Fprintf(&sb, "        %s\n", appName)
		}
		sb.WriteString("    end\n")
	}

	// Write system apps subgraph (includes host-agent)
	sb.WriteString("    subgraph System\n")
	sb.WriteString("        host-agent\n")
	for _, appName := range systemApps {
		fmt.Fprintf(&sb, "        %s\n", appName)
	}
	sb.WriteString("    end\n")

	g := &graphBuilder{}
	g.addIntegrationEdges(apps)
	g.addSSOEdges(apps)
	g.addRoutingEdges(apps)
	g.edges = append(g.edges, "    host-agent -->|database*| postgres")
	for _, edge := range g.edges {
		sb.WriteString(edge + "\n")
	}

	sb.WriteString("```\n")
	sb.WriteString("\n_* = required integration_\n")

	return sb.String()
}

// graphBuilder accumulates mermaid edge lines, deduplicating by
// from->to so overlapping passes never emit the same edge twice.
type graphBuilder struct {
	seen  map[string]bool
	edges []string
}

// addEdge records "from -->|label| to" unless that pair was already added.
func (g *graphBuilder) addEdge(from, to, label string) {
	key := from + "->" + to
	if g.seen[key] {
		return
	}
	if g.seen == nil {
		g.seen = make(map[string]bool)
	}
	g.seen[key] = true
	g.edges = append(g.edges, fmt.Sprintf("    %s -->|%s| %s", from, label, to))
}

// addIntegrationEdges emits one edge per compatible app declared in each
// app's integrations block; required integrations get a "*" suffix.
func (g *graphBuilder) addIntegrationEdges(apps map[string]*AppMetadata) {
	for _, appName := range sortedKeys(apps) {
		app := apps[appName]
		// Sort integration names for consistent output
		for _, intName := range sortedKeys(app.Integrations) {
			integration := app.Integrations[intName]
			label := intName
			if integration.Required {
				label += "*"
			}
			for _, compat := range integration.Compatible {
				g.addEdge(appName, compat.App, label)
			}
		}
	}
}

// addSSOEdges emits implicit sso edges for apps whose SSO strategy is
// served by authentik (forward-auth and native-oidc).
func (g *graphBuilder) addSSOEdges(apps map[string]*AppMetadata) {
	for _, appName := range sortedKeys(apps) {
		switch apps[appName].SSO.Strategy {
		case "forward-auth", "native-oidc":
			g.addEdge(appName, "authentik", "sso")
		}
	}
}

// addRoutingEdges emits implicit routing edges to traefik for web-routed
// apps (everything except traefik itself and infrastructure apps).
func (g *graphBuilder) addRoutingEdges(apps map[string]*AppMetadata) {
	for _, appName := range sortedKeys(apps) {
		if appName == "traefik" || apps[appName].Category == "infrastructure" {
			continue
		}
		g.addEdge(appName, "traefik", "routing")
	}
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
