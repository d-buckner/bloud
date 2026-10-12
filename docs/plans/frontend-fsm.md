# Plan: One frontend store, modeled as state machines

> Status: in progress

Closes [#309](https://github.com/d-buckner/bloud/issues/309).

## What has landed

Phases 0 to 2, plus the developer graph out of phase 3, are in `src/lib/store/`,
and the old modules they replace are deleted, not deprecated:

- **Phase 0.** `kernel.ts` (the `globalThis` singleton, `replaceReducer`, the dev
  `resync`, devtools), `actions.ts` (the action union), `bindings.svelte.ts` (the
  `select()` binding), `runner.ts` and `reducers/commands.ts` (the command layer
  with the three settle kinds), and `middleware.ts`.
- **Phase 1.** The `session`, `connection`, `apps`, `layout`, and `launchers`
  slices, and `effects/{sse,watchdog,poller,connection}.ts`. Gone: `appEvents.ts`,
  `appFacade.ts`, `poller.ts`, `layoutClient.ts`, and all six `stores/*.ts`.
  There are zero `from 'svelte/store'` imports left in the tree, and exactly one
  `new EventSource`.
- **Phase 2.** `reducers/progress.ts` is the lifecycle mirror, and toasts are a
  middleware over snapshot transitions rather than a module with its own memory.
  `lastStatuses` is gone: what an app's status *was* is state now.

Not landed, and named so the next reader does not assume otherwise:

- **Generated API types** (phase 1's `check:api-types`). `wire.ts` and `types.ts`
  are still hand-typed against the Go structs. This is the largest remaining
  piece of phase 1.
- **The action layer is pre-RTK, not slice-generated.** The store is
  slice-shaped in the way vanilla Redux has always prescribed, eight reducers
  under `combineReducers`. What is hand-written is the action side: type
  constants in `actionTypes.ts` and creators in `actions.ts`, where
  `createSlice` would generate both together. That is what makes
  `KNOWN_ACTION_TYPES` necessary at all: it is a guard for a hole the hand-written
  layer opens, not something a generated action layer would need.
- **The developer graph is store state.** `reducers/graph.ts` holds the payload,
  whether a read has landed, and what failed; `effects/graphPoll.ts` owns the
  500ms cadence, gated on the route being mounted and the tab visible, with
  `setTimeout` rather than `setInterval` so a slow response spaces the next read
  instead of stacking it. The page keeps only the xyflow projection, and keeps it
  on purpose: `layoutGraph` imports dagre, and a reducer that called it would
  move a layout library out of the developer route's chunk and into the store's,
  so every visitor on the home page would download a graph layouter they will
  never see. The slice adds the thing the component could not express: a failed
  poll keeps the last good payload and sets `stale`, so the page can say the
  picture has stopped moving instead of silently freezing it.
- **Phases 3 to 5.** The widgets still own their timers, and settings still own
  their convergence loops. Each is a row in the `EXCEPTIONS` table of
  `scripts/no-component-timers.mjs`.
- **`check:no-legacy-store`** is in, with an empty exception table: nothing in the
  tree imports `svelte/store`, and the check is what keeps it that way.
- **`check:no-component-fetch` and `check:bundle-budget`.** The fetch check needs
  a five-entry exception table before it can start (`SetupWizard`,
  `AppDetailModal`, `ClientAccessPanel`, and the `Storage` and `SystemStats`
  widgets still call `fetch` directly), so it lands with phase 4, when those
  entries have a destination.

## Goal

Put every piece of frontend state that is not a component's own transient
pixels into one store, model the three things that are genuinely state machines
as machines, and make a hot reload swap code without throwing away what the
dashboard knows.

Three acceptance questions, all answerable by reading the tree at
the end:

1. Is there exactly one place that holds app catalog data, install progress,
   graph state, and in-flight commands? Today there are six store modules, three
   polling mechanisms, and six components that call `fetch` themselves.
2. Do the components stop deciding what the data means? A page should read a slice
   and dispatch a command. Today `routes/+page.svelte` holds 16 `$state`, the
   developer page owns a polling loop, and the status sets that decide what a
   click means live in components.
3. Does editing a reducer in `./bloud dev` keep the grid, the open install
   progress, and the SSE stream alive? Today it cannot: nothing in the web tree
   contains `import.meta.hot`, so a store-module edit falls back to a full page
   reload.

The issue asks for vanilla redux. That choice survives the whole design: the
kernel is the `redux` package, and everything above it is ours.

## What is scattered today

Verified against the tree at the time of writing. Paths are under
`services/host-agent/web/` unless noted.

| Kind | Where | What is wrong |
|---|---|---|
| Svelte stores | `src/lib/stores/apps.ts`, `grid.ts`, `launchers.ts`, `appProgress.ts`, `toasts.ts`, `user.ts` | Six stores, no root state, no single subscribe point, no request phase (a `loading` and an `error` boolean where a phase belongs) |
| Orchestration state in module closures | `src/lib/clients/appEvents.ts`: `startedAt`, `lastEventAt`, `fallbackActive`, `started`, `watchdog`, `lastStatuses`; `appFacade.ts`: `initialized`; `poller.ts`: `pollerTimeout`, `activeCallback` | The connection FSM already exists and is implicit. Only `shouldEnableFallback` is exported and testable; the rest is invisible to a test |
| Per-page fetch islands | `src/routes/catalog/+page.svelte` (catalog list, own loading/error), `src/routes/developer/+page.svelte` (own 500 ms interval, `inflight`, `payload`), `src/routes/+layout.svelte` (raw `fetch` of `/api/setup/status` and `/api/auth/me`), `src/routes/+page.svelte` (`externalRecords` plus a hand-rolled in-flight promise coalescer) | The same data is re-read per route, nothing is shared, nothing is cancellable |
| Widgets polling on their own | `src/lib/widgets/SystemStats.svelte` (10 s), `Storage.svelte`, `QuickNotes.svelte` | Three more timers, each with its own error flag |
| Raw `fetch` in components | 6 files: `+layout.svelte`, `SetupWizard.svelte`, `ClientAccessPanel.svelte`, `AppDetailModal.svelte`, `widgets/SystemStats.svelte`, `widgets/Storage.svelte` | Bypasses the error contract in `clients/httpClient.ts` |
| Optimistic updates | `appFacade.uninstallApp` patches status then hand-rolls a revert in `catch` | Rollback is duplicated per command instead of modeled once |
| Writes that report the wrong completion | `AISettingsSection.handleSave` awaits a 202 from an intent endpoint and reports success; `AddressSettingsSection.handleSaveAddress` hand-rolls a 500 ms convergence poll with a 20 s deadline; `UsersSettingsSection` refetches after each of its three mutations | Three components each invent their own answer to "when is this write actually done" |
| Transport | `internal/api/events_module.go` (SSE: snapshot, node, pull, activity) plus `internal/api/system_module.go:154` (`/api/system/status/stream`, a 500 ms SSE the dashboard never opens) plus three client pollers | Live data arrives by four mechanisms, and none of them is owned by the store |

The cost is not tidiness. Three concrete failures follow from this shape.

**Toast derivation depends on memory no one can see.** `lastStatuses` in
`appEvents.ts` is a module-level `Map` that `stopAppEvents` resets, so the
"X is ready" transition table in `stores/toasts.ts:detectToasts` is fed by state
that is not in any store and not restorable.

**Progress entries have no identity.** `mergeSnapshot` in `appProgress.ts`
decides an entry is settled by comparing a status against a phase
(`status === 'running' && entry.phase === 'running'`). There is no episode to
key on, so a `pull` event for an app that never reports a node phase leaves an
entry in the map until the app is uninstalled.

**The developer page cannot share anything with the home page.** It re-polls
`/api/system/developer` at 500 ms with its own loop while the home page holds an
open stream that already carries the same node transitions. The polling is not the
flaw; owning it in the page is.

## The model

Three machines, and everything else is a projection. One slice turns out not to
be: settings is a set of writable resources, and the section on it says why that
difference matters.

The distinction that keeps this honest: a machine is something the client
*drives*. The client drives its own connection, its own commands, and its own
reading of what it is watching. It does not drive an app's lifecycle. Backend
invariant 1 (the orchestrator is the single writer, `AGENTS.md`) has a frontend
equivalent, and it is the reason the mirror below needs explicit reconciliation
rules rather than being a copy of the engine.

### Machine 1: session and connection

The client's own lifecycle. This subsumes every closure variable listed above.

```
bootstrapping ──setup needed──▶ awaiting_setup
      │
      └──auth ok──▶ connecting ──sse open + first snapshot──▶ streaming
                        ▲                                        │
                        │                                        │ no event for
                        │                                        │ FALLBACK_DELAY_MS
                        │                                        ▼
                     offline ◀──N poll failures── polling ◀──(watchdog)
                        ▲                          ▲
                        └──── 401 / signed out ────┘
```

| Current state | Event | Next state |
|---|---|---|
| `bootstrapping` | `setup.required` | `awaiting_setup` |
| `bootstrapping` | `auth.ok` | `connecting` |
| `bootstrapping` | `auth.unreachable` | `bootstrapping` (retry, bounded) |
| `connecting` | `sse.snapshot` | `streaming` |
| `streaming` | `sse.message` | `streaming` (records `lastEventAt`) |
| `streaming` | `watchdog.tick` with `now - max(lastEventAt, startedAt) >= FALLBACK_DELAY_MS` | `polling` |
| `polling` | `poll.ok` | `polling` (records `lastSnapshotAt`) |
| `polling` | `sse.message` | `streaming` |
| `polling` | `poll.fail` past the ceiling | `offline` |
| any | `http.401` | `signed_out` |
| any | `visibility.hidden` | same state, timers paused |
| any | `visibility.visible` | same state, immediate probe |

`shouldEnableFallback` (`appEvents.ts`) becomes one row of this table rather
than a lone export. The transition function is pure: `nextConnectionPhase(state,
event)`, with a table-driven test that walks every pair.

Two behaviors the current code does not have and the machine makes cheap:
`visibilitychange` (a closed lid leaves an `EventSource` that looks open and is
dead, and today nothing notices until the watchdog), and a bounded retry out of
`bootstrapping` (today a failed probe falls into the `catch` in
`+layout.svelte` and silently proceeds).

### Machine 2: commands

One entry per in-flight user action, keyed by a command key
(`install:jellyfin`, `uninstall:immich`, `layout:save`, `settings:public-url`).

```
idle ──dispatch──▶ pending ──ok──▶ settled ──▶ (dropped)
                     │
                     └──err──▶ failed ──▶ (dropped, optimistic patch inverted)
```

`commands.byId[key] = { key, kind, target, phase, startedAt, error?, inverted? }`
where `inverted?` is the recorded undo patch for whatever optimistic write the
command made.

Rules, each replacing something hand-rolled today:

- **Dedupe by key.** A second `install:jellyfin` while one is pending is a no-op
  that returns the existing key. Replaces `retrying` in
  `AppInstallModal.svelte`, `inflight` in `developer/+page.svelte`, and
  `externalRecordsInFlight` in `routes/+page.svelte`.
- **Optimistic state is an overlay, never a write.** A command declares the patch
  it wants to show; the patch goes in `commands.optimistic` and the selector
  applies it over the confirmed state. Nothing is overwritten, so nothing needs a
  revert: a failed command drops its overlay entry and the old state was never
  gone. This replaces the manual revert in `appFacade.uninstallApp`, and it is
  the same mechanism the grid needs (see "Optimistic state is an overlay, not a
  write").
- **Every command is cancellable by an effect teardown.** Route-scoped reads
  cancel with the route; HMR dispose cancels everything in flight.
- **A command declares how it settles.** Three settle kinds, and the third is
  the one the current code cannot express:
  - `request`: the HTTP response is the whole story (rename, user role change).
  - `reread`: the response means "stored", and settling means re-reading the
    resource. `UsersSettingsSection` hand-rolls this as an explicit
    `await loadUsers()` after each of its three mutations.
  - `convergence`: the response is an acknowledgement carrying an `intentId`,
    and the command is not done until the orchestrator agrees. The address save
    is exactly this, hand-rolled as a 500 ms poll with a 20 s deadline inside
    `AddressSettingsSection.handleSaveAddress`. Install is this too, and the
    install modal infers it from progress events instead of from a declared
    predicate.

  The predicate never lives in state: a function in a reducer's input breaks
  devtools and makes a reducer swap surprising. A command module registers
  `settlesBy(kind, predicate, deadline)` and an effect evaluates it against each
  new state. The store holds only the facts: `deadline`, `settledBy`, and the
  observed value that ended the wait.

**One settings write currently reports the wrong completion.** `setAISettings`
returns an `IntentResponse` (`clients/settingsClient.ts:69`), so the save is an
intent submission, and `AISettingsSection.handleSave` awaits the 202, reloads,
and reports success. The reconciler may not have applied anything yet. That is
not a bug in the component so much as the only shape the component could reach
for: there is no way to say "pending until the system agrees", so the code says
"pending until the socket answers".

### Machine 3: the lifecycle mirror

The client keeps its own per-node phase machine, in the engine's vocabulary,
advanced by SSE `node` and `pull` facts and fenced by snapshots. This is a
deliberate choice over treating progress as a bag of fields, and it needs rules,
because a mirror that can drift is worse than a poll.

The vocabulary is already shared. The engine's node statuses are
`INITIALIZING`, `PRESTART_CONFIG`, `STARTING`, `POSTSTART_CONFIG`, `RUNNING`,
`ERROR` (`internal/engine/graph/graph.go:18`), and `phaseForStatus`
(`internal/engine/orchestrator/status.go:126`) maps them to the wire strings
`queued`, `configuring`, `starting`, `finalizing`, `running`, `failed`. The
client's `PHASE_ORDER` (`stores/appProgress.ts`) is that same list plus
`pulling`, which is the one phase the engine never emits: it comes from
`TypePull` events, and it belongs between `queued` and `configuring`.

Six rules, because a mirror without a reconciliation contract is just a second
source of truth that happens to be stale.

**M1. Episodes, not comparisons.** A mirror instance is keyed
`(catalogId, generation)`. The generation increments when one of our commands is
accepted, or when a snapshot first reports a non-running status with no episode
open. The episode closes on a terminal phase confirmed by a snapshot, or when the
app disappears from a snapshot. This is what `mergeSnapshot` is approximating
with a status/phase comparison.

**M2. Monotonic inside an episode.** A phase never moves backwards on its own.
`phaseRank` already encodes the order with `failed` above everything; keep it.

**M3. A server correction is an explicit rule with a fixture, not an accident of
code order.** When a snapshot reports an earlier phase than the node events imply,
the snapshot wins for the app-level phase, and the reducer records `corrected`
so dev can name it and a test can pin it. The rule has to exist in either design;
the only question is whether it is a named branch with a fixture or an emergent
property of which `if` runs first.

**M4. Snapshots fence facts, the mirror may lead on detail.** `apps.byId[x].status`
is a store read and always equals the last snapshot. The mirror may be ahead in
detail only: per-container phase, pull percent, per-container error. The split
already exists in the UI (`AppTile` shows phase from progress, `routes/+page.svelte`
decides click behavior from `app.status`); the rule makes it a contract.

**M5. Aggregation stays one pure function, and it must agree with the server.**
`aggregatePhase` in `appProgress.ts` (failed dominates, otherwise the
least-advanced container wins) is kept verbatim. The engine aggregates
independently in `setupStatusSync` (`status.go:23`: error fires immediately,
running only when every container runs). Those two rules agree, and a
table-driven test asserts they do, so a change on one side fails loudly instead
of producing a tile that disagrees with the status it reports.

**M6. No phase for a node with no event.** `emptyProgress` starts at
`phase: null`, and the graph legend already renders "not reconciled" as a hollow
dot (`routes/developer/+page.svelte`, `src/lib/graph/phase.ts`). The mirror does
not guess `queued` for a node the engine has never reported.

Plus a ceiling the current code lacks: an episode with no event for N minutes
closes as `stale` rather than living until the app is uninstalled.

### Projections

Everything else is a slice with an explicit request phase
(`idle | loading | stale | ready | error`) instead of a `loading`/`error` boolean
pair: `apps`, `layout`, `launchers`, `catalog`, `graph`, `external`, `settings`,
`toasts`, `ui`.

`toasts` keeps `detectToasts` as a pure function, but its input
(previous statuses) moves into the `apps` slice, so a toast is derived from
state that is in the store.

One rule applies to every slice: **no slice stores a `saving` boolean.** Whether
something is in flight is a fact about the `commands` slice, derived from the
commands whose target is that slice. That is what retires the `loading`,
`saving`, `submitting`, and per-action boolean sets in the three settings
sections (13, 9, and 6 `$state` declarations).

## Catalog, graph, and settings are three different kinds of thing

All three are in the store, and none of them is the same kind of slice. Treating
them as one generic `Resource` is how the settings bug above got written, so
each gets its own modeling.

### catalog: a read-only projection whose problem is joins

`GET /api/apps` changes only when the catalog is refreshed
(`POST /api/apps/refresh-catalog`, admin-only, and nothing in the dashboard
calls it today). So its request phase is `ready` almost always, and the
interesting part is not its lifecycle but its joins:

- **With `apps`.** A catalog card shows installed status next to catalog data,
  and today the join happens in the page: `catalog/+page.svelte:getAppStatus`
  scans `$installedApps` per card. The rule: a slice never imports another
  slice's store. Joins are selector functions that take `RootState`, which
  keeps them testable without a store and stops a component from becoming a
  fourth source of truth.
- **With per-app sub-resources.** `/api/apps/{name}/metadata` and the two plan
  endpoints `/api/apps/{name}/install-plan` and `/remove-plan`
  (`internal/api/apps_module.go:431`) are unread by the dashboard today. A plan
  belongs keyed under the catalog entry, so the install-experience plan lands
  as catalog data rather than as a new island.
- **Staleness is honest, not derived.** Nothing in the snapshot carries a catalog
  version, so `catalog.fetchedAt` plus an explicit refresh is the whole model.
  `stale` should mean "older than this session", not "wrong".

### graph: one projection, three change rates

The slice is split by how often each part changes: `topology` (nodes and edges,
from the catalog, stores, providers, and the address), `states` (per-node
`NodeState`), and `orchestrator` (queue depth, converging, activity ring). Three
rates means three reducers, and that is not a style preference here: the poll
returns all three at once every 500 ms, so a tick that only changed node phases
must not rebuild topology or re-run the layout, because
`timeline/graphSync.ts` preserves node identity precisely so xyflow does not
re-measure the canvas. So the topology reducer is the only place node objects
are replaced, and the states reducer never touches them.

Lifetime is route-scoped; ownership is not. The graph slice stays in the store
after the developer route unmounts, so a return visit renders instantly and a hot
reload keeps it, while only its effect stops.

### settings: not a projection at all

Settings is three resources with three different write semantics, which is why
one generic `Resource` would have hidden the bug:

| Resource | Read | Write | Settle |
|---|---|---|---|
| Address | `GET/PUT /api/settings/public-url` | intent | `convergence`: the live URL equals the canonical origin the PUT returned |
| AI | `GET/PUT /api/settings/ai` | intent (`IntentResponse`) | `convergence`, not implemented today |
| Users | `GET /api/admin/users` plus POST/PATCH/DELETE | plain | `reread` |

Two more things belong to this slice rather than to the components:

- **The admin gate is state, not a lifecycle hook.** All three are admin-only in
  the API (`internal/api/router.go:432`, the `admin` group). Today
  `settings/+page.svelte` redirects from `onMount` on a store read, so a member
  renders the page shell first. As a machine, `session.user.role` decides
  whether the settings effects ever start.
- **Patch semantics are data.** The AI resource never echoes its secret (`hasApiKey`
  only), and the rule "omitted means keep what is on file, blank means wipe it"
  already exists for external records in `utils/externalAppPatch.ts`, unit-tested
  for exactly that reason. The same rule for AI settings belongs beside it, not
  in a form component.

## The kernel: reducers, a snapshot fence, and a swap point

Plain redux: `combineReducers` over slices, actions as the only way in, and one
rule that keeps the whole thing honest.

**The snapshot is the fence.** Every server-derived slice is *replaced*, not
merged, by the next snapshot; between snapshots, events advance it. That is
already the shape of the transport (`events_module.go:70` sends a full snapshot
on connect and after any app-store change), and it is what makes a lost event
recoverable instead of catastrophic. Most of what an event log is for, a periodic
authoritative re-read already gives you.

### Why not an append-only log with state derived from it

It was in this plan until it was argued out of it. The reasons are worth keeping
in the document, because the idea is attractive and will come back.

- **The engine is not a log.** The graph holds current node status and diffs it
  against intent every pass. "The client projects the reconciler" therefore
  points at current state plus a periodic full re-read, which is exactly what
  reducers plus a snapshot fence produce. The log borrowed a metaphor the server
  does not use.
- **Bounded windows break the claim that justified it.** Replay is exact only if
  the log is complete, and the design had per-topic windows, so a replay after a
  code swap would rebuild from a partial history and produce a different state.
  The property being sold was not actually on offer.
- **Redux devtools already is an action log**, with time travel, free and
  dev-only. Rebuilding one inside the store spends production bytes to duplicate a
  debugging tool.
- **Every bug becomes "was it the derivation or the log?"** Two representations of
  every fact, kept in sync by the one function that must be total, and a
  derivation that throws takes the dashboard down where a reducer that misses a
  case corrupts one slice.
- **It raises the contributor bar for no user-visible gain.** Writing a reducer is
  a skill every web contributor has; topics, windows, and per-topic replay rules
  are concepts this dashboard has no problem large enough to require.

What the log was reaching for is still in the plan, just not as architecture:
actions are the unit of testing (fixture sequences, below), the optimistic layer
is a separate slice that selectors apply over confirmed state rather than a write
into it, and a dev-only `resync()` re-dispatches the last snapshot so an edited
reducer can be seen against live state without a page reload. It is a function
that dispatches `home/snapshot`, not an action type of its own: re-applying the
snapshot already re-derives every server slice, so a `dev/resync` action would
appear in the devtools log and change nothing. `__tests__/actions.test.ts` fails
an action type no reducer implements, which is what caught that.

```
src/lib/store/
  kernel.ts          redux store, singleton, devtools hook, replaceReducer wiring
  actions.ts         action creators and the action-type unions, per slice
  bindings.svelte.ts select(): the only redux-to-runes seam
  commands.ts        command layer: dedupe, settle kinds, optimistic overlay
  slices/            session, connection, apps, layout, launchers, progress,
                   catalog, graph, external, settings, commands, toasts, ui
  effects/           sse, watchdog, poller, graphPoll, widgetPoll
  hmr.ts             import.meta.hot accept/dispose wiring
```

The kernel is `redux` (`createStore`, `combineReducers`, `replaceReducer`,
`compose`): no toolkit, no saga, no `redux-thunk`, because the command layer
needs the store object anyway and covers what middleware would. Phase 0 records
the measured bundle delta rather than predicting it; the current build is 588 KB
of JS total, developer route 226 KB, home route 139 KB, so there is a real line
to hold. Devtools is three lines against `window.__REDUX_DEVTOOLS_EXTENSION__`,
dev-only, no dependency.

## The binding: runes, not legacy stores

The first draft of this plan wrapped redux in `svelte/store` `readable` objects
so components could keep writing `$apps`. That is a shim with no deletion
criterion, and a shim that never gets deleted is worse than no shim: every new
component gets written in the old idiom because the old idiom still works. The
end state is runes-native, and it is also less code.

```ts
// store/bindings.svelte.ts
let version = $state(0);
store.subscribe(() => { version++; });

export function select<T>(sel: (s: RootState) => T): () => T {
  return () => { version; return sel(store.getState()); };
}
```

A version counter bumped on notify, and a getter Svelte 5 tracks. Components
write `const apps = select(s => s.apps.byId)` and call `apps()`. Consequences
worth naming:

- Any dispatch invalidates every selector. At this scale that is free, and it is
  what `deepSubscriptions: false` does today anyway. If profiling ever says
  otherwise, per-selector memoization is a local change inside `select`.
- No `$store` syntax survives, so `get()` disappears from command code in favour
  of `store.getState()`, and the `derived` stores (`visibleApps`,
  `enabledWidgetIds`, `isAdmin`) become plain selector functions.
- The guardrail is a grep: `from 'svelte/store'` may appear in
  `bindings.svelte.ts` and nowhere else. That is checkable in the fast tier, and
  it is what stops the old idiom coming back.

## Effects: one owner per timer

Every effect is a store subscription that owns exactly one timer or socket,
starts and stops on a slice transition, and only ever dispatches.

| Read | Today | Becomes | Runs while |
|---|---|---|---|
| `GET /api/apps/events` (SSE) | `appEvents.ts`, opened by `appFacade`, timers in module closures | `effects/sse.ts`, `effects/watchdog.ts` | `connection.phase` is `connecting`, `streaming`, or `polling` |
| `GET /api/user/home` (poll fallback) | `poller.ts`, callback handed in by `appEvents` | `effects/poller.ts` | `connection.phase === 'polling'` |
| `GET /api/system/developer` | `developer/+page.svelte`: 500 ms `setInterval`, `inflight`, `payload`, `error` | `effects/graphPoll.ts` | the graph route is mounted and the tab is visible |
| `GET /api/system/status`, `/storage` | `SystemStats.svelte`, `Storage.svelte`, each with a `$effect` timer | `effects/widgetPoll.ts` | the widget is mounted (one subscription per mounted widget) |
| `GET /api/apps` | `catalog/+page.svelte` `onMount` | `commands.fetchCatalog`, from the route's `load()` | once, then on an explicit refresh or a stale phase |
| `GET /api/setup/status`, `/api/auth/me` | `+layout.svelte` on-mount fetches | `commands.bootstrap`, from `+layout.ts` | once, inside `session: bootstrapping` |

`poller.ts` stops taking a callback from its caller. That inversion is the whole
point: today the poller's lifetime is a side effect of whoever called
`startPolling` last.

## What a dumb component means

The test applied to every phase: a component reads slices and dispatches commands,
and holds nothing the store should own. Two real examples, before and after.

**The developer page** owns six `$state`, a timer, an in-flight guard, and its own
error extraction today (`developer/+page.svelte:34-115`):

```ts
let loaded = $state(false); let error = $state('');
let nodes = $state<Node[]>([]); let edges = $state<Edge[]>([]);
let payload = $state<DeveloperGraph | null>(null);
let inflight = false;
async function load() { if (inflight) return; inflight = true; /* ... */ }
onMount(() => { void load(); interval = setInterval(() => {
    if (document.hidden) return; void load(); }, 500); });
```

After, the page holds no data at all:

```ts
const view = select(graphView);       // nodes, edges, rows, error, orchestrator
useGraphPoll();                       // the effect starts and stops itself
```

The route-scoped call is the only thing the page still does about data, and even
that is a call, not a loop. `inflight` disappears because the command layer dedupes
by key; `error` becomes part of the slice's phase; the `document.hidden` check
moves to the effect, where the widget polls need the same rule instead of
repeating it.

**The install tile** reads progress from one store and status from another, and the
page decides which one means what (`routes/+page.svelte:31,48,68,93`):

```ts
const INSTALL_VIEW_STATUSES = new Set<string>([/* ... */]);
let installModalApp = $state<App | null>(null);
let installModalLiveApp = $derived.by(() =>
    $apps.find((a) => a.catalog_id === installModalApp?.catalog_id) ?? installModalApp);
if (INSTALL_VIEW_STATUSES.has(app.status)) { installModalApp = app; /* ... */ }
```

After: `select(appView(id))` returns `{ status, phase, percent, error, isOpen }`
already joined, and the click handler asks the slice what a click on this tile
means. `INSTALL_VIEW_STATUSES` and the live re-lookup become selectors
(`canOpen(app)`, `openProgressOn(app)`), which is where they belong: they are rules
about the domain, not about a tile.

What stays in components, deliberately: modal open, context-menu coordinates,
search text, form drafts, filter selection. Those are the component's own pixels,
and moving them into the store would make it a dumping ground rather than a source
of truth.

## Transport: the endpoints stay, ownership moves

The transport is not where the value is. The problem this plan exists to fix is
that components own orchestration, not that the developer page asks the server a
question twice a second. So every endpoint keeps its shape and gains an owner, per
the table above.

Keeping the graph poll is the better choice, and it is worth being explicit about
why, because "replace polling with push" reads like the mature option:

- **The poll costs nothing worth optimizing.** One request per half second, on a
  route an operator opens deliberately, answered from in-memory catalog and store
  reads. This is not a hot path.
- **Push would be more work and more risk for the same UX.** It needs a bus type,
  publisher wiring, and a coalescing drain in the SSE writer
  (`internal/api/events_module.go:85`), because a convergence pass fires dozens of
  node transitions and an uncoalesced rebuild per transition would be worse than
  the poll it replaced. That is backend work with its own burst-case tests, buying
  latency nobody can perceive.
- **The poll self-heals for free.** A missed change is corrected by the next full
  payload. A push design has to buy that guarantee back with a resync path, which
  is exactly how the stream-revision idea below started.
- **The FSM benefit is identical either way.** A page becomes dumb when the store
  owns the read and the page reads a slice. Whether that slice is fed by a timer or
  a socket is invisible to it, which is the proof the transport was never the point.

Two adjacent findings, both worth fixing and neither belonging here: `/api/system/status/stream`
(`internal/api/system_module.go:154`) is a 500 ms SSE nothing consumes while the
widgets poll every 10 s, so it should be used or deleted; and
`/api/system/developer` is mounted unauthenticated
(`internal/api/router.go:418`, `NewSystemRouter(m.system, npub)`), so topology,
container names, and orchestrator queue depth are readable by anyone who can reach
the port. Both are their own issues, not smuggled into a state refactor.

### Deferred: stream revisions

Stamping events with an `id:` from a monotonic counter and honoring
`Last-Event-ID` would let the client tell "nothing happened" from "you missed
something" instead of inferring it from silence (`FALLBACK_DELAY_MS`). Deferred:
the fence already heals disconnects (a full snapshot on every connect), the graph
and widget polls heal their own reads, and the ring is server-side state plus a
replay path bought for a shorter stale window. If the watchdog starts firing in
normal use, that is the evidence to build it on.

## Hot reload

What has to survive a code edit is the state the server told us. That is a much
smaller claim than replaying history, and the transport already provides it.

- `kernel.ts` creates the store once on a `globalThis` key and reuses it if the
  module is re-evaluated. This is the only mechanism that survives a module
  replacement, which is why the singleton is a kernel concern rather than an
  accident of module scope.
- `hmr.ts` accepts the slices module and calls `store.replaceReducer(next)`. The
  state survives and the new reducers govern everything after the swap. That is
  the honest scope of the claim: a reducer edit no longer costs the grid, the open
  install progress, or the stream.
- For the case where the *new* logic should be seen applied to state the *old*
  logic built, `resync()` re-dispatches the last snapshot after the swap: the
  snapshot fence refills the server-derived slices, and the new
  reducers compute them from scratch. One request, no page reload, and it uses the
  fence the design already has instead of a replay mechanism invented for it.
- `hmr.ts` disposes effects on `import.meta.hot.dispose`: close the
  `EventSource`, clear timers, cancel commands. On re-accept the effects restart
  from what `connection.phase` says, and with stream revisions the reconnect
  replays the gap rather than relying on a full snapshot.
- The test is small and it is enough: dispatch a sequence, swap the reducer, and
  assert the state survived and a new action lands under the new logic.
- View state (an open modal, a typed form draft) is deliberately not preserved.
  Losing a dialog on a reload is fine; losing install progress is not.
- Honest limits: a `.svelte` edit Vite cannot hot-swap still reloads the page,
  and a full reload still starts from empty. That is fine and it is not a gap: the
  server holds the facts, the snapshot contract refills the store in one request,
  and a client-side cache of them would be a second source of truth with worse
  latency than the first.

## Optimistic state is an overlay, not a write

The grid is where this matters, and it is the case the first draft hand-waved.
`GridStackGrid` holds positions the server has not confirmed yet, `PUT
/api/user/layout` replaces the whole set, and the next snapshot overwrites
whatever the user just moved. So optimistic edits live in their own slice,
`commands.optimistic`, and one selector applies them *over* the confirmed state:

```
layout selector = confirm(state.layout) ⊕ overlay(state.commands.optimistic.layout)
```

A confirmed save drops the overlay entries; a failed save drops them and the
pre-edit positions reappear with no rollback code, because nothing was ever
overwritten. That also gives the layout its real sync machine
(`clean → dirty → saving → clean | conflict`), and it exposes the bug the current
shape hides: `PUT /api/user/layout` is last-writer-wins with no revision, so two
open tabs silently clobber each other. The fix is a revision on the position set
and a rejected write on a stale one, which is one column and a `WHERE` clause
(schema migration 10). Optimistic concurrency, not a merge: two tabs dragging the
same tile is a conflict to report, not a document to reconcile.

## State shape

```ts
interface RootState {
  session:    { phase: 'bootstrapping'|'awaiting_setup'|'connecting'|'ready'|'signed_out';
                user: CurrentUser|null; setupRequired: boolean; attempts: number };
  connection: { phase: 'idle'|'connecting'|'streaming'|'polling'|'offline';
                lastEventAt: number; lastSnapshotAt: number; fallbackReason: string };
  apps:       { phase: Phase; byId: Record<string, App>; order: string[];
                lastSnapshotAt: number };
  layout:     { elements: GridElement[]; sync: 'clean'|'dirty'|'saving'|'failed' };
  launchers:  { byId: Record<string, Launcher>; order: string[] };
  progress:   { episodes: Record<string, Episode> };   // key: `${catalogId}:${gen}`
  catalog:    { phase: Phase; byId: Record<string, CatalogApp>; order: string[];
                fetchedAt: number; plans: Record<string, { install?: InstallPlan; remove?: RemovePlan }> };
  graph:      { phase: Phase; topology: DeveloperGraph|null; states: Record<string, NodeState>;
                orchestrator: OrchestratorStatus|null; lastPushAt: number };
  external:   { phase: Phase; byId: Record<string, ExternalApp>; order: string[] };
  settings:   { publicUrl: Resource<string>; ai: Resource<AISettings>; users: Resource<ManagedUser[]> };
  commands:   { byKey: Record<string, { kind: string; target: string;
                phase: 'pending'|'settled'|'failed'; settle: 'request'|'reread'|'convergence';
                deadline?: number; error?: string; inverted?: Patch }> };
  toasts:     { items: Toast[] };
  ui:         { sidebarCollapsed: boolean };
}
```

`Phase` is the shared `idle | loading | stale | ready | error` union. Slices are
written only by reducers, and a server-derived slice is replaced whole by the next
snapshot (the fence) rather than merged into. `Resource<T>`
is that phase plus `value: T | null` and an optional error, and it deliberately
has no `saving` field: in-flight is a fact about `commands`, derived from the
commands whose `target` is the resource. That is what collapses the
`loading`/`error`/`saving`/`submitting` boolean sets in the three settings
sections (13, 9, and 6 `$state` declarations respectively).

## Phasing: fixed end state, no shims left behind

Strangling is still the right path. What changed is the rule: **every phase
deletes the thing it is standing in for.** A phase that leaves a compatibility
layer behind has not landed, because the layer is where the next contributor
writes. The target above is not a stretch goal to reach if time allows.

| Phase | Scope | Deleted in this phase | Cost |
|---|---|---|---|
| 0 | Kernel, action unions, `select()` binding, command layer with the three settle kinds, `globalThis` singleton, `replaceReducer` plus dev `resync`, devtools | Nothing yet: nothing consumes it | S |
| 1 | `session`, `connection`, `apps`, `layout`, `launchers`; `commands.bootstrap`; generated API types | `appEvents.ts`, `poller.ts`, `shouldEnableFallback`, `appFacade.initialized`, and every legacy `$store` import in a migrated component | L |
| 2 | The mirror as reducers over the node/pull events; toasts; correction fixtures | `mergeSnapshot`'s settled heuristic, `lastStatuses` module memory | S |
| 3 | Catalog (plus plans), graph as a store-owned poll, external providers, widget polls | The developer page's interval and `inflight`, the widget timers, `developer/+page.svelte`'s six `$state` | M |
| 4 | Settings as resources with declared settle kinds, setup wizard, SvelteKit `load()` for route data, admin gate as state | Every `onMount` fetch, the address convergence loop, the AI save's fake success | M |
| 5 | Layout as a versioned document (schema migration 10), optimistic overlay for grid edits, component test harness, bundle budget, docs | `gridElements`' direct-mutation API (`addApp`, `toggleWidget`), the last `fetch` allowlist entries | M |

Three ordering notes, because these are the calls that look arbitrary:

- **Phase 0 is small on purpose.** Kernel, binding, and commands are one unit: the
  binding has nothing to select over without the kernel, and commands without
  settle kinds grow the per-component convergence loops that phase 4 is supposed
  to delete. Smaller than that and it is a library with no consumer.
- **Phase 1 carries the generated types on purpose.** Generated payloads are what
  stop the mirror from inventing a phase of its own, and every later phase reads
  through them. Deferring them leaves the slices hand-typed against a Go struct
  they match only by comment, which is the drift this plan is meant to end.
- **Phase 5 is last because it is the only phase that needs a schema migration.**
  Everything before it is frontend only.

Cost, honestly: the extra over an incremental version is concentrated in phase 1
(the generated types) and phase 5 (the layout revision).
Phase 0 is small. What the extra buys is that nothing in the result is temporary:
no legacy-store shim, no provisional transport, no convergence loop parked in a
component with a comment saying it should move. The cheaper plan produces a
working store inside a codebase that still has three ways to do the same thing.

## Guardrails

The repo keeps rules honest with data-driven checks that fail when an exemption
guards nothing (`scripts/pinned-images.mjs`, `scripts/file-length.mjs`). The
refactor needs the same treatment, or it quietly reverses itself.

- **`check:no-component-fetch`**: no `fetch(` inside `*.svelte`, with an
  `EXCEPTIONS` table carrying a reason per file, shrinking to empty as phases
  land. Without it, phase 4 regrows the islands and the refactor is temporary.
- **`check:no-legacy-store`**: `from 'svelte/store'` may appear in
  `store/bindings.svelte.ts` and nowhere else. This is the check that keeps the
  runes binding from becoming an option.
- **`check:no-component-timers`**: no `setInterval` or polling `setTimeout`
  inside `*.svelte`, and at most one `new EventSource` in the whole tree. Timers
  live in `store/effects/`. This is the check that keeps components dumb after the
  refactor lands, which is the part that tends to rot.
- **`check:api-types`**: regenerate `types.gen.ts` from the Go request/response
  structs, the `eventbus` payloads, and `phaseForStatus`, and fail on a diff. Same
  gate shape as `depgraph --check` and `catalogdoc --check`. Today
  `lib/types.ts` claims to match `catalog.App` in a comment, and `PHASE_ORDER`
  duplicates `phaseForStatus` by hand; both drift silently, and a generated file
  cannot.
- **`check:bundle-budget`**: a ratchet on total JS bytes in `web/build`, same
  baseline shape as the file-length ratchet, where a recorded ceiling can only
  ever go down. This is the issue's "minimize bloat" made enforceable, and it is
  the check that notices when the next graph library lands.
- **Settle completeness**, enforced by the type rather than by a test:
  `CommandSpec` is a discriminated union on the settle kind, so a `convergence`
  command with no predicate or no deadline does not compile. That landed with
  phases 0 to 2 and is stronger than the test originally sketched here, for the
  same reason `apps/configtest/waitbudget_test.go` is stronger than a runtime
  check: the mistake is impossible to merge.
- **Action-type registry**: `KNOWN_ACTION_TYPES` in `store/actions.ts` lists
  every action the store accepts, and a middleware drops a type that is not on
  it with a console error. `Action` is structurally `{ type: string }`, so the
  union is a convention the compiler cannot enforce; a typo'd type would fall
  through every reducer's `default` and the symptom would be a UI that quietly
  stops updating.

All of them go in the `fast` tier of `validation.yaml` and in the pre-push hook.

## Testing

- **Action sequences are the main test shape.** A scripted sequence of actions
  goes through the reducers and the resulting state is compared against a golden
  file. `.golden.yml` testdata is already a pattern here, so the shape exists. One
  recorded SSE transcript of a real Jellyfin install becomes the richest test in
  the suite: snapshot, node transitions, pull ticks, and a snapshot that corrects
  them, all deterministic.
- **Transition tables**: one table-driven test per machine, every
  (state, event) pair, including the pairs that must be no-ops. A machine that
  accepts an undefined transition is a bug, and the table is where it shows.
- **The old merge tests are the migration net**: `mergeNodeEvent`,
  `mergePullEvent`, and `mergeSnapshot` cases move to action-sequence fixtures
  unchanged, so a regression in phase 2 shows up as a failing legacy case.
- **Agreement test**: the client aggregation rule against the server's, over a
  table of container-phase combinations (rule M5).
- **Reducer-swap test**: dispatch a sequence, call `replaceReducer`, assert the
  state survived and a new action lands under the new logic. That is the hot
  reload claim, stated at the size it can actually be verified.
- **Component harness**: seed a store from a fixture and render `AppTile` or
  `AppInstallModal` in vitest, so tile and modal states stop being e2e-only
  coverage. Playwright keeps the cross-process journeys.
- **e2e contracts must not move**: `.app-slot`, `.install-spinner`,
  `.phase-label` are asserted in `e2e/tests/install-streaming.spec.ts:51` and
  `e2e/tests/launcher.spec.ts:23`, and `docs/features/dashboard.md` names them
  as contracts. The binding exists so they never have to.
- **No new e2e for the store itself.** The store is internal; the existing
  install-streaming and lifecycle specs already cover the user-visible path it
  serves, and a browser test that asserts a reducer ran is testing the kernel.

## Decisions

| Fork | Chosen | What it buys, what it costs |
|---|---|---|
| Kernel | `redux` package plus a hand-written command layer, no toolkit or middleware library | `combineReducers` and `replaceReducer` are not worth re-implementing, and `replaceReducer` is what lets a swapped reducer take effect live; the cost is one dependency and a rule that the command layer never grows into a saga |
| Lifecycle modeling | Mirror the orchestrator's phases client-side | Live per-node transitions without a round trip, and a graph view that can show `target` against `actual`. Costs the six reconciliation rules above; the drift risk is why M1 to M6 are not optional |
| Transport | Keep every endpoint, move ownership of every read into the store | The FSM value comes from ownership, not transport: a page that reads a slice is dumb whether the slice is fed by a poll or a socket. Costs nothing, and it leaves the graph poll's free self-healing in place |
| Hot-reload scope | Server data and commands only | The state that is expensive to recover survives; a modal or a form draft does not. Costs nothing, and it keeps the store from becoming a dumping ground for component trivia |
| Settings writes | Modeled as convergence-bound commands | The address save already polls for convergence by hand and the AI save treats a 202 as completion; one declared settle kind makes both representable, and makes "saved but never applied" a state the UI can say out loud. Costs a predicate registry and a completeness test |

### Calls that cost more now

These are the ones the plan refuses to do cheaply, listed so the cost is visible at
the point of choosing rather than at the point of discovering it.

| Call | Over what | Why |
|---|---|---|
| Runes binding via one `select()` | `svelte/store` `readable` wrappers | The wrapper is a shim with no deletion criterion, and shims attract new code in the old idiom. Costs touching every component once |
| Generated API types | A hand-maintained `lib/types.ts` | The file already claims to match a Go struct in a comment, and `PHASE_ORDER` duplicates `phaseForStatus`. Costs a generator and a `--check` gate, both of which this repo already knows how to build |
| Layout revision and rejected stale writes | Last-writer-wins `PUT /api/user/layout` | Two open tabs clobber each other today and neither notices. Costs one column, one `WHERE` clause, and migration 10 |
| SvelteKit `load()` for route data | `onMount` fetches | Parallel loads, real error boundaries, no shell flash before a redirect, and the data need is declared where the route lives. Costs moving six `onMount` bodies, plus `ssr = false` on those routes: adapter-static prerenders at build time, when there is no API to talk to, which is exactly why the current code hides its fetches in `onMount` |

### Considered and rejected

Recorded because both ideas are attractive and someone will propose them again.

- **An append-only fact log with state derived from it.** Rejected: the engine is
  not a log, bounded windows break the replay-exactness that justifies it, devtools
  already is an action log, and it doubles the representation of every fact. The
  snapshot fence gives most of the benefit for none of the machinery. See "Why not
  an append-only log".
- **Stream revisions with `Last-Event-ID`.** Deferred, not rejected: a real
  improvement, but the fence already heals disconnects and a forced resync covers
  the rest. Details and the trigger to revisit are in "Deferred: stream
  revisions".
- **A `catalogVersion` on the catalog read and the snapshot**, to turn
  `catalog.stale` from an assumption into a fact. Deferred: nothing in the FSM
  needs it, and it is the missing piece for the catalog-update reconciliation plan
  rather than for this one.
- **Optimistic concurrency for the grid via merge.** Rejected in favour of a
  revision and a rejected write: two tabs dragging one tile is a conflict to
  report, not a document to reconcile.
- **Pushing graph state over the event bus instead of polling it.** Rejected: the
  poll is not costing anything, push needs a bus type plus a coalescing drain to
  avoid being worse than the poll it replaced, and the FSM benefit is identical
  either way. See the transport section.

## What this is not

Stated because "right long-term design" invites exactly the wrong kind of scope:

- **Not a component-library or design-system change.** The design and
  accessibility work in #270 is separate and landed at the P1 stage.
- **No SSR.** `adapter-static` stays. host-agent serves a bundle; there is no
  server-render pass to design for.
- **No persistence, no offline mode.** The log lives in memory and the server
  replays it. A client-side cache would be a second source of truth with a longer
  latency than the first.
- **No merge engine for the grid.** A stale layout write is a conflict to report,
  not a document to reconcile. See "Considered and rejected".
- **No `redux-toolkit`, no sagas, no `reselect`.** The command layer and the
  plain selectors cover what those buy, and each one is a dependency this dashboard
  does not need to load.
- **No rewrite of the canvas or the grid.** `graphLayout.ts`, `graphSync.ts`, and
  `GridStackGrid.svelte` keep their integrations; the store feeds them the shapes
  they already expect.

## Risks

- **Two reactivity systems is the real risk.** Redux holds state, Svelte holds
  subscriptions, and the seam is where the bugs will be. The seam is one function
  (`select`), and it trades per-selector precision for one version counter, which
  is the right trade at this scale and the wrong one if a screen ever renders
  thousands of subscribers. Name the trade in the file so the next reader can
  measure it instead of guessing. And no component dispatches from a `$effect`
  that reads a selector: that is how a reactivity loop gets written.
- **A mirror that drifts is the second risk**, accepted with the six rules and
  the correction record as the mitigation. If corrections start showing up in
  normal use, the right response is to narrow the mirror, not to relax the rule.
- **Phase 0 lands nothing a user can see**, and phase 1 is the big one. That is
  the honest cost of doing the transport properly, and it is the argument someone
  will make against the plan. The answer is that the stream contract is what makes
  the connection machine honest instead of heuristic: deferring it leaves the
  machine provisional for the whole migration, which is the shim this plan exists
  to avoid.
- **Overlay correctness is the subtle part.** A selector that forgets to apply
  `commands.optimistic` shows the user their own edit as not-yet-happened, which
  reads as a bug in the save rather than in a selector. Mitigation: the overlay is
  applied in exactly one module, and a test asserts a drag renders before the save
  confirms.
- **Phase 1 touches the live stream and the stream contract.** It is the phase
  most likely to regress the install journey, which is why `install-streaming.spec.ts`
  and a recorded SSE transcript are its exit criteria rather than a manual click
  through the dashboard.
