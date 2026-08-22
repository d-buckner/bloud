# Plan: App Install & Dashboard — Live State Streaming

## Problem

Installing an app feels clunky and the dashboard doesn't communicate state well:

1. **Dead time after click.** Clicking Install returns `202 {intentId}` with no state.
   The orchestrator's intent queue debounces **5 s** (`queue.go`) before it plans the
   install and writes the `installing` row, and the frontend only learns of it on the
   next `/api/user/home` poll (**10 s idle / 2 s active** — and it's on the 10 s clock
   until a poll has already seen a transitioning app). Result: ~7–17 s of nothing.
2. **Opaque long phases.** Once the tile appears it shows a bare spinner for the rest
   of the install — image pull (minutes for large images), PreStart, start, PostStart,
   health — with zero phase information.
3. **Failures are dead ends.** Status flips to `failed`; no error message, no log link,
   no obvious retry. The tile is even `pointer-events: none` while installing, so the
   user can't investigate.
4. **Dashboard state is flat.** `running / starting / installing / uninstalling /
   stopped / error / failed` are 7 states with blurry semantics and nearly identical
   visuals. Multi-container apps (Immich = 4) show one spinner instead of per-component
   progress. There is no "needs attention" signal anywhere.

### Assets already in the codebase

- `graph.Graph` has an **event system**: `On(EventNodeUpdated, handler)`,
  `Node{ID, TargetStatus, ActualStatus, Error}`. Lifecycle states
  `INITIALIZING → PRESTART_CONFIG → STARTING → POSTSTART_CONFIG → RUNNING / ERROR`
  are already driven by the reconciler. *The phase model exists; it's just not shown.*
- `AppStore` has a **`SetOnChange(fn)` hook** that fires on every app-row mutation.
- The podman client already speaks the **Podman HTTP API** (container create via
  `POST /libpod/containers/create`), which has a **streaming pull endpoint**
  (`POST /libpod/images/pull?reference=…&decoding=true`) that emits JSON progress
  events. (Today `PullImage` execs `podman pull` and swallows the output.)
- **SSE plumbing exists**: `GET /api/apps/{name}/logs` and
  `GET /api/system/status/stream` in `logs_module.go`.
- `/api/system/developer` already exposes graph nodes + orchestrator activity
  (`queueDepth`, `isConverging`, `recentActivity`) — proof the state is all there.

## Why SSE this time (and not for layout)

`layout-refactor.md` rejected SSE because layout is a **two-way split-state problem**
(positions flow both directions; the hub needed reconnect loops, suppression flags,
pending-install tracking). App lifecycle is different: **the orchestrator is the sole
writer** (invariant #1) and the dashboard never writes state back. This is a pure
one-way broadcast of server truth — the shape SSE handles well, with no write-back
reconciliation. Layout stays polling; app state becomes push.

## Architecture

### 1. Event bus (new, small)

`internal/orchestrator/eventbus.go` (or `internal/apievents`): in-process pub/sub.

- Subscribers register a buffered channel (cap ~16). Non-blocking publish; a slow
  consumer that overflows is dropped and, on reconnect, receives a fresh snapshot.
- Producers (all read-only consumers of existing state):
  1. `AppStore.SetOnChange` → `apps-changed` (row is the truth).
  2. `graph.On(EventNodeUpdated)` → `node` events (per-container phase + error).
  3. Container-runtime pull progress callbacks → `pull` events (throttled ≤ 2/s).
  4. Orchestrator `recordActivity` → `activity` events (timeline + toasts).
- The bus is started in `NewRouter`/`main` wiring alongside the orchestrator; it
  never writes state.

### 2. SSE endpoint

`GET /api/apps/events` (authenticated group, same middleware as `/api/user/home`;
loopback/trusted-net bypass unchanged).

Events:

| Event | Payload | Fired |
| --- | --- | --- |
| `snapshot` | full home payload (same shape as `GET /api/user/home`) | on connect, and whenever the app store changes |
| `node` | `{app, container, phase, error?}` | graph node actual-status change |
| `pull` | `{app, image, phase: "pulling" \| "done", detail?}` | pull progress, throttled |
| `activity` | `{time, event, detail}` | orchestrator activity ring buffer entries |

- `snapshot`-on-connect is the entire reconnect story (SSE auto-reconnects; no
  `Last-Event-ID` replay needed for v1).
- **Timeout middleware fix (pre-existing bug):** the global
  `middleware.Timeout(60 s)` on the main mux will kill *any* SSE stream after 60 s —
  including the existing logs stream. Move the timeout middleware off the stream
  routes: register the SSE routes (new + `/api/apps/{name}/logs`) on a sub-mux that
  has the auth middleware but not the timeout middleware.

### 3. Phase & progress model

Map graph `NodeStatus` → user-facing phase:

| Graph state | Phase shown |
| --- | --- |
| `INITIALIZING` | `queued` (planning / waiting on dependencies) |
| `PRESTART_CONFIG` | `configuring` |
| `STARTING` | `starting` (or `pulling` while a pull event is in flight) |
| `POSTSTART_CONFIG` | `finalizing` (SSO wiring etc.) |
| `RUNNING` | `running` |
| `ERROR` | `failed` (+ error text) |

New fields on `GET /api/user/home` app entries (and in `snapshot` events):

```jsonc
{
  "catalog_id": "jellyfin",
  "status": "installing",          // existing, unchanged
  "phase": "pulling",              // NEW: user-facing phase
  "phase_detail": "34% — 340 MB of 1.1 GB",  // NEW: pull progress or ""
  "components": [                  // NEW: only for multi-container apps or non-running
    { "name": "jellyfin", "phase": "pulling", "error": "" }
  ],
  "last_error": ""                 // NEW: persisted failure reason
}
```

DB: add `last_error TEXT` to `apps` (ad-hoc migration with a `PRAGMA table_info`
guard, matching existing practice — note: ad-hoc migrations are a known tech-debt
item). Only the orchestrator writes it (invariant #1): on `ERROR` it stores the
error; on a successful re-install it clears it.

### 4. Pull progress

`PullImage(ctx, image)` gains a progress-aware sibling (interface change in
`internal/container/runtime.go`, `internal/podman/client.go`):

- `PullImageWithProgress(ctx, image, onProgress func(PullProgress))`.
- Implementation: `POST /libpod/images/pull?reference=<ref>&decoding=true` against
  the podman socket (the client's existing `post`/socket plumbing). Parse streamed
  JSON events (`status`/`progress`/`id`/`from` lines); compute percent from
  `progressDetail` (current/total) when present; throttle `onProgress` to ~2/s.
- `PodmanRuntime.Ensure` passes a callback up to the orchestrator, which publishes
  `pull` events on the bus and updates `phase_detail` on the owning app.
- Image already local → podman returns an "exists"-style event immediately; emit
  `pull {phase: "done"}` and proceed (no fake progress).
- Fallback: if the socket API pull fails for an infra reason, retry via the existing
  exec `podman pull` path (no progress) so installs never regress to being blocked
  by the new path.

### 5. Latency fixes (server)

- **Record-at-enqueue, orchestrator-owned.** New `Orchestrator.Submit(intent)`
  method: for `InstallAppIntent`, synchronously `appStore.Install(...)` (upsert,
  `status='installing'`) *then* enqueue. The API handler still only calls
  `Submit` — the orchestrator remains the sole writer (invariant #1). The 202
  response then includes the app record (handler reads current state after submit,
  which invariant #1 explicitly allows), so the frontend applies it immediately —
  no synthetic state, no poll dependency.
- **Debounce = coalesce, not delay.** `WaitAndDrain` currently sleeps the full 5 s
  after the *first* intent. Change: the first intent wakes the loop immediately; the
  debounce window only applies to *additional* intents arriving during processing
  (batching bursts). Drop the default from 5 s to ~750 ms. This also speeds up the
  CLI and every other intent (rename, tailnet, …).
- Drain-phase `recordIntent` becomes an idempotent upsert (row already exists from
  Submit) — no behavior change for the reconciler.

## Frontend

### 6. Event client (primary source) + polling fallback

`src/lib/api/appEvents.ts` replaces the poller as the primary source:

- `EventSource('/api/apps/events')`:
  - `snapshot` → `apps.set(...)` + `gridElements.setFromHome(...)` (existing handlers).
  - `node` / `pull` → merge into a separate `appProgress` store keyed by catalog_id
    (phase, phase_detail, components) — keeps grid/layout state untouched.
  - `activity` → toast store + recent-activity store.
- **Fallback:** the existing adaptive poller stays, demoted to safety net. If SSE
  hasn't delivered an event for ~10 s, start polling at the active interval; a
  successful `snapshot` event suspends polling again. This covers the 60 s-timeout
  window during rollout, proxy weirdness, and browser tab sleep/wake.
- Install click flow: apply the app record from the 202 response immediately
  (optimistic fallback: if the response is slow, insert a synthetic `installing`
  entry — mirrors the existing uninstall optimistic pattern).
- Uninstall/rename keep their current behavior; the `snapshot` stream replaces the
  manual "next poll will fix it" assumption.

### 7. Tile & dashboard visuals

- **AppTile:**
  - Installing: phase label under the icon ("Pulling image · 42%") + thin progress
    bar when percent is known; spinner only when no phase info exists.
  - **Remove `pointer-events: none` / `disabled` while installing** — clicking opens
    the detail modal (investigation is the point).
  - Failed: red accent ring + "Failed" label, always clickable.
  - Stopped: dimmed. Running: normal. (Distinct treatment per state — see §8.)
- **AppDetailModal → live install view.** When `status` is installing/starting/failed:
  - Step timeline: Accepted → Planned → Pulling image (progress) → Configuring →
    Starting → Finalizing → Ready, with timestamps; current step highlighted.
    Data: `activity` events + per-container `node` phases.
  - On failure: `last_error` text, **View logs** (existing
    `/api/apps/{name}/logs` SSE), **Retry install** (POST install again).
  - Running apps: unchanged catalog info + open/uninstall as today.
- **Toasts:** app → running ("Jellyfin is ready") and → failed ("Jellyfin failed —
  view details"), derived from snapshot/node events.
- **"Needs attention" chip** in the sidebar: count of apps in `error`/`failed`
  (the client already tracks all app states).
- **Catalog card:** show estimated size ("~1.2 GB") so long pulls feel expected.
  Source: new optional `estimatedSizeMB` in `metadata.yaml` (catalog model), falling
  back to `podman image inspect` size when the image is already local.

### 8. Status vocabulary (documented, UI-enforced)

One-paragraph state machine for `specs/spec.md`:

- `installing` — work is in progress; sub-phases from §3 (`queued/pulling/
  configuring/starting/finalizing`).
- `running` — healthy and reachable.
- `stopped` — intentionally not running (managed, restartable).
- `failed` — install/reconcile gave up; `last_error` explains why; **Retry** available.
- `error` — degraded: running but failing its health check; auto-retry in progress.
- `uninstalling` — removal in progress.

(`error` vs `failed` becomes crisp: *degraded-but-recovering* vs *gave-up*.)

## Out of scope (deliberately)

- Layout/position sync — stays server-owned + polling (per `layout-refactor.md`).
- WebSockets — SSE is sufficient and simpler through any future proxying.
- Background pre-pull of catalog images — separate feature; builds naturally on the
  new pull-progress infra.
- Per-user event scoping — apps are host-global; one hub suffices for v1.
- Sharing/remote-app lifecycle — different state model, untouched.

## Invariants & risks

- **Invariant #1 (orchestrator sole writer):** record-at-enqueue happens inside
  `Orchestrator.Submit`; the SSE layer only reads. ✓
- **Invariant #7 (routes regenerated after convergence):** untouched. ✓
- SSE through Traefik: not an issue — the dashboard is served by host-agent itself
  on :3000 (loopback), not proxied. ✓
- **Backpressure:** buffered per-subscriber channels, drop-on-overflow + snapshot on
  reconnect; pull events throttled at the source.
- **Podman API pull:** depends on socket access the client already uses for container
  create; keep the exec-pull fallback so the new path can't block installs.
- **Timeout middleware change:** affects all routes — non-stream routes must keep
  timing out. ✅ Resolved in M1: scoping is explicit per group via `With()`; the
  only routes without a timeout are the three SSE streams.
- `middleware.Timeout` currently breaks the existing logs SSE at 60 s — fixing it
  changes live behavior (streams now last); confirm no client relies on the 60 s
  cutoff (none known). ✅ Fixed in M1; no client relied on the cutoff.

## Milestones

| # | Scope | Est. | Status |
| --- | --- | --- | --- |
| M1 | Event bus + SSE endpoint + snapshot + `last_error` column + timeout-middleware fix | 1–2 d | ✅ done — commit `1d44be0` (2026-08-20) |
| M2 | Pull progress (API pull + runtime callback + `pull` events) | 1 d | ✅ done — 2026-08-22 |
| M3 | `Submit` record-at-enqueue + state in 202 + debounce coalescing | 0.5 d | ✅ done — 2026-08-22 |
| M4 | Frontend: event client + fallback, tile phase/progress/error visuals, live install timeline modal, toasts, attention chip | 2 d | ✅ done — 2026-08-22 |
| M5 | Catalog size estimates, status-vocab doc in spec.md, e2e coverage, polish | 1 d | pending |

## M1 implementation notes (done 2026-08-20, commit `1d44be0`)

Delivered as planned, with these implementation details/deviations:

- **New files:** `internal/eventbus/{eventbus,eventbus_test}.go`,
  `internal/api/events_module.go` (+ test), `internal/orchestrator/events_test.go`.
  Schema: `last_error TEXT NOT NULL DEFAULT ''` in `schema.sql` + `v7` migration in
  `db.runMigrations` (fire-and-forget `ALTER TABLE`, matching v1–v6 practice).
- **Timeout fix structure:** chi v5.2.3 `Use()` panics once any route is registered
  on the same mux, so "register streams before the timeout" is impossible on one
  mux. Instead the global `r.Use(Timeout)` was removed and non-streaming route
  groups opt in explicitly via `r.With(requestTimeout)` / `api.With(requestTimeout,
  authMiddleware)` (inline muxes sharing the parent tree — verified against chi
  source). SSE routes sit on `api.With(authMiddleware)` only: authenticated, no
  timeout. Top-level public routes and the frontend catch-all also opt in.
- **Bus overflow semantics (refined):** when a subscriber's buffer is full the
  event is dropped and a `resyncPending` flag is set; the *next* publish delivers
  a forced `apps-changed` (snapshot) first. (The original "force resync in the
  same publish" can itself be dropped when the buffer is still full.)
- **SSE handler:** 30 s `: ping` heartbeat comment; `X-Accel-Buffering: no`; write
  errors end the stream (client is gone) — EventSource reconnects and re-snapshots.
- **`last_error` writes** live inside the existing `setupStatusSync` graph handler
  (single-container and multi-container paths both), so the single-writer invariant
  holds: the orchestrator is the only author. Reinstall (`Install` upsert) clears
  the column too.
- **`pull` events:** type + payload defined on the bus and the SSE handler relays
  them, but nothing publishes `pull` yet — that's M2.
- **Tests added:** eventbus fan-out / overflow-resync / slow-subscriber non-blocking
  / unsubscribe; store `last_error` round-trip; orchestrator node-error →
  `last_error` + node event, running → clear, activity publish, no-bus no-panic,
  `phaseForStatus`; SSE handler contract (snapshot, activity, node, resnapshot on
  apps-changed); router-level snapshot + resync + 401-for-non-local.
  Full pre-commit suite green (530 host-agent + 29 apps + 11 web tests, `-race` clean).

## Testing

- **Unit (Go):** eventbus fan-out/overflow-drop ✅ (M1); phase mapping ✅ (M1);
  podman pull event parsing (fake streamed JSON incl. "already exists") (M2);
  `Submit` upsert idempotency (M3); `WaitAndDrain` first-intent-immediate +
  coalescing (M3).
- **Unit (web, vitest):** event merging into stores; SSE→polling fallback switching;
  timeline step derivation.
- **Integration (Go, `internal/e2e`, build tag `integration`):** install Jellyfin via
  the real API; assert the SSE stream delivers `snapshot → node/pull → running` and
  that the home response carries `phase`/`components`; assert `last_error` on a
  forced failure (e.g., temporarily invalid image ref in a test-only app).
- **Playwright (`e2e/`):** extend `jellyfin.spec.ts` or add `install.spec.ts` — click
  install, assert tile appears **< 2 s** with a phase label; assert timeline modal
  shows ordered steps; assert failure path shows error + Retry.
- **Pre-commit:** fast tier unchanged (unit + vitest + svelte-check); integration
  tier covers the full path.

## M4 implementation notes (done 2026-08-22)

Delivered as planned, with these implementation details/deviations:

- **New web files:** `src/lib/api/appEvents.ts` (SSE client + watchdog),
  `src/lib/stores/appProgress.ts` (progress store + pure merge fns +
  `recentActivity`), `src/lib/stores/toasts.ts`,
  `src/lib/services/installTimeline.ts` (pure timeline derivation),
  `src/lib/components/Toasts.svelte`,
  `src/lib/components/AppInstallModal.svelte`, plus 4 vitest files (31 new
  tests).
- **Event client as primary source:** `EventSource('/api/apps/events')` —
  `snapshot` → apps + grid stores + progress reconciliation + transition
  toasts; `node`/`pull` → `appProgress` store (kept separate from grid state
  so pull ticks don't rewrite layout); `activity` → bounded
  `recentActivity` ring. The existing adaptive poller is demoted to a
  safety net: a 5 s watchdog enables it when no event (SSE or poll) arrived
  for 10 s; any live event suspends it. The decision is a pure, tested
  function (`shouldEnableFallback`). Snapshot-on-connect remains the whole
  reconnect story.
- **Install click flow:** the M3 202 `app` record is applied to the apps
  store + grid (auto-positioned element) immediately by the facade;
  optimistic synthetic `installing` entry as fallback if the record is
  missing. The tile now appears on click, not on the next poll.
- **AppTile:** no longer disabled while installing (`pointer-events: none`
  removed) — clicking an installing/starting/failed/error tile opens the
  live install modal; running tiles still open the app. Phase label under
  the icon ("Pulling image · 42%" / "Configuring" / …); thin progress bar
  only while pulling with a known percent; spinner only for the brief window
  before the first phase event. `failed` = red ring + Failed label, `error`
  = amber ring + Degraded, `stopped` = dimmed.
- **AppInstallModal (new):** step timeline Accepted → Planned → Pulling
  image → Configuring → Starting → Finalizing → Ready derived purely from
  (status, progress) — `deriveTimeline` is unit tested. Timestamps come from
  a bounded per-app phase history. On failure: `last_error` box (falling
  back to the live node error), **View logs** (opens the existing SSE logs
  stream in a tab) and **Retry install** (re-POSTs install; the M3 upsert
  clears `last_error`). Running apps keep their existing click behavior.
- **Multi-container app phase semantics:** the app phase is the *least*
  advanced component phase (failed dominates) — it represents the remaining
  work, so a container still starting keeps the app out of "Ready" even if
  another container already reached running. (An early max-rank draft was
  wrong: it showed "Ready" mid-install.)
- **Toasts:** pure `detectToasts(prev, next)` on each snapshot — "X is
  ready" (success) on the transition into running, "X failed — view
  details" (error) on entering failed; deliberately silent for `error`
  (degraded, auto-retrying) and for apps already in that state. 5 s TTL.
- **Sidebar attention chip:** failed+error count on the Apps nav item with
  a tooltip naming the affected apps; collapses to icon-only when the
  sidebar is collapsed.
- **Backend addition:** `eventbus.PullInfo` gained a first-class `Percent`
  field (populated in `setupPullEvents`) so the frontend drives the progress
  bar without parsing the rendered detail string.
- **Verification:** 42 vitest tests green (31 new), svelte-check 0 errors,
  full pre-commit suite green.

## M3 implementation notes (done 2026-08-22)

Delivered as planned, with these implementation details/deviations:

- **`Orchestrator.Submit(intent)`** is the new handler-facing entry point:
  for `InstallAppIntent` it synchronously upserts the app row
  (`status='installing'`, `last_error` cleared) from the catalog, then
  `Enqueue`s. Handlers never write stores (invariant #1 intact). The
  `orchestratorCaller` interface in the API package was renamed
  `Enqueue` → `Submit`, so all modules (apps, remote-apps, settings) route
  through it; non-install intents are recorded exactly as before (no store
  write).
- **202 install response** now carries the app record:
  `{"intentId": …, "app": {…}}` where `app` has the same shape as an
  `/api/apps/installed` entry (read back from the store after submit; the
  handler read is allowed by invariant #1). `intentId` is unchanged for
  backward compatibility; `app` is omitted if the store write failed (the
  M4 frontend falls back to its optimistic entry).
- **`WaitAndDrain` semantics (refined):** "first intent immediate" applies
  to the queue-*empty-on-entry* path — the waiter blocks, the first intent
  wakes it, and it drains with zero debounce delay. Intents already in the
  queue when `WaitAndDrain` is called (i.e. they arrived while the
  orchestrator was processing a previous batch) wait out the coalescing
  window, which resets on each new arrival, so bursts collapse into one
  convergence pass. `DefaultDebounce` dropped 5 s → 750 ms.
- **`recordIntent` unchanged** — `appStore.Install` was already an idempotent
  upsert (`ON CONFLICT DO UPDATE`, re-sets `status='installing'`, clears
  `last_error`), so the drain pass re-upserting over the Submit-written row
  is a no-op behaviorally; the reconciler is untouched.
- **Tests added:** `Submit` — row exists before the intent is queued,
  reinstall idempotent (single row, `last_error` cleared), non-install
  intents don't write, missing-catalog app still enqueues; `WaitAndDrain` —
  lone-intent-immediate (vs a 10 s window), pre-queued coalescing wait,
  reset-on-arrival burst (7 intents, window honored after final arrival),
  ctx-cancel on empty queue (nil) and mid-coalesce (accumulated batch);
  API — 202 body shape with/without the app record (fake orchestrator now
  emulates record-at-enqueue when wired to a store). Full pre-commit suite
  green, `-race` clean.

## M2 implementation notes (done 2026-08-22)

Delivered as planned, with these implementation details/deviations:

- **New file:** `internal/podman/pull.go` (+ `pull_test.go`); `client.go` only
  lost the exec body of `PullImage` into a shared `runExecPull` helper.
- **Timeout trap (found while implementing):** the shared `podman.Client`
  `http.Client` has `Timeout: 30 s`, which is a *whole-request* timeout — it
  would have killed every streaming pull over 30 s. `pullViaSocket` uses a
  dedicated per-call client with no timeout; only ctx bounds the stream.
- **`Runtime` interface unchanged** (plan §4 anticipated an interface change;
  avoided it): `PodmanRuntime` implements an optional capability interface
  `container.PullProgressReporter { SetPullProgressReporter(fn) }`, which the
  orchestrator detects via type assertion in `NewOrchestrator`/`setupPullEvents`.
  Test doubles and any other runtime need no changes. The callback carries the
  container name, so the orchestrator attributes the pull to the owning app via
  the existing `containerOwner` map (all current apps use `containers:` lists).
- **Throttling semantics:** coalescing window of 500 ms (≤ 2/s) — updates inside
  the window are held and the *last* one is flushed at stream end, so the final
  percent is never lost. A `done` event always terminates the stream.
- **Percent aggregation is cross-blob:** registries report progress per blob
  (each with its own total); `pullTracker` sums completed blob sizes + the
  in-flight blob's progress over all known blob sizes, so the percentage is
  monotonic and reaches 100% only when all known blobs are done. A blob that
  hasn't been announced yet isn't in the total (early percent is over the
  known subset — acceptable for v1).
- **Fallback rules (refined from plan §4):** the exec `podman pull` retry
  happens only for *infrastructure* failures (socket dial error, non-200 HTTP).
  A `error` event inside the stream (bad reference, registry error) is
  definitive and returned as-is — retrying would hit the same error; a
  cancelled context surfaces without retry. Without a registered reporter,
  `Ensure` keeps the original plain exec pull path (zero behavior change).
- **"Already exists"** (status-only stream, no blob sizes) emits exactly one
  `done` event with the status as detail — no fake progress (per plan §4).
- **Detail rendering** happens in the orchestrator (`pullDetail` +
  `humanBytes`), keeping the podman package UI-agnostic: `"34% — 340.0 MiB of
  1.0 GiB"`, falling back to the raw status line when no sizes are known.
- **Tests added:** podman stream parsing (percent + monotonicity + done),
  already-exists, definitive stream error (no CLI retry), 500 → exec fallback
  (final `done`), cancelled ctx (no fallback), unsafe reference rejection,
  blob aggregation unit test, throttler coalescing; container Ensure →
  reporter (with/without); orchestrator pull → bus with owning-app
  attribution (multi-container + unknown-container fallback), `pullDetail`,
  `humanBytes`; api SSE `pull` relay in the existing stream contract test.
  Full pre-commit suite green, `-race` clean.
