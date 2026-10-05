> Status: draft

# Plan: Catalog-update reconciliation (leaning on existing state)

**Last updated:** 2026-10-03

## Problem

Bloud reconciles *toward* the catalog for things that exist, but it never
derives "this node needs a full re-drive because the catalog changed" or "this
container/provider no longer exists." Three concrete failures follow.

1. **A new catalog removes a container from an app.** `syncAppContainers`
   iterates only the *current* `ContainerDefs()`, `populateGraphNodes` is
   add-only, and nothing else knows the container existed. The removed
   container keeps running forever under `io.bloud.managed`, its graph node
   stays RUNNING, and its mounts linger. A permanent orphan.
2. **A new catalog changes an app's SSO strategy (its outpost).** `ensureSSO`
   runs only inside `runFullLifecycle`, so a RUNNING node never sees the
   change. Even when it does, the `Ensure*` calls are create-or-verify with no
   delete path, so switching `forward-auth` → `native-oidc` (or `ldap` →
   anything) leaves the old provider and outpost behind and never provisions
   the new one.
3. **Any container-spec change (image, env, command, port, volume) on a
   RUNNING app is not applied.** `PodmanRuntime.Ensure` already detects spec
   changes via the `io.bloud.spec-revision` label, but `Ensure` is only reached
   from `runContainerPhases` → `runFullLifecycle`. The periodic pass only
   re-runs `PostStart` for RUNNING nodes, never `PreStart` or `Ensure`.

## Principle: the platform already records most of this

The fix should not add a new desired-state table. The existing state already
answers almost every question a catalog change raises:

- **What spec is a container running?** The container itself, in
  `io.bloud.spec-revision`: `PodmanRuntime.Ensure` hashes the rendered spec and
  stores it as a label, comparing on every create. It is a per-container
  desired-state record that already exists.
- **Who owns a container?** The `io.bloud.app` label (invariant 12). The podman
  client already has `ListContainers`; it just does not surface labels through
  the `container.Runtime` interface yet.
- **What is the lifecycle state?** The persisted graph nodes. Resetting a node
  to INITIALIZING is the existing, proven way to force a full re-drive, and it
  does not disturb the store status (`setupStatusSync` ignores
  non-RUNNING/non-ERROR transitions).
- **What ordering makes an SSO change safe?** Invariant 7: routes regenerate
  only after convergence, so a strategy change can provision the new provider
  and deprovision the old one inside the same full lifecycle before the routes
  flip.

So the only genuinely new durable fact is the SSO strategy the app last
converged to. Everything else already lives on the containers and in the graph.

## Mechanism

One new step, `reconcileCatalogUpdates`, runs in `convergeFromStores` before
`populateGraphNodes`, on every pass. It is a diff against the live container
set and one stored column, not a persisted desired-state document.

### Case A: container spec changed

For every RUNNING container node, render the spec the current catalog produces
(the existing `ContainerSpecFromDef` plus `applyIssuerExtraHost`, extracted
into a pure helper) and compare its revision against the container's stored
`io.bloud.spec-revision`. A mismatch resets that node to INITIALIZING, so the
normal full lifecycle re-runs `PreStart` (config file rewrite, `RestartNeeded`)
then `Ensure` (which recreates on the revision change). Unchanged siblings are
never touched.

This needs only a surface change to the runtime, not new logic: expose the
revision computation and the label. `podman.Container` gains a `Labels` field
(the list API already returns it), `container.Runtime` gains
`ListContainers`, and the existing `specRevision` becomes callable (for
example `Spec.Revision()`).

### Case B: container removed

One `ListContainers` call returns every managed container with its
`io.bloud.app` label. For each installed app, any container whose name is not
in the current `ContainerDefs()` is an orphan: run its `configurator.Remover`
when registered, `Containers.Remove`, and `graph.DeleteNode` (the
`graph_edges` foreign keys cascade, so edges go too). This is
`removeMultiContainerApp` for a subset. **Do not delete data**: the app still
exists and a dropped sidecar may share `apps/<app>/`.

The `io.bloud.app` label is shared by auxiliaries (tailnet nodes, the proxy
outpost), so the diff requires a second signal: the container must also be a
graph node, which only `containers:`-declared containers ever were. A failed
`Containers.Remove` keeps the node, so the next pass retries rather than
leaking the container.

The container set is its own record: no table can go stale the way a stored
snapshot can, because the diff is always against what is actually running.

### Case C: SSO strategy changed

Add one column, `apps.sso_strategy`, recording the strategy the app last
converged to. On each pass, compare it to the current catalog. When they
differ:

1. reset the primary node to INITIALIZING;
2. in the full lifecycle, after `ensureSSO` provisions the *new* strategy,
   deprovision the *old* one (read from the column), then write the new
   strategy back.

The deprovision reuses `pkg/authentik/deletion.go`'s `DeleteAppSSO` (which has
no callers today) for `native-oidc` and `forward-auth`, plus a new
`DeleteLDAPInfrastructure` for `ldap`. `DeleteAppSSO` treats "not found" as
success, so the delete is idempotent. Deprovision runs with the store's current
`display_name`; the pre-existing rename-vs-provider-name gap is unchanged and
out of scope.

Provision-new-then-delete-old, inside one full lifecycle, is what makes the
ordering safe: the new provider exists before the old one goes, and both happen
before routes regenerate, so there is no window with no provider and no
fail-open route.

### Trigger

`reconcileCatalogUpdates` runs on every pass, so the 60s periodic
`ReconcileIntent` catches changes with no operator action. For promptness,
`RefreshCatalogHandler` also `Submit`s a `ReconcileIntent` after the cache
reload (the apps module already holds `orch orchestratorCaller`).

### Removed app from catalog (surface, don't act)

Keep the row and containers exactly as today, and surface the condition.
`GetInstalled` (api/apps_module.go) already enriches each row from the catalog;
add a `catalogMissing: true` field when `catalog.Get(id)` fails, so the
dashboard can badge it. "Don't act" also means "stop acting": exclude
catalog-missing apps from the desired-state diff, from resets, and from the
PostStart resync (guard `runConfigurator` or `readyForPostStartResync` on
catalog presence), so the only thing left running is their container.

## What this deliberately does not do

- **Provider-identity propagation to `PreStart` consumers.** ~~Not done.~~
  **Done, by a different route than the one sketched here.** Hermes bakes
  `inference` and `mcp` into `config.yaml` in `PreStart`, so a provider change
  (an MCP app installed later, an MCP path, a rotated bearer) needs a full
  re-drive that the old PostStart-only resync did not give it. Rather than
  digesting resolved bindings per app, the resync now runs `PreStart` as well
  as `PostStart` for every node at `RUNNING` on every pass
  (`execution.go:runResync`), and recreates the container only when `PreStart`
  reports `RestartNeeded`. That covers the provider-change case, the
  drift-between-binding-and-config case, and the instance-inference case in one
  mechanism. `resetInferenceConsumers` stays: it is the prompt path for a
  settings save, and the resync is the floor that catches what no intent raised.
- **Parameter-only SSO changes** (scopes, access-token lifetime, bypass paths):
  these are reconciled by the idempotent `Ensure*` on the next full lifecycle,
  and a scope tweak on a RUNNING app is low-stakes. A strategy change is the
  case that flips which outpost/provider exists, and that is what the column
  catches.
- **`dependsOn` and `healthCheck` changes**: the former is an edge concern
  (not part of the container spec, so the revision does not cover it); the
  latter only matters during a lifecycle drive, so it applies on the next
  re-drive whatever the cause.
- **Edge reconciliation beyond `DeleteNode`'s cascade.** Provider uninstall
  already cascades edges; a `dependsOn` or `compatible`-list change is rare
  and deferred.

## Rejected alternatives

- **A persisted desired-state document per app** (a new table holding the full
  catalog-derived snapshot, diffed each pass). This was the first design. It is
  more machinery than the platform needs: the container already holds its own
  spec revision, and its own owner label, so a stored snapshot can only go
  stale relative to those. The one fact neither carries (the old SSO strategy)
  is a single column, not a table.
- **Call `Ensure` from the RUNNING-node resync path** so the spec-revision
  compare recreates changed containers without a full re-drive. Rejected: it
  skips `PreStart`, which is where a configurator writes config files and
  signals `RestartNeeded`. A catalog change is exactly the case that must
  re-run `PreStart`, so the trigger has to be a reset to INITIALIZING, not a
  bare `Ensure`.

## Interactions with existing resets

This composes with, and does not replace, the two existing reset paths:

- `resetSSONodes` (host address change) resets SSO-dependent nodes. A host
  change and a catalog change both landing in one pass simply both reset
  nodes; idempotency absorbs the overlap.
- `resetErroredNodes` / `retryErroredNodes` (install retry, self-heal) reset
  ERROR nodes. The spec-revision and strategy diffs never touch ERROR nodes;
  they only decide *whether* a full re-drive is wanted, and node status owns
  the retry.

## Phases

1. **Runtime surface.** Add `Labels` to `podman.Container`, expose
   `ListContainers` through `container.Runtime`, and make the spec revision
   callable. Unit: list returns labels; revision is stable for an unchanged
   spec.
2. **Spec-change detection (Case A).** Extract the spec build from
   `ensureContainerFromDef`; compare revision vs the label for RUNNING nodes
   and reset on mismatch. Orchestrator test: an image bump resets only that
   node; unchanged siblings untouched; identical passes reset nothing.
3. **Prune (Case B).** Diff `ListContainers` against `ContainerDefs` and prune
   orphans. Orchestrator test with a fake runtime: container removed, node
   deleted, data untouched, siblings not reset.
4. **SSO strategy change (Case C).** Add `apps.sso_strategy` (migration 10).
   Wire `SSOProvisioner.Deprovision` → `DeleteAppSSO`; deprovision after
   `ensureSSO` in the full lifecycle. Integration-tier assertion that flipping
   an installed app's strategy deletes the old provider via the Authentik API.
   LDAP teardown is a separate slice with a live re-verify.
5. **Trigger + removed-app surface.** `RefreshCatalogHandler` submits a
   `ReconcileIntent`; `GetInstalled` reports `catalogMissing`; catalog-missing
   apps are excluded from the diff and resync.

## Validation

Two layers, matching the repo's split between the fast tier and the VM
behavioral tier. The new test primitive in both is the mutation itself: change
the catalog, then assert the system converged.

### Fast tier (unit, `-race`)

The orchestrator fake runtime (`fakes_test.go`) gains `ListContainers` (canned
names plus labels) and a spec revision it can compare. `catalog_update_test.go`
pins each case:

- `TestResetsOnlyChangedNodes`: one of two container revisions changes; exactly
  that node resets, the sibling is untouched.
- `TestNoDiffIsStable`: several passes against an unchanged catalog perform
  zero resets and zero removals. This is the flap-loop guard: a reconciler
  that churns every pass is the failure a single green run cannot see.
- `TestPrunesRemovedContainer`: the runtime lists an orphan under
  `io.bloud.app` that is not in `ContainerDefs`; `Remove` and `DeleteNode` run,
  siblings are untouched, and the app data dir is not deleted.
- `TestPruneIsIdempotent`: an already-gone container is a no-op, not an error.
- `TestSSOChange_DeprovisionsOldAfterProvision`: a recording `SSOProvisioner`
  asserts `EnsureNativeOIDC` fires before `Deprovision("forward-auth")`.
- `TestSSOChangeToNone_DeprovisionsOnly`.
- `TestCatalogMissing_ExcludedFromResync`: an installed app missing from the
  catalog is not reset, not resynced, and flagged `catalogMissing`.

### Integration tier (VM)

The e2e binary already runs inside the VM next to host-agent and drives every
mutation through the API. It gains write access to the deployed catalog dir
(`BLOUD_APPS_DIR`) so it can change what the loader reads, then
`POST /api/apps/refresh-catalog`.

A configurator-less fixture app makes the structural cases cheap: the lifecycle
skips a nil configurator's phases and still `Ensure`s the container, so two
tiny `testdata/` catalogs stand in for product apps:

- `structural/`: three `alpine` containers, no SSO; `v2` drops one and bumps
  another's image.
- `sso/`: `forward-auth` in `v1`, `native-oidc` in `v2`.

Three scenarios, asserted behaviorally:

1. Install `structural/v1`, converge, overwrite with `v2`, refresh: the
   dropped container's `podman inspect` fails, the other two stay RUNNING, and
   the app's data dir survives.
2. Overwrite with a bumped image, refresh: the running container reports the
   new image tag.
3. Install `sso/v1`, assert the Authentik proxy provider exists; overwrite
   `sso/v2`, refresh: the proxy provider is deleted and an OAuth2 provider
   exists, via the Authentik API.

## Acceptance criteria

- `./bloud validate --tier fast` green at every phase.
- Dropping a container from an installed app's `metadata.yaml`, then
  `refresh-catalog`, removes exactly that container and its node within one
  pass; the app's other containers and data are untouched.
- Bumping an image tag for a RUNNING app recreates only that container and
  re-runs its `PreStart`/`PostStart`.
- Flipping an app's `sso.strategy` re-provisions the new provider and deletes
  the old one, with the old provider gone only after the new one is live.
- A catalog-removed app stays installed and running, and
  `GET /api/apps/installed` reports `catalogMissing: true`.

## Risks and open questions

- **SSO deprovision ordering.** Provision-new-then-delete-old is chosen so a
  strategy change never has a no-provider window; the two providers coexist
  briefly. A missed cleanup (crash between provision and delete) is re-detected
  on the next pass because the stored strategy has not been updated.
- **LDAP teardown is net-new.** Deleting the outpost that Jellyfin/Radicale
  bind against deserves its own slice and live re-verification, separate from
  the OIDC/proxy delete.
- **A strategy change toward `forward-auth` (or `none`) has a bounded
  fail-open window.** The app disables its own auth when the container is
  recreated, and the forward-auth middleware is added only when routes
  regenerate at the end of the pass. The window is one convergence pass on an
  app the operator is actively reconfiguring, but it is real and worth a
  follow-up if strategy changes become routine.
- **The revision comparison covers the rendered container spec, not
  `dependsOn` or `healthCheck`.** Both are lifecycle concerns, not container
  config, and both apply on the next re-drive whatever the cause.
- **Rename vs. provider names.** Provider names embed `displayName`; the
  existing `RenameAppIntent` already leaves the SSO provider name stale. This
  plan deprovisions with the store's current name and does not fix that
  pre-existing gap.

## References

- `docs/plans/apt-repository.md` (the delivery half: ships the new catalog via
  `apt upgrade`)
- `docs/plans/media-stack-integration.md` §9 F2 (this plan realizes it more
  cheaply: the container already carries the desired-state record)
- `docs/specs/reconciler-spec.md` (invariants 1, 2, 5, 7, 9, 12, 15)
- `services/host-agent/internal/engine/orchestrator/pipeline.go`,
  `graph_build.go`, `execution.go`, `sso_resolve.go`, `status.go`,
  `orchestrator_containers.go`, `inference.go`, `config.go`
- `services/host-agent/internal/container/runtime.go` (`spec-revision` label,
  `Ensure` compare)
- `services/host-agent/internal/podman/client.go` (`ListContainers`)
- `services/host-agent/pkg/authentik/deletion.go` (`DeleteAppSSO`, no callers)
- `services/host-agent/internal/schema/migrations.go` (ledger at version 9)
