# Bloud: Design & Code Review, APoSD lens (2026-10-03)

**Reviewer:** external code review through the lens of John Ousterhout's
*A Philosophy of Software Design* (APoSD)
**Scope:** the Go runtime: `services/host-agent/` (orchestrator, catalog, store,
config, hostset, wire, container/podman, api, pkg/*) and the `apps/` configurator
surface. The frontend and CLI were read for context but are not the focus.
**Code under review:** `6e1e2ef` ("docs: refresh generated docs [skip ci]"), `origin/main`

> **This is a dated snapshot, not the debt ledger.**
> The single living ledger for backend debt and its repayment plan remains
> [`docs/operations/tech-debt.md`](../operations/tech-debt.md). The genuinely new
> findings from this review were folded in there as items 26-34 at the time of
> writing; this file records the system as reviewed through the APoSD lens on
> 2026-10-03. Where a finding below maps to an existing ledger item (5, 18, 20)
> it is called out rather than duplicated.

APoSD grades for **complexity** (anything that makes the system hard to understand
or modify), using its two causes (dependencies, obscurity) and three symptoms
(change amplification, cognitive load, unknown unknowns), plus its two prescriptive
tools (deep modules / information hiding, and "define errors out of existence").

---

## 1. Verdict up front

This is an unusually disciplined codebase. The team has already internalized most of
what APoSD teaches: the docs are the best I have read in a production Go repo, the
single-writer orchestrator is a real invariant rather than a comment, the catalog loader
*defines errors out of existence* by rejecting invalid metadata at load time, and the
recent consolidations (`pkg/servarr`, `pkg/bootstrap`, `managedfile`, `hostset`) are
textbook deep-module work. **The dominant failure mode is not shallow modules; it is
*wideness*.** The orchestrator's configuration surface and the store's interface have
grown to a size where they resist understanding, and the codebase has responded with
symptoms APoSD names precisely:

- a 29-field `OrchestratorConfig` hand-copied from a 25-field `wire.Input`, with a
  dedicated **completeness test** whose existence is itself the tell;
- two parallel catalog models (`App` and `AppDefinition`) loaded by two near-identical
  walkers from the same `metadata.yaml`;
- a 15-method `AppStoreInterface` consumed by callers that each use a different slice of it.

None of these is a bug. All of them are *complexity*: they make the next change slower and
the current shape harder to hold in one head.

---

## 2. What the codebase does *right* (the bar the rest is measured against)

These are real exemplars. Any recommendation below must not regress them.

### 2.1 `internal/hostset`: a deep module built around "one valid answer"

`hostset.go` takes a problem that used to be a tangle of `SSOBaseURL` / `SSOAuthentikURL` /
`SSOIssuerURL` / `BaseDomain` strings and collapses it into a single immutable value type
`HostSet` with one parse gate (`ParsePublicURL`) and one resolution function (`Resolve`).
Every derived URL (issuer, redirect URIs, `extraHosts` pin) is a *method* on that value,
not a string threaded through call sites. This is exactly the APoSD "design it twice, then
pick the deep interface" outcome:

- **Information hiding:** the http/https/IP-literal/loopback/special-use-name rules live in
  one place. `Deployability` and `ProxyConsistency` *name the misconfiguration* instead of
  letting a stalled login surface it later.
- **Errors defined out of existence:** `ParsePublicURL` rejects a path/query/credential/IP-as-https
  at the boundary, so a "redirect URI that never matches what the browser sends" cannot be
  constructed. The `IssuerExtraHost()` returning `""` under https removes an entire bug class.
- **The comments carry the *why*, not the what**: "verified against a real proxied
  deployment" is the gold standard for a comment.

The one wart: `BuiltinSet()` allocates a fresh map on every `Contains`/`IsBuiltin`/`BaseURLFor`
call. Not a complexity issue, just a per-call allocation behind a hot comparison; a package
var or a `map[string]bool` built once would do.

### 2.2 `internal/catalog` validation: errors made impossible at load time

`loader.go`'s `validateApp` tree is a masterclass in the "push errors to the earliest
possible point" principle. `validateRequiredDefault` refuses a `required` integration with
no `default: true` entry *because the failure mode is an empty credential forever, with the
symptom nowhere near the cause*. `validateContractSecrets`/`validateContractValues` reject
cross-file agreements that "no compiler sees". Each validation error is written to explain
*what the consumer would wrongly read*, not just "invalid". This is the single most
cost-effective complexity-reduction pattern in the repo, and it is applied consistently.

### 2.3 `internal/container`: a deep `Runtime` with a guard that defines destruction out of existence

`container/runtime.go` hides all Podman specifics behind `Spec`/`Runtime`. Three decisions
stand out:

- `SpecRevisionLabel` (a sha256 of the desired spec) turns "did the spec change?" into a
  label comparison, so reconcile diffs without recreating.
- `isManaged` is the *one* predicate both destructive paths (`Ensure`'s recreate branch and
  `Remove`) call, so "refuse to destroy a container Bloud did not create" cannot be forgotten
  on one path. This is precisely the fix APoSD recommends for a rule that lives in two places.
- The `pull → remove → create → start` ordering (pulled before destroy) removes the
  no-rollback failure mode.

The `podmanClient` interface (redeclared here for test fakes) is mild interface duplication,
but it buys a clean seam; acceptable.

### 2.4 `pkg/servarr` / `pkg/bootstrap` / `managedfile`: consolidation done right

Sonarr and Radarr went from 394 lines each (byte-identical except nine values) to ~45 lines
over one `PVRConfigurator` parameterised by a `PVRApp` struct. The `PVRApp` value is *the*
difference between the two apps; everything else is shared. This is the APoSD "general-purpose
module" ideal: the interface is the same depth, but one module now serves two apps.

`pkg/bootstrap` similarly collapsed three divergent copy-pasted admin-account sequences into
one policy ("an unverifiable admin is a reported condition, never a terminal error").

### 2.5 The `Intent` queue and liveness surface

The sealed `Intent` interface plus a single `WaitAndDrain` consumer is a clean single-writer
design. The `([]Intent, bool)` live-flag fix: where a stale signal token can no longer end
reconciliation forever: and the `Stopped()`/`LastConverged()` surface that makes "dead loop"
distinguishable from "idle loop" are both *errors defined out of existence*: the old shape
made a specific outage invisible, the new shape makes it unrepresentable.

### 2.6 The operation ledger and the tech-debt ledger itself

`docs/operations/tech-debt.md` is the best-maintained debt ledger I have seen: every entry
carries evidence (file:line), a severity, and a "closed on <date> with what changed". It is
worth calling out that this *discipline* is what makes the code reviewable at all.

---

## 3. Findings

Findings are grouped by the APoSD symptom they exhibit. Each has a severity
(**C** = complexity/design, not a runtime bug), evidence, and a direction.

### 3.1 Shallow module / wide interface: `OrchestratorConfig` (and its mirror `wire.Input`)

**Severity: C1 (highest design debt).**

Evidence:

- `OrchestratorConfig` is **29 fields**; the `Orchestrator` struct it builds is **41 fields**.
- `wire.Input` is **25 fields**, and `buildOrchestratorConfig` copies them into the config one
  by one.
- `wire.Output` exposes `Config` *for no purpose except a test*; the comment says so outright:
  "it is exposed so a test can assert that every field the type declares was deliberately set…
  the alternative is a test-only accessor."

That last fact is the smoking gun. The team built a **completeness test to compensate for a
config surface too wide to wire correctly by eye**. APoSD would call this the classic failure
of a *shallow* module: the interface (`OrchestratorConfig`) exposes almost as much as it hides,
so its only job (hiding the construction complexity) is not being done.

The 29 fields are individually well-documented and mostly have honest "nil = subsystem
disabled" semantics, which is a defensible pattern for a test-composable orchestrator. The
problem is not any one field; it is that they are a flat bag. There is a natural grouping the
comment in `wire.go` already names: "the whole dependency set: the lifecycle graph, the
catalog dependency graph, the container runtime, the tailnet node, the gateway, the remote
proxy, the proxy outpost, the Traefik route generator, the durable operation store, the SSO
provisioner, and the one-shot auth-key migration." Those are ~10 *subsystems*, each currently
flattened into 2-4 fields of the same struct.

**Direction (not a small patch):** group the config into subsystem structs
(`SSO`, `Tailnet`, `Proxy`, `Store`…), each a small value the orchestrator takes, so the
"nil disables this subsystem" decision moves into each subsystem's own zero value. The
completeness test then shrinks to "each subsystem struct is either nil or fully populated",
and `wire.Input` can be *composed of* the same structs instead of hand-mapped field-by-field.
This is the highest-leverage structural change in the repo, and it is exactly what the
`hostset` package already did for the address problem (collapse N strings into one value).

### 3.2 Change amplification: the intent type switch lives in two places

**Severity: C2.**

`Intent` is a sealed interface with 11 implementations (`intent.go`). To add a twelfth, you must
edit, in lockstep:

1. `intent.go`: the struct + `intentMarker()` + constructor + the `var _ Intent = …` block;
2. `pipeline.go` `applyIntents`: the drain `switch i := intent.(type)` arm;
3. `pipeline.go` `intentTypeName`: the logging-name switch.

That is a pure Go-idiomatic type switch (there is no sealed-interface exhaustiveness), but the
*duplication of the enumeration* across (2) and (3) is avoidable. `intentTypeName` is a
parallel switch over the same closed set as `applyIntents`; a new intent that is forgotten in
one of the two compiles clean and fails only at runtime (the `default: "Unhandled"`/`"Unknown"`
arms), which is the *obscurity* symptom: the error appears far from the cause.

**Direction:** make the name a method on the interface (`IntentName() string`, implemented on
`intentBase` or each type), so the name travels with the type and the logging switch disappears.
Then the *only* place that must grow per-intent is `applyIntents` (plus the constructor).
Alternatively, `intentTypeName` should be implemented via `reflect.TypeOf(intent).Name()`
with the struct names normalized, eliminating the parallel switch entirely. (The drain switch
itself is fine; the point is to not maintain the *same* list twice.)

### 3.3 Change amplification + obscurity: the dual catalog model (`App` vs `AppDefinition`)

**Severity: C1.**

Evidence:

- `models.go` defines `App` (the full model: name, display, containers, SSO, ports, …).
- `types.go` defines `AppDefinition` (name + integrations) plus `Integration`/`Provides`/
  `CompatibleApp`/`ContractProvides`.
- `loader.go` has **two walkers**: `LoadAll` (→ `map[string]*App`) and `LoadGraph`
  (→ `[]*AppDefinition`): that duplicate the `os.ReadDir` / skip-no-metadata / read / unmarshal
  logic almost line-for-line.
- `App.Integrations` is typed `map[string]Integration`, i.e. the *full* model reaches into the
  *planning* model's types, but the two top-level structs stay separate.

So the same `metadata.yaml` is parsed into two different Go types by two different functions,
and the two types overlap on `Integrations` but not on identity (`CatalogID` vs `Name`) or
anything else. Consequences, all APoSD symptoms:

- **Change amplification:** a new top-level metadata field must be added to `App` (and its
  validation) and, if the planner needs it, to `AppDefinition`: two structs, two yaml tags,
  two loaders.
- **Obscurity:** "which model is authoritative for what?" is an answer you must hold in your
  head. The orchestrator converges from `App`; the install planner plans from `AppDefinition`.
  The `catalog.AppGraphInterface` the orchestrator holds is built from `AppDefinition`, while
  the catalog cache is built from `App`. A reader has to notice these are *different graphs of
  the same directory*.

**Direction:** collapse to one model. `AppDefinition` is a strict subset of `App`; the planner
only needs `Name` + `Integrations` + `Provides`. Either (a) make the planner read the `App`
cache directly and delete `AppDefinition` + `LoadGraph`, or (b) embed the shared fields
(`Integrations`, `Provides`) in a shared struct that both compose. Option (a) is the APoSD
answer: one loader, one model, the planner and the reconciler agree because they read the same
object. The `AppGraph` reverse-index (`dependents`) can be derived from `[]*App` just as it
is today from `[]*AppDefinition`.

### 3.4 Change amplification: `AppStore` column list and scan logic in four places

**Severity: C2.**

Evidence (`store/apps.go`):

- The 20-column `SELECT … LEFT JOIN operations` appears verbatim in `GetAll` **and**
  `GetByCatalogID`.
- `scanApp` (from `*sql.Rows`) and `scanAppRow` (from `*sql.Row`) are near-identical ~55-line
  functions that repeat the `Scan(...)` argument list and the same `port`/`configJSON`/`op*`
  post-processing.

Adding a column (or the `Operation` join shape changing) touches **four** places: two SQL
strings and two scan bodies. `database/sql` gives no shared `Scanner` interface over `*Rows`
and `*Row`, which is *why* the duplication exists: but that is a reason to lift the scan,
not to accept the duplication.

**Direction:** extract a single `scanAppColumns(scan func(...any) error) (*InstalledApp, error)`
taking a `func(dest ...any) error` (which both `rows.Scan` and `row.Scan` satisfy), and have
both `GetAll` and `GetByCatalogID` pass their scanner. The column list then lives once in
each query and the `Scan` order lives once in `scanAppColumns`. This is a mechanical,
low-risk consolidation of the same shape already applied to `pkg/servarr`.

### 3.5 Wide interface: `AppStoreInterface` (15 methods) shared by two unequal consumers

**Severity: C3.**

`interfaces.go`'s `AppStoreInterface` exposes 15 methods (get/set status, last-error, tailnet-id,
SSO strategy, integration config, display name, install/uninstall, is-installed, onChange…).
The orchestrator and the API layer both consume it, but they use *different* subsets, and the
method names leak the table's columns (`SetTailnetID`, `GetSSOStrategy`, `SetSSOStrategy`)
rather than the caller's intent. APoSD: "an interface is a tool for hiding information; if it
exposes more than it hides it is shallow." A 15-method interface is a sign the abstraction is
the table, not the behavior.

This is lower severity because it is *internal* and the two consumers are close; but it is
the same disease as 3.1 at the store layer, and the cure is the same: define the *two*
role interfaces the callers actually need (an orchestrator writer interface, a handler reader
interface) and let `*AppStore` satisfy both. Go makes this cheap (interfaces at the call site).
The concrete `AppStore` stays as-is; only the interface shapes change.

### 3.6 Mixed abstraction: `appclient.Call` serves three modes in one struct

**Severity: C2.**

`pkg/appclient/call.go` is a fluent builder (~470 lines) whose single `Call` struct carries
the union of three concerns:

- a **plain** request (method/path/body/headers/query/timeout);
- a **declared-idempotency** request (`okStatuses`/`alreadyStatuses`/`alreadyFunc`/
  `declaredContract`/`noRetry`);
- a **readiness wait** (`ready`/`interval`/`stable`/`tolerateFailures`/`Within`/`retryOverride`).

Fields that are meaningful in one mode are inert in the others, and several invariants span
modes (`retriesAllowed()` reasons about `x.ready`, `x.noRetry`, `x.declaredContract`, and the
verb, in one expression; `Within` reaches into `retryOverride` and sets `budgetErr` that `Wait`
surfaces later). The machinery is *correct*: the `Within`/`Timeout` distinction and the
`budgetErr`-surfaced-at-`Wait` behavior are genuinely good, but the cost is cognitive: to
understand any one call you must hold the whole struct's state machine.

This is the APoSD "general-purpose vs special-purpose" tension resolved too far toward
"one general thing." A `Call` that is *either* a one-shot *or* a wait is two abstractions
wearing one struct.

**Direction:** consider splitting `Wait` into its own builder type that *wraps* a plain
`Call` (`client.GET(p).Wait(...)` returning a `Wait`), so the wait-only fields
(`ready`, `interval`, `stable`, `tolerateFailures`, `Within`) move off `Call`. The
idempotency-contract half (`OK`/`AlreadyDone`/`Ensure`) arguably stays on `Call` because it
is used by both. Even documenting the three modes with a state diagram in the package comment
would reduce the obscurity, but the field split is the real fix. Mark as lower priority:
this is a working, well-tested abstraction and a split is a behavior-preserving move.

### 3.7 Information leakage: store callers enforce a store-level invariant

**Severity: C2 (obscurity).**

`AppStore.Install` unconditionally does `ON CONFLICT … status = 'installing'`. The invariant
"do not downgrade a *running* app to `installing` on a reinstall" is enforced **by callers**,
not by the store:

- `orchestrator.go` `recordInstallNow` checks `existing.Status == "running"` and returns early,
  with a comment explaining *why* downgrading would strand the app at "installing";
- `pipeline.go` `recordIntent` checks the same and returns early.

Two call sites re-derive the same guard, each with its own comment, because the store cannot
be trusted with the call. The knowledge ("a running app must stay running") lives in the
orchestrator, where it is correct, but the *risk* lives in the store, where a future third
caller will forget it. This is textbook information leakage: the store's `Install` is a
lower-level primitive than its callers want, so the higher-level policy is copied.

**Direction:** put the policy in the store: `Install` should keep `status` when the existing
row is already `running` (or add a distinct `Reinstall` that does). Then the two caller guards
and their comments collapse to one documented rule in one place. (The same pattern appears
around `recordIntent`'s "skip if running" check.)

### 3.8 Obscurity: stringly typed statuses and phases

**Severity: C3.**

`InstalledApp.Status` is a `string` (`"installing"`, `"uninstalling"`, `"running"`,
`"error"`), `Operation.Type`/`Phase`/`Status` are `string`s, and the graph has a parallel
`graph.Status*` enum. The store layer compares against string literals
(`app.Status != "uninstalling"`, `existing.Status == "running"`) throughout the orchestrator,
and `UpdateStatus`/`SetSSOStrategy`/`SetLastError` all accept bare strings. A typo in a status
literal is a runtime failure (an app silently never converges, or never uninstalls), which is
exactly the *unknown unknown* APoSD warns about. The graph layer already has a typed status;
the store layer has not caught up.

**Direction:** introduce typed constants (`store.AppStatusRunning`, …) or reuse the graph's
status type at the store boundary, so an invalid status cannot be constructed. Low urgency,
high safety dividend.

### 3.9 Operation recorder keys on the app while the scheduler keys on the node

**Severity: C2 (already known: ledger item 5; reframed here as an abstraction mismatch).**

`recordOpPhase`/`recordOpFail` key the operations row on `ownerApp(id)` (one row per app), while `processLevel` dispatches **nodes** concurrently within a level. For a multi-container
app, N nodes issue `UPDATE operations WHERE app_name='authentik'` against a single row at the
same time. The ledger documents the resulting `SQLITE_BUSY` loss and notes the value-identical
writes usually mask it. APoSD framing: the recorder's key granularity (app) is coarser than
the engine's unit of work (node), so the two abstractions disagree about what "one drive" is.
The fix is not a lock; it is to make the recorder's unit match the engine's unit (one row per
node, aggregated to the app at read time), which removes the contention *and* the
arbitrary-survivor ambiguity in the same move.

### 3.10 The `dev/` hot-reload loop and the CLI are a separate complexity budget

**Severity: note, not a finding.**

`cli/` (48 files, `dev_*`/`e2e_*` families) and the native-backend hot-reload machinery
(`dev_watch.go`, `dev_hotreload.go`, `dev_output.go`) carry a *lot* of incidental complexity
(process-group signaling, log mirroring, vite proxying, backend selection) that is orthogonal
to the product's core. It is well-documented in AGENTS.md and behaves, but it is worth flagging
that the CLI's dev/e2e surface is the *other* large complexity sink in the repo and would
benefit from its own APoSD pass (the `dev_*.go` file split is already a step toward it).

---

## 4. Ranked recommendations

Ranked by (complexity removed) × (safety). All are structural; none change behavior.

| # | Recommendation | Kills symptom | Effort | Risk |
|---|---|---|---|---|
| 1 | Collapse `App`/`AppDefinition` into one catalog model + one loader (3.3) | change amplification, obscurity | M | Low–Med (tests pin both) |
| 2 | Group `OrchestratorConfig` into subsystem structs; make `wire.Input` compose them (3.1) | cognitive load, wide interface | L | Med (touches every test's config) |
| 3 | Move the "running app is never downgraded" policy into `AppStore.Install` (3.7) | information leakage | S | Low |
| 4 | Make `intentTypeName` a method / `reflect` name; delete the parallel switch (3.2) | change amplification | S | Low |
| 5 | Extract `scanAppColumns(scan func(...any) error)`; one Scan order (3.4) | change amplification | S | Low |
| 6 | Split role interfaces for `AppStoreInterface` at the call sites (3.5) | wide interface | S | Low |
| 7 | Split `appclient.Call` wait mode into a `Wait` builder (3.6) | cognitive load | M | Med |
| 8 | Type the store statuses/phases (3.8) | unknown unknowns | S | Low |
| 9 | Key operation rows by node, aggregate at read (3.9; supersedes ledger item 5's band-aid) | obscurity + contention | M | Med |

Items 3, 4, 5, and 8 are one-commit, behavior-preserving cleanups; 1, 2, and 9 are the
structural moves. Item 2 is the one I would do *first* if only one is done, because it is the
root of the widest surface and everything else in the orchestrator is downstream of it.

---

## 5. Closing observation

The single most telling artifact in this repo is not a bug: it is the existence of
`wire.Output.Config` with the comment "exposed so a test can assert that every field the
type declares was deliberately set." That is a symptom being treated as a feature. APoSD's
core claim is that complexity is what makes a system expensive to change, and the honest
reading of that comment is: *the configuration surface has grown complex enough that we no
longer trust ourselves to wire it, so we wrote a machine to check us.* The right response is
not to keep the checker; it is to shrink the surface the checker has to watch. The codebase
has already proven it knows how to do this: `hostset` did it for the address, `pkg/servarr`
did it for the PVR apps. The same move, applied to the orchestrator's configuration and the
catalog's dual model, is the highest-value work available in this tree.
