# Agent memory

**Status:** Shipped. One provider (`apps/hindsight`), one consumer (`apps/hermes`).
**Last updated:** 2026-10-16

---

## Overview

An agent that forgets between sessions is not much of an agent. Bloud gives the
harness a memory it does not have to think about: the server holds the facts, the
harness captures and recalls them, and the operator installs an app.

A memory server in Bloud is **an app that publishes a REST API and provides the
`memory` contract**. The harness consumes that contract, installs the plugin that
speaks the provider's API, and activates it.

```
hindsight  (provides memory)  ──▶  hermes  (integrates memory, one provider)
      │                                  │
      │  REST API on /v1, /version       │  memory.provider: hindsight
      │  bearer: httpToken               │  $HERMES_HOME/hindsight/config.json
      ▼                                  ▼
   gate (the app's one port)      plugin at a pinned commit
```

The contract is not new vocabulary for an old thing. Bloud already has `mcp`, and
Hindsight used to be wired through it. The two make the agent do different work:

| | `mcp` | `memory` |
|---|---|---|
| Who writes a fact | the model, when it calls `retain` | the harness, on every turn |
| Who reads one back | the model, when it calls `recall` | the harness, before the next answer |
| Reaches the model as | a tool schema, deferred behind Tool Search | injected context, plus `retain`/`recall`/`reflect` tools |
| How many are active | as many as the instance provides | exactly one |

Wiring both to one server would give the agent two memories in two banks, with the
model choosing which one to write to. Hindsight therefore stopped offering `mcp`
when it started offering `memory`, its MCP endpoint is no longer routed, and the
harness has one way in.

## What the provider declares

The `memory` contract carries one secret and three values. The secret is the
bearer the provider's own API checks, so it is a credential the provider validates
rather than a string Bloud invented.

The three values are the harness's half of the integration, and they are values
because only the provider knows them:

| Value | Hindsight declares | What it means |
|---|---|---|
| `pluginName` | `hindsight` | the name in the harness's plugin catalog, and the name the harness installs |
| `pluginCommit` | a 40 character commit | the pin, reviewed with the image tag |
| `pluginConfigPath` | `hindsight/config.json` | where that plugin keeps its settings, relative to the harness's home |

A **second** memory provider is therefore a metadata change, not a code change in
the harness. That is the property the contract exists to hold.

## What the consumer writes

`apps/hermes` writes two files, with two rules.

`config.yaml` gets `memory.provider: <pluginName>`. It is adopt-once: Bloud writes
it only when the operator has not, and removes it only when it names a provider the
catalog declares. An operator who pointed Hermes at another memory server keeps it.

`$HERMES_HOME/hindsight/config.json` gets the connection:

```json
{
  "mode": "local_external",
  "api_url": "http://localhost:9999",
  "api_key": "<the bearer the provider published>",
  "bank_id": "hermes",
  "memory_mode": "hybrid"
}
```

`mode`, `api_url`, and `api_key` are Bloud's and are rewritten on every pass, so a
rotated bearer or a moved address lands. `bank_id` and `memory_mode` are
adopt-once, because they say where the memory lives and how much of it is
injected.

The file is written by the consumer rather than by the provider: an app never
writes another app's files (invariant 15).

## The install is pinned

The harness installs the plugin with `hermes plugins install <name> --ref <commit>
--enable`, using the commit the provider declares. A catalog-name install follows
the vendor's catalog, whose pin moves without this repository reviewing it, so the
commit sits in `apps/hindsight/metadata.yaml` beside the image tag, where a
reviewer sees both.

Measured on the pinned image: the plugin code lands in `$HERMES_HOME/plugins/`,
its Python dependencies land in `$HERMES_HOME/lazy-packages/`, and both are on the
app's data mount, so an installed plugin outlives the container that installed it.
The record Hermes writes inside the plugin directory is what makes the check
idempotent: it names the installed commit, so a pass with nothing to do runs no
command, and a pin bump is visible as a difference.

The harness restarts the container on the pass that installed something, which is
what the vendor's own install message asks for. Every later pass leaves the
running agent alone.

## What an operator can turn off

Memory is not forced. The provider is an optional integration, the harness is a
working agent without it, and every setting above that is the operator's stays the
operator's:

- `memory.provider` is written only into an unset key.
- `bank_id` and `memory_mode` are written only into an unset file.
- Uninstalling the provider removes the provider choice on the next pass, and the
  memory server's own data is the app's to keep or delete.

## Where the details live

| Question | Read |
|---|---|
| The provider's routes, the gate, and what was measured | [apps/hindsight/INTEGRATION.md](../../apps/hindsight/INTEGRATION.md) |
| The harness's two files, the install, and the known limits | [apps/hermes/INTEGRATION.md](../../apps/hermes/INTEGRATION.md#the-memory-provider-contract-integrationsmemory) |
| How MCP namespaces work, and why this is not one | [mcp.md](mcp.md) |
| The contract's own entry and its rules | `services/host-agent/internal/catalog/contracts.go` |
