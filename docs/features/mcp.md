# Bloud MCP

**Status:** Built for the AFFiNE chain; plumbing general
**Last updated:** 2026-10-02
**Roadmap:** [plans/mcp-integrations.md](../plans/mcp-integrations.md)

---

## Overview

An MCP server in Bloud is **an app that ships a first-party MCP endpoint and
provides the `mcp` contract**. A harness consumes that contract and registers
each provider as a tool namespace.

```
affine (provides mcp)  <──  hermes (integrates mcp, optional + multi)
```

There is no wrapper container. AFFiNE ships its own MCP server: a stateless
streamable-HTTP endpoint per workspace, authenticated by a scoped
`aff_mcp_v1.<credentialId>.<secret>` bearer that AFFiNE mints through its own
GraphQL API. Bloud's job is to make that endpoint reachable and credentialed
without the operator hand-assembling anything: the AFFiNE configurator creates
the workspace, mints the credential, and publishes both. A harness that
declares the contract gets a real AFFiNE credential it can revoke in the
AFFiNE UI.

Nothing in that chain is a new kind of thing. The provider is an ordinary
catalog app; the credential is an ordinary published secret under a contract
name; the consumer is an ordinary `multi` integration consumer.

The design goal is that adding the next MCP provider is a `provides:` block in
`metadata.yaml` plus the calls that mint its credential, with no change to the
orchestrator, the contract registry, or the container runtime.

### Why not a wrapper

The first draft of this design assumed an MCP server had to be a separate app
wrapping the target: `affine-mcp` with a required dependency on `affine`. That
was written before the pinned image was inspected.

A wrapper is the right shape for an app that has no MCP server and no way to
mint a scoped credential. It is the wrong shape for one that ships both:

- **It duplicates the trust boundary.** A wrapper holds the target's admin
  credential and republishes a derived one. The app already has a scoped
  credential mechanism that does not need the admin password in a second
  place.
- **It cannot see what the app knows.** The AFFiNE MCP endpoint is scoped to a
  workspace id that only AFFiNE assigns. A wrapper would have to discover it
  anyway, over the same API, from outside the app that owns it.
- **It is one more container, one more image to vet, one more thing that breaks
  when the target's API changes.** The upstream ships the endpoint and owns its
  compatibility.

So the provider is the app. Where a future target genuinely has no MCP server
of its own, the wrapper shape is still available; nothing in the plumbing
depends on the provider being the app rather than a wrapper around it.

---

## Core Principles

- **Real keys only.** The published bearer is a credential the provider app
  created and validates. A string Bloud invents is not a credential unless the
  app was told about it. This is what makes revocation mean something: revoke
  it in the AFFiNE UI and MCP access stops.
- **The credential shape is the provider's private concern.** The consumer
  receives one opaque string. Whether the provider minted it over GraphQL,
  adopted it from a config file, or generated it at bootstrap is invisible
  across the boundary and stays that way.
- **A provider declares what it fills at runtime.** Some contract values are
  static metadata; some only exist once the app is running. Both are declared
  at catalog load, and the loader checks that every value the contract declares
  is covered by one channel or the other.
- **Harnesses choose their own tool sets.** `multi: true` on the consumer side
  means Hermes takes every MCP provider the instance offers while a
  specialized harness takes two.
- **One published credential means one principal.** No per-user MCP
  authorization. Stated limit, not oversight.

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
# apps/affine/metadata.yaml
provides:
  mcp:
    secrets: [httpToken]
    values:
      serverName: affine
    runtimeValues: [path]
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
      - app: affine
        default: true
```

`requires: [httpToken]` is the whole consumer surface. Declaring a contract
gets the provider's address and the named secrets, never a credential that was
not asked for.

### `runtimeValues`: values the provider cannot know at load time

`path` is `/api/workspaces/<workspaceId>/mcp`. The workspace id is assigned by
AFFiNE when the workspace is created, which happens in `PostStart`, which
happens after the catalog has loaded. It cannot live in `metadata.yaml`.

`ContractProvides` therefore carries `RuntimeValues []string` alongside the
static `Values map[string]string`. The loader enforces three things:

1. A runtime value must be a single name, not a dotted path or a template.
2. It must be a key the contract actually declares.
3. It must **not** also appear in the static `Values` map. Declaring the same
   key twice means two sources of truth for one field, and the resolution order
   would silently prefer one.

Resolution prefers the published runtime value and falls back to the static
metadata (`contractValue` in
`internal/engine/orchestrator/orchestrator.go`). So a provider can move a
value from runtime to static, or the reverse, without touching consumers.

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
host-networked harness cannot resolve it. A composed URL would be right for
some consumers and silently wrong for others, so the binding hands over both
addresses plus the path and the consumer composes the one its own topology can
dial.

### Why `mcp` has no `SatisfiedBy`

The `inference` contract declares `SatisfiedBy: [modelSource]` so a consumer
keeps working when no gateway is installed. That promotion is implemented by
`promotedSources` (`internal/engine/orchestrator/inference.go`), which
iterates **every installed provider of the fallback contract** and ignores the
consumer's `compatible` list.

For a tool server that is the wrong failure mode. A harness pointed at an
arbitrary installed MCP provider gets a namespace full of tools it never asked
for. There is no meaningful fallback for "give me AFFiNE's documents," so
`mcp` opts out of promotion.

---

## The Credential Boundary

The rule that governs every provider:

**The published `httpToken` is always a credential the provider itself
validates.**

For AFFiNE that means the configurator asks AFFiNE to mint it:

```
mutation($input: CreateMcpCredentialInput!) {
  createMcpCredential(input: $input) { token }
}
# input: { name: "bloud", workspaceId: <ws>, accessMode: READ_ONLY, expirationDays: 365 }
```

The token is revealed only by the call that creates it, which is why the
configurator stores it rather than re-reading the credential list. It is
stored through the ordinary published-secret path:

```go
c.secrets.SetAppSecret(appName, "httpToken", token)
c.secrets.SetAppContractValue(appName, "mcp", "path", path)
```

`SetAppContractValue` writes into `AppSecrets[app].PublishedValues[contract]`
(`internal/secrets/manager.go`), a per-contract bag kept separate from the
flat `Published` map so a value scoped to `mcp` cannot be read by a consumer
of a different contract that happens to use the same key name.

### Why the app mints and Bloud does not

Bloud could generate a random string and publish it. That would be a field
named `token` that authenticates against nothing, and every consumer downstream
would assume a scope it does not have.

Minting through the provider's own API buys three things a generated string
cannot:

- **Revocation works.** The credential appears in AFFiNE's own credential
  list, named `bloud`. An operator can revoke it and MCP access stops.
- **Scope is the provider's, not Bloud's guess.** `READ_ONLY` is enforced by
  AFFiNE, so the harness cannot write to the knowledge base even if it wanted
  to.
- **Expiry is real.** The credential carries an expiry the provider enforces,
  so a forgotten install does not keep a valid tool credential forever.

### Read-only, and not by choice

`READ_WRITE` is rejected outright on a stable AFFiNE release with "MCP write
tools are not available" unless the server runs with `env.dev` or a canary
channel. Asking for write would fail every pass on the pinned image. `READ_ONLY`
is what the stable release serves, and it is also the right scope for a
knowledge-base tool wired into an agent.

### The `copilot.enabled` gate

The MCP server lives under AFFiNE's copilot module and the flag defaults off,
so without it every mint and every MCP request answers "Copilot is disabled."
The configurator sets it in the `config.json` it already writes in `PreStart`.

Enabling the flag also opens AFFiNE's BYOK AI surface. That stays inert until
the operator supplies a provider key in AFFiNE's own settings, and it grants
Bloud nothing: the MCP path never reads an AI key.

### The empty-token rule, and where it applies

`publishedSecret` (`internal/engine/orchestrator/orchestrator.go`) returns `""`
both when the consumer did not require the secret and when the provider has not
published it. Those two empties are conflated by design.

**The consumer must treat an empty token as not-ready and write nothing.**
This matters more on the consumer side than the provider side, because
`buildIntegrations` binds every compatible provider an optional contract
declares, including providers that are not installed at all (they arrive with
`Installed: false` so the consumer can prune entries Bloud wrote for them).
The harness filter is therefore two conditions:

```go
if !b.Installed || b.Token == "" || b.Path == "" {
    // not there yet, or not published yet: remove any entry Bloud wrote
}
```

Writing an entry with an empty bearer registers a tool namespace that 401s
forever.

### Provider failures do not fail the node

A wrapper's whole purpose is its MCP endpoint, so a wrapper that cannot publish
a credential should fail the pass and retry. AFFiNE is not a wrapper: the node
is the knowledge base itself, and a node serving its users fine should not land
in ERROR because its MCP credential could not be minted.

So `ensureMCPCredential` logs and returns. The consumer is protected anyway by
the empty-token filter, so a broken credential registers no namespace rather
than one that fails forever. What is given up is the reconciler's own failure
signal, which is what the warning carries.

---

## Reachability

No new port plumbing was needed, because the provider is an ordinary app with
an ordinary published port.

The harness dials `LocalURL + Path`. `LocalURL` is `http://localhost:<port>`,
which is the address Traefik itself uses to reach every app, and the address a
host-networked harness can resolve. `BaseURL` (`http://apps-affine:3010`) is
the network-scoped name that a host-networked consumer cannot resolve, which is
why the binding carries both and the consumer picks.

This is why the loopback-only publish and `routing.public: false` from the first
draft are not in this change. They solved a problem the wrapper shape created:

- A wrapper needed a port published for a consumer that could not reach it by
  container name. The provider's existing port is already reachable at
  `LocalURL`.
- A wrapper needed to be excluded from Traefik routing. The provider is
  legitimately routed: it is a user-facing app. Its MCP endpoint sits behind
  the same entrypoint as the rest of the app, authenticated by its own bearer
  rather than by network position.

That second point is a real difference in exposure and worth stating plainly:
`https://affine.example.com/api/workspaces/<ws>/mcp` is reachable from wherever
the app is reachable. It is not a secret URL. What gates it is the bearer
token, which is a scoped, revocable, read-only credential the provider issued.
That is a defensible boundary, but it is a different one from "nothing outside
the Bloud host can open a socket," and it is the boundary the app itself chose
for its own MCP server.

### Deferred: loopback-only publishing

Publishing a container port on `127.0.0.1` instead of `0.0.0.0` is still a
missing capability worth having on its own merits (a live stack shows
`0.0.0.0:9001->9000/tcp`). It is not MCP work; it is a network-exposure
reduction for every app. See the roadmap.

---

## Data Flow

### Install

1. Operator installs Hermes. The `mcp` integration is optional and multi, so
   `PlanInstall` auto-configures every compatible provider it can.
2. If AFFiNE is not installed, the recorded integration config makes the
   orchestrator bring it up first: `computeAppDeps`
   (`internal/engine/orchestrator/pipeline.go`) turns the recorded integration
   into a graph edge.
3. AFFiNE's `PreStart` writes `config.json` with `copilot.enabled: true`.
4. AFFiNE's `PostStart` signs in as the bootstrap owner, settles a workspace,
   mints the credential, and publishes `mcp.path` and `mcp.httpToken`.
5. Hermes' `PreStart` reads `Integrations.MCPServers`, filters, and renders
   `mcp_servers.affine` into its own `config.yaml`.
6. Hermes starts with the namespace registered.

Step 4 before step 5 is the graph edge. Without it Hermes would render nothing
and pick the namespace up on a later pass.

### Reconcile

`PreStart` and `PostStart` run on every pass that **drives** a node. That
qualifier is load-bearing and was measured, not assumed: a node already at
`RUNNING` contributes no work to a pass (`reconcile pass started` followed by
`level work collected: work: 0` on every level). What does drive a node is a
transition: a host-agent restart, a crash recovery, a reinstall, or an intent
that re-plans it. A container restarted out from under the agent does not count
either, because the graph still believes the node is `RUNNING`.

There **is** now a periodic trigger, and it does not close this gap. The
self-healing pass (`selfheal.go`, armed 60s after the first convergence and
idle-reset on every pass so the interval is a floor) submits a
`ReconcileIntent` that drives exactly one thing: `retryErroredNodes()`, which
begins `if node.ActualStatus != graph.StatusError { continue }`. Healthy nodes
are never touched, by design.

That matters here because of the decision to make provider failures log and
swallow rather than ERROR the node. A revoked or broken MCP credential leaves
AFFiNE `RUNNING`, so the periodic pass walks straight past it. **The credential
self-heals on the next pass that actually drives the provider node, not on the
60s timer.** Do not document or design against spontaneous re-minting, and do
not "fix" this by making the credential failure ERROR the node: the node is
the knowledge base, and taking it down because an add-on capability broke is
the category error this section exists to warn about. If the credential needs to
heal faster than a restart, the answer is a contract-scoped health signal the
self-heal pass understands, not a status change.

`PostStart` on the provider is a probe, not a re-mint. It calls `tools/list`
with the stored bearer:

- **200**: the credential is good. Nothing is minted. A steady-state pass costs
  one round trip.
- **401/403**: the credential was revoked or expired. Mint a replacement and
  republish.
- **transport fault**: keep the stored credential and try again next pass.
  Minting a spare for every network blip is a pile-up a long-lived install
  cannot clean up, because AFFiNE keeps every credential it has ever issued.

The workspace is sticky: the workspace Bloud already published wins as long as
it still exists, so a workspace the operator creates later cannot silently
move the credential out from under a running harness. Failing that, the first
existing workspace is adopted rather than a second copy being created, and only
an instance with no workspaces at all gets one made for it.

`PreStart` on the consumer re-renders the server list and reports `changed`
only when the document actually differs, so a steady-state pass does not
recreate the container.

### Revocation

An operator revokes the `bloud` credential in AFFiNE's UI. The next provider
pass that drives the node probes, gets 401, mints a replacement, and
republishes. The consumer's next pass sees a changed token and rewrites its
config. No operator step beyond the revoke, subject to the driving caveat
above.

### Uninstall the provider

The binding arrives with `Installed: false`. The consumer's filter removes
`mcp_servers.affine` and leaves any operator-added server untouched. If the
section is empty it is deleted rather than left as an empty map.

### Uninstall the consumer

Nothing special: Hermes' config file goes with it.

---

## Search: why `doc_search` needs a bundled indexer

`tools/list` succeeding does not mean the tools can answer. `doc_search` fails
with `SEARCH_UNAVAILABLE` unless a search backend is reachable, and the reason
is visible in the shipped bundle. `DocumentRetrievalService.search` runs two
channels and throws only when **both** come back null:

```js
[config.indexer.enabled ? indexer.searchDocsByKeyword(...)          : null,
 context.canEmbedding    ? context.matchWorkspaceDocCandidates(...) : null]
```

| Channel | Off because | What turns it on |
|---|---|---|
| lexical | `config.indexer.enabled` defaults to `false` | `AFFINE_INDEXER_ENABLED=true` plus a reachable indexer service |
| vector | `canEmbedding` needs an embedding model | an operator-supplied AI provider key (BYOK) |

Bloud lights the **lexical** channel by bundling Manticore Search as a
container of the AFFiNE app, which is invariant 3 doing its job: the app owns
its own search infrastructure the same way it owns its own postgres and redis.
The vector channel stays dark until an operator supplies an AI key, which is
why responses carry `degraded_reason: "VECTOR_UNAVAILABLE"` and that is the
expected steady state, not a fault.

The provider enum is exactly `manticoresearch | elasticsearch`; the endpoint
default is `http://localhost:9308`. Bloud sets it explicitly to the sidecar:

```yaml
AFFINE_INDEXER_ENABLED: "true"
AFFINE_INDEXER_SEARCH_PROVIDER: manticoresearch
AFFINE_INDEXER_SEARCH_ENDPOINT: "http://apps-affine-search:9308"
```

Verified on a live install. Before the sidecar:

```json
{"code": -32001, "message": "Error executing tool: SEARCH_UNAVAILABLE"}
```

After:

```json
{"retrieval_mode": "lexical", "degraded_reason": "VECTOR_UNAVAILABLE", "hits": []}
```

`hits: []` on a workspace with no documents is the correct answer, not a
failure. The indexer creates its `doc` and `block` real-time tables on startup
and `indexer.autoIndexWorkspaces` runs on its own schedule.

One trap worth recording: Manticore's HTTP interface answers `GET /` with a
hardcoded **Elasticsearch 7.4.1 compatibility banner** (`"tagline": "You
Know, for Search"`). It is Manticore. Do not conclude from that banner that
something else is listening.

---

## Workspace scoping, and why one credential is not enough

An AFFiNE MCP credential is **per workspace**. `createMcpCredential` takes a
`workspaceId`, `mcp_credentials` carries `workspace_id` and `user_id`, and the
endpoint is `/api/workspaces/<ws>/mcp`. Scope is enforced by the provider: a
credential minted for workspace B returns **401** against workspace A's
endpoint. Verified on a live instance.

That makes the single sticky credential a real limitation: the agent sees one
workspace and nothing else. Closing it needs two things, and only one of them is
in Bloud's hands.

**What Bloud cannot do: enumerate the instance.** AFFiNE has the queries
(`adminWorkspaces`, `adminWorkspacesCount`, `adminDashboard`), and Bloud's
bootstrap account really does hold the `administrator` user feature. They all
still return `404 Resource not found`, because the shipped bundle hardcodes the
gating context:

```js
context: ({ req, res }) => ({ req, res, isAdminQuery: false })
```

One occurrence, no env override, no admin-enable flag in the image's env list.
The admin GraphQL surface is not reachable from this build. So the only
enumeration available is `workspaces`, which returns workspaces the caller is a
member of.

**The shape that closes it: a typed list payload.** The contract publishes a
list of endpoints instead of one `path` and one `httpToken`:

```go
Values: []ValueSpec{{Key: "endpoints", Kind: List}}

secrets.SetAppContractList(app, "mcp", []MCPEndpoint{
  {WorkspaceID: "aaa", ServerName: "affine-notes",
   Path: "/api/workspaces/aaa/mcp", Token: "aff_mcp_v1..."},
  {WorkspaceID: "bbb", ServerName: "affine-work",
   Path: "/api/workspaces/bbb/mcp", Token: "aff_mcp_v1..."},
})
```

JSON at the storage edge only; the typed setter and the `bindContract` arm keep
the type at the configurator boundary, which is what invariant 15 asks for.
The consumer side is free: Hermes' `mcp_servers` map already holds N entries,
so one provider renders one namespace per workspace.

**How the Bloud account gains visibility.** It cannot discover workspaces it
cannot see, so visibility has to be granted: the operator invites the Bloud
account into a workspace through AFFiNE's normal sharing flow, and the next
pass that drives the provider node enumerates it, mints for it, and publishes
it. That is the correct posture anyway. An agent should read what it was
granted, not everything on the instance.

This is deliberately **not built yet**. The current change ships the search
backend and the single-workspace credential; the list payload is the follow-up.

---

## Harness Configuration

Hermes reads `mcp_servers` from its own `config.yaml`:

```yaml
mcp_servers:
  affine:
    url: http://localhost:3010/api/workspaces/<ws>/mcp
    headers:
      Authorization: Bearer aff_mcp_v1.<cred>.<secret>
```

Bloud manages the namespace, not the section. The key a provider's
`serverName` names is Bloud's to write and to remove, because that is the app
the integration contract points at. An entry the operator added by hand under
any other name survives every pass, the same discipline `applyInference`
follows with a hand-chosen model.

The whole map is re-rendered each pass, so a removed provider leaves no entry
behind rather than lingering as a stale namespace.

---

## Security Properties

- **Scoped credential.** `READ_ONLY`, enforced by the provider. The harness
  cannot write to the knowledge base.
- **Revocable by the owner.** The credential is named `bloud` in AFFiNE's own
  credential list. Revocation does not require a Bloud action.
- **Bounded lifetime.** 365 days, provider-enforced. The configurator re-mints
  on its own when the credential stops validating, so expiry is not an outage.
- **Contract-scoped secrets.** A harness declaring `mcp` receives only the
  secrets listed under `requires`. Declaring a contract never grants more than
  was asked for.
- **No admin credential on the wire.** The MCP request carries the scoped
  bearer, never the owner password. The owner password is used once, in
  `PostStart`, to mint.
- **Blast radius.** One provider credential, one app, one read-only scope. A
  harness with five providers holds five independently revocable credentials.

### What these properties do not cover

**One published credential means every agent using that provider acts as one
principal.** Bloud has per-human SSO identity and no per-harness-user MCP
authorization. A tool call through the AFFiNE namespace is attributable to
"the Bloud MCP integration," not to the human who asked for it.

**The endpoint is as reachable as the app.** Under a public domain, the MCP
path is reachable from wherever AFFiNE is reachable. The bearer is the gate,
not the address. This is the provider's own design for its own endpoint; a
Bloud-chosen wrapper could have been stricter.

**The credential is stored in Bloud's secrets file.** `secrets.json` holds the
bearer. Anyone with the Bloud host's filesystem access holds it, which is the
same exposure as every other published credential.

---

## Non-Goals

- **stdio transport.** A stdio-only MCP server cannot cross a container
  boundary. Streamable HTTP is the only supported transport.
- **A Bloud-hosted MCP gateway.** A gateway that hosts many upstreams collapses
  the per-app provider edge, which is the part that makes ordering and
  uninstall-blocking work. Per-app providers are the design.
- **An MCP client in the dashboard.** The harness is the client. Bloud hosts
  servers and wires them; it does not consume them.
- **Wrapping apps that have no MCP server.** Available if a future target needs
  it, and the plumbing supports it, but nothing in this change is shaped around
  it.

---

## Adding an MCP Provider

1. **Verify the app ships a streamable-HTTP MCP endpoint and a way to mint a
   scoped credential.** Both are prerequisites. An app with an MCP endpoint but
   no minting mechanism has no real key to publish, which fails the first
   principle.
2. **Declare the contract.**

   ```yaml
   provides:
     mcp:
       secrets: [httpToken]
       values:
         serverName: <app>
       runtimeValues: [path]   # only if the path is not static
   ```

3. **Mint and publish in `PostStart`.** Sign in with the credential the
   configurator already bootstraps, create or adopt the scope the endpoint
   needs, mint, and publish:

   ```go
   c.secrets.SetAppContractValue(appName, "mcp", "path", path)
   c.secrets.SetAppSecret(appName, "httpToken", token)
   ```

4. **Make it idempotent and self-healing.** Probe before minting. Treat a
   transport fault as "keep what we have," and only a definitive rejection as
   "replace." A configurator that mints per pass leaves an unbounded pile of
   live credentials behind.
5. **Log and swallow provider failures.** Do not fail the node. See
   [Provider failures do not fail the node](#provider-failures-do-not-fail-the-node).
6. **Test it.** The AFFiNE tests (`apps/affine/mcp_test.go`) are the pattern:
   first pass creates and publishes, steady state mints nothing, revocation is
   replaced, a failed mint publishes nothing, a CSRF-demanding server is
   satisfied, and a transport fault mints no spare.

---

## Verified Facts (AFFiNE 0.27.4)

Measured against `ghcr.io/toeverything/affine:0.27.4`, the pinned image, not
read from documentation. These are the facts the implementation rests on.

| Fact | Value |
|---|---|
| Endpoint | `POST /api/workspaces/{workspaceId}/mcp` |
| Transport | Stateless streamable HTTP, batch up to 20 requests |
| Auth | `Authorization: Bearer aff_mcp_v1.<credentialId>.<secret>` |
| Bad token | `401` |
| Tools (READ_ONLY) | `doc_search`, `read_document` |
| Minting | GraphQL `createMcpCredential(input: {name, workspaceId, accessMode, expirationDays})` returning `{token, credential}` |
| Token reveal | Only by the creating call |
| READ_WRITE | Rejected on stable: "MCP write tools are not available" unless `env.dev` or canary |
| Workspace create | GraphQL `createWorkspace { id }`; the `init` upload arg is optional, so a server-side create needs no local-first document blob |
| Gate | `copilot.enabled: true` in `config.json` |
| Sign-in | `POST /api/auth/sign-in` with email/password works self-hosted; returns a session cookie and a CSRF cookie |
| GraphQL auth | `x-csrf-token` header plus the session cookie |

---

## Open Questions

1. **Service account or owner account.** The credential is minted by the
   bootstrap owner account. A dedicated bot user would be separable in the
   audit log, and costs a first-class "service account" concept in the SSO
   module that does not exist today.
2. **Attribution.** If the harness knows which human asked for a tool call,
   passing that through (a header on the MCP request, logged by the provider)
   is cheap. Whether Bloud should require it, and what a provider is expected
   to do with it, is unresolved.
3. **Catalog presentation.** As MCP providers accumulate, a flat catalog reads
   as noise. A `mcp` tag, a grouped presentation, or showing a provider only
   where a harness that can use it is installed are all open. Dashboard, not
   reconciler.
4. **Workspace policy.** Bloud adopts the first existing workspace rather than
   always creating one, which is the least-surprising default for a single-user
   home instance. Whether an operator should be able to pick which workspace
   the agent reads is a settings question this change deliberately does not
   answer.
