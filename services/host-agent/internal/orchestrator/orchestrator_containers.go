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
		defs := catalogApp.ContainerDefs()
		// Skip apps with no container definitions or multi-container apps
		// (multi-container lifecycle is tracked via graph events, not this path).
		if err != nil || len(defs) != 1 {
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

// RegenerateRoutes rebuilds the Traefik dynamic config for all installed apps,
// manages the gateway, and reconciles remote proxies.
// It is a no-op when traefikGen is not configured.
func (o *Orchestrator) RegenerateRoutes() error {
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

	// Ensure gateway is running whenever a tailnet is active. The gateway
	// provides a SOCKS5 proxy so remote apps (shared from other hosts) can
	// be proxied through Traefik to the LAN.
	if o.gateway != nil && o.activeTailnetID != nil && o.activeTailnetID() != "" {
		if err := o.gateway.EnsureRunning(context.Background()); err != nil {
			o.logger.Warn("gateway not available", "error", err)
		}
	}

	// Build remote app routes if store is available.
	var remoteRoutes []traefikgen.RemoteAppRoute
	if o.remoteAppStore != nil {
		remoteApps, err := o.remoteAppStore.List()
		if err != nil {
			o.logger.Warn("failed to list remote apps for route generation", "error", err)
		} else {
			// Build proxy targets for reconciliation.
			var targets []sharing.ProxyTarget
			for _, ra := range remoteApps {
				targets = append(targets, sharing.ProxyTarget{
					ID:         ra.AppID + "-" + slug.Slugify(ra.HostLabel),
					TailnetURL: "https://" + ra.TailnetAddr,
				})
			}

			// Reconcile reverse proxies — returns port assignments.
			if o.remoteProxy != nil && len(targets) > 0 {
				portMap := o.remoteProxy.Reconcile(targets)
				for _, t := range targets {
					if port, ok := portMap[t.ID]; ok {
						remoteRoutes = append(remoteRoutes, traefikgen.RemoteAppRoute{
							ID:       t.ID,
							ProxyURL: fmt.Sprintf("http://localhost:%d", port),
						})
					}
				}
			} else if o.remoteProxy != nil {
				// No targets — stop all proxies.
				o.remoteProxy.Reconcile(nil)
			}
		}
	}

	// Discover tailnet domain for tailnet-specific routes (forward-auth via
	// the standalone proxy outpost). Only available when the gateway is running.
	var tailnetDomain string
	if o.gateway != nil && o.activeTailnetID != nil && o.activeTailnetID() != "" {
		if domain, err := o.gateway.GetTailnetDomain(context.Background()); err == nil {
			tailnetDomain = domain
		}
	}

	return o.traefikGen.GenerateAll(apps, remoteRoutes, tailnetDomain)
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

	// Use Network (singular) if set; fall back to first entry of Networks (plural).
	network := def.Network
	if network == "" && len(def.Networks) > 0 {
		network = def.Networks[0]
	}

	env := make(map[string]string, len(def.Environment))
	for k, v := range def.Environment {
		env[k] = render(v)
	}

	spec := containerruntime.Spec{
		Name:          def.Name,
		Image:         def.Image,
		Environment:   env,
		Network:       network,
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
