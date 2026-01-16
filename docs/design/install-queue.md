# Design: Install Queue with Batching

## Overview

Handle concurrent app install requests safely by queuing requests and batching them into single nixos-rebuild operations. This prevents race conditions where multiple simultaneous installs could corrupt the generated Nix config or cause nixos-rebuild conflicts.

## Goals

- **Safe concurrent installs** - Multiple rapid install clicks don't corrupt state
- **Efficient batching** - Concurrent requests become a single rebuild
- **Idempotent** - Each rebuild produces the complete desired state
- **Simple** - Queue with batching is simpler than cancel-and-restart

## Non-Goals

- Real-time progress for individual apps in a batch (batch succeeds/fails as unit)
- Priority ordering of install requests
- Cancel/interrupt of in-flight installs

---

## Current State (Problem)

The orchestrator has **no concurrency protection**:

```go
// orchestrator_nix.go - Install() has no mutex
func (o *Orchestrator) Install(ctx context.Context, req InstallRequest) (InstallResponse, error) {
    // ... generates apps.nix
    // ... triggers nixos-rebuild
}
```

**What happens with concurrent installs:**

1. User clicks "Install Radarr"
2. User immediately clicks "Install Sonarr"
3. Both HTTP requests hit `orchestrator.Install()` concurrently
4. Both generate `apps.nix` - **race condition**, one overwrites the other
5. Both trigger `nixos-rebuild switch` - **will conflict/fail**

**Result**: Undefined behavior, potential data corruption, failed installs.

---

## Proposed Design

### Architecture

```
┌─────────────┐     ┌──────────────┐     ┌─────────────────┐
│ HTTP Handler│────▶│ Install Queue│────▶│ Install Worker  │
│ (concurrent)│     │ (thread-safe)│     │ (single-threaded)│
└─────────────┘     └──────────────┘     └─────────────────┘
                           │
                           ▼
                    ┌──────────────┐
                    │  Batch Timer │
                    │  (100ms)     │
                    └──────────────┘
```

### Queue Structure

```go
type InstallQueue struct {
    mu        sync.Mutex
    pending   []InstallRequest  // requests waiting for next batch
    running   bool              // rebuild in progress
    batchWait time.Duration     // how long to collect requests (e.g., 100ms)

    // Channels for coordination
    requestCh chan InstallRequest
    resultCh  map[string]chan InstallResponse  // keyed by request ID
}
```

### Flow

1. **Request arrives** → Add to `pending`, start batch timer if not running
2. **Batch timer fires** → Collect all `pending`, clear queue, start rebuild
3. **Rebuild runs** → Build transaction with ALL pending apps, single nixos-rebuild
4. **Rebuild completes** → Notify all waiting requests, check for new pending
5. **If more pending** → Start new batch timer, repeat

### Batching Logic

```go
func (q *InstallQueue) worker() {
    for {
        // Wait for batch to be ready
        batch := q.collectBatch()
        if len(batch) == 0 {
            continue
        }

        // Build unified transaction with all apps
        tx := q.buildBatchTransaction(batch)

        // Single nixos-rebuild for entire batch
        result := q.orchestrator.executeBatch(tx)

        // Notify all waiting callers
        q.notifyResults(batch, result)
    }
}

func (q *InstallQueue) collectBatch() []InstallRequest {
    q.mu.Lock()

    // Wait for batch window to collect concurrent requests
    if len(q.pending) > 0 {
        q.mu.Unlock()
        time.Sleep(q.batchWait)  // e.g., 100ms
        q.mu.Lock()
    }

    batch := q.pending
    q.pending = nil
    q.mu.Unlock()

    return batch
}
```

### HTTP Handler Changes

```go
func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
    req := InstallRequest{...}

    // Queue request and wait for result
    result := s.installQueue.Enqueue(r.Context(), req)

    // Return result to caller
    json.NewEncoder(w).Encode(result)
}
```

### Transaction Building

The key insight: `buildInstallTransaction` already loads current state and adds new apps. For batching, we just add ALL requested apps:

```go
func (q *InstallQueue) buildBatchTransaction(batch []InstallRequest) *nixgen.Transaction {
    // Load current state (already installed apps)
    current := q.generator.LoadCurrent()

    tx := &nixgen.Transaction{
        Apps: make(map[string]nixgen.AppConfig),
    }

    // Copy existing apps
    for name, app := range current.Apps {
        tx.Apps[name] = app
    }

    // Add ALL apps from batch
    for _, req := range batch {
        plan := q.graph.PlanInstall(req.App)

        // Add main app
        tx.Apps[req.App] = nixgen.AppConfig{
            Name:         req.App,
            Enabled:      true,
            Integrations: req.Choices,
        }

        // Add dependencies
        for _, dep := range plan.Dependencies {
            if _, exists := tx.Apps[dep]; !exists {
                tx.Apps[dep] = nixgen.AppConfig{Name: dep, Enabled: true}
            }
        }
    }

    return tx
}
```

---

## Edge Cases

### Request arrives during rebuild

```
T0: Batch starts with [radarr]
T1: nixos-rebuild running...
T2: User clicks "Install sonarr"
T3: sonarr added to NEW pending queue
T4: Rebuild completes
T5: Worker checks pending, finds [sonarr]
T6: New batch starts with [sonarr]
```

Result: Two sequential rebuilds. Sonarr waits for radarr to finish, then gets its own rebuild.

### Same app requested twice

Deduplicate in queue - only keep first request, notify both callers with same result.

### Uninstall during install

Uninstall should use the same queue. The transaction builder handles both:
- Install: `app.Enabled = true`
- Uninstall: `app.Enabled = false`

A batch with "install radarr" + "uninstall radarr" would result in radarr being disabled (last write wins, or we could detect conflict).

---

## Alternatives Considered

### 1. Simple Mutex (Rejected)

```go
func (o *Orchestrator) Install(...) {
    o.mu.Lock()
    defer o.mu.Unlock()
    // ...
}
```

**Pros**: Trivial to implement
**Cons**: Second request blocks until first completes. No batching - if 5 apps installed quickly, 5 sequential rebuilds.

### 2. Cancel and Restart (Rejected)

Cancel in-flight rebuild when new request arrives, restart with complete state.

**Pros**: Always working toward latest desired state
**Cons**:
- Risk of never completing if requests keep coming
- Complexity around partial rebuild states
- nixos-rebuild interruption may leave system in inconsistent state

### 3. Dependency-Aware Merging (Deferred)

Only cancel if new app needs deps we haven't resolved yet.

**Pros**: Could be more efficient in some cases
**Cons**: Complex to implement correctly, queue with batching handles most cases well

---

## Implementation Plan

1. Add `InstallQueue` struct to orchestrator package
2. Modify `New()` to create and start queue worker
3. Change `Install()` to enqueue rather than execute directly
4. Add batch timer and collection logic
5. Update `Uninstall()` to use same queue
6. Add tests for concurrent install scenarios

---

## Open Questions

1. **Batch timeout**: 100ms? 500ms? Configurable?
2. **Max batch size**: Limit how many apps per rebuild?
3. **Error handling**: If batch fails, should we retry individual apps?
4. **SSE updates**: How to report progress for batched installs?
