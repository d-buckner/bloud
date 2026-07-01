package orchestrator

import (
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/reconciler"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Compile-time assertion: PortableOrchestrator implements reconciler.AppLifecycleManager.
var _ reconciler.AppLifecycleManager = (*PortableOrchestrator)(nil)

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
	SSO             SSOProvisioner                  // optional; nil when Authentik is not available
	TailnetNode     sharing.TailnetNodeManagerInterface // optional; nil when Tailscale auth key is not set
	Gateway         sharing.GatewayManagerInterface // optional; manages gateway Tailscale container for remote app proxying
	RemoteProxy     *sharing.RemoteProxyManager     // optional; manages per-remote-app reverse proxies through SOCKS5 gateway
	RemoteAppStore  store.RemoteAppStoreInterface   // optional; nil when remote apps are not configured
	ActiveTailnetID func() string                   // returns the active tailnet connection ID (empty if none)
	SSOBaseURL      string                          // base URL for building app external URLs (e.g. "http://localhost:8080")
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
	sso             SSOProvisioner
	tailnetNode     sharing.TailnetNodeManagerInterface
	gateway         sharing.GatewayManagerInterface
	remoteProxy     *sharing.RemoteProxyManager
	remoteAppStore  store.RemoteAppStoreInterface
	activeTailnetID func() string
	ssoBaseURL      string
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
		sso:             cfg.SSO,
		tailnetNode:     cfg.TailnetNode,
		gateway:         cfg.Gateway,
		remoteProxy:     cfg.RemoteProxy,
		remoteAppStore:  cfg.RemoteAppStore,
		activeTailnetID: cfg.ActiveTailnetID,
		ssoBaseURL:      cfg.SSOBaseURL,
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

		case app.Status == "running" && (!state.Exists || !state.Running):
			// DB says running but container isn't → mark stopped
			o.logger.Info("container not running, marking as stopped", "app", app.CatalogID, "exists", state.Exists)
			_ = o.appStore.UpdateStatus(app.CatalogID, "stopped")

		case (app.Status == "error" || app.Status == "stopped") && state.Running:
			// Container recovered or was started externally → mark running
			o.logger.Info("container running, marking as running", "app", app.CatalogID, "previous_status", app.Status)
			_ = o.appStore.UpdateStatus(app.CatalogID, "running")
		}
	}

	o.logger.Info("container state sync completed")
}

// ReconcileState converges all durable installed-app state after process restart.
func (o *PortableOrchestrator) ReconcileState(ctx context.Context) {
	apps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Error("failed to load portable runtime state", "error", err)
		return
	}
	for _, app := range apps {
		if app.Status == "uninstalling" {
			continue
		}
		// Skip apps without container specs (e.g. authentik, traefik) — they're
		// managed by bootstrap or externally, not by the portable orchestrator.
		if catalogApp, err := o.catalog.Get(app.CatalogID); err != nil || catalogApp.Container == nil {
			continue
		}
		if err := o.ensureApp(ctx, app.CatalogID); err != nil {
			_ = o.appStore.UpdateStatus(app.CatalogID, "error")
			o.logger.Error("failed to reconcile portable app", "app", app.CatalogID, "error", err)
		}
	}
}

// PreStartApp runs the pre-start configurator phase for an app.
// No-op if the app has no configurator registered.
func (o *PortableOrchestrator) PreStartApp(ctx context.Context, appName string) error {
	app, err := o.catalog.Get(appName)
	if err != nil {
		return err
	}

	cfg := o.configurator(appName)
	if cfg == nil {
		return nil
	}

	state := o.buildAppState(app)

	if sc, ok := cfg.(configurator.PreStartConfigurator); ok {
		changed, err := sc.PreStartConfig(ctx, state)
		if err != nil {
			return fmt.Errorf("prestart config: %w", err)
		}
		if changed {
			o.logger.Info("prestart config changed", "app", appName)
		}
		return nil
	}
	if err := cfg.PreStart(ctx, state); err != nil {
		return fmt.Errorf("prestart: %w", err)
	}
	return nil
}

// EnsureContainer creates the network, mount directories, and container for an app.
// Sets the app status to "starting" on success.
func (o *PortableOrchestrator) EnsureContainer(ctx context.Context, appName string) error {
	app, err := o.catalog.Get(appName)
	if err != nil {
		return err
	}
	if app.Container == nil {
		return fmt.Errorf("app has no portable container topology")
	}

	spec, err := PortableContainerSpec(app, o.dataDir, o.templateVars)
	if err != nil {
		return err
	}
	if err := o.containers.EnsureNetwork(ctx, spec.Network); err != nil {
		return fmt.Errorf("ensure network: %w", err)
	}
	for _, mount := range spec.Mounts {
		if err := os.MkdirAll(mount.Source, 0755); err != nil {
			return fmt.Errorf("create mount source %s: %w", mount.Source, err)
		}
	}
	if _, err := o.containers.Ensure(ctx, spec); err != nil {
		return fmt.Errorf("ensure container: %w", err)
	}
	return o.appStore.UpdateStatus(appName, "starting")
}

// HealthCheckApp runs the health check for an app's configurator.
// No-op if the app has no configurator registered.
func (o *PortableOrchestrator) HealthCheckApp(ctx context.Context, appName string) error {
	cfg := o.configurator(appName)
	if cfg == nil {
		return nil
	}
	if err := cfg.HealthCheck(ctx); err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	return nil
}

// PostStartApp runs the post-start configurator phase for an app.
// No-op if the app has no configurator registered.
func (o *PortableOrchestrator) PostStartApp(ctx context.Context, appName string) error {
	app, err := o.catalog.Get(appName)
	if err != nil {
		return err
	}

	cfg := o.configurator(appName)
	if cfg == nil {
		return nil
	}

	state := o.buildAppState(app)

	if dc, ok := cfg.(configurator.PostStartConfigurator); ok {
		if err := dc.PostStartConfig(ctx, state); err != nil {
			return fmt.Errorf("poststart config: %w", err)
		}
		return nil
	}
	if err := cfg.PostStart(ctx, state); err != nil {
		return fmt.Errorf("poststart: %w", err)
	}
	return nil
}

// ProvisionSSO provisions forward-auth SSO for an app in the identity provider.
// No-op if the app doesn't use forward-auth or SSO is not configured.
func (o *PortableOrchestrator) ProvisionSSO(ctx context.Context, appName string) error {
	app, err := o.catalog.Get(appName)
	if err != nil {
		return err
	}

	if app.SSO.Strategy != "forward-auth" || o.sso == nil || o.ssoBaseURL == "" {
		return nil
	}

	externalURL := appSubdomainURL(o.ssoBaseURL, app.CatalogID)
	return o.sso.EnsureForwardAuth(app.CatalogID, app.DisplayName, externalURL)
}

// buildAppState constructs the configurator AppState for an app.
func (o *PortableOrchestrator) buildAppState(app *catalog.App) *configurator.AppState {
	state := &configurator.AppState{
		DataPath:      filepath.Join(o.dataDir, app.CatalogID),
		BloudDataPath: o.dataDir,
		SSOEnabled:    app.SSO.Strategy != "" && app.SSO.Strategy != "none",
	}
	if app.SSO.Strategy == "ldap" {
		state.LDAP = o.ldapOutput
	}
	return state
}

// RemoveApp removes a single app's container and store entry. It does NOT check
// graph blockers, update the graph, or regenerate routes — the reconciler handles those.
func (o *PortableOrchestrator) RemoveApp(ctx context.Context, appName string, clearData bool) error {
	// Stop tailnet node before removing app container (best-effort).
	if o.tailnetNode != nil {
		if err := o.tailnetNode.Stop(ctx, appName); err != nil {
			o.logger.Warn("failed to stop tailnet node", "app", appName, "error", err)
		}
		o.appStore.SetTailnetID(appName, "")
	}

	app, err := o.catalog.Get(appName)
	if err != nil {
		return fmt.Errorf("load app catalog: %w", err)
	}
	if app.Container != nil {
		if err := o.containers.Remove(ctx, PortableContainerName(app)); err != nil {
			return fmt.Errorf("remove container: %w", err)
		}
	}

	if err := o.appStore.Uninstall(appName); err != nil {
		return fmt.Errorf("remove app state: %w", err)
	}

	if clearData {
		if err := os.RemoveAll(filepath.Join(o.dataDir, appName)); err != nil {
			return fmt.Errorf("remove app data: %w", err)
		}
	}

	return nil
}

func (o *PortableOrchestrator) ensureApp(ctx context.Context, appName string) error {
	app, err := o.catalog.Get(appName)
	if err != nil {
		return err
	}
	if app.Container == nil {
		return fmt.Errorf("app has no portable container topology")
	}

	state := &configurator.AppState{
		DataPath:      filepath.Join(o.dataDir, app.CatalogID),
		BloudDataPath: o.dataDir,
		SSOEnabled:    app.SSO.Strategy != "" && app.SSO.Strategy != "none",
	}
	if app.SSO.Strategy == "ldap" {
		state.LDAP = o.ldapOutput
	}

	if cfg := o.configurator(appName); cfg != nil {
		if sc, ok := cfg.(configurator.PreStartConfigurator); ok {
			changed, err := sc.PreStartConfig(ctx, state)
			if err != nil {
				return fmt.Errorf("prestart config: %w", err)
			}
			if changed {
				o.logger.Info("prestart config changed", "app", appName)
			}
		} else {
			if err := cfg.PreStart(ctx, state); err != nil {
				return fmt.Errorf("prestart: %w", err)
			}
		}
	}

	spec, err := PortableContainerSpec(app, o.dataDir, o.templateVars)
	if err != nil {
		return err
	}
	if err := o.containers.EnsureNetwork(ctx, spec.Network); err != nil {
		return fmt.Errorf("ensure network: %w", err)
	}
	for _, mount := range spec.Mounts {
		if err := os.MkdirAll(mount.Source, 0755); err != nil {
			return fmt.Errorf("create mount source %s: %w", mount.Source, err)
		}
	}
	if _, err := o.containers.Ensure(ctx, spec); err != nil {
		return fmt.Errorf("ensure container: %w", err)
	}
	if err := o.appStore.UpdateStatus(appName, "starting"); err != nil {
		return err
	}

	if cfg := o.configurator(appName); cfg != nil {
		if err := cfg.HealthCheck(ctx); err != nil {
			return fmt.Errorf("health check: %w", err)
		}
		if dc, ok := cfg.(configurator.PostStartConfigurator); ok {
			if err := dc.PostStartConfig(ctx, state); err != nil {
				return fmt.Errorf("poststart config: %w", err)
			}
		} else {
			if err := cfg.PostStart(ctx, state); err != nil {
				return fmt.Errorf("poststart: %w", err)
			}
		}
	}

	// Provision SSO in the identity provider for forward-auth apps.
	// Non-fatal: Authentik may not be running yet (e.g. first bootstrap).
	if app.SSO.Strategy == "forward-auth" && o.sso != nil && o.ssoBaseURL != "" {
		externalURL := appSubdomainURL(o.ssoBaseURL, app.CatalogID)
		if err := o.sso.EnsureForwardAuth(app.CatalogID, app.DisplayName, externalURL); err != nil {
			o.logger.Warn("failed to provision forward-auth SSO (non-fatal)", "app", appName, "error", err)
		}
	}

	// Start Tailscale tailnet node for non-system user apps (sharing support).
	// Non-fatal: sharing is optional and shouldn't block app startup.
	if o.tailnetNode != nil && !app.IsSystem {
		if err := o.tailnetNode.EnsureRunning(ctx, app.CatalogID); err != nil {
			o.logger.Warn("failed to start tailnet node (sharing unavailable for this app)", "app", appName, "error", err)
		} else if o.activeTailnetID != nil {
			if tid := o.activeTailnetID(); tid != "" {
				o.appStore.SetTailnetID(appName, tid)
			}
		}
	}

	return o.appStore.UpdateStatus(appName, "running")
}

// appSubdomainURL builds the external URL for an app from the base URL.
// e.g., "http://localhost:8080" + "navidrome" → "http://navidrome.localhost:8080"
func appSubdomainURL(baseURL, appName string) string {
	// Simple string manipulation: insert "appName." before the host.
	// Handles both http://localhost:8080 and http://example.com.
	const httpScheme = "http://"
	const httpsScheme = "https://"
	switch {
	case len(baseURL) > len(httpScheme) && baseURL[:len(httpScheme)] == httpScheme:
		return httpScheme + appName + "." + baseURL[len(httpScheme):]
	case len(baseURL) > len(httpsScheme) && baseURL[:len(httpsScheme)] == httpsScheme:
		return httpsScheme + appName + "." + baseURL[len(httpsScheme):]
	default:
		return baseURL
	}
}

func (o *PortableOrchestrator) configurator(appName string) configurator.Configurator {
	if o.registry == nil {
		return nil
	}
	return o.registry.Get(appName)
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
					ID:         ra.AppID + "-" + slugify(ra.HostLabel),
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

	return o.traefikGen.GenerateAll(apps, remoteRoutes)
}

// slugify converts a string to a URL-safe slug for subdomain routing.
func slugify(s string) string {
	s = strings.ToLower(s)
	var result []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			result = append(result, c)
		} else if len(result) > 0 && result[len(result)-1] != '-' {
			result = append(result, '-')
		}
	}
	// Trim trailing dash
	if len(result) > 0 && result[len(result)-1] == '-' {
		result = result[:len(result)-1]
	}
	return string(result)
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
