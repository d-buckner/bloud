package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// RouteRegenerator regenerates routing rules (e.g. Traefik config) after a
// convergence pass completes.
type RouteRegenerator interface {
	RegenerateRoutes() error
}

// ContainerStateSyncer syncs the actual container state into the lifecycle graph
// at startup, before intent processing begins.
type ContainerStateSyncer interface {
	SyncContainerState(ctx context.Context)
}

// OrchestratorConfig holds tunable parameters for the Orchestrator.
type OrchestratorConfig struct {
	// HealthCheckTimeout limits how long each app's HealthCheck can run.
	// Zero means no timeout (the caller's context deadline applies).
	HealthCheckTimeout time.Duration

	// LDAPOutput is the LDAP provider endpoint injected into apps with
	// LDAP SSO strategy. Nil when no LDAP provider is configured.
	LDAPOutput *configurator.LDAPOutput
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
	graph          *graph.Graph
	registry       configurator.RegistryInterface
	catalog        catalog.CacheInterface
	dataDir        string
	logger         *slog.Logger
	config         OrchestratorConfig
	routeGenerator RouteRegenerator     // optional; called at end of Reconcile
	stateSyncer    ContainerStateSyncer // optional; called by Startup

	// Intent processing fields (Cycle 4)
	queue          *IntentQueue
	appStore       store.AppStoreInterface
	catalogGraph   catalog.AppGraphInterface
	tailnetStore   store.TailnetStoreInterface
	remoteAppStore store.RemoteAppStoreInterface
	tailnetNode    TailnetNodeEnsurer
	gateway        GatewayEnsurer
	proxyStopper   ProxyStopper
	proxyOutpost   ProxyOutpostEnsurer
	tailnetDomain  TailnetDomainDiscoverer
	forwardDomainSSO ForwardDomainProvisioner

	// Start/Stop lifecycle
	cancel  context.CancelFunc
	started chan struct{}
	done    chan struct{}
	once    sync.Once
}

// NewOrchestrator creates an Orchestrator backed by the provided graph.
// catalogCache may be nil; when nil, SSO detection is disabled.
func NewOrchestrator(
	g *graph.Graph,
	registry configurator.RegistryInterface,
	catalogCache catalog.CacheInterface,
	dataDir string,
	logger *slog.Logger,
	config OrchestratorConfig,
) *Orchestrator {
	return &Orchestrator{
		graph:    g,
		registry: registry,
		catalog:  catalogCache,
		dataDir:  dataDir,
		logger:   logger,
		config:   config,
		queue:    NewIntentQueue(DefaultDebounce),
		started:  make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Enqueue adds an intent to the orchestrator's queue for processing.
func (o *Orchestrator) Enqueue(intent Intent) {
	o.logger.Info("intent enqueued", "type", intentTypeName(intent), "id", intent.IntentID())
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

	pendingClearData := make(map[string]bool)
	o.applyIntents(intents, pendingClearData)
	o.convergeFromStores(ctx, pendingClearData)
}

// WithRouteGenerator sets the RouteRegenerator called at the end of each
// Reconcile pass. Returns the receiver for chaining.
func (o *Orchestrator) WithRouteGenerator(rg RouteRegenerator) *Orchestrator {
	o.routeGenerator = rg
	return o
}

// WithStateSyncer sets the ContainerStateSyncer called by Startup.
// Returns the receiver for chaining.
func (o *Orchestrator) WithStateSyncer(ss ContainerStateSyncer) *Orchestrator {
	o.stateSyncer = ss
	return o
}

// Startup runs pre-reconcile initialisation: syncs actual container state into
// the graph so the first Reconcile pass has accurate information.
func (o *Orchestrator) Startup(ctx context.Context) error {
	if o.stateSyncer != nil {
		o.stateSyncer.SyncContainerState(ctx)
	}
	return nil
}

// RemoveApp calls NodeLifecycle.Remove for the named app (if a configurator is
// registered), then deletes the graph node.
func (o *Orchestrator) RemoveApp(ctx context.Context, appName string, clearData bool) error {
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

	changedIDs := make(map[string]bool)

	for _, level := range levels {
		if err := o.processLevel(ctx, level, changedIDs); err != nil {
			return err
		}
	}

	if o.routeGenerator != nil {
		if err := o.routeGenerator.RegenerateRoutes(); err != nil {
			o.logger.Warn("route regeneration failed", "error", err)
		}
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
			for _, dep := range deps {
				depNode, err := o.graph.GetNode(dep)
				if err != nil || depNode == nil {
					continue
				}
				if depNode.ActualStatus != graph.StatusRunning {
					blocked = true
					break
				}
			}
			if !blocked {
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
		o.runPostStartOnly(ctx, id)
		return false // staleness re-runs don't propagate changedIDs further
	}

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
	if err := cfg.PostStart(ctx, state); err != nil {
		o.logger.Warn("staleness re-run: PostStart failed", "app", id, "error", err)
	}
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
	_ = o.graph.SetActualStatus(id, graph.StatusPreStartConfig, "")
	changed, err := cfg.PreStart(ctx, state)
	if err != nil {
		o.logger.Warn("PreStart failed", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		return false
	}

	// Phase 2: EnsureContainer (forceRestart if PreStart produced a config change)
	_ = o.graph.SetActualStatus(id, graph.StatusStarting, "")
	if err := cfg.EnsureContainer(ctx, changed); err != nil {
		o.logger.Warn("EnsureContainer failed", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		return false
	}

	// Phase 3: HealthCheck
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

	// Phase 4: PostStart
	_ = o.graph.SetActualStatus(id, graph.StatusPostStartConfig, "")
	if err := cfg.PostStart(ctx, state); err != nil {
		o.logger.Warn("PostStart failed", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		return false
	}

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
	if ssoEnabled && catalogApp.SSO.Strategy == "ldap" && o.config.LDAPOutput != nil {
		state.LDAP = o.config.LDAPOutput
	}

	return state, nil
}
