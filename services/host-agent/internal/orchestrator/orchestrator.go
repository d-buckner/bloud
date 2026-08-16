package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/netutil"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sso"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const maxOrchestratorEvents = 20

// OrchestratorStatus is a snapshot of the orchestrator's current state for
// the developer API.
type OrchestratorStatus struct {
	QueueDepth     int             `json:"queueDepth"`
	IsConverging   bool            `json:"isConverging"`
	RecentActivity []ActivityEvent `json:"recentActivity"`
}

// ActivityEvent records a single orchestrator lifecycle event.
type ActivityEvent struct {
	Time   time.Time `json:"time"`
	Event  string    `json:"event"` // "intent_enqueued", "drain_complete", "converge_start", "converge_step", "converge_complete"
	Detail string    `json:"detail"`
}

// OrchestratorConfig is the complete set of tunable parameters and optional
// dependencies for the Orchestrator. Nil/zero fields disable the corresponding
// subsystem. All fields are set at construction time via NewOrchestrator.
type OrchestratorConfig struct {
	// ── Tunable parameters ───────────────────────────────────────────────

	// HealthCheckTimeout limits how long each app's HealthCheck can run.
	// Zero means no timeout (the caller's context deadline applies).
	HealthCheckTimeout time.Duration

	// LDAPOutput is the LDAP provider endpoint injected into apps with
	// LDAP SSO strategy. Nil when no LDAP provider is configured.
	LDAPOutput *configurator.LDAPOutput

	// ── Container runtime ────────────────────────────────────────────────

	// Containers is the container runtime used to create app containers from
	// catalog specs. Nil disables catalog-driven container creation.
	Containers containerruntime.Runtime

	// TemplateVars are extra variables for container spec template rendering
	// (e.g. "postgresPassword"). Passed to ContainerSpec.
	TemplateVars map[string]string

	// ── Converge dependencies (nil = subsystem disabled) ─────────────────

	AppStore         store.AppStoreInterface
	CatalogGraph     catalog.AppGraphInterface
	TailnetStore     store.TailnetStoreInterface
	RemoteAppStore   store.RemoteAppStoreInterface
	TailnetNode      TailnetNodeEnsurer
	Gateway          GatewayManager
	RemoteProxy      RemoteProxyManager
	ProxyOutpost     ProxyOutpostEnsurer
	ForwardDomainSSO ForwardDomainProvisioner
	SSO              SSOProvisioner
	SSOBaseURL       string // base URL for building app subdomain URLs (e.g. "http://localhost:8080")
	SSOHostSecret    string // master secret for deriving deterministic per-app OIDC client secrets
	SSOAuthentikURL  string // browser-accessible Authentik URL for OIDC issuer/discovery
	SSOIssuerURL     string // OIDC issuer base URL reachable from app containers (empty = SSOAuthentikURL)
	TraefikGen       traefikgen.GeneratorInterface
	ActiveTailnetID  func() string // returns the active tailnet connection ID (empty if none)
}

// Orchestrator drives app nodes through their lifecycle phases in dependency
// order, processing nodes within each level concurrently.
//
// Lifecycle phases per node:
//
//	INITIALIZING → PRESTART_CONFIG → STARTING → POSTSTART_CONFIG
//
// After all nodes in a reconcile pass complete their phases, routes are
// regenerated and then nodes are promoted to RUNNING. This ensures the UI
// shows an app as "installed" only once external access (Traefik routes) is
// live.
//
// Error handling:
//   - Individual app errors set the node to ERROR and are not propagated.
//   - ERROR is terminal: a node in ERROR is skipped on all subsequent passes
//     until its status is explicitly reset.
//   - A node whose dependency is in ERROR is also skipped (blocked).
//
// Staleness: if a node is already RUNNING and one of its dependencies
// completes successfully this pass, the node re-runs PostStart to pick up
// any new configuration exposed by that dependency.
type Orchestrator struct {
	// Core lifecycle fields
	graph    *graph.Graph
	registry configurator.RegistryInterface
	catalog  catalog.CacheInterface
	dataDir  string
	logger   *slog.Logger
	config   OrchestratorConfig

	// Intent processing fields
	queue            *IntentQueue
	appStore         store.AppStoreInterface
	catalogGraph     catalog.AppGraphInterface
	tailnetStore     store.TailnetStoreInterface
	remoteAppStore   store.RemoteAppStoreInterface
	tailnetNode      TailnetNodeEnsurer
	gateway          GatewayManager
	remoteProxy      RemoteProxyManager
	proxyOutpost     ProxyOutpostEnsurer
	forwardDomainSSO ForwardDomainProvisioner
	sso              SSOProvisioner
	ssoBaseURL       string
	ssoHostSecret    string
	ssoAuthentikURL  string
	ssoIssuerURL     string
	traefikGen       traefikgen.GeneratorInterface
	activeTailnetID  func() string

	// Start/Stop lifecycle
	cancel  context.CancelFunc
	started chan struct{}
	ready   chan struct{}
	done    chan struct{}
	once    sync.Once

	// containerOwner maps container node names to their owning app catalog ID.
	// Used for multi-container apps where node names differ from app catalog IDs.
	// e.g. "apps-authentik-server" → "authentik"
	containerOwner map[string]string

	// Activity log for the developer API
	activityMu  sync.Mutex
	activityBuf [maxOrchestratorEvents]ActivityEvent
	activityPos int
	converging  atomic.Bool
}

// NewOrchestrator creates a fully-configured Orchestrator backed by the
// provided graph. All subsystem dependencies are supplied up-front via config;
// nil/zero fields disable the corresponding subsystem.
// catalogCache may be nil; when nil, SSO detection is disabled.
func NewOrchestrator(
	g *graph.Graph,
	registry configurator.RegistryInterface,
	catalogCache catalog.CacheInterface,
	dataDir string,
	logger *slog.Logger,
	config OrchestratorConfig,
) *Orchestrator {
	o := &Orchestrator{
		graph:            g,
		registry:         registry,
		catalog:          catalogCache,
		dataDir:          dataDir,
		logger:           logger,
		config:           config,
		appStore:         config.AppStore,
		catalogGraph:     config.CatalogGraph,
		tailnetStore:     config.TailnetStore,
		remoteAppStore:   config.RemoteAppStore,
		tailnetNode:      config.TailnetNode,
		gateway:          config.Gateway,
		remoteProxy:      config.RemoteProxy,
		proxyOutpost:     config.ProxyOutpost,
		forwardDomainSSO: config.ForwardDomainSSO,
		sso:              config.SSO,
		ssoBaseURL:       config.SSOBaseURL,
		ssoHostSecret:    config.SSOHostSecret,
		ssoAuthentikURL:  config.SSOAuthentikURL,
		ssoIssuerURL:     config.SSOIssuerURL,
		traefikGen:       config.TraefikGen,
		activeTailnetID:  config.ActiveTailnetID,
		queue:            NewIntentQueue(DefaultDebounce),
		started:          make(chan struct{}),
		ready:            make(chan struct{}),
		done:             make(chan struct{}),
		containerOwner:   make(map[string]string),
	}
	o.setupStatusSync()
	return o
}

// setupStatusSync registers a graph event handler that keeps the app store's
// status field in sync with lifecycle graph transitions. This is the single
// authoritative path from graph state → DB status; no caller needs to call
// appStore.UpdateStatus for lifecycle-driven transitions.
func (o *Orchestrator) setupStatusSync() {
	if o.appStore == nil {
		return
	}
	o.graph.On(graph.EventNodeUpdated, func(node graph.Node) {
		appID := o.ownerApp(node.ID)
		if appID == node.ID {
			// Single-container node: direct status mapping.
			switch node.ActualStatus {
			case graph.StatusRunning:
				_ = o.appStore.UpdateStatus(appID, "running")
			case graph.StatusError:
				_ = o.appStore.UpdateStatus(appID, "error")
			}
			return
		}
		// Multi-container node: aggregate across all containers.
		// Error fires immediately on any container; running only when all are up.
		switch node.ActualStatus {
		case graph.StatusError:
			_ = o.appStore.UpdateStatus(appID, "error")
		case graph.StatusRunning:
			if o.allContainersRunning(appID) {
				_ = o.appStore.UpdateStatus(appID, "running")
			}
		}
	})
}

// allContainersRunning returns true when every container node for appID has
// reached StatusRunning. Returns true for single-container apps (no rollup needed).
func (o *Orchestrator) allContainersRunning(appID string) bool {
	if o.catalog == nil {
		return true
	}
	catalogApp, err := o.catalog.Get(appID)
	if err != nil || catalogApp == nil {
		return true
	}
	defs := catalogApp.ContainerDefs()
	if len(catalogApp.Containers) == 0 {
		return true
	}
	for _, def := range defs {
		node, err := o.graph.GetNode(def.Name)
		if err != nil || node == nil || node.ActualStatus != graph.StatusRunning {
			return false
		}
	}
	return true
}

// recordActivity appends an event to the ring buffer.
func (o *Orchestrator) recordActivity(event, detail string) {
	o.activityMu.Lock()
	o.activityBuf[o.activityPos] = ActivityEvent{Time: time.Now(), Event: event, Detail: detail}
	o.activityPos = (o.activityPos + 1) % maxOrchestratorEvents
	o.activityMu.Unlock()
}

// Status returns a snapshot of the orchestrator's current state.
func (o *Orchestrator) Status() OrchestratorStatus {
	o.activityMu.Lock()
	recent := make([]ActivityEvent, 0, maxOrchestratorEvents)
	for i := 0; i < maxOrchestratorEvents; i++ {
		idx := (o.activityPos - 1 - i + maxOrchestratorEvents) % maxOrchestratorEvents
		if o.activityBuf[idx].Event == "" {
			continue
		}
		recent = append(recent, o.activityBuf[idx])
	}
	o.activityMu.Unlock()

	return OrchestratorStatus{
		QueueDepth:     o.queue.PendingCount(),
		IsConverging:   o.converging.Load(),
		RecentActivity: recent,
	}
}

// Enqueue adds an intent to the orchestrator's queue for processing.
func (o *Orchestrator) Enqueue(intent Intent) {
	o.logger.Info("intent enqueued", "type", intentTypeName(intent), "id", intent.IntentID())
	o.recordActivity("intent_enqueued", intentTypeName(intent))
	o.queue.Enqueue(intent)
}

// Start runs an initial convergence pass and then processes intents as they
// arrive. It blocks until the context is cancelled or Stop is called. Must be
// called exactly once (typically via goroutine).
func (o *Orchestrator) Start(ctx context.Context) {
	ctx, o.cancel = context.WithCancel(ctx)
	close(o.started)
	defer close(o.done)

	o.logger.Info("orchestrator started")

	// Initial convergence (blocks until system apps are up).
	o.converge(ctx, nil)
	close(o.ready)

	for {
		intents := o.queue.WaitAndDrain(ctx)
		if intents == nil {
			o.logger.Info("orchestrator stopped")
			return
		}
		o.converge(ctx, intents)
	}
}

// Ready returns a channel that is closed after the first convergence pass completes.
func (o *Orchestrator) Ready() <-chan struct{} {
	return o.ready
}

// Stop cancels the intent processing loop and waits for it to finish.
// Safe to call multiple times.
func (o *Orchestrator) Stop() {
	o.once.Do(func() {
		<-o.started
		o.cancel()
		<-o.done
	})
}

// converge processes a batch of intents: applies them to stores, then
// converges the system state from the stores.
func (o *Orchestrator) converge(ctx context.Context, intents []Intent) {
	if o.appStore == nil {
		o.logger.Info("convergence pass complete (stub)", "intentCount", len(intents))
		return
	}

	o.converging.Store(true)
	defer o.converging.Store(false)
	o.recordActivity("converge_start", fmt.Sprintf("%d intents", len(intents)))

	start := time.Now()
	pendingClearData := make(map[string]bool)

	if len(intents) > 0 {
		o.applyIntents(intents, pendingClearData)
		o.recordActivity("drain_complete", fmt.Sprintf("%d", len(intents)))
	}

	o.convergeFromStores(ctx, pendingClearData)
	o.recordActivity("converge_complete", fmt.Sprintf("%d intents, %s", len(intents), time.Since(start).Round(time.Millisecond)))
}

// RemoveApp calls NodeLifecycle.Remove for the named app (if a configurator is
// registered), removes containers, then deletes graph node(s).
// For multi-container apps, all container nodes are removed.
func (o *Orchestrator) RemoveApp(ctx context.Context, appName string, clearData bool) error {
	o.logger.Info("removing app", "app", appName, "clear_data", clearData)

	// Multi-container apps: remove each container node individually.
	if o.catalog != nil {
		if catalogApp, err := o.catalog.Get(appName); err == nil && catalogApp != nil {
			if len(catalogApp.Containers) > 0 {
				return o.removeMultiContainerApp(ctx, appName, catalogApp.ContainerDefs(), clearData)
			}
		}
	}

	// Single-container (or system) app: existing behavior.
	nl := o.registry.Get(appName)
	if nl != nil {
		state, err := o.buildAppState(appName)
		if err != nil {
			return fmt.Errorf("build app state: %w", err)
		}
		if err := nl.Remove(ctx, state, clearData); err != nil {
			return fmt.Errorf("remove app %q: %w", appName, err)
		}
	}
	return o.graph.DeleteNode(appName)
}

// removeMultiContainerApp removes all container nodes for a multi-container app,
// running per-node configurator Remove() and container runtime Remove() for each.
func (o *Orchestrator) removeMultiContainerApp(ctx context.Context, appName string, defs []catalog.ContainerDef, clearData bool) error {
	for _, def := range defs {
		nl := o.registry.Get(def.Name)
		if nl != nil {
			state, err := o.buildAppState(def.Name)
			if err != nil {
				o.logger.Warn("failed to build state for container removal", "container", def.Name, "error", err)
			} else if err := nl.Remove(ctx, state, clearData); err != nil {
				o.logger.Warn("configurator remove failed", "container", def.Name, "error", err)
			}
		}
		if o.config.Containers != nil {
			if err := o.config.Containers.Remove(ctx, def.Name); err != nil {
				o.logger.Warn("failed to remove container", "container", def.Name, "error", err)
			}
		}
		if err := o.graph.DeleteNode(def.Name); err != nil {
			o.logger.Warn("failed to delete graph node", "container", def.Name, "error", err)
		}
		delete(o.containerOwner, def.Name)
	}
	if clearData {
		dataDir := filepath.Join(o.dataDir, appName)
		if err := os.RemoveAll(dataDir); err != nil {
			o.logger.Warn("failed to remove data directory", "app", appName, "path", dataDir, "error", err)
		}
	}
	return nil
}

// Reconcile runs one full reconciliation pass over all graph nodes.
// Nodes are processed level by level (dependencies before dependents);
// nodes within the same level run concurrently.
//
// Returns an error only for infrastructure failures (e.g. corrupt graph);
// individual app lifecycle errors are recorded as ERROR status on the node.
func (o *Orchestrator) Reconcile(ctx context.Context) error {
	levels, err := o.graph.GetTopologicalLevels()
	if err != nil {
		return fmt.Errorf("get topological levels: %w", err)
	}

	o.logger.Info("reconcile pass started", "levels", len(levels))
	changedIDs := make(map[string]bool)

	for _, level := range levels {
		if err := o.processLevel(ctx, level, changedIDs); err != nil {
			return err
		}
	}

	// Regenerate routes now that all lifecycle phases are complete. Deferring
	// this — and the RUNNING promotion below — ensures the UI never shows an
	// app as "installed" before its Traefik routes are live.
	if err := o.RegenerateRoutes(); err != nil {
		o.logger.Warn("route regeneration failed", "error", err)
	}

	for id := range changedIDs {
		o.logger.Info("marking node RUNNING after route generation", "app", id)
		_ = o.graph.SetActualStatus(id, graph.StatusRunning, "")
	}

	return nil
}

// processLevel runs all work items for one topological level concurrently,
// then updates changedIDs with the IDs of nodes that successfully reached
// their target status.
func (o *Orchestrator) processLevel(ctx context.Context, nodeIDs []string, changedIDs map[string]bool) error {
	work, err := o.collectWorkForLevel(nodeIDs, changedIDs)
	if err != nil {
		return err
	}
	o.logger.Info("level work collected", "nodes", len(nodeIDs), "work", len(work))
	if len(work) == 0 {
		return nil
	}

	type result struct {
		id      string
		success bool
	}
	results := make(chan result, len(work))

	g, gCtx := errgroup.WithContext(ctx)
	for _, id := range work {
		id := id
		g.Go(func() error {
			success := o.runConfigurator(gCtx, id)
			results <- result{id, success}
			return nil // app errors are captured as node status, not propagated
		})
	}
	_ = g.Wait()
	close(results)

	for r := range results {
		if r.success {
			changedIDs[r.id] = true
		}
	}

	return nil
}

// collectWorkForLevel returns the subset of nodeIDs that need action this pass:
//  1. Nodes whose actual status hasn't reached their target (excluding ERROR nodes
//     and nodes whose dependencies have not yet completed: neither RUNNING from a
//     prior pass nor present in changedIDs from this pass).
//  2. Nodes already at RUNNING whose dependency appeared in changedIDs (staleness).
func (o *Orchestrator) collectWorkForLevel(nodeIDs []string, changedIDs map[string]bool) ([]string, error) {
	var work []string
	for _, id := range nodeIDs {
		node, err := o.graph.GetNode(id)
		if err != nil {
			return nil, fmt.Errorf("get node %q: %w", id, err)
		}
		if node == nil {
			continue
		}

		// ERROR is terminal — never retry without an explicit status reset.
		if node.ActualStatus == graph.StatusError {
			o.logger.Info("skipping node in ERROR status", "app", id, "error", node.Error)
			continue
		}

		if node.TargetStatus != node.ActualStatus {
			// Node needs to progress. Only proceed if all deps are ready:
			// either RUNNING from a prior pass, or having completed their
			// lifecycle phases this pass (present in changedIDs). A dep in
			// ERROR blocks this node regardless.
			deps, err := o.graph.GetDependencies(id)
			if err != nil {
				return nil, fmt.Errorf("get dependencies for %q: %w", id, err)
			}
			blocked := false
			var blockingDep string
			for _, dep := range deps {
				depNode, err := o.graph.GetNode(dep)
				if err != nil || depNode == nil {
					continue
				}
				if depNode.ActualStatus != graph.StatusRunning && !changedIDs[dep] {
					blocked = true
					blockingDep = dep
					break
				}
			}
			if !blocked {
				o.logger.Info("queuing node for lifecycle", "app", id, "actual", node.ActualStatus, "target", node.TargetStatus)
				work = append(work, id)
				continue
			}
			o.logger.Info("skipping blocked node", "app", id, "actual", node.ActualStatus, "target", node.TargetStatus, "blocking_dep", blockingDep)
			continue
		}

		// Node is already at its target. Check staleness: re-run PostStart if a
		// direct dependency successfully completed this pass.
		if node.ActualStatus == graph.StatusRunning {
			deps, err := o.graph.GetDependencies(id)
			if err != nil {
				return nil, fmt.Errorf("get dependencies for %q: %w", id, err)
			}
			for _, dep := range deps {
				if changedIDs[dep] {
					o.logger.Info("queuing stale node for PostStart re-run", "app", id, "changed_dep", dep)
					work = append(work, id)
					break
				}
			}
		}
	}
	return work, nil
}

// runConfigurator drives a single node through its lifecycle, or re-runs
// PostStart only for nodes that are already RUNNING (staleness).
// Returns true only when the node successfully transitions to its target status
// for the first time this pass (staleness re-runs return false).
func (o *Orchestrator) runConfigurator(ctx context.Context, id string) bool {
	node, err := o.graph.GetNode(id)
	if err != nil || node == nil {
		return false
	}

	// Staleness re-run: node is already at RUNNING, update PostStart config only.
	if node.ActualStatus == graph.StatusRunning && node.TargetStatus == graph.StatusRunning {
		o.logger.Info("dispatching staleness re-run", "app", id)
		o.runPostStartOnly(ctx, id)
		return false // staleness re-runs don't propagate changedIDs further
	}

	o.logger.Info("dispatching full lifecycle", "app", id, "actual", node.ActualStatus, "target", node.TargetStatus)
	return o.runFullLifecycle(ctx, id, node)
}

// runPostStartOnly re-runs PostStart for an already-RUNNING node whose
// dependency just became available. Used for staleness propagation.
func (o *Orchestrator) runPostStartOnly(ctx context.Context, id string) {
	cfg := o.registry.Get(id)
	if cfg == nil {
		return
	}
	state, err := o.buildAppState(id)
	if err != nil {
		o.logger.Warn("staleness re-run: failed to build state", "app", id, "error", err)
		return
	}
	o.logger.Info("staleness re-run: running PostStart", "app", id)
	if err := cfg.PostStart(ctx, state); err != nil {
		o.logger.Warn("staleness re-run: PostStart failed", "app", id, "error", err)
		return
	}
	o.logger.Info("staleness re-run: PostStart complete", "app", id)
}

// ensureContainerFromDef ensures a container exists and is running from a ContainerDef,
// creating required networks and mount directories first.
func (o *Orchestrator) ensureContainerFromDef(ctx context.Context, def *catalog.ContainerDef, appCatalogID string) error {
	if o.config.Containers == nil {
		return nil
	}

	// Collect all networks referenced by this container.
	var networks []string
	if def.Network != "" {
		networks = append(networks, def.Network)
	}
	for _, n := range def.Networks {
		if n != def.Network {
			networks = append(networks, n)
		}
	}
	for _, network := range networks {
		if network == "host" {
			continue // host network mode doesn't need to be created
		}
		if err := o.config.Containers.EnsureNetwork(ctx, network); err != nil {
			o.logger.Warn("failed to ensure network", "container", def.Name, "network", network, "error", err)
		}
	}

	spec, err := ContainerSpecFromDef(*def, appCatalogID, o.dataDir, o.config.TemplateVars)
	if err != nil {
		return fmt.Errorf("build container spec: %w", err)
	}

	for _, mount := range spec.Mounts {
		// Skip file mounts - only create directories for directory mounts
		if !strings.HasSuffix(mount.Source, ".yml") && !strings.HasSuffix(mount.Source, ".yaml") && !strings.HasSuffix(mount.Source, ".json") && !strings.HasSuffix(mount.Source, ".conf") {
			if err := os.MkdirAll(mount.Source, 0755); err != nil {
				o.logger.Warn("failed to create mount directory", "container", def.Name, "path", mount.Source, "error", err)
			}
		}
	}

	if _, err := o.config.Containers.Ensure(ctx, spec); err != nil {
		return fmt.Errorf("ensure container: %w", err)
	}
	return nil
}

// runFullLifecycle executes all lifecycle phases for a node that has not yet
// reached its target status.
func (o *Orchestrator) runFullLifecycle(ctx context.Context, id string, node *graph.Node) bool {
	// Target INITIALIZING means "unmanage" — snap actual to match and stop.
	if node.TargetStatus == graph.StatusInitializing {
		_ = o.graph.SetActualStatus(id, graph.StatusInitializing, "")
		return false
	}

	def, appCatalogID := o.containerDefForNode(id)
	cfg := o.registry.Get(id)

	if cfg == nil && def == nil {
		o.logger.Info("no configurator registered, will mark RUNNING after route generation", "app", id)
		return true
	}

	state, err := o.buildAppState(id)
	if err != nil {
		o.logger.Error("failed to build app state", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		return false
	}

	// Phase 1: PreStart
	var configChanged bool
	if cfg != nil {
		o.logger.Info("lifecycle phase: PreStart", "app", id)
		_ = o.graph.SetActualStatus(id, graph.StatusPreStartConfig, "")
		var err error
		configChanged, err = cfg.PreStart(ctx, state)
		if err != nil {
			o.logger.Warn("PreStart failed", "app", id, "error", err)
			_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
			return false
		}
		o.logger.Info("lifecycle phase: PreStart complete", "app", id, "config_changed", configChanged)
	}

	// SSO provisioning: ensure the forward-auth provider exists in Authentik before the
	// container starts, so requests can be authenticated immediately on first boot.
	if err := o.ensureSSO(ctx, id); err != nil {
		o.logger.Warn("SSO provisioning failed", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		return false
	}

	// Phase 2: EnsureContainer
	// If PreStart reported config changes to mounted files, remove the existing
	// container first so Ensure() creates a fresh one that picks up the changes.
	if def != nil {
		if configChanged {
			o.logger.Info("config changed, removing container before re-create", "app", id)
			_ = o.config.Containers.Remove(ctx, def.Name)
		}
		o.logger.Info("lifecycle phase: EnsureContainer", "app", id)
		_ = o.graph.SetActualStatus(id, graph.StatusStarting, "")
		if err := o.ensureContainerFromDef(ctx, def, appCatalogID); err != nil {
			o.logger.Warn("EnsureContainer failed", "app", id, "error", err)
			_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
			return false
		}
		o.logger.Info("lifecycle phase: EnsureContainer complete", "app", id)

		// Phase 3: HealthCheck
		o.logger.Info("lifecycle phase: HealthCheck", "app", id)
		healthCtx := ctx
		if o.config.HealthCheckTimeout > 0 {
			var cancel context.CancelFunc
			healthCtx, cancel = context.WithTimeout(ctx, o.config.HealthCheckTimeout)
			defer cancel()
		}
		if def.HealthCheck != nil {
			if err := o.runContainerHealthCheck(healthCtx, def.Name, def.HealthCheck); err != nil {
				o.logger.Warn("HealthCheck failed", "app", id, "error", err)
				_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
				return false
			}
		}
		o.logger.Info("lifecycle phase: HealthCheck complete", "app", id)
	}

	// Phase 4: PostStart
	if cfg != nil {
		o.logger.Info("lifecycle phase: PostStart", "app", id)
		_ = o.graph.SetActualStatus(id, graph.StatusPostStartConfig, "")
		if err := cfg.PostStart(ctx, state); err != nil {
			o.logger.Warn("PostStart failed", "app", id, "error", err)
			_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
			return false
		}
		o.logger.Info("lifecycle phase: PostStart complete", "app", id)
	}

	o.logger.Info("lifecycle phases complete, will mark RUNNING after route generation", "app", id)
	return true
}

// registerContainerOwner records that containerName belongs to appCatalogID.
// Called during convergence when multi-container nodes are created.
func (o *Orchestrator) registerContainerOwner(containerName, appCatalogID string) {
	o.containerOwner[containerName] = appCatalogID
}

// ownerApp returns the app catalog ID that owns the given node ID.
// For multi-container nodes, returns the owning app's catalog ID.
// For single-container nodes (or unregistered nodes), returns nodeID itself.
func (o *Orchestrator) ownerApp(nodeID string) string {
	if appID, ok := o.containerOwner[nodeID]; ok {
		return appID
	}
	return nodeID
}

// containerDefForNode returns the ContainerDef for a multi-container node,
// along with the owning app's catalog ID. Returns nil, "" for single-container nodes.
func (o *Orchestrator) containerDefForNode(nodeID string) (*catalog.ContainerDef, string) {
	appID := o.ownerApp(nodeID)
	if appID == nodeID {
		return nil, "" // not a registered multi-container node
	}
	if o.catalog == nil {
		return nil, appID
	}
	catalogApp, err := o.catalog.Get(appID)
	if err != nil || catalogApp == nil {
		return nil, appID
	}
	for _, def := range catalogApp.ContainerDefs() {
		if def.Name == nodeID {
			d := def
			return &d, appID
		}
	}
	return nil, appID
}

// convertHealthCheckTest converts Docker-style health check test format to podman exec args.
// CMD-SHELL: ["CMD-SHELL", "cmd"] → ["/bin/sh", "-c", "cmd"]
// CMD:       ["CMD", "exec", "arg1"] → ["exec", "arg1"]
func convertHealthCheckTest(test []string) []string {
	if len(test) == 0 {
		return test
	}
	switch test[0] {
	case "CMD-SHELL":
		if len(test) == 1 {
			return []string{"/bin/sh", "-c", ""}
		}
		return []string{"/bin/sh", "-c", test[1]}
	case "CMD":
		if len(test) <= 1 {
			return test
		}
		return test[1:]
	default:
		return test
	}
}

// runContainerHealthCheck polls the health check command inside the named container
// until it passes or retries are exhausted, respecting context cancellation.
func (o *Orchestrator) runContainerHealthCheck(ctx context.Context, containerName string, hc *catalog.ContainerHealthCheck) error {
	if o.config.Containers == nil {
		return nil
	}
	interval := time.Duration(hc.Interval) * time.Second
	if interval == 0 {
		interval = 5 * time.Second
	}
	timeout := time.Duration(hc.Timeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	retries := hc.Retries
	if retries == 0 {
		retries = 3
	}

	for attempt := 0; attempt < retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		}
		execCtx, cancel := context.WithTimeout(ctx, timeout)
		execCmd := convertHealthCheckTest(hc.Test)
		err := o.config.Containers.Exec(execCtx, containerName, execCmd)
		cancel()
		if err == nil {
			return nil
		}
		o.logger.Info("container health check attempt failed", "container", containerName, "attempt", attempt+1, "retries", retries, "error", err)
	}
	return fmt.Errorf("container health check failed after %d attempts for %q", retries, containerName)
}

// buildAppState constructs a configurator.AppState for the given app ID
// using catalog metadata when available.
func (o *Orchestrator) buildAppState(id string) (*configurator.AppState, error) {
	state := &configurator.AppState{
		DataPath:      filepath.Join(o.dataDir, o.ownerApp(id)),
		BloudDataPath: o.dataDir,
	}

	if o.catalog == nil {
		return state, nil
	}

	catalogApp, err := o.catalog.Get(id)
	if (err != nil || catalogApp == nil) && o.ownerApp(id) != id {
		catalogApp, err = o.catalog.Get(o.ownerApp(id))
	}
	if err != nil || catalogApp == nil {
		return state, nil
	}

	ssoEnabled := catalogApp.SSO.Strategy != "" && catalogApp.SSO.Strategy != "none"
	if ssoEnabled {
		o.logger.Info("SSO enabled for app", "app", id, "strategy", catalogApp.SSO.Strategy)
		switch catalogApp.SSO.Strategy {
		case "ldap":
			if o.config.LDAPOutput != nil {
				state.LDAP = o.config.LDAPOutput
			}
		case "native-oidc":
			if inputs := o.oidcInputsForApp(catalogApp); inputs != nil && len(inputs.RedirectURIs) > 0 {
				state.OIDC = &configurator.OIDCOutput{
					ClientID:     inputs.ClientID,
					ClientSecret: inputs.ClientSecret,
					IssuerURL:    inputs.IssuerURL,
					RedirectURI:  inputs.RedirectURIs[0],
				}
			}
		}
	}

	return state, nil
}

// ensureSSO provisions the per-app SSO provider in the identity provider for
// apps that use forward-auth or native-oidc SSO. It is a no-op when SSO is not
// configured or the app's strategy is not provisioned in the identity provider
// (e.g. "ldap", which is provisioned by the LDAP outpost). Safe to call on
// every lifecycle pass (idempotent).
func (o *Orchestrator) ensureSSO(ctx context.Context, id string) error {
	if o.sso == nil || o.ssoBaseURL == "" || o.catalog == nil {
		return nil
	}
	// Use the owning app's catalog ID for subdomain and provider name. Graph
	// nodes are container names (e.g. "apps-navidrome") for apps defined with a
	// containers list, while routing and Authentik must use the catalog ID
	// ("navidrome") so forward-auth matches the app's real subdomain.
	appID := o.ownerApp(id)
	catalogApp, err := o.catalog.Get(appID)
	if err != nil || catalogApp == nil {
		return nil
	}
	// SSO is an app-level concern: provision it exactly once, on the app's
	// primary container node. The inter-app dependency edges are attached to
	// the same node, so it runs after SSO dependencies (e.g. Authentik) are
	// ready. Non-primary nodes (postgres, redis, ...) skip it.
	if o.primaryContainerNode(appID) != id {
		return nil
	}

	switch catalogApp.SSO.Strategy {
	case "forward-auth":
		o.logger.Info("provisioning forward-auth SSO", "app", appID)
		externalURL := buildAppSubdomainURL(o.ssoBaseURL, appID)
		return o.sso.EnsureForwardAuth(appID, catalogApp.DisplayName, externalURL)

	case "native-oidc":
		if o.ssoHostSecret == "" || o.ssoAuthentikURL == "" {
			o.logger.Warn("native-oidc SSO skipped: missing SSO host secret or Authentik URL", "app", appID)
			return nil
		}
		inputs := o.oidcInputsForApp(catalogApp)
		if inputs == nil || len(inputs.RedirectURIs) == 0 {
			return fmt.Errorf("building OIDC inputs for %q", appID)
		}
		o.logger.Info("provisioning native-oidc SSO", "app", appID)
		return o.sso.EnsureNativeOIDC(appID, catalogApp.DisplayName, inputs.ClientID, inputs.ClientSecret, inputs.RedirectURIs, inputs.LaunchURL)
	}

	return nil
}

// oidcInputsForApp computes the deterministic OIDC inputs for a native-oidc
// app from the configured base URL and host secret. Returns nil when the SSO
// base URL or host secret is not configured.
func (o *Orchestrator) oidcInputsForApp(catalogApp *catalog.App) *sso.OIDCInputs {
	if o.ssoBaseURL == "" || o.ssoHostSecret == "" {
		return nil
	}
	gen := sso.NewBlueprintGenerator(
		o.ssoHostSecret,
		"",
		netutil.BuildBaseURLs(o.ssoBaseURL),
		o.ssoAuthentikURL,
		o.ssoIssuerURL,
		"", // no blueprints dir: provisioning goes through the identity provider API
		nil,
	)
	return gen.OIDCInputsForApp(catalogApp)
}

// buildAppSubdomainURL constructs the app's subdomain URL from a base URL.
// e.g., "http://localhost:8080" + "navidrome" → "http://navidrome.localhost:8080"
func buildAppSubdomainURL(baseURL, appName string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	parsed.Host = appName + "." + parsed.Host
	return parsed.String()
}
