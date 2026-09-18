# Backend Tech Debt

> **This is the single debt ledger.** New backend findings go here, not into
> `docs/specs/review.md`; that file is a dated review snapshot whose findings are
> annotated against the statuses recorded here.

**Status:** Active debt inventory  
**Last updated:** 2026-09-17 (route-generation purity landed; prior updates the
same day: versioned migrations + durable operation state)

## Biggest Debt: Credentialless Loopback Admin

`authMiddlewareFn` (`internal/api/router.go`) grants `RoleAdmin` to any
request arriving from loopback or `BLOUD_TRUSTED_LOCAL_NETS` with no
credential at all. Network position *is* the credential. Anything running on
the host — a compromised container with host networking, a stray local
process — gets full operator control of the appliance: install, uninstall,
settings, secrets-adjacent surfaces.

It tops the list now because the two larger structural items above it in
earlier versions (missing operation state, entangled route generation) are
paid, and this is the largest remaining gap between what the system promises
(a controlled operator surface) and what it enforces (an unauthenticated
one). Per the previous audit: the largest security win per line changed in
the repo.

## Evidence (still open)

- `internal/api/router.go`
  - `authMiddlewareFn`: loopback/trusted-net → `RoleAdmin`, no credential.
    The top item above.
- `cmd/host-agent/configure.go` vs `internal/api/router.go`
  - Two hand-built orchestrator configurations (CLI `reconcile` minimal path vs
    the product path). The "how to wire the system core" knowledge exists in
    two places with different config surfaces; drift is silent.
- Share/guest handlers write stores directly: unchanged, and still a
  deliberate, documented boundary (pure store writes, synchronous invite
  tokens). Not a top item.

## Already Paid

### Route generation no longer owns runtime side effects (2026-09-17)

The "Biggest Debt" of the previous version — a route config step that
silently started gateways and mutated proxies — is closed:

- `RegenerateRoutes(remoteRoutes, tailnetDomain)`
  (`internal/engine/orchestrator/orchestrator_containers.go`) is pure
  with respect to the runtime: no gateway calls, no proxy mutation. All
  runtime-shaped inputs are passed in by the caller.
- `SyncRoutes()` owns the explicit ordering the old interleaving only
  accidentally provided: `ensureGateway()` → `reconcileRemoteProxies()`
  (the former `buildRemoteRoutes`, named for what it does) →
  `resolveTailnetDomain()` → config write.
- Contract tests (`route_sync_test.go`): the pure generator touches zero
  runtime fakes even when gateway/proxy are configured; SyncRoutes order
  and input piping; inactive tailnet skips the gateway entirely.

### Durable lifecycle operation state (2026-09-17)

The "Biggest Debt" of the 2026-09-16 version — no readable state behind the
orchestrator's phases — is closed for the backend:

- `store/operations.go`: one row per app, the current-or-last drive:
  `{id, type, phase, status, retryable, cause, timestamps}`, migrated as
  ledger version 7 (`internal/schema/migrations.go`, `schema.sql`).
- `internal/engine/orchestrator/operation_recorder.go`: the drive path
  reports phase boundaries. Every helper is best-effort; recorder failure
  logs and never changes lifecycle behavior.
- Write ordering is "last entered phase, never last completed":
  `runFullLifecycle` records each phase *before* entering it, so a running
  row plus a dead process is a crash trace, not a lie.
- Startup orphan rule: `Start` flips surviving `running` rows to
  failed/retryable ("interrupted by restart") before first convergence;
  normal reconciliation re-drives the work. No resume cursors by design.
- User intent outcome is preserved: failed installs keep their failure row;
  RemoveApp never leaves a failed removal untracked; staleness re-runs heal
  or fail the row without resurrecting completed drives.
- Read surface: `GET` payloads embed `operation` on each installed app
  (LEFT JOIN in `store/apps.go`); the SSE home snapshot carries it
  unchanged. `apps.status` remains the narrowed user projection — the
  operation row is authoritative for failure context only, so the
  three-vocabulary overload on `apps.status` is broken.
- Tests: `store/operations_test.go` (transition matrix),
  `orchestrator/operation_recorder_test.go` (failure recording,
  status-sync completion, orphan flip, steady-state no-write guard),
  `-race` clean across levels.

Design doc: `docs/plans/archive/operation-state-design.md` (landed).

### Versioned migrations + the `user_app_positions` schema fork (2026-09-17)

`internal/db.runMigrations` (seven `_, _ = db.Exec` calls with the version
only in a comment) is replaced by the ordered, checked ledger in
`internal/schema/migrations.go`: a `schema_migrations` table records each
applied version; a failing migration aborts boot instead of half-migrating
durable state. The fork itself — v6's dead
`(user_id, app_id, position)` shape silently shadowing the grid schema via
`CREATE TABLE IF NOT EXISTS` — is migrated by ledger entry 6
(`fix: user_app_positions shape fork`). The landmine the previous version of
this doc flagged as first-ranked is defused; every future migration inherits
the checked path instead of the ad hoc one.

### Earlier (since 2026-09-14)

- Status progression moved fully into the orchestrator: zero `go func(`
  launches in non-test `internal/api` code; `Submit()` records the immediate
  user-visible effect, `setupStatusSync()` owns the transition mapping.
  Invariant 1 holds in practice, not just by convention.
- App-client (S1-S10) gave configurators a deep client layer: `appclient`
  owns transport, retry policy, readiness polling, and asset provenance. All
  five user apps adopted it uniformly with a typed `api.go`. No second
  integration idiom.
- Hardcoded fallback secrets: `config.Load` is fallible
  (env > `secrets.json` > error); the corrupt-file fault-downgrade is closed;
  Jellyfin's bootstrap password moved to the secrets manager.

## Revised Repayment Plan

Ranked by risk times cheapness, not by architectural ambition.

### 1. Loopback Admin Behind a Credential

Grant loopback admin only with a CLI-obtained token (or signed request), not
on bare network position. One middleware change plus one CLI change: the
`ReadRuntimeFile` backend seam carries the token across Lima/QEMU/native.

### 2. Single Orchestrator Builder

One builder function owns the orchestrator wiring; `configure.go` and
`router.go` both call it with an explicit profile. Makes config drift
between the CLI path and the product path impossible. (With operation state
landed, the recorder wiring is one more field that would otherwise fork.)

### 3. Persist Only Externally-Issued Artifacts (rescopes the old item 4)

Integration credentials are derived, not stored: `DeriveSecret`
(HKDF-SHA256 from the host secret) and `OIDCInputsForApp` compute client
credentials, redirect URIs, and issuer/launch URLs as a pure function of
host secret and host set. Persisting derived state creates a second source of
truth and every disagreement becomes a reconciliation bug. What is genuinely
worth persisting is state we do not derive because something else issued it:
remote proxy port assignments, the tailnet domain, gateway state. The old
item 4 is re-scoped to exactly that.

## Non-Goals

- Do not persist derived state (secrets, OIDC inputs) that can be recomputed
  from the host secret. The derivation is the source of truth.
- Do not split files just to reduce line count.
- Do not add a generic workflow engine. The operation row is a ledger, not a
  scheduler.
- Do not introduce speculative provider abstractions before a concrete
  consumer needs them.
- Do not block small product fixes, but avoid adding new lifecycle side
  effects to route generation or API handlers. (Route generation is now a
  pure config step; keep it that way — see the `route_sync_test.go` contract.)

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
