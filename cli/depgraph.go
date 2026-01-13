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
	var appNames []string
	var systemApps []string
	var userApps []string
	for name := range apps {
		appNames = append(appNames, name)
		if apps[name].IsSystem {
			systemApps = append(systemApps, name)
		} else {
			userApps = append(userApps, name)
		}
	}
	sort.Strings(appNames)
	sort.Strings(systemApps)
	sort.Strings(userApps)

	// Track which edges we've added to avoid duplicates
	edges := make(map[string]bool)
	// Track apps that appear in edges
	appsInEdges := make(map[string]bool)
	// Collect edges to write after subgraph
	var edgeLines []string

	// Generate edges for each app's integrations
	for _, appName := range appNames {
		app := apps[appName]

		// Sort integration names for consistent output
		var integrationNames []string
		for intName := range app.Integrations {
			integrationNames = append(integrationNames, intName)
		}
		sort.Strings(integrationNames)

		for _, intName := range integrationNames {
			integration := app.Integrations[intName]
			for _, compat := range integration.Compatible {
				// Edge: app depends on compatible app
				edgeKey := fmt.Sprintf("%s->%s", appName, compat.App)
				if edges[edgeKey] {
					continue
				}
				edges[edgeKey] = true
				appsInEdges[appName] = true
				appsInEdges[compat.App] = true

				label := intName
				if integration.Required {
					label += "*"
				}
				edgeLines = append(edgeLines, fmt.Sprintf("    %s -->|%s| %s", appName, label, compat.App))
			}
		}
	}

	// Add implicit SSO edges for apps using authentik
	for _, appName := range appNames {
		app := apps[appName]
		if app.SSO.Strategy == "forward-auth" || app.SSO.Strategy == "native-oidc" {
			edgeKey := fmt.Sprintf("%s->authentik", appName)
			if !edges[edgeKey] {
				edges[edgeKey] = true
				appsInEdges[appName] = true
				appsInEdges["authentik"] = true
				edgeLines = append(edgeLines, fmt.Sprintf("    %s -->|sso| authentik", appName))
			}
		}
	}

	// Add implicit traefik edges for web-routed apps (exclude infrastructure)
	for _, appName := range appNames {
		app := apps[appName]
		if appName == "traefik" || app.Category == "infrastructure" {
			continue
		}
		edgeKey := fmt.Sprintf("%s->traefik", appName)
		if !edges[edgeKey] {
			edges[edgeKey] = true
			appsInEdges[appName] = true
			appsInEdges["traefik"] = true
			edgeLines = append(edgeLines, fmt.Sprintf("    %s -->|routing| traefik", appName))
		}
	}

	// Write user apps subgraph (above system)
	if len(userApps) > 0 {
		sb.WriteString("    subgraph Apps\n")
		for _, appName := range userApps {
			sb.WriteString(fmt.Sprintf("        %s\n", appName))
		}
		sb.WriteString("    end\n")
	}

	// Write system apps subgraph (includes host-agent)
	sb.WriteString("    subgraph System\n")
	sb.WriteString("        host-agent\n")
	for _, appName := range systemApps {
		sb.WriteString(fmt.Sprintf("        %s\n", appName))
	}
	sb.WriteString("    end\n")

	// Add host-agent's database dependency
	edgeLines = append(edgeLines, "    host-agent -->|database*| postgres")

	// Write all edges
	for _, edge := range edgeLines {
		sb.WriteString(edge + "\n")
	}

	sb.WriteString("```\n")
	sb.WriteString("\n_* = required integration_\n")

	return sb.String()
}
