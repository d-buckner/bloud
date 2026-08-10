# Backend Tech Debt

**Status:** Active debt inventory  
**Last updated:** 2026-06-23

## Biggest Debt: Lifecycle State Ownership

The largest backend debt is that application lifecycle state does not have one clear
owner. Desired state, observed state, side effects, and recovery behavior are currently
spread across the API server, portable orchestrator, reconciler, stores, route generation,
and sharing managers.

This makes Bloud behave more like a sequence of assumed-success commands than a durable
reconciliation system. That conflicts with the release architecture in `specs/spec.md`, which
requires durable desired and observed application and integration state, explicit
invalidation, phase-specific failure records, and resume-after-restart semantics.

## Evidence

- `internal/orchestrator/orchestrator.go` + `orchestrator_containers.go`
  - `Reconcile` runs topological levels concurrently; `runFullLifecycle` interleaves
    prestart config, container/network creation, health checks, poststart config, and SSO
    provisioning.
  - `RegenerateRoutes` also starts the gateway and reconciles remote app proxies, so
    route generation has runtime side effects.
- `internal/orchestrator/pipeline.go`
  - `applyInstallIntent` resolves dependencies via `catalogGraph` and records apps; the
    lifecycle graph itself is in-memory (`MapRepository`) — the durable SQLite backing is
    dead code, so ERROR-terminal state does not survive restart.
- `internal/api/router.go`
  - Constructs the orchestrator with `CatalogGraph: nil`, making the install path inert
    in production. Grants admin to any loopback request without a credential.
  - Share/guest handlers write to stores directly, bypassing the intent queue.
- `internal/store/apps.go`
  - The `apps.status` field is doing too much work. It represents install operation
    progress, observed runtime health, and user-visible application state.
- `internal/config/config.go`
  - Hardcoded fallback secrets (`password`, `dev-secret-change-in-production`, etc.) ship
    in the production config path when `secrets.json`/env are unset.
- `internal/db/db.go`
  - Schema migration is ad hoc and ignores errors. This increases risk as state tables
    become more important.

## Why It Matters

New lifecycle features must thread through too many layers. Examples include persistent
remote proxy ports, gateway FQDN persistence, owner remote access, provider-output
invalidation, selective restarts, and retry behavior.

The current shape creates recurring risks:

- Partial failures are hard to resume precisely after host-agent termination or reboot.
- Background goroutines can update status outside the same operation owner.
- Routing and sharing side effects can happen while generating routes.
- Failures lose phase context such as provider, integration type, retryability, and cause.
- Tests verify many pieces, but there is no single contract for lifecycle state transitions.

## Target Shape

Move toward this ownership model:

1. API writes desired intent only.
2. Planner (catalog.AppGraph) calculates deterministic topology and integration changes.
3. Orchestrator exclusively advances observed state and operation state.
4. Runtime, routing, sharing, health, and configurators are effect adapters.
5. Store persists separate desired state, observed state, operation state, and integration
   state.

The target is not a large rewrite. Each slice should create a narrower contract and move
one lifecycle responsibility behind it.

## Suggested Repayment Plan

### First Contained Slice: Remove Route-Generation Side Effects

The shortest useful first PR is to extract gateway startup and remote proxy reconciliation
out of `RegenerateRoutes`.

`RegenerateRoutes` should only compute and write Traefik configuration. Starting the
gateway and reconciling remote proxies should become explicit reconciliation steps with
their own tests. This is smaller than introducing the full durable operation model, but it
sets the correct boundary and prevents new lifecycle behavior from accumulating inside
route generation.

Add a test that proves route generation has no runtime side effects.

### 1. Define Durable Operation State

Add a minimal operation-state model separate from `apps.status`.

Track:

- operation ID
- app name
- operation type: install, uninstall, reconcile, reconfigure
- phase: planning, topology, prestart, health, poststart, routing, sharing, complete
- status: pending, running, failed, complete
- retryability and failure cause

Keep `apps.status` as user-facing observed state until it can be narrowed.

### 2. Make the Orchestrator the Only Lifecycle Mutator

Move status progression and phase handling out of API background helpers and into the
orchestrator. API handlers should request work and return the current operation/result.

First candidates:

- startup container sync
- startup state reconciliation
- install/uninstall follow-up reconciliation
- health reconciliation for `starting` and `error` apps

### 3. Split Routing From Runtime Effects

Make route generation pure with respect to runtime state. Gateway startup and remote proxy
reconciliation should be explicit reconciliation steps, not hidden side effects of
`RegenerateRoutes`.

### 4. Introduce Durable Integration Instances

Persist desired and observed integration instances with provider identity, consumer
identity, integration type, revision, status, and failure phase. Use this before adding
more provider-output or selective restart behavior.

### 5. Replace Ad Hoc Migrations

Introduce versioned schema migrations with checked errors before adding more durable
state columns. This does not need a heavy framework, but migrations should be ordered,
idempotent, and test-covered.

## Non-Goals

- Do not split files just to reduce line count.
- Do not add a generic workflow engine.
- Do not introduce speculative provider abstractions before a concrete consumer needs
  them.
- Do not block small product fixes, but avoid adding new lifecycle side effects to route
  generation or API handlers.

## Validation Bar

Each repayment slice should leave behind:

- a lower-level contract test for the new boundary
- a crash/retry or partial-failure test where applicable
- no new app-specific branch in shared orchestration
- a clear deletion path for replaced code

Current baseline as of this note:

```sh
cd services/host-agent && go test ./...
cd apps && go test ./...
```

Both pass.
