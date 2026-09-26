// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

// Container runtime utilities: container-state sync, route regeneration, and
// catalog-to-spec helpers. All methods are on *Orchestrator.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/slug"
)

// SyncContainerState aligns DB state and the lifecycle graph with actual
// container reality. It runs on every convergence pass, so a container
// killed externally (OOM, `podman rm`) while host-agent was up is repaired
// without a restart, not just one killed while it was down.
//
// Two corrections happen here, and the second is the one that matters. The
// store correction records that the app is not up. The graph correction puts
// the node back on the lifecycle path: without it the node still reads
// RUNNING, target equals actual, `collectWorkForLevel` finds nothing to do,
// and the app stays dead forever. It is a no-op when the container runtime
// or app store is not configured.
func (o *Orchestrator) SyncContainerState(ctx context.Context) {
	if o.config.Containers == nil || o.appStore == nil || o.catalog == nil {
		return
	}

	apps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Error("failed to load apps for container sync", "error", err)
		return
	}

	for _, app := range apps {
		if app.IsSystem {
			continue
		}
		o.syncAppContainers(ctx, app)
	}

	o.logger.Info("container state sync completed")
}

// syncAppContainers aligns one installed app's store row and its graph
// nodes with the containers it declares in the catalog.
func (o *Orchestrator) syncAppContainers(ctx context.Context, app *store.InstalledApp) {
	catalogApp, err := o.catalog.Get(app.CatalogID)
	if err != nil || catalogApp == nil {
		// An installed row with no catalog entry: the app's directory
		// was removed or renamed (the loader skips dirs without
		// metadata.yaml). There is no recover() anywhere in host-agent,
		// so dereferencing the nil result here kills the daemon on the
		// next convergence pass. Skip instead.
		o.logger.Warn("installed app missing from catalog, skipping container sync",
			"app", app.CatalogID, "error", err)
		return
	}
	defs := catalogApp.ContainerDefs()
	if len(defs) == 0 {
		return
	}

	states := o.inspectContainers(ctx, app.CatalogID, defs)
	if len(states) == 0 {
		return
	}

	// An app being uninstalled is never re-driven: its containers are on
	// the way out, and re-creating them here would fight the removal.
	if app.Status == "uninstalling" {
		if containersAllGone(states) {
			o.logger.Info("cleaning up uninstalled app", "app", app.CatalogID)
			_ = o.appStore.Uninstall(app.CatalogID)
		}
		return
	}

	switch {
	case app.Status == "running" && !containersAllRunning(states):
		// Reality disagrees with the store: the app is not up.
		o.logger.Info("container gone, marking as stopped", "app", app.CatalogID)
		_ = o.appStore.UpdateStatus(app.CatalogID, "stopped")

	case app.Status == "stopped" && containersAllRunning(states):
		// Container recovered externally after a clean stop → mark running.
		// "stopped" only applies to apps that previously completed full lifecycle,
		// so no lifecycle re-run is needed.
		o.logger.Info("container recovered, marking as running", "app", app.CatalogID)
		_ = o.appStore.UpdateStatus(app.CatalogID, "running")
	}

	for _, def := range defs {
		if state, ok := states[def.Name]; ok {
			o.repairDriftedNode(def.Name, state)
		}
	}
}

// inspectContainers reads the live state of every container an app declares,
// keyed by container name. A container that cannot be inspected is left out
// of the result rather than guessed at: an unknown state must not be treated
// as either present or absent.
func (o *Orchestrator) inspectContainers(ctx context.Context, appID string, defs []catalog.ContainerDef) map[string]containerruntime.State {
	states := make(map[string]containerruntime.State, len(defs))
	for _, def := range defs {
		state, err := o.config.Containers.Inspect(ctx, def.Name)
		if err != nil {
			o.logger.Warn("failed to inspect container during sync",
				"app", appID, "container", def.Name, "error", err)
			continue
		}
		states[def.Name] = state
	}
	return states
}

func containersAllGone(states map[string]containerruntime.State) bool {
	for _, state := range states {
		if state.Exists {
			return false
		}
	}
	return true
}

func containersAllRunning(states map[string]containerruntime.State) bool {
	for _, state := range states {
		if !state.Running {
			return false
		}
	}
	return true
}

// repairDriftedNode puts a node whose recorded status disagrees with the
// container runtime back on the lifecycle path. A node that claims RUNNING
// while its container is not running is reset to INITIALIZING, which makes
// target differ from actual so the normal path re-runs the full lifecycle
// this pass. Safety rests on invariant 2: configurators are idempotent, so a
// re-drive is always safe.
//
// Only a RUNNING node is a drift candidate. ERROR is terminal by design and
// must not be silently retried, and any intermediate status means a drive is
// already in flight. Returns true when a reset was issued.
func (o *Orchestrator) repairDriftedNode(nodeID string, state containerruntime.State) bool {
	if state.Running {
		return false
	}
	node, err := o.graph.GetNode(nodeID)
	if err != nil || node == nil {
		// No node yet: populateGraphNodes creates it at INITIALIZING, which
		// is already the state that gets driven.
		return false
	}
	if node.ActualStatus != graph.StatusRunning {
		return false
	}
	o.logger.Info("container drift detected, re-driving lifecycle",
		"node", nodeID, "container_exists", state.Exists)
	_ = o.graph.SetActualStatus(nodeID, graph.StatusInitializing,
		"container not running while node was marked RUNNING")
	return true
}

// tailnetActive reports whether a tailnet is currently connected.
func (o *Orchestrator) tailnetActive() bool {
	return o.activeTailnetID != nil && o.activeTailnetID() != ""
}

// SyncRoutes is the full route-sync entry point. The runtime steps the
// route config depends on run first, as explicit named steps: bring up
// the gateway (best-effort), reconcile the remote-app reverse proxies,
// and discover the tailnet domain. Only then is the pure config write
// performed. Callers use this; RegenerateRoutes itself never touches
// the runtime.
func (o *Orchestrator) SyncRoutes() error {
	o.ensureGateway()
	remoteRoutes := o.reconcileRemoteProxies()
	tailnetDomain := o.resolveTailnetDomain()
	return o.RegenerateRoutes(remoteRoutes, tailnetDomain)
}

// ensureGateway brings up the tailnet gateway when a tailnet is active.
// The gateway provides the SOCKS5 proxy that lets remote apps (shared
// from other hosts) be proxied through Traefik to the LAN. Best-effort:
// an unavailable gateway is logged, not fatal: routes for local apps
// are still written.
func (o *Orchestrator) ensureGateway() {
	if o.gateway == nil || !o.tailnetActive() {
		return
	}
	if err := o.gateway.EnsureRunning(context.Background()); err != nil {
		o.logger.Warn("gateway not available", "error", err)
	}
}

// resolveTailnetDomain discovers the tailnet MagicDNS domain used for
// tailnet-specific routes (forward-auth via the standalone proxy
// outpost). Only meaningful while the gateway runs; returns "" otherwise.
func (o *Orchestrator) resolveTailnetDomain() string {
	if o.gateway == nil || !o.tailnetActive() {
		return ""
	}
	domain, err := o.gateway.GetTailnetDomain(context.Background())
	if err != nil {
		return ""
	}
	return domain
}

// RegenerateRoutes writes the Traefik dynamic config for all installed
// apps. Pure with respect to the runtime: it starts nothing and mutates
// no proxies. Everything runtime-shaped that the config depends on
// (remote proxy port assignments, the tailnet domain) is passed in by
// the caller (see SyncRoutes). No-op when traefikGen is not configured.
func (o *Orchestrator) RegenerateRoutes(remoteRoutes []traefikgen.RemoteAppRoute, tailnetDomain string) error {
	if o.traefikGen == nil {
		return nil
	}
	names, err := o.appStore.GetInstalledCatalogIDs()
	if err != nil {
		return err
	}
	apps := make([]*catalog.App, 0, len(names))
	authentikEnabled := false
	for _, name := range names {
		app, err := o.catalog.Get(name)
		if err != nil {
			continue
		}
		apps = append(apps, app)
		authentikEnabled = authentikEnabled || name == "authentik"
	}
	o.traefikGen.SetAuthentikEnabled(authentikEnabled)
	return o.traefikGen.GenerateAll(apps, remoteRoutes, tailnetDomain)
}

// reconcileRemoteProxies reconciles the reverse proxies for remote
// (shared) apps (a runtime mutation) and translates the resulting
// port assignments into Traefik routes. Returns nil when no remote app
// store is configured.
func (o *Orchestrator) reconcileRemoteProxies() []traefikgen.RemoteAppRoute {
	if o.remoteAppStore == nil {
		return nil
	}
	remoteApps, err := o.remoteAppStore.List()
	if err != nil {
		o.logger.Warn("failed to list remote apps for route generation", "error", err)
		return nil
	}

	// Build proxy targets for reconciliation.
	var targets []sharing.ProxyTarget
	for _, ra := range remoteApps {
		targets = append(targets, sharing.ProxyTarget{
			ID:         ra.AppID + "-" + slug.Slugify(ra.HostLabel),
			TailnetURL: "https://" + ra.TailnetAddr,
		})
	}

	if o.remoteProxy == nil {
		return nil
	}

	// Reconcile reverse proxies: returns port assignments. With no
	// targets this stops all proxies.
	portMap := o.remoteProxy.Reconcile(targets)
	var remoteRoutes []traefikgen.RemoteAppRoute
	for _, t := range targets {
		if port, ok := portMap[t.ID]; ok {
			remoteRoutes = append(remoteRoutes, traefikgen.RemoteAppRoute{
				ID:       t.ID,
				ProxyURL: fmt.Sprintf("http://localhost:%d", port),
			})
		}
	}
	return remoteRoutes
}

// ContainerSpecFromDef builds a container spec from a ContainerDef.
// appCatalogID is the owning app's catalog ID, used for the io.bloud.app label
// and for resolving {{appDataDir}}.
// vars supplies the template variables beyond {{dataDir}} and {{appDataDir}};
// nil means none. It is read once into a snapshot, so a value another
// goroutine is still writing cannot change underneath the render.
func ContainerSpecFromDef(def catalog.ContainerDef, appCatalogID string, dataDir string, vars *configurator.TemplateVars) (containerruntime.Spec, error) {
	if def.Image == "" {
		return containerruntime.Spec{}, fmt.Errorf("container %q has no image", def.Name)
	}

	extraVars := vars.Snapshot()

	render := func(value string) string {
		value = strings.ReplaceAll(value, "{{dataDir}}", dataDir)
		value = strings.ReplaceAll(value, "{{appDataDir}}", filepath.Join(dataDir, appCatalogID))
		for k, v := range extraVars {
			value = strings.ReplaceAll(value, "{{"+k+"}}", v)
		}
		return value
	}

	// Collect all networks the container should attach to, preserving the
	// singular Network (if set) before the plural Networks list.
	var networks []string
	if def.Network != "" {
		networks = append(networks, def.Network)
	}
	for _, n := range def.Networks {
		if n != def.Network {
			networks = append(networks, n)
		}
	}

	env := make(map[string]string, len(def.Environment))
	for k, v := range def.Environment {
		env[k] = render(v)
	}

	spec := containerruntime.Spec{
		Name:          def.Name,
		Image:         def.Image,
		Environment:   env,
		ExtraHosts:    def.ExtraHosts,
		Networks:      networks,
		Command:       def.Command,
		RestartPolicy: def.RestartPolicy,
		Labels:        map[string]string{"io.bloud.app": appCatalogID},
	}
	for _, port := range def.Ports {
		spec.Ports = append(spec.Ports, containerruntime.Port{
			Host: port.Host, Container: port.Container, Protocol: port.Protocol,
		})
	}
	for _, volume := range def.Volumes {
		spec.Mounts = append(spec.Mounts, containerruntime.Mount{
			Source: render(volume.Source), Destination: volume.Destination, Options: volume.Options,
		})
	}
	return spec, nil
}
