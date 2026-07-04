package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Reconciler owns the IntentQueue and runs a background loop that drains
// intents and converges the system state. When config is nil the converge
// phase is a stub that only logs (Phase 2 behavior).
type Reconciler struct {
	queue   *IntentQueue
	logger  *slog.Logger
	config  *Config
	cancel  context.CancelFunc
	started chan struct{}
	done    chan struct{}
	once    sync.Once
	reconcilerStatus
}

// New creates a Reconciler with the default debounce duration.
// cfg can be nil for stub mode (convergence is a no-op log).
func New(logger *slog.Logger, cfg *Config) *Reconciler {
	return &Reconciler{
		queue:   NewIntentQueue(DefaultDebounce),
		logger:  logger,
		config:  cfg,
		started: make(chan struct{}),
		done:    make(chan struct{}),
		reconcilerStatus: reconcilerStatus{
			activity: NewActivityLog(),
		},
	}
}

// Enqueue adds an intent to the reconciler's queue.
func (r *Reconciler) Enqueue(intent Intent) {
	typeName := intentTypeName(intent)
	r.logger.Info("intent enqueued",
		"type", typeName,
		"id", intent.IntentID(),
	)
	r.activity.Record("intent_enqueued", fmt.Sprintf("%s:%s", typeName, intentTarget(intent)))
	r.queue.Enqueue(intent)
}

// Start runs the reconciler loop. It blocks until the context is cancelled
// or Stop is called. Must be called exactly once (typically via goroutine).
func (r *Reconciler) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	close(r.started)
	defer close(r.done)

	r.logger.Info("reconciler started")

	for {
		intents := r.queue.WaitAndDrain(ctx)
		if intents == nil {
			r.logger.Info("reconciler stopped")
			return
		}

		r.logger.Info("drained intents", "count", len(intents))
		r.converging.Store(true)
		r.converge(ctx, intents)
		r.converging.Store(false)

		if ctx.Err() != nil {
			r.logger.Info("reconciler stopped")
			return
		}
	}
}

// Stop cancels the reconciler loop and waits for it to finish.
// Safe to call multiple times.
func (r *Reconciler) Stop() {
	r.once.Do(func() {
		<-r.started
		r.cancel()
		<-r.done
	})
}

// converge processes a batch of intents. When config is nil, this is a stub
// that only logs. When config is set, it drains intents into stores and then
// converges the system state.
func (r *Reconciler) converge(ctx context.Context, intents []Intent) {
	if r.config == nil {
		r.logger.Info("convergence pass complete (stub)", "intentCount", len(intents))
		return
	}

	pendingClearData := make(map[string]bool)
	r.applyIntents(intents, pendingClearData)
	r.convergeFromStores(ctx, pendingClearData)
}

// Status returns a snapshot of the reconciler's current state.
func (r *Reconciler) Status() Status {
	return Status{
		QueueDepth:     r.queue.PendingCount(),
		IsConverging:   r.converging.Load(),
		RecentActivity: r.activity.Recent(),
		AppPhases:      r.getAppPhases(),
	}
}

// intentTypeName returns a human-readable name for an intent type.
func intentTypeName(intent Intent) string {
	switch intent.(type) {
	case InstallAppIntent:
		return "InstallApp"
	case UninstallAppIntent:
		return "UninstallApp"
	case RenameAppIntent:
		return "RenameApp"
	case SetTailnetIntent:
		return "SetTailnet"
	case DeleteTailnetIntent:
		return "DeleteTailnet"
	case AddRemoteAppIntent:
		return "AddRemoteApp"
	case DeleteRemoteAppIntent:
		return "DeleteRemoteApp"
	case CreateShareIntent:
		return "CreateShare"
	case RevokeShareIntent:
		return "RevokeShare"
	case ClearAppDataIntent:
		return "ClearAppData"
	case ConvergeIntent:
		return "Converge"
	default:
		return "Unknown"
	}
}

// intentTarget returns a human-readable target identifier for an intent.
func intentTarget(intent Intent) string {
	switch i := intent.(type) {
	case InstallAppIntent:
		return i.AppName
	case UninstallAppIntent:
		return i.AppName
	case RenameAppIntent:
		return i.AppName
	case SetTailnetIntent:
		return i.Name
	case DeleteTailnetIntent:
		return ""
	case AddRemoteAppIntent:
		return i.AppID
	case DeleteRemoteAppIntent:
		return i.RemoteAppID
	case CreateShareIntent:
		return i.AppName
	case RevokeShareIntent:
		return i.ShareID
	case ClearAppDataIntent:
		return i.AppName
	case ConvergeIntent:
		return ""
	default:
		return ""
	}
}
