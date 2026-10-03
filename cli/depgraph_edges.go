// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"sort"
)

// graphEdge is one cross-app edge between two apps, named by catalog id. The
// mermaid renderer wraps both ends in their box ids; the JSON renderer emits
// the ids as they are, which is what the dashboard's graph nodes are keyed by.
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
			if provider == "" || provider == appName || !providerIsDrawn(apps, provider) {
				continue
			}

			label := integrationName
			if integrationName == "sso" {
				label = ssoEdgeLabel(app)
			}

			from, to := appName, provider
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

// defaultProvider returns the provider an integration resolves to by
// default: the entry flagged default, or the first compatible entry when
// none is. The value is a graph node id, so an instance-source entry comes
// back as the AI Model node rather than as an empty app name.
func defaultProvider(integration Integration) string {
	for _, compat := range integration.Compatible {
		if compat.Default {
			return providerNodeID(compat)
		}
	}
	if len(integration.Compatible) > 0 {
		return providerNodeID(integration.Compatible[0])
	}
	return ""
}

// providerNodeID maps one compatible entry to the graph node that stands for
// it. `source: instance` is not a catalog app, so it maps to the node the
// instance provides instead of to a name nothing can look up.
func providerNodeID(compat CompatibleApp) string {
	if compat.Source != "" {
		return instanceProviderNodeID
	}
	return compat.App
}

// providerIsDrawn reports whether a resolved provider is something the graph
// can draw an edge to. The instance provider always is; a catalog app only
// when the catalog actually has it, so an integration naming an app that is
// not shipped drops its edge instead of inventing a node.
func providerIsDrawn(apps map[string]*AppMetadata, provider string) bool {
	return provider == instanceProviderNodeID || apps[provider] != nil
}

// edgeEnd resolves one end of a cross-app edge to its mermaid id. An app is
// its box; the instance provider is a standalone node, because there is no
// app box to put it in.
func edgeEnd(endpoint string) string {
	if endpoint == instanceProviderNodeID {
		return instanceProviderMermaid
	}
	return appBoxID(endpoint)
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
