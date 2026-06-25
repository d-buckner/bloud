# Plan: Move Gateway/Proxy Convergence into Reconciler

## Context

`RegenerateRoutes` previously bundled three concerns: starting the gateway
container, reconciling remote reverse proxies, and writing Traefik config.
The previous step extracted `EnsureGateway`, `ReconcileRemoteProxies`, and
made `RegenerateRoutes` side-effect-free — but scattered explicit calls
across every caller.

The correct architecture: **components declare desired state in the store,
the reconciler converges the runtime to match**. Gateway/proxy/route
convergence belongs in the reconciliation loop, not in API handlers.

## Current state (already done)

- `RemoteProxyManagerInterface` with `GetPortAssignments()` — `sharing/remoteproxy.go`
- `EnsureGateway()` and `ReconcileRemoteProxies()` on `PortableOrchestrator`
- Side-effect-free `RegenerateRoutes()` (reads ports, writes Traefik config)
- All three on the `AppOrchestrator` interface
- `FakeRemoteProxyManager`, `FakeRemoteAppStore`, contract test

## Design

Add a **`RoutingReconciler`** interface to the `Reconciler`. The orchestrator
satisfies it. The Reconciler calls it as **Phase 4** at the end of each cycle,
after apps are configured and healthy.

```
Phase 1: PreStart (config files)
Phase 2: HealthCheck (wait for healthy)
Phase 3: PostStart (configure via APIs)
Phase 4: Routing (gateway → proxies → traefik config)  ← NEW
```

API handlers just mutate the store and call `triggerReconcile()`.

**Note:** The reconciler currently runs HealthCheck/PostStart for every app
even when PreStart detected no changes. Making it skip unchanged apps is a
separate optimization (the `changed` bool from `PreStartConfig` is logged
but not acted on). PostStart configurators are already idempotent, so the
full cycle is safe if not maximally efficient.

## Files to modify

1. `orchestrator/reconcile.go` — add `RoutingReconciler` interface, field, setter, Phase 4 call
2. `orchestrator/orchestrator_portable.go` — simplify `Install()`/`Uninstall()` to just `RegenerateRoutes`
3. `orchestrator/interface.go` — remove `EnsureGateway`/`ReconcileRemoteProxies` from `AppOrchestrator`
4. `api/server.go` — wire orchestrator as `RoutingReconciler`; simplify startup; add convergence to startup goroutine
5. `api/settings.go` — `ensureGatewayAndProxies()` becomes `triggerReconcile()`
6. `api/remote_apps.go` — handlers call `triggerReconcile()` instead of explicit steps
7. `orchestrator/fakes_test.go` — add `FakeRoutingReconciler`
8. `orchestrator/reconcile_test.go` — test that Reconciler calls routing at end of cycle

## Steps

### Step 1: Add `RoutingReconciler` to Reconciler

In `orchestrator/reconcile.go`:

Add interface and setter:
```go
type RoutingReconciler interface {
    EnsureGateway(ctx context.Context) error
    ReconcileRemoteProxies()
    RegenerateRoutes() error
}
```

Add `routing RoutingReconciler` field to `Reconciler` struct.

Add `SetRoutingReconciler(rr RoutingReconciler)` setter (same pattern as `SetReconfigDispatcher`).

At end of `Reconcile()`, after optional-dep dispatch, before duration log:
```go
r.mu.Lock()
routing := r.routing
r.mu.Unlock()
if routing != nil {
    if err := routing.EnsureGateway(ctx); err != nil {
        r.logger.Warn("gateway not available", "error", err)
    }
    routing.ReconcileRemoteProxies()
    if err := routing.RegenerateRoutes(); err != nil {
        r.logger.Warn("failed to regenerate routes", "error", err)
    }
}
```

### Step 2: Simplify Install/Uninstall

In `orchestrator_portable.go`, both `Install()` and `Uninstall()` tail blocks become:
```go
installed, _ := o.appStore.GetInstalledNames()
o.graph.SetInstalled(installed)
if err := o.RegenerateRoutes(); err != nil {
    o.logger.Warn("failed to regenerate routes", "error", err)
}
```

`RegenerateRoutes` stays because it's cheap (writes Traefik config) and the
user expects routes to work immediately after install. `EnsureGateway` and
`ReconcileRemoteProxies` are removed — the reconciler handles them via
`triggerReconcile()` which is already called after install/uninstall in
`routes.go`.

### Step 3: Trim AppOrchestrator interface

In `orchestrator/interface.go`, remove `EnsureGateway` and
`ReconcileRemoteProxies`. They stay as public methods on `PortableOrchestrator`
(satisfying `RoutingReconciler`) but aren't part of the API-facing interface.

### Step 4: Wire in server.go

In `initOrchestrator()`, after creating the Reconciler, wire:
```go
if s.reconciler != nil {
    s.reconciler.SetRoutingReconciler(portable)
}
```

Simplify the synchronous startup block back to just `RegenerateRoutes`:
```go
if s.orchestrator != nil {
    if err := s.orchestrator.RegenerateRoutes(); err != nil {
        logger.Warn("failed to regenerate Traefik routes on startup", "error", err)
    }
}
```

Add gateway/proxy convergence to the existing startup goroutine (after
`ReconcileState` finishes and containers are up):
```go
go func() {
    portable.SyncContainerState(context.Background())
    portable.ReconcileState(context.Background())
    // Converge routing after containers are reconciled.
    ctx := context.Background()
    if err := portable.EnsureGateway(ctx); err != nil {
        s.logger.Warn("gateway not available on startup", "error", err)
    }
    portable.ReconcileRemoteProxies()
    if err := portable.RegenerateRoutes(); err != nil {
        s.logger.Warn("failed to regenerate routes on startup", "error", err)
    }
}()
```

### Step 5: Simplify API callers

**`settings.go`** — `ensureGatewayAndProxies()` becomes:
```go
func (s *Server) ensureGatewayAndProxies() {
    s.triggerReconcile()
}
```

**`remote_apps.go`** — both handlers replace the explicit
`ReconcileRemoteProxies` + `RegenerateRoutes` block with:
```go
s.triggerReconcile()
```

### Step 6: Update tests

- Add `FakeRoutingReconciler` to `fakes_test.go` (tracks calls to all 3 methods)
- Add test in `reconcile_test.go`: `TestReconciler_CallsRoutingAtEndOfCycle`
  - Wire Reconciler with `FakeRoutingReconciler`
  - Run `Reconcile()`
  - Assert all 3 routing methods were called, in order
- Existing `TestRegenerateRoutes_NoRuntimeSideEffects` should still pass unchanged

## Verification

```bash
cd services/host-agent && go test ./...
cd apps && go test ./...
```

```bash
./bloud e2e lifecycle --host-only
```
