> Status: accepted (in progress). PRs 1-3 landed (#85 migrations, #86 operation
> state, #88 route purity); PR 7 landed 2026-09-20 (SQLite pragmas in the DSN).
> **PR 6 landed 2026-09-20** (engine silent failures: nil guard, cache lock,
> queue live-flag), with the orchestrator liveness substrate PR 11 depends on.
> **PR 4 implemented 2026-09-19** (revised first: its
> original design was insufficient against a forgeable forwarding header) and
> verified live against a deployed host-agent.

**Status:** Active debt inventory  
**Last updated:** 2026-09-25 (item 6 closed: the readiness/wait contract is
now honoured, and the wait budget is single-sourced and harness-enforced).
Prior update 2026-09-26 (reconciliation audit against the working tree).
The ranked inventory is whole again: items 5-10 are restored (the table
previously jumped from 4 to 11, hiding the still-open P1 items 6, 8, 9 and
10, plus the closed 5 and 7). Item 6's `WaitPolicy` note is corrected: it
has two explicit consumers (paperless-ngx, vaultwarden) but is still not the
ready-path default, and the Home Assistant `SkipIf` half is marked closed.
Item 24 records a catalog-entry mutation race the PR 6 mutex does not cover.
Prior update 2026-09-20 (PR 6 landed: the engine's three silent-failure
paths are closed: the catalog nil-deref is guarded, `MemoryCache` is
lock-protected with a swap-on-refresh, and the intent loop can no longer
exit on a stale signal token (`WaitAndDrain` returns a live flag; only
cancellation stops `Start`). The health surface now sees a dead loop via
`Orchestrator.Stopped()`, and `validation.yaml` runs `-race` over
`internal/catalog`. Same day: PR 7 landed: the SQLite per-connection
pragmas travel in the DSN, so every pooled connection opens with
`foreign_keys=ON` and `busy_timeout=5000`; the lost-cascade and
lost-write halves of item 5 are closed and pinned by multi-connection
tests. Same day: a configurator-layer conformance inventory was added;
its C1-C4, C8 and C13 shipped the same day, see "Closed 2026-09-20".
Prior update 2026-09-19: item 5 re-observed on a post-PR-4 redeploy, adding a fifth member to
the silent-failure class: the lost write is a `WARN` nobody sees; earlier
same-day updates: first-ranked item re-scoped and re-ranked: the loopback admin
exemption is remotely forgeable, not merely a local-process concern; a new
second-ranked class records the engine's silent-failure paths; prior update
2026-09-17: route-generation purity landed, plus versioned migrations + durable
operation state)

Source for this revision: [`docs/specs/review-2026-09-19.md`](../specs/review-2026-09-19.md)
(9 parallel subsystem audits, high-severity findings re-verified at source),
plus a live runtime observation on 2026-09-19 (item 5's lost write, captured
during a normal convergence pass; see the silent-failure class below).
Frontend (SSE fallback poller, toast on first observation, dead `sso_launch_path`),
CLI/validation (`--app` inert, `**` glob makes "medium confidence" dead, `reset`
wipes every Podman container) and CI/hook findings (no PR gate, post-commit
timestamp rewriting, `cli` tests absent from precommit) are recorded there and
are **not** duplicated here. The former `check:app-http` script was folded into
the `forbidigo` rule in `.golangci.yml` on 2026-09-20, so the raw-HTTP guard now
runs in the `fast` tier and CI instead of pre-commit only.

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
`clearData`, `PUT /api/settings/hosts`, `POST /api/admin/users` (persist as a
real Authentik admin), tailnet keys, shares, remote apps. `:3000` binds every
interface (`internal/api/server.go:108`), so the direct path works too, and any
app container can reach the host gateway.

**Fix surface (measured).** `r.RemoteAddr` has exactly two consumers in all of
host-agent, both inside `isLocalRequest` (`auth_module.go:100,102`); the
forwarded-header surface is three reads total, all in the same file
(`:135` `X-Forwarded-Host`, `:161` `X-Forwarded-Proto`). Nothing else uses
client IP: no rate limiting, no source-keyed audit log. The bypass can
therefore be removed with no behavioural regression beyond the bypass itself.

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
reports nothing), and item 5 (below) reached the same end from a fifth
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

## Open inventory (ranked)

Ranked by risk×cheapness. Severity is the review's, not a guess.
Configurator-layer consistency items are collected in the conformance section
below (C1-C14) rather than ranked here.

| # | Item | Sev | Evidence |
|---|---|---|---|
| 1 | ~~Auth bypass via spoofable forwarding header~~ **FIXED 2026-09-19** (see above) | **P0→closed** | `router.go:236,546-550`; `traefik.go:76-80`; `auth_module.go:99-124` |
| 2 | ~~`SyncContainerState` nil-deref → process death~~ **FIXED 2026-09-20 (PR 6)**: err/nil checked before the deref, catalog-miss regression tests pin the skip and the repair path | P1→closed | `orchestrator_containers.go:41-56` |
| 3 | ~~`MemoryCache` data race → unrecoverable fatal~~ **FIXED 2026-09-20 (PR 6)**: RWMutex guards all access; `Refresh` builds off-lock and swaps under the write lock; race test runs in the `fast` tier | P1→closed | `catalog/cache.go`; `catalog/cache_test.go`; `validation.yaml` (`go-host-agent-race`) |
| 4 | ~~Intent queue exits permanently on a stale token~~ **FIXED 2026-09-20 (PR 6)**: `WaitAndDrain` returns `([]Intent, bool)`; `Start` exits only on cancellation; empty live batches are skipped; liveness exposed via `Stopped()`/`LastConverged()` | P1→closed | `queue.go:77-125`; `orchestrator.go` `Start` loop + loop-liveness tests |
| 5 | ~~SQLite pragmas applied with a one-off `db.Exec` reached exactly one pooled connection; every other connection ran `foreign_keys=OFF` (cascades stop) and `busy_timeout=0` (writes fail `SQLITE_BUSY` immediately)~~ **FIXED 2026-09-20 (PR 7)**: the pragmas ride the DSN so the modernc driver applies them at every connection open; multi-connection tests pin the cascade and the busy-wait | P1→closed | `db/db.go` `pragmaQuery`/`dsn`; `db/pragmas_test.go` |
| 6 | ~~`appclient.Call.Timeout()` is a no-op: `timeoutOverride` is written and never read, so immich's and affine's declared 5-minute and 3-minute first-boot waits silently run on the 15 s client default plus `DefaultRetry`'s 30 s deadline and can land a cold-boot node in terminal ERROR. `WaitPolicy` is documented as the ready-path default but is not wired as one~~ **FIXED 2026-09-25**: `Timeout()` is now a real per-request deadline (applied per attempt, and able to extend past the client default because the client-level `http.Client.Timeout` is gone); `Ready()` now actually defaults to `WaitPolicy`; and the total wait budget moved to a new `Within(d)`, which is what the apps' long first-boot waits were always trying to say. A per-request timeout is transient, never terminal, so one slow probe cannot end a wait. `DefaultPostStartBudget` was raised to `MaxWaitBudget` (150 s cut the declared 5-minute waits short anyway). Enforced by a harness rule: no declared `Within()` may exceed `MaxWaitBudget`, and an unevaluable budget fails the check rather than skipping it | P1→closed | `pkg/appclient/call.go` (`Timeout`, `Within`); `pkg/appclient/attempt.go` (`requestContext`); `pkg/appclient/waits.go` (`Ready`, `Wait`); `pkg/appclient/retry.go` (`MaxWaitBudget`); `apps/configtest/waitbudget_test.go`; `pkg/appclient/wait_budget_test.go` |
| 7 | ~~Home Assistant's asset `SkipIf` never matched (release tag `v1.2.1` vs the manifest's bare `1.2.1`) → re-download plus destructive container recreate on every pass~~ **FIXED 2026-09-25**: the constant is now the bare-semver manifest value, and the conformance harness caught the mismatch against the real manifest | P1→closed | `apps/homeassistant/configurator.go:51` |
| 8 | Container drift is never repaired while the process is alive: `SyncContainerState` flips the store to `stopped` but leaves the in-memory node `RUNNING`, so `collectWorkForLevel` never re-drives it, and multi-container apps are skipped entirely (`len(defs) != 1`) | P1 | `orchestrator_containers.go` `SyncContainerState`; `pipeline.go` `populateGraphNodes` |
| 9 | `Ensure` force-removes the running container *before* pulling: a registry outage or digest mismatch leaves the app with no container and no rollback, and the recreate path skips `Remove`'s `io.bloud.managed` ownership guard | P1 | `internal/container/runtime.go` |
| 10 | The health surface is still blind to the runtime: a dead loop is now visible (`Stopped()`/`LastConverged()`, PR 6), but an unavailable Podman socket has no degraded signal, the startup gate is SQLite-only, and a failed system-app convergence still `os.Exit(1)`s the control plane | P1 | `internal/api/server.go` `checkSystemHealth`; `cmd/host-agent/main.go` `waitForSystemConvergence` |
| 11 | ~~Two orchestrator wirings: the CLI `reconcile` path builds a different graph shape (per-`CatalogID`) and configures no store/runtime/catalog-graph, so it reports success while doing nothing~~ **FIXED 2026-09-25**: the CLI path was deleted for having no caller at all, and `internal/wire` is now the only place a `NewOrchestrator` call happens. Guarded by a config-completeness test. | P1→closed | `cli/distro.go` era `cmd/host-agent/configure.go` (deleted); `internal/wire/wire.go`; `internal/wire/completeness_test.go` |
| 12 | ~~Two `AppState` builders that disagree on SSO: the CLI path reads legacy `SSOBaseURL`, ignoring admin-set hosts~~ **FIXED 2026-09-25**: the second builder went with the CLI path. `orchestrator.buildAppState` is the only one, and it resolves SSO through the live host set. | P2→closed | `orchestrator.go` `buildAppState` / `resolveSSOURLs` |
| 13 | An admin-selected **built-in** primary host is never persisted → primary silently reverts to `localhost` on restart, changing the OIDC issuer | P2 | `pipeline.go:~292-300`; `hostset.go:239-283` |
| 14 | System-app hiding keys off `category == "infrastructure"`, which no `metadata.yaml` sets (traefik is `network`, authentik is `security`) → both appear as installable user apps, contradicting invariant 5 | P2 | `catalog/cache.go:70-103`; `api/apps_module.go:101` |
| 15 | Sharing module and system module are wired with `nil` (tailnet node, graph, orchestrator) → `POST /api/sharing/invites` always 503 | P2 | `router.go:224-229`; `sharing_module.go:227-229` |
| 16 | `ClearAppDataIntent` is dropped by the drain switch (logged "unhandled"), and `appsModule.ClearData` is unreachable **and** targets `<appsDir>/<name>` (the catalog dir) instead of the data dir: a latent destroyer of `apps/<name>/` | P2 | `intent.go:132-142`; `pipeline.go:31-49`; `apps_module.go:209-245` |
| 17 | `sso.DeriveSecret` still contains a literal fallback secret; unreachable behind current guards, so invariant 8 now survives only by caller discipline | P2 | `sso/blueprint.go:293-297` |
| 18 | Generated Traefik YAML is hand-assembled with app names unquoted and never parsed before the write; no strict YAML decode or metadata validation at load (port, image pin, container name, SSO strategy) | P2 | `traefikgen/generator.go:44-60,216-224`; `catalog/loader.go:110-130` |
| 19 | `PlanInstall` never sets `CanInstall=false`/`Blockers`, so the consumer's blocker guard is unreachable; user integration choices are still dropped (`buildIntegrationConfig(nil, …)`); `review.md` §H3 remains open | P2 | `pipeline.go:76-80` |
| 20 | `primaryContainerNode` = the **last** container def: an order-dependent, undocumented convention that decides inter-app edges and which node owns SSO provisioning | P2 | `pipeline.go:730-737` |
| 21 | `IconHandler` rejects `/` and `\` but not `..`, and never re-checks the joined path | P2 | `apps_module.go:262-276` |
| 22 | App networks and orphaned containers are never removed or swept; `ListContainers` is test-only | P2 | no `RemoveNetwork` in tree |
| 23 | Health checks never reach Podman; the emulated loop reads `retries` as total attempts | P2 | `internal/container/runtime.go`; orchestrator health path |
| 24 | `appsModule.GetCatalog` mutates cached catalog entries: it writes `app.EstimatedSizeMB` on the `*catalog.App` structs `GetUserApps` returns, which are the same pointers the cache map holds. PR 6's RWMutex guards the map, not the pointed-to structs, so concurrent `GET /api/apps` requests race on that field (and the catalog is no longer purely disk-driven) | P2 | `catalog/cache.go` `GetAll`; `api/apps_module.go` `GetCatalog` |

**Documented exception (not debt).** Share/guest/preference handlers write their
stores directly, bypassing the intent queue. Verified non-racing: the
orchestrator has no share/guest dependency, so nothing else writes those tables.
The dead `CreateShareIntent`/`RevokeShareIntent` types were deleted; this stays
a deliberate boundary, and **no new direct-write domain should be added**.

## Configurator-layer conformance debt (2026-09-20)

The app configurator surface is thirteen user apps under `apps/` plus the
system apps under `internal/appconfig/`. It follows a strong shared contract;
the items below are where implementations drifted from it. As of 2026-09-25
the contract is enforced by a conformance harness rather than by convention,
so the drift listed here is closed and a new app cannot reintroduce it.
The severity labels here are assigned by this note (P2 = hides behavior or
breaks a stated invariant; P3 = consistency and duplication), not taken from a
dated review.

**Closed 2026-09-20:** C1, C2, C4 (except its `templateVars` side channel,
now C15), C8 and C13. **Closed 2026-09-25:** C3, C5, C6, C7, C10, C11, C14,
C15. Still open: C9. See "Closed 2026-09-25" below.

### The surface

Thirteen user apps plus the system apps. Every row is covered by the
conformance harness (`apps/conformance_test.go`), which enumerates the
registry rather than this table, so the table cannot hide an app.

| App | Graph node | SSO strategy | Implementation |
|---|---|---|---|
| jellyfin | `apps-jellyfin` | ldap | `apps/jellyfin/` |
| navidrome | `apps-navidrome` | forward-auth | `apps/navidrome/` |
| immich | `apps-immich-server` | native-oidc | `apps/immich/` |
| affine | `apps-affine` | native-oidc | `apps/affine/` |
| paperless-ngx | `apps-paperless-ngx` | native-oidc | `apps/paperless-ngx/` |
| homeassistant | `apps-homeassistant` | native-oidc | `apps/homeassistant/` |
| hermes | `apps-hermes` | native-oidc (loopback issuer) | `apps/hermes/` |
| vaultwarden | `apps-vaultwarden` | native-oidc | `apps/vaultwarden/` |
| sonarr | `apps-sonarr` | forward-auth | `apps/sonarr/` (thin over `pkg/servarr`) |
| radarr | `apps-radarr` | forward-auth | `apps/radarr/` (thin over `pkg/servarr`) |
| prowlarr | `apps-prowlarr` | forward-auth | `apps/prowlarr/` |
| qbittorrent | `apps-qbittorrent` | forward-auth | `apps/qbittorrent/` |
| seerr | `apps-seerr` | forward-auth | `apps/seerr/` |
| authentik | `apps-authentik-server` | system | `apps/authentik/` + `internal/appconfig/register.go` |
| traefik | `apps-traefik` | system | `internal/appconfig/traefik.go`; `apps/traefik/` is metadata only |

### Conventions the surface follows

- Declarative and imperative split: `metadata.yaml` owns containers, volumes,
  ports, health checks, the `dependsOn` DAG, and `sso.strategy`/`callbackPath`;
  the configurator owns only what cannot be static
  (`pkg/configurator/interface.go`).
- A single node contract: `NodeLifecycle` = `Name`/`PreStart`/`PostStart`, all
  idempotent. Teardown is the optional `Remover` interface. `PreStart` returns
  `changed`, and the orchestrator removes and recreates the container when it is
  true (`orchestrator.go:1070`).
- Self-registration: each app's `registration.go` `init()` calls
  `MustRegisterFactory`; `apps/registry.go` holds the import list and
  `NodeNames()`, guarded by `TestRegisterAll`. System apps (Traefik, Authentik)
  register as lazy factories from `internal/appconfig/register.go`.
- A typed `api.go` per app over `pkg/appclient`; no raw HTTP in a configurator.
- Host services injected through `Deps` (secrets, `PrimaryBaseURL` as a
  function, `TraefikPort`, `RestartContainer`, `HTTP`, `Assets`), never global
  state.
- Config-file generation as render, compare bytes, write on change, return
  `changed`; deterministic rendering so an unchanged config does not rewrite
  itself on every reconciliation pass.
- An internal bootstrap account whose password comes from
  `Secrets.GenerateAppAdminPassword`, with a login fast path before a first-run
  create.
- Declared idempotency (`AlreadyDone`, `AlreadyDoneFunc`, `Ensure`) and
  declared readiness (`Ready(...).WithRetry(...).Wait(ctx)`).
- SSO knowledge split at the provider and consumer boundary: `internal/sso`
  derives the client credentials and redirect URIs and provisions Authentik;
  the configurator only consumes the typed `LDAPOutput`/`OIDCOutput` in
  `AppState`.

### Gaps

| # | Item | Sev | Evidence |
|---|---|---|---|
| C1 | ~~No shared config-file helper; four apps hand-rolled `os.ReadFile` + `bytes.Equal` + `os.WriteFile`~~ **FIXED 2026-09-20**: affine, immich, paperless and authentik now use `managedfile.Write`, the existing atomic write-if-changed helper. | P3→closed | `apps/affine/configurator.go`; `apps/immich/configurator.go`; `apps/paperless-ngx/configurator.go`; `apps/authentik/server_configurator.go` |
| C2 | ~~`appExternalURL` duplicated between affine and paperless~~ **FIXED 2026-09-20**: both call `configurator.AppExternalURL`. | P3→closed | `services/host-agent/pkg/configurator/urls.go` |
| C3 | ~~`Remove` a no-op in all six user apps; `BaseNodeLifecycle` and the deprecated aliases had zero callers~~ **FIXED 2026-09-20**: teardown is the optional `configurator.Remover`; the six no-op `Remove` methods are deleted with `base.go`, and the orchestrator type-asserts before calling it. | P3→closed | `services/host-agent/pkg/configurator/interface.go`; `orchestrator.go:614,635` |
| C4 | ~~authentik's `ServerConfigurator` bypassed the container runtime and the `Deps` injection~~ **FIXED 2026-09-20**: built from `Deps` (logger, `HTTP`, `Exec`), runs `ak shell` through `Deps.Exec` with no hardcoded podman, and registered as a lazy factory. The `templateVars` side channel remains as its own item (C15). | P2→closed | `apps/authentik/server_configurator.go`; `services/host-agent/internal/appconfig/register.go` |
| C5 | ~~Phase boundaries leak. Home Assistant runs a live network probe and can force a container recreate from `PreStart` (`staleForce`), and patches a disk file plus restarts the container from `PostStart`.~~ **FIXED 2026-09-25**: the probe stays in `PreStart` on purpose. A `PostStart` probe cannot tell "not installed" from "restarting", and moving it would delay the self-heal by a full cycle. What changed is the channel: the recreate request is now a typed signal with a reason instead of a bare bool leaking out of a config-diff. | P2→closed | `apps/homeassistant/configurator.go`; `services/host-agent/pkg/configurator/interface.go` |
| C6 | ~~The `changed` return conflates "a mounted file changed" with "recreate the container".~~ **FIXED 2026-09-25**: `PreStart` returns `PreStartResult{RestartNeeded, Reason}`. The field is named for the orchestrator's side effect, so a self-heal signal and a config diff can no longer be mistaken for each other, and every recreate is traceable to the reason that caused it. Deliberately no `ConfigWritten` field: writing a file is not what the orchestrator acts on. | P3→closed | `services/host-agent/pkg/configurator/interface.go`; `orchestrator.go` |
| C7 | ~~Bootstrap-account logic is copy-pasted with divergent names and failure semantics.~~ **FIXED 2026-09-25**: `pkg/bootstrap` owns the login-fast-path / create / verify sequence and one policy: an unverifiable admin is a reported condition (`Outcome`), never a terminal error, because SSO users do not depend on the internal account. Navidrome, Immich and Paperless-ngx map their APIs onto two callbacks and carry no policy of their own. The account names stay as shipped: renaming would orphan the credential in every existing install. | P3→closed | `services/host-agent/pkg/bootstrap/bootstrap.go`; `apps/navidrome/configurator.go`; `apps/immich/configurator.go`; `apps/paperless-ngx/configurator.go` |
| C8 | ~~Transient-versus-terminal `PostStart` error handling inconsistent and undocumented~~ **FIXED 2026-09-20**: the contract is stated on `NodeLifecycle.PostStart` (an error is terminal; resolve transients inside). jellyfin's initial-user wait is now best-effort instead of returning a transient error, and affine's unreachable `return ""` is a real error return. | P2→closed | `services/host-agent/pkg/configurator/interface.go`; `apps/jellyfin/api.go`; `apps/affine/configurator.go` |
| C9 | Test coverage is uneven for the same contract: none for authentik or traefik; immich is 1.7 KB against jellyfin's 35 KB and Home Assistant's 31 KB. | P3 | app package listings |
| C10 | ~~Cross-file constants are enforced only by comment: the constructor's port default must equal `metadata.yaml`'s `port`, and nothing pinned any of them.~~ **FIXED 2026-09-25**: the conformance harness reads each `metadata.yaml` and asserts the configurator's default port matches, alongside name, idempotency, offline-`PreStart`, empty-`Deps` and honest-teardown checks. It caught two wrong port guesses (Hermes 9119, qBittorrent 8081) and one live version bug on its first run. | P3→closed | `apps/configtest/configtest.go`; `apps/conformance_test.go` |
| C11 | ~~Registration style drifts: Home Assistant registers `NewConfigurator(8123, deps)` while every other app passes `0`.~~ **FIXED 2026-09-25**: every app declares one `nodeName` constant used by both `Name()` and `registration.go`, passes `0` for the port, and keeps its default as a named constant the harness checks against metadata. | P3→closed | `apps/*/registration.go`; `apps/configtest/configtest.go` |
| C12 | ~~Cross-app coupling through a file path: navidrome's user sync reads `<dataDir>/authentik/api-token`, written by authentik's server configurator, and the two agree only because `ownerApp("apps-authentik-server")` is `authentik`.~~ **FIXED 2026-09-21**: authentik declares `provides.sso.secrets: [apiToken]` and publishes the token through `SetAppSecret`; navidrome reads it from its `sso` integration binding, so no app path name crosses a boundary. The same change removed the Servarr sibling reads (`pkg/servarr.SiblingConfigPath`, deleted) and the consumer-side provider port constants. See invariant 15. | P3→closed | `apps/navidrome/configurator.go` (`authentikToken`); `apps/authentik/server_configurator.go`; `services/host-agent/pkg/configurator/interface.go` |
| C13 | ~~Comment residue in `apps/jellyfin/network_config.go`: duplicated doc comments and an orphaned `Remove` comment~~ **FIXED 2026-09-20**. | P3→closed | `apps/jellyfin/network_config.go` |
| C14 | ~~File modes are ad hoc with per-app rationale and no policy for a new app to follow.~~ **FIXED 2026-09-25**: `managedfile.ModeHostOnly` (0600) and `managedfile.ModeSharedConfig` (0644), named by who has to read the file under rootless Podman rather than by a magic number. Every call site uses a named mode. | P3→closed | `services/host-agent/pkg/managedfile/write.go` |
| C15 | ~~The LDAP outpost token reaches the orchestrator through a shared mutable `templateVars` map.~~ **FIXED 2026-09-25**: `configurator.TemplateVars` is a typed store. Static values are copied at construction, the token is written through a named `SetLDAPOutpostToken`, an `RWMutex` guards it, and `Snapshot()` hands the renderer a private copy. Tested for copy semantics, snapshot isolation, nil-store safety and concurrent write/snapshot. | P3→closed | `services/host-agent/pkg/configurator/templatevars.go` |

### Highest-value consolidations (shipped 2026-09-20)

All four landed in one change:

1. `managedfile.Write` (already the atomic write-if-changed helper) now backs
   affine, immich, paperless and authentik, and `configurator.AppExternalURL`
   replaces the duplicated host-URL derivation in affine and paperless (C1, C2).
2. `Remove` left the app contract: it is the optional `configurator.Remover`,
   the six no-op implementations and `pkg/configurator/base.go` are deleted, and
   the orchestrator type-asserts before calling it (C3).
3. authentik's `ServerConfigurator` is built from `Deps` (logger, `HTTP`,
   `Exec`), runs `ak shell` through the host runtime instead of a hardcoded
   `podman exec`, and registers as a lazy factory (C4; the `templateVars` side
   channel is C15).
4. The `PostStart` error contract is stated once on the interface, jellyfin's
   initial-user wait tolerates a transient not-ready, and affine's unreachable
   branch returns a real error (C8).

### Closed 2026-09-25

The whole configurator-conformance list (C3, C5, C6, C7, C10, C11, C14, C15)
closed in one branch, with the enforcement built before the fixes so the fix
list was a verified inventory rather than a recollection.

**The harness came first.** `apps/configtest` runs six assertions per
registered app, table-driven over the registry, so a new app is covered the
moment it registers: `Name()` matches the registered node; `PreStart` makes
no network call; `PreStart` converges so a second pass asks for no recreate;
a configurator survives empty `Deps`; a `Remover` implementation must
actually own teardown; and the constructor's default port equals the port
`metadata.yaml` publishes. 78 subtests, one per app per assertion.

It paid for itself immediately. Home Assistant compared the `hass-oidc-auth`
manifest against `"v1.2.1"` while the shipped manifest reports `"1.2.1"`, so
the skip never matched and **every reconciliation pass re-downloaded the
component and requested a container recreate**. The existing unit test carried
the same wrong version as the code, which is why the test passed and the bug
did not. The harness used the real manifest and caught it.

**What landed:**

- `PreStartResult{RestartNeeded, Reason}` replaces the bare bool (C5, C6).
  The field is named for the orchestrator's side effect, so a self-heal
  signal and a config diff cannot be mistaken for each other.
- The seven no-op `Remove` methods are deleted. A method that returns `nil`
  while the orchestrator already owns teardown is a lie about who owns
  teardown, and the harness now refuses the lie (C3).
- One registration shape: a single `nodeName` constant feeds both `Name()`
  and `registration.go`, every app passes `0` for the port, and the default
  is a named constant checked against metadata (C10, C11).
- `configurator.TemplateVars` replaces the shared mutable map: static values
  copied at construction, the LDAP outpost token behind a named setter, an
  `RWMutex` guarding it, `Snapshot()` returning a private copy (C15).
- `managedfile.ModeHostOnly` and `ModeSharedConfig` replace magic modes,
  named by who has to read the file under rootless Podman (C14).
- `pkg/servarr.PVRConfigurator` is one lifecycle parameterised by `PVRApp`.
  Sonarr and Radarr were 394 lines each and byte-identical apart from nine
  values; they are now 45 lines each over one shared implementation.
- `pkg/bootstrap` owns the admin-account sequence and one failure policy:
  an unverifiable admin is reported, never terminal (C7).

**What deliberately did not change.** The Home Assistant stale-trust probe
stays in `PreStart`. Moving it to `PostStart` would put a probe on the far
side of the container start, where it cannot separate "not installed" from
"restarting", and would delay the self-heal by a full cycle. Only its
reporting channel changed. The bootstrap account names stay as shipped: a
rename would orphan the credential in every existing install for no gain the
code could collect.

Still open: C9 (test coverage is uneven per app). The harness closes the
floor on that, since every app now gets six checks it did not choose, but
the depth gap between, say, Immich and Jellyfin remains.

### Closed 2026-09-20

One change consolidated the configurator surface as described above, and added
the enforcement the surface previously lacked: a `forbidigo` rule in
`.golangci.yml` fails any `os.WriteFile`/`os.Create`/`os.CreateTemp`/
`os.OpenFile`/`ioutil.*`, `exec.Command(Context)`, or raw `net/http` in
`apps/**/*.go` (excluding tests), pointing at `pkg/managedfile.Write`,
`Deps.Exec` and `pkg/appclient`. It replaces `scripts/no-adhoc-http.mjs`, so the
raw-HTTP guard now runs under `lint:go` (in the `fast` tier and CI) instead of
pre-commit only. `apps/authentik` writes its token through `managedfile.Write`;
`apps/immich`'s marker that is written only when absent carries one
`//nolint:forbidigo // reason`. New tests:
`pkg/configurator/urls_test.go`, `apps/authentik/server_configurator_test.go`,
and the orchestrator case proving a configurator with no `Remover` still has
its node deleted on removal. Verified with `go test ./...` in all three Go
modules, `npm run lint:go` (0 issues) and gofmt.

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

## Already Paid

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
- a fresh connection rejects an orphan `shares` insert and the
  `guests` DELETE actually cascades to the dependent share;
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

## Revised Repayment Plan

Ranked by risk times cheapness, not by architectural ambition. The execution
plan with per-slice acceptance criteria is
[`docs/plans/tech-debt-repayment.md`](../plans/tech-debt-repayment.md) (PRs 1-3
landed; PR 4 revised, PRs 6-11 added 2026-09-19).

### 1. Close the auth bypass (P0): DONE 2026-09-19

Shipped as described in "Biggest Debt" above, with the router-level table test
and live verification. Two deviations from the original sketch: host-agent is
**not** bound to loopback (QEMU's slirp presents forwarded connections from
`10.0.2.2`, so a loopback-only bind breaks the qemu dev loop and the CLI/e2e API
path: the credential is the control, the bind address is not); and a
per-invocation token read replaced the proposed `ReadRuntimeFile` seam, because
the CLI's curl runs host-side while the token file lives in the guest, so
`./bloud token` reads it through the existing executor instead.

### 2. Stop the silent failures (P1, items 2-4, 10): DONE 2026-09-20 (PR 6)

Nil-guarded the catalog lookup; `MemoryCache` is lock-protected with an
off-lock build + swap-on-refresh; `WaitAndDrain` distinguishes "empty"
from "cancelled" and `Start` exits only on cancellation. The health
surface can now see a dead orchestrator (`Stopped()` + `LastConverged()`
feed `CheckSystemHealth`); the rest of PR 11's degraded-payload /
startup-gate scope is still open.

### 3. Make declared intent real (P1, items 6-7): DONE 2026-09-25

Both halves are closed. The Home Assistant half landed first:
`oidcComponentVersion` now carries the bare-semver manifest value (`1.2.1`),
so `SkipIf` matches and the per-pass re-download plus destructive container
recreate are gone (the conformance harness caught the mismatch against the
real manifest).

The appclient half closed the same day. `Call.Timeout` is honoured as a
per-request deadline, `Ready()` defaults to `WaitPolicy` as its own doc
always claimed, and the total wait budget is a distinct `Within(d)`. The
choice between "honour it" and "delete it and fail loudly" turned out not to
be binary: honouring `Timeout` needed a second method anyway, because
"how long one probe may take" and "how long the whole wait may take" are
different numbers and the old single method was being used to mean the
second one.

The failure this was preventing is not a slow boot, it is a permanently dead
app: a wait that gives up at 30 s returns an error from `PostStart`, the
node goes to `ERROR`, and `collectWorkForLevel` skips `ERROR` nodes forever
("ERROR is terminal: never retry without an explicit status reset"). Immich,
AFFiNE and Hermes all declared a five-minute budget that did nothing about
it. The ceiling is now single-sourced (`MaxWaitBudget` == the orchestrator's
`DefaultPostStartBudget`) and the harness refuses a declared wait that could
not run.

### 4. Durability substrate (P1, item 5): DONE 2026-09-20 (PR 7)

Pragmas moved into the DSN; the `Exec` loop deleted. Tests open the
real `InitDB` path with 2+ connections and assert the cascade fires
and the contended write waits. See "Already Paid" above.

### 5. Make reality match intent (P1, items 8-9)

Reconcile container existence on every pass (or reset the node when a known
container is gone), extend `SyncContainerState` to multi-container apps, and pull
before removing in `Ensure`.

### 6. Single Orchestrator Builder (P1, items 11-12): DONE 2026-09-25

One builder owns the orchestrator wiring: `internal/wire`. `main.go` is the
composition root; it builds the shared stores, the auth ref, and the
orchestrator, starts the intent loop under its own cancellable context, and
hands the result to the API. The API cannot construct an orchestrator at all.

**The profiled approach in the original plan was replaced by deletion, and
the reason is worth keeping.** The plan said `configure.go` and `router.go`
should both call one builder with an explicit profile. That framing is
accurate about the code and wrong about the action, because it never asked
whether the second site was reachable. It was not: a repo-wide search for
`bloud-agent` found only the deleted file's own usage strings, and the path
looked configurators up by catalog ID while the registry keys on graph node
names, so it configured nothing even if invoked. You cannot unify two copies
of nothing. Before unifying anything with a profile, ask who calls the second
branch.

Three guards keep it from forking again:

- a completeness test reflects over the orchestrator config after a build and
  fails on any field that is neither populated nor allowlisted with a reason,
  and fails an allowlist entry that guards nothing
- a node-shape test asserts every node the catalog planner creates for a
  non-system app has a registered configurator, and that the registry's
  declared list has no extras
- the API has no construction path: no implicit fallback, no opt-out flag

The duplicate `AppState` builder went with the CLI path, so the orchestrator's
own builder is the only answer to "what is this app's OIDC client", and it
resolves through the live host set rather than the legacy single-host string.

### 7. Persist Only Externally Issued Artifacts (item 17, as corrected)

Integration credentials are derived, not stored: `DeriveSecret`
(HKDF-SHA256 from the host secret) and `OIDCInputsForApp` compute client
credentials, redirect URIs, and issuer/launch URLs as a pure function of
host secret and host set. Persisting derived state creates a second source of
truth and every disagreement becomes a reconciliation bug, so delete the
`oauthClientSecret` write (and the literal fallback in `DeriveSecret`). What is
genuinely worth persisting is state we do not derive because something else
issued it: remote proxy port assignments, the tailnet domain, gateway state.

### 8. Honest surfaces and dead code (items 13-16, 18-23)

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
  pure config step; keep it that way: see the `route_sync_test.go` contract.)

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
re-pinning the test; fix the expectation for single-backend hosts.
