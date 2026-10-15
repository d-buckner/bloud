# Tech-debt history

The record of debt this repository has already paid, kept because the
reasoning and the measurements are the value. The living inventory and the
newest repayment are in [`../tech-debt.md`](../tech-debt.md).

This file was split out of the ledger on 2026-10-15: 480 of the ledger's 965
lines were closed-item narrative, which buried the 78 lines of open inventory
a reader comes for.

## Biggest Debt: the auth bypass was remote and forgeable (fixed 2026-09-19)

> **Status: closed** (working tree; PR 4 of the repayment plan). Kept here as the
> record of what the rule costs, because the fix is a set of small constraints
> that are easy to undo one at a time.

**What it was.** The 2026-09-17 version of this entry said loopback requests are
granted `RoleAdmin` with no credential, so *"network position is the credential"*.
That framing was too generous to the system. Network position was not required:

| # | Location | Fact |
|---|---|---|
| 1 | `apps/traefik/metadata.yaml:15-16` | `apps-traefik` runs `network: host` |
| 2 | `internal/appconfig/traefik.go:76-80` | entrypoint `web` sets `forwardedHeaders: insecure: true` |
| 3 | `internal/appconfig/traefik.go:109` | upstream is `http://localhost:<hostAgentPort>` |
| 4 | `internal/appconfig/traefik.go:118-121` | router `host-api` exposes `PathPrefix('/api')` on the public entrypoint |
| 5 | `internal/api/router.go:236` | `r.Use(middleware.RealIP)`: global, before the `/api` subtree |
| 6 | `internal/api/auth_module.go:99-124` | `isLocalRequest` → `ip.IsLoopback()` → `true` |
| 7 | `internal/api/router.go:546-550` | loopback ⇒ `User{"_cli", RoleAdmin}`, no credential |

Traefik's `XForwarded.ServeHTTP` skips `DeleteXForwardedHeaders` when
`insecure` is set, so client-supplied `X-Real-Ip` / `X-Forwarded-*` survive
unmodified; `True-Client-IP` is not a Traefik-managed header at all; chi's
`realIP` checks `True-Client-IP` first and assigns `r.RemoteAddr` from it
without validating the sender.

```sh
curl -H 'True-Client-IP: 127.0.0.1' http://<box>:8080/api/apps/installed
```

That request is admin. So is `POST /api/apps/immich/uninstall` with
`clearData`, `PUT /api/settings/public-url`, and `POST /api/admin/users`
(persist as a real Authentik admin). `:3000` binds every interface
(`internal/api/server.go:108`), so the direct path works too, and any
app container can reach the host gateway.

**Fix surface (measured).** `r.RemoteAddr` has exactly two consumers in all of
host-agent, both inside `isLocalRequest` (`auth_module.go:100,102`); the
forwarded-header surface is three reads total, all in the same file
(`:135` `X-Forwarded-Host`, `:161` `X-Forwarded-Proto`). Nothing else uses
client IP: no rate limiting, no source-keyed audit log. The bypass can
therefore be removed with no behavioral regression beyond the bypass itself.

**This was a shipping blocker**, and the cheapest high-impact change in the
repo. **What closed it (2026-09-19):**

- `middleware.RealIP` deleted from `NewRouter`: `r.RemoteAddr` is the real TCP
  peer again. Nothing else in host-agent consumed client IP (verified: two
  readers, both inside `isLocalRequest`).
- The trusted position is now a *scope*, not a credential: admin requires
  `Authorization: Bearer <apiToken>` **and** a trusted position, checked before
  the session path (`authMiddlewareFn`). Order matters: a position-first check
  would force every Traefik-proxied browser request through the token path and
  lock the dashboard out (Traefik's backend connection is loopback).
- An empty configured token disables the position path entirely (fail closed).
- `apiToken` is generated and backfilled by the secrets manager, and written
  standalone to `<dataDir>/host-agent-api-token` (0600) for the CLI/e2e to read.
  The name is mirrored, not shared: `cli/executor.DataDirs.APITokenPath`,
  pinned by `secrets.TestAPITokenFileNameIsStable`.
- Traefik no longer ships `forwardedHeaders.insecure: true`; its default
  (discard client-supplied `X-Forwarded-*`/`X-Real-Ip`, then set its own) is the
  whole config now, pinned by an `appconfig` test.
- OAuth redirect/logout URLs come from the host set, not the request:
  `X-Forwarded-Host`/`X-Forwarded-Proto` are no longer read, and the login
  handler no longer **registers** an unknown redirect URI in Authentik
  (`AddRedirectURI` deleted from the client and the module interface).
- `GET /api/setup/status` and `POST /api/setup/create-user` are registered
  exactly once, on a public bootstrap router (`NewSetupRouter`). They must be
  public: first-run has no user to authenticate as, and create-user self-limits
  with a 409 once any user exists.
- The suite can now see this boundary: a router-classification table test over
  the **real** tree with a non-loopback `RemoteAddr`, spoofed-header cases, the
  ordering trap, and the bootstrap routes. The old helper forced loopback, which
  is why none of this was visible before.

**Verified live** against a deployed host-agent (lima `bloud-dev`), not just in
unit tests: unauthenticated `GET /api/apps/installed` → 401, including with
`True-Client-IP` / `X-Real-IP` / `X-Forwarded-For` spoofs, directly and through
Traefik; `POST /api/apps/jellyfin/uninstall` with a spoof → 401; wrong bearer →
401; valid bearer (from `./bloud token`) → 200; `/api/setup/status` → 200
unauthenticated; `/auth/login` with `Host: evil.example` →
`redirect_uri=http://localhost:8080/auth/callback` (the configured host), and
nothing registered in Authentik; `./bloud install`/`uninstall` still work end to
end. Re-verified by `auth_bypass_test.go`, which fails with `200`s against the
pre-fix tree.

**Remaining verification gap:** the Playwright suites (`./bloud e2e lifecycle`,
`./bloud e2e app`) have not been run since the change.

## Second: the appliance fails silently (closed 2026-09-20, PR 6)
 
> **Status: closed** for the engine members; kept as the record of the class.
> The four defects below are fixed as of PR 6; item 5's lost-write variant
> was closed by PR 7 the same day. The health-surface bullet is half closed:
> a dead intent loop is now visible (`Stopped()` + `LastConverged()` feed
> `CheckSystemHealth` and `OrchestratorStatus`); the degraded-payload and
> startup-gate work (PR 11) remains open.
 
Four independent defects shared one property (the system stops working and
reports nothing), and item 5 in the ledger reached the same end from a fifth
direction: a write that is dropped, logged at `WARN`, and otherwise ignored.
This was a ranked class, not a footnote, because each one converted an
ordinary bug into an unnoticeable outage.

- **`SyncContainerState` nil-deref kills the process.** **FIXED (PR 6).**
  The catalog result was dereferenced before checking the error;
  `MemoryCache.Get` returns `(nil, err)` on a miss and `ContainerDefs()`
  has a pointer receiver. A miss is reachable whenever an installed app's
  directory is removed or renamed (`catalog/loader.go:43-45` skips dirs
  without `metadata.yaml`), and the sync runs on every convergence pass.
  There is **no `recover()` anywhere in host-agent**, so the daemon died.
  Now: err/nil checked first, the app is skipped with a `WARN`, and
  `orchestrator_containers_test.go` pins the skip and the repair path.
- **`MemoryCache` was lock-free across a live refresh.** **FIXED (PR 6).**
  All access goes through a `sync.RWMutex`; `Refresh` builds the new map
  off-lock and swaps it in as one write-locked operation, so readers never
  see a half-filled cache and the disk load does not block them.
  `cache_test.go` hammers concurrent `Refresh` × readers under `-race`,
  and the race tier now covers `./internal/catalog/...`.
- **The intent loop could exit permanently.** **FIXED (PR 6).**
  `WaitAndDrain` returns `([]Intent, bool)`; cancellation is the only
  shutdown signal. The stale-token wake returns an empty live batch and
  `Start` skips it (`orchestrator.go` `Start` loop). Pinned by
  `TestWaitAndDrain_StaleTokenIsLiveEmptyBatch` and
  `TestStart_LoopSurvivesStaleSignalTokens`.
- **The health surface could not see any of it.** **HALF FIXED (PR 6):**
  `Orchestrator.Stopped()` reports the loop has exited and
  `LastConverged()` stamps every completed pass; `CheckSystemHealth` now
  fails on a dead loop, and `OrchestratorStatus` carries both fields for
  the developer surface. Still open (PR 11): the degraded payload for a
  missing Podman socket, the startup-gate depth, and the exit-on-failure
  policy.
- **Operation-ledger writes are lost to lock contention on one row (item 5,
  re-observed 2026-09-19).** On a normal convergence pass during a redeploy,
  `recordOpPhase` → `OperationStore.AdvancePhase` lost the write:

  ```
  WARN operation recorder: phase failed  app=authentik phase=prestart
       error="advance operation authentik to prestart: database is locked (5) (SQLITE_BUSY)"
  ```

  The collision is structural, not a rare interleaving. Every phase record keys
  on `owner := o.ownerApp(id)` (`orchestrator.go:1023,1054,1066,1091`, …), one
  row per *app*, while the scheduler dispatches *nodes*, and nodes in one
  topological level run concurrently. So for a multi-container app, N nodes
  issue `UPDATE operations … WHERE app_name='authentik'` against a **single row**
  at the same time. The same log window shows the reproduction exactly:

  ```
  level work collected              nodes=2 work=2
  dispatching full lifecycle        app=apps-authentik-worker
  dispatching full lifecycle        app=apps-authentik-server
  lifecycle phase: EnsureContainer  app=apps-authentik-worker
  lifecycle phase: PreStart         app=apps-authentik-server
  operation recorder: phase failed  app=authentik phase=prestart SQLITE_BUSY
  ```

  This is item 5 firing, not a separate defect: `db.InitDB` sets
  `PRAGMA busy_timeout=5000` with `db.Exec` on **one** pooled connection, so
  every other connection has `busy_timeout=0` and fails immediately instead of
  waiting for the lock. The recorder is the highest-frequency writer in the
  system, so it is simply the first to lose. `recordOpPhase` logs and returns
  (`operation_recorder.go:85`); by design it must never change lifecycle
  behavior, which also means nothing downstream notices: the row keeps whatever
  write landed last, and since the operations row is *authoritative for failure
  context*, the dashboard's diagnostic surface can show a phase that is stale or
  not the one the drive actually reached, with no visible error. (Container nodes
  frequently write the same phase value, so many of these losses are
  value-identical, but each still takes the write lock, and where the nodes'
  phases differ the surviving value is arbitrary.) The other half of item 5 is
  worse and fully silent: `foreign_keys=OFF` on those same connections means
  cascades stop firing and orphan `shares` / `user_app_positions` rows
  accumulate with no log line at all. **Fixed by repayment PR 7
  (2026-09-20):** the pragmas now ride the DSN and are applied by the
  driver at every connection open; see "Already Paid" below.

  The structural half closed later, in #202 (2026-10-15): `operations` is
  keyed `(app_name, node)`, so the recorder's unit of work matches the
  engine's and no two nodes write the same row. The app-level answer is now
  a documented projection rather than whichever write landed last. Ledger
  item 34.

## Corrections to earlier "Already Paid" entries (2026-09-19)

Two claims in the history below do not hold up against the code:

- **"Persist only externally issued artifacts"**: the direction is right, but
  the claim that integration credentials are *not* stored is already violated:
  `sso/blueprint.go:284-287` persists the HKDF-derived OAuth client secret via
  `secrets.SetAppSecret(app, "oauthClientSecret", …)`, and the field's own
  comment in `secrets/manager.go:52-53` says "derived from SSOHostSecret".
  Nothing reads it back, so today it is dead persisted state, not a split brain.
  Delete the write, or make the derivation authoritative and drop the field.
- **"Versioned migrations + the `user_app_positions` schema fork"**: the ledger
  itself is correct and worth keeping, but entry 6 cannot fire: the grid table
  shape already existed before the ledger ran, so the "fork fix" is a permanent
  no-op. The migration path is still an improvement; the described bug fix is
  not evidence of one.

### The instance converges on its own (2026-09-30, item 25)

The invariant this closes: **reality is made to match intent without a
human in the loop.** Item 8 put the repair machinery in place, but it sat
behind a trigger that only user actions pulled. A container that died
overnight stayed dead, and an app that landed in `error` stayed there with
`retryable: true` on its operation row, which was a promise nothing kept.

- **The trigger is an intent, not a second driver** (`selfheal.go`).
  `startSelfHealing` submits a `ReconcileIntent` into the same queue every
  other trigger uses. A second goroutine calling `converge` directly would
  have been a second writer into the engine, which is the one thing
  invariant 1 forbids.
- **The timer is idle-based, not a ticker.** Every completed pass pings the
  loop and pushes the next one out a full interval, so the interval is a
  floor on the gap between passes. A ticker would have stacked a pass
  100ms behind a user install that happened to land at t+59.9s, and a
  pass that outlives the interval would have turned the loop into a tight
  submit cycle.
- **`retryable` gets its driver.** `retryErroredNodes` resets an `ERROR`
  node whose owning app's operation row is failed-and-retryable, which is
  the case with no other answer: the settings-change path deliberately
  does not force a failing app, so without this the change would never
  reach it. `ERROR` stays terminal for every other trigger, and a failure
  marked non-retryable is left alone, so the pass cannot hammer a cause no
  retry can fix.
- **A no-op pass is proven free, not assumed free.** The issue called this
  the deliverable rather than an afterthought, and it is the reason the
  feature is safe at all: a once-a-minute pass that restarted something
  would replace a rare bug with a 60-second reboot cycle.
  `TestSelfHeal_IdlePassChangesNothing` converges a healthy stack twice and
  asserts zero container creates, zero removals, zero node status writes,
  and zero configurator `PreStart`/`PostStart` calls.
- **The route file was the one thing that was not idempotent.**
  `GenerateAll` wrote atomically on every pass regardless of content, so
  Traefik reloaded the whole dynamic config once a minute forever. It now
  compares the rendered bytes to what is on disk and skips the write when
  they match.

Tunable with `BLOUD_RECONCILE_INTERVAL` (Go duration syntax; `off`
disables). The default is `DefaultSelfHealInterval`, 60s, applied by
`wire.Build` so a hand-built orchestrator in a unit test stays quiet unless
it asks for a timer.

### Reality matches intent for container drift and image pulls (2026-09-27, items 8-9)

The invariant this closes: **a graph node must never claim `RUNNING` while
its container is not running.** `collectWorkForLevel` drives all work off
exactly one comparison, `TargetStatus != ActualStatus`, so any observation
that contradicts a node's actual status has to be written into the node.
Writing it into the store instead produces a system that knows something is
wrong and can act on none of it.

- **Drift is repaired, not just recorded** (`orchestrator_containers.go`).
  `repairDriftedNode` resets a `RUNNING` node whose container is not
  running to `INITIALIZING` with a reason, so the same convergence pass
  re-dispatches the full lifecycle. Safety rests on invariant 2:
  configurators are idempotent, so a re-drive is the normal case, not a
  special one. `ERROR` is terminal and is deliberately not retried, and an
  intermediate status means a drive is already in flight, so neither is a
  drift candidate.
- **Multi-container apps are covered.** The `len(defs) != 1` skip was
  justified by a comment claiming their lifecycle was "tracked via graph
  events, not this path". That division of labor did not exist, and the
  skip excluded exactly the apps with sidecars (postgres, redis, ML) where
  one dead container takes the app down. Each def is now inspected and each
  node repaired independently; app-level status still aggregates through
  the existing `allContainersRunning`.
- **Uninstalling apps are excluded before any repair can fire**, so drift
  detection cannot resurrect an app the user is removing, and an
  uninspectable container is omitted rather than assumed gone, so a
  transient runtime error cannot reset healthy nodes.
- **`Ensure` pulls before it destroys** (`internal/container/runtime.go`).
  The order is `guard → pull → remove → create → start`. A registry
  outage, rate limit, or digest mismatch now fails while the previous
  container is still running, instead of after it has been removed with
  `force=true` and with no rollback.
- **One ownership guard.** The `io.bloud.managed` check is extracted to
  `isManaged` and called by both destructive paths. Previously `Remove`
  enforced it and `Ensure` bypassed it, so the guard lived only where it
  was not needed and a name collision outside Bloud was destroyed without
  a look.

Contract tests: drift on a single-container node, drift on one node of a
multi-container app with the healthy node left untouched, `ERROR` not
retried, uninstalling not re-driven, no reset when the container is
genuinely running (so the repair cannot become a per-pass recreate loop),
pull failure leaves the running container present and unremoved, unmanaged
container refused on the recreate path, and the exact call sequence
`pull, remove, create, start`.

`TestSyncContainerState_MultiContainerSkipped` was deleted rather than
adapted: it asserted the wrong contract, so preserving it in any form would
have preserved the bug as a requirement.

Plan: [`docs/plans/archive/reality-match-intent.md`](../../plans/archive/reality-match-intent.md).

*Still open from this area:* the live end-to-end check (`./bloud e2e
lifecycle`, plus manually `podman rm -f apps-jellyfin` against a running
host-agent and watching the next pass recreate it) has not been run; and
item 10's visibility half remains, since a repaired drift is silent by
design and an unavailable Podman socket still has no degraded signal.

### The engine's silent failures closed (2026-09-20, PR 6)

The three crash/hang members of the silent-failure class, each with its
contract test:

- **Catalog nil-deref** (`orchestrator_containers.go`): err/nil checked
  before the deref; a catalog miss skips the app with a `WARN` instead of
  panicking the daemon on the next convergence pass. Tests pin the skip
  (no runtime call, status untouched) and the single-container repair path.
- **`MemoryCache` race** (`internal/catalog/cache.go`): `sync.RWMutex`
  guards every read/write; `Refresh` builds off-lock, swaps once. New
  concurrent `Refresh` × readers test under `-race`;
  `validation.yaml`'s `go-host-agent-race` tier now runs
  `./internal/engine/orchestrator/... ./internal/catalog/...` (the
  2026-09-19 validation-bar ask: the old selection could not see this).
- **Intent queue stale-token exit** (`queue.go`, `orchestrator.go`):
  `WaitAndDrain` returns `([]Intent, bool)`; `live=false` only on
  context cancellation, queued intents are returned even then. `Start`
  treats an empty live batch as nothing-to-do. Tests: the deterministic
  stale-token reproduction, loop-survives-tokens, and
  `TestStart_ExposesLoopLivenessAndConvergenceStamp`.

Plus the liveness substrate PR 11 needed: `Orchestrator.Stopped()`,
`Orchestrator.LastConverged()`, both surfaced in `OrchestratorStatus`
(`LoopStopped`, `LastConverged`), and `Server.CheckSystemHealth()` fails
when the loop has exited.

### SQLite pragmas travel in the DSN (2026-09-20, PR 7)

`db.InitDB` no longer applies `journal_mode` / `busy_timeout` /
`foreign_keys` with a one-off `db.Exec`: that configured exactly one
pooled connection, leaving every other connection with cascades off and
`busy_timeout=0` (the observed `SQLITE_BUSY` phase-loss and the silent
orphan accumulation). The settings now ride in the SQLite DSN, which
the modernc driver executes at every connection open (busy_timeout
first, per the driver's own ordering requirement). `db.MemoryDSN()`
hands the same DSN query to `internal/testdb`, so the test and
production configurations are built from one source and cannot drift;
testdb keeps its single-connection pinning, which is still correct for
per-connection `:memory:` databases.

Contract tests (`internal/db/pragmas_test.go`; all three fail against
the pre-fix Exec-loop tree):

- every connection in a widened pool reports `foreign_keys=1` and
  `busy_timeout=5000` (not just the boot connection);
- a fresh connection rejects an orphan `user_app_positions` insert and the
  `user_preferences` DELETE actually cascades to the dependent position;
- a write contended by a held RESERVED lock blocks in the busy handler
  and succeeds after the commit, instead of failing instantly with
  `SQLITE_BUSY`.

The recorder keeps its log-and-return contract: with a real busy-wait
the lost-write window narrows to >5 s lock starvation, and the
multi-container single-row contention (nodes of one app racing one
`operations` row) remains a design constraint, not a correctness bug:
the surviving phase value is last-writer-wins by ledger semantics.

### Route generation no longer owns runtime side effects (2026-09-17)

The "Biggest Debt" of the previous version, a route config step that
silently started gateways and mutated proxies, is closed:

- `SyncRoutes()` (`internal/engine/orchestrator/orchestrator_containers.go`)
  is pure with respect to the runtime: no gateway calls, no proxy mutation.
  It reads the installed set from the store and hands it to the generator;
  the config write is its only effect.
- Contract tests (`route_sync_test.go`): the sync starts no runtime and writes
  only the route config; an uninstall stops routing the removed app in the same
  step that deletes its store row.

*Still open from this area:* `SyncRoutes` errors are logged only, promotion to
RUNNING is unconditional, and the generated YAML is never validated (item 18).

### Durable lifecycle operation state (2026-09-17)

The "Biggest Debt" of the 2026-09-16 version, no readable state behind the
orchestrator's phases, is closed for the backend:

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
  unchanged. `apps.status` remains the narrowed user projection: the
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
durable state. The fork itself, v6's dead
`(user_id, app_id, position)` shape silently shadowing the grid schema via
`CREATE TABLE IF NOT EXISTS`, is migrated by ledger entry 6
(`fix: user_app_positions shape fork`). The landmine the previous version of
this doc flagged as first-ranked is defused; every future migration inherits
the checked path instead of the ad hoc one.

*Correction 2026-09-19:* entry 6 is a permanent no-op in practice: see
"Corrections" above. The migration ledger itself stands.

### Earlier (since 2026-09-14)

- Status progression moved fully into the orchestrator: zero `go func(`
  launches in non-test `internal/api` code; `Submit()` records the immediate
  user-visible effect, `setupStatusSync()` owns the transition mapping.
  Invariant 1 holds in practice, not just by convention.
- App-client (S1-S10) gave configurators a deep client layer: `appclient`
  owns transport, retry policy, readiness polling, and asset provenance. All
  five user apps adopted it uniformly with a typed `api.go`. No second
  integration idiom. *(The retry-policy surface has since been found partly
  inert; see item 6.)*
- Hardcoded fallback secrets: `config.Load` is fallible
  (env > `secrets.json` > error); the corrupt-file fault-downgrade is closed;
  Jellyfin's bootstrap password moved to the secrets manager.

