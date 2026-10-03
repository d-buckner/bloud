// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// resetSSONodes resets every RUNNING node that depends on the host set back to
// INITIALIZING: all containers of installed native-oidc / forward-auth apps
// (their configs and SSO providers bake in host URLs) plus the Authentik
// server container (its PostStart sets the embedded outpost's browser URL).
func (o *Orchestrator) resetSSONodes() {
	if o.graph == nil || o.appStore == nil {
		return
	}
	for _, nodeID := range o.ssoDependentNodes() {
		node, err := o.graph.GetNode(nodeID)
		if err != nil || node == nil || node.ActualStatus != graph.StatusRunning {
			continue
		}
		o.logger.Info("resetting SSO-dependent node for host change", "node", nodeID)
		_ = o.graph.SetActualStatus(nodeID, graph.StatusInitializing, "")
	}
}

// ssoDependentNodes lists the nodes whose config or provider bakes in the host
// set: the Authentik server container, plus every container of each installed
// app that joined the identity provider.
func (o *Orchestrator) ssoDependentNodes() []string {
	var nodes []string
	// Authentik server: PostStart re-applies the embedded outpost host URL.
	if _, err := o.appStore.GetByCatalogID("authentik"); err == nil {
		nodes = append(nodes, "apps-authentik-server")
	}
	apps, err := o.appStore.GetAll()
	if err != nil {
		return nodes
	}
	for _, app := range apps {
		nodes = append(nodes, o.ssoAppNodes(app)...)
	}
	return nodes
}

// ssoAppNodes returns the container nodes of one installed app when its SSO
// strategy makes it host-dependent, or the app's own node when it declares no
// containers of its own.
func (o *Orchestrator) ssoAppNodes(app *store.InstalledApp) []string {
	if app.IsSystem || o.catalog == nil {
		return nil
	}
	catalogApp, err := o.catalog.Get(app.CatalogID)
	if err != nil || catalogApp == nil {
		return nil
	}
	switch catalogApp.SSO.Strategy {
	case "native-oidc", "forward-auth":
	default:
		return nil
	}
	if len(catalogApp.Containers) == 0 {
		return []string{app.CatalogID}
	}
	nodes := make([]string, 0, len(catalogApp.Containers))
	for _, def := range catalogApp.Containers {
		nodes = append(nodes, def.Name)
	}
	return nodes
}
