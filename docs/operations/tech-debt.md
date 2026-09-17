# Backend Tech Debt

> **This is the single debt ledger.** New backend findings go here, not into
> `docs/specs/review.md`. That file is a dated review snapshot whose findings are
> annotated against the statuses recorded here.

**Status:** Active debt inventory  
**Last updated:** 2026-09-16 (re-verified against HEAD `13dfd11`)

## Biggest Debt: Lifecycle State Ownership

The largest backend debt is that application lifecycle state does not have one clear
owner. Desired state, observed state, side effects, and recovery behavior are spread
across the engine, stores, route generation, and sharing managers.

The intent side of this has been tightened since the note was first written: the API now
submits typed intents and nothing but the orchestrator advances lifecycle status. What
has not been repaid is the durability half. The lifecycle graph lives in process memory,
so observed state, terminal ERROR markers, and phase context are rebuilt from scratch on
every start rather than resumed.

This makes Bloud behave more like a sequence of assumed-success commands than a durable
reconciliation system. That conflicts with the release architecture in
`docs/specs/spec.md`, which requires durable desired and observed application and
integration state, explicit invalidation, phase-specific failure records, and
resume-after-restart semantics.

## Evidence

Re-verified 2026-09-16 against HEAD `13dfd11`. Statuses reflect the code as it
stands now, not as of the original note.

### Still open

- `internal/engine/orchestrator/orchestrator_containers.go:82` (`RegenerateRoutes`)
  still has runtime side effects. It calls `gateway.EnsureRunning` (line 106) and
  `buildRemoteRoutes`, which calls `remoteProxy.Reconcile` (line 154) and therefore
  starts and stops live listeners. No test asserts that route generation is free of
  runtime side effects. This is the "First Contained Slice" below, still unclaimed.
- `internal/engine/graph/sqlite_repository.go` is still dead code.
  `NewSQLiteRepository` has zero call sites outside its own file; both production
  wirings use the in-memory map: `internal/api/router.go:358` and
  `cmd/host-agent/configure.go:219`. The `graph_nodes` and `graph_edges` tables are
  created by migration v5 and never written. Consequence: the graph is rebuilt from
  the app store on every start with every node at `INITIALIZING`
  (`pipeline.go:660`), so a node's terminal ERROR state and its phase context are
  lost across a restart. `apps.status` keeps the user-visible `error` string only
  until the next reconcile overwrites it.
- `internal/api/router.go:545` still grants admin to any loopback or
  `BLOUD_TRUSTED_LOCAL_NETS` request with no credential: `isLocalRequest` injects
  `User{Username: "_cli", Role: RoleAdmin}`.
- `internal/store/apps.go:19`: the single `Status` string still carries install
  progress, observed runtime health, and user-visible state. `last_error` (v7) added
  a message but no phase, retryability, or operation identity.
- `internal/db/db.go:56` (`runMigrations`) still fires seven
  `_, _ = db.Exec(...)` statements with errors deliberately ignored, on every boot.
- `runFullLifecycle` (`orchestrator.go:950`) still interleaves prestart config, SSO
  provisioning, container creation, health checks, and poststart config in one pass.

### Landed since the 2026-09-14 note

- ~~Background goroutines can update status outside the same operation owner.~~
  No non-test file under `internal/api/` calls `UpdateStatus` or `SetLastError`
  anymore. `setupStatusSync` (`orchestrator.go:246`) is the single authoritative
  graph-to-DB status path, and install/uninstall/rename handlers submit intents and
  return 202 with an intent ref (`apps_module.go:334-396`).
- Startup container sync and startup state reconciliation now run inside the
  orchestrator's convergence pass (`pipeline.go:451-491`), not in API helpers.
  Caveat carried forward below: `SyncContainerState` skips every multi-container app.
- Dead share intent types removed 2026-09-14. Share and guest handlers still write
  stores directly (`sharing_module.go:242,297,351`), which remains a deliberate,
  documented boundary: pure store writes, no lifecycle side effects, because invite
  tokens must return synchronously.
- ~~Hardcoded fallback secrets ship in the production path~~ **Fixed 2026-09-14**:
  `Load` is fallible; resolution is env > `secrets.json` (auto-generated when
  missing) > error, with no static fallback and the corrupt-file fault-downgrade
  closed. Jellyfin's bootstrap password migrated to the secrets manager.

### Partially repaid

- Schema source of truth landed: `internal/schema` now holds the single embedded
  `schema.sql` with a checked `Run(db)`, applied by both `db.InitDB` and `testdb`,
  so prod and test schemas cannot drift. The ad hoc, error-ignoring `runMigrations`
  in `db.go` is still present and still the path for incremental column changes.

## Why It Matters

New lifecycle features must thread through too many layers. Examples include persistent
remote proxy ports, gateway FQDN persistence, owner remote access, provider-output
invalidation, selective restarts, and retry behavior.

The current shape creates recurring risks:

- Partial failures are hard to resume precisely after host-agent termination or reboot.
- ~~Background goroutines can update status outside the same operation owner.~~
  Closed 2026-09-16: `setupStatusSync` is the only graph-to-DB status writer and API
  handlers no longer advance status themselves.
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

~~Move status progression and phase handling out of API background helpers and into the
orchestrator.~~ **Largely landed 2026-09-16.** API handlers submit intents and return
202 with an intent ref; `setupStatusSync` (`orchestrator.go:246`) is the single
graph-to-DB status path; startup container sync and startup reconciliation run inside
the convergence pass.

Remaining:

- `SyncContainerState` only covers single-container apps. It bails on any app whose
  catalog declares more or fewer than one container
  (`orchestrator_containers.go:45`, `len(defs) != 1`), so Immich, AFFiNE, and
  Authentik get no startup drift correction. Extending it to walk container defs and
  reconcile per node is the next concrete step here.
- Health reconciliation for `starting` and `error` apps still has no durable record of
  which phase failed, so it cannot resume mid-operation. That is item 1.

### 3. Split Routing From Runtime Effects

Make route generation pure with respect to runtime state. Gateway startup and remote proxy
reconciliation should be explicit reconciliation steps, not hidden side effects of
`RegenerateRoutes`.

### 4. Introduce Durable Integration Instances

Persist desired and observed integration instances with provider identity, consumer
identity, integration type, revision, status, and failure phase. Use this before adding
more provider-output or selective restart behavior.

### 5. Replace Ad Hoc Migrations

~~Introduce a single schema source of truth.~~ **Half landed 2026-09-16:**
`internal/schema` now owns one embedded `schema.sql` with a checked `Run(db)`, and
both `db.InitDB` and `testdb` apply it, so prod and test schemas cannot drift.

Remaining: retire `runMigrations` in `db.go`. Its seven error-ignoring statements
should move into the schema package as ordered, idempotent, test-covered steps that
fail loudly, before any further durable state columns are added.

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

Baseline re-run 2026-09-16 at HEAD `13dfd11`:

```sh
cd services/host-agent && go test ./...
cd apps && go test ./...
```

Both pass.
