// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// populateGraphNodes creates graph nodes and edges for all installed apps.
// Apps with multiple containers (containers: list in metadata) are expanded into
// one node per container. Single-container apps retain a single node per app.
// Within-app dependsOn edges are wired from each container's DependsOn list.
// Inter-app edges connect from each app's primary container to the provider's
// primary container.
func (o *Orchestrator) populateGraphNodes(appMap map[string]*store.InstalledApp) {
	o.createGraphNodes(appMap)
	o.wireInAppEdges(appMap)
	o.wireInterAppEdges(appMap)
	o.setGraphTargets(appMap)
}

// containerDefsFor returns the catalog container defs for an installed app,
// and whether the app declares any containers (vs. legacy single-node).
func (o *Orchestrator) containerDefsFor(appName string) ([]catalog.ContainerDef, bool) {
	if o.catalog == nil {
		return nil, false
	}
	catalogApp, err := o.catalog.Get(appName)
	if err != nil || catalogApp == nil || len(catalogApp.Containers) == 0 {
		return nil, false
	}
	return catalogApp.ContainerDefs(), true
}

// Pass 1: create one node per container def for multi-container apps, and
// one with the catalog ID for legacy/no-container apps.
func (o *Orchestrator) createGraphNodes(appMap map[string]*store.InstalledApp) {
	for appName := range appMap {
		defs, hasCatalogContainers := o.containerDefsFor(appName)
		if !hasCatalogContainers {
			if existing, _ := o.graph.GetNode(appName); existing == nil {
				_ = o.graph.AddNode(appName)
			}
			continue
		}
		for _, def := range defs {
			if existing, _ := o.graph.GetNode(def.Name); existing == nil {
				_ = o.graph.AddNode(def.Name)
			}
			o.registerContainerOwner(def.Name, appName)
		}
	}
}

// Pass 2: wire within-app dependsOn edges for multi-container apps.
func (o *Orchestrator) wireInAppEdges(appMap map[string]*store.InstalledApp) {
	for appName := range appMap {
		defs, ok := o.containerDefsFor(appName)
		if !ok {
			continue
		}
		for _, def := range defs {
			for _, dep := range def.DependsOn {
				_ = o.graph.AddEdge(def.Name, dep)
			}
		}
	}
}

// Pass 3: wire inter-app dependency edges through each app's primary node.
func (o *Orchestrator) wireInterAppEdges(appMap map[string]*store.InstalledApp) {
	appDeps := computeAppDeps(appMap, o.catalog)
	for appName, deps := range appDeps {
		fromNode := o.primaryContainerNode(appName)
		for _, dep := range deps {
			toNode := o.primaryContainerNode(dep)
			_ = o.graph.AddEdge(fromNode, toNode)
		}
	}
}

// Pass 4: set every created node's target to RUNNING.
func (o *Orchestrator) setGraphTargets(appMap map[string]*store.InstalledApp) {
	for appName := range appMap {
		defs, ok := o.containerDefsFor(appName)
		if !ok {
			_ = o.graph.SetTargetStatus(appName, graph.StatusRunning)
			continue
		}
		for _, def := range defs {
			_ = o.graph.SetTargetStatus(def.Name, graph.StatusRunning)
		}
	}
}

// primaryContainerNode returns the graph node ID that represents the "entry point"
// for an app in inter-app dependency edges. For multi-container apps, returns the
// last container's name (the main service container). For single-container apps,
// returns the catalog ID itself.
func (o *Orchestrator) primaryContainerNode(appName string) string {
	if o.catalog == nil {
		return appName
	}
	catalogApp, err := o.catalog.Get(appName)
	if err != nil || catalogApp == nil {
		return appName
	}
	if len(catalogApp.Containers) == 0 {
		return appName
	}
	defs := catalogApp.ContainerDefs()
	return defs[len(defs)-1].Name
}

// computeAppDeps builds a map of app name → list of installed dependency names.
func computeAppDeps(apps map[string]*store.InstalledApp, catalogCache catalog.CacheInterface) map[string][]string {
	deps := make(map[string][]string)
	for name := range apps {
		if catalogCache == nil {
			continue
		}
		catalogApp, err := catalogCache.Get(name)
		if err != nil || catalogApp == nil {
			continue
		}
		// The set model: a dependency is every declared compatible provider
		// that is installed. `required` only installs the provider with the
		// consumer; it does not change which installed providers are ordered.
		seen := make(map[string]bool)
		for _, integration := range catalogApp.Integrations {
			for _, declared := range catalog.DeclaredProviders(integration) {
				// An instance provider has no container and therefore no node
				// to order. Filtering on the source rather than relying on
				// `apps[""]` missing keeps the invariant explicit: the graph
				// never gains a phantom node for a setting.
				if declared.Source != "" {
					continue
				}
				if _, installed := apps[declared.App]; !installed {
					continue
				}
				if seen[declared.App] {
					continue
				}
				seen[declared.App] = true
				deps[name] = append(deps[name], declared.App)
			}
		}
	}
	return deps
}
