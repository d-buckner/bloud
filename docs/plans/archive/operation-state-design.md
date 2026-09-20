> Status: landed 2026-09-17 (backend + read-surface API landed; UI tile rework deferred per §9)

# Design: Durable Lifecycle Operation State

**Issue:** `docs/operations/tech-debt.md`: "Missing lifecycle operation state"
**Depends on:** versioned migration ledger (`docs/plans/tech-debt-repayment.md` PR 1)
**Decision needed before implementation: yes (this doc)**

---

## 1. The Problem, Precisely

`runFullLifecycle` interleaves prestart → container ensure → health → poststart
→ SSO provisioning. When it fails, the only durable facts are
`apps.status='error'` and one string (`last_error`). Lost:

- **Phase**: did it die in prestart, health, or poststart?
- **Retryability**: is this a transient wait failure or a broken config?
- **Crash visibility**: if the host-agent died mid-phase, nothing shows it.
- **Operation identity**: the user's install and the background reconcile are
  indistinguishable in the error record.

The spec (`docs/specs/spec.md`) requires phase-specific failure records and
resume-after-restart. Today neither exists.

## 2. The Design Question That Matters: What Is "Operation State" FOR?

Pressure-testing three candidate purposes:

**(a) A durable resume cursor.** Persist exact phase position so restart resumes
mid-lifecycle.
**REJECTED.** Invariant 2 (configurators run every reconcile cycle, must be
idempotent) plus convergent `ensureContainer` mean *re-running from the top is
always safe*. A cursor adds a second state machine that can disagree with the
graph, buys nothing correctness-wise, and creates a scope monster (per-phase
resume semantics for 8 phases × 4 operation types). Kill this purpose
explicitly.

**(b) A failure-context ledger + crash detector.** Record, per app, the current
or most recent drive: what was attempted, which phase it last entered, outcome,
retryability, cause.
**ADOPTED.** This is what consumers (dashboard tiles, `./bloud status`,
future retry policy, e2e assertions) actually need.

**(c) A history/audit log.** Full timeline of all operations ever.
**DEFERRED.** No consumer today. Design allows evolving to it (add table,
keep writer) but nothing in this design requires append semantics.

## 3. Core Model

One row per app: **current-or-last operation**, upserted. Not append-only.

```sql
CREATE TABLE operations (
    app_name    TEXT PRIMARY KEY,          -- one row per app, upserted
    id          TEXT NOT NULL,           -- operation id (uuid), fresh per user intent
    type        TEXT NOT NULL,           -- install | uninstall | reconfigure | reconcile
    phase       TEXT NOT NULL,           -- planning|topology|prestart|health|poststart|routing|sharing|complete
    status      TEXT NOT NULL,           -- running | failed | complete
    retryable   INTEGER NOT NULL DEFAULT 1,
    cause       TEXT NOT NULL DEFAULT '',-- wrapped error message
    started_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
```

### 3.1 Who writes (single-writer rule preserved)

Only the orchestrator's drive path writes, via one internal method:

```go
func (o *Orchestrator) recordPhase(appName string, phase Phase, status Status, err error)
```

- **Created** when a user intent is accepted (`Submit`): `type=install|
  uninstall|reconfigure`, `phase=planning`, `status=running`.
- **Updated** at every phase boundary during the drive.
- **Steady-state reconcile** (boot convergence of already-RUNNING apps,
  staleness re-runs): upserts a `type=reconcile` row **only when it drives a
  non-trivial transition** (re-running lifecycle or poststart for an app that
  was in failed/error state). Pure no-op passes write nothing. This is the
  rule that prevents per-cycle write amplification:
  **a reconcile pass with no failure and no restart-of-failed-work writes zero
  rows.**
- API handlers never touch it (they already only call `Submit`).

### 3.2 Write ordering: "last attempted phase", never "last completed"

Write the phase row **before entering** the phase, terminal status after.
Crash mid-poststart ⇒ row reads `phase=poststart, status=running`. That's the
crash detector. A crash-after-completion is handled by the ordering:
`complete` is the last durable write, so `running` + all-live means "not
finished, honestly."

### 3.3 Startup orphan rule

At orchestrator start, before first convergence:

```
UPDATE operations SET status='failed', retryable=1,
       cause='interrupted by host-agent restart'
WHERE status='running'
```

Everything was running-but-incomplete (per 3.2 ordering). These are then
re-driven by normal convergence, which is safe by invariant 2. The row is
diagnostic continuity ("your install was interrupted at prestart; it's
resuming as a reconcile drive"), not a cursor.

## 4. Authoritative-State Boundary (the drift-prevention section)

**Graph state (`NodeStatus` target/actual) is authoritative for convergence
control. The operations row is authoritative for failure context and
user-intent outcome. No consumer reads both to decide one thing.**

| Question | Answered by |
|---|---|
| Should this node be worked this pass? | graph (target vs actual, ERROR-terminal, deps) |
| Is this app converged/running? | graph actual status → `apps.status` projection |
| What did the user ask for and where did it go wrong? | operations |
| Is the current failure retryable / what phase died? | operations |

The two are written in the *same* drive path (phase boundary updates both), so
they can lag each other by microseconds but never diverge structurally. If a
future change makes anything read `operations` to gate the reconcile loop,
that is a design violation; flag it. Operations is downstream of control,
upstream of display.

**Rejected alternative:** enrich `NodeStatus` with failed-phase variants
(`PRESTART_FAILED`, `HEALTH_FAILED`, ...). Rejected: multiplies the status
enum (8 phases × terminal variants), touches every existing consumer of
`NodeStatus` (SSE, dashboard mapping, staleness logic), and still has no
place for operation identity or retryability.

## 5. Retryability Semantics (kept minimal)

- Configurator/orchestrator code marks transient failures by wrapping in a
  typed `ErrRetryable` (or setting a flag on the phase write). Default:
  retryable.
- This design **records** retryability. Acting on it (auto-retry loops,
  backoff policy) is a later consumer, non-goal here. Today the user's retry
  path is whatever reset/reinstall action already exists; the flag tells
  *that* flow (and future automation) whether retry is worth attempting.

## 6. Interaction with Existing Semantics

- `apps.status`: unchanged. Still the 4-value user projection ("error").
  Operations explains the error. The eventual narrowing of `apps.status` is
  a separate future PR that migrates consumers one at a time.
- **ERROR-terminal stays.** The operations row is the explainer: when a node
  goes ERROR, its row already carries phase/cause from the failed boundary
  write.
- `Submit()`'s existing `recordInstallNow` ("installing" write) stays; the
  operations row is added alongside it, same call site.
- `runPostStartOnly` (staleness re-run) updates the row as
  `type=reconcile, phase=poststart` only if it fails or was previously
  failed: same no-silent-writes rule as 3.1.

## 7. Read Surfaces (minimal this PR)

- `apps.{name}` payloads and the SSE home snapshot gain an `operation` field:
  `{type, phase, status, retryable, cause, updated_at}`.
- Dashboard tile detail: "install failed at poststart: retryable" replaces
  the raw `last_error` string blob (UI change optional in this PR; the API
  contract lands regardless).
- `./bloud status` can show last-operation per app.

## 8. Testing Contract

- **Boundary contract:** every phase entry/exit in `runFullLifecycle` drives
  the expected row transition (mock runtime + configurators; assert full
  sequence of (phase,status) pairs for an install).
- **Failure matrix:** mid-prestart / mid-health / mid-poststart failure →
  row shows matching phase, `failed`, correct retryable/cause.
- **Crash test:** row `status=running` pre-seeded (simulates mid-phase
  death) → startup rule flips to `failed/retryable=1` and boot convergence
  re-drives.
- **No-silent-write test:** steady-state reconcile of RUNNING apps with a
  no-op staleness pass writes zero rows (guards the amplification rule).
- `-race` on the orchestrator stays clean (concurrent levels hit the store).

## 9. Explicit Non-Goals

- Resume cursors (see 2a)
- Operation history/audit (see 2c)
- Auto-retry policy, backoff
- Migrating or narrowing `apps.status`
- UI rework (API lands first)

## 10. Open Questions for Review

1. **Reconfigure type:** `rename`/`SetHostsIntent` flows re-drive apps. Do
   they create `reconfigure` rows, or is their failure context carried by
   the reconcile row? (Recommend: SetHosts resets SSO apps → those get
   `reconcile` rows; only deliberate per-app reconfigure actions create
   `reconfigure`.)
2. **Multi-container nodes:** phase writes are per *node*, but the row is
   keyed by *app*. Recommend: row keyed by app; phase boundary writes use
   the app-level drive; container-node-level detail stays in graph + SSE.
   Confirm this doesn't hide per-container failure causes (cause string can
   name the container).
3. **Row lifetime:** last operation row persists forever after `complete`.
   Alternative: delete on complete, lose crash-visibility window. Recommend
   persist; it's one row per app.
