> Status: brainstorm

# Plan: external apps: launchers, remote installs, and off-host providers

## Problem

Bloud's catalog apps run *on* Bloud: `install affine` pulls four containers,
reconciles them, and their `provides:` contracts become real because the
configurator booted the app and minted its credentials.

An operator often already runs the app somewhere else. They have AFFiNE on a
NAS, Jellyfin on a small box, a Prowlarr instance a friend hosts. Bloud should
let them say "I already have an AFFiNE at `https://affine.example.com`, here are
the credentials", and then every consumer in the catalog that integrates with
AFFiNE (`affine-mcp`, or a future Hermes tool) wires to the *remote* install
instead of a container Bloud would otherwise have to run.

That is **external app support**: a catalog app that is instantiated not as
containers on the app network but as an operator-supplied endpoint plus
credentials, and that still satisfies its contracts for consumers.

Seen a little wider, that is one of three shapes the same idea takes. The
operator may also want a tile that just opens something they already run (a
*launcher*: a URL and an icon, nothing more), or an off-host AI endpoint (an
*inference server*, which the AI Settings already model under `ai_upstreams`).
All three are one thing: a named, operator-declared pointer to something Bloud
does not run. The section below gives them one data model.

## The motivating walkthrough

Today, wiring `affine-mcp` to AFFiNE means running both locally:

1. Install `affine` (postgres + redis + server containers). Its configurator
   boots the server, mints the bootstrap owner, and publishes under the `appApi`
   contract: `password` as a secret, `username` and `workspaceId` as
   runtime-published values.
2. Install `affine-mcp`. Its `PreStart` reads the resolved `appApi` binding and
   writes `AFFINE_BASE_URL`, `AFFINE_EMAIL`, `AFFINE_PASSWORD`, and
   `AFFINE_WORKSPACE_ID` into the wrapper's saved config
   (`apps/affine-mcp/configurator.go:renderConfig`).

Under external support, step 1 disappears. The operator registers AFFiNE as an
external app with:

```
endpoint:   https://affine.example.com
appApi:
  password:    <the companion account's real password>
  username:    <the companion account's email>
  workspaceId: <the workspace Bloud should pin>
```

Step 2 is unchanged, and its configurator is unchanged: it still reads
`binding.BaseURL`, `binding.Username`, `binding.Password`, `binding.WorkspaceID`.
The only difference is that `BaseURL` is now the operator's origin instead of
`http://apps-affine:3010`, and the username/workspace came from the operator
instead of from a local boot. The consumer never learns which.

That is the load-bearing claim of this whole feature: **a consumer configurator
should need zero changes**, because the contract abstraction already hides
"where the provider lives" behind `ProviderRef`.

## What the code already knows

The pieces are all here; this feature is a generalization, not a new paradigm.

- **The contract registry** (`internal/catalog/contracts.go`) already states,
  per contract, exactly what a provider must publish: `Secrets[]` and
  `Values[]`. That is a ready-made template for "what to ask the operator when
  the provider is external": one endpoint plus each secret plus each non-optional
  value.
- **`ProviderRef` already has a non-container shape.**
  `pkg/configurator/interface.go` defines `Kind` (`"app"` | `"instance"`), and
  for the instance provider `Node`, `Port`, and `BaseURL` are deliberately empty
  while a contract-specific `Endpoint` field carries the dialable value. External
  apps are the same idea with a different `Kind` and with the *catalog ID*
  retained (an instance is anonymous; an external app is still "AFFiNE").
- **A non-app provider is already filtered out of the graph.** `source: instance`
  (`internal/catalog/types.go`) produces no node and no edge;
  `computeAppDeps` (`orchestrator/graph_build.go`) skips `declared.Source != ""`
  so a setting never becomes a phantom container. External apps need the same
  discipline, keyed differently.
- **The resolver already separates "address" from "payload".**
  `orchestrator/integrations.go` builds each binding from `providerRef` (the
  address) plus `offer` and `publishedSecret`/`contractValue` (the payload). The
  only new work is a branch on "the provider is external" for each of those three
  reads.
- **`inference-provider-settings.md` is the precedent.** It faced the same
  collision (the operator's external OpenAI-compatible server is not an
  installed app) and solved it with a second provider *source* rather than a
  new kind of catalog entry. External apps are that source, generalized from one
  hardcoded Settings section to "any catalog app that provides a contract".

  The instance is also already a *registry*, not a single value: `ai_upstreams`
  stores a JSON list of N endpoints (`internal/inference/settings.go`), each
  entry carrying a stable `ID` so a rename does not read as remove-plus-add. The
  operator-declared provider set is N-shaped before this plan touches it, which
  is the fact the litellm shape below builds on.

## Core insight: external is an *instantiation*, not a new vocabulary

The tempting design is a new kind of catalog entry: a "logical app" or an
`external:` metadata block. `inference-provider-settings.md` already argued
against that shape for inference, and the same argument holds here, harder.

The catalog metadata for `affine` already says everything the resolver and the
consumer need: it `provides: appApi` with `secrets: [password]` and
`runtimeValues: [username, workspaceId]`. Whether that `appApi` is satisfied by
four containers on `apps-net` or by `https://affine.example.com` is a property
of the *installation*, not of the *app*. So:

- **No new *catalog* metadata.** The app's `provides:` stays as it is; no
  `external:` block in `metadata.yaml`, no new contract names. What *is* new is
  a small runtime entity (the external-app record), and that lives in the store,
  not in the catalog.
- **External is its own entity, not a property of the installed row.** The
  operator adds an external app (a `kind` discriminator plus shared fields), and
  the catalog-app case is one `source` of it (a `provider` pointing at a catalog
  app). A given catalog ID can be run locally *or* pointed at externally, not
  both at once in v1 (axis H).
- **The catalog's workload description is ignored for an external row.** The
  `affine` metadata still lists `containers:` (postgres, redis, server),
  `port: 3010`, `sso:`, and `extraPorts:`. Every one of those describes the
  local workload the operator is choosing *not* to run. For an external row only
  `provides:` (and the contract registry behind it) means anything, because that
  is what the resolver and the consumer read.

## The litellm shape: one contract, providers of three kinds

The instance provider is the precedent for a second reason the AFFiNE
walkthrough does not show. `instance` is not just "a provider with no
container"; it is already an operator-declared provider registry. `ai_upstreams`
holds a JSON list, and each entry keeps a stable `ID` across saves. That is the
same shape an external app's config takes, one level up: a named thing, an
endpoint, a credential.

What differs is what the name hangs off. An external AFFiNE hangs off a catalog
app's identity (`affine`). An external AI endpoint hangs off the reserved
`instance` identity, because there is no catalog app for "an OpenAI-compatible
server"; `modelSource` is the contract that stands in for one.

A future `litellm` gateway makes the two meet in one consumer: it declares
`modelSource` as `multi`, and that one contract resolves to providers of
*different kinds* at once:

```
litellm (integrates modelSource, multi)
  +-- source: instance   -> N upstreams (the operator's external AI endpoints)
  +-- app: ollama        -> 1 provider (a locally installed runtime)
        |
        +-> litellm provides inference (single) -> hermes, ...
```

The resolver already has both halves, but not the union:

- `bindAppProviders` resolves every installed *app* provider of a contract,
  generically and as a set.
- `resolveInference` resolves the *instance* provider as a single binding,
  special-cased, and `ActiveUpstream` returns "the first enabled" rather than
  "all of them".

The code even names the seams that make the union the obvious next step:
`ActiveUpstream` is documented "v1 resolves to a single upstream ... a
multi-upstream design changes this function rather than every caller", and
`SecretAPIKey` is documented "one name for one upstream ... a multi-upstream
Settings UI needs a key per upstream". Both were written as deliberate
single-entry shapes with the multi-entry change located in one place.

The consequence for this plan: **external apps must be a third member of the
same set, not a second single-binding special case.** The end state is one
uniform resolution: a contract yields zero-to-N bindings across local apps, the
instance, and external apps, each built by the same `bindContract` payload, and
the instance-versus-external distinction is only *which identity the operator's
config hangs off*.

Two things follow, recorded here so they are not rediscovered:

- **Collapse `instance` into `external`?** An instance provider is an external
  provider whose `App` is the reserved `instance` name instead of a catalog id.
  Collapsing them makes `ProviderRef.Kind` a two-way split (local app vs
  external) instead of three-way, and `source: instance` becomes sugar for "an
  external provider owned by the instance". The cost is touching the inference
  resolver, which is exactly the code already marked for the multi-upstream
  change; doing both in one pass may be less work than keeping two special cases
  and generalizing around them separately.
- **The litellm shape needs `modelSource` to go multi first.** That is a change
  to the instance provider (`ActiveUpstream` becomes "all enabled upstreams", a
  key per upstream) plus an `ollama` app, and it is orthogonal to external apps:
  litellm does not need external apps, and external apps do not need litellm.
  They share the `bindContract` union, which is the piece worth designing for
  both at once.

## One entity, two kinds

The three shapes share one record, and the kinds are structural, not
contract-shaped:

```
external app
  +-- id, name, url, icon
  +-- kind                          # the shape; decides which fields exist
        +-- launcher  -> no role; url is what the tile opens
        +-- provider  -> plays a provider role
              +-- source: app(affine)             # a remote install of a catalog app
              +-- source: contract(modelSource)   # a bare contract provider
```

`kind` names a *shape*, not a contract. A launcher has no role and no contracts.
A provider has a `source`, and the source names what it satisfies:

| kind | source | contracts satisfied | extra fields |
|---|---|---|---|
| `launcher` | none | none | nothing; `url` is the tile target |
| `provider` | `app: affine` | every contract in `affine.provides` | per-contract secrets/values from `affine.provides` |
| `provider` | `contract: modelSource` | `modelSource` | `apiKey`; the discovered model list |

`modelSource` is a *value* of `source`, not a kind, and that is the whole point.
Baking it into the kind enum would fork the enum for every off-host contract
(`pvr`, `mediaServer`, `appApi`, `mcp`) and add a resolver arm for a shape that
is identical every time. The structural split is launcher-versus-provider; the
specificity lives in the `source`.

That split is not invented here. A consumer's `compatible:` entry is already
`app: <name>` or `source: instance` (`internal/catalog/types.go`): name a
provider by identity, or name a role the operator fills in. An external
`provider` record is the runtime counterpart of that declaration:

- `source: app(affine)` matches a consumer that declared `compatible: [{app:
  affine}]`: the consumer named AFFiNE specifically, so the operator's remote
  AFFiNE satisfies it.
- `source: contract(modelSource)` matches a consumer that declared
  `source: instance` on `modelSource`: the consumer named the role, not a
  provider, so any operator-declared thing that fills the role satisfies it.

The existing `source: instance` is the seed of this generalization. Today it
means exactly one thing (the AI Settings), which is why "instance" has read as a
magic word. Under this model `instance` is just the set of `provider` records
with `source: contract(modelSource)`, and a future off-host role (say a
`mediaServer` a friend hosts) is `source: contract(mediaServer)`, not a new
kind.

The `app` source is the one whose contract set is read from the catalog rather
than named. Everything else about it (icon, display name, the per-contract form)
is derived from that catalog entry, so "remote install of a thing Bloud already
knows" stays one source and needs no hand-typed contract list.

`url` is one field whose consumption is decided by what consumes it: a launcher
opens it in a browser; an `app` source hands it to consumers as `BaseURL` (an
origin, contract paths appended); a `contract` source hands it to whichever
consumer that contract names, in whatever shape that contract expects (an
OpenAI base URL for `modelSource`). One field, per-source validation.

## Design axes

Each axis below has a recommendation and the alternatives. They are separable,
but the recommendation for A drives the rest.

### A. Representation: one entity, not an install mode

The unified model above settles the first design question. An external app is
its own entity, not a flag on an `apps` row, because the `launcher` kind has no
catalog app to be "installed" under. The earlier install-mode idea worked for
the catalog-app case and reused rename/uninstall/dependents for free, but it
cannot express a launcher or a bare AI endpoint without pretending they are
catalog apps they are not.

The entity is a small registry: a `kind` discriminator plus the shared fields.
Its effects are two, and both are type-dependent:

- **Presentation.** Every external app renders in the dashboard grid like an
  installed app (position, rename, remove), so the grid sees one unified list. A
  launcher's tile opens `url`; a catalog app's tile opens `url` too (the remote
  install's own UI); a `modelSource` app draws no tile, the way the AI Settings
  never did.
- **Wiring.** Only `app` and `modelSource` external apps feed contract
  resolution. A `launcher` never reaches the resolver.

What the install-mode idea was really buying (rename, uninstall, dependents,
dashboard listing) comes from *rendering the registry into the same grid and
planning layers* as installed apps, not from storing external apps in the `apps`
table. The convergence layer (`populateGraphNodes`, `computeAppDeps`) never sees
external apps at all, which is the cleaner version of the load-bearing filter:
there is nothing to skip, because a launcher is never a candidate node.

### B. The operator surface

Derived from the contract registry, not hand-written per app. The form asks
for the endpoint plus only the fields the operator actually owns:

- the **endpoint** (an HTTP or HTTPS origin; `BaseURL` for consumers, so no path, and a
  path lives on the contract's `values`, never in the origin);
- one input per distinct required **secret name** across every contract the
  app `provides:`. One credential fills every contract that declares the
  same name: Radarr's `pvr` and `icsFeed` offers both publish `apiKey`, it
  is the same key, and the server shares it (`sharedSecret`), so asking
  twice only lets the two copies disagree;
- one input per required **value the catalog does not already answer**. A
  value the provider declares statically under `provides: <contract>:
  values:` is a fact about the app rather than about this install, so the
  form never shows it and the server merges it back in on save.

That derivation assumes the credential a contract declares is something the
operator can read out of the remote app's own settings page. Where it is not,
the app registers a **credential exchange** instead and the form asks for a
sign-in: Bloud logs in to that instance and stores whatever it hands back. See
[remote-app-signin-exchange.md](remote-app-signin-exchange.md), which is the
Seerr case (its admin is a Jellyfin login and its key is a string nobody has ever
looked at) and the only app that needs it today.
- one input per value the offer claims under `provides: <contract>:
  operatorValues:`. That list is the app saying "this static default describes
  the install Bloud booted, not the app". Jellyfin's `adminUsername` is the
  account Bloud created; a Jellyfin down the hall has whatever admin its own
  operator named. The form asks for it, shows no default, and the constant is
  never merged in, because writing it into a remote record is not a gap but a
  confident wrong answer that surfaces later as a login error in whichever
  consumer received it. The loader keeps every entry honest about the one shape
  where it means something: declared statically, required by the contract, and
  not supplied at runtime.

Optional values stay out of the form entirely. An optional field with
nothing to say reads as an empty binding either way, so the input only
manufactures a blank.

The record's **name** is not asked for on add either. It is the catalog
app's own display name, and a field prefilled with the only thing it can
hold is a field that gets skipped, not read. Renaming is the Configure
modal's job, which is the same surface that already holds the endpoint and
the credential. A launcher still asks for a name, because a launcher has no
catalog app to answer it.

For a remote Sonarr or Radarr that leaves the endpoint and one API key. For
AFFiNE it leaves the endpoint, `appApi.password`, and `appApi.username`
(`workspaceId` is optional in the registry). For Jellyfin it leaves the endpoint,
`mediaServer.adminPassword`, and `mediaServer.adminUsername`, because that name
is the operator's to supply. No per-app code, and no contract grouping either:
which roles a record fills is not something a person needs in order to fill the
form in.

The registry's `Secrets`/`Values` stay the single source of truth for what
the form asks, exactly as they are for `validateProvides`, and the value
half of the derivation reads the same `declaredValueKeys` the validator
applies to what the form sends back. Two independent derivations of one
registry is how a form ends up demanding what the server refuses, or hiding
what it needs.

Resolved for the fields, still open for the capability. The thing the opt-in was
meant to supply, a per-app statement of what the operator has to configure, is
data now: `operatorValues` names the inputs a remote operator owns, and the
loader rejects an entry that would ask for nothing. What remains open is a block
saying what externalizing an app *means* (e.g. "the remote instance's auth is
its own; Bloud does not SSO it") and excluding the contracts where externalizing
is nonsense; `nonExternalizableContracts` covers that last part in Go for now.

### C. `ProviderRef` and the resolver

Three `ProviderKind` values fall out of the source model, and they name the
*origin* of a provider rather than its shape:

| Kind | origin | `App` | address field |
|---|---|---|---|
| `ProviderKindApp` | a local catalog app (containers) | catalog ID | `BaseURL` = `http://<node>:<port>` |
| `ProviderKindExternalApp` | a remote catalog app (`source: app`) | catalog ID (`affine`) | `BaseURL` = the operator's URL |
| `ProviderKindSetting` | a bare contract (`source: contract`) | reserved/empty | the contract's own address field |

`ProviderKindExternalApp` is the new one. For an external provider:

| Field | Value |
|---|---|
| `Kind` | `externalApp` |
| `App` | the catalog ID (`affine`); retained, unlike a setting |
| `Installed` | `true` (the operator registered it) |
| `Node` | empty |
| `Port` | `0` (the port lives inside the endpoint) |
| `BaseURL` | the operator's endpoint |
| `LocalURL` | the operator's endpoint (there is no host/container split) |

`BaseURL` is filled, where a setting leaves it empty. That is the difference
that makes consumers "just work": every consumer already reads `BaseURL` as "the
address my own process should dial", and for an `app` source that address is the
operator's origin, so `BaseURL` is the right field and no new payload field is
needed.

`ProviderKindSetting` is not new; it is the existing `ProviderKindInstance`
renamed. The old name said "the instance's own configuration", which was only
true while there was exactly one such thing (the AI Settings). Once a `source:
contract` can name any contract, the honest word is "setting": an
operator-declared value with no catalog app behind it. It feeds
`ModelSourceBinding.Endpoint` today exactly as `instanceInferenceSource` does,
and a future `source: contract(pvr)` feeds `PVRBinding` with the operator's URL
in `BaseURL` and no container node.

The rename carries through the three places the old name lives: the
`ProviderKind` value, the reserved `App` string, and the `source: instance`
metadata value a consumer declares (`InstanceProviderSource`). Whether the
metadata value stays `instance` for compatibility or becomes `source: setting`
is a catalog-change question to settle alongside it.

Resolver changes, all localized to `orchestrator/integrations.go`:

- `providerRef` branches on the row being external and returns the external
  shape above instead of computing `http://<node>:<port>`.
- `publishedSecret` and `contractValue` read from the external app's own scope
  (below) instead of the app's local scope.

### D. Secrets and runtime values

The operator-supplied values must not collide with a local install of the same
catalog ID, and must not leak into the app's own scope. The secrets manager
already keys everything by app name (`GetAppSecret(appName, key)`). For an
external row, use a distinct scope (`external/<name>` or a per-instance id) for
both the secrets and the operator-supplied values.

The subtle case is `runtimeValues`. Locally, `username` and `workspaceId` are
minted by the AFFiNE configurator at boot and published through
`SetAppContractValue`. For an external row there is no boot and no configurator,
so the operator must supply them directly. `contractValue` therefore needs a
third source: static metadata, runtime-published, then *external config*. The
external config wins, and there is no writer behind it, which is exactly right:
a value the operator typed is authoritative and static until they edit it.

Secrets are the same shape: the operator supplies `appApi.password`, stored
under the external scope, read by `publishedSecret` when `Kind == external`.

### E. Graph, ordering, status

- **No node, no edge.** An external provider has nothing to converge, and under
  the entity model it never enters the convergence layer at all: `computeAppDeps`
  and `populateGraphNodes` iterate installed apps only, and external apps are not
  installed apps. The safety property is that an external AFFiNE (`source:
  app(affine)`) still has catalog metadata that declares `containers:`, but those
  containers are never expanded because the external record is never fed to the
  container builder.
- **No ordering needed.** The consumer (`affine-mcp`) needs no edge to wait on,
  because the operator's credentials exist from the moment they are saved. The
  consumer's own lifecycle is unchanged and its `PreStart` reads the binding on
  the next pass.
- **Status is terminal, not converged.** There is no lifecycle to drive. An
  external row is `running` (or a dedicated `external` status) immediately after
  a valid save; an invalid endpoint is rejected at the API boundary rather than
  stored. Whether Bloud *health-checks* the remote endpoint for display purposes
  is a separate question; the inference upstream already does model discovery,
  so there is precedent for a read-only probe. But it must never be the thing
  that decides "installed" (invariant 15's anti-probe rule).

### F. Routing, SSO, the dashboard tile

An external app is not on Bloud's network, so it is not routed and does not join
the identity provider:

- **No Traefik route.** Route generation (`apps-routes.yml`) is for containers
  Bloud serves. An external row contributes no router.
- **No SSO provisioning.** The remote instance authenticates itself; Bloud has
  nothing to provision and must not mint an OIDC client for a host it does not
  run. This is also the answer for `sso`-strategy consumers: `affine-mcp` is a
  local container and keeps its own SSO/proxy wiring; only its `appApi` binding
  changed.
- **The tile opens the endpoint.** Where a local app's tile opens the routed
  origin, an external app's tile is a link to the operator's endpoint. It shows
  in the installed list, in the grid, and in the developer graph as an
  "external" node (no box, no containers); presentation of a real wiring, the
  same way the inference plan renders the instance's "AI Model" node.

### G. Which contracts first, and which are meaningful

Mechanically any contract is externalizable: the registry already says what to
ask for. But not all of them *mean* something when pointed off-host:

- **Natural first set** (endpoint + a credential, for user apps with consumers):
  `appApi` (the motivating case), `mcp`, `agentApi`, `pvr`, `mediaServer`,
  `icsFeed`, `caldav`, `downloadClient`.
- **Already covered elsewhere:** `inference`/`modelSource` are the instance's
  Settings surface; they do not need the external-app mechanism. Making
  `modelSource` multi (the litellm shape above) is a separate prerequisite, not
  part of this feature.
- **Deferred, probably never:** `proxy` (Traefik is Bloud's own), `database`
  (invariant 3 bundles per-app databases; BYO postgres is a different feature),
  and `sso` (pointing the identity provider at a remote Authentik is a
  deployment-shaped problem, not an app-shaped one).

The MVP ships the mechanism generically (the registry derivation) but *exercises*
it on `appApi` to `affine-mcp` end to end, because that is the story that proves
consumers need no changes.

### H. Local vs external: exclusivity and multiplicity

- **v1: one instantiation per catalog ID, local XOR external.** You either run
  AFFiNE here or point at a remote one. This matches intuition and keeps the
  set-model's `multi: false` consumers (which pick the first complete binding)
  from ever facing two `affine` providers they cannot tell apart.
- **Multiplicity is an explicit non-goal for v1.** Multiple *remote* instances
  of the same app, or a local + a remote, are a later problem and would require
  the consumer side to grow an identity for "which one"; precisely the "choice
  system" `DeclaredProviders` deliberately does not have.

## What does not change

- **Consumer configurators.** `affine-mcp` (and every future consumer) reads
  `ProviderRef.BaseURL` and the contract payload; those fields carry the external
  values. No `Kind` branch in consumers, no new payload fields.
- **The contract registry.** No new contracts, no new `ValueSpec` flags. The
  `Secrets`/`Values` the operator is asked for are exactly the ones a local
  provider already publishes.
- **The set model.** `DeclaredProviders` still returns the declared set; "is it
  wired" is still answered from the store, not a probe; `required` still only
  installs the default local provider (an external provider is never
  auto-installed by installing a consumer; the operator adds it explicitly).
- **Invariant 15.** A consumer still reads only the secret names it declared in
  `requires`. The external scope is gated by the same `requires` check in
  `publishedSecret`.

## The one load-bearing filter

Everything else is additive; this one is the safety-critical part. The catalog
metadata for an external AFFiNE (`source: app(affine)`) still declares
`containers:` (postgres, redis, server), because that metadata describes the
*local* workload. The external record must never cause those containers to be
built, started, or routed:

- `populateGraphNodes` / `createGraphNodes` iterate installed apps only; external
  apps are not installed apps, so they produce zero nodes.
- the install intent's container/convergence path runs only for installed apps;
  an external app is added by a separate add-external path that persists the
  record and its secrets, nothing more.
- route generation sees no external app, so no router is emitted.
- SSO provisioning sees no external app, so no OIDC client is minted.

This is the same class of guard as `computeAppDeps`'s `declared.Source != ""`
filter, but it lives at the *boundary between the external registry and the
convergence layer* rather than inside it: the convergence layer is never handed
an external record at all. A test that pins "an external app produces zero graph
nodes and zero container operations, for every kind" is the non-negotiable
regression gate for the feature.

## Shipping: a PR sequence

Ordered so every PR lands green on its own and the first one de-risks the rest
without touching the resolver. Five PRs; the litellm shape is a separate epic.

### PR 1: the external-app entity and the launcher (vertical slice)

The goal is to prove the store, the API, and the grid rendering end to end on the
one kind that touches none of the contract machinery.

- **Store.** `internal/schema/schema.sql` gains an `external_apps` table:
  `{id, kind, source, name, url, icon, values_json}`. `kind` is `launcher` |
  `provider`; `source` is empty for launchers and `app:<catalogID>` |
  `contract:<name>` for providers; `values_json` holds the non-secret per-source
  fields (contract values, not credentials). Credentials never enter this table:
  they land in the secrets manager under an `external/<id>` scope. New
  `internal/store/external_apps.go` plus an interface.
- **Orchestrator.** New intents (`AddExternalAppIntent`,
  `RemoveExternalAppIntent`, `UpdateExternalAppIntent`) in `intent.go`, and an
  applier that persists and notifies. No container work and no consumer resets
yet:
  launchers have no consumers. This keeps invariant 1 (the orchestrator is the
  single writer) intact.
- **API.** New `internal/api/external_apps_module.go`, mounted on the admin
  router: `GET /api/external-apps`, `POST /api/external-apps`,
  `DELETE /api/external-apps/{id}`, `PATCH /api/external-apps/{id}`. Handlers
  submit intents and never write the store directly (mirroring `settings_ai.go`).
- **Dashboard.** `home_module.go` merges launchers into the home payload,
  `grid.ts` turns them into tiles, and `AppTile.svelte` (or a small
  `ExternalTile`) opens `url` in a new tab instead of a routed origin. A minimal
  "add launcher" form (name, URL, icon) in `web/src/routes/settings/` or as a
  modal.
- **Tests.** Store unit tests; API handler tests (intent submitted, store not
  written directly); a `grid.ts` vitest; one e2e assertion that a launcher tile
  renders and opens the URL.
- **Does not touch** the resolver, `ProviderRef`, contracts, or the catalog.

### PR 2: `source: app` and a remote catalog app wired to consumers

The load-bearing slice: an external AFFiNE satisfies `affine-mcp`'s `appApi`
binding with the consumer configurator untouched.

- **Entity.** `source: app(<catalogID>)` now validates against the catalog app
  (exists, non-system, provides at least one contract) and carries the
  per-contract `values_json` (for example `appApi.username`,
  `appApi.workspaceId`).
- **Resolver.** `pkg/configurator` gains `ProviderKindExternalApp`. In
  `orchestrator/integrations.go`, `buildIntegrations` and `bindAppProviders`
  consult the external-app registry alongside the installed set; `providerRef`
  returns the external shape (`App` = catalog ID, `BaseURL` = `url`, no node or
  port); `publishedSecret` and `contractValue` read from the `external/<id>`
  scope.
- **Form.** The API returns the field schema for a chosen catalog app, derived
  from its `provides:` and the contract registry (secrets plus non-optional
  values, with `runtimeValues` becoming operator-supplied). The dashboard form
  renders it.
- **Convergence isolation.** A test pins that an external app produces zero
  graph nodes, containers, routes, and SSO clients: the "one load-bearing
  filter" from the design.
- **End to end.** Integration or e2e: add external AFFiNE, install `affine-mcp`,
  assert the wrapper's saved config carries `AFFINE_BASE_URL=https://affine.example.com`
  and the operator's credentials, with `apps/affine-mcp/configurator.go`
  untouched.

### PR 3: the `ProviderKind` rename (`Instance` to `Setting`)

A pure mechanical rename, split out so PR 4's behavior change stays readable.
`ProviderKindInstance` becomes `ProviderKindSetting`, `instanceSource()` becomes
`settingSource()`, `instanceProviderRef()` becomes `settingProviderRef()`,
`isInstance()` becomes `isSetting()`, plus the comment updates in
`pkg/configurator/interface.go`. The reserved `InstanceProviderSource` string and
the `source: instance` metadata value stay put here; they change in PR 4. No
behavior change; the whole test suite must pass untouched.

### PR 4: `source: contract` and the `ai_upstreams` migration

The magic word dies here.

- **Entity.** `source: contract(<name>)` validates against the contract registry
  (the "natural first set" from axis G; `proxy`, `database`, and `sso` are
  rejected).
- **Metadata.** `source: instance` becomes `source: setting` in
  `apps/hermes/metadata.yaml` and `apps/affine/metadata.yaml`; the loader's
  `validateIntegrations` and `catalog/types.go`
  (`InstanceProviderSource` becomes `SettingProviderSource`) follow, plus the
  developer-graph rendering in `system_module.go`.
- **Resolver.** Generalize the instance path: `source: contract(modelSource)`
  feeds `ModelSourceBinding.Endpoint` exactly as `instanceInferenceSource` does
  today (now `settingSource`), and the reserved `App` value for a setting stops
  being "instance".
- **Migration.** `ai_upstreams` (settings KV) migrates into `external_apps` rows
  of `kind: provider, source: contract(modelSource)`; the API key moves from
  `secrets["ai"]["apiKey"]` to `external/<id>`. The AI settings surface
  (`settings_ai.go`) reads through the new store, so the Settings page keeps
  working while the storage changes under it. This resolves open question 7.
- **Tests.** A migration test; a resolver test that a `source: setting` consumer
  gets the migrated binding; the existing inference tests rewritten against the
  new kind.

### PR 5: developer graph and dashboard polish for external apps

Render external apps in the developer graph (`cli/depgraph.go` plus
`system_module.go`) as a distinct "external" node with no container box, the
same way `source: instance` renders as an "AI Model" node today, generalized to
every external app. Confirm the grid's rename, position, and remove all work for
external records, and that a launcher's tile opens `url` while an app tile opens
the routed origin.

### Follow-on (separate epic): `modelSource` goes multi

The litellm shape from above (`ActiveUpstream` returns all enabled upstreams, a
key per upstream, and an `ollama` app) is orthogonal to external apps and should
be planned on its own. It can land before or after PR 4; if it lands first, PR 4
must handle N `modelSource` external apps rather than one.

### Ordering rationale

PR 1 first because it proves the store, the API, and the grid on the kind with no
resolver surface, so a mistake in the entity model is caught before it becomes
load-bearing. PR 2 next because it is the feature's whole point (a consumer wires
to a remote install) and it introduces `ProviderKindExternalApp` on its own. PR 3
is a rename that keeps PR 4 readable. PR 4 is where the existing `instance` path
is absorbed, and it is sequenced after PR 2 so the resolver's external-app
consultation exists before the instance path is rerouted into it. PR 5 is
presentation and can move earlier if the developer graph is needed sooner.

## Open questions

1. **Store shape**: one JSON-backed store vs a table. Resolved by PR 1: a
   dedicated `external_apps` table, so the entity can grow per-source fields
   and be queried by `kind` without a settings-KV round trip.
2. **Which catalog apps are selectable as an `app` source**: every non-system
   app that provides a contract, or an explicit opt-in marker per app? `provides:`
   is enough to render the form; the marker only adds prose.
3. **Endpoint validation**: reuse `ParsePublicURL`'s strictness or a looser
   "any HTTP or HTTPS origin" check? The public-address parser rejects a lot that is
   legitimate for a LAN peer (an IP literal over http).
4. **Readiness probe**: does an external app get a read-only health probe for
   the dashboard, and is the answer per kind (a launcher wants a HEAD, a
   `modelSource` app already does `/models` discovery)?
5. **Promotion interaction**: `promotedSources` (inference's `SatisfiedBy`)
   scans `installed`; if external providers live outside `installedSet`, the
   promotion scan will not see them. The `modelSource` kind makes this a
   first-class case rather than an edge, so it needs an explicit answer.
6. **Rename/position semantics**: an external app gets a display name and a grid
   position like any installed app; confirm nothing in the grid store assumes a
   routed origin when it opens a tile.
7. **Migration of `ai_upstreams`**: resolved by PR 4: the existing AI Settings
   rows migrate in place into `external_apps` records of `source:
   contract(modelSource)`, and `settings_ai.go` reads through the new store.

## Invariants to respect

- **Invariant 4 (graph sees nodes, not apps)**: an external row is an app with
  zero nodes; the graph must not gain a node for it.
- **Invariant 5 (catalog is disk-driven; bootstrap gate)**: external rows are a
  runtime/operator fact, not a catalog change; the catalog itself is untouched.
- **Invariant 15 (typed contracts, never probe, never read another app's
  files)**: the external binding flows through the same typed contract payloads;
  "is it wired" comes from the store; the operator's secrets live in a scoped
  store and are handed over only through the declared `requires`.
- **Invariant 6 (SSO strategies)**: an external app joins no identity provider;
  the remote instance's auth is its own and must not be provisioned or rewritten
  by Bloud.
