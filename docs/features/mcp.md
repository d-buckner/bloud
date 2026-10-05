# Bloud MCP

**Status:** Shipped. Two providers today: `apps/affine-mcp` and
`apps/caldav-mcp`, both consumed by Hermes. AFFiNE's own MCP server is
deliberately not exposed.
**Last updated:** 2026-10-11
**Roadmap:** [plans/mcp-integrations.md](../plans/mcp-integrations.md)

---

## Overview

An MCP server in Bloud is **an app that ships a streamable-HTTP MCP endpoint and
provides the `mcp` contract**. A harness consumes that contract and registers
each provider as a tool namespace.

```
affine-mcp  (provides mcp)  <──┐
                               ├──  hermes (integrates mcp, optional + multi)
caldav-mcp  (provides mcp)  <──┘
```

`multi: true` is what makes that a fan-in rather than a choice. The resolver
binds every compatible provider an optional contract declares, and Hermes
writes one `mcp_servers` entry per provider under that provider's
`serverName`. Installing a new MCP-capable app gives the agent a new namespace
without any change to Hermes, and uninstalling one removes its entry on the
next pass instead of leaving a tool the agent keeps trying to call.

AFFiNE ships its own MCP server, and the first version of this design used it:
`apps/affine` minted a scoped `aff_mcp_v1.<credentialId>.<secret>` bearer through
AFFiNE's GraphQL API and published it. That server is **read-only and
workspace-scoped** (`doc_search`, `read_document`; the write tools are gated
behind `env.dev` or a canary channel even in the current upstream), so it cannot
author documents or touch databases. It was removed from the catalog. The
provider is now `apps/affine-mcp`, the third-party
[affine-mcp-server](https://github.com/DAWNCR0W/affine-mcp-server), which speaks
AFFiNE's GraphQL and WebSocket APIs directly and exposes a far wider read-write
surface.

Nothing in the chain is a new kind of thing. The provider is an ordinary catalog
app; its inbound bearer is an ordinary published secret under a contract name;
the consumer is an ordinary `multi` integration consumer. The only thing that
changed from the built-in shape is where the credential comes from, and that is
forced by the protocol: see [The `appApi` contract](#the-appapi-contract).

### Why the provider is a wrapper

The design's original rule was "the provider is the app": a target with its own
MCP server should not need a separate container wrapping it. That rule is right
when the app's server is sufficient. It is wrong when the app's server is
weaker than a third party's, which is exactly AFFiNE's case.

The wrapper shape costs a second container to vet and a boundary that can drift
from the target's API. It buys the tool surface the built-in server does not
have. Those trade-offs are recorded here rather than hidden:
[plans/mcp-integrations.md](../plans/mcp-integrations.md) carries the decision
history, including the point where the built-in provider was removed.

### What removing the built-in server also removed

The Manticore Search sidecar existed only to light the lexical channel of
AFFiNE's `doc_search` for the built-in MCP endpoint. With that endpoint gone the
sidecar (713 MB) and the `AFFINE_INDEXER_*` wiring were removed too. AFFiNE's
own in-app AI retrieval degrades as a result (`SEARCH_UNAVAILABLE` until an
embedding provider is configured); the wrapper's `search_docs` is a title search
over workspace metadata and is unaffected.

---

## Core Principles

- **Real keys only.** The published bearer is a credential the provider app
  validates. The wrapper validates the bearer Bloud writes into its config; a
  string nothing validated would be a token named `token` that authenticates
  against nothing.
- **The credential shape is the provider's private concern.** The consumer
  receives one opaque string. Whether the provider minted it, adopted it from a
  config file, or was handed it at bootstrap is invisible across the boundary.
- **A provider declares what it fills at runtime.** Some contract values are
  static metadata; some only exist once the app is running. Both are declared at
  catalog load, and the loader checks that every value the contract declares is
  covered by one channel or the other.
- **Harnesses choose their own tool sets.** `multi: true` on the consumer side
  means Hermes takes every MCP provider the instance offers while a specialized
  harness takes two.
- **One published credential means one principal.** No per-user MCP
  authorization. A stated limit, not an oversight.

---

## The `mcp` Contract

```go
// services/host-agent/internal/catalog/contracts.go
{
    Name:    "mcp",
    Secrets: []string{"httpToken"},
    Values: []ValueSpec{
        {Key: "path", AbsolutePath: true},
        {Key: "serverName"},
    },
}
```

- `httpToken`: the bearer the provider's MCP endpoint accepts.
- `serverName`: the tool namespace the harness registers under.
- `path`: the endpoint path, absolute on the provider's address.

The provider side:

```yaml
# apps/affine-mcp/metadata.yaml
provides:
  mcp:
    secrets: [httpToken]
    values:
      serverName: affine-mcp
      path: /mcp
```

The consumer side:

```yaml
# apps/hermes/metadata.yaml
integrations:
  mcp:
    required: false
    multi: true
    requires: [httpToken]
    compatible:
      - app: affine-mcp
      - app: caldav-mcp
```

`requires: [httpToken]` is the whole consumer surface. Declaring a contract gets
the provider's address and the named secrets, never a credential that was not
asked for.

### `runtimeValues`: values the provider cannot know at load time

`ContractProvides` carries `RuntimeValues []string` alongside the static
`Values map[string]string`. The loader enforces three things:

1. A runtime value must be a single name, not a dotted path or a template.
2. It must be a key the contract actually declares.
3. It must **not** also appear in the static `Values` map. Declaring the same key
   twice means two sources of truth for one field, and the resolution order would
   silently prefer one.

Resolution prefers the published runtime value and falls back to the static
metadata (`contractValue` in `internal/engine/orchestrator/orchestrator.go`). A
provider can move a value from runtime to static, or the reverse, without
touching consumers.

The shipped `mcp` provider uses both static values (`/mcp`, `affine-mcp`),
because the wrapper serves one endpoint for every workspace. The mechanism is
still load-bearing: `appApi` publishes `username` and `workspaceId` at runtime,
because those are facts only AFFiNE can produce. The built-in MCP server used it
for a workspace-scoped `path`, which is the shape this section was written for
and remains available to a future provider.

### Why there is no composed URL in the binding

```go
// services/host-agent/pkg/configurator/interface.go
type MCPBinding struct {
    ProviderRef // App, Installed, Node, Port, BaseURL, LocalURL
    ServerName string
    Token      string
    Path       string
}
```

An earlier version of this struct carried `URL: ref.BaseURL + path`. That field
is gone on purpose. `BaseURL` is `http://<Node>:<Port>`, a name podman's
network-scoped DNS serves only to containers attached to that network. A
host-networked harness cannot resolve it. A composed URL would be right for some
consumers and silently wrong for others, so the binding hands over both addresses
plus the path and the consumer composes the one its own topology can dial.

### Why `mcp` has no `SatisfiedBy`

The `inference` contract declares `SatisfiedBy: [modelSource]` so a consumer keeps
working when no gateway is installed. That promotion is implemented by
`promotedSources` (`internal/engine/orchestrator/inference.go`), which iterates
**every installed provider of the fallback contract** and ignores the consumer's
`compatible` list.

For a tool server that is the wrong failure mode. A harness pointed at an
arbitrary installed MCP provider gets a namespace full of tools it never asked
for. There is no meaningful fallback for "give me AFFiNE's documents," so `mcp`
opts out of promotion.

---

## The `appApi` contract

A wrapper is a companion that is not a browser: it cannot follow an SSO redirect
into the target app. It needs a real account credential into the target's API,
and that is what `appApi` carries.

This is distinct from a target's own scoped-credential mechanism, which a
provider that ships a first-party endpoint would publish through `mcp`. AFFiNE
has one, and the difference is the protocol: its `aff_mcp_v1.*` credential is
validated only on `POST /api/workspaces/<id>/mcp`, and its GraphQL API takes a
session cookie or a short-lived session JWT. A GraphQL-speaking wrapper therefore
has no AFFiNE-issued token to use, on the pinned 0.27.4 or on current canary; an
account credential is the only durable thing that works.

```go
// internal/catalog/contracts.go
{
    Name:    "appApi",
    Secrets: []string{"password"},
    Values:  []ValueSpec{{Key: "username"}, {Key: "workspaceId"}},
}
```

The username and the workspace id are values rather than secrets because the
target's own UI shows both; the password is the secret. The workspace id is the
scope the provider provisions and wants a companion to address by default, which
is what lets a companion default every call to the workspace Bloud owns instead
of discovering and choosing one. A provider with no such scope never publishes
it. A consumer that declares the contract without `requires: [password]` gets
both values and an empty password, the same least-privilege rule every contract
follows.

The password is the target's real account credential, published by the provider,
not a Bloud invention:

```go
c.secrets.SetAppSecret("affine", "password", ownerPassword)
c.secrets.SetAppContractValue("affine", "appApi", "username", ownerEmail)
c.secrets.SetAppContractValue("affine", "appApi", "workspaceId", sharedWorkspaceID)
```

---

## The Credential Boundary

The rule that governs every provider:

**The published `httpToken` is always a credential the provider itself validates.**

For `affine-mcp` that means Bloud generates the shared secret and writes it into
both places at once: the wrapper's config file (`AFFINE_MCP_HTTP_TOKEN`) and the
binding under `provides.mcp.secrets.httpToken`. The wrapper then requires that
bearer on every `/mcp` request, so the credential is validated by the thing that
was told it. Persisting it in the secrets store is what keeps a harness's
registered namespace valid across restarts.

```go
c.secrets.SetAppSecret(appName, "httpToken", token)
```

`SetAppContractValue` writes into `AppSecrets[app].PublishedValues[contract]`
(`internal/secrets/manager.go`), a per-contract bag kept separate from the flat
`Published` map so a value scoped to one contract cannot be read by a consumer of
a different contract that happens to use the same key name.

### What the built-in server did, and why it is not the model here

AFFiNE's built-in MCP endpoint, now unused, took the opposite route: the
configurator asked AFFiNE to mint a `READ_ONLY`, workspace-scoped credential that
expires and is revocable in AFFiNE's UI. That model is strictly better where it
applies: revocation is provider-enforced and the operator can see the credential.
It does not apply to the wrapper, because the wrapper's inbound listener takes a
static shared secret and its outbound credential into AFFiNE is an account
password that AFFiNE offers no token alternative for.

### The empty-token rule, and where it applies

`publishedSecret` (`internal/engine/orchestrator/orchestrator.go`) returns `""`
both when the consumer did not require the secret and when the provider has not
published it. Those two empties are conflated by design.

**The consumer must treat an empty token as not-ready and write nothing.** This
matters more on the consumer side than the provider side, because
`buildIntegrations` binds every compatible provider an optional contract
declares, including providers that are not installed at all (they arrive with
`Installed: false` so the consumer can prune entries Bloud wrote for them). The
harness filter is therefore three conditions:

```go
if !b.Installed || b.Token == "" || b.Path == "" {
    // not there yet, or not published yet: remove any entry Bloud wrote
}
```

Writing an entry with an empty bearer registers a tool namespace that 401s
forever.

### Provider failures fail the wrapper's pass

A wrapper's whole purpose is its MCP endpoint, so a wrapper that cannot reach its
target should fail the pass and retry. `affine-mcp` does: its `PostStart` probes
`/readyz`, which checks the configured AFFiNE GraphQL endpoint, and returns the
error on failure so the self-healing pass retries the node.

AFFiNE itself is not a provider of anything the harness reads anymore, so its
configurator continues to log and swallow failures to settle the shared
workspace: the node is the knowledge base, and a knowledge base that serves its
users fine must not land in ERROR because a companion-facing value could not be
published. The companion is protected anyway by the empty-token and
empty-scope filters.

---

## Reachability

The provider is an ordinary app with an ordinary published port.

The harness dials `LocalURL + Path`. `LocalURL` is `http://localhost:<port>`,
which is the address Traefik itself uses to reach every app, and the address a
host-networked harness can resolve. `BaseURL` (`http://apps-affine-mcp:9222`) is
the network-scoped name that a host-networked consumer cannot resolve, which is
why the binding carries both and the consumer picks. Hermes runs in the host
network namespace, so it uses `LocalURL`.

The wrapper's `/mcp` endpoint is reachable wherever the app is reachable. It is
not a secret URL. What gates it is the bearer Bloud generates, which is a
long-lived instance credential the operator can rotate by reinstalling or by
clearing `httpToken` from the secrets store. The catalog's deferred
loopback-only port publish would tighten this for every app; it is not MCP work.

---

## Data Flow

### Install

1. Operator installs Hermes. The `mcp` integration is optional and multi, so
   `PlanInstall` auto-configures every compatible provider it can.
2. Installing `affine-mcp` requires AFFiNE: the `appApi` integration is required
   and single-provider, so `computeAppDeps`
   (`internal/engine/orchestrator/pipeline.go`) turns it into a graph edge and
   AFFiNE converges first.
3. AFFiNE's `PreStart` writes `config.json` (public URL, OIDC, the BYOK policy).
4. AFFiNE's `PostStart` signs in as the bootstrap owner, settles the shared
   workspace, and publishes `appApi.username`, `appApi.password`, and
   `appApi.workspaceId`.
5. `affine-mcp`'s `PreStart` reads the resolved `appApi` binding, generates and
   publishes its own `httpToken`, and writes the wrapper's config file
   (`AFFINE_BASE_URL`, `AFFINE_EMAIL`, `AFFINE_PASSWORD`, `AFFINE_WORKSPACE_ID`,
   `AFFINE_MCP_HTTP_TOKEN`).
6. The wrapper starts, `PostStart` probes `/readyz`, and the node reaches
   `RUNNING`.
7. Hermes' `PreStart` reads `Integrations.MCPServers`, filters, and renders
   `mcp_servers.affine-mcp` into its own `config.yaml`.
8. Hermes starts with the namespace registered.

Steps 4 and 5 before step 7 are the graph edges. Without them Hermes would
render nothing and pick the namespace up on a later pass.

### Reconcile

Every pass re-runs both config phases for every node that is already `RUNNING`:
the config resync (`levels.go:readyForConfigResync` → `execution.go:runResync`).
The container is recreated only when `PreStart` reports `RestartNeeded`, so a
steady-state pass reads, changes nothing, and disturbs nothing.

A steady-state `affine-mcp` pass re-reads the `appApi` binding, rewrites the
config file only if its bytes changed, and probes `/readyz`. The bearer is read
back, never regenerated, so a restart does not invalidate the namespace.

That the resync includes `PreStart` is what makes install order stop mattering.
A provider that appears after its consumer resolves a new binding on the next
pass, and the consumer's `PreStart` is the only thing that can write that
binding into the file its app reads at boot. While the resync ran `PostStart`
only, a node at `RUNNING` never re-ran `PreStart`, so Hermes kept serving the
`mcp_servers` map written before the provider existed and a namespace added
later appeared only after a Hermes restart.

The resync can therefore restart a container, which is what the resync breaker
caps: two consecutive resync-triggered restarts per node, then refusal with a
WARN and an entry on the developer status snapshot
(`OrchestratorStatus.ResyncBreakers`). The loop it stops is an app that
rewrites the file Bloud manages while it runs, which no offline test can see.
A pass that converges, or a full lifecycle drive, clears the accounting.

The self-healing pass (`selfheal.go`) submits a `ReconcileIntent` on an idle
timer; the pass it triggers retries `ERROR` nodes and resyncs every healthy one.

The workspace is sticky: the workspace AFFiNE already published wins as long as
it still exists, so a workspace the operator creates later cannot silently move
the companion's scope. Failing that, the first existing workspace is adopted
rather than a second copy being created, and only an instance with no workspaces
at all gets one made for it.

### Revocation

The wrapper's bearer is Bloud's, not AFFiNE's. Clearing `httpToken` for
`affine-mcp` in the secrets store, or reinstalling the app, rotates it; the next
pass generates a new one, writes it into the config, and republishes it. An
operator who wants to cut an agent off revokes it there.

### Uninstall the provider

The binding arrives with `Installed: false`. The consumer's filter removes
`mcp_servers.affine-mcp` and leaves any operator-added server untouched. If the
section is empty it is deleted rather than left as an empty map.

### Uninstall the consumer

Nothing special: Hermes' config file goes with it.

---

## Workspace scoping

The wrapper is **not** workspace-scoped in its endpoint: one `/mcp` serves every
workspace the owner credential can see, and a tool call can address any of them.
That is the opposite of the built-in endpoint, which was scoped by the workspace
id in its path and its bearer.

Bloud still has one shared workspace, and the agent must default to it. Every
workspace-scoped tool in the wrapper resolves `args.workspaceId ||
AFFINE_WORKSPACE_ID`, so Bloud pins `AFFINE_WORKSPACE_ID` to the id AFFiNE
publishes as `appApi.workspaceId`. Without the pin an agent has to call
`list_workspaces` and choose on every call, and can write to the wrong
workspace.

The id is settled from the same workspace that the BYOK AI profile is registered
against, so the AI wiring and the agent's default cannot disagree about which
workspace Bloud owns. A provider that published no scope leaves the pin unset,
which is the wrapper's documented "the caller supplies the id" mode.

### One endpoint instead of one credential per workspace

The narrower built-in model could mint a credential per workspace and let a
harness register one namespace per workspace. That is a follow-up worth having
for a multi-workspace operator, and it needs a typed list payload
(`ValueSpec{Kind: List}` plus `SetAppContractList` and a `[]MCPEndpoint`
binding). It is deliberately not built: the wrapper already reaches every
workspace with one credential, and the add-then-publish shape of the built-in
provider is what the list payload was designed for.

---

## Harness Configuration

Hermes reads `mcp_servers` from its own `config.yaml`:

```yaml
mcp_servers:
  affine-mcp:
    url: http://localhost:9222/mcp
    headers:
      Authorization: Bearer <generated>
  caldav-mcp:
    url: http://localhost:9333/mcp
    headers:
      Authorization: Bearer <generated>
```

Bloud manages the namespace, not the section. The key a provider's `serverName`
names is Bloud's to write and to remove, because that is the app the integration
contract points at. An entry the operator added by hand under any other name
survives every pass, the same discipline `applyInference` follows with a
hand-chosen model.

The whole map is re-rendered each pass, so a removed provider leaves no entry
behind rather than lingering as a stale namespace.

---

## Security Properties

- **The bearer is validated by the provider.** Bloud generates it and writes it
  into the wrapper's own config, so the listener rejects anything else.
- **The credential into AFFiNE is the owner account.** A documented limitation,
  not a design goal. AFFiNE offers no token or service account for its GraphQL
  surface, so revoking the wrapper means clearing its config or the account's
  password, not a scoped token in AFFiNE's UI.
- **Contract-scoped secrets.** A harness declaring `mcp` receives only the
  secrets listed under `requires`. Declaring a contract never grants more than
  was asked for.
- **The tool surface is the boundary, not the credential.**
  `AFFINE_TOOL_PROFILE` defaults to `full` (all 106 tools, including
  `delete_workspace` and `delete_doc`, which require an exact-match confirmation
  argument). An operator who wants less sets `read_only`, `core`, or `authoring`.

### What these properties do not cover

**One published credential means every agent using that provider acts as one
principal.** Bloud has per-human SSO identity and no per-harness-user MCP
authorization. A tool call through the AFFiNE namespace is attributable to "the
Bloud MCP integration," not to the human who asked for it.

**The wrapper reaches AFFiNE with the owner's authority.** It can read and write
everything the owner can, and the owner is the server admin. The `full` tool
profile includes destructive operations. This is the open question the roadmap
records: a dedicated least-privilege AFFiNE account would fix it and needs a
user-creation path AFFiNE does not expose today.

**The bearer is stored in Bloud's secrets file.** `secrets.json` holds it.
Anyone with the Bloud host's filesystem access holds it, which is the same
exposure as every other published credential.

**The endpoint is as reachable as the app.** Under a public domain, `/mcp` is
reachable from wherever the app is reachable. The bearer is the gate, not the
address.

---

## Non-Goals

- **stdio transport.** A stdio-only MCP server cannot cross a container
  boundary. Streamable HTTP is the only supported transport.
- **A Bloud-hosted MCP gateway.** A gateway that hosts many upstreams collapses
  the per-app provider edge, which is the part that makes ordering and
  uninstall-blocking work. Per-app providers are the design.
- **An MCP client in the dashboard.** The harness is the client. Bloud hosts
  servers and wires them; it does not consume them.
- **Per-user MCP authorization.** See the security section: one credential, one
  principal.

---

## Adding an MCP Provider

1. **Decide whether the app's own server is sufficient.** If it is, the provider
   is the app: declare `provides.mcp` and publish its own scoped credential. If
   it is not, the provider is a wrapper, and the wrapper consumes the `appApi`
   contract (or a future sibling) to reach the target.
2. **Declare the contract.**

   ```yaml
   provides:
     mcp:
       secrets: [httpToken]
       values:
         serverName: <app>
       # runtimeValues: [path]  # only when the app mints the path at runtime
   ```

3. **Publish the credential in `PreStart` or `PostStart`.** A provider whose
   listener takes a static secret publishes a generated one (`SetAppSecret`); a
   provider whose own API results in it, or whose target mints it, publishes that
   instead. A runtime value goes through `SetAppContractValue`.
4. **Make it idempotent.** Regenerate only when the stored credential is absent.
   A configurator that generates per pass invalidates every registered namespace.
5. **Fail the node when the provider is the thing being wired.** The wrapper's
   whole purpose is its endpoint; a wrapper that cannot reach its target should
   retry. Log and swallow only when the node's core value to users is independent
   of the contract.
6. **Test it.** `apps/affine-mcp/configurator_test.go` is the wrapper pattern
   (config written, bearer persisted and not rotated, incomplete binding writes
   no credential, readiness probe). `apps/affine/workspace_test.go` is the
   provider-that-owns-the-scope pattern.

---

## Verified Facts (AFFiNE 0.27.4)

Measured against `ghcr.io/toeverything/affine:0.27.4`, the pinned image, not read
from documentation. These are the facts the design rests on. The built-in MCP
endpoint is listed because its shape is why the wrapper exists, not because Bloud
uses it.

| Fact | Value |
|---|---|
| Built-in endpoint (unused) | `POST /api/workspaces/{workspaceId}/mcp` |
| Built-in auth (unused) | `Authorization: Bearer aff_mcp_v1.<credentialId>.<secret>` |
| Built-in tools (unused) | `doc_search`, `read_document`; write tools gated behind `env.dev` or canary |
| Built-in minting (unused) | GraphQL `createMcpCredential(input: {name, workspaceId, accessMode, expirationDays})` |
| GraphQL auth | Session cookie plus `x-csrf-token` header; `aff_mcp_v1.*` tokens are not accepted on `/graphql` |
| Session JWT | 15-minute TTL, so unusable as a durable companion credential |
| Sign-in | `POST /api/auth/sign-in` with email/password works self-hosted; returns a session cookie and a CSRF cookie |
| Workspace create | GraphQL `createWorkspace { id }`; the `init` upload arg is optional, so a server-side create needs no local-first document blob |
| Avatar/profile name | Read from the workspace realtime root document over WebSocket, best-effort |

---

## Open Questions

1. **Service account or owner account.** The wrapper reaches AFFiNE with the
   bootstrap owner's credential. A dedicated bot user would be separable in the
   audit log and would let the tool profile be enforced against a credential that
   cannot administer the instance; it costs a first-class "service account"
   concept in the SSO module that does not exist today, and AFFiNE has no user
   creation API for it.
2. **Attribution.** If the harness knows which human asked for a tool call,
   passing that through (a header on the MCP request, logged by the provider) is
   cheap. Whether Bloud should require it, and what a provider is expected to do
   with it, is unresolved.
3. **Catalog presentation.** As MCP providers accumulate, a flat catalog reads as
   noise. A `mcp` tag, a grouped presentation, or showing a provider only where a
   harness that can use it is installed are all open. Dashboard, not reconciler.
4. **Workspace policy.** Bloud pins the agent to the shared workspace it owns.
   Whether an operator should be able to pick a different one, or expose several
   through one provider, is a settings question this change deliberately does not
   answer.
5. **Search.** The wrapper's `search_docs` is a title search. If an operator wants
   full-text retrieval through MCP, the Manticore sidecar that was removed with
   the built-in provider is the path back, and it would belong to the wrapper
   rather than to AFFiNE.
