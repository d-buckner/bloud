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
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/slug"
)

// SyncContainerState aligns DB state with actual container reality on startup.
// If a container was killed externally while the host-agent was down, the DB
// still shows "running". This method inspects each container and corrects the DB.
// It is a no-op when the container runtime or app store is not configured.
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
		catalogApp, err := o.catalog.Get(app.CatalogID)
		if err != nil || catalogApp == nil {
			// An installed row with no catalog entry: the app's directory
			// was removed or renamed (the loader skips dirs without
			// metadata.yaml). There is no recover() anywhere in host-agent,
			// so dereferencing the nil result here kills the daemon on the
			// next convergence pass. Skip instead.
			o.logger.Warn("installed app missing from catalog, skipping container sync",
				"app", app.CatalogID, "error", err)
			continue
		}
		defs := catalogApp.ContainerDefs()
		// Skip apps with no container definitions or multi-container apps
		// (multi-container lifecycle is tracked via graph events, not this path).
		if len(defs) != 1 {
			continue
		}

		containerName := defs[0].Name
		state, err := o.config.Containers.Inspect(ctx, containerName)
		if err != nil {
			o.logger.Warn("failed to inspect container during sync", "app", app.CatalogID, "error", err)
			continue
		}

		switch {
		case app.Status == "uninstalling" && !state.Exists:
			// Container gone + was uninstalling → clean up DB
			o.logger.Info("cleaning up uninstalled app", "app", app.CatalogID)
			_ = o.appStore.Uninstall(app.CatalogID)

		case app.Status == "running" && !state.Exists:
			// Container gone entirely → mark stopped so it can be re-created.
			o.logger.Info("container gone, marking as stopped", "app", app.CatalogID)
			_ = o.appStore.UpdateStatus(app.CatalogID, "stopped")

		case app.Status == "stopped" && state.Running:
			// Container recovered externally after a clean stop → mark running.
			// "stopped" only applies to apps that previously completed full lifecycle,
			// so no lifecycle re-run is needed.
			o.logger.Info("container recovered, marking as running", "app", app.CatalogID)
			_ = o.appStore.UpdateStatus(app.CatalogID, "running")
		}
	}

	o.logger.Info("container state sync completed")
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
// extraVars supplies additional template variables beyond {{dataDir}} and {{appDataDir}}.
func ContainerSpecFromDef(def catalog.ContainerDef, appCatalogID string, dataDir string, extraVars map[string]string) (containerruntime.Spec, error) {
	if def.Image == "" {
		return containerruntime.Spec{}, fmt.Errorf("container %q has no image", def.Name)
	}

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
