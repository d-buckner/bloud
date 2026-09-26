// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/netutil"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sso"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const maxOrchestratorEvents = 20

// DefaultPostStartBudget bounds a node's PostStart finalization when the
// configured OrchestratorConfig.PostStartBudget is zero. It replaces the
// per-app detach-and-timeout the apps used to implement themselves.
const DefaultPostStartBudget = 150 * time.Second

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
	// PostStartBudget bounds how long a node's PostStart finalization wait may
	// run before the framework cancels it. It is the ceiling the apps used to
	// set themselves (jellyfin's 90s / homeassistant's per-app timeout) lifted
	// into the framework so the budget is uniform and Stop() can still interrupt
	// the call. Zero means use DefaultPostStartBudget.
	PostStartBudget time.Duration

	// LDAPOutput is the LDAP provider endpoint injected into apps with
	// LDAP SSO strategy. Nil when no LDAP provider is configured.
	LDAPOutput *configurator.LDAPOutput

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
	TraefikGen       traefikgen.GeneratorInterface
	ActiveTailnetID  func() string // returns the active tailnet connection ID (empty if none)

	// Secrets is the host secret store. Integration bindings resolve a
	// provider's published credentials from it (configurator.AppSecretsProvider),
	// so a consumer never reads a provider's files. Nil disables publishing:
	// bindings still carry the provider's identity and address.
	Secrets configurator.AppSecretsProvider

	// Hosts is the live host-set state (multi-host SSO). When non-nil it
	// supersedes the SSOBaseURL/SSOAuthentikURL/SSOIssuerURL strings above.
	Hosts *hostset.State
	// HostStore persists admin-configured custom hosts (nil = not supported).
	HostStore store.HostStoreInterface
	// OnHostsChanged fires after a SetHosts intent is applied (e.g. to
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

	// Multi-host SSO state (nil = legacy single-URL mode from config).
	hosts          *hostset.State
	hostStore      store.HostStoreInterface
	onHostsChanged func()

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
		hostStore:        config.HostStore,
		onHostsChanged:   config.OnHostsChanged,
		queue:            NewIntentQueue(DefaultDebounce),
		events:           config.Events,
		started:          make(chan struct{}),
		ready:            make(chan struct{}),
		done:             make(chan struct{}),
		containerOwner:   make(map[string]string),
	}
	o.setupStatusSync()
	o.setupNodeEvents()
	o.setupPullEvents()
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
				_ = o.appStore.SetLastError(appID, "")
				o.recordOpComplete(appID)
			case graph.StatusError:
				_ = o.appStore.UpdateStatus(appID, "error")
				_ = o.appStore.SetLastError(appID, node.Error)
			}
			return
		}
		// Multi-container node: aggregate across all containers.
		// Error fires immediately on any container; running only when all are up.
		switch node.ActualStatus {
		case graph.StatusError:
			_ = o.appStore.UpdateStatus(appID, "error")
			_ = o.appStore.SetLastError(appID, node.Error)
		case graph.StatusRunning:
			if o.allContainersRunning(appID) {
				_ = o.appStore.UpdateStatus(appID, "running")
				_ = o.appStore.SetLastError(appID, "")
				o.recordOpComplete(appID)
			}
		}
	})
}

// setupNodeEvents broadcasts lifecycle node transitions on the event bus so
// API subscribers (the SSE stream) can surface per-container progress and
// errors without polling.
func (o *Orchestrator) setupNodeEvents() {
	if o.events == nil {
		return
	}
	o.graph.On(graph.EventNodeUpdated, func(node graph.Node) {
		o.events.Publish(eventbus.Event{
			Type: eventbus.TypeNode,
			Node: &eventbus.NodeInfo{
				App:       o.ownerApp(node.ID),
				Container: node.ID,
				Phase:     phaseForStatus(node.ActualStatus),
				Error:     node.Error,
			},
		})
	})
}

// setupPullEvents wires image pull progress from the container runtime into
// the event bus so SSE subscribers see live pull percentages. Runtimes that
// don't implement PullProgressReporter (e.g. test doubles) are skipped.
func (o *Orchestrator) setupPullEvents() {
	if o.events == nil || o.config.Containers == nil {
		return
	}
	reporter, ok := o.config.Containers.(containerruntime.PullProgressReporter)
	if !ok {
		return
	}
	reporter.SetPullProgressReporter(func(containerName, image string, p containerruntime.PullProgress) {
		o.events.Publish(eventbus.Event{
			Type: eventbus.TypePull,
			Pull: &eventbus.PullInfo{
				App:     o.ownerApp(containerName),
				Image:   image,
				Phase:   p.Phase,
				Percent: p.Percent,
				Detail:  pullDetail(p),
			},
		})
	})
}

// pullDetail renders a user-facing pull progress detail, e.g.
// "34% (356.5 MiB of 1.0 GiB)", falling back to the raw status line when no
// sizes are known.
func pullDetail(p containerruntime.PullProgress) string {
	if p.Total > 0 {
		return fmt.Sprintf("%d%% (%s of %s)", p.Percent, humanBytes(p.Current), humanBytes(p.Total))
	}
	return p.Detail
}

// humanBytes formats a byte count as a compact binary unit (1.0 GiB).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// phaseForStatus maps a lifecycle graph state to the user-facing phase name
// shown in the dashboard.
func phaseForStatus(s graph.NodeStatus) string {
	switch s {
	case graph.StatusInitializing:
		return "queued"
	case graph.StatusPreStartConfig:
		return "configuring"
	case graph.StatusStarting:
		return "starting"
	case graph.StatusPostStartConfig:
		return "finalizing"
	case graph.StatusRunning:
		return "running"
	case graph.StatusError:
		return "failed"
	default:
		return string(s)
	}
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

// recordActivity appends an event to the ring buffer and, when an event bus
// is configured, publishes it to live API subscribers.
func (o *Orchestrator) recordActivity(event, detail string) {
	now := time.Now()
	o.activityMu.Lock()
	o.activityBuf[o.activityPos] = ActivityEvent{Time: now, Event: event, Detail: detail}
	o.activityPos = (o.activityPos + 1) % maxOrchestratorEvents
	o.activityMu.Unlock()
	if o.events != nil {
		o.events.Publish(eventbus.Event{
			Type:     eventbus.TypeActivity,
			Activity: &eventbus.ActivityInfo{Time: now, Event: event, Detail: detail},
		})
	}
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
		LoopStopped:    o.Stopped(),
		LastConverged:  o.LastConverged(),
	}
}

// NodePhases returns the user-facing phase (see phaseForStatus) of every
// lifecycle graph node, keyed by node ID: the container name for
// multi-container apps, the catalog ID for single-container apps. Read-only
// snapshot for the developer dashboard; nil when the graph is unavailable.
func (o *Orchestrator) NodePhases() map[string]string {
	if o.graph == nil {
		return nil
	}
	nodes, err := o.graph.Nodes()
	if err != nil {
		o.logger.Warn("failed to read graph nodes", "error", err)
		return nil
	}
	phases := make(map[string]string, len(nodes))
	for _, node := range nodes {
		phases[node.ID] = phaseForStatus(node.ActualStatus)
	}
	return phases
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
// arrive. It blocks until the context is cancelled or Stop is called. Must be
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

	for {
		intents, live := o.queue.WaitAndDrain(ctx)
		if !live {
			o.logger.Info("orchestrator stopped")
			return
		}
		if len(intents) == 0 {
			// A stale signal token can wake the wait with an empty queue
			// (see IntentQueue.WaitAndDrain). The loop survives; only a
			// cancelled context stops it.
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

// RemoveApp calls a configurator's optional Remover.Remove for the named app
// (when one is registered and implements teardown), removes containers, then
// deletes graph node(s).
// For multi-container apps, all container nodes are removed.
// The drive's terminal operation state is recorded here: the whole
// removal is one uninstall phase from the row's point of view.
func (o *Orchestrator) RemoveApp(ctx context.Context, appName string, clearData bool) error {
	// Guarantee a drive row: the Submit-created uninstall row is
	// continued when present; otherwise the removal is recorded as a
	// fresh drive so a failed removal never goes untracked.
	o.ensureOpDrive(appName)
	if err := o.removeApp(ctx, appName, clearData); err != nil {
		o.recordOpFail(appName, store.OpPhaseTopology, err, true)
		return err
	}
	o.recordOpComplete(appName)
	return nil
}

func (o *Orchestrator) removeApp(ctx context.Context, appName string, clearData bool) error {
	o.logger.Info("removing app", "app", appName, "clear_data", clearData)

	// Multi-container apps: remove each container node individually.
	if o.catalog != nil {
		if catalogApp, err := o.catalog.Get(appName); err == nil && catalogApp != nil {
			if len(catalogApp.Containers) > 0 {
				return o.removeMultiContainerApp(ctx, appName, catalogApp.ContainerDefs(), clearData)
			}
		}
	}

	// Single-container (or system) app. Only a configurator that owns teardown
	// gets a Remove call; container and data removal are the orchestrator's.
	if r, ok := o.registry.Get(appName).(configurator.Remover); ok {
		state, err := o.buildAppState(appName)
		if err != nil {
			return fmt.Errorf("build app state: %w", err)
		}
		if err := r.Remove(ctx, state, clearData); err != nil {
			return fmt.Errorf("remove app %q: %w", appName, err)
		}
	}
	return o.graph.DeleteNode(appName)
}

// removeMultiContainerApp removes all container nodes for a multi-container app,
// running per-node configurator Remove() and container runtime Remove() for each.
func (o *Orchestrator) removeMultiContainerApp(ctx context.Context, appName string, defs []catalog.ContainerDef, clearData bool) error {
	for _, def := range defs {
		// Release container-owned data while the container is still alive
		// (see releaseContainerOwnedData).
		if clearData {
			o.releaseContainerOwnedData(ctx, appName, def)
		}
		if r, ok := o.registry.Get(def.Name).(configurator.Remover); ok {
			state, err := o.buildAppState(def.Name)
			if err != nil {
				o.logger.Warn("failed to build state for container removal", "container", def.Name, "error", err)
			} else if err := r.Remove(ctx, state, clearData); err != nil {
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

// releaseContainerOwnedData empties a container's app-data volumes while the
// container is still running, so the host-side os.RemoveAll afterwards can
// delete the whole app data directory. Containers keep their data as their
// (possibly non-root) container user, which leaves mode-0700 directories on
// the host that the host-agent user cannot enter or delete (e.g. the
// pgvector image's postgres user). The container's own filesystem view can
// still reach them, so the cleanup runs inside the container. Volumes whose
// host source is outside the app data directory (e.g. shared media) are left
// untouched. Failures are logged, not fatal: the host-side RemoveAll is
// still attempted.
func (o *Orchestrator) releaseContainerOwnedData(ctx context.Context, appName string, def catalog.ContainerDef) {
	if o.config.Containers == nil {
		return
	}
	appDataDir := filepath.Join(o.dataDir, appName)
	var targets []string
	for _, v := range def.Volumes {
		src := strings.ReplaceAll(v.Source, "{{appDataDir}}", appDataDir)
		src = strings.ReplaceAll(src, "{{dataDir}}", o.dataDir)
		if src == appDataDir || !strings.HasPrefix(src, appDataDir+string(os.PathSeparator)) {
			continue
		}
		targets = append(targets, v.Destination)
	}
	if len(targets) == 0 {
		return
	}
	state, err := o.config.Containers.Inspect(ctx, def.Name)
	if err != nil || !state.Running {
		return // container absent or already stopped: host-side removal is best-effort
	}
	for _, dest := range targets {
		cmd := []string{"sh", "-c", fmt.Sprintf("find %s -mindepth 1 -delete 2>/dev/null || true", dest)}
		if err := o.config.Containers.Exec(ctx, def.Name, cmd); err != nil {
			o.logger.Warn("failed to release container-owned data", "container", def.Name, "volume", dest, "error", err)
		}
	}
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

		// ERROR is terminal: never retry without an explicit status reset.
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
	appID := o.ownerApp(id)
	state, err := o.buildAppState(id)
	if err != nil {
		o.logger.Warn("staleness re-run: failed to build state", "app", id, "error", err)
		return
	}
	o.logger.Info("staleness re-run: running PostStart", "app", id)
	if err := o.runPostStart(ctx, cfg, state); err != nil {
		o.logger.Warn("staleness re-run: PostStart failed", "app", id, "error", err)
		o.ensureOpDrive(appID)
		o.recordOpFail(appID, store.OpPhasePoststart, opCause(id, appID, err), true)
		return
	}
	o.healOp(appID)
	o.logger.Info("staleness re-run: PostStart complete", "app", id)
}

// runPostStart invokes a configurator's PostStart bounded by the framework's
// PostStartBudget (DefaultPostStartBudget when unset). The budget ctx is
// derived from the pass ctx, so a Stop()-cancellation propagates immediately
// while the budget independently caps a hung finalization. The framework, not
// the app, owns this ceiling; apps use the ctx they are given directly.
func (o *Orchestrator) runPostStart(ctx context.Context, cfg configurator.NodeLifecycle, state *configurator.AppState) error {
	budget := o.config.PostStartBudget
	if budget <= 0 {
		budget = DefaultPostStartBudget
	}
	bctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return cfg.PostStart(bctx, state)
}

// ensureContainerFromDef ensures a container exists and is running from a ContainerDef,
// creating required networks and mount directories first.
func (o *Orchestrator) ensureContainerFromDef(ctx context.Context, def *catalog.ContainerDef, appCatalogID string) error {
	if o.config.Containers == nil {
		return nil
	}

	o.ensureNetworksForContainer(ctx, def)

	spec, err := ContainerSpecFromDef(*def, appCatalogID, o.dataDir, o.config.TemplateVars)
	if err != nil {
		return fmt.Errorf("build container spec: %w", err)
	}

	o.applyIssuerExtraHost(&spec, appCatalogID)
	o.ensureMountDirs(def.Name, spec)

	if _, err := o.config.Containers.Ensure(ctx, spec); err != nil {
		return fmt.Errorf("ensure container: %w", err)
	}
	return nil
}

// ensureNetworksForContainer creates every user-defined network the
// container references ("host" mode needs no creation). Failures are
// logged, not fatal: Ensure() surfaces the real error later.
func (o *Orchestrator) ensureNetworksForContainer(ctx context.Context, def *catalog.ContainerDef) {
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
}

// applyIssuerExtraHost adds the OIDC issuer host-gateway mapping to native
// OIDC app containers so they can reach the issuer by the same hostname
// browsers use (token exchange happens inside the container). Apps on the
// loopback issuer (sso.loopbackIssuer) are skipped: they share the host
// network namespace, where localhost already resolves to Traefik and the
// shared issuer hostname is never used.
func (o *Orchestrator) applyIssuerExtraHost(spec *containerruntime.Spec, appCatalogID string) {
	if o.hosts == nil || o.catalog == nil {
		return
	}
	catalogApp, err := o.catalog.Get(appCatalogID)
	if err != nil || catalogApp == nil || catalogApp.SSO.Strategy != "native-oidc" {
		return
	}
	if catalogApp.SSO.LoopbackIssuer {
		return
	}
	ehost := o.hosts.Get().IssuerExtraHost()
	if !hasExtraHost(spec.ExtraHosts, ehost) {
		spec.ExtraHosts = append(spec.ExtraHosts, ehost)
	}
}

// ensureMountDirs creates the source directory for each directory mount.
// File mounts (.yml/.yaml/.json/.conf) are skipped: their parents are
// created by whoever generates the file.
func (o *Orchestrator) ensureMountDirs(containerName string, spec containerruntime.Spec) {
	for _, mount := range spec.Mounts {
		if isFileMountPath(mount.Source) {
			continue
		}
		if err := os.MkdirAll(mount.Source, 0755); err != nil {
			o.logger.Warn("failed to create mount directory", "container", containerName, "path", mount.Source, "error", err)
		}
	}
}

// isFileMountPath reports whether a mount source names a config file rather
// than a directory.
func isFileMountPath(source string) bool {
	for _, ext := range []string{".yml", ".yaml", ".json", ".conf"} {
		if strings.HasSuffix(source, ext) {
			return true
		}
	}
	return false
}

// hasExtraHost reports whether the spec already carries the given host:target
// extraHosts entry.
func hasExtraHost(entries []string, want string) bool {
	for _, e := range entries {
		if e == want {
			return true
		}
	}
	return false
}

// runFullLifecycle executes all lifecycle phases for a node that has not yet
// reached its target status.
func (o *Orchestrator) runFullLifecycle(ctx context.Context, id string, node *graph.Node) bool {
	// Target INITIALIZING means "unmanage": snap actual to match and stop.
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

	owner := o.ownerApp(id)
	o.ensureOpDrive(owner)

	state, err := o.buildAppState(id)
	if err != nil {
		o.logger.Error("failed to build app state", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		o.recordOpFail(owner, store.OpPhasePlanning, opCause(id, owner, err), true)
		return false
	}

	// Phase 1: PreStart
	var prestart configurator.PreStartResult
	if cfg != nil {
		o.logger.Info("lifecycle phase: PreStart", "app", id)
		o.recordOpPhase(owner, store.OpPhasePrestart)
		_ = o.graph.SetActualStatus(id, graph.StatusPreStartConfig, "")
		var err error
		prestart, err = cfg.PreStart(ctx, state)
		if err != nil {
			o.logger.Warn("PreStart failed", "app", id, "error", err)
			_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
			o.recordOpFail(owner, store.OpPhasePrestart, opCause(id, owner, err), true)
			return false
		}
		o.logger.Info("lifecycle phase: PreStart complete",
			"app", id,
			"restart_needed", prestart.RestartNeeded,
			"restart_reason", prestart.Reason)
	}

	// SSO provisioning: ensure the forward-auth provider exists in Authentik before the
	// container starts, so requests can be authenticated immediately on first boot.
	if err := o.ensureSSO(ctx, id); err != nil {
		o.logger.Warn("SSO provisioning failed", "app", id, "error", err)
		_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
		o.recordOpFail(owner, store.OpPhasePrestart, opCause(id, owner, err), true)
		return false
	}

	// Phase 2: EnsureContainer
	// If PreStart asked for a recreate, remove the existing container first so
	// Ensure() creates a fresh one that picks up the change.
	if def != nil {
		if prestart.RestartNeeded {
			o.logger.Info("PreStart requires recreate, removing container",
				"app", id, "reason", prestart.Reason)
			_ = o.config.Containers.Remove(ctx, def.Name)
		}
		o.logger.Info("lifecycle phase: EnsureContainer", "app", id)
		o.recordOpPhase(owner, store.OpPhaseTopology)
		_ = o.graph.SetActualStatus(id, graph.StatusStarting, "")
		if err := o.ensureContainerFromDef(ctx, def, appCatalogID); err != nil {
			o.logger.Warn("EnsureContainer failed", "app", id, "error", err)
			_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
			o.recordOpFail(owner, store.OpPhaseTopology, opCause(id, owner, err), true)
			return false
		}
		o.logger.Info("lifecycle phase: EnsureContainer complete", "app", id)

		// Phase 3: HealthCheck
		o.logger.Info("lifecycle phase: HealthCheck", "app", id)
		o.recordOpPhase(owner, store.OpPhaseHealth)
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
				o.recordOpFail(owner, store.OpPhaseHealth, opCause(id, owner, err), true)
				return false
			}
		}
		o.logger.Info("lifecycle phase: HealthCheck complete", "app", id)
	}

	// Phase 4: PostStart runs under the framework's PostStartBudget so the
	// finalization wait is bounded and Stop() can interrupt it (apps no longer
	// detach their own contexts). A failure whose cause is the cancelled pass
	// context is an interruption, not a fault: leave the node where it is so the
	// next start re-converges, rather than parking a shutdown in ERROR (R3).
	if cfg != nil {
		o.logger.Info("lifecycle phase: PostStart", "app", id)
		o.recordOpPhase(owner, store.OpPhasePoststart)
		_ = o.graph.SetActualStatus(id, graph.StatusPostStartConfig, "")
		if err := o.runPostStart(ctx, cfg, state); err != nil {
			if ctx.Err() != nil {
				o.logger.Info("PostStart interrupted by shutdown; leaving status for re-converge", "app", id, "error", err)
				o.recordOpFail(owner, store.OpPhasePoststart, opCause(id, owner, fmt.Errorf("interrupted by shutdown: %w", err)), true)
				return false
			}
			o.logger.Warn("PostStart failed", "app", id, "error", err)
			_ = o.graph.SetActualStatus(id, graph.StatusError, err.Error())
			o.recordOpFail(owner, store.OpPhasePoststart, opCause(id, owner, err), true)
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
	state.SSOEnabled = ssoEnabled
	if ssoEnabled {
		o.logger.Info("SSO enabled for app", "app", id, "strategy", catalogApp.SSO.Strategy)
		switch catalogApp.SSO.Strategy {
		case "ldap":
			if o.config.LDAPOutput != nil {
				state.LDAP = o.config.LDAPOutput
			}
		case "native-oidc":
			if inputs := o.oidcInputsForApp(catalogApp, o.resolveSSOURLs()); inputs != nil && len(inputs.RedirectURIs) > 0 {
				state.OIDC = &configurator.OIDCOutput{
					ClientID:     inputs.ClientID,
					ClientSecret: inputs.ClientSecret,
					IssuerURL:    inputs.IssuerURL,
					RedirectURI:  inputs.RedirectURIs[0],
				}
			}
		}
	}

	state.Integrations = o.buildIntegrations(catalogApp.CatalogID, catalogApp)

	return state, nil
}

// buildIntegrations resolves an app's integration contracts into typed
// bindings, so a consumer is handed its providers' identity, address and
// contract payload instead of discovering any of it itself (probing a port,
// reading a sibling's config file).
//
// A contract binds every provider the app's metadata declares for it: the
// choice recorded in the app's integration config, plus the compatible apps in
// the app's own metadata (which is what the graph ordered for the same set in
// computeAppDeps). Providers that are not installed are bound too, with
// Installed false: a consumer needs their address to prune the entry Bloud wrote
// for them.
func (o *Orchestrator) buildIntegrations(app string, catalogApp *catalog.App) configurator.Integrations {
	var out configurator.Integrations
	if o.appStore == nil || len(catalogApp.Integrations) == 0 {
		return out
	}
	installedApps, err := o.appStore.GetAll()
	if err != nil {
		o.logger.Warn("cannot resolve integration bindings; configurators run without them", "app", app, "error", err)
		return out
	}
	installed := make(map[string]bool, len(installedApps))
	for _, a := range installedApps {
		installed[a.CatalogID] = true
	}

	choices := map[string]string{}
	for _, a := range installedApps {
		if a.CatalogID == app {
			choices = a.IntegrationConfig
			break
		}
	}

	for contract, integration := range catalogApp.Integrations {
		for _, providerID := range resolveProviders(integration, choices[contract]) {
			// An app cannot be its own provider: a self-edge would also make
			// the graph order the node after itself.
			if providerID == app {
				continue
			}
			provider, err := o.catalog.Get(providerID)
			if err != nil || provider == nil {
				continue
			}
			o.bindContract(&out, contract, o.providerRef(providerID, provider, installed[providerID]), provider.Provides[contract], providerID, integration.Requires)
		}
	}
	return out
}

// bindContract appends one provider's binding for one contract. The payload it
// builds is the only contract-specific code in the resolver: a provider of an
// existing contract is pure metadata, and adding a contract means adding an arm
// here plus its payload type and its registry entry.
//
// Required secret names come from the registry rather than being repeated here,
// so the name a provider publishes and the name the payload reads cannot drift,
// and a secret is resolved only when the consumer declared it in
// `integrations.<contract>.requires`. That is what keeps the payload least
// privilege: an app that integrates with the identity provider for SSO is not
// handed the provider's API token unless it says it reads it.
func (o *Orchestrator) bindContract(
	out *configurator.Integrations,
	contract string,
	ref configurator.ProviderRef,
	offer catalog.ContractProvides,
	providerID string,
	requires []string,
) {
	switch contract {
	case "pvr":
		out.PVRs = append(out.PVRs, configurator.PVRBinding{ProviderRef: ref, APIKey: o.publishedSecret(providerID, contract, offer, requires)})
	case "mediaServer":
		out.MediaServers = append(out.MediaServers, configurator.MediaServerBinding{ProviderRef: ref, AdminPassword: o.publishedSecret(providerID, contract, offer, requires)})
	case "sso":
		out.SSO = append(out.SSO, configurator.SSOBinding{ProviderRef: ref, APIToken: o.publishedSecret(providerID, contract, offer, requires)})
	case "downloadClient":
		out.DownloadClients = append(out.DownloadClients, configurator.DownloadClientBinding{ProviderRef: ref})
	case "mcp":
		out.MCPServers = append(out.MCPServers, configurator.MCPBinding{
			ProviderRef: ref,
			ServerName:  offer.Values["serverName"],
			URL:         ref.BaseURL + offer.Values["path"],
			Token:       o.publishedSecret(providerID, contract, offer, requires),
		})
	default:
		// Contracts with no payload (proxy, database) need no consumer input
		// beyond the address, which the graph edge already encodes. A contract
		// that *does* carry a payload and lands here is a bug in this switch,
		// and silence would look exactly like "the provider published nothing",
		// so say so.
		if spec, known := catalog.ContractFor(contract); known && (len(spec.Secrets) > 0 || len(spec.Values) > 0) {
			o.logger.Warn("integration contract carries a payload but has no binding here; consumers of it receive nothing",
				"contract", contract, "provider", providerID)
		}
	}
}

// publishedSecret returns the secret a single-secret contract carries, or "" when
// the consumer did not require it or the provider has not published it yet. The
// two are the same empty field on purpose: a consumer has to tell "not ready"
// from an empty credential, and "I did not ask for it" is a metadata mistake it
// can see in its own `requires`.
func (o *Orchestrator) publishedSecret(providerID, contract string, offer catalog.ContractProvides, requires []string) string {
	spec, ok := catalog.ContractFor(contract)
	if !ok || len(spec.Secrets) != 1 {
		return ""
	}
	if o.secrets == nil || len(offer.Secrets) == 0 {
		return ""
	}
	if !slices.Contains(requires, spec.Secrets[0]) {
		return ""
	}
	return o.secrets.GetAppSecret(providerID, spec.Secrets[0])
}

// providerRef resolves where a provider is reachable: its node on the app
// network, its published port, and the two URLs a consumer needs (what its app
// stores, and what its configurator calls).
func (o *Orchestrator) providerRef(appID string, provider *catalog.App, installed bool) configurator.ProviderRef {
	node := o.primaryContainerNode(appID)
	ref := configurator.ProviderRef{
		App:       appID,
		Installed: installed,
		Node:      node,
		Port:      provider.Port,
	}
	if provider.Port > 0 {
		ref.BaseURL = fmt.Sprintf("http://%s:%d", node, provider.Port)
		ref.LocalURL = fmt.Sprintf("http://localhost:%d", provider.Port)
	}
	return ref
}

// resolveProviders returns the provider catalog IDs an integration binds, in
// declaration order, whichever of them are installed.
//
// The set mirrors the dependency edges computeAppDeps builds for the same
// contract: the recorded choice, plus, for an *optional* contract, every
// compatible app the metadata declares. A required contract binds only what was
// chosen, so a binding can never describe a provider the graph does not order.
func resolveProviders(integration catalog.Integration, choice string) []string {
	var out []string
	add := func(appID string) {
		for _, existing := range out {
			if existing == appID {
				return
			}
		}
		out = append(out, appID)
	}

	if choice != "" {
		add(choice)
	}
	if !integration.Required {
		for _, compatible := range integration.Compatible {
			add(compatible.App)
		}
	}
	return out
}

// ssoURLs is the resolved set of SSO URLs for one provisioning pass.
type ssoURLs struct {
	hostSet      hostset.HostSet
	baseURLs     []string // every base URL for redirect-URI registration (primary first, then other hosts, then IPs)
	hostSecret   string
	authentikURL string // browser-accessible Authentik URL for OIDC issuer/discovery
	issuerURL    string // OIDC issuer base URL reachable from app containers (empty = authentikURL)
}

// resolveSSOURLs computes the SSO URLs from the live host state when one is
// configured, falling back to the legacy single-URL config fields. IP-based
// base URLs (detected local addresses) are appended so login keeps working
// when the host is reached by IP.
func (o *Orchestrator) resolveSSOURLs() ssoURLs {
	if o.hosts != nil {
		hs := o.hosts.Get()
		return ssoURLs{
			hostSet:      hs,
			baseURLs:     hs.AllBaseURLs(),
			hostSecret:   o.ssoHostSecret,
			authentikURL: hs.PrimaryBaseURL(),
			issuerURL:    hs.IssuerBaseURL(),
		}
	}
	return ssoURLs{
		hostSet:      hostset.New([]string{hostFromURL(o.ssoBaseURL)}, hostFromURL(o.ssoBaseURL)),
		baseURLs:     netutil.BuildBaseURLs(o.ssoBaseURL),
		hostSecret:   o.ssoHostSecret,
		authentikURL: o.ssoAuthentikURL,
		issuerURL:    o.ssoIssuerURL,
	}
}

func hostFromURL(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "localhost"
}

// ensureSSO provisions the per-app SSO provider in the identity provider for
// apps that use forward-auth or native-oidc SSO. It is a no-op when SSO is not
// configured or the app's strategy is not provisioned in the identity provider
// (e.g. "ldap", which is provisioned by the LDAP outpost). Safe to call on
// every lifecycle pass (idempotent).
func (o *Orchestrator) ensureSSO(ctx context.Context, id string) error {
	if o.sso == nil || o.catalog == nil {
		return nil
	}
	u := o.resolveSSOURLs()
	if len(u.baseURLs) == 0 {
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
		externalURL := buildAppSubdomainURL(u.hostSet.PrimaryBaseURL(), appID)
		return o.sso.EnsureForwardAuth(ctx, appID, catalogApp.DisplayName, externalURL)

	case "native-oidc":
		if u.hostSecret == "" || u.authentikURL == "" {
			o.logger.Warn("native-oidc SSO skipped: missing SSO host secret or Authentik URL", "app", appID)
			return nil
		}
		inputs := o.oidcInputsForApp(catalogApp, u)
		if inputs == nil || len(inputs.RedirectURIs) == 0 {
			return fmt.Errorf("building OIDC inputs for %q", appID)
		}
		o.logger.Info("provisioning native-oidc SSO", "app", appID)
		return o.sso.EnsureNativeOIDC(ctx, appID, catalogApp.DisplayName, inputs.ClientID, inputs.ClientSecret, inputs.RedirectURIs, inputs.LaunchURL,
			authentik.OIDCTuning{ExtraScopes: inputs.ExtraScopes, AccessTokenMinutes: inputs.AccessTokenMinutes})
	}

	return nil
}

// oidcInputsForApp computes the deterministic OIDC inputs for a native-oidc
// app from the resolved SSO URLs and host secret. Returns nil when the SSO
// base URL or host secret is not configured.
func (o *Orchestrator) oidcInputsForApp(catalogApp *catalog.App, u ssoURLs) *sso.OIDCInputs {
	if len(u.baseURLs) == 0 || u.hostSecret == "" {
		return nil
	}
	issuerURL := u.issuerURL
	if catalogApp.SSO.LoopbackIssuer {
		// The app's OIDC client refuses a non-loopback http issuer, so route
		// its issuer through the host loopback rather than the shared issuer
		// host. The app container shares the host network namespace, which
		// makes localhost:<Traefik port> reach Traefik.
		issuerURL = u.hostSet.LoopbackIssuerBaseURL()
	}
	gen := sso.NewBlueprintGenerator(
		u.hostSecret,
		"",
		u.baseURLs,
		u.authentikURL,
		issuerURL,
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
