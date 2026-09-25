# Refactor Plan: One Orchestrator Builder, No Fork

Target: tech-debt items 11 and 12 (duplicated orchestrator wiring and the
disagreeing `AppState` builder). Driver: long-term maintainability, not a fire.

## Problem Statement

There are two hand-maintained places that construct the orchestrator, and they
disagree about what the system is.

The product path builds it inside the API layer. It sets roughly 25 fields on
`OrchestratorConfig`: the container runtime, the catalog dependency graph, the
Traefik generator, the operation store, the tailnet node, the gateway, the
remote proxy, the proxy outpost, the SSO provisioner, the live host set, the
host store, and the host-change hook.

The CLI path builds a second one in `cmd/host-agent/configure.go`. It sets
three fields: `LDAPOutput`, `AppStore`, `Secrets`. No container runtime, no
catalog graph, no Traefik generator, no operation recorder.

Investigating this turned up three things the ledger does not say:

1. **The CLI `configure` subcommand has no caller.** A repo-wide search for
   `bloud-agent` finds only the usage strings inside `configure.go` itself. No
   caller in `cli/`, `packaging/`, `install.sh`, any systemd unit, any script,
   or any CI workflow. It is a vestige of an older architecture where systemd
   units called a prestart hook before launching each app container. The
   orchestrator replaced that.

2. **Even if it were called, it would do nothing.** Configurators register
   under graph *node* names (`apps-jellyfin`, `apps-immich-server`). The CLI
   looks them up by *catalog ID* (`jellyfin`). `registry.Get("jellyfin")`
   returns nil, and `runPreStart` and `runPostStart` exit 0 on "no configurator
   registered." `runReconcile` builds its graph nodes keyed by catalog ID too,
   so every node misses its configurator and `Reconcile` reports success having
   configured nothing.

3. **A chain of otherwise-dead machinery is kept alive only by that path.**
   Each of these has exactly one non-test caller, and all of them are in
   `configure.go`: `secrets.Manager.WriteEnvFiles`, `secrets.Manager.AppendEnvVars`,
   `sso.BlueprintGenerator.GetSSOEnvVars` (plus its entry in the
   `BlueprintGeneratorInterface`), and `configurator.WaitForSSOReady`.
   That chain is dead twice over: no `apps/*/metadata.yaml` declares the
   `sso.env` mappings its input, so `GetSSOEnvVars` returns an empty map for
   every app in the current catalog.

The second `AppState` builder compounds this. The orchestrator's own
`buildAppState` resolves SSO through `resolveSSOURLs()`, which prefers the
live `hostset.State` that admin changes drive, and it populates the typed
`Integrations` bindings. The CLI copy reads the legacy `SSOBaseURL` config
string, ignores admin-set hosts entirely, and never populates `Integrations` at
all. Two builders, two answers to "what is this app's OIDC client," and the
worse one is reachable only by a command nobody runs.

There is also a standing invariant caveat. `docs/specs/review-2026-09-19.md`
records that invariant 1 ("orchestrator is the single writer") holds for apps
with one exception: the `configure.go` path drives `Reconcile` outside the
intent queue. That exception exists only to serve a command with no caller.

From the developer's side, the cost is this. Every new orchestrator subsystem
has to be added in two places, and nothing fails when you add it in one. The
operation-state recorder is a live example: it is one more field that would
otherwise fork. The plan in `docs/plans/tech-debt-repayment.md` says to unify
both sites behind a profiled builder. This plan does not, because there is no
second caller to profile. You cannot unify two copies of nothing. You delete one
and make the survivor the only way to get an orchestrator.

## Solution

Two moves, in that order.

**First, delete the fork and everything hanging off it.** The `configure`
subcommand, its file, and the four-function orphan chain that only it called,
including the `sso.env` declarative schema field. The declarative env-var SSO
mechanism is superseded: Vaultwarden is the env-var-only app in the catalog,
and it configures itself by rendering a dotenv file from its own configurator
with `managedfile.Write`, pointed at by `ENV_FILE` in its container spec.
Static values go in `containers[].environment`. Two live mechanisms already
cover every app. The third promised a delivery path that nothing delivered.

**Second, make one builder the only way to construct an orchestrator.** A new
`internal/wire` package owns the whole dependency set: the lifecycle graph, the
catalog dependency graph, the container runtime, the tailnet node, the
gateway, the remote proxy, the proxy outpost, the Traefik generator, the
operation store, the SSO provisioner, and the `BLOUD_TS_AUTHKEY` migration.
It takes an `Input` bundle and returns an `Output` carrying the built
orchestrator. `main.go` calls it, then starts the loop explicitly, then hands
the orchestrator to the API server.

The one entanglement is the orchestrator's `OnHostsChanged` hook, which today
re-ensures the dashboard OAuth app that lives in the API package. It crosses the
boundary as a plain `func()`, so `wire` never imports `api`. `main.go` creates
the auth ref first, passes `authRef.Ensure` into the builder, and passes the
same ref to the server. No forward reference, no import cycle.

The API stops constructing orchestrators. The implicit branch in
`buildRouterDeps` that creates a real one when the caller supplies none is
deleted, along with the `noOrchestrator` escape hatch. If nobody hands the
router an orchestrator, it has none, and that is visible rather than silently
half-wired.

Two tests make the change stick. A completeness guard reflects over
`OrchestratorConfig` after a build and asserts every field is populated, with
an explicit allowlist of the ones that are legitimately optional. A node-shape
test asserts that every node the catalog planner will create for a non-system
app has a registered configurator factory, and that the registry's declared
node list contains no extras. That second test is the general form of the
catalog-ID-versus-node-name confusion that made the CLI path inert. It would
have caught this bug class the first time.

## Commits

The plan runs in four phases. Each commit leaves the tree building and every
existing test green. Deletion comes first because it is the cheap half and it
shrinks the surface the builder has to cover.

### Phase A: remove the fork

**1. Delete the `configure` subcommand.**
Remove `services/host-agent/cmd/host-agent/configure.go` in full. That file
holds `runConfigure`, `runPreStart`, `runPostStart`, `runReconcile`,
`runCatalogRefresh`, `buildAppState`, and `writeSSOEnvVars`; all seven go with
it. In `services/host-agent/cmd/host-agent/main.go`, drop the `case "configure":`
arm from the subcommand switch. Leave `init-secrets` alone.

Do this commit alone, without touching the orphan chain. Go does not error on an
unused exported function, so the orphans still compile, and keeping the caller
deletion separate makes the diff reviewable as one fact: this command is gone.

Verify: `cd services/host-agent && go build ./... && go vet ./... && go test ./...`

**2. Delete the env-file regeneration entrypoints.**
Remove the exported `WriteEnvFiles` and `AppendEnvVars` from the secrets
manager. Keep the unexported `writeEnvFiles` and `writeAppEnvFile`: the
product path still calls them when secrets are saved, so env files keep being
generated exactly as before. Only the manual re-generation entry and the
append-after-the-fact helper go away.

Verify: `cd services/host-agent && go test ./internal/secrets/... && go test ./...`

**3. Delete the declarative SSO env-var chain.**
Four pieces, one concept:

- `GetSSOEnvVars` from the blueprint generator.
- Its entry in `BlueprintGeneratorInterface`, so the interface no longer
  advertises a capability nothing calls.
- The `SSOEnv` struct and the `Env` field on `SSO` in the catalog models, so a
  new app author never sees a `sso.env:` key that delivers nothing.
- The `GetSSOEnvVars` methods on the two blueprint test doubles in the
  orchestrator test suite, which lose their reason to exist.

Verify: `cd services/host-agent && go build ./... && go test ./... && go test -race ./internal/catalog/...`

**4. Delete `WaitForSSOReady`.**
Remove the health helper from `pkg/configurator` (the file holds only it, so the
file goes). Its own doc comment already says it is "the one health helper with a
live caller (the CLI configure path)." Configurators use `appclient`'s
`Call.Wait` with their own readiness predicates instead, and that is the pattern.

Verify: `cd services/host-agent && go test ./... && npm run lint:go && npm run check:gofmt`

### Phase B: one builder

**5. Add the `internal/wire` package, unused.**
Create `wire.Input` (config values, the database handle, the app store, the
catalog cache, the configurator registry, the container runtime, the event bus,
the tailnet store, the Authentik client, the live host set, the host store, and
the `OnHostsChanged` callback), `wire.Output` (the built orchestrator), and
`wire.Build`. Port the body of `initOrchestratorHelper` into it as a
behavior-preserving move: same construction order, same nil-handling, same
`BLOUD_TS_AUTHKEY` to `tailnet_connections` migration, same catalog-graph
load with the same non-fatal error path.

Deliberate difference from the original: `Build` does not launch the loop. The
caller starts it. That keeps `Build` a pure construction step that a unit test
can call without a running goroutine racing its assertions.

Nothing calls `Build` yet. Add the builder unit test in the same commit so the
new package arrives covered rather than bare. The test asserts that a build from
a fully populated `Input` returns a non-nil orchestrator, and that a build with
an optional collaborator absent still succeeds with that subsystem disabled.

Verify: `cd services/host-agent && go test ./internal/wire/...`

**6. Switch `main.go` to the builder.**
In `runServer`, replace the implicit construction with an explicit sequence:
create the auth ref, call `wire.Build` with `OnHostsChanged` bound to the auth
ref's ensure method, start the orchestrator loop with an explicit `go orch.Start`
under a context the shutdown path cancels, then construct the API server with
the orchestrator passed in through a new `Orchestrator` field on the server
config. The router already prefers a supplied orchestrator over building its
own, so this commit only adds the field and rewires the caller.

Ordering check for the reviewer: today the orchestrator is built and started
inside `NewRouter`, before the listener opens. The new order is build, start,
construct server, open listener, wait for convergence. The bootstrap gate still
holds the API closed until the first convergence pass finishes, so the visible
behavior is unchanged.

This is the commit to smoke-test by hand. Run `./bloud dev` and confirm the
waiting page appears, the ready line prints, and the dashboard loads.

Verify: `cd services/host-agent && go build ./... && go test ./... && ./bloud dev`
(then Ctrl-C once it prints the ready line)

**7. Delete the implicit construction path.**
Remove `initOrchestratorHelper` from the API package, the `else if
!options.noOrchestrator` branch in `buildRouterDeps`, and the
`noOrchestrator` field on the router options. Update the four tests that set
`noOrchestrator = true` to simply not set it: with the implicit branch gone,
an absent orchestrator means no orchestrator, which is exactly what those tests
wanted.

After this commit there is exactly one call site of `NewOrchestrator` in
non-test code, and it is in `wire`.

Verify: `cd services/host-agent && go test ./internal/api/... && go test ./... && grep -rn "NewOrchestrator(" --include="*.go" services/ | grep -v _test`
(the last command should print only the `wire` call and the definition itself)

**8. Add the config-completeness guard.**
In the `wire` package, add a test that reflects over `OrchestratorConfig` after
a `Build` and asserts that every field is non-zero, except for an explicit
allowlist. Each allowlist entry carries a one-line reason in the test source, so
adding a field and forgetting it fails with a message naming the field, and
adding a field to the allowlist is a deliberate, reviewable act.

Do this after commit 7, not before, so the guard runs against the final field
set rather than a transitional one.

Verify: `cd services/host-agent && go test ./internal/wire/... -run Completeness -v`

### Phase C: pin the node shape

**9. Add the catalog-to-registry node-shape test.**
Assert two directions:

- For every non-system app in the catalog, the node the planner would create
  for its primary service has a registered configurator factory. The primary
  node is the app's last container definition, or the catalog ID itself when
  the app declares no containers.
- Every name in the user-app registry's declared node list is the primary node
  of some non-system catalog app. No orphans.

Verified against the current catalog, the 13 user apps' last-container names
match the registry's declared node list exactly, so this test lands green.

Scope note for the implementer: exclude system apps. Authentik's last container
is `apps-authentik-ldap`, which has no configurator; its registered node is
`apps-authentik-server`. That mismatch is ledger item 20 and is explicitly out
of scope here. This test pins the existing convention, it does not change it.

This test lives in the host-agent module, not the apps module. The apps module
cannot import the catalog loader across the `internal/` boundary, so the shape
check has to run where both sides are visible.

Verify: `cd services/host-agent && go test ./internal/wire/... && cd apps && go test ./...`

### Phase D: docs

**10. Update the docs to match the code.**

- Drop the `configure` row from the subcommand table in
  `docs/architecture/overview.md`. The table currently advertises a command
  that no longer exists.
- Mark ledger items 11 and 12 closed in `docs/operations/tech-debt.md`, with
  the evidence: one construction site, the CLI path deleted for having no
  caller, and the completeness and shape guards in place. Update repayment
  plan section 6 to record that the profiled-builder approach was replaced by
  deletion, and why.
- Add the retired identifiers (`bloud-agent configure`, `WaitForSSOReady`,
  `sso.env`) to the Vale reject vocabulary, but only after confirming that no
  live doc quotes them. The archived plans and the dated 2026-09-19 review
  quote them legitimately and are already exempt from that rule.

Do not edit `docs/specs/review-2026-09-19.md`. It is a dated review and its
caveat about `configure.go` driving `Reconcile` outside the queue was true when
written. The deletion resolves it; rewriting the review would erase the record.

Verify: `npm run lint:prose && npm run check:docs-links && npm run check:no-emdash`

### Final gate

**11. Run the full fast tier and the lifecycle run.**

Verify: `./bloud validate --tier fast && ./bloud e2e lifecycle`

The lifecycle run is the one that matters. It installs Jellyfin through the
real host-agent API on the dependency-graph path, restarts services, and
uninstalls. If the builder produced a runtime that boots, converges, survives a
restart, and cleans up, the refactor preserved the invariant.

## Decision Document

- **The CLI `configure` subcommand is deleted, not unified.** It has zero
  callers and cannot function as written. The profiled-builder plan from the
  repayment doc is rejected because a profile requires a real second consumer,
  and inventing one to justify the abstraction is worse than the fork we are
  removing.
- **The single builder lives in a new `internal/wire` package** in the
  host-agent module. It is not kept inside the API package, because a future
  non-HTTP consumer (a tailnet outpost or a remote control plane, per
  invariant 13) must be able to build an orchestrator without importing the
  HTTP layer.
- **The builder takes an `Input` bundle and returns an `Output`.** It does not
  start the orchestrator loop. The caller starts it, under a context the
  shutdown path cancels. This keeps construction testable without a running
  goroutine.
- **The `OnHostsChanged` hook crosses the package boundary as a plain
  `func()`.** The auth ref stays in the API package. `main.go` creates it
  before calling the builder and passes the ensure method in. This avoids an
  import cycle without moving auth out of the API package.
- **The API cannot construct an orchestrator implicitly.** The fallback branch
  that built a real one when no orchestrator was supplied is deleted, and the
  `noOrchestrator` test option with it. Callers supply one or they get none.
- **The declarative `sso.env` schema is deleted, not parked.** The `SSOEnv`
  struct, the `Env` field on the SSO config type, the env-var derivation, its
  interface entry, the append-to-env-file helper, and the SSO-ready wait all
  go. The live mechanisms for an env-var-only app are static
  `containers[].environment` entries and a configurator-rendered dotenv file
  selected by `ENV_FILE`. The deleted declarative layer sits below both and
  serves neither.
- **The env-file generation itself is kept.** The unexported write path that
  runs when secrets are saved is untouched. Only the manual re-generation and
  append entrypoints are removed.
- **The drift guard is a reflection test, not a type change.** `OrchestratorConfig`
  keeps its exported fields. Making them unexported with builder setters was
  considered and rejected: it is a much larger diff across every existing test
  that constructs the config literal, for a guarantee the completeness test
  already gives at the one construction site that matters.
- **The node-shape test is scoped to non-system apps.** Authentik's primary-node
  mismatch is ledger item 20 and is not touched.
- **The `BlueprintGeneratorInterface` itself stays**, minus the env-var method.
  It has no production consumer (only test doubles implement it against it),
  which is its own dead-code question, deliberately not answered here.

## Testing Decisions

A good test in this refactor asserts an externally observable contract, not the
shape of the code that produces it. Two contracts qualify:

**Construction completeness.** Given a fully populated input bundle, the built
orchestrator has every subsystem it needs. The reflection guard is
deliberately aware of the config type's fields. That is not testing an
implementation detail, it is testing the contract of the config type itself:
every field means "a subsystem exists," and a subsystem that exists but is
never wired is a bug. The failure message names the offending field, so the
test is a tool rather than a wall. Fields that are legitimately optional are
allowlisted with a stated reason, which turns "is this optional?" into a
decision someone had to write down.

**Node-name agreement.** Given the catalog, every node the planner will create
for a non-system app has a configurator. This is behavioral from the
orchestrator's point of view: it looks up a configurator by node name, and a
miss means the app installs unconfigured. It says nothing about how the
registry stores factories or how the planner picks the primary node.

What we are not testing: the internals of the reconcile loop, the ordering of
phases, the intent queue, or the behavior of individual configurators. Those
are covered elsewhere and are unchanged here.

Modules under test: the new `wire` package (construction completeness,
optional-subsystem degradation, node shape), and the secrets and catalog
packages by regression (their deleted entrypoints must have no remaining
consumer).

Prior art in this codebase, which the new tests should look like:

- `apps/registry_test.go` `TestRegisterAll`: the existing catalog-versus-
  registry agreement test. The new shape test is the same idea extended from
  "every declared name is registered" to "every node the planner creates is
  registered, and the declared list has no extras."
- `catalog/cache_test.go`: the race test that runs in the fast tier, an
  example of a narrow test pinned to one invariant rather than one function.
- `api/auth_bypass_test.go`: router-level wiring through the router options,
  the pattern the four updated `noOrchestrator` tests follow.
- `internal/e2e/e2e_test.go` and the media-stack integration tests: the
  behavioral style the lifecycle run represents, asserting through the app's
  own API rather than by reading config values back.

The honest gap: `cmd/host-agent` has no test files today and this refactor does
not add any. The builder moved the construction knowledge into a tested
package, which is the better place for it. A test of `main.go` itself would be
testing that Go can call functions in order.

## Out of Scope

- **Ledger item 20**, the order-dependent `primaryContainerNode` convention.
  The shape test pins the current rule (last container definition wins) but
  does not change it. Authentik's primary node being the LDAP outpost rather
  than the server is a real smell and a separate change.
- **Ledger item 15**, the sharing module being wired with nil collaborators so
  invite creation always 503s. The new builder already constructs the real
  gateway and tailnet node, so `wire.Output` could hand them to the sharing
  module and fix item 15 almost for free. That is a follow-on, not part of
  this change, and it should be its own commit with its own tests.
- **Ledger items 13 through 17 and 19 through 23.** System-app hiding by
  category, the unpersisted primary host, the unreachable clear-data handler,
  the `DeriveSecret` fallback literal, unparsed Traefik YAML, the unset
  install blockers, the icon path traversal, orphaned networks, and health
  checks that never reach Podman.
- **Any change to orchestrator semantics.** Reconcile behavior, phase
  ordering, the intent queue, error terminality, and the configurator contract
  are all untouched. This is a construction refactor.
- **Replacing the deleted CLI command with a new one.** If a one-shot recovery
  verb is ever wanted, it should be built on the builder and given a real use
  case first, not resurrected from the deleted path.
- **Removing `BlueprintGeneratorInterface`.** It has no production consumer.
  Deciding whether to collapse it to the concrete type is a separate cleanup.
- **The `sso.env` replacement mechanism.** If a future app genuinely needs
  declarative env-var SSO injection, that is a design task, not a revert.
- **Frontend, e2e specs, and the app catalog contents.** No app metadata
  changes. The catalog is an input to this refactor, not a target.

## Further Notes

**Why the ledger's plan was wrong, and how to notice that sooner.** The
repayment doc described this as "two sites with divergent config" and proposed
making both call one builder. That framing is accurate about the code and wrong
about the action, because nobody checked whether the second site was reachable.
The cheapest fix for a fork where one branch is dead is deletion. Before
unifying anything with a profile, ask who calls the second branch. If the
answer is "nothing," the profile is a monument.

**The verification that actually matters is `./bloud dev`.** The unit and
shape tests prove the builder is complete and the registry agrees with the
catalog. They do not prove the appliance boots. Commit 6 is the one to run by
hand. The e2e lifecycle run at the end is the automated version of the same
question.

**A useful side effect.** Once `wire.Output` exists, the sharing module's nil
wiring (item 15) becomes a one-line change instead of a construction project,
because the real gateway and tailnet node are already built and sitting in the
output. Worth remembering when that item gets picked up.

**Sequence risk.** Commits 1 through 4 delete code that compiles but is
unreferenced. If a reviewer wants to keep any of it, the later commits are
unaffected; the phases are independent. Commit 6 is the only one with a
behavioral risk worth a manual check, and commit 7 is the one that makes the
invariant structural rather than conventional.

**Suggested next step:** run `kickoff-branch` to create the refactor branch
before starting commit 1.
