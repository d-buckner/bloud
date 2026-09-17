# Backend Tech Debt

> **This is the single debt ledger.** New backend findings go here, not into
> `docs/specs/review.md`; that file is a dated review snapshot whose findings are
> annotated against the statuses recorded here.

**Status:** Active debt inventory  
**Last updated:** 2026-09-16 (re-audited after the S1-S10 app-client series,
layout cleanup, and the security pass landed; previous version 2026-09-14)

## Biggest Debt: Lifecycle Operation State Is Missing

The orchestrator has phases but no state for them. `runFullLifecycle`
(`internal/engine/orchestrator/orchestrator.go`) interleaves prestart config,
container/network creation, health checks, poststart config, and SSO
provisioning in one control-flow block. Phases are implicit in code position,
not facts anyone can read. A failure mid-pass records an ERROR with a message
but loses which phase it happened in, whether it was retryable, and where a
resume should land. Meanwhile `apps.status` carries install progress, observed
runtime health, and user-visible state at once, in three vocabularies from
three writers.

The previous version of this doc said lifecycle state was "spread across the
API server, the engine, stores, route generation, and sharing managers." Half
of that is now obsolete. The API side is paid (see "Already Paid" below). What
remains is inside the orchestrator, and it is narrower: a missing
operation-state model, not a missing single writer.

The conflict with the release architecture in `docs/specs/spec.md` is
unchanged: the spec requires explicit invalidation, phase-specific failure
records, and resume-after-restart semantics that the current code cannot
express.

## Evidence

- `internal/engine/orchestrator/orchestrator.go`
  - `runFullLifecycle` interleaves all lifecycle phases per node. Phase
    context is control flow, not recorded state.
  - `setupStatusSync` maps graph transitions onto `apps.status` as "the single
    authoritative path from graph state to DB status". Correct as far as it
    goes: the field it writes into is the overloaded one.
- `internal/engine/orchestrator/orchestrator_containers.go`
  - `RegenerateRoutes` also starts the gateway (`EnsureRunning`), queries the
    tailnet domain, and reconciles remote app proxies (`buildRemoteRoutes`).
    Route generation still has runtime side effects.
- `internal/store/apps.go`
  - `apps.status` carries three meanings from three writers: `Submit` writes
    "installing", the orchestrator status sync writes "running"/"error",
    `SyncContainerState` writes "stopped"/"running", and `EnsureSystemApp`
    pins "running". No single owner of the field's meaning.
- `internal/engine/graph/`
  - Production wires the in-memory `MapRepository` in both
    `cmd/host-agent/configure.go` and `internal/api/router.go`;
    `SQLiteRepository` exists but is never constructed.
  - Nuance the previous version lacked: because integration credentials are
    HKDF-derived from the host secret, rebuilding the graph from the app store
    on restart actually works. What is lost is only ERROR-terminal semantics,
    and a restart retries those apps anyway, which is arguably the
    self-healing behavior we want. This is less severe than the earlier
    "durable backing is dead code" framing implied.
- `internal/api/router.go`
  - `authMiddlewareFn` grants `RoleAdmin` to any loopback/trusted-net request
    with no credential. Still open.
- `internal/db/db.go`
  - `runMigrations` is seven `_, _ = db.Exec` calls with the version recorded
    only in a comment. The error-ignoring is deliberate (idempotent ALTERs),
    but the versionless ledger is what produced the fork below.
- Share/guest handlers write stores directly: unchanged, and still a
  deliberate, documented boundary (pure store writes, synchronous invite
  tokens). Not a top item.

## The Landmine the Previous Version Missed: `user_app_positions` Schema Fork

`runMigrations` v6 creates `user_app_positions(user_id, app_id, position)`.
`internal/schema/schema.sql` defines the same table as
`(username, element_id, element_type, x, y, w, h)`, and that is the shape
`store/positions.go` queries. On fresh databases `schema.sql` wins and
everything works. On any database that ran v6 before the grid redesign,
`CREATE TABLE IF NOT EXISTS` is a no-op, the table keeps the dead v6 shape,
and every position read and write fails at query time. The write path is
best-effort and swallows errors, so upgraded installs break the dashboard
layout silently.

This is what "ad hoc migrations increase risk as state tables become more
important" looked like when it became real. It ranks first because it is cheap
to fix, already has a broken upgrade path as live evidence, and every future
migration inherits the same failure mode if the mechanism is not fixed.

## Already Paid Since 2026-09-14

- The old plan's item 2, "move status progression out of API background
  helpers into the orchestrator", is done. There are zero `go func(` launches
  in non-test `internal/api` code. All non-test `UpdateStatus` callers live
  inside the orchestrator: `Submit()` records the immediate user-visible
  effect, `setupStatusSync()` owns the transition mapping. Invariant 1 (the
  orchestrator is the only lifecycle writer) holds in practice, not just by
  convention.
- App-client (S1-S10) gave configurators a deep client layer: `appclient`
  owns transport, retry policy, readiness polling, and asset provenance. All
  five user apps (jellyfin, homeassistant, immich, affine, navidrome) adopted
  it uniformly with a typed `api.go` over it. There is no second integration
  idiom. The tactical per-app HTTP and retry code that amplified changes in
  the old shape is gone.
- Hardcoded fallback secrets: fixed 2026-09-14. `config.Load` is fallible
  (env > `secrets.json` > error), the corrupt-file fault-downgrade is closed,
  and Jellyfin's bootstrap password moved to the secrets manager.

## Revised Repayment Plan

Ranked by risk times cheapness, not by architectural ambition.

### 1. Versioned Migrations (fix the schema fork with them)

Replace `runMigrations` with a versioned ledger: a `schema_migrations` table,
an ordered list of applied versions, checked errors, each migration
individually tested. `schema.sql` becomes the generated baseline and the
migration list is the single upgrade path. Concrete first fix inside the
ledger: detect the dead v6 `user_app_positions` shape (a `user_id` column),
migrate or drop it, then apply the grid schema.

This needs no heavyweight framework. Ordered, idempotent, checked.

### 2. Remove Route-Generation Side Effects

Unchanged from the previous version; still the right first boundary slice.
Extract gateway startup (`EnsureRunning`) and remote proxy reconciliation
(`buildRemoteRoutes`) out of `RegenerateRoutes` into explicit reconciliation
steps. `RegenerateRoutes` should only compute and write Traefik
configuration. Add a test that proves route generation has no runtime side
effects.

### 3. Durable Lifecycle Operation State

Add a minimal operation-state model separate from `apps.status`:

- operation ID
- app name
- operation type: install, uninstall, reconcile, reconfigure
- phase: planning, topology, prestart, health, poststart, routing, sharing, complete
- status: pending, running, failed, complete
- retryability and failure cause

Design consequence: `runFullLifecycle` becomes a phase reporter instead of a
phase owner. Keep `apps.status` as user-facing observed state until it can be
narrowed. This single model kills the three-vocabulary writes that
`apps.status` lives on today.

### 4. Loopback Admin Behind a Credential

Grant loopback admin only with a CLI-obtained token (or signed request), not
on bare network position. One middleware change plus one CLI change: the
largest security win per line changed in the repo.

### 5. Single Orchestrator Builder

`cmd/host-agent/configure.go` (CLI `reconcile`, minimal config) and
`internal/api/router.go` (full product path) each hand-construct the
orchestrator with different config surfaces. The "how to wire the system
core" knowledge exists in two places. One builder function makes drift between
them impossible.

### 6. Persist Only Externally-Issued Artifacts (rescopes the old item 4)

The old plan called for persisting integration instances (provider identity,
consumer identity, type, revision, status). That contradicts the design that
actually shipped: integration credentials are derived, not stored.
`DeriveSecret` (HKDF-SHA256 from the host secret) and `OIDCInputsForApp`
compute client credentials, redirect URIs, and issuer/launch URLs as a pure
function of host secret and host set. Persisting derived state creates a
second source of truth for values that are already recomputable, and every
disagreement between the copies becomes a reconciliation bug.

What is genuinely worth persisting is state we do not derive because
something else issued it: remote proxy port assignments, the tailnet domain,
gateway state. The old item 4 is re-scoped to exactly that.

## Non-Goals

- Do not persist derived state (secrets, OIDC inputs) that can be recomputed
  from the host secret. The derivation is the source of truth.
- Do not split files just to reduce line count.
- Do not add a generic workflow engine.
- Do not introduce speculative provider abstractions before a concrete
  consumer needs them.
- Do not block small product fixes, but avoid adding new lifecycle side
  effects to route generation or API handlers.

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
