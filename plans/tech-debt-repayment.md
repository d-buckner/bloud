> Status: draft

# Plan: Tech-Debt Repayment — Next Five PRs

**Source:** `docs/operations/tech-debt.md` (2026-09-16 re-audit)
**Goal:** Execute the ledger's top items as a sequence of small, independently
mergeable PRs, ordered so each one builds on the previous foundation.

---

## PR 1 — Versioned migration ledger + `user_app_positions` fork fix

**The most urgent slice.** Two reasons: the fork is a live silent break on upgraded
databases, and everything durable we add next (operations state) lands on this
mechanism — it must hold first.

**Design decisions**

- New `schema_migrations` table: `version INTEGER PRIMARY KEY, applied_at TEXT`.
- `internal/schema` becomes the owner: `BaselineVersion` (the version the
  `schema.sql` full-DDL corresponds to) + `Migrations []Migration{Version, Up}`.
- Fresh DBs: run `schema.sql`, stamp `BaselineVersion`. Existing DBs: read max
  applied version, run only `Migration`s with `Version > applied`, each in a
  transaction, **errors checked and fatal**.
- `db.go`'s hand-rolled `runMigrations` (`internal/db/db.go:56-78`) is deleted
  whole — no shims. The seven historical `v1..v7` changes are already covered by
  the `schema.sql` baseline for fresh DBs; for upgraded DBs they become
  versioned migration entries (idempotent ALTERs move into the ledger with
  version numbers, errors now checked).

**The fork fix (first real migration above baseline)**

- Detect the dead shape: `PRAGMA table_info(user_app_positions)` contains
  `user_id` or `app_id` instead of `username`/`element_id`.
- Drop the dead table (positions are recoverable from `user_preferences.layout`
  via the existing `migrateLayoutToPositions` logic if present), then create
  the grid shape from `schema.sql:81-90`.
- **Posture change (explicit):** a failed migration now refuses boot rather
  than degrading silently. Right for a reconciliation appliance — half-migrated
  durable state is worse than down — but it changes failure UX: operator sees a
  fatal startup error with the migration version + cause in the journal.
- **Baseline bump rule:** every change to `schema.sql` for fresh DBs must bump
  `BaselineVersion` and add the equivalent `Migration` entry for upgraded DBs.
  Test enforces: baseline DDL ≡ schema.sql + all migrations applied to empty.

**Tests**

- Fresh-DB boot stamps baseline; second boot is a no-op (exactly one ledger row).
- Construct a DB with the v6 dead shape, run the ledger, assert grid shape +
  positions readable through `PositionStore`.
- Out-of-order / re-run safety: applying an already-applied version is a no-op.
- A failing migration aborts and is visible (error returned, version not stamped).

**Files:** `internal/db/db.go`, `internal/schema/*`, new `migrations.go`, tests.

**~1 day. Blocks PR 2.**

---

## PR 2 — Durable lifecycle operation state

**Design required: yes.** Full design in `plans/operation-state-design.md` —
resolves: current-or-last row (not append log), single-writer
`recordPhase`, "last attempted phase" write ordering, startup orphan rule,
and the authoritative-state boundary vs `NodeStatus`. Per-phase resume
cursors explicitly killed (invariant 2 makes re-drive safe). Get that doc
reviewed before starting.

**The most important slice.** Phases stop being control-flow position inside
`runFullLifecycle` and become recorded facts. This is the foundation every
roadmap item (selective restart, retry behavior, provider-output invalidation,
persistent proxy ports) needs, and each of them is cheap-on-top-of /
expensive-without-this.

**Design decisions**

- New table `operations` (created via PR 1's ledger):
  `id, app_name, type (install|uninstall|reconcile|reconfigure), phase
  (planning|topology|prestart|health|poststart|routing|sharing|complete),
  status (pending|running|failed|complete), retryable INTEGER, cause TEXT,
  started_at, updated_at`.
- New `internal/store/operations.go` with `OperationStore`; interface per
  existing store conventions.
- `OperationRecorder` inside the orchestrator: a single method
  `recordPhase(appName, phase, err)` called at each phase boundary in
  `runFullLifecycle` / `Submit` paths. Failure writes phase + `retryable` +
  wrapped cause. Successful pass stamps `complete`.
- `apps.status` semantics unchanged — stays the user-facing projection. This PR
  **adds** the missing layer, does not migrate the old one (that narrowing is a
  later PR when consumers are migrated).
- Crash-after-phase: on restart, the ledger shows `phase=prestart,
  status=running` (orphaned) for the dead op → next reconcile treats it as a
  resumable marker, not a mystery. Initial handling: mark orphaned ops `failed,
  retryable=true` at startup; the normal reconcile path re-drives them.

**Explicitly out of scope** (follow-ups, listed so nobody gold-plates this PR):
rich resume logic per phase, op history retention policy, UI surfacing.

**Tests**

- Contract test: every phase transition in `runFullLifecycle` writes the
  expected `operations` row (drive with the existing mock runtime/configurators).
- Failure mid-prestart → row `phase=prestart, status=failed, retryable=...`,
  cause wrapped.
- Startup-orphan test: pre-seed `running` op, call startup reconcile, assert it
  becomes `failed/retryable=true`.
- `go test -race ./internal/engine/orchestrator/...` stays clean.

**Files:** `internal/store/operations.go`, `internal/schema` migration,
`orchestrator.go` (`Submit`, `runFullLifecycle`, `recordPhase`), tests.

**~2-3 days. Builds on PR 1.**

---

## PR 3 — Remove route-generation side effects

**Fully parallel with PR 2** — touches `orchestrator_containers.go`, not the
operation-state machinery. Good slice for a context switch.

**Design decisions**

- Extract from `RegenerateRoutes` (`orchestrator_containers.go:105-121`):
  - gateway startup: `ensureGateway()` (the `EnsureRunning` call)
  - remote proxy reconciliation: `reconcileRemoteProxies()` → returns the port
    assignments that `buildRemoteRoutes` currently produces as a side effect
  - tailnet domain discovery: `resolveTailnetDomain()`
- `RegenerateRoutes(apps, remoteRoutes, tailnetDomain)` becomes: read
  catalog/store → `traefikGen.GenerateAll(...)`. Pure with respect to runtime:
  no gateway, no proxy mutation.
- The convergence step (`pipeline.go:515`) calls the extracted steps *then*
  `RegenerateRoutes`, in the order that today's interleaving happened to give.

**Tests**

- Contract test: `RegenerateRoutes` with a gateway configured performs zero
  gateway calls (mock gateway with call assertion).
- Convergence ordering test: gateway ensured before routes written (so the
  tailnet domain is available exactly as before).
- Existing route-generation golden tests unchanged.

**Files:** `orchestrator_containers.go`, `pipeline.go`, tests. **~1 day.**

---

## PR 4 — Loopback admin behind a credential

**High value, zero coupling** to the other PRs — can land anytime, or be pulled
forward if you want a quick win between bigger slices.

**Design decisions**

- On boot / `init-secrets`, generate `api_token` in the secrets manager
  (`internal/secrets`).
- Middleware (`router.go:545-548`): the `isLocalRequest` path no longer grants
  admin on network position alone — requires `Authorization: Bearer <api_token>`.
  Loopback-without-token → 401 for all authenticated routes.
- **Token distribution across backends (the resolved hole).** "CLI reads
  `secrets.json`" is false for Lima (data dir is guest-only) and QEMU
  (needs SSH). Resolution: the CLI's existing backend abstraction gains
  `ReadRuntimeFile(path)`: native = direct read of
  `/var/tmp/bloud-native-runtime/data/secrets.json`; lima =
  `limactl shell <inst> cat …`; qemu = `ssh -p 2222 … cat …`. The CLI fetches
  the token once per invocation through that seam and attaches it as
  `Authorization: Bearer`.
- **`bloud token` command** prints the runtime token (resolved via the same
  seam). e2e's Playwright helpers run host-side TS, so they shell out to
  `bloud token` (or its underlying `bloud shell cat` primitive) in their API
  fixture — one helper in `e2e/lib/`, not per-spec plumbing.
- Update AGENTS.md: the e2e note "loopback, no auth needed" becomes
  "loopback + `bloud token` bearer credential".
- Keep `BLOUD_TRUSTED_LOCAL_NETS` semantics for remote ops, but now
  token-gated (the token is the credential; the net restriction stays as a
  second factor of "where it may be used").

**Tests**

- Loopback without token → 401; with token → as today.
- Existing api/auth tests updated; middleware unit tests for token compare
  (constant-time).
- e2e suite green with token plumbing.

**Files:** `router.go` middleware, `internal/secrets`, `cli/*` request helpers,
`e2e/lib/*`, tests. **~1-2 days** (mostly the e2e ripple).

---

## PR 5 — Single orchestrator builder

**Optional glue; pull before PR 2 if the wiring churn annoys.** Two
`NewOrchestrator` sites with divergent config (`cmd/host-agent/configure.go:246`
CLI-reconcile vs `internal/api/router.go:430` product path) is duplicated
"how to wire the system core" knowledge.

**Design decisions**

- One constructor function (e.g. `engine.Build(c BuildConfig)` or a single
  `wiring.go` in `cmd/`/`api/` shared location) that owns the full dependency
  set and returns a fully-wired orchestrator.
- The CLI reconcile path calls the same builder with the same subsystems the
  router enables — the CLI stops lying about being a faithful mini-wiring.
  (If any subsystem genuinely must differ in CLI mode, the builder takes an
  explicit option for it; the default is the product path.)
- Graph construction (`configure.go:219` / `router.go:358` builds the
  in-memory graph from the app store) also collapses into the builder.

**Tests**

- Builder unit test: given the same config inputs, both call sites produce the
  same subsystem set (assert non-nil set equality).
- Existing orchestrator/router tests updated to the builder; old direct
  `NewOrchestrator` call sites deleted.

**Files:** new builder file, `configure.go`, `router.go`, tests. **~1 day.**

---

## Sequencing

```
PR 1 (ledger) ──→ PR 2 (operation state) ──→ [narrow apps.status later]
PR 3 (route purity)      parallel with 2
PR 4 (loopback token)    anytime
PR 5 (builder)           before 2 if it helps, else after
```

Merge discipline per the ledger's validation bar: each PR leaves a contract test
for its boundary, a failure/crash test where applicable, and the code it
replaces is deleted in the same PR — no shims. `./bloud validate --tier fast`
green on each; PR 2 also runs `-race` on the orchestrator.
