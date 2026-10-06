# dav-mcp Integration

## Status: Working (live-verified)

The catalog ID is `dav-mcp`, named after the upstream package it wraps:
[`PhilflowIO/dav-mcp`](https://github.com/PhilflowIO/dav-mcp), image
`ghcr.io/philflowio/dav-mcp:4.1.2`.

It exposes 27 MCP tools over the CalDAV and CardDAV that Radicale already
serves: calendars, events, to-dos, contacts, address books, free/busy queries.
Hermes registers it as one tool namespace.

## Why this replaced the previous wrapper

This app is not a rename of the old `caldav-mcp` entry, it is a different app
that took its place. The old entry is gone from the tree, not carried forward
under a new name: `caldav-mcp` names the npm package that was removed, and a
catalog entry called `apps-caldav-mcp` running `dav-mcp` is a name that points
somewhere the bytes are not.

The old shape was `supergateway` bridging the npm package `caldav-mcp@0.10.0`
from stdio to HTTP. It never converged: the node sat in `error` with

```
waiting for the caldav-mcp server: caldav-mcp: wait POST /mcp did not become
ready after 7 attempts (last error: ... read body: context deadline exceeded)
```

The cause was not the bridge. Piping stdio straight into the wrapped server
still took **24.4s** to answer `initialize`, because `caldav-mcp@0.10.0` runs
`CalDAVClient.create()` (14.95s) and `getCalendars()` (3.5s) *before* it
connects the transport. Bloud's `appclient` default per-request timeout is
15s, so the readiness probe could never win. The underlying LDAP cost is
tracked separately in
[#237](https://github.com/d-buckner/bloud/issues/237); this port only had to
stop making the MCP handshake depend on it.

`dav-mcp` binds its port first and does its DAV login during startup, so the
handshake is decoupled from the round trip:

| | old (bridged `caldav-mcp@0.10.0`) | `dav-mcp:4.1.2` |
|---|---|---|
| container start to `/health` 200 | n/a | ~18s |
| MCP `initialize` | 24.4s (never under 15s) | **0.003s** |
| `tools/list` | 10 tools | **27 tools, 0.003s** |
| transport | stdio bridged by a second process | native streamable HTTP |
| auth on the MCP endpoint | none | bearer required |
| unreachable DAV server | starts anyway | exits 1 in under 8s |

Real tool calls still pay the LDAP cost, which is the honest number:
`list_calendars` 6.6s, `list_addressbooks` 3.2s. What changed is that the
node converges and the tools work, instead of never coming up.

## The generated env file

`dav-mcp` reads its whole configuration from `process.env` and has no config
file. Three of the values it needs cannot be rendered into the container spec,
because they are resolved bindings rather than static metadata:

- `CALDAV_SERVER_URL`: the DAV root, from the `caldav` contract
  (provider container address plus the provider's declared path)
- `CALDAV_USERNAME` / `CALDAV_PASSWORD`: the service account, from Radicale's
  `appApi` offer
- `BEARER_TOKEN`: the MCP credential this app generates and publishes under
  the `mcp` contract

So the configurator writes `{{appDataDir}}/config/env` and the container
declares it as `envFile`. The runtime reads that file on the host and merges it
into the podman config at create time; the file is not mounted into the
container. File values override `environment:`, which is what lets a resolved
value win over a declared default.

Only values that actually resolved are written. An unresolved binding is an
absent variable, not an empty one: `CALDAV_PASSWORD=''` would tell the server
it was configured with a blank credential, which is a different and less
true statement than "not configured yet". A later pass rewrites the file and
restarts the container once the binding lands.

A value containing a quote or a line break is omitted with a comment naming the
key and never the value, because one of these keys is a password and the file
sits on disk. Every value here is generated (base64url) or resolved from
catalog metadata, so none should ever contain one; failing quietly and safely
beats writing a file whose quoting no longer means what it says.

## Why a service account

The wrapper is not a browser and cannot join the identity provider. It
authenticates as `caldav-service`, the machine credential Radicale provisions
and publishes under `appApi`, the same way `apps/affine-mcp` consumes AFFiNE's.

The consequence is worth stating plainly: the tools see the **service
account's** collections, not the operator's personal ones. Live, `list_calendars`
returns

```
### 1. Family
- Components: VTODO, VEVENT, VJOURNAL
- URL: http://apps-radicale:5232/caldav-service/family/
```

That is the service account's own tree. An agent using these tools operates on
shared data by design, not on whatever calendar the human happens to be
looking at.

## Why the readiness probe is the MCP handshake

The container's own `/health` returns 200 whenever the process is up, whether
or not the DAV side works. Promoting a node on that would report a healthy
app that cannot serve a single MCP request.

So `PostStart` POSTs a real `initialize` to the same `/mcp` path a harness
calls and requires the server's own identity back. A status code cannot answer
this: a server that is up but cannot reach its backend answers 200 and puts
the failure in the JSON-RPC envelope as an `error`, so only reading the
envelope separates "serving MCP" from "process up, server broken".

**The probe must present the bearer.** This is the bug the port actually caught
in a live install: the container came up healthy, logged into Radicale, and
published 27 tools, while every probe was refused with `401 Unauthorized:
Bearer token required`. The old bridge had no auth on its endpoint, so the
probe sent no credential. Against a server that enforces one, a probe that
does not authenticate reports a green node that every real consumer is about
to be refused by, which is worse than a red one.

The probe reads the current bearer from the secrets provider rather than
carrying one from `PreStart`, so a rotation landing between the two phases is
still picked up.

## Collection creation is denied by the rights backend

Writes to an existing collection work end to end. Verified live: `create_event`
wrote into `/caldav-service/family/` and the object read back straight from
Radicale with the service account's own credential.

Creating a *new* collection does not. Radicale runs
`[rights] type = owner_only`, and the service account is refused:

```
MKCOL request '/caldav-service/contacts/' (type:UNKNOWN):
rejected because of missing rights 'W'
```

So `make_calendar` and the contact-write tools are reachable but cannot
create the container they write into. That is a Radicale provisioning
characteristic, not a `dav-mcp` limitation: the tool surface is wider than
the account's rights. If the agent should be able to create address books,
the rights backend (or a Bloud-side provisioning step that creates one for
the service account) is the thing to change, not this app.

## Readiness budget

`PostStart` waits 30s, not the 120s the old shape used. The old budget existed
because the entrypoint installed an npm package before the gateway bound its
port. Nothing here does that: the port binds only after the DAV login, and the
container health check (30 retries x 5s) already covers that window. What is
left to wait for is a process that is up but not yet serving, not a cold start.

## Trade-offs

- **27 tools instead of 10** is a wider surface for the model to choose from.
  That is the point: the previous wrapper could not write contacts at all.
- **The bearer is a shared secret** in the host secrets store. Anyone with it
  can call the tools. It is not exposed in the browser and the port is not
  published beyond the host, but it is a credential with write access to the
  service account's calendars.
- **Real tool calls are slow** (3-7s) because of the LDAP bind cost in #237.
  Convergence is fast; individual calls are not.
- **`envFile` is new framework surface.** It exists because this image reads
  only `process.env` and three of its values are resolved bindings. The
  alternative was hardcoding `http://apps-radicale:5232/` in metadata, which
  duplicates what the `caldav` contract exists to resolve.
- **The etag round trip is fiddly.** `delete_event` returns 412 when handed
  the etag with its quotes stripped; Radicale's `If-Match` wants the quoted
  form the header carries. Callers should pass the etag exactly as returned.

## Verification

Live against the native dev stack, installed through the host-agent API (the
real dependency-graph path):

- install converged `planning -> topology -> health -> poststart -> complete`
  and the node reached `RUNNING`
- `initialize` over the published port: 200 in 4.5ms
- `tools/list`: 27 tools in 3.2ms
- `list_calendars`: 1 calendar (the service account's Family), 6.6s
- `create_event`: wrote `event-...@tsdav-mcp.ics`, then read back directly
  from Radicale over HTTP with the service account credential and confirmed
  `DTSTART:20261006T100000Z` and the description round-tripped intact
- unauthenticated `/mcp`: refused with `Bearer token required`
- test event deleted; confirmed 404 afterwards

## Icon

selfh.st/icons has no dav-mcp entry, so the app ships no icon and the
dashboard falls back to the letter avatar.
