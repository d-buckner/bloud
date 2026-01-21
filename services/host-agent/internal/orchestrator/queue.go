package orchestrator

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// OperationQueue serializes install/uninstall operations to prevent race conditions.
// It batches concurrent requests into a single nixos-rebuild operation.
type OperationQueue struct {
	mu           sync.Mutex
	pending      []QueuedOperation
	batchWait    time.Duration
	requestCh    chan QueuedOperation
	stopCh       chan struct{}
	stoppedCh    chan struct{}
	orchestrator *Orchestrator
	logger       *slog.Logger
}

// QueuedOperation represents a single install or uninstall request waiting in the queue.
type QueuedOperation struct {
	Type     OperationType
	Install  *InstallRequest
	Uninstall *UninstallRequest
	ResultCh chan OperationResult
	Ctx      context.Context
}

// OperationType distinguishes between install and uninstall operations.
type OperationType int

const (
	OpInstall OperationType = iota
	OpUninstall
)

// OperationResult contains the result of a queued operation.
type OperationResult struct {
	InstallResult   InstallResponse
	UninstallResult UninstallResponse
	Err             error
}

// QueueConfig configures the operation queue.
type QueueConfig struct {
	BatchWait time.Duration // How long to collect requests before processing (default: 5s)
}

// DefaultQueueConfig returns sensible defaults for the queue.
func DefaultQueueConfig() QueueConfig {
	return QueueConfig{
		BatchWait: 5 * time.Second,
	}
}

// NewOperationQueue creates a new operation queue.
func NewOperationQueue(orchestrator *Orchestrator, cfg QueueConfig, logger *slog.Logger) *OperationQueue {
	if cfg.BatchWait == 0 {
		cfg.BatchWait = DefaultQueueConfig().BatchWait
	}

	return &OperationQueue{
		batchWait:    cfg.BatchWait,
		requestCh:    make(chan QueuedOperation, 100), // Buffer to avoid blocking callers
		stopCh:       make(chan struct{}),
		stoppedCh:    make(chan struct{}),
		orchestrator: orchestrator,
		logger:       logger,
	}
}

// Start begins the queue worker goroutine.
func (q *OperationQueue) Start() {
	go q.worker()
}

// Stop signals the worker to stop and waits for it to finish.
func (q *OperationQueue) Stop() {
	close(q.stopCh)
	<-q.stoppedCh
}

// EnqueueInstall adds an install request to the queue and waits for the result.
func (q *OperationQueue) EnqueueInstall(ctx context.Context, req InstallRequest) (InstallResponse, error) {
	resultCh := make(chan OperationResult, 1)

	op := QueuedOperation{
		Type:     OpInstall,
		Install:  &req,
		ResultCh: resultCh,
		Ctx:      ctx,
	}

	q.logger.Info("enqueueing install request", "app", req.App)

	select {
	case q.requestCh <- op:
		q.logger.Debug("install request queued", "app", req.App)
	case <-ctx.Done():
		q.logger.Warn("install request cancelled before queuing", "app", req.App, "error", ctx.Err())
		return nil, ctx.Err()
	case <-q.stopCh:
		q.logger.Warn("install request rejected, queue stopping", "app", req.App)
		return nil, context.Canceled
	}

	q.logger.Info("waiting for install result", "app", req.App)

	select {
	case result := <-resultCh:
		if result.Err != nil {
			q.logger.Error("install completed with error", "app", req.App, "error", result.Err)
		} else {
			q.logger.Info("install completed", "app", req.App, "success", result.InstallResult.IsSuccess())
		}
		return result.InstallResult, result.Err
	case <-ctx.Done():
		q.logger.Warn("install request cancelled while waiting", "app", req.App, "error", ctx.Err())
		return nil, ctx.Err()
	}
}

// EnqueueUninstall adds an uninstall request to the queue and waits for the result.
func (q *OperationQueue) EnqueueUninstall(ctx context.Context, req UninstallRequest) (UninstallResponse, error) {
	resultCh := make(chan OperationResult, 1)

	op := QueuedOperation{
		Type:      OpUninstall,
		Uninstall: &req,
		ResultCh:  resultCh,
		Ctx:       ctx,
	}

	q.logger.Info("enqueueing uninstall request", "app", req.App, "clearData", req.ClearData)

	select {
	case q.requestCh <- op:
		q.logger.Debug("uninstall request queued", "app", req.App)
	case <-ctx.Done():
		q.logger.Warn("uninstall request cancelled before queuing", "app", req.App, "error", ctx.Err())
		return nil, ctx.Err()
	case <-q.stopCh:
		q.logger.Warn("uninstall request rejected, queue stopping", "app", req.App)
		return nil, context.Canceled
	}

	q.logger.Info("waiting for uninstall result", "app", req.App)

	select {
	case result := <-resultCh:
		if result.Err != nil {
			q.logger.Error("uninstall completed with error", "app", req.App, "error", result.Err)
		} else {
			q.logger.Info("uninstall completed", "app", req.App, "success", result.UninstallResult.IsSuccess())
		}
		return result.UninstallResult, result.Err
	case <-ctx.Done():
		q.logger.Warn("uninstall request cancelled while waiting", "app", req.App, "error", ctx.Err())
		return nil, ctx.Err()
	}
}

// worker is the main loop that processes batched operations.
func (q *OperationQueue) worker() {
	defer close(q.stoppedCh)

	for {
		select {
		case <-q.stopCh:
			// Drain any remaining requests and cancel them
			q.drainPending()
			return
		case op := <-q.requestCh:
			// Got first request of a batch - collect more
			batch := q.collectBatch(op)
			if len(batch) > 0 {
				q.executeBatch(batch)
			}
		}
	}
}

// collectBatch waits for the batch window and collects all pending operations.
func (q *OperationQueue) collectBatch(first QueuedOperation) []QueuedOperation {
	batch := []QueuedOperation{first}

	q.logger.Info("starting batch collection", "batchWait", q.batchWait)

	// Wait for batch window to collect more requests
	timer := time.NewTimer(q.batchWait)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			q.logger.Info("batch collection complete", "collected", len(batch))
			return q.deduplicateBatch(batch)
		case op := <-q.requestCh:
			var appName string
			if op.Type == OpInstall {
				appName = op.Install.App
			} else {
				appName = op.Uninstall.App
			}
			q.logger.Debug("adding to batch", "app", appName, "type", op.Type, "batchSize", len(batch)+1)
			batch = append(batch, op)
		case <-q.stopCh:
			q.logger.Warn("batch collection interrupted by shutdown", "collected", len(batch))
			// Shutting down - cancel all collected operations
			for _, op := range batch {
				op.ResultCh <- OperationResult{Err: context.Canceled}
			}
			return nil
		}
	}
}

// deduplicateBatch removes duplicate operations for the same app.
// If install and uninstall for same app, last one wins.
// If multiple installs for same app, merge choices and notify all.
func (q *OperationQueue) deduplicateBatch(batch []QueuedOperation) []QueuedOperation {
	// Track operations by app name
	// For installs: merge choices, track all result channels
	// For uninstalls: keep the one with most flags (clearData)
	type appState struct {
		lastOp    OperationType
		install   *InstallRequest
		uninstall *UninstallRequest
		resultChs []chan OperationResult
		ctx       context.Context
	}

	apps := make(map[string]*appState)

	for _, op := range batch {
		var appName string
		if op.Type == OpInstall {
			appName = op.Install.App
		} else {
			appName = op.Uninstall.App
		}

		state, exists := apps[appName]
		if !exists {
			state = &appState{ctx: op.Ctx}
			apps[appName] = state
		}

		state.resultChs = append(state.resultChs, op.ResultCh)
		state.lastOp = op.Type

		if op.Type == OpInstall {
			if state.install == nil {
				state.install = op.Install
			} else {
				// Merge choices from both requests
				if op.Install.Choices != nil {
					if state.install.Choices == nil {
						state.install.Choices = make(map[string]string)
					}
					for k, v := range op.Install.Choices {
						state.install.Choices[k] = v
					}
				}
			}
			state.uninstall = nil // Install overrides previous uninstall
		} else {
			// Uninstall - keep the one with clearData=true
			if state.uninstall == nil {
				state.uninstall = op.Uninstall
			} else if op.Uninstall.ClearData {
				state.uninstall.ClearData = true
			}
			state.install = nil // Uninstall overrides previous install
		}
	}

	// Build deduplicated batch
	var result []QueuedOperation
	for _, state := range apps {
		op := QueuedOperation{
			Type:     state.lastOp,
			ResultCh: nil, // We'll handle notification separately
			Ctx:      state.ctx,
		}

		if state.lastOp == OpInstall {
			op.Install = state.install
		} else {
			op.Uninstall = state.uninstall
		}

		// Create a wrapper to notify all original result channels
		wrapperCh := make(chan OperationResult, 1)
		go func(resultChs []chan OperationResult, wrapperCh chan OperationResult) {
			result := <-wrapperCh
			for _, ch := range resultChs {
				ch <- result
			}
		}(state.resultChs, wrapperCh)

		op.ResultCh = wrapperCh
		result = append(result, op)
	}

	if len(batch) != len(result) {
		q.logger.Info("deduplicated batch operations",
			"original", len(batch),
			"deduplicated", len(result))
	}

	return result
}

// executeBatch processes all operations in the batch.
// For now, operations are executed sequentially in the batch.
// Future: could combine all installs into a single transaction.
func (q *OperationQueue) executeBatch(batch []QueuedOperation) {
	// Separate installs and uninstalls
	var installs, uninstalls []QueuedOperation
	var installApps, uninstallApps []string
	for _, op := range batch {
		if op.Type == OpInstall {
			installs = append(installs, op)
			installApps = append(installApps, op.Install.App)
		} else {
			uninstalls = append(uninstalls, op)
			uninstallApps = append(uninstallApps, op.Uninstall.App)
		}
	}

	q.logger.Info("executing batch",
		"totalOperations", len(batch),
		"installs", len(installs),
		"uninstalls", len(uninstalls),
		"installApps", installApps,
		"uninstallApps", uninstallApps)

	// Process uninstalls first (in case an app is being reinstalled)
	for i, op := range uninstalls {
		q.logger.Info("executing uninstall", "app", op.Uninstall.App, "index", i+1, "total", len(uninstalls))
		q.executeUninstall(op)
	}

	// Process installs
	// TODO: Combine all installs into a single transaction for efficiency
	for i, op := range installs {
		q.logger.Info("executing install", "app", op.Install.App, "index", i+1, "total", len(installs))
		q.executeInstall(op)
	}

	q.logger.Info("batch execution complete", "totalOperations", len(batch))
}

// executeInstall runs a single install operation.
func (q *OperationQueue) executeInstall(op QueuedOperation) {
	result, err := q.orchestrator.Install(op.Ctx, *op.Install)
	op.ResultCh <- OperationResult{
		InstallResult: result,
		Err:           err,
	}
}

// executeUninstall runs a single uninstall operation.
func (q *OperationQueue) executeUninstall(op QueuedOperation) {
	result, err := q.orchestrator.Uninstall(op.Ctx, *op.Uninstall)
	op.ResultCh <- OperationResult{
		UninstallResult: result,
		Err:             err,
	}
}

// drainPending cancels all pending operations during shutdown.
func (q *OperationQueue) drainPending() {
	for {
		select {
		case op := <-q.requestCh:
			op.ResultCh <- OperationResult{Err: context.Canceled}
		default:
			return
		}
	}
}
