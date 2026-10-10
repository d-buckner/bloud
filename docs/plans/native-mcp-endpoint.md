> Status: draft

# Plan: a Bloud-native MCP endpoint

**Last updated:** 2026-10-09

## Why

Bloud's differentiator is that it holds the integration knowledge: which app
depends on which, what credential joins them, and whether the relationship still
works. [features/mcp.md](../features/mcp.md) makes an app that serves MCP an
ordinary catalog app, and that is right for a tool surface over **one** target.

It cannot express the tool surface that would actually be worth having, which is
the one that spans apps. "Why is this film not playable" needs Radarr, Sonarr,
the download client and Jellyfin in one answer. "What is broken right now" needs
the orchestrator. "Who has an account here" needs Authentik. No wrapper can write
any of those, because a wrapper is outside the graph and can only see what its own
one contract hands it.

This plan proposes that Bloud serve those tools itself, from the same resolved
bindings the dashboard already consumes.

## Where this came from

The request was to add
[`jhomen368/overseerr-mcp`](https://github.com/jhomen368/overseerr-mcp). It could
not be used: its HTTP transport serves `/mcp` with no credential check at all, so
publishing an `httpToken` for it would be a token that authenticates against
nothing, which is the exact thing the `mcp` contract rule forbids.

The replacement candidate found in its place was
[`bardesss/arr-mcp`](https://github.com/bardesss/arr-mcp), one server for twelve
media services, 76 stars, mandatory bearer, released daily. It was spiked live
against Seerr v3.4.1 and it works. It was adopted, not rejected: the plan that
shipped it, with the scope that made the concerns below moot, is
[arr-mcp-provider.md](arr-mcp-provider.md). This document is the case for the
tools no wrapper can write, which arr-mcp does not and cannot cover.

## The decision rule

**Bloud serves the tools that only Bloud can write. Third-party wrappers serve
the tools that go deep inside the apps they are configured for.**

That is not a preference, it is a comparison of who has the information:

| Tool shape | Who can write it | Example |
|---|---|---|
| Deep in one app's own model | a specialist wrapper | AFFiNE's 106 document tools |
| Across several apps a wrapper declares contracts for | a multi-contract wrapper | arr-mcp (Radarr, Sonarr, Jellyfin, Seerr) |
| About the instance itself | Bloud only | what is installed, degraded, exposed |
| About the wiring | Bloud only | which contracts are met, which are not |

A wrapper will always beat Bloud at a single app's document model, because the
wrapper's author spends every day on that app. Bloud will always beat a wrapper
at anything that needs the graph, because the wrapper is not allowed to see it.
Getting this boundary wrong in either direction is expensive, so it is stated
here rather than decided per app.

## What only Bloud can see

Four sources, all already in the process:

1. **The graph.** Node states, what converged, what is in `ERROR`, what is
   waiting on a provider. `internal/engine/orchestrator`.
2. **The contract bindings.** Which provider satisfies which contract, whether it
   is `Installed`, and what address and credential came out of resolution.
   `pkg/configurator` bindings.
3. **The identity layer.** Users, groups, which app is guarded by which strategy.
   `pkg/authentik`, the user-management router.
4. **The routing and exposure picture.** What Traefik publishes, on what host,
   behind what guard. `internal/traefikgen`.

A wrapper can approximate none of these from outside. That is the moat, and it is
also the honest scope: anything that does not need one of these four belongs in a
wrapper.

## Architecture

### It lives in host-agent, not in `services/`

Invariant 13 says a directory under `services/` is earned by shipping to a
machine where host-agent does not run. An MCP endpoint over the orchestrator's own
state runs on the same box as the orchestrator, exactly like the API, SSO and the
store, which all stayed as packages under `internal/`. So this is a router under
`internal/api`, not a new service.

### It provides the `mcp` contract through a zero-container app

Hermes consumes `mcp` providers and picks its own set
([plans/mcp-integrations.md](mcp-integrations.md) decision 8). The native
endpoint should not be special-cased into Hermes; it should look like every other
provider. A catalog app that declares no container does that:

```yaml
name: bloud-mcp
displayName: Bloud MCP
headless: true
isSystem: true
provides:
  mcp:
    secrets: [httpToken]
    values:
      serverName: bloud
      path: /mcp
```

The orchestrator already tolerates an app with no containers:
`primaryContainerNode` returns the app name itself
(`graph_build.go:109-113`) and the all-nodes-running check returns true
trivially (`status.go:154-157`). So the app converges without a node, and the
contract resolves against host-agent's own listener.

This needs verifying rather than assumed: the zero-container path exists in those
two functions, but install, status and removal were not written with a
container-less user app in mind. It is the first thing to test.

### The credential follows the same rule as every other provider

Host-agent mints a bearer, stores it, and verifies it per request. The published
`httpToken` is a credential the provider validates, which is the rule
[features/mcp.md](../features/mcp.md) states and which the spike re-confirmed
matters: an arr-mcp listener returned `401` with no bearer and `401` with a wrong
one, and that behaviour is what made it usable at all.

Nothing new is needed here. `SetAppSecret("bloud-mcp", "httpToken", ...)` is the
same call `dav-mcp` makes.

### Tools are derived from the graph, and that has a caching consequence

An agent on a Bloud with only Seerr installed should not be offered the Radarr
half of a tool. Deriving `tools/list` from installed apps is the correct
behaviour and it is free, since the resolver already computes it.

The consequence is that the tool list now changes at runtime, and MCP clients
cache it. arr-mcp advertises an hour `ttlMs` for `tools/list` precisely because
its list is static. Bloud's is not, so the endpoint must either advertise
`listChanged` and push a refresh when the graph changes, or advertise a short
`ttlMs`. This is a real protocol decision, not a detail, and it is the one thing
about this design that has no existing precedent in the tree.

## Candidate first tool set

Deliberately small, and every one of them impossible for a wrapper:

| Tool | Answers | Needs |
|---|---|---|
| `bloud_stack` | what is installed, each node's state, what is degraded | graph |
| `bloud_diagnose` | why is this title not playable | `pvr` + `mediaServer` + `downloadClient` bindings |
| `bloud_requests` | what has been requested and what is pending | `requestManager` binding |
| `bloud_accounts` | who has accounts, and through which provider | identity layer |
| `bloud_exposure` | what is routed, on what host, behind which SSO strategy | routing + catalog |
| `bloud_wiring` | which declared contracts are satisfied and which are not | resolver |

`bloud_wiring` is the one to build first: it is read-only, it needs no new client
code at all, and it is the purest demonstration of the thesis. An agent that can
ask "is anything unwired" is doing something no third-party server can.

## What this does not replace

**`apps/affine-mcp` stays.** AFFiNE is the deep-single-app case. Its tool surface
is 106 read-write tools over AFFiNE's document model, reached through GraphQL and
WebSocket, and the reason Bloud ships a wrapper at all is that AFFiNE's own
endpoint is read-only and workspace-scoped. Bloud does have the hard part of an
AFFiNE client already, the cookie plus CSRF session and the GraphQL envelope in
`apps/affine/api.go`, but that plumbing exists to settle an owner and a
workspace. Reimplementing a document tool surface in Go would be a large,
permanent, low-differentiation investment against a specialist that is better.

The same reasoning keeps `apps/dav-mcp`.

The two shapes are complementary rather than competing. The native endpoint takes
the class of tool no wrapper can write; wrappers keep contributing their own
namespace through the same contract they use today. Installing a wrapper adds a
tool surface; installing apps adds native capability. Neither has to know about
the other.

There is one AFFiNE-shaped exception worth noting: instance-level facts about
AFFiNE, which workspaces exist and who is in them, are already read by
`apps/affine/members.go`. Those belong to the native endpoint's `bloud_accounts`
question, not to a document tool surface.

## Relationship to arr-mcp

arr-mcp was adopted and shipped as `apps/arr-mcp`; the decision and the scope
are in [arr-mcp-provider.md](arr-mcp-provider.md). Its shape, several optional
contracts rather than one, is what moves the "across apps" row in the table
above out of Bloud-only reach: a wrapper can join the apps it declares. What it
still cannot see is the instance itself, which is the remaining moat and the
subject of this plan: graph state, the whole wiring picture, and the identity
and exposure facts that live in Bloud rather than in any provider. The two are
complementary rather than competing, and this plan stays deferred.

## Open questions

1. **Zero-container app support.** The orchestrator tolerates it in two functions.
   Whether install, status and removal do is untested.
2. **Where it is served.** A path on the dashboard origin, or its own host. The
   endpoint needs to be reachable from a container on `apps-net` and, for a
   browser-based harness, from the public URL.
3. **`tools/list` invalidation.** See the caching consequence above. This is the
   only genuinely new protocol problem in the design.
4. **One principal.** Same non-goal as the existing MCP design: one published
   bearer means one agent identity, with no per-human authorization. A native
   endpoint makes this more visible rather than worse, because it can see more.
5. **Read-only by default.** The first set is read-only. Whether writes ever come
   is a separate decision, and if they do, Bloud's orchestrator intent queue is
   the only acceptable path: an MCP handler that mutates directly would break
   invariant 1.
6. **Category and presentation.** A native provider is not a productivity app. The
   catalog presentation question left open in
   [features/mcp.md](../features/mcp.md) gets answered by this one either way.

## Related

- [features/mcp.md](../features/mcp.md): the contract, the credential boundary,
  the shipped providers.
- [plans/mcp-integrations.md](mcp-integrations.md): the decision history for the
  wrapper shape, including why the built-in AFFiNE provider was removed.
- [plans/media-stack-integration.md](media-stack-integration.md): the app-side
  wiring this endpoint would expose rather than duplicate.
- [architecture/overview.md](../architecture/overview.md),
  [guides/contributing-apps.md](../guides/contributing-apps.md).
