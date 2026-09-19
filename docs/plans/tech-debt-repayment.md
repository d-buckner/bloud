> Status: accepted (in progress) — PRs 1-3 landed (#85 migrations, #86 operation
> state, #88 route purity). **PR 4 implemented 2026-09-19** (revised first: its
> original design was insufficient against a forgeable forwarding header) and
> verified live against a deployed host-agent. PRs 6-11 added 2026-09-19 from
> `docs/specs/review-2026-09-19.md` and are proposals until each one is started.

# Plan: Tech-Debt Repayment

**Sources:** `docs/operations/tech-debt.md` (the ledger; re-ranked 2026-09-19) and
`docs/specs/review-2026-09-19.md` (the audit that added PRs 6-11).

**Goal:** execute the ledger's open inventory as small, independently mergeable
PRs, ordered so each one builds only on what already landed.

**Merge discipline (applies to every PR below):** one contract test for the new
boundary, a crash/retry or partial-failure test where applicable, no new
app-specific branch in shared orchestration, and the replaced code deleted in the
same PR — no shims, no aliases. `./bloud validate --tier fast` green. Anything
touching auth also runs the PR 4 router-classification test.

---

## Landed

### PR 1 — Versioned migration ledger ✅ (#85)

`schema_migrations` + `BaselineVersion` + ordered `Migration`s, one transaction
per entry, a failing migration aborts boot. `db.go`'s hand-rolled `runMigrations`
deleted whole. Baseline bump rule: any `schema.sql` change for fresh DBs must
bump `BaselineVersion` and add the matching migration for upgraded DBs.

*Post-hoc correction (2026-09-19):* the `user_app_positions` fork fix in ledger
entry 6 cannot fire — the grid shape already existed before the ledger ran, so
that migration is a permanent no-op. The ledger mechanism is still the
improvement this PR claimed; the fork fix is not evidence of one. No code change
implied.

### PR 2 — Durable lifecycle operation state ✅ (#86)

`operations` table (one current-or-last row per app), `OperationRecorder` at
phase boundaries, "last entered phase, never last completed", startup orphan
sweep to `failed/retryable`, read surface via the `GET` LEFT JOIN. Design:
`docs/plans/archive/operation-state-design.md`.

*Still open from this area:* the health surface cannot see a dead orchestrator
(PR 11).

### PR 3 — Route generation no longer owns runtime side effects ✅ (#88)

`RegenerateRoutes(remoteRoutes, tailnetDomain)` is pure; `SyncRoutes()` owns the
explicit ordering (`ensureGateway` → `reconcileRemoteProxies` →
`resolveTailnetDomain` → write). Contract-pinned by `route_sync_test.go`.

*Still open from this area:* `SyncRoutes` errors are logged only and promotion to
RUNNING is unconditional; the generated YAML is never validated (PR 10).

---

## PR 4 — Close the auth bypass (rewritten 2026-09-19) — IMPLEMENTED

**Status: implemented in the working tree, pending commit + the Playwright
suites.** Shipped: `RealIP` removed; admin requires `Bearer <apiToken>` **from**
a trusted position (session path unaffected, ordering pinned by a test);
`apiToken` generated/backfilled by the secrets manager and written to
`<dataDir>/host-agent-api-token` (0600) for `./bloud token`; Traefik's
`forwardedHeaders.insecure: true` removed (secure default, pinned by test);
OAuth redirect/logout URLs derived from the host set with no request-header
input and no lazy redirect-URI registration (`AddRedirectURI` deleted);
`NewSetupRouter` makes the bootstrap pair public and single-registered; the
router-classification table test + spoof cases + ordering test added, and
`serverRequest` now authenticates with the token instead of by position.

**Not done from the sketch:** binding `:3000` to loopback (decision 8 below —
rejected for QEMU), and `BLOUD_TRUSTED_LOCAL_NETS` still exists as the scope
list.

**Verified live** (lima `bloud-dev`): every spoofed-header and unauthenticated
admin call → 401 (direct and through Traefik), wrong bearer → 401, valid bearer
→ 200, `/api/setup/status` → 200 unauthenticated, `/auth/login` with a spoofed
Host → configured redirect URI, `./bloud install`/`uninstall` green.
**Remaining:** `./bloud e2e lifecycle` / `./bloud e2e app` have not been run.

### Original redesign (kept for the rationale)

The original PR 4 design was "loopback admin behind a credential": keep the
position check, add a bearer token, and treat `BLOUD_TRUSTED_LOCAL_NETS` as a
second factor for *where* the token may be used. That design is now known to be
insufficient, because the position check is client-controlled:

- `middleware.RealIP` (`internal/api/router.go:236`) sets `r.RemoteAddr` from
  `True-Client-IP` / `X-Real-IP` / `X-Forwarded-For` — chi checks
  `True-Client-IP` first and never strips it.
- Traefik runs `network: host` (`apps/traefik/metadata.yaml:15-16`) with
  `forwardedHeaders: insecure: true` (`internal/appconfig/traefik.go:76-80`),
  which makes it *skip* its own `DeleteXForwardedHeaders`, and proxies to
  `http://localhost:<hostAgentPort>` (`:109`).
- So `curl -H 'True-Client-IP: 127.0.0.1' http://<box>:8080/api/...` reaches
  `isLocalRequest` as loopback (`auth_module.go:99-124`) and
  `router.go:546-550` mints `RoleAdmin`.

A token requirement does neutralize the spoof (the attacker has no token), but
only if the check is *ordered* correctly, and the "trusted net = second factor"
rationale is dead — the net is forgeable, so it is not a factor at all.

### Design decisions

1. **Order the check: credential first, position second.**
   ```go
   // correct
   if tokenValid(r) && (isLoopbackOrTrustedNet(r)) { admin }
   else if session := sessionFromCookie(r); session != nil { member/admin from session }
   else { 401 }
   ```
   **Not** `if isLoopbackOrTrustedNet(r) { if !tokenValid(r) { 401 } ... }` — every
   browser request through Traefik arrives from Traefik's loopback connection, so
   that form forces the entire dashboard through the token path and locks the UI
   out. This ordering bug is the one way to ship this PR broken while the token
   tests still pass; pin it with a test that asserts a **session-cookie request
   with no token and loopback RemoteAddr** still authenticates as that session's
   user (not as `_cli` admin).
2. **Delete `middleware.RealIP`.** Nothing else in host-agent consumes client IP
   (verified: `r.RemoteAddr` has two readers, both inside `isLocalRequest`), so
   this is a deletion with no behavioural regression beyond the bypass. It also
   removes the `X-Forwarded-Host` / `X-Forwarded-Proto` spoof (PR 4 item 5).
   Keep the position check as a *coarse* scoping factor only.
3. **Position becomes non-authoritative.** `isLocalRequest` stops granting
   anything on its own; it gates whether the token is honoured at all (defence in
   depth, not the credential). Keep `BLOUD_TRUSTED_LOCAL_NETS` for the QEMU slirp
   case, but document it as a scoping hint, not a security boundary.
4. **Token generation and distribution** — as originally planned and still
   correct: `api_token` in `internal/secrets`; CLI gains `ReadRuntimeFile(path)`
   across native/lima/qemu; new `bloud token` command; e2e's Playwright helpers
   shell out to `bloud token` in one helper in `e2e/lib/`, not per spec.
   Constant-time compare (`hmac.Equal` on a hash, or `subtle.ConstantTimeCompare`).
5. **Reject `forwardedHeaders.insecure: true`.** Replace with
   `forwardedHeaders.trustedIPs` limited to Traefik's own address, so a client
   can no longer inject `X-Real-Ip` even if a future change re-introduces
   `RealIP`.
6. **Stop deriving OAuth URLs from request headers.** `requestHost`
   (`auth_module.go:134-139`) prefers client `X-Forwarded-Host`, and
   `LoginHandler` **registers that URL in Authentik** via `AddRedirectURI`
   (`:364-378`). Build redirect/logout URLs from `hostset.State.AllBaseURLs()` /
   the primary host instead (already computed in `initAuthHelper`,
   `router.go:494-508`), and delete lazy redirect-URI registration: redirect URIs
   come from the admin-controlled host set, and unknown ones are rejected.
7. **`GET /api/setup/status` must be genuinely public** (see PR 10). It is
   registered twice and chi's last-registration-wins currently makes it
   admin-only (`router.go:271` vs `:288`). Today first-run setup works *only*
   because of the bypass being closed here — so this fix is a hard prerequisite
   for PR 4, not a nice-to-have: without it, a fresh install is unreachable for
   any non-loopback browser.
8. **Rejected: binding host-agent to loopback only.** The 2026-09-19 review
   recommended it; it breaks the QEMU backend, where slirp presents forwarded
   connections from `10.0.2.2` (which is why `./bloud dev` sets
   `BLOUD_TRUSTED_LOCAL_NETS=10.0.2.0/24`), and the CLI/e2e API path targets
   `:3000`. The credential is the control; the bind address is not. Revisit as
   opt-in hardening (`BLOUD_BIND`) once the token path is proven.

### Tests

- **Router classification table test over the real tree** (new, and the reason
  this PR is safe): build the actual router, issue each route with a
  **non-loopback** `RemoteAddr` and no credential, and assert the exact status
  class (200/401/403) per route. The current helper forces
  `RemoteAddr = 127.0.0.1:1234` (`api_test.go:699-703`), which auto-authenticates
  as admin and hides every middleware regression — that default must go.
- Spoofed `True-Client-IP: 127.0.0.1` / `X-Real-IP: 127.0.0.1` / `X-Forwarded-For:
  127.0.0.1` → 401 without a token, admin with a valid token.
- Session-cookie request + loopback `RemoteAddr` + no token → authenticated as the
  session user (ordering guard, decision 1).
- Token compare is constant-time; loopback without token → 401 on every
  authenticated route.
- `GET /api/setup/status` with no credential → 200.
- `bloud` CLI install/uninstall still work on native, lima and qemu backends.
- e2e suite green with token plumbing.

### Files

`internal/api/router.go` (middleware order, drop `RealIP`, server wiring),
`internal/api/auth_module.go` (`isLocalRequest`, `requestHost`, `requestBaseURL`,
lazy redirect-URI registration), `internal/appconfig/traefik.go` (entrypoint
headers), `internal/secrets`, `cli/*` (request helper + `token` command),
`e2e/lib/*`, `AGENTS.md` (the "loopback, no auth needed" note becomes
"loopback + `bloud token`"), `docs/operations/tech-debt.md`.

**~2 days** (mostly e2e/CLI ripple through three backends). **Blocks nothing, but
must not be merged before item 7 is fixed.**

---

## PR 5 — Single orchestrator builder

**Two `NewOrchestrator` sites with divergent config** (`cmd/host-agent/configure.go:214-260`
CLI-reconcile vs `internal/api/router.go:395-470` product path) is duplicated
"how to wire the system core" knowledge — and the CLI copy is worse than
divergent, it is inert: it configures only `LDAPOutput` (no `AppStore`, no
`Containers`, no `CatalogGraph`) and builds a **different graph shape**
(per-`CatalogID` nodes with edges from `IntegrationConfig`, versus the product
path's per-container nodes), so `configure reconcile` reports success while doing
nothing.

### Design decisions

- One builder owns the full dependency set and returns a wired orchestrator; the
  CLI path calls it with the **same** subsystems (an explicit profile if a
  difference is ever genuinely required; the default is the product path).
- Graph construction moves into the builder too. Decide explicitly whether the
  CLI keeps a per-app-node graph or gets the per-container one — the product
  shape wins unless there is a reason, and then the difference is a named profile.
- Fold the duplicate `AppState` builder in as well: `orchestrator.buildAppState`
  (`:1214`) vs `cmd/host-agent/configure.go:295` disagree on SSO — the CLI reads
  the legacy `appCfg.SSOBaseURL` and ignores admin-set hosts.

### Tests

- Builder test: same config inputs → same non-nil subsystem set at both call
  sites.
- Delete the direct `NewOrchestrator` call sites; existing router tests move to
  the builder.

**Files:** new builder, `configure.go`, `router.go` (serialize with PR 4).
**~1 day.**

---

## PR 6 — Stop the engine's silent failures (new, 2026-09-19)

Three independent defects that each convert a bug into an unnoticeable outage.
Each fix is small; each test is cheap. They are independent — parallelizable.

### 6a. Nil-guard the catalog lookup (process death)

`orchestrator_containers.go:41-45` dereferences before checking the error;
`MemoryCache.Get` returns `(nil, err)` on a miss and `ContainerDefs()` has a
pointer receiver. Reachable whenever an installed app's directory is removed or
renamed (`catalog/loader.go:43-45` skips dirs without `metadata.yaml`), and it
runs on **every** convergence pass (`pipeline.go:455`). No `recover()` exists
anywhere in host-agent, so this kills the daemon.

- Test: store row present, catalog entry absent → `SyncContainerState` returns
  without panicking.
- Fix: `if err != nil || catalogApp == nil { continue }` before the deref, then
  audit the other `.ContainerDefs()` call sites for the same shape
  (`orchestrator.go:1141`, `pipeline.go:656,735` are guarded today — keep them so).

### 6b. Make `MemoryCache` concurrency-safe (unrecoverable fatal)

`catalog/cache.go:12-14` has no mutex; `Refresh` publishes then fills the map
while orchestrator goroutines read it from graph event handlers and
`applyIssuerExtraHost`. `POST /api/apps/refresh-catalog` races them into
`fatal error: concurrent map read and map write`.

- Fix: `sync.RWMutex` (simplest) or build-locally + `atomic.Pointer` swap.
- Test: concurrent `Get` loop + `Refresh` loop under `-race`; add the package to
  the `-race` set in `validation.yaml` (today only
  `internal/engine/orchestrator` runs under `-race`).

### 6c. `WaitAndDrain` must distinguish "empty" from "cancelled" (permanent, silent)

`queue.go`: a stale `signal` token left by the debounce-timer branch
(`:34-38`, `:75-114`) makes the next `WaitAndDrain` consume it, get `nil` from
`Drain()`, and `Start` reads that as shutdown (`orchestrator.go:520-526`). Every
later `Submit` returns 202 and nothing reconciles again.

- Fix: return `([]Intent, bool)` (or a sentinel error); `Start` exits only on
  cancellation.
- Test: deterministic reproduction of the stale-token state (enqueue during the
  debounce window, force the timer branch, then assert the next call does not
  report shutdown); plus a "loop survives an empty drain" test.

**Files:** `internal/catalog/cache.go`, `internal/engine/orchestrator/{queue.go,orchestrator.go,orchestrator_containers.go}`,
`validation.yaml`. **~1 day.**

---

## PR 7 — SQLite pragmas belong in the DSN (new, 2026-09-19)

`db.InitDB` (`:29-39`) applies `PRAGMA foreign_keys=ON` and
`PRAGMA busy_timeout=5000` with `db.Exec`, i.e. to **one** pooled connection;
both are per-connection settings in SQLite, and production never caps the pool
(the only `SetMaxOpenConns` in the tree is `internal/testdb/testdb.go:29`, which
pins `1` for `:memory:` — which is exactly why tests cannot see this). Result: on
every other connection `foreign_keys=OFF` (cascades silently stop firing →
orphan `shares` / `user_app_positions` rows) and `busy_timeout=0` (writes fail
`SQLITE_BUSY` immediately instead of waiting).

- Fix: open with `file:<path>?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)`
  so modernc's driver applies them at every connection open; delete the `Exec`
  loop. Consider `SetMaxOpenConns(1)` to serialize writers (SQLite reality) and
  say why in a comment.
- Test: open the real `InitDB` path, force ≥2 connections, assert (a) a
  `ON DELETE CASCADE` actually cascades, (b) a locked-DB write waits rather than
  failing instantly. Keep testdb's single-connection pinning (it is right for
  `:memory:`) but stop relying on it as the only configuration.

**Files:** `internal/db/db.go`, tests. **~half a day.** Independent of everything else.

---

## PR 8 — Make declared intent real (new, 2026-09-19)

### 8a. `appclient.Call.Timeout()` is a no-op; `WaitPolicy` has no consumers

`call.go:31,119` writes `timeoutOverride` and nothing reads it;
`effectivePolicy()` consults only `retryOverride` / `c.retry` (`call.go:382`,
`waits.go:57`); `retry.go:43-51` documents `WaitPolicy` as the ready-path
default, and `grep` shows zero consumers. So `apps/immich/api.go:33-34` and
`apps/affine/api.go:31-32,58-59` declare 5-minute / 3-minute first-boot waits
that silently run on `DefaultRetry` (5 attempts, 30 s) and land the node in
terminal ERROR on a cold boot whose first migration is slow. Jellyfin escapes
only because `policies.go` sets explicit `RetryPolicy` overrides.

- Fix: honour `timeoutOverride` in `effectivePolicy`/the attempt loop, and make
  `Wait()` use `WaitPolicy` as its default when no override is set. If either is
  genuinely not wanted, **delete the accessor** so an unsupported option fails
  loudly instead of degrading — silently weakening a declared timeout is the
  defect, not the missing feature.
- Test: a call declaring `Timeout(2s)` against a handler that responds at 500 ms
  and 4 s proves the override is honoured; a `Ready()` wait with no override uses
  the wait policy, not the retry policy.

### 8b. Home Assistant's asset `SkipIf` never matches

`apps/homeassistant/configurator.go:44` sets `oidcComponentVersion = "v1.2.1"`
(the release tag) and compares it to the manifest's `version` field via
`ManifestVersionEq`. The real `v1.2.1` release artifact contains
`"version": "1.2.1"` (verified by fetching it), so `SkipIf` is always false:
every full lifecycle pass re-downloads the component, reports `changed`, and
makes the orchestrator destroy and recreate the HA container
(`orchestrator.go:1041-1045`) — invariant 2 broken, plus a network dependency in
the boot path.

- Fix: compare against the manifest value (`"1.2.1"`); keep the tag only in the
  download URL; make the test fixture use the **real** manifest value (it
  currently fabricates the tag, which is why this passed).
- Test: install once, run `PreStart` again, assert `changed == false` and zero
  HTTP fetches on the second pass.

**Files:** `pkg/appclient/{call.go,waits.go,retry.go}`,
`apps/{immich,affine}/*_test.go`, `apps/homeassistant/configurator.go` + fixture.
**~1 day.**

---

## PR 9 — Make reality match intent (new, 2026-09-19)

### 9a. Container drift is never repaired while the process is alive

`SyncContainerState` flips the store to `stopped` when a container is gone, but
the in-memory graph node stays `RUNNING`; `populateGraphNodes` only adds *missing*
nodes and `SetTargetStatus` no-ops when the target is unchanged, so
`collectWorkForLevel` sees `target == actual == RUNNING` and never re-drives. A
killed container (OOM, `podman rm`) stays dead until host-agent restarts, while
the dashboard shows `stopped`. Multi-container apps are skipped entirely
(`len(defs) != 1 → continue`).

- Fix: on every pass, reset the node when a known container is gone
  (`SetActualStatus(INITIALIZING)`) so the normal path re-drives it; extend the
  sync to multi-container apps (iterate `ContainerDefs`, aggregate per container).
- Test: seed a RUNNING node with no container present → after a convergence pass
  the container is recreated; same test for a multi-container app.

### 9b. `Ensure` removes before pulling

`internal/container/runtime.go:162-171` force-removes the running container, then
pulls. A registry outage, rate-limit or digest mismatch leaves the app with **no
container and no rollback**.

- Fix: pull (and verify) first, then remove + create. The recreate path must also
  apply the same `io.bloud.managed` ownership guard `Remove` enforces
  (`:195-199`), so a name collision outside Bloud can never be destroyed.
- Test: pull failure → the original container is still running and untouched;
  foreign container without the label → `Ensure` refuses.

**Files:** `internal/engine/orchestrator/orchestrator_containers.go`,
`internal/container/runtime.go`, tests. **~1-2 days.**
Independent of PRs 4-8.

---

## PR 10 — Honest surfaces and dead code (new, 2026-09-19)

Small, mostly deletions — but each one is currently a lie in an API or a doc.

1. **System apps leak into the user catalog.** The user-facing filter is
   `SystemCategories[app.Category] == "infrastructure"` and **no
   `metadata.yaml` sets it** (traefik is `network`, authentik is `security`, both
   `isSystem: true`), so `GET /api/apps` offers Traefik and Authentik as
   installable apps — contradicting invariant 5. Fix: filter on `IsSystem`;
   delete `SystemCategories` / `IsSystemAppByName` (no production caller) or
   re-point them at `IsSystem`. Test: the catalog API excludes both.
2. **Delete the dead clear-data path.** `ClearAppDataIntent` is dropped by the
   `applyIntents` switch (`pipeline.go:31-49`) despite being declared, mapped in
   `intentTypeName` and compile-asserted; `appsModule.ClearData` is unreachable
   (no route) **and** its `os.RemoveAll(filepath.Join(m.appsDir, name))` targets
   the **catalog** directory rather than the data directory — a latent destroyer
   of `apps/<name>/` if ever wired. Delete the intent, the method, and the vacuous
   test that asserts 404 on a nonexistent route (`api_test.go:924-927`). If
   clear-data returns, it comes back as an orchestrator intent with the data-dir
   path resolved by the single writer.
3. **Delete `NewAuthRouter`** (tests-only; production registers the same handlers
   inline) and point the auth tests at the real router.
4. **Register each chi pattern exactly once.** `POST /api/apps/refresh-catalog` is
   admin-only purely because `router.go:286` is authoritatively last; any reorder
   silently downgrades it. The router-classification test from PR 4 pins this.
5. **Persist the primary host.** `applySetHostsIntent` leaves `storedPrimary`
   empty for a built-in primary, so an admin who selects `bloud.local` as primary
   silently reverts to `localhost` on the next boot — changing the OIDC issuer
   and re-provisioning SSO. Fix: persist the primary unconditionally; test
   boot-resolve after a built-in-primary change.
6. **`sso.DeriveSecret`'s literal fallback** (`blueprint.go:293-297`) — delete it
   and make an empty master secret an error, since every live path already guards
   it. In the same pass, delete the persisted-but-never-read
   `oauthClientSecret` write (`:284-287`), which contradicts the ledger's explicit
   non-goal "do not persist derived state".
7. **Wiring honesty in the sharing/system modules.** `router.go:224-229` passes
   `nil` for the sharing module's tailnet node and the system module's graph and
   orchestrator, so `POST /api/sharing/invites` always returns 503. Either wire
   them from the same objects the orchestrator owns, or remove the endpoints that
   can only ever fail — an endpoint that cannot succeed is worse than a 404.
8. **Validate what we generate and what we load.** Parse the generated Traefik
   YAML before writing it (or emit it via `yaml.Marshal` rather than
   `fmt.Fprintf` with unquoted app names), and add strict decoding + field
   validation at catalog load (unknown keys, port range, pinned image tag,
   container-name shape, SSO strategy). Pin the `primaryContainerNode` = last-def
   convention in the app spec, or replace it with an explicit
   `primary: true` marker — it currently decides inter-app edges and which node
   owns SSO provisioning.

**Files:** `internal/catalog/cache.go`, `internal/api/{apps_module.go,router.go,api_test.go}`,
`internal/engine/orchestrator/{intent.go,pipeline.go}`, `internal/sso/blueprint.go`,
`internal/traefikgen/generator.go`, `docs/specs/app-spec.md`. **~1-2 days.**

---

## PR 11 — Make the health surface able to see this system (new, 2026-09-19)

`CheckSystemHealth` is `db.Ping()` plus `orch == nil → nil` (`server.go:157-167`),
so a host with no working Podman socket boots **"healthy" with no orchestrator**
(installs then 503), a dead intent loop is indistinguishable from an idle one, and
any system-app failure or the 10-minute timeout `os.Exit(1)`s the entire control
plane — including the dashboard that would explain what is broken
(`main.go:227-241`).

- Fix: extend the health payload with orchestrator liveness (`Ready()` plus a
  "last convergence completed at / last error" stamp) and report `degraded`
  rather than exiting; keep the DB check.
- Fix: deepen the *startup* gate to the system apps it actually gates on
  (Traefik, Authentik, LDAP outpost) instead of SQLite.
- Decide explicitly: does a failed system-app convergence block serving the API,
  or serve a degraded dashboard? Recommended: serve, with a loud banner — the
  dashboard is the only tool an owner has to diagnose the box.
- Test: nil orchestrator → health reports degraded (not healthy); stale
  convergence stamp → degraded; a stub that never converges → API still serves.

**Files:** `internal/api/server.go`, `cmd/host-agent/main.go`, health tests.
**~1 day.** Depends on PR 6 for the orchestrator to *have* a truthful liveness
signal.

---

## Sequencing

```
PR 4  (auth bypass: token + drop RealIP + public setup/status)   FIRST — P0
      └─ PR 10.4 (route registration) rides along; PR 10.1 (public setup/status) is a prerequisite

PR 6  (engine silent failures: nil guard, cache lock, queue)     parallel with 7, 8, 9
PR 7  (SQLite pragmas in the DSN)                                parallel, ~half day
PR 8  (timeout/WaitPolicy + HA SkipIf)                           parallel
PR 9  (container drift + pull-before-remove)                     parallel
PR 5  (single orchestrator builder)                              after 4 (router.go contention)
PR 10 (honest surfaces, dead code)                               after 4
PR 11 (health surface)                                           after 6
```

**File-contention rules:** `internal/api/router.go` has one owner at a time —
PR 4 → PR 5/PR 10. `internal/engine/orchestrator/` is touched by PR 6, 9 and 10.2;
PR 6 first (it is the crash-class), then 9, then the deletion in 10.

## Decisions to confirm before implementation

1. **Loopback credential carrier** — per-boot token in the runtime data dir read
   through `ReadRuntimeFile` (as PR 4 plans), versus a unix socket that only local
   callers can reach. Recommended: the token; the seam already exists for all
   three backends. The socket is cleaner but is a fourth backend-specific path.
2. **`BLOUD_TRUSTED_LOCAL_NETS` after PR 4** — keep as a scoping hint (recommended,
   QEMU needs it), or delete the config surface entirely once the token works
   everywhere. Note the CLI sets it in `dev.go:584`, `e2e_lifecycle_exec.go:52`,
   `validate_integration.go:86`.
3. **`RealIP`** — delete outright (recommended; nothing consumes client IP) or
   keep with `trustedIPs` for future access logs.
4. **Health on failed system convergence** — degrade-and-serve (recommended) or
   keep exit-on-failure.
5. **`primaryContainerNode`** — keep the positional convention (document it) or
   add an explicit `primary: true` to the container schema (a metadata change,
   touching every multi-container app).
