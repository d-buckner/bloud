> Status: accepted (in progress). The per-slice plan is
> [`docs/plans/tech-debt-repayment.md`](../plans/tech-debt-repayment.md).
> PRs 1-4 and 6-9 landed; PR 5 landed as a deletion rather than as a unification
> (see "Single Orchestrator Builder" below); PR 11 landed in part, and its
> degraded-payload and startup-gate scope is the open item 10.

**Status:** Active debt inventory  
**Last updated:** 2026-10-15, re-verified against `main` at `e53a817`.  
This pass re-verified every open entry against the tree and re-scoped ten of them.
Item 27 closed on the orchestrator half and stays open on `wire.Input`. Items 14,
16, 22, 23, and 36 each had one half closed and now say which half remains. Items
17, 20, 21, 29, 31, and 35 had stale citations, stale counts, or an unstated
condition, now corrected. Most of the app-extension surface findings the
2026-10-12 review raised are repaid: findings 3.4, 3.5, and 3.6 are closed, and
3.1, 3.2, and 3.3 are closed on the half the review itself scheduled first. See
"The app-SDK became a module".

**How to read the dates.** Each dated note records what was true when it was
written, and every closed item names the change that closed it. The long record of
closed items is in [`archive/tech-debt-history.md`](archive/tech-debt-history.md);
revision chatter is not kept at all, because `git log` holds it.

| Revised | What changed |
|---|---|
| 2026-10-15 | Item 34 closed: `operations` is keyed `(app_name, node)`. Item 27 half-closed by the app-SDK extraction. Items 14, 16, 22, 23 re-scoped after a source re-read. |
| 2026-10-04 | Design-debt repayment began: items 26, 28, 29, 30, and 32 closed (issues #203-#206); item 31 deferred with a recorded reason. |
| 2026-10-03 | Design and complexity findings from the APoSD review folded in as items 26-34: [`review-2026-10-03-aposd.md`](../specs/review-2026-10-03-aposd.md). |
| 2026-09-30 | Item 25 closed: the reconciler runs on a self-healing timer, so convergence no longer waits on somebody submitting an intent, and `retryable` on the operation row finally has a driver. |
| 2026-09-27 | Items 8 and 9 closed: drift is written into the graph node, not only into the store, and `Ensure` pulls before it destroys. Plan archived at [`docs/plans/archive/reality-match-intent.md`](../plans/archive/reality-match-intent.md). |
| 2026-09-25 | Item 6 closed: the readiness and wait contract is honored, and the wait budget is single-sourced and harness-enforced. The configurator-conformance list closed in the same branch. |
| 2026-09-20 | PRs 6 and 7 landed: the engine's three silent-failure paths are closed, and the SQLite pragmas travel in the DSN. The configurator-layer conformance inventory was added. |
| 2026-09-19 | The P0 re-scoped: the loopback admin exemption is remotely forgeable, not merely a local-process concern. Item 5 re-observed on a post-PR-4 redeploy. Source: [`review-2026-09-19.md`](../specs/review-2026-09-19.md), 9 parallel subsystem audits with high-severity findings re-verified at source, plus one live runtime observation. |

Three groups of findings from the 2026-09-19 review are recorded there and are
**not** duplicated here: the frontend (SSE fallback poller, toast on first
observation, dead `sso_launch_path`), the CLI and validation layer (`--app`
inert, the `**` glob making "medium confidence" dead, `reset` wiping every
Podman container), and CI and hooks (no PR gate, post-commit timestamp
rewriting, `cli` tests absent from pre-commit). The former `check:app-http`
script moved into the `forbidigo` rule in `.golangci.yml` on 2026-09-20, so the
raw-HTTP guard runs in the `fast` tier and in CI instead of in pre-commit only.

## Open inventory (ranked)

Ranked by risk×cheapness. Severity is the review's where a dated review set it, and
this ledger's own judgement where it did not; an entry that this ledger re-scopes
says so and gives the reason.
Configurator-layer consistency items are collected in the conformance section
below (C1-C15) rather than ranked here.

| # | Item | Sev | Evidence |
|---|---|---|---|
| 1 | ~~Auth bypass via spoofable forwarding header~~ **FIXED 2026-09-19** (see [the history](archive/tech-debt-history.md)) | **P0→closed** | `router.go:236,546-550`; `traefik.go:76-80`; `auth_module.go:99-124` |
| 2 | ~~`SyncContainerState` nil-deref → process death~~ **FIXED 2026-09-20 (PR 6)**: err/nil checked before the deref, catalog-miss regression tests pin the skip and the repair path | P1→closed | `orchestrator_containers.go:41-56` |
| 3 | ~~`MemoryCache` data race → unrecoverable fatal~~ **FIXED 2026-09-20 (PR 6)**: RWMutex guards all access; `Refresh` builds off-lock and swaps under the write lock; race test runs in the `fast` tier | P1→closed | `catalog/cache.go`; `catalog/cache_test.go`; `validation.yaml` (`go-host-agent-race`) |
| 4 | ~~Intent queue exits permanently on a stale token~~ **FIXED 2026-09-20 (PR 6)**: `WaitAndDrain` returns `([]Intent, bool)`; `Start` exits only on cancellation; empty live batches are skipped; liveness exposed via `Stopped()`/`LastConverged()` | P1→closed | `queue.go:77-125`; `orchestrator.go` `Start` loop + loop-liveness tests |
| 5 | ~~SQLite pragmas applied with a one-off `db.Exec` reached exactly one pooled connection; every other connection ran `foreign_keys=OFF` (cascades stop) and `busy_timeout=0` (writes fail `SQLITE_BUSY` immediately)~~ **FIXED 2026-09-20 (PR 7)**: the pragmas ride the DSN so the modernc driver applies them at every connection open; multi-connection tests pin the cascade and the busy-wait | P1→closed | `db/db.go` `pragmaQuery`/`dsn`; `db/pragmas_test.go` |
| 6 | ~~`appclient.Call.Timeout()` is a no-op: `timeoutOverride` is written and never read, so immich's and affine's declared 5-minute and 3-minute first-boot waits silently run on the 15 s client default plus `DefaultRetry`'s 30 s deadline and can land a cold-boot node in terminal ERROR. `WaitPolicy` is documented as the ready-path default but is not wired as one~~ **FIXED 2026-09-25**: `Timeout()` is now a real per-request deadline (applied per attempt, and able to extend past the client default because the client-level `http.Client.Timeout` is gone); `Ready()` now actually defaults to `WaitPolicy`; and the total wait budget moved to a new `Within(d)`, which is what the apps' long first-boot waits were always trying to say. A per-request timeout is transient, never terminal, so one slow probe cannot end a wait. `DefaultPostStartBudget` was raised to `MaxWaitBudget` (150 s cut the declared 5-minute waits short anyway). Enforced by a harness rule: no declared `Within()` may exceed `MaxWaitBudget`, and an unevaluable budget fails the check rather than skipping it | P1→closed | `pkg/appclient/call.go` (`Timeout`, `Within`); `pkg/appclient/attempt.go` (`requestContext`); `pkg/appclient/waits.go` (`Ready`, `Wait`); `pkg/appclient/retry.go` (`MaxWaitBudget`); `apps/configtest/waitbudget_test.go`; `pkg/appclient/wait_budget_test.go` |
| 7 | ~~Home Assistant's asset `SkipIf` never matched (release tag `v1.2.1` vs the manifest's bare `1.2.1`) → re-download plus destructive container recreate on every pass~~ **FIXED 2026-09-25**: the constant is now the bare-semver manifest value, and the conformance harness caught the mismatch against the real manifest | P1→closed | `apps/homeassistant/configurator.go:51` |
| 8 | ~~Container drift is never repaired while the process is alive: `SyncContainerState` flips the store to `stopped` but leaves the in-memory node `RUNNING`, so `collectWorkForLevel` never re-drives it, and multi-container apps are skipped entirely (`len(defs) != 1`)~~ **FIXED 2026-09-27**: a node at `RUNNING` whose container is not running is reset to `INITIALIZING`, so `target != actual` and the normal path re-drives it in the same pass. The multi-container skip is gone; every declared container is inspected and each node is repaired independently. `ERROR` nodes are not touched (terminal by design), and uninstalling apps are excluded so drift repair cannot resurrect an app being removed | P1→closed | `orchestrator_containers.go` `SyncContainerState` / `repairDriftedNode`; `orchestrator_containers_test.go` |
| 9 | ~~`Ensure` force-removes the running container *before* pulling: a registry outage or digest mismatch leaves the app with no container and no rollback, and the recreate path skips `Remove`'s `io.bloud.managed` ownership guard~~ **FIXED 2026-09-27**: the order is now `guard → pull → remove → create → start`, pinned by an explicit call-sequence assertion. The ownership check is extracted to one `isManaged` predicate that both destructive paths call, so no path can forget it | P1→closed | `internal/container/runtime.go` `Ensure` / `isManaged`; `runtime_test.go` |
| 10 | The health surface is still blind to the runtime: a dead loop is now visible (`Stopped()`/`LastConverged()`, PR 6), but an unavailable Podman socket has no degraded signal, the startup gate is SQLite-only, and a failed system-app convergence still `os.Exit(1)`s the control plane | P1 | `internal/api/server.go` `checkSystemHealth`; `cmd/host-agent/main.go` `waitForSystemConvergence` |
| 11 | ~~Two orchestrator wirings: the CLI `reconcile` path builds a different graph shape (per-`CatalogID`) and configures no store/runtime/catalog-graph, so it reports success while doing nothing~~ **FIXED 2026-09-25**: the CLI path was deleted for having no caller at all, and `internal/wire` is now the only place a `NewOrchestrator` call happens. Guarded by a config-completeness test. | P1→closed | `cli/distro.go` era `cmd/host-agent/configure.go` (deleted); `internal/wire/wire.go`; `internal/wire/completeness_test.go` |
| 12 | ~~Two `AppState` builders that disagree on SSO: the CLI path reads legacy `SSOBaseURL`, ignoring admin-set hosts~~ **FIXED 2026-09-25**: the second builder went with the CLI path. `orchestrator.buildAppState` is the only one, and it resolves SSO through the live host set. | P2→closed | `orchestrator.go` `buildAppState` / `resolveSSOURLs` |
| 13 | ~~An admin-selected **built-in** primary host is never persisted → primary silently reverts to `localhost` on restart, changing the OIDC issuer~~ **CLOSED 2026-09-26 by the model change**: there is no primary selection any more. The address is one URL stored at `settings['public_url']`, and whatever the operator typed is what gets persisted and read back, so there is no built-in-versus-stored case left to get wrong. | P2→closed | `store/settings.go`; `orchestrator/pipeline.go` `applySetPublicURLIntent` |
| 14 | System-app hiding still keys off `category == "infrastructure"`, and no `metadata.yaml` sets it (traefik is `network`, authentik is `security`), so `GET /api/apps` still lists both system apps. The dashboard hides them with its own filter, so the user-visible half of the invariant-5 contradiction is gone and the server-side half is not: the API contract is enforced by the client that happens to ship with it. The fix is to read `App.IsSystem`, which both system apps already declare. **Half closed 2026-10-15** | P2 | `catalog/cache.go` `SystemCategories`/`IsSystemApp`/`GetUserApps`; `web/src/lib/clients/catalog.ts` `fetchCatalog`; `apps/traefik/metadata.yaml` |
| 15 | ~~Sharing module and system module are wired with `nil` (tailnet node, graph, orchestrator) → `POST /api/sharing/invites` always 503~~ **CLOSED 2026-10-05 by removal**: the sharing subsystem never worked end to end and was deleted from the product rather than repaired. The design notes are archived in `docs/plans/archive/` | P2→closed | `internal/sharing/` (deleted) |
| 16 | `ClearAppDataIntent` is a dead intent: nothing submits it, and the drain switch still has no arm for it, so a submitter would get a `WARN` and no effect. The destructive half of this entry is **closed**: `appsModule.ClearData` now removes `dirs.AppDataDir(dataDir, name)` (the writable tree under `BLOUD_DATA_DIR`), never `<appsDir>/<name>`, so it cannot delete a catalog directory. What is left is a dead type plus an intent the queue can carry and the engine cannot honour: delete the intent, or give it an arm | P3 (was P2) | `intent.go` `ClearAppDataIntent`; `pipeline.go` `applyIntents` default arm; `apps_module.go` `ClearData`; `internal/dirs` |
| 17 | `sso.DeriveSecret` still contains a literal fallback secret (`return context + "-fallback-secret"` when the master secret is empty); unreachable behind current guards, so invariant 8 now survives only by caller discipline | P2 | `internal/sso/blueprint.go` `DeriveSecret` |
| 18 | Generated Traefik YAML is hand-assembled with app names unquoted and never parsed before the write; no strict YAML decode or metadata validation at load (port, image pin, container name, SSO strategy) | P2 | `traefikgen/generator.go:44-60,216-224`; `catalog/loader.go:110-130` |
| 19 | `PlanInstall` never sets `CanInstall=false`/`Blockers`, so the consumer's blocker guard is unreachable; `review-2026-09-17.md` §H3 remains open | P2 | `pipeline.go:76-80` |
| 20 | `primaryContainerNode` = the **last** container def: an order-dependent convention that decides inter-app edges and which node owns SSO provisioning. It is now written down in one app's metadata (`apps/radicale/metadata.yaml`: "the last container in this list is the app's primary node") and nowhere in the code, so it is documented by example rather than by contract. The consequence is live: Authentik's last def is the LDAP outpost, so every inter-app edge on `authentik` resolves to that node | P2 | `orchestrator/graph_build.go` `primaryContainerNode`; `apps/radicale/metadata.yaml` |
| 21 | `IconHandler` rejects `/` and `\` but not `..`, and never re-checks the joined path | P2 | `api/apps_module.go` `IconHandler` |
| 22 | App networks are never removed, and a container orphaned by a crash mid-uninstall is never swept. **Partly closed 2026-10-15**: `container.Runtime.ListContainers` now has production callers (`orchestrator/catalog_update.go` `reconcileCatalogUpdates`, which prunes a labeled container the catalog no longer declares and that once had a graph node, and `pruneContainer`), and the dev fast gate reads the podman client's list. What remains: there is still no `RemoveNetwork` anywhere in the `Runtime` interface, so a network Bloud created for a removed app outlives it, and nothing sweeps a labeled container whose app row is gone rather than whose definition changed | P2 | no `RemoveNetwork` in `internal/container` `Runtime`; `orchestrator/catalog_update.go` `reconcileCatalogUpdates`/`pruneContainer` |
| 23 | ~~Health checks never reach Podman~~ **CLOSED 2026-10-15**: `runContainerHealthCheck` runs the declared test inside the container through `Runtime.Exec`, with the Docker-style `CMD`/`CMD-SHELL` forms translated, an interval, a per-attempt timeout, and a context deadline. The first probe is immediate (the sleep is guarded by `attempt > 0`), so the window is `(retries-1) x interval`. One residual stays open: the loop reads `retries` as *total* attempts, so a check declared `retries: 3` gets 3 tries where the OCI reading is 1 plus 3 retries. That is one attempt short of what the app author declared, and it is silent | P3 (was P2) | `orchestrator/container_health.go` `runContainerHealthCheck`, `convertHealthCheckTest`; `orchestrator/execution.go` HealthCheck phase |
| 24 | `appsModule.GetCatalog` mutates cached catalog entries: it writes `app.EstimatedSizeMB` on the `*catalog.App` structs `GetUserApps` returns, which are the same pointers the cache map holds. PR 6's RWMutex guards the map, not the pointed-to structs, so concurrent `GET /api/apps` requests race on that field (and the catalog is no longer purely disk-driven) | P2 | `catalog/cache.go` `GetAll`; `api/apps_module.go` `GetCatalog` |
| 25 | ~~Nothing re-runs convergence unless an intent is submitted, so an app that lands in `error` stays there indefinitely and the item 8 repair only fires when some other trigger happens to run a pass. `retryable: true` on the operation row was descriptive metadata with no driver: the row promised a retry was expected to help and no retry ever came~~ **CLOSED 2026-09-30**: a `ReconcileIntent` on an idle-based timer (~60s, `BLOUD_RECONCILE_INTERVAL`) submits through the same single-writer queue, and the pass it runs resets `ERROR` nodes whose operation row is retryable. The pass is proven free over a healthy stack: zero container creates, zero config writes, zero node transitions, and no Traefik file rewrite | P1→closed | `orchestrator/selfheal.go`; `orchestrator/selfheal_test.go`; `traefikgen/generator.go` `GenerateAll` |

**Design/complexity debt from the 2026-10-03 APoSD review**
([`docs/specs/review-2026-10-03-aposd.md`](../specs/review-2026-10-03-aposd.md)).
These are complexity findings (change amplification, obscurity, wide/shallow
interfaces), not runtime bugs; severity is the review's. They are design debt,
so "risk×cheapness" here means "how much do they slow the next change".

| # | Item | Sev | Evidence |
|---|---|---|---|
| 26 | ~~Two catalog models of the same `metadata.yaml`: `App` (`models.go`) and `AppDefinition` (`types.go`), loaded by two near-identical walkers (`LoadAll` → `map[string]*App`, `LoadGraph` → `[]*AppDefinition`). The planner reads one graph, the reconciler the other, so they can disagree by construction, and a new top-level metadata field must be added to both structs, both yaml tags, and both loaders~~ **FIXED 2026-10-04**: `AppGraph` now operates on `*App` (via `CatalogID`), `LoadGraph` is a thin wrapper over `LoadAll`, and `AppDefinition`/`loadAppDefinition` are deleted; `TestLoadGraphAndLoadAllAgree` pins the one-model invariant (#206) | P2→closed | `catalog/graph.go`; `catalog/loader.go`; `catalog/types.go` |
| 27 | ~~`OrchestratorConfig` is 29 fields~~ **half closed**: `OrchestratorConfig` is now six subsystem structs (`Tuning`, `Runtime`, `Stores`, `SSO`, `Hosts`, plus the planner), so `nil` disables a subsystem through its own zero value and no caller hand-copies 29 fields. `wire.Input` is still 24 flat fields copied one by one in `buildOrchestratorConfig`, and `wire.Output.Config` still exists only so the completeness test can assert every field was wired. The remaining move is the same one the closed half made: compose the subsystem structs in `wire.Input` instead of flattening them again | P2 | `orchestrator/subsystems.go`; `orchestrator/orchestrator.go` `OrchestratorConfig`; `wire/wire.go` `Input`/`buildOrchestratorConfig`; `wire/completeness_test.go` |
| 28 | ~~The sealed `Intent` set is enumerated in two parallel switches: `applyIntents` (the drain) and `intentTypeName` (logging). Adding a twelfth intent forgotten in one of the two compiles clean and fails only at runtime~~ **FIXED 2026-10-04**: `intentTypeName` is derived from the concrete type (`strings.TrimSuffix(reflect.TypeOf(...).Name(), "Intent")`), so the set is enumerated once; `TestIntentTypeName_AllTypes` pins every name (#204) | P3→closed | `orchestrator/pipeline.go` `intentTypeName` |
| 29 | ~~`AppStore`'s 20-column `SELECT … LEFT JOIN operations` appears verbatim in `GetAll` and `GetByCatalogID`, and `scanApp`/`scanAppRow` are near-identical ~55-line copies of the same `Scan` order and post-processing~~ **FIXED 2026-10-04**: `scanAppColumns(scan func(...any) error)` holds the Scan order and post-processing once; `TestAppStore_ScanPathsAgree` pins `GetAll`/`GetByCatalogID` agreement (#205). Residual (2026-10-15): the column `SELECT` list is still written twice, once per read path, and the old `LEFT JOIN operations` became `attachOperations` | P3→closed, with residual | `store/apps.go` `scanAppColumns` |
| 30 | ~~The invariant "a running app is never downgraded to `installing`" is enforced by two orchestrator callers, each with its own guard and comment, because `AppStore.Install` unconditionally sets `status='installing'` on conflict~~ **FIXED 2026-10-04**: `Install` preserves the running status via a `CASE` in the `ON CONFLICT`; `TestAppStore_Install_RunningAppNotDowngraded` pins it at the store (#205) | P2→closed | `store/apps.go` `Install` |
| 31 | `AppStoreInterface` is a 14-method single interface consumed by the orchestrator and the API layer, which use different subsets; method names leak table columns rather than caller intent. **DEFERRED 2026-10-04**: the interface is consumed by 7+ modules (orchestrator + apps/home/system/settings) with overlapping subsets, so a clean two-role split does not emerge; forcing one adds churn without a consumer benefit. Revisit only if it grows | P3 | `store/interfaces.go` `AppStoreInterface` |
| 32 | ~~Store statuses/phases are bare strings compared to literals throughout, while the graph layer already has a typed status; a typo in a status literal is a silent non-convergence~~ **FIXED 2026-10-04**: `apps.status` is now `store.AppStatus` with a closed set of five values (`store/status.go`); `TestAppStatusClosedSet` pins the set. Operation type/phase/status were already typed constants (#203) | P2→closed | `store/status.go`; `store/apps.go` `InstalledApp.Status`; `store/apps.go` `UpdateStatus` |
| 33 | ~~`appclient.Call` is one struct carrying three modes: plain request, declared-idempotency request, and readiness wait: with fields inert in the modes they do not serve (`ready`/`interval`/`stable`/`tolerateFailures`/`Within`) and invariants spanning modes (`retriesAllowed` reasons over `ready`/`noRetry`/`declaredContract`/verb at once)~~ **FIXED 2026-10-06**: wait mode is a `Wait` builder over a plain `Call` (`GET(p).Wait(pred).Interval(2s).Within(4m).Do(ctx)`). `Call` carries no wait-only field; the predicate is a required argument of `Call.Wait`, so the "wait with no predicate" runtime error is now a compile error. The idempotency contract (`OK`/`AlreadyDone`/`Ensure`) stays on `Call`, since both the plain and the wait path use it | P3→closed | `pkg/appclient/waits.go` |
| 34 | ~~The operation recorder keys its row on the owning app (`ownerApp(id)`), one row per app, while `processLevel` dispatches nodes concurrently: so N nodes of a multi-container app contend on one `UPDATE … WHERE app_name=?`. Item 5's `busy_timeout` fix made writes wait instead of fail, but the unit-of-work mismatch and the arbitrary-survivor ambiguity remain. Key per node and aggregate to the app at read time~~ **FIXED 2026-10-15** (#202): `operations` is keyed `(app_name, node)` (migration 13), so each concurrent node writes its own row. `OperationStore.Get` is a documented projection over those rows, not a surviving write: failed beats running beats complete, then most recently updated, then the lower node name. `TestOperationStore_ConcurrentNodesOfOneAppDoNotContend` pins the concurrency claim and `TestAppStore_OperationAggregatesNodeRows` pins the read side | P2→closed | `store/operations.go`; `schema/migrations.go` `keyOperationsByNode`; `orchestrator/operation_recorder.go` |
| 35 | The provider set still has one copy outside the shared helper: `cli/depgraph_edges.go` `drawnProviders` re-implements it for the README generator, because `cli` is a separate Go module that deliberately parses `metadata.yaml` itself and does not import host-agent. Inside host-agent the rule is now one function (`catalog.DeclaredProviders`, added for #233), read by the resolver, `computeAppDeps`, and the developer graph, with the defunct choice machinery deleted rather than patched. The CLI copy agrees today and is tested, but it is not the same code, so the #227 and #233 divergence can still happen in one and not the other. They also do not implement the same rule: the CLI draws only the default provider for a non-`multi` contract, while `DeclaredProviders` returns the whole declared set, so the two agree only while no app declares a non-`multi` contract with more than one compatible provider. **Re-verified 2026-10-15**, with that condition still true and still unchecked. Repaying it means either giving `cli` a shared catalog module or generating the README from a host-agent-produced `--json` snapshot instead of a second parser | P3 | `orchestrator/graph_build.go` `computeAppDeps`; `catalog/resolution.go`; `cli/depgraph_edges.go` |
| 36 | **A restarted system app can be promoted to `RUNNING` before it can serve.** The Authentik LDAP outpost publishes its port before it can answer a user bind, and the graph promotes its dependents once the declared health check passes. That check is an in-container TCP probe on the LDAP port (`apps/authentik/metadata.yaml`), so promotion means "the socket accepts", not "the service answers". Radicale is the victim: its `ldap` backend raises on the failed bind and the server turns that into a 500, so a correct credential reads as a broken app inside that window. **Partly closed, and re-scoped 2026-10-15**: promotion is gated by the declared probe rather than a container-state read, which is the fix this entry asked for, so what remains is the narrower socket-accepts-versus-service-answers gap, live for every consumer of a restarted system app, not only in CI. Repaying the rest means a real service probe: a bind that returns the base DN, or an HTTP request that returns the expected payload | P2 (was P1) | `orchestrator/execution.go` `runContainerHealthCheck`; `apps/authentik/metadata.yaml` `apps-authentik-ldap` healthCheck; `apps/radicale/configurator.go` `PostStart` (already classifies 500/502/503 as a provider outage rather than an app fault) |

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

Twenty-two user apps and two system apps, and the number moves every time an app
lands. This section does not list them. `apps/*/metadata.yaml` is the only place
an app is written down, the conformance harness (`apps/conformance_test.go`)
enumerates the registry rather than a copy of it, and the README's
[one login](../../README.md#one-login) table is generated from the same metadata
by `./bloud catalogdoc`. A hand-copied table here could only be a stale
photograph, and it was one.

What is stable enough to record is the shape:

- Every app implements `NodeLifecycle` for one graph node, including every
  headless app. Headless means only that the dashboard grid leaves the app out,
  because it has no page to open: the app is still installed, reconciled, and
  routed. The MCP wrappers are the headless set today.
- SSO strategy is one of the four values in invariant 6, and the generated README
  table groups the apps by it. `none` is not an empty group: it holds the apps
  that cannot delegate authentication, and the wrapper endpoints Bloud guards with
  a token it mints.
- Two apps are system apps (`isSystem: true`): Traefik and Authentik. Traefik has
  no configurator of its own; `internal/appconfig` builds its config and
  `apps/traefik/` is metadata plus an icon.

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
  declared readiness (`Wait(pred).WithRetry(...).Do(ctx)`).
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
| C9 | Test depth is uneven for the same contract. The "none for authentik or traefik" half is **closed**: the Authentik server configurator has 12 KB of tests and Traefik's config builder has 11 KB in `internal/appconfig`. The gap that remains is between the apps with a large API surface and the ones without one: Hermes (64 KB), AFFiNE (62 KB), Home Assistant (50 KB), Jellyfin (36 KB), and Paperless-ngx (29 KB) against Immich (3.9 KB) and Navidrome (2.1 KB). The conformance harness covers the floor for every app; it does not exercise an app's own API, so an app whose API is thin needs fewer tests by design | P3 | app package listings |
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

## Recently paid

The newest repayment, and the one the 2026-10-12 review is graded against.
Every earlier closing, with its reasoning and its measurements, is in
[`archive/tech-debt-history.md`](archive/tech-debt-history.md).

### The app-SDK became a module (2026-10-14, review-2026-10-12 findings 3.1-3.6)

The goal the 2026-10-12 review measured everything against: *adding an app, and
updating an app later, should be a change scoped to `apps/<name>/`*. Four changes
closed the structural half of that finding list.

- **`Deps` is grouped, not flat** (#335). The eleven-field bag is four leaf fields
  plus three subsystem values, so an app author reads the four they can use and
  the rest is namespaced. This is the same move ledger item 27 asked the
  orchestrator config to make, and `pkg/configurator/deps.go` is now smaller than
  the `interface.go` it came out of.
- **One `Secret` type states the unpublished-credential rule once** (#333). The
  rule "a credential field is empty until the provider publishes it" was prose,
  repeated per binding type. It is now a type, so a configurator cannot read an
  unpublished credential by forgetting a comment.
- **Contract binders are a table with a completeness test** (#325). `bindContract`
  was an eleven-arm string switch whose `default:` arm only logged. Now
  `contractBinders` maps a contract name to its binder, and
  `contract_binders_test.go` fails a binder that names no declared contract and a
  payload-bearing contract with no binder. The one contract outside the table,
  `inference`, is an allowlist entry with its reason, and an allowlist entry that
  guards nothing is itself a failure. A forgotten binding is a build failure
  instead of a consumer that silently receives nothing.
- **The SDK is a module** (#341). Everything under `services/host-agent/pkg/` moved
  to a top-level `pkg/` module (`codeberg.org/d-buckner/bloud/pkg`), and
  `apps/go.mod` now requires that instead of the runtime, with `replace => ../pkg`.
  The author contract and the contract vocabulary are separate packages
  (`pkg/configurator`, `pkg/bindings`), and `Deps.HTTP`/`Deps.Assets` are inverted
  so the SDK does not reach into the host. `pkg/configurator/interface.go` went
  from 576 lines to 249. Invariant 14 is what made this cheap: the shared packages
  never imported host internals, so the move was a move rather than a surgery.

Two facts about the result are worth keeping, because both were the argument
against doing it. The move is nine packages, not the seven the review listed:
`apps/` also imported `authentik` and `bootstrap`, and leaving either behind keeps
a `require` on the runtime module. And the `bindings` split is roughly 40 files of
mechanical churn, which is why it came last and not first: the plan that ordered
it measured the call sites before it scheduled the move.

*Still open from that review.* Three things, and the review named each of them as
the expensive half:

- The contract vocabulary is still catalog code under `internal/`, so a genuinely
  new contract is still a runtime edit (finding 3.3's "later" half).
- The host plumbing (`Deps`, `AppSecretsProvider`, `Registry`) still lives in the
  same package as the author contract, and `appclient` and `appasset` types are
  still in every configurator's compile closure (findings 3.1 and 3.2).
- The declarative-only path is still not what the author guide leads with
  (finding 3.7). Every one of the 22 user apps registers a configurator, and
  `docs/guides/contributing-apps.md` opens with "A configurator (Go code)" rather
  than with the question of whether the reader needs one.

## Revised Repayment Plan

Ranked by risk times cheapness, not by architectural ambition. The execution
plan with per-slice acceptance criteria is
[`docs/plans/tech-debt-repayment.md`](../plans/tech-debt-repayment.md) (PRs 1-3
landed; PR 4 revised, PRs 6-11 added 2026-09-19).

### 1. Close the auth bypass (P0): DONE 2026-09-19

Shipped as described in [the history](archive/tech-debt-history.md), with the router-level table test
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
from "canceled" and `Start` exits only on cancellation. The health
surface can now see a dead orchestrator (`Stopped()` + `LastConverged()`
feed `CheckSystemHealth`); the rest of PR 11's degraded-payload /
startup-gate scope is still open.

### 3. Make declared intent real (P1, items 6-7): DONE 2026-09-25

Both halves are closed. The Home Assistant half landed first:
`oidcComponentVersion` now carries the bare-semver manifest value (`1.2.1`),
so `SkipIf` matches and the per-pass re-download plus destructive container
recreate are gone (the conformance harness caught the mismatch against the
real manifest).

The appclient half closed the same day. `Call.Timeout` is honored as a
per-request deadline, the readiness wait defaults to `WaitPolicy` as its own
doc always claimed, and the total wait budget is a distinct `Within(d)`. The
choice between "honor it" and "delete it and fail loudly" turned out not to
be binary: honoring `Timeout` needed a second method anyway, because
"how long one probe may take" and "how long the whole wait may take" are
different numbers and the old single method was being used to mean the
second one.

The failure this was preventing is not a slow boot, it is a permanently dead
app: a wait that gives up at 30 s returns an error from `PostStart`, the
node goes to `ERROR`, and `collectWorkForLevel` skips `ERROR` nodes forever
("ERROR is terminal: never retry without an explicit status reset"). Immich,
AFFiNE and Hermes all declared a five-minute budget that did nothing about
it. The ceiling is now single-sourced (`configurator.PhaseBudget` == the
orchestrator's `DefaultAppPhaseBudget`, 5m per configurator phase) and the
harness refuses a declared wait that could not run inside it. `MaxWaitBudget`
(10m) remains the library ceiling on any single `Wait.Within()` wait.

### 4. Durability substrate (P1, item 5): DONE 2026-09-20 (PR 7)

Pragmas moved into the DSN; the `Exec` loop deleted. Tests open the
real `InitDB` path with 2+ connections and assert the cascade fires
and the contended write waits. See the history file.

### 5. Make reality match intent (P1, items 8-9): DONE 2026-09-27

Both halves landed. The reconciler now writes drift into the record the
engine actually reads, and the container runtime no longer destroys a
working container before it has the bytes to replace it.

The framing that unlocked 9a is the invariant: `target != actual` is the
only signal `collectWorkForLevel` consumes, so an observation stored
anywhere else is an observation the system cannot act on. The old code
noticed the drift and filed the notice in the database, which is the record
the user reads and not the record the engine acts on.

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
issued it: an upstream provider's own credential, a certificate the CA
returned, a version pin chosen at install.

### 8. Honest surfaces and dead code (items 13-16, 18-23): partly done 2026-10-15

The plan said: filter system apps by `IsSystem`; delete `ClearAppDataIntent`,
`appsModule.ClearData` and its vacuous 404 test, and `NewAuthRouter`; stop
trusting `category` as a system marker.

Closed since the plan was written: `appsModule.ClearData` is reachable and removes
the app's data directory rather than its catalog directory (item 16's destructive
half), health checks reach Podman (item 23), and `ListContainers` has a
production caller (item 22's second half).

What is left, cheapest first:

1. `catalog.IsSystemApp` reads `App.IsSystem`, so `GetUserApps` stops returning
   the system apps. The dashboard filter that compensates today is a client
   enforcing a server's contract (item 14).
2. Delete `ClearAppDataIntent`, or give the drain an arm for it (item 16).
3. Delete `NewAuthRouter`. The production router builds its own auth routes, and
   only tests call the exported helper.
4. `sso.DeriveSecret`'s literal fallback (item 17), the hand-built Traefik YAML
   with unquoted app names and no parse before the write (item 18), `PlanInstall`
   never setting `CanInstall: false` so the consumer's blocker guard cannot fire
   (item 19), `primaryContainerNode`'s undocumented "last def wins" convention
   (item 20), and `IconHandler` accepting `..` (item 21).

## Non-Goals

- Do not persist derived state (secrets, OIDC inputs) that can be recomputed
  from the host secret. The derivation is the source of truth.
- Do not extend trust-by-network-position, and do not add a new
  client-controlled input to an authorization decision. The P0 record in
  [the history](archive/tech-debt-history.md) is what that path costs.
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
  in [the history](archive/tech-debt-history.md) is invisible to the current
  selection)

Current baseline as of this note:

```sh
cd services/host-agent && go test ./...   # pass
cd apps && go test ./...                  # pass
cd cli && go test ./...                   # pass
```

The CLI suite used to read `red on darwin` here, on a test named
`TestResolveBackendPrecedence`. That test is gone, and the resolution-order cases
it left behind call `precedenceRelease` (`cli/preferences_test.go`) to pin a host
where every backend is available. They therefore exercise the resolution order
instead of the machine's own backend list, and the suite passes on Linux and on a
single-backend host. It is also in the `fast` tier, so a regression cannot hide
behind a hook that skipped `cli`.
