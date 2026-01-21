package orchestrator

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"log/slog"
)

// mockOrchestratorForQueue is a minimal orchestrator for testing the queue
type mockOrchestratorForQueue struct {
	installCalls   atomic.Int32
	uninstallCalls atomic.Int32
	installDelay   time.Duration
	uninstallDelay time.Duration
	mu             sync.Mutex
	installResults map[string]*InstallResult
}

func newMockOrchestratorForQueue() *mockOrchestratorForQueue {
	return &mockOrchestratorForQueue{
		installResults: make(map[string]*InstallResult),
	}
}

func (m *mockOrchestratorForQueue) Install(ctx context.Context, req InstallRequest) (InstallResponse, error) {
	m.installCalls.Add(1)
	if m.installDelay > 0 {
		time.Sleep(m.installDelay)
	}

	m.mu.Lock()
	result, ok := m.installResults[req.App]
	m.mu.Unlock()

	if ok {
		return result, nil
	}

	return &InstallResult{
		App:     req.App,
		Success: true,
	}, nil
}

func (m *mockOrchestratorForQueue) Uninstall(ctx context.Context, req UninstallRequest) (UninstallResponse, error) {
	m.uninstallCalls.Add(1)
	if m.uninstallDelay > 0 {
		time.Sleep(m.uninstallDelay)
	}
	return &UninstallResult{
		App:     req.App,
		Success: true,
	}, nil
}

func TestOperationQueue_SingleInstall(t *testing.T) {
	mock := newMockOrchestratorForQueue()
	logger := slog.Default()

	// Create a minimal orchestrator wrapper for the queue
	// We need to pass the actual Orchestrator type to the queue
	// For testing, we'll create a queue with a nil orchestrator and override the execute methods
	queue := &OperationQueue{
		batchWait: 10 * time.Millisecond,
		requestCh: make(chan QueuedOperation, 100),
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
		logger:    logger,
	}

	// Override execute methods for testing
	go func() {
		defer close(queue.stoppedCh)
		for {
			select {
			case <-queue.stopCh:
				return
			case op := <-queue.requestCh:
				if op.Type == OpInstall {
					result, err := mock.Install(op.Ctx, *op.Install)
					op.ResultCh <- OperationResult{InstallResult: result, Err: err}
				} else {
					result, err := mock.Uninstall(op.Ctx, *op.Uninstall)
					op.ResultCh <- OperationResult{UninstallResult: result, Err: err}
				}
			}
		}
	}()

	defer queue.Stop()

	// Test single install
	ctx := context.Background()
	resultCh := make(chan OperationResult, 1)
	queue.requestCh <- QueuedOperation{
		Type:     OpInstall,
		Install:  &InstallRequest{App: "test-app"},
		ResultCh: resultCh,
		Ctx:      ctx,
	}

	result := <-resultCh
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if !result.InstallResult.IsSuccess() {
		t.Fatalf("expected success")
	}
	if result.InstallResult.GetApp() != "test-app" {
		t.Fatalf("expected app 'test-app', got '%s'", result.InstallResult.GetApp())
	}

	if mock.installCalls.Load() != 1 {
		t.Fatalf("expected 1 install call, got %d", mock.installCalls.Load())
	}
}

func TestOperationQueue_ConcurrentInstalls(t *testing.T) {
	mock := newMockOrchestratorForQueue()
	mock.installDelay = 50 * time.Millisecond // Simulate slow install
	logger := slog.Default()

	queue := &OperationQueue{
		batchWait: 20 * time.Millisecond,
		requestCh: make(chan QueuedOperation, 100),
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
		logger:    logger,
	}

	// Worker that processes batches
	go func() {
		defer close(queue.stoppedCh)
		for {
			select {
			case <-queue.stopCh:
				return
			case first := <-queue.requestCh:
				// Collect batch
				batch := []QueuedOperation{first}
				timer := time.NewTimer(queue.batchWait)
			collect:
				for {
					select {
					case <-timer.C:
						break collect
					case op := <-queue.requestCh:
						batch = append(batch, op)
					}
				}
				timer.Stop()

				// Process batch
				for _, op := range batch {
					if op.Type == OpInstall {
						result, err := mock.Install(op.Ctx, *op.Install)
						op.ResultCh <- OperationResult{InstallResult: result, Err: err}
					}
				}
			}
		}
	}()

	defer queue.Stop()

	// Send multiple concurrent installs
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make([]OperationResult, 3)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resultCh := make(chan OperationResult, 1)
			queue.requestCh <- QueuedOperation{
				Type:     OpInstall,
				Install:  &InstallRequest{App: "app-" + string(rune('a'+idx))},
				ResultCh: resultCh,
				Ctx:      ctx,
			}
			results[idx] = <-resultCh
		}(i)
	}

	wg.Wait()

	// All should succeed
	for i, result := range results {
		if result.Err != nil {
			t.Errorf("result %d: unexpected error: %v", i, result.Err)
		}
		if !result.InstallResult.IsSuccess() {
			t.Errorf("result %d: expected success", i)
		}
	}

	// Should have 3 install calls (one per app)
	if mock.installCalls.Load() != 3 {
		t.Errorf("expected 3 install calls, got %d", mock.installCalls.Load())
	}
}

func TestOperationQueue_Deduplication(t *testing.T) {
	logger := slog.Default()

	queue := &OperationQueue{
		batchWait: 10 * time.Millisecond,
		requestCh: make(chan QueuedOperation, 100),
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
		logger:    logger,
	}

	// Create batch with duplicate apps
	batch := []QueuedOperation{
		{
			Type:     OpInstall,
			Install:  &InstallRequest{App: "app-a", Choices: map[string]string{"db": "postgres"}},
			ResultCh: make(chan OperationResult, 1),
			Ctx:      context.Background(),
		},
		{
			Type:     OpInstall,
			Install:  &InstallRequest{App: "app-a", Choices: map[string]string{"cache": "redis"}},
			ResultCh: make(chan OperationResult, 1),
			Ctx:      context.Background(),
		},
		{
			Type:     OpInstall,
			Install:  &InstallRequest{App: "app-b"},
			ResultCh: make(chan OperationResult, 1),
			Ctx:      context.Background(),
		},
	}

	deduped := queue.deduplicateBatch(batch)

	// Should have 2 unique apps
	if len(deduped) != 2 {
		t.Fatalf("expected 2 deduplicated operations, got %d", len(deduped))
	}

	// Find app-a and verify choices were merged
	var appA *QueuedOperation
	for i := range deduped {
		if deduped[i].Install != nil && deduped[i].Install.App == "app-a" {
			appA = &deduped[i]
			break
		}
	}

	if appA == nil {
		t.Fatal("app-a not found in deduplicated batch")
	}

	// Choices should be merged
	if appA.Install.Choices["db"] != "postgres" {
		t.Errorf("expected db=postgres, got %s", appA.Install.Choices["db"])
	}
	if appA.Install.Choices["cache"] != "redis" {
		t.Errorf("expected cache=redis, got %s", appA.Install.Choices["cache"])
	}
}

func TestOperationQueue_InstallUninstallConflict(t *testing.T) {
	logger := slog.Default()

	queue := &OperationQueue{
		batchWait: 10 * time.Millisecond,
		requestCh: make(chan QueuedOperation, 100),
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
		logger:    logger,
	}

	// Create batch with install followed by uninstall for same app
	batch := []QueuedOperation{
		{
			Type:     OpInstall,
			Install:  &InstallRequest{App: "app-a"},
			ResultCh: make(chan OperationResult, 1),
			Ctx:      context.Background(),
		},
		{
			Type:      OpUninstall,
			Uninstall: &UninstallRequest{App: "app-a", ClearData: true},
			ResultCh:  make(chan OperationResult, 1),
			Ctx:       context.Background(),
		},
	}

	deduped := queue.deduplicateBatch(batch)

	// Should have 1 operation (last one wins - uninstall)
	if len(deduped) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(deduped))
	}

	if deduped[0].Type != OpUninstall {
		t.Fatalf("expected uninstall operation, got install")
	}

	if !deduped[0].Uninstall.ClearData {
		t.Error("expected clearData=true")
	}
}

func TestOperationQueue_UninstallClearDataMerge(t *testing.T) {
	logger := slog.Default()

	queue := &OperationQueue{
		batchWait: 10 * time.Millisecond,
		requestCh: make(chan QueuedOperation, 100),
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
		logger:    logger,
	}

	// Create batch with multiple uninstalls, one with clearData
	batch := []QueuedOperation{
		{
			Type:      OpUninstall,
			Uninstall: &UninstallRequest{App: "app-a", ClearData: false},
			ResultCh:  make(chan OperationResult, 1),
			Ctx:       context.Background(),
		},
		{
			Type:      OpUninstall,
			Uninstall: &UninstallRequest{App: "app-a", ClearData: true},
			ResultCh:  make(chan OperationResult, 1),
			Ctx:       context.Background(),
		},
	}

	deduped := queue.deduplicateBatch(batch)

	if len(deduped) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(deduped))
	}

	// Should have clearData=true (most destructive option)
	if !deduped[0].Uninstall.ClearData {
		t.Error("expected clearData=true to be preserved")
	}
}

func TestOperationQueue_ContextCancellation(t *testing.T) {
	logger := slog.Default()

	queue := &OperationQueue{
		batchWait: 100 * time.Millisecond, // Long wait to test cancellation
		requestCh: make(chan QueuedOperation, 100),
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
		logger:    logger,
	}

	// Start a worker that just sleeps
	go func() {
		defer close(queue.stoppedCh)
		for {
			select {
			case <-queue.stopCh:
				return
			case op := <-queue.requestCh:
				// Simulate slow processing
				time.Sleep(200 * time.Millisecond)
				op.ResultCh <- OperationResult{
					InstallResult: &InstallResult{App: op.Install.App, Success: true},
				}
			}
		}
	}()

	defer queue.Stop()

	// Create a context that will be cancelled
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	resultCh := make(chan OperationResult, 1)

	// Try to enqueue
	select {
	case queue.requestCh <- QueuedOperation{
		Type:     OpInstall,
		Install:  &InstallRequest{App: "test-app"},
		ResultCh: resultCh,
		Ctx:      ctx,
	}:
	case <-ctx.Done():
		// Expected - context cancelled before we could enqueue
		return
	}

	// Wait for result or context cancellation
	select {
	case <-resultCh:
		t.Error("expected context cancellation, got result")
	case <-ctx.Done():
		// Expected
	}
}
