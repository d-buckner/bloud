# Agent API and multi-port apps

An app can expose more than one thing. `port:` describes the one a person
clicks; `extraPorts:` describes the others, and a contract names which one it
is served on.

This is the platform primitive behind [Hermes](../../apps/hermes/INTEGRATION.md)
handing its agent to other apps, written down separately because the shape
generalizes and the reasoning is about the platform rather than about Hermes.

## The problem this solves

`App.Port` was doing two jobs at once:

- the **UI port**, which Traefik routes the app's root to and the dashboard
  opens
- the **app-to-app port**, which `ProviderRef.BaseURL` composes as
  `http://<container>:<port>`

For a single-surface app those are the same number and nothing notices. They
come apart as soon as an app serves a second thing. Hermes runs a browser
dashboard on 9119 and an OpenAI-compatible agent API on 8642, and a consumer
wants the second one. There was no way to say that in metadata.

## The shape

```yaml
port: 9119          # the UI: dashboard tile, routed at <app>.<host>

extraPorts:
  - name: gateway
    port: 8642
    pathPrefix: /v1

provides:
  agentApi:
    port: gateway   # the name, not the number
    secrets:
      - apiKey
```

The offer names a port rather than repeating one. The number lives in exactly
one place, so a consumer cannot be handed an address for a surface the provider
moved, renamed, or never exposed.

## Why an extra port is not a published port

This is the part worth being strict about.

A container `ports:` entry publishes to the host, and the host has a LAN
interface. For a web UI that is a familiar trade-off. For an agent endpoint
with tool and terminal access it is not: it puts a thing that executes behind
one bearer token on every device on the network. That is the same failure class
that got `HERMES_DASHBOARD_INSECURE` removed upstream.

An `extraPort` is published to Bloud's consumers instead of to the network:

```
consumer container ──apps-net──▶ host-gateway ──▶ Traefik ──loopback──▶ app:8642
                                                    │
                                          HostRegexp(^app\.)
                                          && PathPrefix(/v1)
```

The app binds loopback. Traefik shares the host namespace and reaches it. The
LAN never sees the port, so it gains no reach it was not declared to have.

## What each layer does

**Catalog** (`internal/catalog`) validates the declarations. Names are unique,
ports are valid and do not collide with the UI port, and `pathPrefix` is a
non-root absolute path. A prefix of `/` is rejected because that is the UI's.

**The loader rejects an offer naming a port the app does not declare.** It does
not fall back to the UI port. A fallback would hand the consumer an address that
connects, speaks the wrong protocol, and points at an app that did nothing
wrong. That reads as a broken provider rather than as a catalog missing a line,
which is the worse failure.

**Traefik** (`internal/traefikgen`) emits a second router per extra port,
keyed on the prefix and ranked above the UI catch-all. The priority matters:
the UI router matches the whole host, so a lower-ranked prefix router would let
the dashboard answer with HTML where the consumer expected JSON. This reuses
the shape the forward-auth outpost and bypass routers already use.

**The resolver** composes the consumer's endpoint from the provider's routed
public origin plus the prefix, and adds the `host-gateway` extraHosts pin to
the consumer's container so that hostname resolves under plain http. That is
the same mechanism `IssuerExtraHost` uses for the OIDC issuer, for the same
reason: the routed name is not a container name and has no DNS record of its
own. Under a https public URL no pin is added, because the name resolves to
the real terminator and pinning it to this box would point at something that
serves no certificate for it.

## Why `agentApi` is its own contract

It could have reused `inference`. Both are OpenAI-shaped. They mean different
things, and the difference is what a consumer is agreeing to.

`inference` means "a model I can run completions against". Pointing a
summarizer at it is harmless and the consumer has no reason to ask what is
behind it.

`agentApi` means "a thing with tools, memory, and often a terminal that happens
to speak OpenAI". Folding the two together would let a consumer that asked for a
model be handed an agent that can execute commands, and nothing in the metadata
would have said so.

Invariant 15 makes a new capability a new contract entry for exactly this
reason, and `agentApi` has no `SatisfiedBy`: standing `inference` or
`modelSource` into this slot would hand a consumer a raw model where it asked
for an agent, and it could not tell until it sent a tool call and got a
completion back.

## Adding a second surface to your app

1. Declare it under `extraPorts:` with a name, a port, and a path prefix.
2. Bind your contract offer to that name with `port:`.
3. Bind the service to loopback. Do not add a `ports:` entry for it.
4. If the credential is minted by your app rather than by Bloud, read it back
   and publish it with `SetAppSecret` rather than writing into a file your app
   owns and re-secures on every boot.

The loader will tell you if the pieces do not line up, and the tests in
`internal/catalog/agentapi_contract_test.go` and
`internal/traefikgen/extraport_test.go` cover the shape end to end.
