# Bloud: APoSD review of the app-extension surface (2026-10-12)

**Reviewer:** external code review through the lens of John Ousterhout's
*A Philosophy of Software Design* (APoSD).
**Scope:** the surface an app author faces to add or update a catalog app, and
the architecture that surface sits on: `services/host-agent/pkg/configurator`,
`services/host-agent/internal/catalog`, the orchestrator's configurator drive and
integration resolver (`internal/engine/orchestrator`), and the registration /
conformance / validation layers in `apps/`. The frontend, CLI, and the broader
runtime are read for context only.
**Code under review:** `e66f2f6` ("external apps: close the add modal once
something is added"), `origin/main`.

**Objective function.** The review is measured against one goal: *adding an app,
and updating an app later, should be as close as possible to a change scoped to
`apps/<name>/`.* Everything below is graded on whether it moves the system toward
or away from that.

**Relationship to prior artifacts.** This is the third review in the series, and
it is the one aimed at the goal above:

- [`review-2026-10-03-aposd.md`](review-2026-10-03-aposd.md) reviewed the
  *runtime's* module shapes (orchestrator config, store interfaces, appclient).
- [`../plans/archive/app-add-tax.md`](../plans/archive/app-add-tax.md) measured the *workflow*
  tax: which files outside `apps/<name>/` an app add touches, and six shippable
  derivations to remove it.
- This review covers the *architecture of the extension surface itself*: why the
  tax exists, at the level of the types an author compiles against. The plan
  removes the registries; this review asks whether the registries should have to
  exist at all.

Where a finding overlaps the ledger, the ledger
([`../operations/tech-debt.md`](../operations/tech-debt.md)) is the source of
truth and is referenced rather than duplicated.

---

## 0. Status since publication (updated 2026-10-15)

This review is a snapshot of `e66f2f6`. Two days of work closed most of it. The
findings below are left as written, because the reasoning is the value; this table
says what is true now so a reader does not act on a closed item.

| Finding | Severity | Now | What closed it |
|---|---|---|---|
| 3.1 One package, three audiences | C1 | Partly closed | `bindings` split out of `pkg/configurator` (#341); `interface.go` went from 576 lines to 249. The host plumbing (`Deps`, `AppSecretsProvider`, `Registry`) still shares the author-contract package. |
| 3.2 `Deps` is a flat god-parameter | C2 | Partly closed | Four leaf fields plus three subsystem values (#335). `appclient` and `appasset` types are still in every configurator's compile closure. |
| 3.3 The contract triangle | C1 | Partly closed | The `bindContract` switch became the `contractBinders` table, and `contract_binders_test.go` fails a payload-bearing contract with no binder (#325). The "later" half, moving the vocabulary out of `internal/`, is open. |
| 3.4 Three in-module registries | C2 | Closed, at the scope this review endorsed | `DefaultPort` left the conformance table; the configurator reports its own port (#334). `NodeNames()` and the flags stayed, which is what this review argued for. |
| 3.5 The module boundary is not a boundary | C2 | Closed | `apps/go.mod` requires `codeberg.org/d-buckner/bloud/pkg`, and `services/host-agent/pkg/` is gone (#341). |
| 3.6 "Empty means not-ready" re-derived in prose | C2 | Closed | One `Secret` type states the rule once (#333). |
| 3.7 The declarative-only path is real but not led with | C3 | Open | Nothing changed. Every user app still registers a configurator, and the author guide still opens with the Go file. |

The two plans this review produced both shipped and are archived:
[app-add-tax.md](../plans/archive/app-add-tax.md) (all six derivations) and
[configurator-sdk-surface.md](../plans/archive/configurator-sdk-surface.md) with
its child [app-sdk-module.md](../plans/archive/app-sdk-module.md).

---

## 1. Verdict up front

The declarative half of the extension surface is exceptional; the imperative half
is where the goal leaks away. An author can declare almost everything about an app
in `metadata.yaml`, and the catalog loader turns that declaration into a set of
cross-file agreements that fail the load instead of failing at runtime. That half
already achieves "change scoped to the app dir." But the moment an app needs
anything beyond static containers, the author steps into `pkg/configurator`, and
that package is a single wide module that bundles three different audiences, a
flat eleven-field dependency bag, and a twelve-slice integration vocabulary whose
bindings are wired in a parallel switch. The same fact ("this app exists") then
has to be re-stated in three registries inside the app module and two or three
outside it, each compensated for by a completeness test. That is the APoSD
diagnosis: the surface is wide enough that the team no longer trusts itself to
keep the copies in sync, so it wrote machines to check. The machines are good;
the width they guard is the debt.

The one structural lever bigger than any registry derivation is already present
but not led with: the orchestrator has a fully working **declarative-only path**
(`runFullLifecycle` handles `cfg == nil`), so an app with static containers and no
hook needs no Go at all. Nothing in the author workflow leads with it.

---

## 2. The bar: what the extension surface does right

These are the parts to protect. Every recommendation below must not regress them.

### 2.1 The metadata schema and loader: errors made impossible at load time

`internal/catalog/models.go` + `types.go` + `contracts.go` + `loader.go` together
are the best declarative extension surface I have read. The loader's `validateApp`
tree (`loader.go`) holds every cross-file agreement that no compiler sees and
rejects it with a message that names *what the consumer would wrongly read*, not
just "invalid":

- `validateRequiredDefault` refuses a `required` integration with no
  `default: true` entry, because the failure is "an empty credential forever,
  symptom nowhere near the cause."
- `validateContractSecrets` / `validateContractValues` hold a provider's
  `provides:` offer to the contract registry, so a half-empty binding cannot be
  constructed.
- `validateSharedNamespaces` catches a collision (two apps claiming the same
  `icsFeed.calendarName`) that is invisible in either app's own file and only
  exists once the whole catalog is on the table.
- `resolveOfferPort` refuses to fall back to the UI port when a contract names an
  `extraPorts` entry that does not exist, because the fallback "connects, speaks
  the wrong protocol, and points at an app that did nothing wrong."

This is the APoSD "push errors to the earliest possible point" principle applied
with unusual discipline, and it is *the* reason adding a declarative app is cheap.
The contract registry (`contracts.go`) carries the *why* for every entry, and the
`ValueSpec` shape rules (`AbsolutePath`, `PathSegment`, `Optional`) are a small,
honest type system for "what may a contract value be."

### 2.2 The conformance harness: a doc comment that enforces itself

`apps/configtest/configtest.go` is the best single artifact in the tree for the
goal. Its package comment states the philosophy outright: *"NodeLifecycle documents
what a configurator may do in each phase, but a doc comment enforces nothing, and
thirteen apps were reading the same contract thirteen different ways."* The harness
turns the interface's promises into assertions every app is held to: PreStart is
offline, PreStart is idempotent, a second pass asks for no recreate, teardown is
not a lie, `Name()` matches the registered node, and the constructor's default
port matches `metadata.yaml`. A new app is covered the moment it registers.

This is exactly the right answer to the question "how do you keep N independent
authors honest against one contract." It is a deep module: the complexity of
checking the contract is hidden behind `configtest.Run(t, tc)`, and the app author
pays a table row, not a checklist.

### 2.3 `ProviderRef` and the typed-slice `Integrations`: least privilege done right

`ProviderRef` (`pkg/configurator/interface.go`) collapses "who is this provider,
where is it, is it installed" into one embedded value, and the contract payloads
embed it rather than repeating it. The `Kind`/`Installed`/`BaseURL`/`LocalURL`
fields make "a consumer must not branch on where the provider lives" an
*enforceable* property instead of a convention. The least-privilege gate in
`publishedSecret` (a consumer only receives the secret it declared under
`requires`) is the right security shape, and it runs before the external-provider
branch so the rule has one home.

### 2.4 `pkg/servarr`: the general-purpose-module outcome

Sonarr and Radarr are ~45 lines each over a shared `PVRConfigurator` parameterised
by a `PVRApp` value. `apps/sonarr/configurator.go` is 42 lines and the value is
the whole of the difference. This is the APoSD "general-purpose vs special-purpose"
tension resolved correctly: one module, two apps, the interface depth unchanged.

### 2.5 The lazy factory registry: cheap, and the right granularity

`RegisterFactory` / `MustRegisterFactory` + the lazy `Registry.Get` double-checked
instantiation mean an app is constructed only when it is actually reconciled.
`apps/registry.go`'s blank-import mechanism is idiomatic and greppable, and
`TestRegisterAll` + `MissingConfigurators` catch a drift between the blank import
and the `NodeNames()` list. The mechanism is sound; the finding below is about how
many *parallel* lists sit on top of it, not about the mechanism itself.

### 2.6 The declarative-only path already exists in the runtime

`runFullLifecycle` (`internal/engine/orchestrator/execution.go`) handles a node
with `cfg == nil && def != nil` by running container phases and skipping
PreStart/PostStart. A node with no container and no configurator is marked RUNNING
after route generation. In other words: an app that is fully expressible as
`metadata.yaml` (containers, health checks, volumes, a `none` SSO strategy, a
static `provides:` offer) needs **no configurator, no registration, no conformance
row**. The runtime already models it. The author workflow simply does not lead with
it, and no current app takes the path.

---

## 3. Findings

Severity is **C** (complexity/design, not a runtime bug) as in the prior review.
Evidence is `file:line` where it is stable.

### 3.1 One package, three audiences: `pkg/configurator`

**Severity: C1 (highest).**

`pkg/configurator` is a single package (`interface.go` is 576 lines) that serves
three different readers with one import:

1. **The author contract**: `NodeLifecycle`, `AppState`, `PreStartResult`,
   `PhaseBudget`, `SessionRevoker`, `Remover`. This is what an app author must
   know to write a configurator.
2. **The host plumbing**: `Deps` (11 fields), `AppSecretsProvider` (7 methods),
   `ExecFunc`, `ContainerRunningFunc`, `RegistryInterface`. This is what the
   orchestrator provides and the app receives, and it grows every time the host
   exposes a new capability to configurators.
3. **The integration vocabulary**: `ProviderRef`, `ProviderKind`, and 12 payload
   types (`PVRBinding` … `ICSFeedBinding`) plus the 12-field `Integrations`
   struct. This is what a *consumer* of a contract reads.

All 20+ app packages import this one package, so every app compiles against the
whole vocabulary. A Sonarr (which needs `NodeLifecycle`, `Deps`, and nothing else)
is no closer to a minimal surface than arr-mcp (which reads four contracts). APoSD:
*"an interface is a tool for hiding information; if it exposes more than it hides,
it is shallow."* This package is the interface between the runtime and every app
that will ever be written, and it is wide in proportion to the host's capabilities
rather than to any one app's needs.

The tell, same as the prior review's `wire.Output.Config`: the three audiences are
kept honest not by a small surface but by tests (`TestRegisterAll`,
`TestConformanceTableIsComplete`, the conformance assertions). The tests are
excellent; their existence is the symptom.

**Direction.** Split the package along the audiences, or at least separate the
author contract from the vocabulary so a plain app imports one small package.
Go makes this cheap because the three audiences are already mostly disjoint types.
A `configurator` package that holds `NodeLifecycle` + `AppState` + `PreStartResult`
+ `PhaseBudget`, and a sibling `bindings` (or `integrations`) package that holds
the 12 payload types and `Integrations`, would let a hook-less or single-contract
app import a fraction of what it imports today. The host plumbing (`Deps`) can move
behind interfaces defined in the author package, which also fixes 3.2.

### 3.2 `Deps` is a flat god-parameter

**Severity: C2.**

`Deps` (`pkg/configurator/factory.go`) carries 11 fields: `Logger`, `Secrets`,
`OperatorEmail`, `OperatorUsername`, `PrimaryBaseURL`, `TraefikPort`,
`RestartContainer`, `Exec`, `ContainerRunning`, `HTTP`, `Assets`. Every field
documents a nil contract ("may be nil in degraded/CLI contexts; factories must
tolerate nil"), and the zero value is "usable" because `ClientFactory` and
`appasset.Installer` were made lazy. That is honest, and it is the same flat-bag
pattern the 10-03 review flagged in `OrchestratorConfig`: the "nil = subsystem
disabled" decision is repeated per field instead of living in each subsystem's
zero value.

The cost to the goal is concrete: `Deps.HTTP` and `Deps.Assets` drag
`pkg/appclient` and `pkg/appasset` into the compile surface of every configurator,
including the ones that only write a file. Adding the next host capability
(a new function field) is a change to the one struct every app compiles against.

**Direction.** Group `Deps` into subsystem values (a `Runtime` struct holding
`RestartContainer`/`Exec`/`ContainerRunning`, an `Identity` struct holding the
operator functions, a `Networking`/`HTTP` struct), or define per-need interfaces
at the call site (`deps.Runtime`, `deps.Secrets`). The goal is that a file-writing
configurator declares the capabilities it uses rather than accepting eleven of
them. See also 3.5: the cleanest fix is to make `Deps` itself the boundary of a
separate app-SDK module, which forces the grouping.

### 3.3 The contract triangle: `contracts.go` / `Integrations` / `bindContract`

**Severity: C1.**

A new payload-bearing contract is a three-file edit, and the repo has codified it
as invariant 15 ("a contract entry, a payload type in `pkg/configurator`, and one
arm in `bindContract`"). The three sites are:

1. `internal/catalog/contracts.go`: the `Contract` entry (name, secrets, values,
   `SatisfiedBy`). 15 entries today, 12 of them payload-bearing.
2. `pkg/configurator/interface.go`: a payload struct and a new field on
   `Integrations` (12 slices).
3. `internal/engine/orchestrator/integrations.go`: an arm in `bindContract`'s
   switch (11 arms), plus one contract, `inference`, resolved outside the switch
   by `resolveInference` because it alone can be served by a provider the
   consumer never named.

The arms are shape-identical restatements of the registry: `pvr` is `{secret}`,
`sso` is `{secret}`, `mcp` is `{secret, serverName, path}`, and so on. APoSD names
this change amplification precisely: the same vocabulary is represented in three
places that must change together, and the join between them is a string switch
with a `default:` arm whose only action is `warnUnboundPayload`, a *log line*.

That default arm is the sharpest finding in this review. There is no completeness
test binding `bindContract`'s arms to `catalog.Contracts()` (the metadata-side
test in `contract_declarations_test.go` proves offers match the registry, but no
test proves the registry matches the binders). So the one place where a forgotten
binding could turn into "a consumer silently receives nothing" is guarded by the
weakest possible signal, in a codebase whose defining habit is replacing warnings
with build failures. `apps/affine-mcp`, `apps/ha-mcp`, `apps/arr-mcp`, and
`apps/dav-mcp` each added a contract in the last two months, so this is not a
theoretical path: it is the most actively edited surface in the tree.

**Direction.** Two steps, as `../plans/archive/app-add-tax.md` change C already lays out,
reframed here as the architecture question it is:

- **Now (cheap, no behavior change):** replace the `bindContract` switch with a
  binder table keyed by contract name, and add the house-pattern completeness test
  (every payload-bearing contract has exactly one binder, every binder names a
  declared contract). This turns the log line into a build failure and shrinks the
  per-contract edit from three files to two (registry + payload/binder, which can
  live together). This is the same shape the repo already uses for
  `pinned-images` `EXCEPTIONS` and `file-length` `BASELINE`: an exemption that
  guards nothing is itself a failure.
- **Later (the real fix, with the trade-off stated):** the contract vocabulary is
  app-domain knowledge sitting in `internal/`, so declaring new vocabulary means
  editing the runtime. It should be catalog data, loaded with the apps. The honest
  cost is generic payloads: `binding.Values["path"]` trades change amplification
  for obscurity (the stringly typed status disease the 10-03 review flagged in
  finding 3.8). That trade is real and must not be waved away; typed payloads are
  a load-time correctness check the generic form loses. The resolution is to keep
  the *consumer-facing* binding typed but make the *binder* generic over the
  declared `Secrets`/`Values` spec, so only genuinely new wire shapes need a new
  type. This is a design problem worth its own plan, not a rename.

### 3.4 Three in-module registries, each compensated by a test

**Severity: C2.**

Inside the app module itself, one app add touches three hand-maintained lists:

1. `apps/registry.go`: a blank import **and** an entry in `NodeNames()`.
2. `apps/conformance_test.go`: an `appSpec` row (`Dir`, `Node`, `DefaultPort`,
   `WithSSO`/`WithLDAP`/`WithAgent`/`WithAppAPI`/`OwnsTeardown`).
3. (conceptually) the app's own `registration.go` `MustRegisterFactory(nodeName, …)`.

Each is kept honest by a test: `TestRegisterAll` pins (1), `TestConformanceTableIsComplete`
pins (2). `NodeNames()`'s duplication is a *deliberate stability pin* (a rename
must be an explicit edit, not a refactor side effect), and the conformance row's
flags are real inputs about the configurator's code paths (Radicale is `ldap` and
needs both `WithSSO` and `WithLDAP`). So this is not "delete the duplication"; it
is "the three lists are the same fact stated three ways, and the ways are only
partly derivable." The plan's change F correctly keeps `NodeNames()` and the flags
while dropping only `DefaultPort` (which is a third copy of a fact the harness
already compares). I endorse that split and would not go further.

The APoSD point is not the lists; it is that the *node name* is the one fact with
no single source. It appears as the `containers[].name` in metadata, the
`MustRegisterFactory` key, the `Name()` return, the `NodeNames()` entry, and the
conformance row, and only `TestRegisterAll` + `AssertNodeName` hold them together.
A configurator whose `Name()` is derived from the metadata node name (the way
Sonarr derives `nodeName` from `app.NodeName()`) has one source; a configurator
that types the string again has two. The framework could make the single-source
case the default by deriving the registration key from the metadata node name at
`RegisterAll` time, but that runs into the same deliberate-pin argument above, so
this stays a judgment call per app, not a rule.

### 3.5 The module boundary is not a boundary

**Severity: C2.**

`apps/go.mod` requires `codeberg.org/d-buckner/bloud/services/host-agent` with a
`replace … => ../services/host-agent`. The app module therefore does not depend on
a slim "app SDK"; it depends on the whole host-agent module, and `pkg/configurator`
reaches into `pkg/appclient` and `pkg/appasset` (via `Deps.HTTP` and `Deps.Assets`),
so those two packages are in every app's compile closure. The `replace` also makes
the two modules co-located by construction: they cannot version independently, and
"build the apps" always means "build against the local tree."

The consequence for the goal is subtle but real: the thing a reviewer reads when
they open an app dir is `go.mod` requiring the *runtime*, not a documented,
versioned, minimal contract. There is no published boundary an author can compile
against without the rest of the monorepo. Invariant 14 (shared packages stay free
of host internals) is the discipline that *could* support a `pkg/configurator` +
`pkg/appclient` + `pkg/appasset` extraction into their own module, but as long as
they live inside `services/host-agent`, the apps module's dependency is the whole
service.

**Direction.** Extract the author-facing surface into its own Go module
(`pkg/configurator` plus the packages it imports for its public types:
`appclient`, `appasset`, `managedfile`, `servarr`, `slug`, `xmlutil`). This is the
structural version of the plan's goal: a change to `apps/<name>/` would then
compile against a published SDK, and a change to the host's internals would no
longer re-enter the app module's dependency graph. It is a larger move than the
plan's six PRs, and it is the one that makes the "app dir" a real boundary rather
than a directory convention. Do it after 3.1 and 3.2, because those define what
the SDK's surface is.

### 3.6 "Empty means not-ready" is re-derived in prose, once per binding type

**Severity: C3 (obscurity).**

Twelve payload types each carry a comment spelling out the same rule in its own
words: an empty `APIKey`/`Token`/`Password` means "the provider has not published
it yet", never "an empty credential", and a consumer must treat it as not-ready
and write nothing. `MCPBinding.Token`, `AppAPIBinding.Password`,
`RequestManagerBinding.Password`, `MediaServerBinding.AdminPassword`,
`ICSFeedBinding.APIKey`, `AgentAPIBinding.APIKey`, `PVRBinding.APIKey`,
`SSOBinding.APIToken` all restate it. The rule is real and load-bearing (it is what
lets a consumer distinguish "the provider is not up yet" from "the provider gave
me nothing"), but it is the same invariant written N times with N slight
variations, which is exactly the obscurity that invites the next binding to get
it subtly wrong.

This is low severity because the prose is good and the conformance harness would
catch a configurator that misreads it. It is worth naming because it is the
*other* cost of the typed-slice design in 3.3: every new payload re-derives the
empty-signal contract by hand. A shared `Secret` wrapper type (a named string with
a `Published() bool` method and a doc comment) would state the rule once and make
the next binding inherit it.

### 3.7 The declarative-only path is real but not led with

**Severity: C2 (directly against the goal).**

The runtime already converges a `cfg == nil` app (finding 2.6). But
`AGENTS.md`'s "Adding an app" step 3 is "`apps/<name>/configurator.go`",
`guides/contributing-apps.md` makes a configurator a required step, and every one
of the 22 registered user-app nodes has one. So the cheapest possible app add,
the one that should be the first thing an author reaches for, is not a documented
path, not exercised by any catalog entry, and not surfaced by the conformance
harness (which only iterates apps that register a configurator).

For the goal, this is the largest single lever. A plain app (one container, a
health check, a `none` SSO strategy, maybe a static `provides:` secret minted by
the host) should be pure `metadata.yaml` + `icon.png`. Today that app would still
be told to write a configurator, a registration, a conformance row, and registry
entries, because the author workflow does not distinguish "I need a hook" from
"I need the runtime to run my container."

**Direction.** Make the declarative-only path a first-class, tested, documented
author path: a catalog app with no `registration.go` and no conformance row is
legitimate, the conformance harness and `validation.yaml` derivation treat it as
such, and `guides/contributing-apps.md` leads with "do you need Go at all?" before
"write a configurator." A fixture app with no Go files is the regression test: it
installs, converges, and appears in the catalog, proving the no-hook path cannot
rot. This is the change that most directly advances "minimal changes scoped to
the app dir," because for the largest class of apps it makes the change *only* the
app dir.

---

## 4. Credit where due: what changed since the 10-03 review

The prior review was against `6e1e2ef`; this one is against `e66f2f6`, nine days
and several merges later. Its findings have been paid down faster than any debt
ledger I have watched:

| 10-03 finding | Status at `e66f2f6` |
|---|---|
| 3.3 dual catalog model (`App` / `AppDefinition`) | **Resolved.** `AppDefinition` is gone; `LoadGraph` is a thin wrapper over `LoadAll` ("one loader, one model"). |
| 3.4 `scanApp` / `scanAppRow` duplication | **Resolved.** `scanAppColumns(scan func(...any) error)` exists; `GetAll` and `GetByCatalogID` pass their scanner. |
| 3.2 `intentTypeName` parallel switch | **Resolved.** The name is `reflect`-derived from the type, "enumerated exactly once (in `applyIntents`)." |
| 3.8 stringly typed statuses | **Mostly resolved.** `AppStore.UpdateStatus` takes a typed `AppStatus`. |
| 3.1 `OrchestratorConfig` subsystem grouping | Open. |
| 3.5 `AppStoreInterface` role split | Open (still 15 methods). |
| 3.6 `appclient.Call` wait-mode split | Open. |
| 3.7 store `Install` downgrade policy | Open. |
| 3.9 operation-row keyed by app vs node | Open. |

This matters for the review's credibility and for the plan's sequencing: the team
is already doing the strategic work the prior review asked for. The findings in
section 3 are the *next* layer, aimed at the author surface rather than the
runtime core, and none of them contradict the open items above.

---

## 5. Ranked recommendations

Ranked by (distance to the goal) × (safety). The first three directly serve
"changes scoped to the app dir"; the rest are the structural moves that make the
first three cheap.

| # | Recommendation | Kills | Effort | Risk |
|---|---|---|---|---|
| 1 | Make the declarative-only path first-class: document it, test it with a fixture no-Go app, let the conformance harness and `validation.yaml` derivation treat hook-less apps as complete (3.7) | the largest class of out-of-dir changes | S | Low |
| 2 | Binder table + completeness test over `bindContract` vs `Contracts()` (3.3 now-step, = plan change C1) | change amplification + the only log-guarded contract failure | S | Low |
| 3 | Drop the conformance `DefaultPort` column, read the port from metadata in the harness (plan change F) | a third copy of one fact | S | Low |
| 4 | Split `pkg/configurator` along its three audiences; narrow what a plain app imports (3.1) | cognitive load + wide interface | M | Med (touches every app's import) |
| 5 | Group `Deps` into subsystem values; define per-need capability interfaces (3.2) | god-parameter, compile-surface creep | M | Med |
| 6 | Extract the author-facing surface into its own module; make `apps/` depend on an SDK, not the runtime (3.5) | module boundary is not a boundary | L | Med |
| 7 | Shared `Secret` wrapper type for "empty means not-ready" (3.6) | obscurity, once per binding | S | Low |
| 8 | Contract vocabulary as catalog data with a generic binder and typed consumer payloads (3.3 later-step, = plan change C2) | the root of the contract triangle | L | Med-High (the generic-vs-typed trade) |

Recommendations 1, 2, and 3 are one-PR, behavior-preserving (1 is additive), and
are the ones to do first. 4 and 5 are the same move the prior review asked of
`OrchestratorConfig`, applied to the author surface, and are prerequisites for 6.
8 is the only one that changes the runtime's relationship to the catalog and
deserves its own plan with the generic-vs-typed trade examined in writing before
any code.

---

## 6. How to know it worked

Measure against the goal, using the same method as `../plans/archive/app-add-tax.md`:

| Metric | Today | Target |
|---|---|---|
| Files an author must touch outside `apps/<name>/` for a plain app | `apps/registry.go` (2), `apps/conformance_test.go` (1), `validation.yaml` (1), `e2e/tests/<name>.spec.ts`, host-agent integration test | 0 for a declarative-only app; 2 (the two tests) for a hooked app |
| Hand-maintained registries an app must join | 7 (blank import, `NodeNames()`, conformance row, `validation.yaml`, three backend port maps) | 3 (blank import, `NodeNames()`, conformance row), and 0 of them for a declarative-only app |
| Contracts whose forgotten binder is a build failure | 0 of 12 | 12 of 12 |
| Packages a file-writing configurator compiles against | `configurator` + `appclient` + `appasset` + their closure | `configurator` + what it declares |

If a new app still requires a hand edit outside `apps/<name>/` for a fact it
already declared in `metadata.yaml`, the surface is not done; name the missing
derivation rather than absorbing it.

---

## 7. Closing observation

The prior review ended on `wire.Output.Config` and its comment "exposed so a test
can assert that every field the type declares was deliberately set": a symptom
being treated as a feature. This review's version of the same tell is
`TestConformanceTableIsComplete`, `TestRegisterAll`, and `AssertPortMatchesMetadata`
on one side, and the *absence* of a binder-completeness test on the other. The
three tests exist because the same fact (this app exists, on this node, at this
port) is written in several places that must change together; the missing test is
the one place the team has not yet compensated, and so it is the one place a
forgotten change still surfaces as a log line instead of a red build.

The codebase's instinct, everywhere, is to respond to width by adding a checker.
That instinct is correct and has produced the best extension surface I have read.
But the goal stated for this review is not "check the copies"; it is "stop making
the copies." The declarative half of the surface already proves the team knows how:
`metadata.yaml` plus the loader is one fact, one place, errors at load time. The
work in front of this tree is to extend that discipline from the declarative half
to the imperative half, so that an app author who needs no hook writes no Go, and
an app author who needs one writes against the smallest surface the host can
offer.
