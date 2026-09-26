# Refactor Plan: Make Reality Match Intent

Target: tech-debt ledger items 8 and 9 (both P1): container drift is never
repaired while the process is alive, and `Ensure` destroys the running
container before it pulls. Driver: the reconciler's core promise was not
actually being kept. Status: **implemented** on
`refactor/reality-match-intent`.

## The Invariant

One invariant governs this whole refactor, and everything else follows from it:

> **A graph node's `actual` status must never claim `RUNNING` while the
> container behind it is not running.**

This is the single thing that makes the reconciler a reconciler. The whole
engine drives work off exactly one comparison, in `collectWorkForLevel`:

```go
if node.TargetStatus != node.ActualStatus { /* this node needs work */ }
```

`target != actual` is the *only* signal that a node needs attention. So any
observation that contradicts a node's `actual` status has to be written
**into the node**. Writing it somewhere else (into the database, into a log
line, into a dashboard field) produces a system that knows something is
wrong and can act on none of it.

Before this refactor that invariant was broken by design, and the previous
code documented the break rather than fixing it.

## Problem Statement

### Item 8: the drift detector could detect and nothing else

`SyncContainerState` runs on **every** convergence pass (pipeline step 1).
Its job is to notice that reality and the books disagree. It noticed. It
then wrote the observation to the one place the reconciler never reads.

The old repair path, in full:

```go
case app.Status == "running" && !state.Exists:
    // Container gone entirely → mark stopped so it can be re-created.
    _ = o.appStore.UpdateStatus(app.CatalogID, "stopped")
```

The comment says "so it can be re-created". Nothing re-created it. The
store row went to `"stopped"`. The graph node kept `ActualStatus == RUNNING`
and `TargetStatus == RUNNING`. `collectWorkForLevel` compared them, found
them equal, and moved on. `populateGraphNodes` only *adds* missing nodes,
and `SetTargetStatus` no-ops when the target is unchanged, so neither could
correct it.

The observable failure: kill a container with `podman rm` or an OOM while
host-agent is running, and the app stays dead indefinitely. The dashboard
shows `stopped`. It heals only when host-agent restarts, because a restart
rebuilds the graph from scratch at `INITIALIZING`. The detector fired every
pass and accomplished nothing every pass.

Worse, the multi-container case was excluded outright:

```go
// Skip apps with no container definitions or multi-container apps
// (multi-container lifecycle is tracked via graph events, not this path).
if len(defs) != 1 {
    continue
}
```

That comment is false. Multi-container lifecycle is tracked via graph
*nodes*, and this is the path that keeps those nodes honest. So Immich,
Authentik, AFFiNE, Paperless-ngx and Home Assistant (every app that owns
its own postgres or redis) never got drift detection at all. Which is the
worst possible set of apps to skip, because a dead `*-postgres` sidecar takes
the whole app down with it.

### Item 9: `Ensure` destroyed the container, then asked for the image

The old recreate sequence in the container runtime:

```go
if current != nil {
    r.client.RemoveContainer(ctx, spec.Name, true)   // destructive, first
}
if err := r.pullImage(ctx, spec.Name, spec.Image); err != nil {
    return EnsureResult{}, err                        // failure, after
}
r.client.CreateContainer(...)
```

A registry outage, a rate limit, a revoked token, or a digest mismatch lands
on the second statement, after the first has already destroyed the running
container. The app is down, there is no rollback, and the next pass repeats
the same failure until the registry comes back. Every spec change was a
small game of chicken with the registry.

The same path had a second defect: it called `client.RemoveContainer`
directly, bypassing the ownership guard that `Remove` enforces two functions
away:

```go
if current.Labels[managedLabel] != "true" {
    return fmt.Errorf("refusing to remove unmanaged container %q", name)
}
```

So `Remove` refused to touch a container Bloud did not create, and `Ensure`
destroyed it without looking. The guard existed in exactly the one place it
was not needed.

## Solution

Two fixes, both small, both resting on the invariant above.

### 9a: write the drift into the node

When a node claims `RUNNING` and its container is not running, reset the
node's `actual` status to `INITIALIZING`. That makes `target != actual`,
which puts the node back on the normal lifecycle path in the same pass.
`runConfigurator` sees a non-RUNNING actual and dispatches
`runFullLifecycle`, which re-runs `PreStart` → SSO → `EnsureContainer` →
health → `PostStart` and converges the node back to `RUNNING`.

Three properties make this safe rather than reckless:

- **Invariant 2 does the load-bearing work.** Configurators are idempotent
  by contract; `PreStart`/`PostStart` run on every pass already. A re-drive
  is not a special case, it is the normal case.
- **`ERROR` is not touched.** `ERROR` is terminal by design ("never retry
  without an explicit status reset"). Silently retrying it would turn a
  deliberate stop into an infinite loop. Only a node that specifically
  claims `RUNNING` is a drift candidate.
- **Intermediate statuses are not touched.** `PRESTART_CONFIG` or `STARTING`
  means a drive is already in flight. Resetting it would interrupt work that
  is progressing.

The store correction is kept. It is the honest "at this instant" user-visible
status, and the re-drive resolves it back to `running` in the same pass via
`setupStatusSync`, which remains the single authoritative graph→DB path.

The multi-container skip is removed. Every declared container is inspected,
and each node is repaired independently: a dead `immich-machine-learning`
resets its own node while `immich-server` is left alone, and the app-level
store status aggregates through the existing `allContainersRunning`.

### 9b: pull before you destroy

Reorder `Ensure` to `guard → pull → remove → create → start`. The pull
failure now happens while the old container is still running, so the app
survives a registry outage with its previous version intact.

The ownership check is extracted into one named predicate, `isManaged`, and
both destructive paths call it. One check, one name, no path that forgot it.

## Commits

The ladder as executed. Each commit leaves the tree green.

1. **Extract the managed-container ownership check in the container
   runtime.** Add `isManaged(details)` returning whether the
   `io.bloud.managed` label is `"true"`, and make `Remove` call it instead
   of inlining the label comparison. No behavior change; this exists so the
   next commit has one guard to apply rather than a second copy of a
   condition.
   → verify: `cd services/host-agent && go test ./internal/container/...`

2. **Pull before destroying, and guard the recreate path.** In `Ensure`,
   move `pullImage` ahead of `RemoveContainer`, and refuse the whole
   operation when an existing container is not Bloud-managed. Give the test
   fake a chronological event log and an injectable pull failure so ordering
   and failure behavior are both assertable rather than inferred.
   → verify: `cd services/host-agent && go test ./internal/container/... -run TestPodmanRuntime -v`

3. **Put drifted container nodes back on the lifecycle path.** In
   `SyncContainerState`, add `repairDriftedNode`: a node at `RUNNING` whose
   container is not running is reset to `INITIALIZING` with a reason.
   Decompose the per-app work into `syncAppContainers`, `inspectContainers`,
   `containersAllGone` and `containersAllRunning` to stay inside the
   cyclomatic-complexity gate. Drop the `len(defs) != 1` skip so
   multi-container apps are covered. Replace the test that pinned the old
   skip with tests for the new contract.
   → verify: `cd services/host-agent && go test ./internal/engine/orchestrator/... -run TestSyncContainerState -v`

4. **Close ledger items 8 and 9 and record this plan.** Move both items to
   "Already Paid" in the tech-debt ledger with the reasoning, and replace
   the previous plan in `specs/REFACTOR_LATEST.md`.
   → verify: `npm run check:docs-links`

Full-suite gate after the ladder:

```sh
cd services/host-agent && go test ./...
cd services/host-agent && go test -race ./internal/engine/orchestrator/... ./internal/catalog/...
cd apps && go test ./...
npm run lint:go && npm run check:gofmt
```

## Decision Document

**Container runtime (`internal/container`).**

- The ownership predicate `isManaged` is package-private and shared by every
  destructive path. The rule is: no code removes a container without
  passing through it.
- `Ensure`'s ordering contract is now `guard → pull → remove → create →
  start`. This is pinned by an explicit event-sequence assertion, not just
  by the absence of a removal on failure, because the ordering is the
  contract.
- A pull failure is returned unchanged. It is not swallowed and not
  retried here; the reconciler's next pass is the retry.
- The unmanaged-container refusal on the recreate path is a hard error, not
  a skip. A name collision means the desired state is ambiguous and
  silently proceeding either way is wrong.

**Orchestrator (`internal/engine/orchestrator`).**

- `repairDriftedNode(nodeID, state)` is the single drift-repair primitive.
  It returns whether it issued a reset, so callers and tests can observe
  the decision.
- Drift is defined narrowly: `state.Running == false` **and**
  `node.ActualStatus == RUNNING`. Not "any mismatch". `ERROR` is terminal
  and untouched; intermediate statuses mean a drive is in flight.
- The reset target is `INITIALIZING`, not `STOPPED`. `INITIALIZING` with
  target `RUNNING` dispatches the full lifecycle. A hypothetical
  `STOPPED`-with-target-`RUNNING` would need a new dispatch rule, and the
  graph already has the right state for "start over".
- The reset carries a reason string on the node's `Error` field so the
  recreate is traceable to its cause. This follows the `PreStartResult`
  precedent: a recreate signal and its reason travel together.
- The store-status corrections are deliberately kept. They are the
  user-visible truth at the moment of sync, and existing tests pin them.
  The node reset is additive, not a replacement.
- Uninstalling apps are excluded from drift repair before any repair can
  happen. Their containers are expected to be absent; treating that as drift
  would resurrect an app the user is removing.
- An uninspectable container is omitted from the state map rather than
  assumed absent. Treating "could not inspect" as "gone" would reset nodes
  on a transient runtime error.
- Multi-container apps are now inspected per container def. App-level store
  status continues to aggregate through the existing
  `allContainersRunning`; no new aggregation path was added.

**No schema change, no API change, no config change.** This is entirely
inside the reconcile loop and the container runtime.

## Testing Decisions

A good test here asserts a **state transition the reconciler can act on**,
never an internal helper's return value. The tests ask: after a sync pass,
is this node back on the lifecycle path, and is it distinguishable from a
node that should not be?

Specifically, the drift tests assert three things together, because the bug
was a three-way disagreement:

- the node's `actual` status moved to `INITIALIZING`
- the node's `target` is still `RUNNING` (a reset that also lowered the
  target would be a no-op dressed as a fix)
- therefore `target != actual`, which is the condition the reconciler
  consumes

Modules tested:

- **Container runtime.** Pull failure leaves the existing running container
  present and unremoved. The recreate path refuses an unmanaged container.
  The call sequence is exactly `pull, remove, create, start`. Existing
  idempotency and progress-reporting tests are untouched and still pass.
- **Orchestrator container sync.** Drift on a single-container app is
  repaired. Drift on one node of a multi-container app is repaired while
  the healthy node is untouched. An `ERROR` node is not retried. An
  uninstalling app is not re-driven. A container that is genuinely running
  causes no reset, so the repair cannot become a per-pass recreate loop. The
  catalog-miss guard from PR 6 is unchanged.

Prior art followed:

- `orchestrator_containers_test.go` already established the shape: a
  `FakeAppStore`, a fake catalog cache, a `MockContainerRuntime`, and an
  assertion about the resulting state rather than about calls made. The new
  tests extend that file rather than introducing a new harness.
- The `TestSyncContainerState_MultiContainerSkipped` test was **deleted,
  not adapted**. It asserted the wrong contract, so keeping it in any form
  would preserve the bug as a requirement. Replacing a test that pinned
  wrong behavior is part of the fix, not collateral damage.
- The runtime's ordering assertion follows the existing fake-client pattern
  in `runtime_test.go`, extended with a chronological event log because
  "which happened first" was previously unobservable.

Deliberately not tested: the full end-to-end "kill container, watch it come
back" journey. That is the `./bloud e2e lifecycle` suite's job, and it is
listed under Further Notes as the outstanding verification step.

## Out of Scope

- **Item 10 (health surface).** A drifted-and-repaired container is now
  self-healing, but a Podman socket that is unavailable entirely still has
  no degraded signal, the startup gate is still SQLite-only, and a failed
  system-app convergence still `os.Exit(1)`s the control plane. That is
  PR 11's remaining scope and a different problem: visibility, not repair.
- **The `operations` ledger single-row contention.** Multi-container nodes
  of one app still race one `operations` row on phase writes. PR 7 made
  that last-writer-wins-by-ledger-semantics rather than a lost write, and
  the ledger records it as a design constraint, not a correctness bug.
  Untouched here.
- **`primaryContainerNode` ordering convention (item 20).** Which container
  is "last" still decides inter-app edges and SSO ownership. Related to
  multi-container handling but a separate decision with its own blast
  radius.
- **Graceful drain before recreate.** `Ensure` still removes with
  `force=true`. A spec change still interrupts the container immediately;
  this refactor changed *when* we destroy relative to *pulling*, not how
  gently we destroy.
- **Restart-policy interaction.** Containers with `restartPolicy: always`
  are largely restarted by Podman itself before the reconciler notices.
  Drift repair is the backstop for the cases Podman does not handle, not a
  replacement for restart policies.
- **Drift for system apps.** `SyncContainerState` skips `IsSystem` apps,
  as before. Traefik and Authentik drift is out of scope for this path.

## Further Notes

**Why the old code was written that way, and why that is the actual lesson.**
The store write in the drift path was not stupid. It was the natural move for
someone reading the database as the system of record. The bug is that this
system has two records, and only one of them is load-bearing for behavior.
The store is what the user reads. The graph is what the engine acts on. A
correction that updates only the readable record is a correction that
changes nothing. Every future "sync reality with intent" feature has to
answer one question first: *which record does the actor actually read?*

**The comment was the bug's hiding place.** Both defects were documented as
if they were design decisions. "Mark stopped so it can be re-created"
described an intention that no code fulfilled. "Multi-container lifecycle is
tracked via graph events, not this path" described a division of labor that
did not exist. A confident wrong comment is worse than no comment: it stops
the next reader from checking. Both comments are gone, replaced with the
reasoning for the new behavior.

**Verification still outstanding.** The unit and race tiers are green, but
the live behavior has not been observed end to end. Before this is treated
as fully proven, run:

```sh
./bloud e2e lifecycle
```

and ideally the manual check it does not cover: with host-agent running,
`podman rm -f apps-jellyfin`, then watch the next convergence pass recreate
it without a restart. That is the exact scenario the old code could not
handle, and it is worth watching once rather than trusting.

**Note on the pre-existing red test.** `cd cli && go test ./...` fails on
darwin at `TestResolveBackendPrecedence` for reasons unrelated to this
change (macOS auto-resolves to its single available backend; the test
expects a stored `qemu` preference). It is invisible to the pre-commit hook,
which omits `cli` tests. The ledger already records it; fixing the
expectation for single-backend hosts is a separate small change.
