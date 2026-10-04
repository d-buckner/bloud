# Plan: Show what an install will do before it happens

> Status: draft

## Goal

Make the install and uninstall journeys tell the operator what is about to
happen, instead of telling them what already happened.

Concretely: installing `affine-mcp` should say "this also installs AFFiNE"
before the button is pressed, not after. Uninstalling AFFiNE should say which
apps depend on it. And where the platform currently picks an integration
provider silently, the operator should get to pick.

This is a UX plan. The reconciler barely changes: almost all of the modeling
work already exists in `internal/catalog/plan.go` and is never shown to anyone.

## The current journey

```
catalog card
  -> installApp(name)                        web/src/routes/catalog/+page.svelte:71
  -> POST /api/apps/{name}/install          internal/api/apps_module.go:186
  -> NewInstallAppIntent(name)              internal/engine/orchestrator/intent.go:35
  -> 202 Accepted + the app row
  -> modal opens on a progress timeline     web/src/lib/components/AppInstallModal.svelte
```

Every step after the click is reactive. `AppInstallModal` renders an install
progress timeline, a recent-activity list, and a Retry button on failure. It
opens once the install is already committed, so the most informative thing it
can say arrives too late to change anything.

Uninstall is thinner. The entire body of `UninstallModal.svelte` is:

> Are you sure you want to remove **affine-mcp**?

That is the raw catalog id rather than the display name, with no dependents, no
data warning, and no `clearData` option.

## What already exists and is unused

Three findings shape this plan, all verified against the tree.

**1. The plan types are already computed and already JSON-tagged.**
`InstallPlan` (`internal/catalog/plan.go:7`) carries `CanInstall`, `Blockers`,
`Choices`, `AutoConfig`, and `Dependents`. `RemovePlan` carries `CanRemove`,
`Blockers`, and `WillUnconfigure`. `PlanInstall` resolves every integration a
app declares into one of those buckets. None of it reaches the browser: there is
no install-plan endpoint, and `choices` appears nowhere in the Svelte tree.

**2. The seam for user choices exists and is empty.**
`applyInstallIntent` calls:

```go
// internal/engine/orchestrator/pipeline.go:79
integrations := buildIntegrationConfig(nil, plan.AutoConfig, plan.Choices)
```

That first parameter is `userChoices map[string]string` (`config_builder.go:19`),
and it is hardcoded `nil`. The function is already built to merge the operator's
selections ahead of the auto-config. `InstallAppIntent` simply has nowhere to
carry them, so `buildIntegrationConfig` falls through to `choice.Recommended`
for every required integration. The operator never picks their provider; they get
whatever metadata marked `default: true`.

**3. `PlanRemove` is dead code in production.**
It is referenced only from test fakes and mocks. `applyUninstallIntent`
(`pipeline.go:127`) marks the app `uninstalling` without consulting it, so the
block the planner models ("AFFiNE cannot go, `affine-mcp` requires `appApi` and
has no alternative") is computed and then ignored. That is bug-shaped, not just
a missing feature: the safety property exists in the model and nothing enforces
it.

## Design

Three phases, where today there is one.

### Phase 1: pre-flight

Clicking a catalog card opens the plan, not the progress view. The dialog reads
`GET /api/apps/{name}/install-plan` and renders what the planner already
resolved:

| Plan field | Dialog row |
|---|---|
| providers in `Choices`/`AutoConfig` not yet installed | "Also installs: **AFFiNE**" |
| providers already installed | "Uses: Traefik *(already installed)*" |
| `Choices` with more than one installed option | radio group: "Which calendar server?" |
| `Blockers` | "Cannot install: X requires a Y" |
| `Dependents` | "Will be configured: Hermes picks up 1 new tool namespace" |
| `estimatedSizeMB` summed over the set | "About 150 MB" |

`estimatedSizeMB` is already on the catalog model
(`internal/catalog/models.go:20`) and already populated per app (affine-mcp
150, hermes 5000), so the footprint line needs no new metadata.

The dialog is the only place a choice can be made, which means the POST has to
carry the answer. That is finding 2: add an integrations map to
`InstallAppIntent` and thread it into the parameter that is already waiting for
it.

### Phase 2: commit and progress

Unchanged. The existing timeline modal is good at what it does. It just stops
being the first thing you see.

### Phase 3: attribution

Anything Bloud installs on behalf of something else says so, in the place that
thing is listed. "AFFiNE MCP: attached to Hermes, added when you installed
AFFiNE," with an Uninstall affordance.

This matters more than it looks. The catalog currently carries `affine-mcp` and
`caldav-mcp` with `headless: true`, which means they are in the catalog and the
API with no dashboard tile. They are discoverable only by going looking, and
nothing there explains why they exist. If the pre-flight checkbox is the primary
path, the catalog entry stops being the discovery surface and becomes the audit
surface, and an audit surface with no provenance is not an audit surface.

## Escalate only on signal

The one constraint that decides whether this helps or hurts.

A dialog that shows the same three system rows every time trains the operator to
click through it, which destroys its value on the occasions it has something
real to say. So the dialog collapses by plan content:

- No uninstalled providers, no choices, no optional companions: plain confirm,
  or no dialog at all.
- Anything genuinely new: the full plan.

The first Jellyfin install saying "this also brings Authentik" is news. The
fifth install repeating it is noise. The rule is: show the rows that carry
information, suppress the rows that are always true.

## The optional-companion rule

This is where the "include MCP?" checkbox comes from, and it needs one graph
query rather than a per-app special case.

> A companion is **offerable** if every integration it marks `required` is
> satisfied by (currently installed) union (the app being installed).

So installing AFFiNE on a machine that already has Hermes surfaces:

> ☐ Also install **AFFiNE MCP** so your agent can read and write your documents
>
> Runs with your admin account. 106 tools, including delete. You can turn this
> off at any time.

Two notes on the mechanics and the copy.

`FindDependents` (`internal/catalog/graph.go:58`) cannot be reused as-is: line
62 skips any ref whose app is not in `installedSet`, so it only ever returns
already-installed dependents. The offerable query needs the unfiltered edge set,
which is a small sibling function, not a change to the existing one.

The copy states the consequence, not the artifact. "Include MCP" is a noun and
an operator cannot reason about a noun. "Your agent can read and write your
documents, with your admin account" is a consequence, and they can reason about
that immediately. `docs/features/mcp.md` is blunt about the properties that
belong in that sentence: the wrapper reaches AFFiNE with the bootstrap owner's
credential, the default tool profile is `full` including `delete_workspace`, and
one published credential means one principal with no per-human authorization.

**Default off, always.** The downside is asymmetric. Default on and one agent
deletes a workspace burns the operator's trust in the whole platform. Default
off and the cost is one tick, forever. The suggestion mechanism, not the default,
is what provides discoverability.

## Suggestions for the time gap

A pre-flight checkbox only covers what is installed at the moment of install.
Install AFFiNE six months after Hermes and nobody asks again, so the same rule
needs a deferred form:

> You added AFFiNE. Your agent can now read and write it. [Attach] [Not now]

Rules that keep this from becoming spam:

- One suggestion per (app, reason), once.
- Never re-ask after dismissal. This is a real requirement, not a nicety: a
  push rule that re-evaluates on the ~60s self-healing pass will re-nag the
  operator for a thing they already declined, unless the decline is persisted.
- Batch: "3 things your agent could access," never three separate notices.
- Surface in the existing activity area (`recentActivity` in
  `web/src/lib/stores/appProgress`) rather than as a modal.

## Uninstall is the same problem, and worse

`RemovePlan` already knows `WillUnconfigure` and `Blockers`. Surfacing it turns
"Remove AFFiNE?" into:

> Removing **AFFiNE** will unconfigure 1 integration:
> - AFFiNE MCP loses its target and will be removed
>
> Your data is kept. [Remove] [Cancel]

And the `CanRemove: false` case should block at the API rather than being
computed and discarded. Today the operator can press Remove on an app that the
planner says cannot be removed, because nothing checks.

Two smaller fixes ride along: use the display name rather than the catalog id in
the heading, and expose the `clearData` flag the intent already carries
(`NewUninstallAppIntent(name, clearData)`) instead of hiding it.

## Backend changes

Small, and mostly already shaped for it.

1. **`GET /api/apps/{name}/install-plan`** returning the existing
   `InstallPlan`. Do not re-derive this in Svelte: the client has the catalog
   but not the resolution logic (`buildIntegrationConfig`, `computeAppDeps`),
   and a TypeScript copy would be a second source of truth that drifts on the
   first metadata edit.
2. **`Integrations map[string]string` on `InstallAppIntent`**, threaded into the
   `userChoices` parameter at `pipeline.go:79`.
3. **`GET /api/apps/{name}/remove-plan`**, and `applyUninstallIntent` actually
   honoring `CanRemove`.

The offerable-companion query is a fourth, but it is a pure function over the
existing graph.

## Non-goals

- **Making the preview transactional.** The plan is computed at T and applied at
  T plus a small epsilon, and the world can change between. The orchestrator
  re-plans on apply regardless. The preview is advisory and the copy should not
  promise more than that; no locks.
- **Changing reconciler semantics.** Auto-attach, opt-out persistence, and the
  companion rule are all decisions made before an intent is submitted. The
  orchestrator stays the single writer (invariant 1) and knows nothing about
  which UI produced the intent.
- **A new app tier.** Earlier discussion considered a "platform service" tier
  between `isSystem` and the catalog for Radicale. That is a separate question
  and this plan does not depend on it: the checkbox works with the existing
  `headless` flag.

## Open questions

1. **Does the pre-flight dialog replace the one-click framing?** The catalog
   subtitle currently reads "One-click installs with automatic integration."
   A plan dialog is two clicks. Worth deciding whether the honest framing is
   "one click, and we show you what it does" or whether trivial plans skip the
   dialog entirely so the one-click claim stays literally true.
2. **Where does a declined companion live?** A settings-backed opt-out table per
   (consumer, provider) pair is the obvious shape and is also what the deferred
   suggestion needs. Whether it is a settings row, an app-store column, or a
   dedicated table is unresolved.
3. **Choice memory.** If the operator picks a non-default provider in the
   pre-flight dialog, does that preference carry to future installs of other
   apps in the same integration slot? Probably yes, and it needs the same
   storage as question 2.
4. **How much of the plan to show for a large fan-out.** Installing a stack that
   brings six apps is a long list. Collapsing by category, or showing only the
   uninstalled ones, both work; neither is obviously right.

## Suggested build order

1. The read endpoints (`install-plan`, `remove-plan`) against the existing plan
   types. No UI, no behavior change, and it makes the plan data inspectable by
   the CLI and by tests immediately.
2. `Integrations` on the install intent, wired to the empty `userChoices`
   parameter, with a test that a supplied choice beats `Recommended`.
3. `applyUninstallIntent` honoring `CanRemove`.
4. The pre-flight dialog, with the escalate-on-signal collapse.
5. The offerable-companion query and the MCP checkbox.
6. Attribution on catalog entries, and the deferred suggestion.

Steps 1 through 3 are independently shippable and each one is small.
