package orchestrator

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/slug"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

// SSOProvisioner provisions per-app SSO in the identity provider (e.g. Authentik).
// Implementations must be idempotent — called on every ensureApp, not just first install.
type SSOProvisioner interface {
	// EnsureForwardAuth creates or verifies the proxy provider + application for a
	// forward-auth app, and adds it to the embedded outpost.
	EnsureForwardAuth(appName, displayName, externalURL string) error
}

// PortableConfig contains the runtime-neutral dependencies used by PortableOrchestrator.
type PortableConfig struct {
	Graph           catalog.AppGraphInterface
	CatalogCache    catalog.CacheInterface
	AppStore        store.AppStoreInterface
	Containers      containerruntime.Runtime
	Registry        configurator.RegistryInterface
	TraefikGen      traefikgen.GeneratorInterface
	LDAPOutput      *configurator.LDAPOutput
	TailnetNode     sharing.TailnetNodeManagerInterface // optional; nil when Tailscale auth key is not set
	Gateway         sharing.GatewayManagerInterface // optional; manages gateway Tailscale container for remote app proxying
	RemoteProxy     *sharing.RemoteProxyManager     // optional; manages per-remote-app reverse proxies through SOCKS5 gateway
	RemoteAppStore  store.RemoteAppStoreInterface   // optional; nil when remote apps are not configured
	ActiveTailnetID func() string                   // returns the active tailnet connection ID (empty if none)
	DataDir         string
	TemplateVars    map[string]string // extra variables for container spec rendering (e.g. postgresPassword)
	Logger          *slog.Logger
}

// PortableOrchestrator converges app lifecycle through portable runtime contracts.
type PortableOrchestrator struct {
	graph           catalog.AppGraphInterface
	catalog         catalog.CacheInterface
	appStore        store.AppStoreInterface
	containers      containerruntime.Runtime
	registry        configurator.RegistryInterface
	traefikGen      traefikgen.GeneratorInterface
	ldapOutput      *configurator.LDAPOutput
	tailnetNode     sharing.TailnetNodeManagerInterface
	gateway         sharing.GatewayManagerInterface
	remoteProxy     *sharing.RemoteProxyManager
	remoteAppStore  store.RemoteAppStoreInterface
	activeTailnetID func() string
	dataDir         string
	templateVars    map[string]string
	logger          *slog.Logger
}

func NewPortable(cfg PortableConfig) *PortableOrchestrator {
	return &PortableOrchestrator{
		graph:           cfg.Graph,
		catalog:         cfg.CatalogCache,
		appStore:        cfg.AppStore,
		containers:      cfg.Containers,
		registry:        cfg.Registry,
		traefikGen:      cfg.TraefikGen,
		ldapOutput:      cfg.LDAPOutput,
		tailnetNode:     cfg.TailnetNode,
		gateway:         cfg.Gateway,
		remoteProxy:     cfg.RemoteProxy,
		remoteAppStore:  cfg.RemoteAppStore,
		activeTailnetID: cfg.ActiveTailnetID,
		dataDir:         cfg.DataDir,
		templateVars:    cfg.TemplateVars,
		logger:          cfg.Logger,
	}
}

// SyncContainerState aligns DB state with actual container reality on startup.
// If a container was killed externally while the host-agent was down, the DB
// still shows "running". This method inspects each container and corrects the DB.
func (o *PortableOrchestrator) SyncContainerState(ctx context.Context) {
	apps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Error("failed to load apps for container sync", "error", err)
		return
	}

	for _, app := range apps {
		catalogApp, err := o.catalog.Get(app.CatalogID)
		if err != nil || catalogApp.Container == nil {
			continue
		}

		containerName := PortableContainerName(catalogApp)
		state, err := o.containers.Inspect(ctx, containerName)
		if err != nil {
			o.logger.Warn("failed to inspect container during sync", "app", app.CatalogID, "error", err)
			continue
		}

		switch {
		case app.Status == "uninstalling" && !state.Exists:
			// Container gone + was uninstalling → clean up DB
			o.logger.Info("cleaning up uninstalled app", "app", app.CatalogID)
			_ = o.appStore.Uninstall(app.CatalogID)

		case (app.Status == "installing" || app.Status == "starting") && !state.Running:
			// Interrupted transition → mark error
			o.logger.Info("marking interrupted app as error", "app", app.CatalogID, "previous_status", app.Status)
			_ = o.appStore.UpdateStatus(app.CatalogID, "error")

		case app.Status == "running" && !state.Exists:
			// Container gone entirely → mark stopped so reconciler re-creates it
			o.logger.Info("container gone, marking as stopped", "app", app.CatalogID)
			_ = o.appStore.UpdateStatus(app.CatalogID, "stopped")

		case (app.Status == "error" || app.Status == "stopped") && state.Running:
			// Container recovered or was started externally → mark running
			o.logger.Info("container running, marking as running", "app", app.CatalogID, "previous_status", app.Status)
			_ = o.appStore.UpdateStatus(app.CatalogID, "running")
		}
	}

	o.logger.Info("container state sync completed")
}

// ReconcileState regenerates routes after a process restart.
// App lifecycle is now driven by the Orchestrator via graph target updates.
func (o *PortableOrchestrator) ReconcileState(ctx context.Context) {
	if err := o.RegenerateRoutes(); err != nil {
		o.logger.Warn("failed to regenerate routes during reconcile", "error", err)
	}
}

func (o *PortableOrchestrator) RegenerateRoutes() error {
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


// PortableContainerName returns the container name for a catalog app.
func PortableContainerName(app *catalog.App) string {
	if app.Container.Name != "" {
		return app.Container.Name
	}
	return "apps-" + app.CatalogID
}

// PortableContainerSpec builds a runtime-neutral container spec from catalog metadata.
// extraVars supplies additional template variables (e.g. "postgresPassword") beyond
// the built-in {{dataDir}} and {{appDataDir}}.
func PortableContainerSpec(app *catalog.App, dataDir string, extraVars map[string]string) (containerruntime.Spec, error) {
	if app.Container == nil || app.Container.Image == "" {
		return containerruntime.Spec{}, fmt.Errorf("app %s has no portable container image", app.CatalogID)
	}

	render := func(value string) string {
		value = strings.ReplaceAll(value, "{{dataDir}}", dataDir)
		value = strings.ReplaceAll(value, "{{appDataDir}}", filepath.Join(dataDir, app.CatalogID))
		for k, v := range extraVars {
			value = strings.ReplaceAll(value, "{{"+k+"}}", v)
		}
		return value
	}

	env := make(map[string]string, len(app.Container.Environment))
	for k, v := range app.Container.Environment {
		env[k] = render(v)
	}

	spec := containerruntime.Spec{
		Name:          PortableContainerName(app),
		Image:         app.Container.Image,
		Environment:   env,
		Network:       app.Container.Network,
		Command:       app.Container.Command,
		RestartPolicy: app.Container.RestartPolicy,
		Labels:        map[string]string{"io.bloud.app": app.CatalogID},
	}
	for _, port := range app.Container.Ports {
		spec.Ports = append(spec.Ports, containerruntime.Port{
			Host: port.Host, Container: port.Container, Protocol: port.Protocol,
		})
	}
	for _, volume := range app.Container.Volumes {
		spec.Mounts = append(spec.Mounts, containerruntime.Mount{
			Source: render(volume.Source), Destination: volume.Destination, Options: volume.Options,
		})
	}
	return spec, nil
}
