> Status: Phase 1 and the AFFiNE chain shipped; further providers pending

# Plan: MCP as an ordinary catalog capability

> The design lives in [features/mcp.md](../features/mcp.md): the contract
> model, the credential boundary, the reachability story, and the security
> properties. This file is the roadmap. It records the decisions and the order
> they got built in; read the feature doc for how the thing works.

## The model

An MCP server is a **capability an app provides**, not a separate app.

```
affine (provides mcp)  <──  hermes (integrates mcp, optional + multi)
```

- The provider ships its own streamable-HTTP MCP endpoint and declares
  `provides: mcp`. Its configurator mints a scoped credential through the
  app's own API and publishes it.
- A harness declares `mcp` as **optional and multi**, so each harness picks its
  own set. A provider useful to Hermes need not appear in a more specialized
  harness.

Nothing here is a new kind of thing. The provider is an app; the credential is
a published secret under a contract name; the consumer is an integration
consumer.

### What changed from the first draft

The first draft (2026-10-01) assumed an MCP server had to be a separate app
wrapping its target: `affine-mcp` with a required dependency on `affine`. That
was written before the pinned image was inspected. Spiking
`ghcr.io/toeverything/affine:0.27.4` found a first-party MCP server with its
own scoped-credential mechanism, which removed most of the design:

| First draft | Shipped |
|---|---|
| `affine-mcp` wrapper app with its own container | AFFiNE provides `mcp` directly |
| `appApi` contract: the wrapper's credential into the target | Not added. Nothing needs it |
| Bloud generates the wrapper's `httpToken` | AFFiNE mints it; Bloud publishes it |
| `routing.public: false` to keep the wrapper unrouted | Not needed. The provider is a routed app |
| Loopback-only port publish for the wrapper | Not needed. The harness dials `LocalURL` |
| Vaultwarden-style dotenv delivery to the wrapper | Not needed. No second container |
| Wrapper fails the pass when it cannot publish | Provider logs and swallows; the node is the app |

The plumbing that survives is the plumbing the spike did not invalidate: the
`mcp` contract, runtime-published values, the consumer filter, and the loader
rule.

## Decisions

### 1. The provider is the app

A wrapper is the right shape for an app that has no MCP server and no way to
mint a scoped credential. It is the wrong shape for one that ships both: it
duplicates the trust boundary, it has to discover the workspace id over the same
API anyway from outside the app that owns it, and it is one more image to vet
that breaks when the target's API changes.

So `affine` provides `mcp`. The wrapper shape stays available for a future
target that needs it, and nothing in the plumbing depends on the provider being
the app rather than a wrapper around it.

### 2. No `appApi` contract

The first draft needed `appApi` because a wrapper required a credential into
another app. With the provider being the app, the configurator talks to its own
app with the credential it already bootstraps. There is no cross-app credential
in this change.

Adding `appApi` now would be vocabulary ahead of code: a contract with no
provider and no consumer, which is exactly why the previous `mcp` contract was
removed in `27e2b8a`. It comes back the day a wrapper needs it.

### 3. `mcp` returns, with a provider this time, and without a composed URL

```go
{Name: "mcp",
 Secrets: []string{"httpToken"},
 Values:  []ValueSpec{{Key: "path", AbsolutePath: true}, {Key: "serverName"}}}
```

The harness binding is `MCPBinding{ProviderRef, ServerName, Token, Path}`. The
removed struct's `URL` field (`ref.BaseURL + path`) is **not** brought back.
`BaseURL` is a network-scoped container name that a host-networked harness
cannot resolve, so a composed URL would be right for some consumers and
silently wrong for others. The binding carries `BaseURL`, `LocalURL`, and the
path; the harness composes the address its own network namespace can dial.

`mcp` declares no `SatisfiedBy`. The `inference` contract's promotion
mechanism iterates every installed provider of the fallback contract and
ignores the consumer's `compatible` list, which for a tool server means a
harness could be pointed at an arbitrary provider and get a namespace of tools
it never asked for. There is no meaningful fallback for "give me AFFiNE's
documents."

### 4. Runtime-published values

`path` is `/api/workspaces/<workspaceId>/mcp`, and the workspace id is
assigned by AFFiNE in `PostStart`, after the catalog has loaded. It cannot
live in `metadata.yaml`.

`ContractProvides` gains `RuntimeValues []string` beside the static `Values`
map. The loader enforces that a runtime value is a single name, is a key the
contract declares, and is not also declared statically. Resolution prefers the
published value and falls back to static metadata, so a provider can move a
value between channels without touching consumers.

This is a general mechanism, not an MCP special case. Any contract value that
only exists once the app is running uses it.

### 5. The app mints the credential; Bloud publishes it

**Rule: the published `httpToken` is always a credential the provider itself
validates.**

Bloud could generate a random string and publish it. That would be a field
named `token` that authenticates against nothing, and every consumer downstream
would assume a scope it does not have.

Minting through the provider's own API buys revocation (the credential shows
up in AFFiNE's own list, named `bloud`), provider-enforced scope
(`READ_ONLY`, so the harness cannot write even if it wants to), and a real
expiry. `READ_WRITE` is not available on a stable AFFiNE at all: it is
rejected unless the server runs with `env.dev` or a canary channel.

`SetAppContractValue` stores contract-scoped values in
`AppSecrets[app].PublishedValues[contract]`, separate from the flat
`Published` map, so a value scoped to `mcp` cannot be read by a consumer of a
different contract that reuses the key name.

### 6. Provider failures do not fail the node

A wrapper's whole purpose is its MCP endpoint, so a wrapper that cannot publish
a credential should fail the pass and retry. AFFiNE is not a wrapper: the node
is the knowledge base itself, and a node serving its users fine should not land
in ERROR because its MCP credential could not be minted.

The consumer is protected regardless by the empty-token filter, so a broken
credential registers no namespace rather than one that fails forever. What is
given up is the reconciler's own failure signal, which is what the warning
carries.

### 7. Probe before minting

`PostStart` calls `tools/list` with the stored bearer before minting anything:

- **200**: good, mint nothing. A steady-state pass costs one round trip.
- **401/403**: revoked or expired, mint a replacement.
- **transport fault**: keep the stored credential, retry next pass.

The third case is the one that matters. AFFiNE keeps every credential it has
ever issued, and the token is revealed only by the creating call, so a
configurator that minted on every network blip would leave an unbounded pile
of live credentials that nothing cleans up.

### 8. The harness filters on `Installed`, token, and path

`buildIntegrations` binds every compatible provider an optional contract
declares, including providers that are not installed (they arrive with
`Installed: false` so the consumer can prune entries Bloud wrote for them),
and `publishedSecret` returns `""` for both "not required" and "not published
yet."

A harness must therefore skip an entry unless it is installed **and** has a
published token **and** a published path. Writing an entry with an empty bearer
registers a tool namespace that 401s forever.

Hermes manages the namespace, not the section: the key a provider's
`serverName` names is Bloud's to write and to remove, and an operator-added
entry under any other name survives every pass.

### 9. The loader enforces the required-integration default

This is the one place the design's safety property rested on a convention
nothing enforced, and it is fixed regardless of the wrapper change.

`PlanInstall` never sets `CanInstall: false` for an unmet required
integration; it records a `Choice` and the orchestrator fills it from
`choice.Recommended` (the `default: true` entry) via `buildIntegrationConfig`
(`internal/engine/orchestrator/config_builder.go`), installing the provider
first.

But `computeAppDeps` (`internal/engine/orchestrator/pipeline.go`) skips the
`compatible` scan for **required** integrations and reads only the recorded
integration config. So if no `compatible` entry carries `default: true`,
nothing is recorded, **there is no graph edge at all**, and the consumer
installs with no dependency: it resolves an empty credential on every pass and
fails without ever producing a plan-time error.

**Shipped: the catalog loader rejects a `required: true` integration that does
not declare exactly one `default: true` compatible entry.** That puts the
failure at catalog load, where every other declaration error already lands.

### 10. The vestigial env-file pair is deleted

`internal/secrets/manager.go` carried a `writeEnvFiles` / `writeAppEnvFile`
pair that wrote per-app `.env` files into the secrets directory, keyed on a
hardcoded `knownApps` list whose only entry (`miniflux`) is not in the catalog
and which no container spec mounts. Deleted. Left in place it reads like the
intended pattern for shipping credentials into containers, and it is not.

## What shipped

| Piece | Where |
|---|---|
| `mcp` contract | `internal/catalog/contracts.go` |
| `RuntimeValues` on `ContractProvides` | `internal/catalog/types.go` |
| Runtime-value and required-default loader validation | `internal/catalog/loader.go` |
| `PublishedValues` bag, `Set/GetAppContractValue` | `internal/secrets/manager.go` |
| `MCPBinding`, provider interface methods | `pkg/configurator/interface.go` |
| `bindContract` mcp arm, `contractValue` resolution | `internal/engine/orchestrator/orchestrator.go` |
| AFFiNE provider: copilot gate, workspace settle, credential mint, probe | `apps/affine/{metadata.yaml,configurator.go,api.go}` |
| AFFiNE search sidecar (Manticore) so `doc_search` answers | `apps/affine/metadata.yaml` |
| Hermes consumer: `mcp_servers` render, filter, namespace ownership | `apps/hermes/{metadata.yaml,configurator.go}` |
| Shipped-catalog contract pairing tests | `internal/catalog/{mcp_contract_test.go,contract_declarations_test.go}` |

Tests cover: first pass creates and publishes, steady state mints nothing,
revocation is replaced, a failed mint publishes nothing, a CSRF-demanding
server is satisfied, a transport fault mints no spare, the workspace is
sticky, operator MCP servers survive, an uninstalled provider leaves no entry,
idempotency across passes, and rotation lands in the file.

## Next

**Phase 2: multi-workspace endpoints.** Shipped state is one credential against
one workspace, which means the agent sees one workspace and nothing else. The
shape that fixes it is a typed list payload (`ValueSpec{Kind: List}` plus
`SetAppContractList` and a `[]MCPEndpoint` binding), so one provider renders
one harness namespace per workspace. Details and the reachability constraint
are in
[features/mcp.md](../features/mcp.md#workspace-scoping-and-why-one-credential-is-not-enough).

The blocker on full coverage is upstream, not plumbing: AFFiNE's admin GraphQL
surface is unreachable in the shipped build (`isAdminQuery` hardcoded false),
so Bloud can only enumerate workspaces its own account belongs to. Visibility
has to be granted by the operator inviting the Bloud account, which is the
correct security posture regardless.

**Phase 3: a wrapper, if a target needs one.** Only if a target worth
integrating has no MCP server of its own. That is where `appApi` comes back,
along with the Vaultwarden-style dotenv delivery pattern (the configurator
writes a file into the app's own mounted config dir and the image loads it)
and its two selection criteria: the image must read config from a file, and the
`managedfile` mode depends on the reading uid.

**Phase 4: loopback-only port publishing.** Publishing a container port on
`127.0.0.1` instead of `0.0.0.0` is a missing capability worth having for
every app (a live stack shows `0.0.0.0:9001->9000/tcp`). It is not MCP work.
It was in the first draft because the wrapper shape needed it; it stands on its
own merits now.

## Non-goals

- **Per-user MCP authorization.** One published credential means every agent
  acts as one principal. Bloud has per-human SSO identity and no
  per-harness-user MCP authorization. The plan states the boundary rather than
  pretending it away: MCP credentials are instance-level service credentials.
- **stdio transport.** A stdio-only MCP server cannot cross a container
  boundary. Streamable HTTP is the only supported transport.
- **Public MCP exposure beyond what the app already does.** The provider's MCP
  path is as reachable as the app itself, gated by its bearer rather than by
  network position. Deliberate extra lockdown for MCP endpoints specifically
  would be the loopback work above, applied to the whole catalog.
- **An MCP client in the dashboard.** The harness is the client.

## Open questions

1. **Service account or owner account.** The credential is minted by the
   bootstrap owner. A dedicated bot user would be separable in the audit log
   and costs a first-class "service account" concept in the SSO module.
2. **Catalog presentation.** MCP providers are `productivity` or
   `infrastructure` with a tag, or a category of their own. UI-only, but it
   decides whether a catalog of ten providers reads as noise.
3. **Health semantics.** A provider whose MCP credential is broken is not
   unhealthy in a way a container probe can see, and the decision not to fail
   the node makes that deliberate. Whether the dashboard should surface a
   per-contract "published / not published" indicator is a dashboard question.
4. **Workspace policy.** Bloud adopts the first existing workspace rather than
   always creating one, which is the least-surprising default for a single-user
   home instance. Whether an operator should pick which workspace the agent
   reads is a settings question this change deliberately does not answer.
