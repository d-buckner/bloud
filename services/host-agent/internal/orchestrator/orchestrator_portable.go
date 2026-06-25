package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/reconciler"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
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
	Graph        catalog.AppGraphInterface
	CatalogCache catalog.CacheInterface
	AppStore     store.AppStoreInterface
	Containers   containerruntime.Runtime
	Registry     configurator.RegistryInterface
	TraefikGen   traefikgen.GeneratorInterface
	LDAPOutput   *configurator.LDAPOutput
	SSO             SSOProvisioner                    // optional; nil when Authentik is not available
	Sidecar         sharing.SidecarManagerInterface   // optional; nil when Tailscale auth key is not set
	Gateway         sharing.GatewayManagerInterface   // optional; manages gateway Tailscale container for remote app proxying
	RemoteProxy     *sharing.RemoteProxyManager       // optional; manages per-remote-app reverse proxies through SOCKS5 gateway
	RemoteAppStore  store.RemoteAppStoreInterface     // optional; nil when remote apps are not configured
	ActiveTailnetID func() string                     // returns the active tailnet connection ID (empty if none)
	SSOBaseURL      string                            // base URL for building app external URLs (e.g. "http://localhost:8080")
	DataDir      string
	TemplateVars map[string]string // extra variables for container spec rendering (e.g. postgresPassword)
	Logger       *slog.Logger
}

// PortableOrchestrator converges app lifecycle through portable runtime contracts.
type PortableOrchestrator struct {
	graph        catalog.AppGraphInterface
	catalog      catalog.CacheInterface
	appStore     store.AppStoreInterface
	containers   containerruntime.Runtime
	registry     configurator.RegistryInterface
	traefikGen   traefikgen.GeneratorInterface
	ldapOutput   *configurator.LDAPOutput
	sso             SSOProvisioner
	sidecar         sharing.SidecarManagerInterface
	gateway         sharing.GatewayManagerInterface
	remoteProxy     *sharing.RemoteProxyManager
	remoteAppStore  store.RemoteAppStoreInterface
	activeTailnetID func() string
	ssoBaseURL      string
	dataDir      string
	templateVars map[string]string
	logger       *slog.Logger
	operationMu  sync.Mutex
}

func NewPortable(cfg PortableConfig) *PortableOrchestrator {
	return &PortableOrchestrator{
		graph:        cfg.Graph,
		catalog:      cfg.CatalogCache,
		appStore:     cfg.AppStore,
		containers:   cfg.Containers,
		registry:     cfg.Registry,
		traefikGen:   cfg.TraefikGen,
		ldapOutput:   cfg.LDAPOutput,
		sso:             cfg.SSO,
		sidecar:         cfg.Sidecar,
		gateway:         cfg.Gateway,
		remoteProxy:     cfg.RemoteProxy,
		remoteAppStore:  cfg.RemoteAppStore,
		activeTailnetID: cfg.ActiveTailnetID,
		ssoBaseURL:      cfg.SSOBaseURL,
		dataDir:      cfg.DataDir,
		templateVars: cfg.TemplateVars,
		logger:       cfg.Logger,
	}
}

func (o *PortableOrchestrator) EnqueueInstall(ctx context.Context, req InstallRequest) (InstallResponse, error) {
	o.operationMu.Lock()
	defer o.operationMu.Unlock()
	return o.Install(ctx, req)
}

func (o *PortableOrchestrator) EnqueueUninstall(ctx context.Context, req UninstallRequest) (UninstallResponse, error) {
	o.operationMu.Lock()
	defer o.operationMu.Unlock()
	return o.Uninstall(ctx, req)
}

// SyncContainerState aligns DB state with actual container reality on startup.
// If a container was killed externally while the host-agent was down, the DB
// still shows "running". This method inspects each container and corrects the DB.
func (o *PortableOrchestrator) SyncContainerState(ctx context.Context) {
	o.operationMu.Lock()
	defer o.operationMu.Unlock()

	apps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Error("failed to load apps for container sync", "error", err)
		return
	}

	for _, app := range apps {
		catalogApp, err := o.catalog.Get(app.Name)
		if err != nil || catalogApp.Container == nil {
			continue
		}

		containerName := PortableContainerName(catalogApp)
		state, err := o.containers.Inspect(ctx, containerName)
		if err != nil {
			o.logger.Warn("failed to inspect container during sync", "app", app.Name, "error", err)
			continue
		}

		switch {
		case app.Status == "uninstalling" && !state.Exists:
			// Container gone + was uninstalling → clean up DB
			o.logger.Info("cleaning up uninstalled app", "app", app.Name)
			_ = o.appStore.Uninstall(app.Name)

		case (app.Status == "installing" || app.Status == "starting") && !state.Running:
			// Interrupted transition → mark error
			o.logger.Info("marking interrupted app as error", "app", app.Name, "previous_status", app.Status)
			_ = o.appStore.UpdateStatus(app.Name, "error")

		case app.Status == "running" && (!state.Exists || !state.Running):
			// DB says running but container isn't → mark stopped
			o.logger.Info("container not running, marking as stopped", "app", app.Name, "exists", state.Exists)
			_ = o.appStore.UpdateStatus(app.Name, "stopped")

		case (app.Status == "error" || app.Status == "stopped") && state.Running:
			// Container recovered or was started externally → mark running
			o.logger.Info("container running, marking as running", "app", app.Name, "previous_status", app.Status)
			_ = o.appStore.UpdateStatus(app.Name, "running")
		}
	}

	o.logger.Info("container state sync completed")
}

// ReconcileState converges all durable installed-app state after process restart.
func (o *PortableOrchestrator) ReconcileState(ctx context.Context) {
	o.operationMu.Lock()
	defer o.operationMu.Unlock()

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
		if catalogApp, err := o.catalog.Get(app.Name); err != nil || catalogApp.Container == nil {
			continue
		}
		if err := o.ensureApp(ctx, app.Name); err != nil {
			_ = o.appStore.UpdateStatus(app.Name, "error")
			o.logger.Error("failed to reconcile portable app", "app", app.Name, "error", err)
		}
	}
}

func (o *PortableOrchestrator) Install(ctx context.Context, req InstallRequest) (InstallResponse, error) {
	result := &InstallResult{App: req.App}

	plan, err := o.graph.PlanInstall(req.App)
	if err != nil {
		result.Error = fmt.Sprintf("failed to plan install: %v", err)
		return result, nil
	}
	if !plan.CanInstall {
		result.Error = fmt.Sprintf("cannot install: %v", plan.Blockers)
		return result, nil
	}

	integrations := buildIntegrationConfig(req.Choices, plan.AutoConfig, plan.Choices)
	appNames := make([]string, 0, len(integrations)+1)
	for _, provider := range integrations {
		appNames = append(appNames, provider)
	}
	appNames = append(appNames, req.App)

	for _, appName := range appNames {
		appIntegrations := map[string]string(nil)
		if appName == req.App {
			appIntegrations = integrations
		}
		if err := o.recordIntent(appName, appIntegrations); err != nil {
			result.Error = fmt.Sprintf("failed to record intent for %s: %v", appName, err)
			return result, nil
		}
		// Skip ensureApp for apps already running (e.g. system apps from bootstrap)
		if existing, _ := o.appStore.GetByName(appName); existing != nil && existing.Status == "running" {
			result.AppsInstalled = append(result.AppsInstalled, appName)
			result.Configured = append(result.Configured, appName)
			continue
		}
		if err := o.ensureApp(ctx, appName); err != nil {
			_ = o.appStore.UpdateStatus(appName, "error")
			result.Error = fmt.Sprintf("failed to converge %s: %v", appName, err)
			return result, nil
		}
		result.AppsInstalled = append(result.AppsInstalled, appName)
		result.Configured = append(result.Configured, appName)
	}

	installed, _ := o.appStore.GetInstalledNames()
	o.graph.SetInstalled(installed)
	if err := o.RegenerateRoutes(); err != nil {
		o.logger.Warn("failed to regenerate routes", "error", err)
	}

	result.Success = true
	result.GenerationInfo = "portable container topology converged"
	return result, nil
}

func (o *PortableOrchestrator) Uninstall(ctx context.Context, req UninstallRequest) (UninstallResponse, error) {
	result := &UninstallResult{App: req.App}

	plan, err := o.graph.PlanRemove(req.App)
	if err != nil {
		result.Error = fmt.Sprintf("failed to plan removal: %v", err)
		return result, nil
	}
	if !plan.CanRemove {
		result.Error = fmt.Sprintf("cannot remove: %v", plan.Blockers)
		return result, nil
	}

	app, err := o.catalog.Get(req.App)
	if err != nil {
		result.Error = fmt.Sprintf("load app topology: %v", err)
		return result, nil
	}
	if app.Container == nil {
		result.Error = fmt.Sprintf("app %s has no portable container topology", req.App)
		return result, nil
	}

	_ = o.appStore.UpdateStatus(req.App, "uninstalling")

	// Stop sidecar before removing app container (best-effort).
	if o.sidecar != nil {
		if err := o.sidecar.Stop(ctx, req.App); err != nil {
			o.logger.Warn("failed to stop sidecar", "app", req.App, "error", err)
		}
		o.appStore.SetTailnetID(req.App, "")
	}

	if err := o.containers.Remove(ctx, PortableContainerName(app)); err != nil {
		result.Error = fmt.Sprintf("remove container: %v", err)
		return result, nil
	}
	if err := o.appStore.Uninstall(req.App); err != nil {
		result.Error = fmt.Sprintf("remove app state: %v", err)
		return result, nil
	}
	if req.ClearData {
		if err := os.RemoveAll(filepath.Join(o.dataDir, req.App)); err != nil {
			result.Error = fmt.Sprintf("remove app data: %v", err)
			return result, nil
		}
	}

	installed, _ := o.appStore.GetInstalledNames()
	o.graph.SetInstalled(installed)
	if err := o.RegenerateRoutes(); err != nil {
		o.logger.Warn("failed to regenerate routes", "error", err)
	}

	result.Success = true
	result.Unconfigured = plan.WillUnconfigure
	return result, nil
}

// EnsureApp is the public wrapper around ensureApp for use by the reconciler.
// It runs PreStart → container ensure → health check → PostStart → SSO → sidecar → status→running.
func (o *PortableOrchestrator) EnsureApp(ctx context.Context, appName string) error {
	return o.ensureApp(ctx, appName)
}

// RemoveApp removes a single app's container and store entry. It does NOT check
// graph blockers, update the graph, or regenerate routes — the reconciler handles those.
func (o *PortableOrchestrator) RemoveApp(ctx context.Context, appName string, clearData bool) error {
	// Stop sidecar before removing app container (best-effort).
	if o.sidecar != nil {
		if err := o.sidecar.Stop(ctx, appName); err != nil {
			o.logger.Warn("failed to stop sidecar", "app", appName, "error", err)
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
		DataPath:      filepath.Join(o.dataDir, app.Name),
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
		externalURL := appSubdomainURL(o.ssoBaseURL, app.Name)
		if err := o.sso.EnsureForwardAuth(app.Name, app.DisplayName, externalURL); err != nil {
			o.logger.Warn("failed to provision forward-auth SSO (non-fatal)", "app", appName, "error", err)
		}
	}

	// Start Tailscale sidecar for non-system user apps (sharing support).
	// Non-fatal: sharing is optional and shouldn't block app startup.
	if o.sidecar != nil && !app.IsSystem {
		if err := o.sidecar.EnsureRunning(ctx, app.Name, app.Port); err != nil {
			o.logger.Warn("failed to start sidecar (sharing unavailable for this app)", "app", appName, "error", err)
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

func (o *PortableOrchestrator) recordIntent(appName string, integrations map[string]string) error {
	existing, err := o.appStore.GetByName(appName)
	if err != nil {
		return err
	}
	if existing != nil && existing.Status == "running" {
		return nil
	}

	app, err := o.catalog.Get(appName)
	if err != nil {
		return err
	}
	return o.appStore.Install(app.Name, app.DisplayName, app.Version, integrations, &store.InstallOptions{
		Port:     app.Port,
		IsSystem: app.IsSystem,
	})
}

func (o *PortableOrchestrator) RegenerateRoutes() error {
	if o.traefikGen == nil {
		return nil
	}
	names, err := o.appStore.GetInstalledNames()
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
	// provides SOCKS5 for remote app proxying and serves as the owner's
	// presence on the tailnet for remote access.
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
					TailnetURL: "https://" + ra.SidecarTailnetAddr,
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
	return "apps-" + app.Name
}

// PortableContainerSpec builds a runtime-neutral container spec from catalog metadata.
// extraVars supplies additional template variables (e.g. "postgresPassword") beyond
// the built-in {{dataDir}} and {{appDataDir}}.
func PortableContainerSpec(app *catalog.App, dataDir string, extraVars map[string]string) (containerruntime.Spec, error) {
	if app.Container == nil || app.Container.Image == "" {
		return containerruntime.Spec{}, fmt.Errorf("app %s has no portable container image", app.Name)
	}

	render := func(value string) string {
		value = strings.ReplaceAll(value, "{{dataDir}}", dataDir)
		value = strings.ReplaceAll(value, "{{appDataDir}}", filepath.Join(dataDir, app.Name))
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
		Labels:        map[string]string{"io.bloud.app": app.Name},
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
