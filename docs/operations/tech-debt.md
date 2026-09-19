# Backend Tech Debt

> **This is the single debt ledger.** New backend findings go here, not into
> `docs/specs/review.md`; that file is a dated review snapshot whose findings are
> annotated against the statuses recorded here. Frontend, CLI and CI-tooling
> findings live in the review snapshot itself (see below).

**Status:** Active debt inventory  
**Last updated:** 2026-09-19 (item 5 re-observed on a post-PR-4 redeploy,
adding a fifth member to the silent-failure class: the lost write is a `WARN`
nobody sees; prior same-day updates: first-ranked item re-scoped and re-ranked
— the loopback admin exemption is remotely forgeable, not merely a local-process
concern; a new second-ranked class records the engine's silent-failure paths;
prior update 2026-09-17: route-generation purity landed, plus versioned
migrations + durable operation state)

Source for this revision: [`docs/specs/review-2026-09-19.md`](../specs/review-2026-09-19.md)
(9 parallel subsystem audits, high-severity findings re-verified at source),
plus a live runtime observation on 2026-09-19 (item 5's lost write, captured
during a normal convergence pass; see the silent-failure class below).
Frontend (SSE fallback poller, toast on first observation, dead `sso_launch_path`),
CLI/validation (`--app` inert, `**` glob makes "medium confidence" dead, `reset`
wipes every Podman container) and CI/hook findings (no PR gate, `check:app-http`
absent from CI, post-commit timestamp rewriting, `cli` tests absent from
precommit) are recorded there and are **not** duplicated here.

## Biggest Debt: the auth bypass was remote and forgeable — fixed 2026-09-19

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
| 5 | `internal/api/router.go:236` | `r.Use(middleware.RealIP)` — global, before the `/api` subtree |
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
`clearData`, `PUT /api/settings/hosts`, `POST /api/admin/users` (persist as a
real Authentik admin), tailnet keys, shares, remote apps. `:3000` binds every
interface (`internal/api/server.go:108`), so the direct path works too, and any
app container can reach the host gateway.

**Fix surface (measured).** `r.RemoteAddr` has exactly two consumers in all of
host-agent, both inside `isLocalRequest` (`auth_module.go:100,102`); the
forwarded-header surface is three reads total, all in the same file
(`:135` `X-Forwarded-Host`, `:161` `X-Forwarded-Proto`). Nothing else uses
client IP — no rate limiting, no source-keyed audit log. The bypass can
therefore be removed with no behavioural regression beyond the bypass itself.

**This was a shipping blocker**, and the cheapest high-impact change in the
repo. **What closed it (2026-09-19):**

- `middleware.RealIP` deleted from `NewRouter`: `r.RemoteAddr` is the real TCP
  peer again. Nothing else in host-agent consumed client IP (verified: two
  readers, both inside `isLocalRequest`).
- The trusted position is now a *scope*, not a credential: admin requires
  `Authorization: Bearer <apiToken>` **and** a trusted position, checked before
  the session path (`authMiddlewareFn`). Order matters — a position-first check
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

## Second: the appliance fails silently

Four independent defects share one property — the system stops working and
reports nothing — and item 5 (below) reaches the same end from a fifth
direction: a write that is dropped, logged at `WARN`, and otherwise ignored.
This is now a ranked class, not a footnote, because each one converts an
ordinary bug into an unnoticeable outage.

- **`SyncContainerState` nil-deref kills the process.** 
  `internal/engine/orchestrator/orchestrator_containers.go:41-45` dereferences
  the catalog result before checking the error; `MemoryCache.Get` returns
  `(nil, err)` on a miss and `ContainerDefs()` has a pointer receiver. A miss is
  reachable whenever an installed app's directory is removed or renamed
  (`catalog/loader.go:43-45` skips dirs without `metadata.yaml`). There is **no
  `recover()` anywhere in host-agent**, so the daemon dies.
- **`MemoryCache` is lock-free across a live refresh.** `internal/catalog/cache.go:12-14,26-34`
  has no mutex while orchestrator goroutines read the map from graph event
  handlers and `applyIssuerExtraHost`; `POST /api/apps/refresh-catalog` races
  them into an unrecoverable `fatal error: concurrent map read and map write`.
- **The intent loop can exit permanently.** `internal/engine/orchestrator/queue.go`
  leaves a stale `signal` token when the debounce timer wins the select; the next
  `WaitAndDrain` consumes it, `Drain()` returns `nil`, and `Start` reads that as
  shutdown (`orchestrator.go:520-526`). Every later `Submit` returns 202 and
  nothing ever reconciles.
- **The health surface cannot see any of it.** `CheckSystemHealth`
  (`internal/api/server.go:157-167`) is `db.Ping()` plus `orch == nil → nil`; a
  host with no working Podman socket boots "healthy" with no orchestrator, and a
  dead intent loop is indistinguishable from an idle one.
- **Operation-ledger writes are lost to lock contention on one row (item 5,
  re-observed 2026-09-19).** On a normal convergence pass during a redeploy,
  `recordOpPhase` → `OperationStore.AdvancePhase` lost the write:

  ```
  WARN operation recorder: phase failed  app=authentik phase=prestart
       error="advance operation authentik to prestart: database is locked (5) (SQLITE_BUSY)"
  ```

  The collision is structural, not a rare interleaving. Every phase record keys
  on `owner := o.ownerApp(id)` (`orchestrator.go:1023,1054,1066,1091`, …) — one
  row per *app* — while the scheduler dispatches *nodes*, and nodes in one
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
  (`operation_recorder.go:85`) — by design it must never change lifecycle
  behavior, which also means nothing downstream notices: the row keeps whatever
  write landed last, and since the operations row is *authoritative for failure
  context*, the dashboard's diagnostic surface can show a phase that is stale or
  not the one the drive actually reached, with no visible error. (Container nodes
  frequently write the same phase value, so many of these losses are
  value-identical — but each still takes the write lock, and where the nodes'
  phases differ the surviving value is arbitrary.) The other half of item 5 is
  worse and fully silent: `foreign_keys=OFF` on those same connections means
  cascades stop firing and orphan `shares` / `user_app_positions` rows
  accumulate with no log line at all. Fix scheduled as repayment PR 7 (pragmas
  in the DSN).

## Open inventory (ranked)

Ranked by risk×cheapness. Severity is the review's, not a guess.

| # | Item | Sev | Evidence |
|---|---|---|---|
| 1 | ~~Auth bypass via spoofable forwarding header~~ **FIXED 2026-09-19** (see above) | **P0→closed** | `router.go:236,546-550`; `traefik.go:76-80`; `auth_module.go:99-124` |
| 2 | `SyncContainerState` nil-deref → process death | P1 | `orchestrator_containers.go:41-45` |
| 3 | `MemoryCache` data race → unrecoverable fatal | P1 | `catalog/cache.go:12-14,26-34` |
| 4 | Intent queue exits permanently on a stale token | P1 | `queue.go:34-38,75-114` |
| 5 | SQLite pragmas applied per-call, not per-connection: FK cascades and `busy_timeout` are off on every pooled connection but one; tests mask it with `SetMaxOpenConns(1)`. **Observed live twice**: during the PR 4 deploy, and again 2026-09-19 on a post-PR-4 redeploy — `WARN operation recorder: … database is locked (5) (SQLITE_BUSY)` on a normal convergence pass, i.e. a phase advance is dropped with a log line as its only trace (see the silent-failure class above) | P1 | `db/db.go:29-39`; `testdb/testdb.go:29`; `operation_recorder.go:85` |
| 6 | `appclient.Call.Timeout()` is a no-op and `WaitPolicy` has no consumers → declared 5-minute first-boot waits silently run on `DefaultRetry` (30 s) and land nodes in terminal ERROR | P1 | `appclient/call.go:31,119,382`; `retry.go:43-51`; `apps/immich/api.go:33-34`; `apps/affine/api.go:31-32,58-59` |
| 7 | Home Assistant asset `SkipIf` compares the release tag to the manifest version (`"v1.2.1"` vs `"1.2.1"`, verified against the real artifact) → re-download and destructive container recreate on every full lifecycle pass | P1 | `apps/homeassistant/configurator.go:44,304-318`; `pkg/appasset/manifest.go:15-24` |
| 8 | Container drift is never repaired at runtime: the store flips to `stopped` while the in-memory graph stays `RUNNING`, so `Reconcile` never re-drives; multi-container apps are skipped entirely | P1 | `orchestrator_containers.go:41-61`; `pipeline.go:664-670` |
| 9 | `Ensure` force-removes the running container **before** pulling → a failed pull leaves the app with no container and no rollback; the recreate path also skips the `io.bloud.managed` guard `Remove` enforces | P1 | `internal/container/runtime.go:152-171` vs `:195-199` |
| 10 | ~~`GET /api/setup/status` registered twice; chi's last-registration-wins made it admin-only~~ **FIXED 2026-09-19**: `NewSetupRouter` (public, self-limiting) + single registration for `refresh-catalog`; pinned by `TestSetupRouter_IsSeparateFromAdminRouter` | closed | `router.go:269-290`; `settings_module.go:639-652` |
| 11 | Two orchestrator wirings: the CLI `reconcile` path builds a different graph shape (per-`CatalogID`) and configures no store/runtime/catalog-graph, so it reports success while doing nothing | P1 | `cmd/host-agent/configure.go:214-260` vs `internal/api/router.go:395-470` |
| 12 | Two `AppState` builders that disagree on SSO: the CLI path reads legacy `SSOBaseURL`, ignoring admin-set hosts | P2 | `orchestrator.go:1214` vs `configure.go:295` |
| 13 | An admin-selected **built-in** primary host is never persisted → primary silently reverts to `localhost` on restart, changing the OIDC issuer | P2 | `pipeline.go:~292-300`; `hostset.go:239-283` |
| 14 | System-app hiding keys off `category == "infrastructure"`, which no `metadata.yaml` sets (traefik is `network`, authentik is `security`) → both appear as installable user apps, contradicting invariant 5 | P2 | `catalog/cache.go:70-103`; `api/apps_module.go:101` |
| 15 | Sharing module and system module are wired with `nil` (tailnet node, graph, orchestrator) → `POST /api/sharing/invites` always 503 | P2 | `router.go:224-229`; `sharing_module.go:227-229` |
| 16 | `ClearAppDataIntent` is dropped by the drain switch (logged "unhandled"), and `appsModule.ClearData` is unreachable **and** targets `<appsDir>/<name>` (the catalog dir) instead of the data dir — a latent destroyer of `apps/<name>/` | P2 | `intent.go:132-142`; `pipeline.go:31-49`; `apps_module.go:209-245` |
| 17 | `sso.DeriveSecret` still contains a literal fallback secret; unreachable behind current guards, so invariant 8 now survives only by caller discipline | P2 | `sso/blueprint.go:293-297` |
| 18 | Generated Traefik YAML is hand-assembled with app names unquoted and never parsed before the write; no strict YAML decode or metadata validation at load (port, image pin, container name, SSO strategy) | P2 | `traefikgen/generator.go:44-60,216-224`; `catalog/loader.go:110-130` |
| 19 | `PlanInstall` never sets `CanInstall=false`/`Blockers`, so the consumer's blocker guard is unreachable; user integration choices are still dropped (`buildIntegrationConfig(nil, …)`) — `review.md` §H3 remains open | P2 | `pipeline.go:76-80` |
| 20 | `primaryContainerNode` = the **last** container def: an order-dependent, undocumented convention that decides inter-app edges and which node owns SSO provisioning | P2 | `pipeline.go:730-737` |
| 21 | `IconHandler` rejects `/` and `\` but not `..`, and never re-checks the joined path | P2 | `apps_module.go:262-276` |
| 22 | App networks and orphaned containers are never removed or swept; `ListContainers` is test-only | P2 | no `RemoveNetwork` in tree |
| 23 | Health checks never reach Podman; the emulated loop reads `retries` as total attempts | P2 | `internal/container/runtime.go`; orchestrator health path |

**Documented exception (not debt).** Share/guest/preference handlers write their
stores directly, bypassing the intent queue. Verified non-racing: the
orchestrator has no share/guest dependency, so nothing else writes those tables.
The dead `CreateShareIntent`/`RevokeShareIntent` types were deleted; this stays
a deliberate boundary, and **no new direct-write domain should be added**.

## Corrections to earlier "Already Paid" entries (2026-09-19)

Two claims in the history below do not hold up against the code:

- **"Persist only externally-issued artifacts"** — the direction is right, but
  the claim that integration credentials are *not* stored is already violated:
  `sso/blueprint.go:284-287` persists the HKDF-derived OAuth client secret via
  `secrets.SetAppSecret(app, "oauthClientSecret", …)`, and the field's own
  comment in `secrets/manager.go:52-53` says "derived from SSOHostSecret".
  Nothing reads it back, so today it is dead persisted state, not a split brain.
  Delete the write, or make the derivation authoritative and drop the field.
- **"Versioned migrations + the `user_app_positions` schema fork"** — the ledger
  itself is correct and worth keeping, but entry 6 cannot fire: the grid table
  shape already existed before the ledger ran, so the "fork fix" is a permanent
  no-op. The migration path is still an improvement; the described bug fix is
  not evidence of one.

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

*Still open from this area:* `SyncRoutes` errors are logged only, promotion to
RUNNING is unconditional, and the generated YAML is never validated (item 18).

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

*Correction 2026-09-19:* entry 6 is a permanent no-op in practice — see
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
  inert — item 6.)*
- Hardcoded fallback secrets: `config.Load` is fallible
  (env > `secrets.json` > error); the corrupt-file fault-downgrade is closed;
  Jellyfin's bootstrap password moved to the secrets manager.

## Revised Repayment Plan

Ranked by risk times cheapness, not by architectural ambition. The execution
plan with per-slice acceptance criteria is
[`docs/plans/tech-debt-repayment.md`](../plans/tech-debt-repayment.md) (PRs 1-3
landed; PR 4 revised, PRs 6-11 added 2026-09-19).

### 1. Close the auth bypass (P0) — DONE 2026-09-19

Shipped as described in "Biggest Debt" above, with the router-level table test
and live verification. Two deviations from the original sketch: host-agent is
**not** bound to loopback (QEMU's slirp presents forwarded connections from
`10.0.2.2`, so a loopback-only bind breaks the qemu dev loop and the CLI/e2e API
path — the credential is the control, the bind address is not); and a
per-invocation token read replaced the proposed `ReadRuntimeFile` seam, because
the CLI's curl runs host-side while the token file lives in the guest, so
`./bloud token` reads it through the existing executor instead.

### 2. Stop the silent failures (P1, items 2-4, 10)

Nil-guard the catalog lookup; give `MemoryCache` a lock or an atomic swap; make
`WaitAndDrain` distinguish "empty" from "cancelled"; register each chi pattern
exactly once. Then make the health surface able to see a dead orchestrator
(`Ready()` plus a last-convergence stamp) instead of `db.Ping()` alone.

### 3. Make declared intent real (P1, items 6-7)

Honour `Call.Timeout` and wire `WaitPolicy` as the ready-path default — or delete
both and fail loudly on an unsupported option. Fix the Home Assistant version
comparison and make its test fixture use the real manifest value.

### 4. Durability substrate (P1, item 5)

Move the pragmas into the DSN so every connection gets `foreign_keys` and
`busy_timeout`; add a test that opens 2+ connections and asserts cascade and
busy-wait actually work.

### 5. Make reality match intent (P1, items 8-9)

Reconcile container existence on every pass (or reset the node when a known
container is gone), extend `SyncContainerState` to multi-container apps, and pull
before removing in `Ensure`.

### 6. Single Orchestrator Builder (P1, items 11-12)

One builder owns the orchestrator wiring; `configure.go` and `router.go` both
call it with an explicit profile. Makes config drift between the CLI path and
the product path impossible, and collapses the duplicate `AppState` builder.
(With operation state landed, the recorder wiring is one more field that would
otherwise fork.)

### 7. Persist Only Externally-Issued Artifacts (item 18, as corrected)

Integration credentials are derived, not stored: `DeriveSecret`
(HKDF-SHA256 from the host secret) and `OIDCInputsForApp` compute client
credentials, redirect URIs, and issuer/launch URLs as a pure function of
host secret and host set. Persisting derived state creates a second source of
truth and every disagreement becomes a reconciliation bug — so delete the
`oauthClientSecret` write (and the literal fallback in `DeriveSecret`). What is
genuinely worth persisting is state we do not derive because something else
issued it: remote proxy port assignments, the tailnet domain, gateway state.

### 8. Honest surfaces and dead code (items 13-17, 19-23)

Filter system apps by `IsSystem`; persist the primary host; delete
`ClearAppDataIntent`, `appsModule.ClearData` and its vacuous 404 test, and
`NewAuthRouter`; stop trusting `category` as a system marker.

## Non-Goals

- Do not persist derived state (secrets, OIDC inputs) that can be recomputed
  from the host secret. The derivation is the source of truth.
- Do not extend trust-by-network-position, and do not add a new
  client-controlled input to an authorization decision. The P0 above is what
  that path costs.
- Do not let a subsystem fail silently: a component that can stop working must
  either be visible in the health surface or be impossible to stop.
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

Additions from the 2026-09-19 review, because the existing suite could not see
any of the P0/P1 items:

- an auth-classification table test over the **real** router tree with a
  non-loopback `RemoteAddr` (the current helper forces `127.0.0.1`, which
  auto-authenticates as admin and hides every middleware regression)
- at least one test that exercises production wiring (`NewServer` /
  `initOrchestratorHelper`), not a hand-assembled fake wiring
- multi-connection coverage for anything applied per-connection (SQLite pragmas)
- `-race` widened beyond `internal/engine/orchestrator` (the catalog cache race
  above is invisible to the current selection)

Current baseline as of this note:

```sh
cd services/host-agent && go test ./...   # pass
cd apps && go test ./...                  # pass
cd cli && go test ./...                   # red on darwin: TestResolveBackendPrecedence
```

The CLI failure is environment-dependent: `TestResolveBackendPrecedence`
(`cli/preferences_test.go:92`) expects the stored `qemu` preference, but macOS
auto-resolves to its single available backend, so it passes on the Linux CI and
fails on the platform `AGENTS.md` calls the default dev platform. It is also
invisible to the pre-commit hook, which omits cli tests. Do not paper over it by
re-pinning the test — fix the expectation for single-backend hosts.
