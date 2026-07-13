package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
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
	Event  string    `json:"event"`  // "intent_enqueued", "drain_complete", "converge_start", "converge_step", "converge_complete"
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
	TraefikGen       traefikgen.GeneratorInterface
	ActiveTailnetID  func() string // returns the active tailnet connection ID (empty if none)
}

// Orchestrator drives app nodes through their lifecycle phases in dependency
// order, processing nodes within each level concurrently.
//
// Lifecycle phases per node:
//
//	INITIALIZING → PRESTART_CONFIG → STARTING → POSTSTART_CONFIG → RUNNING
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
	queue          *IntentQueue
	appStore       store.AppStoreInterface
	catalogGraph   catalog.AppGraphInterface
	tailnetStore   store.TailnetStoreInterface
	remoteAppStore store.RemoteAppStoreInterface
	tailnetNode    TailnetNodeEnsurer
	gateway        GatewayManager
	remoteProxy    RemoteProxyManager
	proxyOutpost   ProxyOutpostEnsurer
	forwardDomainSSO ForwardDomainProvisioner
	sso              SSOProvisioner
	ssoBaseURL       string
	traefikGen       traefikgen.GeneratorInterface
	activeTailnetID  func() string

	// Start/Stop lifecycle
	cancel  context.CancelFunc
	started chan struct{}
	done    chan struct{}
	once    sync.Once

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
		traefikGen:       config.TraefikGen,
		activeTailnetID:  config.ActiveTailnetID,
		queue:            NewIntentQueue(DefaultDebounce),
		started:          make(chan struct{}),
		done:             make(chan struct{}),
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
		switch node.ActualStatus {
		case graph.StatusRunning:
			_ = o.appStore.UpdateStatus(node.ID, "running")
		case graph.StatusError:
			_ = o.appStore.UpdateStatus(node.ID, "error")
		}
	})
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

// Start runs the intent processing loop. It blocks until the context is cancelled
// or Stop is called. Must be called exactly once (typically via goroutine).
func (o *Orchestrator) Start(ctx context.Context) {
	ctx, o.cancel = context.WithCancel(ctx)
	close(o.started)
	defer close(o.done)

	o.logger.Info("orchestrator intent loop started")

	for {
		intents := o.queue.WaitAndDrain(ctx)
		if intents == nil {
			o.logger.Info("orchestrator intent loop stopped")
			return
		}

		o.logger.Info("processing intents", "count", len(intents))
		o.converge(ctx, intents)

		if ctx.Err() != nil {
			o.logger.Info("orchestrator intent loop stopped")
			return
		}
	}
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

	// ConvergeIntent is a trigger-only intent: it carries no store mutations and
	// exists solely to wake the convergence loop. Filter it out before the drain
	// phase so applyIntents only sees intents that actually mutate state.
	var drainIntents []Intent
	for _, intent := range intents {
		if _, ok := intent.(ConvergeIntent); !ok {
			drainIntents = append(drainIntents, intent)
		}
	}
	o.applyIntents(drainIntents, pendingClearData)
	o.recordActivity("drain_complete", fmt.Sprintf("%d", len(intents)))

	o.convergeFromStores(ctx, pendingClearData)
	o.recordActivity("converge_complete", fmt.Sprintf("%d intents, %s", len(intents), time.Since(start).Round(time.Millisecond)))
}

// Startup runs pre-reconcile initialisation: syncs actual container state into
// the graph so the first Reconcile pass has accurate information.
func (o *Orchestrator) Startup(ctx context.Context) error {
	o.logger.Info("startup: syncing container state")
	o.SyncContainerState(ctx)
	o.logger.Info("startup complete")
	return nil
}

// RemoveApp calls NodeLifecycle.Remove for the named app (if a configurator is
// registered), then deletes the graph node.
func (o *Orchestrator) RemoveApp(ctx context.Context, appName string, clearData bool) error {
	o.logger.Info("removing app", "app", appName, "clear_data", clearData)
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

	if err := o.RegenerateRoutes(); err != nil {
		o.logger.Warn("route regeneration failed", "error", err)
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
//     and nodes whose dependencies are not yet RUNNING).
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
			// Node needs to progress. Only proceed if all deps are RUNNING
			// (they are resolved in the level before this one — if any dep
			// ended up in ERROR, block this node as well).
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
				if depNode.ActualStatus != graph.StatusRunning {
					blocked = true
					blockingDep = dep
					break
				}
			}
			if blocked {
				o.logger.Info("skipping blocked node", "app", id, "actual", node.ActualStatus, "target", node.TargetStatus, "blocking_dep", blockingDep)
			} else {
				o.logger.Info("queuing node for lifecycle", "app", id, "actual", node.ActualStatus, "target", node.TargetStatus)
				work = append(work, id)
			}
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

// ensureAppContainer creates the container for an app using its catalog spec.
// If forceRestart is true, any existing container is removed first.
// A nil containers runtime or missing catalog spec is a no-op.
func (o *Orchestrator) ensureAppContainer(ctx context.Context, id string, forceRestart bool) error {
	if o.config.Containers == nil || o.catalog == nil {
		return nil
	}
	catalogApp, err := o.catalog.Get(id)
	if err != nil || catalogApp == nil || catalogApp.Container == nil {
		return nil
	}
	o.logger.Info("ensuring app container", "app", id, "force_restart", forceRestart)
	spec, err := ContainerSpec(catalogApp, o.dataDir, o.config.TemplateVars)
	if err != nil {
		return fmt.Errorf("build container spec: %w", err)
	}
	if forceRestart {
		if err := o.config.Containers.Remove(ctx, spec.Name); err != nil {
			return fmt.Errorf("remove container: %w", err)
		}
	}
	if spec.Network != "" {
		if err := o.config.Containers.EnsureNetwork(ctx, spec.Network); err != nil {
			o.logger.Warn("failed to ensure network", "app", id, "network", spec.Network, "error", err)
		}
	}
	for _, mount := range spec.Mounts {
		if err := os.MkdirAll(mount.Source, 0755); err != nil {
			o.logger.Warn("failed to create mount directory", "app", id, "path", mount.Source, "error", err)
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

	cfg := o.registry.Get(id)

	// No configurator: nothing to configure; mark RUNNING directly.
	if cfg == nil {
		o.logger.Info("no configurator registered, marking RUNNING", "app", id)
		_ = o.graph.SetActualStatus(id, graph.StatusRunning, "")
		return true
	}

	state, err := o.buildAppState(id)
	if err != nil {
		o.logger.Error("failed to build app state", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		return false
	}

	// Phase 1: PreStart
	o.logger.Info("lifecycle phase: PreStart", "app", id)
	_ = o.graph.SetActualStatus(id, graph.StatusPreStartConfig, "")
	changed, err := cfg.PreStart(ctx, state)
	if err != nil {
		o.logger.Warn("PreStart failed", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		return false
	}
	o.logger.Info("lifecycle phase: PreStart complete", "app", id, "config_changed", changed)

	// SSO provisioning: ensure the forward-auth provider exists in Authentik before the
	// container starts, so requests can be authenticated immediately on first boot.
	if err := o.ensureSSO(ctx, id); err != nil {
		o.logger.Warn("SSO provisioning failed", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		return false
	}

	// Phase 2: EnsureContainer (forceRestart if PreStart produced a config change)
	o.logger.Info("lifecycle phase: EnsureContainer", "app", id, "force_restart", changed)
	_ = o.graph.SetActualStatus(id, graph.StatusStarting, "")
	if err := o.ensureAppContainer(ctx, id, changed); err != nil {
		o.logger.Warn("EnsureContainer (catalog spec) failed", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		return false
	}
	if err := cfg.EnsureContainer(ctx, changed); err != nil {
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
	if err := cfg.HealthCheck(healthCtx); err != nil {
		o.logger.Warn("HealthCheck failed", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		return false
	}
	o.logger.Info("lifecycle phase: HealthCheck complete", "app", id)

	// Phase 4: PostStart
	o.logger.Info("lifecycle phase: PostStart", "app", id)
	_ = o.graph.SetActualStatus(id, graph.StatusPostStartConfig, "")
	if err := cfg.PostStart(ctx, state); err != nil {
		o.logger.Warn("PostStart failed", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		return false
	}
	o.logger.Info("lifecycle phase: PostStart complete", "app", id)

	o.logger.Info("node reached RUNNING", "app", id)
	_ = o.graph.SetActualStatus(id, graph.StatusRunning, "")
	return true
}

// buildAppState constructs a configurator.AppState for the given app ID
// using catalog metadata when available.
func (o *Orchestrator) buildAppState(id string) (*configurator.AppState, error) {
	state := &configurator.AppState{
		DataPath:      filepath.Join(o.dataDir, id),
		BloudDataPath: o.dataDir,
	}

	if o.catalog == nil {
		return state, nil
	}

	catalogApp, err := o.catalog.Get(id)
	if err != nil {
		return nil, fmt.Errorf("get catalog app: %w", err)
	}
	if catalogApp == nil {
		return state, nil
	}

	ssoEnabled := catalogApp.SSO.Strategy != "" && catalogApp.SSO.Strategy != "none"
	state.SSOEnabled = ssoEnabled
	if ssoEnabled {
		o.logger.Info("SSO enabled for app", "app", id, "strategy", catalogApp.SSO.Strategy)
		if catalogApp.SSO.Strategy == "ldap" && o.config.LDAPOutput != nil {
			state.LDAP = o.config.LDAPOutput
		}
	}

	return state, nil
}

// ensureSSO provisions the forward-auth provider in Authentik for apps that use
// forward-auth SSO. It is a no-op when SSO is not configured or the app does not
// use forward-auth. Safe to call on every lifecycle pass (idempotent).
func (o *Orchestrator) ensureSSO(ctx context.Context, id string) error {
	if o.sso == nil || o.ssoBaseURL == "" || o.catalog == nil {
		return nil
	}
	catalogApp, err := o.catalog.Get(id)
	if err != nil || catalogApp == nil {
		return nil
	}
	if catalogApp.SSO.Strategy != "forward-auth" {
		return nil
	}
	o.logger.Info("provisioning forward-auth SSO", "app", id)
	externalURL := buildAppSubdomainURL(o.ssoBaseURL, id)
	return o.sso.EnsureForwardAuth(id, catalogApp.DisplayName, externalURL)
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
