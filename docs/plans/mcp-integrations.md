> Status: Phase 3 shipped. The providers are `apps/affine-mcp` and
> `apps/immich-mcp`; AFFiNE's built-in MCP server and the Manticore search
> sidecar were removed. Phase 2 and 4 pending.

# Plan: MCP as an ordinary catalog capability

> The design lives in [features/mcp.md](../features/mcp.md): the contract
> model, the credential boundary, the reachability story, and the security
> properties. This file is the roadmap. It records the decisions and the order
> they got built in, including the reversal; read the feature doc for how the
> thing works.

## The model

An MCP server is a **capability an app provides**, not a hardcoded gateway.

```
affine-mcp (provides mcp)  <──  hermes (integrates mcp, optional + multi)
```

- The provider ships a streamable-HTTP MCP endpoint and declares
  `provides: mcp`. Its configurator publishes the bearer its listener accepts.
- A harness declares `mcp` as **optional and multi**, so each harness picks its
  own set.

Nothing here is a new kind of thing. The provider is an app; the credential is a
published secret under a contract name; the consumer is an integration consumer.

### What changed from the first draft

The first draft (2026-10-01) assumed the MCP server had to be a separate app
wrapping its target: `affine-mcp` with a required dependency on `affine`. Spiking
`ghcr.io/toeverything/affine:0.27.4` found a first-party MCP server, so the first
shipping version used it instead and the wrapper was shelved.

That version worked, and it was narrower than the target: AFFiNE's own endpoint
is `READ_ONLY` and workspace-scoped. So the wrapper came back, the built-in
provider was removed, and the catalog now ships the shape the first draft
sketched.

| First draft | First shipped provider | Final (current) |
|---|---|---|
| `affine-mcp` wrapper app with its own container | AFFiNE provides `mcp` directly | `affine-mcp` provides `mcp` |
| `appApi` contract for the wrapper's credential | Not added | `appApi` ships, carrying owner username, password, workspace id |
| Bloud generates the wrapper's `httpToken` | AFFiNE mints it, Bloud publishes it | Bloud generates it and writes it into the wrapper's config |
| Vaultwarden-style file delivery to the wrapper | Not needed | The wrapper reads its saved config file |
| Wrapper fails the pass when it cannot publish | Provider logs and swallows | Wrapper fails its `/readyz` pass; AFFiNE swallows workspace settlement |
| Manticore sidecar for `doc_search` | Ships, 713 MB | Removed with the built-in provider |

The plumbing the spike did not invalidate survives unchanged: the `mcp`
contract, runtime-published values, the consumer filter, and the loader rule.

## Decisions

### 1. The provider is the app, when the app's server is enough

The original rule: a wrapper is the right shape for an app that has no MCP
server, and the wrong shape for one that ships both, because it duplicates the
trust boundary and has to discover the workspace id from outside the app that
owns it.

AFFiNE has both, so `affine` provided `mcp`. The rule was correct as stated and
insufficient as applied: it asked whether the app has a server, not whether the
server is good enough. AFFiNE's is read-only, so a wrapper was worth the second
container after all. See decision 11.

### 2. No `appApi` contract (superseded)

The first draft needed `appApi` because a wrapper required a credential into
another app. With the provider being the app, the configurator talked to its own
app with the credential it already bootstraps.

Adding `appApi` then would have been vocabulary ahead of code: a contract with
no provider and no consumer, which is exactly why the previous `mcp` contract was
removed in `27e2b8a`. It came back the day a wrapper needed it, which was one
release later.

### 3. `mcp` returns, without a composed URL

```go
{Name: "mcp",
 Secrets: []string{"httpToken"},
 Values:  []ValueSpec{{Key: "path", AbsolutePath: true}, {Key: "serverName"}}}
```

The harness binding is `MCPBinding{ProviderRef, ServerName, Token, Path}`. The
removed struct's `URL` field (`ref.BaseURL + path`) is **not** brought back.
`BaseURL` is a network-scoped container name that a host-networked harness cannot
resolve, so a composed URL would be right for some consumers and silently wrong
for others. The binding carries `BaseURL`, `LocalURL`, and the path; the harness
composes the address its own network namespace can dial.

`mcp` declares no `SatisfiedBy`. The `inference` contract's promotion mechanism
iterates every installed provider of the fallback contract and ignores the
consumer's `compatible` list, which for a tool server means a harness could be
pointed at an arbitrary provider and get a namespace of tools it never asked for.
There is no meaningful fallback for "give me AFFiNE's documents."

### 4. Runtime-published values

`ContractProvides` gained `RuntimeValues []string` beside the static `Values`
map. The loader enforces that a runtime value is a single name, is a key the
contract declares, and is not also declared statically. Resolution prefers the
published value and falls back to static metadata, so a provider can move a value
between channels without touching consumers.

The motivating case was the built-in provider's workspace-scoped `/path`, which
AFFiNE assigned in `PostStart`. That provider is gone, and the mechanism is not:
`appApi` publishes `username` and `workspaceId` at runtime, because only AFFiNE
can produce them. The shipped `mcp` provider uses static values.

### 5. Two credentials, and who generates each

**Rule: the published `httpToken` is always a credential the provider itself
validates.**

For `affine-mcp` the provider is a container Bloud configures, so Bloud
generates the shared secret, writes it into the wrapper's config, and publishes
the same value under `mcp.httpToken`. The listener validates what it was told,
which is what makes the rule hold.

The outbound credential is separate, and is where the design is weakest. The
wrapper needs AFFiNE's GraphQL API, which accepts a session cookie or a
15-minute session JWT and nothing durable. AFFiNE's only scoped credential,
`aff_mcp_v1.*`, is validated on its own MCP endpoint and nowhere else. So `appApi`
publishes the bootstrap owner's password, and the wrapper carries no scoped,
provider-revocable credential into AFFiNE. That is the open question in
`features/mcp.md`, not a hidden property.

### 6. Provider failures and boundary failures

A wrapper's whole purpose is its MCP endpoint, so `affine-mcp` fails its pass
when `/readyz` cannot reach AFFiNE, and the self-healing pass retries it.

AFFiNE is not the provider anymore, but it still owns the shared workspace and
the appApi values. Its configurator logs and swallows a failure to settle the
workspace: the node is the knowledge base, and a knowledge base serving its users
must not land in ERROR because a companion-facing value could not be published.
The companion is protected by the empty-token and empty-scope filters.

### 7. Probe before minting (retired with the built-in provider)

The built-in provider's `PostStart` called `tools/list` with the stored bearer
before minting: 200 meant good, 401/403 meant replace, a transport fault meant
keep. That mattered because AFFiNE keeps every credential it has ever issued and
reveals the token only at creation, so minting per blip left an unbounded pile.

The wrapper's bearer is read back from the secrets store and never regenerated,
so the same rule holds more simply: `ensureHTTPToken` returns the stored value
when one exists.

### 8. The harness filters on `Installed`, token, and path

`buildIntegrations` binds every compatible provider an optional contract
declares, including providers that are not installed (they arrive with
`Installed: false` so the consumer can prune entries Bloud wrote for them), and
`publishedSecret` returns `""` for both "not required" and "not published yet."

A harness must therefore skip an entry unless it is installed **and** has a
published token **and** a published path. Writing an entry with an empty bearer
registers a tool namespace that 401s forever.

Hermes manages the namespace, not the section: the key a provider's `serverName`
names is Bloud's to write and to remove, and an operator-added entry under any
other name survives every pass.

### 9. The loader enforces the required-integration default

This is the one place the design's safety property rested on a convention nothing
enforced, and it is fixed regardless of the provider.

`PlanInstall` never sets `CanInstall: false` for an unmet required integration; it
records a `Choice` and the orchestrator fills it from `choice.Recommended` (the
`default: true` entry) via `buildIntegrationConfig`
(`internal/engine/orchestrator/config_builder.go`), installing the provider first.

But `computeAppDeps` (`internal/engine/orchestrator/pipeline.go`) skips the
`compatible` scan for **required** integrations and reads only the recorded
integration config. So if no `compatible` entry carries `default: true`, nothing
is recorded, **there is no graph edge at all**, and the consumer installs with no
dependency: it resolves an empty credential on every pass and fails without ever
producing a plan-time error.

**Shipped: the catalog loader rejects a `required: true` integration that does not
declare exactly one `default: true` compatible entry.** That puts the failure at
catalog load, where every other declaration error already lands. `affine-mcp`'s
`appApi` integration is the live user of this rule: it is what orders AFFiNE
before the wrapper.

### 10. The vestigial env-file pair is deleted

`internal/secrets/manager.go` carried a `writeEnvFiles` / `writeAppEnvFile` pair
that wrote per-app `.env` files into the secrets directory, keyed on a hardcoded
`knownApps` list whose only entry (`miniflux`) is not in the catalog and which no
container spec mounts. Deleted. Left in place it reads like the intended pattern
for shipping credentials into containers, and it is not.

### 11. The built-in provider was removed

AFFiNE's own MCP server is read-only, workspace-scoped, and, on a stable release,
gated so that write tools are unavailable at all. The wrapper exposes 106
read-write tools over the same workspace. Shipping both meant two namespaces for
one target, one of them a subset of the other, so the built-in provider was
removed from `apps/affine`.

Three things had to move with it:

- **The shared workspace does not.** AFFiNE's configurator still signs in,
  settles the one shared workspace, and publishes it. What left was the credential
  minting, not the workspace. `selectWorkspace` now uses `appApi.workspaceId` for
  stickiness where it used the published MCP path.
- **The Manticore sidecar does.** It existed only so the built-in `doc_search`
  would answer. The wrapper's `search_docs` is a title search over workspace
  metadata and needs no indexer. Removing it drops a 713 MB image and a container;
  AFFiNE's in-app AI retrieval degrades, which is recorded in the feature doc.
- **The `aff_mcp_v1` minting path does.** `createMcpCredential`, `probeMCP`, the
  `copilot.enabled` MCP gate, and their tests left the app. `copilot.enabled`
  itself stays: it also opens the BYOK AI surface the inference wiring uses.

The lesson is recorded in decision 1: "does the app have its own MCP server" is
the wrong question. "Is the app's server sufficient" is the right one.

## What shipped

| Piece | Where |
|---|---|
| `mcp` contract | `internal/catalog/contracts.go` |
| `RuntimeValues` on `ContractProvides` | `internal/catalog/types.go` |
| Runtime-value and required-default loader validation | `internal/catalog/loader.go` |
| `PublishedValues` bag, `Set/GetAppContractValue` | `internal/secrets/manager.go` |
| `MCPBinding`, provider interface methods | `pkg/configurator/interface.go` |
| `bindContract` mcp arm, `contractValue` resolution | `internal/engine/orchestrator/orchestrator.go` |
| `appApi` contract, `AppAPIBinding`, resolver arm | `internal/catalog/contracts.go`, `pkg/configurator/interface.go`, `internal/engine/orchestrator/integrations.go` |
| AFFiNE: owner credential + shared workspace under `appApi` | `apps/affine/{metadata.yaml,configurator.go}` |
| `affine-mcp` wrapper: generated MCP bearer, config file, readiness probe | `apps/affine-mcp/` |
| `appToken` contract, `AppTokenBinding`, resolver arm | `internal/catalog/contracts.go`, `pkg/configurator/interface.go`, `internal/engine/orchestrator/integrations.go` |
| Immich: minted, revocable API key under `appToken` | `apps/immich/{metadata.yaml,configurator.go,api.go}` |
| `immich-mcp` wrapper: authenticated Caddy edge over an unauthenticated tool server | `apps/immich-mcp/` |
| Hermes consumer: `mcp_servers` render, filter, namespace ownership | `apps/hermes/{metadata.yaml,configurator.go}` |
| Shipped-catalog contract pairing tests | `internal/catalog/{mcp_contract_test.go,contract_declarations_test.go}` |
| Removed: AFFiNE built-in provider, Manticore sidecar, `aff_mcp_v1` minting | `apps/affine/` |

Tests cover: workspace settle creates and publishes, steady state creates
nothing, the published workspace is sticky, a sign-in fault publishes nothing, a
CSRF-demanding server is satisfied, the wrapper writes the config and persists
its bearer without rotating it, an incomplete binding writes no credential, an
uninstalled provider leaves no entry, and operator MCP servers survive.

## Next

**Phase 2: multi-workspace endpoints.** The wrapper already reaches every
workspace the owner can see through one endpoint, and the agent's default is
pinned to the shared one. Registering one harness namespace per workspace is a
different shape: a typed list payload (`ValueSpec{Kind: List}` plus
`SetAppContractList` and a `[]MCPEndpoint` binding). It is not built because the
single-endpoint model covers the home-server case.

**Follow-up: dropping a container from metadata does not reap the running one.**
The Manticore sidecar was removed from `apps/affine`'s metadata, but the
orchestrator reconciles only the containers an app currently declares
(`SyncContainerState` -> `inspectContainers` walks the catalog defs); there is no
label-scoped sweep for a container that used to be declared. An existing install
keeps running the orphan until it is removed by hand or the app is reinstalled,
which is what the live verification had to do. A proper fix is an
`io.bloud.app=<name>` orphan sweep in the reconcile path, general app-lifecycle
work rather than MCP work.

**Phase 4: loopback-only port publishing.** Publishing a container port on
`127.0.0.1` instead of `0.0.0.0` is a missing capability worth having for every
app (a live stack shows `0.0.0.0:9222->9222/tcp`). It is not MCP work; it is a
network-exposure reduction for every app.

## Non-goals

- **Per-user MCP authorization.** One published credential means every agent acts
  as one principal. Bloud has per-human SSO identity and no per-harness-user MCP
  authorization. The plan states the boundary rather than pretending it away: MCP
  credentials are instance-level service credentials.
- **stdio transport.** A stdio-only MCP server cannot cross a container boundary.
  Streamable HTTP is the only supported transport.
- **Public MCP exposure beyond what the app already does.** The provider's MCP
  path is as reachable as the app itself, gated by its bearer rather than by
  network position. Deliberate extra lockdown for MCP endpoints specifically
  would be the loopback work above, applied to the whole catalog.
- **An MCP client in the dashboard.** The harness is the client.

## Open questions

1. **Service account or owner account.** `appApi` publishes the bootstrap owner's
   password to `affine-mcp`, so the wrapper reaches AFFiNE with the owner's
   authority and the tool profile is the only boundary. A dedicated bot user would
   be separable in the audit log and could not administer the instance. AFFiNE
   exposes no user-creation or token API for it, so it is blocked upstream.
2. **Catalog presentation.** MCP providers are `productivity` or
   `infrastructure` with a tag, or a category of their own. UI-only, but it
   decides whether a catalog of ten providers reads as noise.
3. **Health semantics.** The wrapper's `/readyz` probe covers "AFFiNE reachable",
   not "the agent's calls succeed". Whether the dashboard should surface a
   per-contract "published / not published" indicator is a dashboard question.
4. **Workspace policy.** Bloud pins the agent to the shared workspace it owns,
   because an unpinned agent chooses per call and can choose wrong. Whether an
   operator should be able to point it elsewhere is a settings question this
   change deliberately does not answer.
