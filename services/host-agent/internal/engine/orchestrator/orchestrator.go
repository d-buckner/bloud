// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const maxOrchestratorEvents = 20

// DefaultAppPhaseBudget bounds one configurator phase -- PreStart or PostStart
// -- for one app on one pass, when the configured
// OrchestratorConfig.AppPhaseBudget is zero.
//
// It is configurator.PhaseBudget rather than a number of the orchestrator's own
// choosing so the ceiling apps compile against is the same value the framework
// enforces, and the apps/configtest harness rule that checks every declared
// wait can read it from the same place. See that constant for why the unit is
// the app and why each phase gets its own full allowance.
const DefaultAppPhaseBudget = configurator.PhaseBudget

// OrchestratorStatus is a snapshot of the orchestrator's current state for
// the developer API.
type OrchestratorStatus struct {
	QueueDepth     int             `json:"queueDepth"`
	IsConverging   bool            `json:"isConverging"`
	RecentActivity []ActivityEvent `json:"recentActivity"`
	// LoopStopped reports the intent loop has exited. A dead loop is
	// otherwise indistinguishable from an idle one: every Submit answers
	// 202 while nothing reconciles.
	LoopStopped bool `json:"loopStopped"`
	// LastConverged is when the most recent convergence pass completed
	// (zero until the first one finishes).
	LastConverged time.Time `json:"lastConverged"`
	// ResyncRestartSignals lists the nodes whose consecutive resync restarts
	// crossed the warning threshold: they keep reporting a config change that
	// never settles. The restarts still happen; this is the record that says
	// something is wrong. Empty when no node has crossed the threshold.
	ResyncRestartSignals []ResyncRestartSignal `json:"resyncRestartSignals,omitempty"`
	// ResyncCostSignals lists the nodes whose config resync keeps exceeding the
	// cost budget. Nothing is failing on those nodes, which is exactly why they
	// need a signal of their own: a no-op that costs seconds every pass is a slow
	// instance, not a broken one, and it is invisible to every other watch.
	// Empty when no node has crossed the threshold.
	ResyncCostSignals []ResyncCostSignal `json:"resyncCostSignals,omitempty"`
}

// ActivityEvent records a single orchestrator lifecycle event.
type ActivityEvent struct {
	Time   time.Time `json:"time"`
	Event  string    `json:"event"` // "intent_enqueued", "drain_complete", "converge_start", "converge_step", "converge_complete"
	Detail string    `json:"detail"`
}

// OrchestratorConfig is the complete set of tunable parameters and optional
// dependencies for the Orchestrator, grouped by subsystem. Nil/zero fields
// disable the corresponding subsystem. All fields are set at construction time
// via NewOrchestrator. The subsystem structs live in config.go.
type OrchestratorConfig struct {
	Tuning  TuningConfig
	Runtime RuntimeConfig
	Stores  StoresConfig
	SSO     SSOConfig
	Hosts   HostsConfig
	// CatalogGraph is the install/remove planner. Nil disables planning.
	CatalogGraph catalog.AppGraphInterface
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
//     until its status is explicitly reset. Two things reset it: an install
//     intent (the user's retry) and the periodic self-healing pass, when the
//     recorded failure is marked retryable. See retryErroredNodes.
//   - A node whose dependency is in ERROR is also skipped (blocked).
//
// Staleness: a node that is already RUNNING is not assumed to still match the
// catalog or its providers. Every pass re-runs its PreStart and PostStart (the
// resync), and the container is recreated only when PreStart reports the config
// actually changed. See runResync.
type Orchestrator struct {
	// Core lifecycle fields
	graph    *graph.Graph
	registry configurator.RegistryInterface
	catalog  catalog.CacheInterface
	dataDir  string
	logger   *slog.Logger
	config   OrchestratorConfig
	// secrets resolves a provider's published credentials into an integration
	// binding. Nil when no store is configured.
	secrets configurator.AppSecretsProvider

	// Intent processing fields
	queue         *IntentQueue
	events        *eventbus.Bus
	appStore      store.AppStoreInterface
	catalogGraph  catalog.AppGraphInterface
	sso           SSOProvisioner
	ssoBaseURL    string
	ssoHostSecret string
	// ssoAuthentikURL is the browser-accessible Authentik URL used for OIDC
	// discovery; ssoIssuerURL is the issuer reachable from app containers.
	ssoAuthentikURL string
	ssoIssuerURL    string
	traefikGen      traefikgen.GeneratorInterface

	// Live address state (nil = legacy single-URL mode from config).
	hosts          *hostset.State
	settings       store.SettingsStoreInterface
	externalApps   store.ExternalAppStoreInterface
	onHostsChanged func()

	// sessionRevokes carries session-revoke requests from the drain phase to
	// the convergence pass that performs them. See session_revoke.go.
	sessionRevokes *revokeTracker

	// Start/Stop lifecycle
	cancel  context.CancelFunc
	started chan struct{}
	ready   chan struct{}
	done    chan struct{}
	once    sync.Once

	// healWake is the one-slot mailbox the self-healing timer waits on:
	// every completed convergence pass pings it, which is how the timer
	// stays idle-based instead of running on a fixed schedule.
	healWake chan struct{}

	// containerOwner maps container node names to their owning app catalog ID.
	// Used for multi-container apps where node names differ from app catalog IDs.
	// e.g. "apps-authentik-server" → "authentik"
	containerOwner map[string]string

	// Activity log for the developer API
	activityMu  sync.Mutex
	activityBuf [maxOrchestratorEvents]ActivityEvent
	activityPos int
	converging  atomic.Bool
	// lastConverged holds the completion time of the most recent
	// convergence pass (nil until the first). Together with Stopped it
	// lets the health and developer surfaces tell a dead loop from a
	// healthy idle one.
	lastConverged atomic.Pointer[time.Time]

	// resync restart watchdog: raises a signal when one node keeps restarting
	// from the resync. It never withholds a restart. Built lazily so a
	// hand-constructed Orchestrator has a working one. See resync_watchdog.go.
	resyncWatchOnce  sync.Once
	resyncWatchValue *resyncWatch

	// resync cost watch: raises a signal when one node's resync keeps costing
	// more than the no-op bargain allows. Also observes only. See resync_cost.go.
	resyncCostOnce  sync.Once
	resyncCostValue *resyncCostWatch
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
		graph:           g,
		registry:        registry,
		catalog:         catalogCache,
		dataDir:         dataDir,
		logger:          logger,
		config:          config,
		appStore:        config.Stores.AppStore,
		secrets:         config.Stores.Secrets,
		catalogGraph:    config.CatalogGraph,
		sso:             config.SSO.SSO,
		ssoBaseURL:      config.SSO.SSOBaseURL,
		ssoHostSecret:   config.SSO.SSOHostSecret,
		ssoAuthentikURL: config.SSO.SSOAuthentikURL,
		ssoIssuerURL:    config.SSO.SSOIssuerURL,
		traefikGen:      config.Runtime.TraefikGen,
		hosts:           config.Hosts.Hosts,
		settings:        config.Stores.Settings,
		externalApps:    config.Stores.ExternalApps,
		onHostsChanged:  config.Hosts.OnHostsChanged,
		queue:           NewIntentQueue(DefaultDebounce),
		events:          config.Tuning.Events,
		started:         make(chan struct{}),
		ready:           make(chan struct{}),
		done:            make(chan struct{}),
		healWake:        make(chan struct{}, 1),
		containerOwner:  make(map[string]string),
	}
	o.setupStatusSync()
	o.setupNodeEvents()
	o.setupPullEvents()
	return o
}

// Enqueue adds an intent to the orchestrator's queue for processing.
func (o *Orchestrator) Enqueue(intent Intent) {
	o.logger.Info("intent enqueued", "type", intentTypeName(intent), "id", intent.IntentID())
	o.recordActivity("intent_enqueued", intentTypeName(intent))
	o.queue.Enqueue(intent)
}

// Submit is the handler-facing entry point: it records the intent's
// immediate user-visible effect on the store, then enqueues it for
// reconciliation. For installs the app row exists (status "installing")
// before this returns, so the API can include it in the 202 response and
// the dashboard shows the tile without waiting for the reconciler. The
// orchestrator remains the sole store writer (invariant #1): handlers only
// ever call Submit, never the stores directly.
func (o *Orchestrator) Submit(intent Intent) {
	if i, ok := intent.(InstallAppIntent); ok {
		o.recordInstallNow(i.AppName)
	}
	// External apps have no convergence to wait for, so the add records the
	// row before enqueuing: the 202 response carries it and the tile appears
	// without a drain round trip. The drain re-applies it idempotently.
	if i, ok := intent.(AddExternalAppIntent); ok {
		o.applyAddExternalAppIntent(i)
	}
	o.Enqueue(intent)
}

// recordInstallNow upserts the app row with status "installing" (clearing
// last_error) before reconciliation picks the intent up. It is a plain
// catalog-based upsert: the drain pass's recordIntent re-upserts the row
// with the resolved integration config, so both writes are idempotent and
// the reconciler sees no behavior change.
func (o *Orchestrator) recordInstallNow(appName string) {
	if o.appStore == nil || o.catalog == nil {
		return
	}
	app, err := o.catalog.Get(appName)
	if err != nil || app == nil {
		o.logger.Warn("submit: app not in catalog, row will be recorded on drain", "app", appName, "error", err)
		return
	}
	// Idempotent reinstall of a running app: keep the status "running" so
	// the drain path's skip check (applyInstallIntent) still recognizes the
	// intent as a no-op. Downgrading to "installing" here would make the
	// drain run a full install that never transitions the graph node, and
	// the app would be stuck at "installing".
	if existing, _ := o.appStore.GetByCatalogID(appName); existing != nil && existing.Status == store.AppStatusRunning {
		return
	}
	if err := o.appStore.Install(app.CatalogID, app.DisplayName, app.Version, nil, &store.InstallOptions{
		Port:     app.Port,
		IsSystem: app.IsSystem,
	}); err != nil {
		o.logger.Error("submit: failed to record install row", "app", appName, "error", err)
		return
	}
	o.recordOpStart(appName, store.OpTypeInstall, store.OpPhasePlanning)
}

// Start runs an initial convergence pass and then processes intents as they
// arrive. It blocks until the context is canceled or Stop is called. Must be
// called exactly once (typically via goroutine).
func (o *Orchestrator) Start(ctx context.Context) {
	ctx, o.cancel = context.WithCancel(ctx)
	close(o.started)
	defer close(o.done)

	o.logger.Info("orchestrator started")

	// A 'running' operation row that survived a process restart means
	// the drive died mid-phase (phase writes happen before entering the
	// phase). Flip those to failed/retryable for diagnostic continuity;
	// normal convergence re-drives the work.
	if o.config.Stores.Operations != nil {
		if n, err := o.config.Stores.Operations.MarkOrphansInterrupted(); err != nil {
			o.logger.Error("operation recorder: orphan sweep failed", "error", err)
		} else if n > 0 {
			o.logger.Warn("operation recorder: flipped interrupted operations", "count", n)
		}
	}

	// Initial convergence (blocks until system apps are up).
	o.converge(ctx, nil)
	close(o.ready)

	// Arm the periodic pass after the boot pass, not before: the boot pass
	// is the activity the idle timer measures from, and arming it earlier
	// would let a tick land in the middle of bootstrap. It runs in its own
	// goroutine but submits into this same queue, so the single-writer rule
	// holds: there is still exactly one caller of converge.
	go o.startSelfHealing(ctx)

	for {
		intents, live := o.queue.WaitAndDrain(ctx)
		if !live {
			o.logger.Info("orchestrator stopped")
			return
		}
		if len(intents) == 0 {
			// A stale signal token can wake the wait with an empty queue
			// (see IntentQueue.WaitAndDrain). The loop survives; only a
			// canceled context stops it.
			continue
		}
		o.converge(ctx, intents)
	}
}

// Ready returns a channel that is closed after the first convergence pass completes.
func (o *Orchestrator) Ready() <-chan struct{} {
	return o.ready
}

// Stopped reports whether the intent loop has exited. While Start runs
// (idle or converging) it returns false; once Start has returned it
// returns true, so the health surface can see a dead loop instead of
// mistaking it for an idle one.
func (o *Orchestrator) Stopped() bool {
	select {
	case <-o.done:
		return true
	default:
		return false
	}
}

// LastConverged returns when the most recent convergence pass finished,
// or the zero time if none has.
func (o *Orchestrator) LastConverged() time.Time {
	if t := o.lastConverged.Load(); t != nil {
		return *t
	}
	return time.Time{}
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
	// Every exit from a pass counts as activity for the self-healing
	// timer, including the stub exit below: the pass either did the work or
	// had nothing to do, and either way the next one is due a full interval
	// later. Deferred first so it runs last, after the converging flag has
	// cleared.
	defer o.signalConverged()

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
	now := time.Now()
	o.lastConverged.Store(&now)
	o.recordActivity("converge_complete", fmt.Sprintf("%d intents, %s", len(intents), time.Since(start).Round(time.Millisecond)))
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

	// Sync routes now that all lifecycle phases are complete, so the UI never
	// shows an app as "installed" before its Traefik routes are live.
	if err := o.SyncRoutes(); err != nil {
		o.logger.Warn("route sync failed", "error", err)
	}

	for id := range changedIDs {
		o.logger.Info("marking node RUNNING after route generation", "app", id)
		_ = o.graph.SetActualStatus(id, graph.StatusRunning, "")
	}

	return nil
}
