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
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const maxOrchestratorEvents = 20

// DefaultPostStartBudget bounds a node's PostStart finalization when the
// configured OrchestratorConfig.PostStartBudget is zero.
//
// It is appclient.MaxWaitBudget rather than a smaller number of its own
// choosing: an app's declared readiness wait (appclient.Within) has to fit under
// this ceiling or the framework cancels it before its own deadline can fire. The
// two constants are deliberately the same value, pinned by the apps/configtest
// harness rule that checks every declared wait against MaxWaitBudget.
const DefaultPostStartBudget = appclient.MaxWaitBudget

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
	// PostStartBudget bounds how long a node's PostStart finalization wait may run
	// before the framework cancels it, so the budget is uniform across apps and
	// Stop() can still interrupt the call. Zero means use DefaultPostStartBudget.
	PostStartBudget time.Duration

	// LDAPOutput is the LDAP provider endpoint injected into apps with
	// LDAP SSO strategy. Nil when no LDAP provider is configured.
	LDAPOutput *configurator.LDAPOutput

	// SelfHealInterval is how long the instance may sit without a
	// convergence pass before the self-healing timer submits one. Zero
	// means no periodic pass: the value has to be asked for, so a
	// hand-built orchestrator (every unit test) stays quiet, while
	// wire.Build always supplies DefaultSelfHealInterval unless the
	// deployment set BLOUD_RECONCILE_INTERVAL. See startSelfHealing.
	SelfHealInterval time.Duration

	// ── Container runtime ────────────────────────────────────────────────

	// Containers is the container runtime used to create app containers from
	// catalog specs. Nil disables catalog-driven container creation.
	Containers containerruntime.Runtime

	// TemplateVars are the extra variables container-spec templates render
	// with (postgresPassword and the authentik values). It is a store, not a
	// bare map, because one of its values is written at runtime by the
	// authentik configurator while the orchestrator reads the rest.
	TemplateVars *configurator.TemplateVars

	// ── Converge dependencies (nil = subsystem disabled) ─────────────────

	// Events is the bus used to broadcast lifecycle transitions and activity
	// to API subscribers (SSE). Nil disables event publishing.
	Events *eventbus.Bus

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
	// TraefikPort is the port the public entrypoint listens on. LAN IP base URLs
	// are built on it: the primary host's URL port describes the public origin,
	// not the socket a client on the LAN reaches. See HostSet.AllBaseURLs.
	TraefikPort     int
	TraefikGen      traefikgen.GeneratorInterface
	ActiveTailnetID func() string // returns the active tailnet connection ID (empty if none)

	// Secrets is the host secret store. Integration bindings resolve a
	// provider's published credentials from it (configurator.AppSecretsProvider),
	// so a consumer never reads a provider's files. Nil disables publishing:
	// bindings still carry the provider's identity and address.
	Secrets configurator.AppSecretsProvider

	// Hosts is the live address state. When non-nil it supersedes the
	// SSOBaseURL/SSOAuthentikURL/SSOIssuerURL strings above.
	Hosts *hostset.State
	// Settings persists the instance-level settings, including the public
	// address (nil = not supported).
	Settings store.SettingsStoreInterface
	// OnHostsChanged fires after a SetPublicURL intent is applied (e.g. to
	// re-ensure the dashboard OAuth app with the new redirect URIs).
	OnHostsChanged func()

	// Operations persists durable lifecycle operation state
	// (current-or-last drive per app). Nil disables the recorder.
	Operations *store.OperationStore
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
	// secrets resolves a provider's published credentials into an integration
	// binding. Nil when no store is configured.
	secrets configurator.AppSecretsProvider

	// Intent processing fields
	queue            *IntentQueue
	events           *eventbus.Bus
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

	// Live address state (nil = legacy single-URL mode from config).
	hosts          *hostset.State
	settings       store.SettingsStoreInterface
	onHostsChanged func()

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
		secrets:          config.Secrets,
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
		hosts:            config.Hosts,
		settings:         config.Settings,
		onHostsChanged:   config.OnHostsChanged,
		queue:            NewIntentQueue(DefaultDebounce),
		events:           config.Events,
		started:          make(chan struct{}),
		ready:            make(chan struct{}),
		done:             make(chan struct{}),
		healWake:         make(chan struct{}, 1),
		containerOwner:   make(map[string]string),
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
	if existing, _ := o.appStore.GetByCatalogID(appName); existing != nil && existing.Status == "running" {
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
	if o.config.Operations != nil {
		if n, err := o.config.Operations.MarkOrphansInterrupted(); err != nil {
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

	// Sync routes now that all lifecycle phases are complete: the
	// runtime steps (gateway, remote proxies) run first, then the pure
	// config write. Deferring this, and the RUNNING promotion below,
	// ensures the UI never shows an app as "installed" before its
	// Traefik routes are live.
	if err := o.SyncRoutes(); err != nil {
		o.logger.Warn("route sync failed", "error", err)
	}

	for id := range changedIDs {
		o.logger.Info("marking node RUNNING after route generation", "app", id)
		_ = o.graph.SetActualStatus(id, graph.StatusRunning, "")
	}

	return nil
}
