# Bloud — Architectural Design Review

**Reviewer:** Architecture review
**Scope:** High-level design & architecture of the Bloud home-cloud integration platform
**Date:** (current)
**Code under review:** `refactor/server-deep-modules` — `go build ./...` green in `services/host-agent`; unit tests green

---

## 0. How to Read This

This is a **design review**, not a bug audit. It evaluates the architecture against the
problem Bloud is trying to solve, the way a deep architecture review would defend or attack
the design. Where the architecture and the implementation
have drifted, that drift is called out explicitly — and it is often more important than
the code itself, because it means the *documented* system and the *running* system are
two different things.

The single most important message up front: **the core architectural idea is good and
worth defending, but the install path is not actually wired into the production server.
Installs silently no-op.** That is the root cause of the reported "installs don't start"
behavior, and it is a wiring gap, not a design flaw.

---

## 1. Executive Summary

Bloud's thesis is:

> Apps declare what they provide and what they consume (`metadata.yaml`); the platform
> owns the integration knowledge and wires everything — SSO, API keys, LDAP, routing,
> per-app databases — **declaratively**. The system manages *desired state* and
> reconciles *actual state* toward it, not a sequence of assumed-success commands.

That thesis is correct for this problem space, and it is largely implemented well. The
**intent-driven reconciler with a single writer**, the debounced FIFO queue, the
topological lifecycle ordering with per-app error isolation, and idempotent convergence
are the right shapes. The `deep-modules` refactor is genuinely good hygiene: the API
surface, store layer, catalog, graph, and orchestrator are cleanly separated, and unit
coverage is strong.

**However, the documented architecture and the running implementation have diverged in
material ways that the tests do not catch.** The most consequential:

1. **The install/dependency path is inert in production.** `initOrchestratorHelper`
   constructs the orchestrator with `CatalogGraph: nil`. `applyInstallIntent` returns
   early at `if o.catalogGraph == nil` before recording the app in the store, so an
   install intent is a silent no-op. Unit tests wire a non-nil graph and pass green;
   CI runs only `go test ./...`; the install path is therefore **untested in production
   while appearing covered by tests**. This is the "installs don't start" bug.
2. **Lifecycle-graph durability is dead code.** The running orchestrator uses an
   in-memory `MapRepository`; the SQLite-backed `SQLiteRepository` and the
   `graph_nodes`/`graph_edges` tables in `schema.sql` are never used. "Resume after
   restart" and the "ERROR is terminal" guarantee are therefore process-local only.
3. **The "single writer" model has documented exceptions that have become permanent
   drift.** Share and guest records are written directly by API handlers, bypassing the
   intent queue, while `CreateShareIntent`/`RevokeShareIntent` remain as dead code.
4. **Hardcoded fallback secrets ship in the production config path.** If `secrets.json`
   is missing and env vars are unset, Authentik boots with admin password `password`,
   the SSO host secret is `dev-secret-change-in-production`, and Postgres uses
   `testpass123`. For a self-hosted platform this is a first-class security finding.

**Verdict:** The design is sound and worth continuing. The gaps are **wiring,
durability, and hardening** — not fundamental wrong turns. Close the production-wiring
gap (install path) before adding more surface area (sharing federation, tailnet
outposts). The `CatalogGraph: nil` is a one-line fix with outsized impact.

---

## 2. What Bloud Is (for the review record)

- A self-hosted home-cloud integration platform (CasaOS/Yunohost-class) that auto-wires
  inter-app integrations.
- Go backend (`services/host-agent`, ~16k LOC), SvelteKit frontend (`web`), Go CLI
  (`cli`), app catalog in YAML (`apps/`).
- Runtime: rootless Podman containers managed directly by the host-agent via the Podman API; Traefik reverse proxy;
  Authentik identity/SSO; per-app PostgreSQL/Redis containers declared in `containers:`.
- Novel/advanced: federated sharing across hosts via Tailscale (gateway container +
  SOCKS5 + remote proxies + invite tokens), remote apps, forward-auth / native-OIDC /
  LDAP SSO strategies, multi-container app specs.

### Core architecture (as implemented)

```
HTTP handler ──enqueue──▶ Intent (typed, immutable)
                            │
                            ▼
        IntentQueue (debounced FIFO)
                            │
                            ▼
        Orchestrator.converge()
            ├─ applyIntents()        → mutate stores (desired state)     [single writer]
            ├─ convergeFromStores()  → read stores + runtime, diff
            │     ├─ ensureSystemAppsInstalled()
            │     ├─ SyncContainerState()     (align DB with reality)
            │     ├─ handle uninstalls
            │     ├─ populateGraphNodes()     (build DAG from stores)
            │     ├─ convergeTailnet()
            │     ├─ catalogGraph.SetInstalled()
            │     └─ Reconcile()      → topological levels, per-level concurrent
            │            phases: INITIALIZING→PRESTART→STARTING→POSTSTART→RUNNING
            └─ RegenerateRoutes()     → Traefik config last, then promote to RUNNING
```

Graph state (`graph.Graph`) holds `targetStatus` (desired) vs `actualStatus` (observed),
with event-driven status sync to the DB. This is a correct model — the only thing wrong
is that the graph's durable backing is not used.

---

## 3. What Is Strong (defend these)

### 3.1 Declarative desired-state as the core model
Bloud manages **desired state** (stores) and reconciles **actual state** (containers,
Traefik, sidecars) toward it. This is the correct inversion for self-hosting: it makes
install, uninstall, reboot, and crash-recovery all the same idempotent loop. The
"converging an already-correct system makes no changes" property is real and valuable.

### 3.2 Intent-driven single-writer queue
Typed immutable intents → debounced FIFO → a single convergence pass. This gives
transactional grouping (three rapid installs → one convergence), serialization by
construction (no mutex), and a uniform 202-accepted API model. The sealed `Intent`
interface with compile-time assertions is good hygiene.

### 3.3 Topological lifecycle with error isolation
`Reconcile` computes topological levels, runs each level concurrently, and treats ERROR
as terminal per node — a failing app blocks only its dependents, not the whole system.
Dependency staleness (re-run PostStart when a dependency completes) is handled
explicitly. This is the right concurrency model for a heterogeneous app fleet.

### 3.4 Multi-container app specs
Expanding an app into one graph node per container (`apps-authentik-server`,
`apps-authentik-redis`, etc.) with within-app `dependsOn` edges is the right evolution
from single-container apps, and `containerOwner` mapping is clean.

### 3.5 Route-generation-last ordering
Promoting a node to RUNNING only *after* Traefik routes are regenerated means the UI
never shows an app as "installed" before external access is live. Small detail, correct
semantics.

### 3.6 Module hygiene
The `deep-modules` refactor (apps/home/logs/remote_apps/settings/sharing/system) with
narrow interfaces and per-module tests is exactly what a mature codebase needs. The
`authConfigRef` atomic-pointer pattern for post-convergence auth re-init is a good
solution to the "Authentik isn't ready at construction" problem.

---

## 4. Critical Findings

### C1 — The install path is inert in production (root cause of "installs don't start")

**Where:** `services/host-agent/internal/api/router.go` (line 371) sets `CatalogGraph: nil`;
`internal/orchestrator/pipeline.go` `applyInstallIntent` returns before recording.

**Mechanism:**
```
POST /apps/{name}/install
  → appsModule.Install(name) → orch.Enqueue(InstallAppIntent)
  → converge() → applyIntents() → applyInstallIntent()
      → existing not running → check `o.catalogGraph == nil` → TRUE → return (no-op)
  → app never recorded in store → never in appMap → never in graph → never starts
```

The install intent is dropped silently. `catalog.NewLoader(cfg.AppsDir).LoadAll()` in
`main.go` already produces an `AppGraph` (`loader.go:121` returns `NewGraph(apps)`), but
`initOrchestratorHelper` never constructs or passes it. Unit tests (`pipeline_test.go`,
`orchestrator_test.go`) wire a non-nil `CatalogGraph` and pass green, so the install path
appears covered while being dead in production. CI runs only `go test ./...`, so the gap
is invisible.

**Why the tests don't catch it:** the tests construct `OrchestratorConfig` with a mock
`CatalogGraph`. The production wiring path in `router.go` is never exercised by tests —
there is no integration test that boots the real router and installs an app.

**Fix:** wire `catalog.NewGraph(...)` (or an equivalent) into `CatalogGraph` in
`initOrchestratorHelper`, and add a router-level integration test that installs an app
and asserts it reaches RUNNING. Also note: `applyInstallIntent` currently *requires*
`catalogGraph` to resolve dependencies; if dependency resolution should remain optional,
the early-return must at minimum record the app directly so installs work without a
planner.

**Priority: P0.** This is the user-visible "installs don't start" bug and the single
highest-impact one-line fix in the codebase.

### C2 — Lifecycle-graph durability is dead code; ERROR-terminal is process-local

**Where:** `router.go` builds `graph.New(graph.NewMapRepository())`; `schema.sql` has
`graph_nodes`/`graph_edges`; `internal/graph/sqlite_repository.go` implements the durable
backing but nothing uses it.

**Mechanism:** The graph is rebuilt in-memory on every boot from the stores, so
topological *structure* is re-derivable. But `actualStatus` — including the **terminal
ERROR state** — is not. A node in ERROR is skipped on every pass *within a process*; on
restart the graph is empty, ERROR is forgotten, and the app is re-driven from scratch,
potentially re-attempting a failing install and re-entering ERROR in a loop.

**Impact:** The "ERROR is terminal" guarantee — the design's core error-isolation
property — does not survive restart. The SQLite tables are dead schema, and the durable
repository is dead code.

**Fix:** either (a) wire `graph.New(graph.NewSQLiteRepository(db))` and hydrate it on
boot, or (b) make ERROR durable in the store (`apps.status`) and have
`populateGraphNodes` re-mark ERROR nodes. Option (a) is the intended design and the
smaller change.

**Priority: P1.**

### C3 — The "single writer" model has drift: share/guest writes bypass the intent queue

**Where:** `api/sharing_module.go` calls `shareStore.Create`, `shareStore.Revoke`, and
`guestStore.Create` directly in handlers; `CreateShareIntent`/`RevokeShareIntent` exist
in `intent.go` and have `intentTypeName` mappings but are **never enqueued** anywhere.

**Mechanism:** The RECONCILER_SPEC explicitly left share creation as an open question
("may be better kept direct") and the implementation chose direct writes — a defensible
call, but it means:
- Share/guest writes are not serialized against the convergence loop (race with
  concurrent installs reading app stores).
- The intent types are dead code that imply the single-writer model covers shares when
  it does not.
- `router.go:325` also writes `tailnetStore.Create` directly in the `BLOUD_TS_AUTHKEY`
  migration path.

**Impact:** The single-writer invariant — the property that makes the reconciler safe —
is silently weaker than documented. Direct store writes from handlers are exactly the
race the architecture was designed to eliminate.

**Fix:** decide and document one of two: (a) keep shares direct and delete the dead
intent types (honest about the boundary), or (b) route share/guest writes through the
queue. Either is fine; the current half-and-half state is not.

**Priority: P1.**

### C4 — Hardcoded fallback secrets ship in the production config path

**Where:** `internal/config/config.go` `getEnvOrSecret` fallbacks: `testpass123`,
`dev-secret-change-in-production`, `password`, `ldap-bind-password-change-in-production`;
`getAuthentikToken` fallback `test-bootstrap-token-change-in-production`.

**Mechanism:** If `secrets.json` is missing and env vars are unset (a fresh install, or
a user who doesn't set env), the system boots with a **known, hardcoded admin password**,
a known SSO host secret, and a known Postgres password. A self-hosted box with a
reachable dashboard is one weak-password guess away from admin.

**Impact:** First-class security finding for a platform whose entire pitch is
self-hosting. The fallbacks exist for dev convenience but they are in the *same code
path* that production uses.

**Fix:** fail hard (or generate + log a random secret) when secrets are missing in
non-dev mode; gate the static fallbacks behind an explicit `BLOUD_DEV` flag. Do not ship
a known password in the default path.

**Priority: P1.**

---

## 5. High Findings

### H1 — Loopback requests are granted admin without any credential

**Where:** `api/router.go` `authMiddlewareFn` — `if isLocalRequest(r)` sets
`User{Role: RoleAdmin}` and passes.

**Mechanism:** Any request whose source IP is loopback gets `_cli` admin, regardless of
who or what makes it. This is the CLI-trust mechanism (the CLI talks to `:3000` over
loopback), but it is *implicit trust of the network namespace*: a compromised local
service, a malicious container with host-network mode, or an SSRF-able app on the same
host all get full admin to the dashboard API.

**Impact:** The boundary between "CLI" and "everything else on this host" is invisible.
There is no credential, no CSRF protection, and no way to distinguish a real CLI session
from any local process.

**Fix:** require a per-session credential (the CLI already authenticates via the same
session flow) or a shared secret; do not grant admin on IP alone. At minimum document
this as an explicit trust boundary and scope what loopback requests can do.

**Priority: P2.**

### H2 — `apps.status` is overloaded; no separate operation-state model

**Where:** `store/apps.go` + `schema.sql` — a single `status` string holds
`stopped`/`running`/`uninstalling`/`error`, written from *both* the convergence loop and
`SyncContainerState`.

**Mechanism:** The graph separates `targetStatus` from `actualStatus`, but the store
collapses them into one column. `SyncContainerState` writes `stopped` when a container
is gone, and `applyInstallIntent` checks `status == "running"` as its "already installed"
guard. The same field is used as both desired and observed state, and as install-trigger
and install-guard. This makes it hard to distinguish "user asked to install, in
progress" from "container crashed, needs restart" from "installed and healthy."

**Impact:** Idempotency bugs and restart behavior are hard to reason about; a crashed
container marked `stopped` will be re-created, but a user uninstall is also `uninstalling`
on the same axis. The design intent (a separate operation state) is documented in
[docs/operations/tech-debt.md](docs/operations/tech-debt.md) but not implemented.

**Fix:** introduce a separate operation/lifecycle state distinct from the observed
status, or at minimum make the status transitions explicit and single-authored (only the
orchestrator writes it).

**Priority: P2.**

### H3 — Installs are choice-less: user integration choices are dropped

**Where:** `pipeline.go:67` — `buildIntegrationConfig(nil, plan.AutoConfig, plan.Choices)`
always passes `nil` user choices.

**Mechanism:** `PlanInstall` returns `Choices` when a required integration has multiple
compatible providers or none installed. `applyInstallIntent` passes `nil` for user
choices, so `buildIntegrationConfig` can only honor auto-config bindings (exactly one
installed provider) and the `Recommended` default of a required choice. Any required
integration whose choice has **no `Recommended` default** (e.g. multiple installed
providers, none marked default) produces **no binding**, and the app records without its
dependency. There is no API path for the user to select a provider — installs are forced
through the auto-config/recommended-default path.

**Impact:** Dependency resolution is incomplete by design. The "one-click install"
promise holds only when exactly one compatible provider is installed, or when a required
integration has a declared default. The multi-option case silently under-wires the app.

**Fix:** either (a) implement choice resolution in the API/intent path (the frontend
already has catalog UI), or (b) define a deterministic default-selection policy so
`Choices` are resolved server-side. The current silent-drop is neither.

**Priority: P2.**

### H4 — Startup is all-or-nothing and health-gated

**Where:** `cmd/host-agent/main.go` — the server blocks on `OrchestratorReady()` with a
10-minute timeout, then `CheckSystemHealth()`; any system-app failure or timeout calls
`os.Exit(1)`.

**Mechanism:** The host-agent refuses to start serving if the system convergence pass
fails. `CheckSystemHealth` only pings the SQLite DB — it does not verify Authentik,
Traefik, or LDAP are actually up, so "system healthy" is mostly "DB is reachable."

**Impact:** A transient system-app failure takes down the whole control plane, including
the dashboard that would let a user diagnose it. The 10-minute hard timeout is a long
hang. The health check is shallow relative to its gatekeeping role.

**Fix:** degrade gracefully (serve the dashboard while system apps converge; surface
system status as degraded rather than exiting), and deepen the health check to cover the
actual system dependencies.

**Priority: P2.**

---

## 6. Medium Findings

### M1 — Dead code / architecture drift inventory

- `config.Config.PostgresURL()` — shared-Postgres connection string; no callers after
  the per-app-database refactor. Dead.
- `graph/sqlite_repository.go` — durable graph backing; never wired (see C2). Dead.
- `CreateShareIntent` / `RevokeShareIntent` — never enqueued (see C3). Dead.
- `apps/authentik/server_configurator.go`, `configurator.go` — check whether the
  "shared postgres" bootstrap references remain after the per-app refactor.
- `specs/reconciler-spec.md`, `docs/architecture/overview.md`, `docs/operations/tech-debt.md`
  describe a "reconciler" component and phases that no longer exist; the code now uses
  an `orchestrator` with the intent pattern folded in. Docs are stale (see M2).

### M2 — Documentation/architecture drift (the "two systems" problem)

The docs described the **reconciler** as a distinct component with phases; the code is an
**orchestrator** that owns both intent-draining and convergence. Several docs referenced
shared system Postgres/Redis, the removed `handlePlanInstall`/`handlePlanRemove`
endpoints, and the old `EnqueueInstall`/`EnqueueUninstall` interface, plus dead files
(`orchestrator_portable.go`, `sharing/sidecar.go`). The running system and the documented
system differed enough that a new contributor reading the docs would be misled.

> **Resolved (2026):** the accompanying doc-update commit refreshes
> `portable-runtime-architecture.md`, `contributing-apps.md`, `backend-tech-debt.md`,
> `RECONCILER_SPEC.md`, and `SPEC.md` to the orchestrator model and flags the dead code.

### M3 — Sharing API layer is wired with nil dependencies

`router.go` constructs `NewSharingModule(..., tailnetNode=nil)` and
`NewSystemModule(..., graph=nil, orch=nil)`. The real orchestrator owns the wired
tailnet/gateway/proxy objects; the API modules get nil, so the sharing and system
modules' graph-aware and tailnet-aware paths are inert at the API layer. The sharing
federation feature — a headline capability — has two parallel wiring paths, one of
which is dead.

### M4 — Redis session store vs. per-app Redis: two meanings of "Redis"

The API uses `RedisAddr` for the **session store** (`store/session_store`), while apps
declare their own per-app Redis containers. The config field name and the per-app
containers use the same word for different things; the shared session Redis is a
separate concern from app Redis. Worth an explicit name/ownership distinction.

### M5 — `SyncContainerState` skips multi-container apps

`SyncContainerState` only reconciles apps with exactly one container def
(`len(defs) != 1 → continue`); multi-container apps rely on graph events. That means a
crashed multi-container app is not re-aligned on startup by the sync path — a durability
gap for the newer multi-container model.

### M6 — Gateway/proxy/outpost lifecycle is complex for MVP

Tailscale gateway + SOCKS5 + per-app tailnet nodes + remote proxies + proxy outpost is a
large surface for a first release. Much of it is gated behind `TSAuthKey == ""` (disabled
when unset), which is reasonable, but the combinatorial complexity is where subtle
sharing bugs live (e.g. the recent "close listener after proxy shutdown" fix). Worth a
single ownership diagram and a clear "enabled only when tailnet is configured" contract.

---

## 7. Recommendations (prioritized)

1. **P0 — Wire `CatalogGraph` in production.** Construct `catalog.NewGraph(...)` from the
   loaded catalog in `initOrchestratorHelper` and pass it into `OrchestratorConfig`.
   Add a router-level integration test that boots the real wiring and asserts an
   install reaches RUNNING. This closes the "installs don't start" bug.
2. **P1 — Make graph state durable.** Wire `NewSQLiteRepository(db)` (the tables already
   exist) and hydrate on boot, or make ERROR terminal durable in the store. Decide
   between the two and delete the unused path.
3. **P1 — Reconcile the single-writer boundary.** Either route share/guest writes through
   the intent queue or remove the dead intent types and document the direct-write
   boundary. One path, documented.
4. **P1 — Remove hardcoded fallback secrets from the production path.** Generate-and-log
   random secrets, or fail hard, outside an explicit dev flag.
5. **P2 — Replace implicit loopback-admin with a credential.** Scope what loopback
   requests can do and require a per-session secret.
6. **P2 — Add an operation-state model** distinct from observed status (per the existing
   tech-debt note), so install-in-progress vs crashed vs healthy are unambiguous.
7. **P2 — Degrade gracefully at startup** instead of exiting on system-convergence
   failure; deepen the health gate.
8. **Docs** — update the reconciler/orchestrator naming, remove dead shared-postgres and
   removed-endpoint references. **Done** in the accompanying doc-update commit
   (`portable-runtime-architecture.md`, `contributing-apps.md`, `backend-tech-debt.md`,
   `RECONCILER_SPEC.md`, `SPEC.md`).

---

## 8. What We Did NOT Deep-Review (scope limits)

- Frontend (`web/`) store/component correctness — reviewed only as an API consumer.
- The `apps/*` configurators' per-app logic (Authentik/Jellyfin/Immich/Navidrome) —
  reviewed the framework, not each app.
- CLI `vm/` execution details — reviewed the interface only.
- Nix packaging (flake) — out of scope.

These are candidates for a follow-up review; the backend control-plane findings above
are the ones that block correct behavior today.

---

## 9. Verdict

Bloud's architecture is **sound and worth continuing**. The declarative desired-state
model, the intent queue, and the topological reconciler are the right foundation. The
blocking issues are not design errors but **wiring, durability, and hardening**:

- One P0 wiring gap (install path inert) explains the user-visible failure and has a
  small, well-understood fix.
- Two P1 durability/ownership gaps (graph persistence, single-writer boundary) undermine
  invariants the design relies on.
- Two P1 hardening gaps (fallback secrets, loopback-admin) are first-class security
  issues for a self-hosted platform.

Close these before expanding surface area (sharing federation, tailnet outposts). The
architecture will then be in a state where new features land on a correct, durable
foundation.
