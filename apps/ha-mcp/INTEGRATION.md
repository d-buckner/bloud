# ha-mcp Integration

## Status: Working (live-verified)

The catalog ID is `ha-mcp`, named after the upstream project it runs:
[`homeassistant-ai/ha-mcp`](https://github.com/homeassistant-ai/ha-mcp), image
`ghcr.io/homeassistant-ai/ha-mcp:8.7.0`.

It exposes Home Assistant to an MCP harness as a tool namespace: entity and
device search, state reads, automation, script, scene and dashboard authoring,
service calls. Hermes registers it as one namespace, the same way it registers
`dav-mcp`, `arr-mcp`, and `affine-mcp`.

The app is two containers:

| Node | Image | Publishes | Owns |
|---|---|---|---|
| `apps-ha-mcp` | `ghcr.io/homeassistant-ai/ha-mcp:8.7.0` | nothing | the MCP server |
| `apps-ha-mcp-gate` | `docker.io/library/nginx:1.31-alpine` | 8086 | the bearer check, and the only door |

Each node has its own configurator, the way Radicale's pimsync sidecar does: a
node's config is written by the pass that owns the container reading it.

## Why a wrapper at all

Home Assistant has a built-in `mcp_server` integration. It was not enough: its
tool surface is the Assist surface (query state, call a service), and the reason
to put an agent in front of a home server is to have it author automations,
helpers, and dashboards, which is what `ha-mcp` does and what the built-in does
not. Upstream also ships an in-process custom component form; that would mean
Bloud generating a config entry and installing a component into Home Assistant's
`custom_components/`, which is Home Assistant's own upgrade surface to own.

So the target is untouched. `ha-mcp` talks to Home Assistant over its public REST
and WebSocket APIs with a token, needs no plugin, and does not care whether
Home Assistant was restarted or upgraded.

## The credential: a token Home Assistant minted

`ha-mcp` needs `HOMEASSISTANT_URL` and `HOMEASSISTANT_TOKEN`. Both come from
Home Assistant's own `appApi` offer, and the token is a long-lived access token.
Not the owner password, and not a Bloud invention: it is a credential Home
Assistant issued and Home Assistant validates, and it is revocable inside Home
Assistant. Who mints it is the only part that depends on whose install it is. If
Bloud runs Home Assistant, the configurator mints it for the owner account over
Home Assistant's WebSocket API; if the operator registered a Home Assistant
Bloud does not run, they minted it on that instance's own profile page and typed
it into the registration form. The mint, the reuse rule, and the revocation path
are documented in
[`../homeassistant/INTEGRATION.md`](../homeassistant/INTEGRATION.md#companion-token-appapi),
and the remote case in
[When the install is not Bloud's](../homeassistant/INTEGRATION.md#when-the-install-is-not-blouds).

The binding arrives through the `appApi` contract with `requires: [password]`.
The contract's secret is named `password` and carries a token here; that is the
one place the two vocabularies meet, and the contract's wording now says what it
means: a credential the provider's own sign-in validates.

The wrapper dials `binding.BaseURL` and asks nothing else about where the
provider lives. For a Home Assistant Bloud booted that field is the **container**
address (`http://apps-homeassistant:8123`): this is a server-side call, and the
container address is what `apps-net` DNS answers. For a registered remote
instance the resolver puts the operator's endpoint in that same field, so the env
file carries that origin instead. The wrapper needs no change between the two and
has no branch on provider kind: the gate, the bearer, and the readiness probe are
all about the harness on this side, not about which Home Assistant is on the
other.

## The gate, and why the bearer has to be real

`ha-mcp`'s standard mode has no notion of a bearer. Its own
`transport_security.py` says so out loud: "Our security boundary is the
high-entropy secret path", and it disables fastmcp's Host/Origin guard on the
grounds that it is designed to sit behind operator-chosen proxies. That is a
coherent posture for a tool a person runs on their own laptop.

It is not what Bloud promises. The `mcp` contract requires a provider to publish
`httpToken`, and Hermes drops any binding whose token is empty and sends
`Authorization: Bearer <token>` on every call. So the promise has to be kept
somewhere, and the image is not that somewhere.

The gate is a nginx front that does exactly two things:

```nginx
location = /mcp {
    if ($http_authorization != "Bearer <published bearer>") { return 401; }
    ...proxy to apps-ha-mcp:8086 at the internal path...
}
location / { return 404; }
```

Default-deny: one exact-match location for the MCP endpoint, one for the container
health check, and everything else 404. The gate is also the container that
publishes the app's port, because Traefik's generated service is
`http://localhost:<app port>` and there is exactly one such address per app.
Nothing can reach the server around the gate: it declares no `ports:` at all.

nginx rather than a purpose-built proxy because Bloud ships no images of its own,
and this is the smallest upstream artifact that compares one header and proxies
one path.

### Two secrets, not one

| | published | who sees it | what it protects |
|---|---|---|---|
| `httpToken` | yes, via the `mcp` contract | every consumer | the endpoint |
| `endpointPath` | no | this app only | the server, if the gate were ever bypassed |

The public path is the fixed `/mcp` and carries no secrecy: the bearer is the
credential. The server listens on a generated high-entropy path that only the
gate knows, which keeps upstream's own security model intact instead of
replacing it. The two never have to be the same value, and the value upstream
treats as its credential is never handed to a consumer.

### The DNS problem the gate would otherwise have

nginx resolves a literal `proxy_pass` hostname **once, at config load**, and never
again. Measured on podman: recreate the server container, its IP moves, and the
gate answers 502 forever after, because it is still dialling the address it read
at startup.

The fix is a variable upstream plus a `resolver`, which makes nginx re-resolve on
a timer (`valid=10s`) instead of pinning. The resolver address is only knowable
from inside the container: podman puts a network's DNS at that network's gateway,
and the gateway depends on how many networks already exist. So the config carries
a `@RESOLVER@` placeholder and the container's entrypoint substitutes it from its
own `/etc/resolv.conf` before starting nginx:

```sh
sed "s|@RESOLVER@|$(awk '/^nameserver/{print $2; exit}' /etc/resolv.conf)|" \
  /etc/nginx/ha-mcp-gate.conf.in > /tmp/nginx.conf
exec nginx -g 'daemon off;' -c /tmp/nginx.conf
```

Verified live: with the placeholder, a server recreate (IP `10.89.2.4` to
`10.89.2.7`) self-heals within the resolver's window; without it, the same
recreate leaves the endpoint dead until the gate is restarted too.

The placeholder also means the gate no longer needs the server's name to resolve
at its own startup, so the two containers cannot deadlock on ordering. The
`dependsOn` stays: the server node's PreStart is what publishes the internal path
the gate's config needs.

## The generated env file

`ha-mcp` reads its configuration from process environment and has no config file.
Three values cannot be rendered into the container spec because they are resolved
bindings or generated secrets, so the configurator writes
`{{appDataDir}}/config/env` and the container declares it as `envFile`:

- `HOMEASSISTANT_URL`: the address from the `appApi` binding, which is the
  container address for a Home Assistant Bloud runs and the registered endpoint
  for a remote one
- `HOMEASSISTANT_TOKEN`: the long-lived token from that binding's secret
- `MCP_SECRET_PATH`: the internal high-entropy path this app mints

Only values that resolved are written. An unresolved binding is an absent
variable, not an empty one: `HOMEASSISTANT_TOKEN=''` would be a credential the
server tries, where an absent one is a configuration error it reports.

Static settings live in `environment:` where they can be read. Two are worth
calling out:

- `HA_MCP_DISABLE_SETTINGS_UI=1`. The settings UI is a browser page for a human
  to paste a token into and flip tool switches. Bloud configures this container
  through env and the contract, so there is nothing for a visitor to do there,
  and every route it adds is one more to reason about through the gate.
- `MCP_HEALTHZ=1`. Opt-in, unauthenticated `/healthz`. Standard mode leaves it off
  because an always-on route tells a scanner the server is listening; inside a
  container that publishes no port, it is exactly the liveness probe the health
  check wants. The image has no shell, no wget, and no curl, so the health check
  runs the interpreter that is there: `/app/.venv/bin/python -c ...`.

There is **no volume mounted**. The image runs as uid 999 (`mcpuser`) with no
`PUID`/`PGID` knob, and Bloud creates mount directories as the runtime user, so a
bind mount would be a write-permission fight for state that does not matter: with
the settings UI off and no policy persistence enabled, there is nothing worth
keeping across a recreate. Anything that matters is regenerated from the contract.

## Readiness: the probe is the composed path

Two containers, and each one's own health check proves only itself: the gate's
says nginx is up, the server's says the process is up. Neither says the pair
works.

So `apps-ha-mcp-gate`'s `PostStart` POSTs a real MCP `initialize` to the published
`/mcp`, through the gate, carrying the published bearer, and requires the
server's own identity back. Presenting the credential is part of what the probe
proves: an unauthenticated probe would report a healthy node that every consumer
is about to be refused by. A 502 fails it too, which is the case where the gate is
up and cannot reach the server.

The server node's `PostStart` is deliberately empty: it publishes no port, so
there is nothing this process can dial, and the gate has not started yet when that
phase runs. Ordering is what makes the gate's probe meaningful, and the graph
provides it: the gate depends on the server, so by the time the gate's `PostStart`
runs, both containers exist.

## Trade-offs

- **The token is an owner token on a Bloud-booted install.** Whatever the agent
  does through these tools, it does as the Home Assistant administrator. A remote
  install is the operator's choice of account, so the same caveat is theirs. A
  dedicated service account is the right follow-up.
- **`READ_ONLY_MODE=0`.** Write-capable tools are on, because an agent that can
  only describe an automation is not worth the install. Flipping it is one metadata
  line, and the server then refuses writes with a `READ_ONLY_MODE` error rather
  than half-working. `REDACT_SECRETS=1` is on: tool output goes into a model's
  transcript, and a Home Assistant config can hold a key.
- **The gate is a second image to pin.** nginx moves slowly and the config is 20
  lines, but it is one more supply chain entry, and its health depends on a
  placeholder substitution happening in its entrypoint.
- **The bearer comparison is an exact string match in nginx.** That is fine for a
  base64url token and is the reason `randomToken` uses that alphabet: no quoting
  concerns in nginx or in the env file, and the renderer refuses anything else
  rather than escaping it.
- **Streamable HTTP is held open.** `proxy_buffering off` and a 3600s read timeout
  are load-bearing: with buffering on, a streaming response is held until the
  upstream finishes, which is never.

## Verification

Live against the native dev stack (`./bloud dev --reset`), installed through the
host-agent API (the real dependency-graph path). One run, one machine.

**Install.** Home Assistant first, then `ha-mcp`. Both nodes reached `RUNNING`;
the gate published `:8086` and the server published nothing.

**The credential.** After one HA pass, `secrets.json` carried
`appSecrets/homeassistant/published/password` (183 chars, a token HA minted), and
HA's own `.storage/auth` listed one `long_lived_access_token` named
`Bloud ha-mcp`. That token answers `GET /api/config` 200 and `GET /api/states`
with the entity list; the same token with one character changed answers 401, which
is the "gone" signal the reuse rule reads.

The first pass failed with `POST /auth/login_flow → 500`, and the reason is worth
keeping: the handler field is a `[type, id]` pair and the built-in provider's id is
`null`, not its type name. `("homeassistant", "homeassistant")` is an unknown
provider and HA raises a `KeyError` out of the view.

**The gate.**

| Request | Result |
|---|---|
| `POST /mcp`, no bearer | 401 |
| `POST /mcp`, wrong bearer | 401 |
| `POST /mcp`, bearer, `initialize` | 200, `serverInfo.name: "ha-mcp"`, `version: "8.6.0"` |
| `tools/list` | 77 tools |
| `tools/call ha_get_state zone.home` | real state (`radius: 100`, `friendly_name: Home`) in 28 ms |
| `GET /healthz` | 200 |
| `GET /`, `GET /api/states` | 404 (default-deny) |
| same set through Traefik on `:8080` | identical |

**Nothing reaches the server around the gate.** The internal path appears in
exactly one file on the host, the app's own env file, and in no published secret.
`provides.mcp.secrets` names only `httpToken`, and the `mcp` contract lists only
`httpToken`, so no consumer can even ask for the other one; a test asserts the
metadata never names it.

**A server recreate self-heals.** `podman rm -f apps-ha-mcp`; the reconciler
recreated it at a new address (`10.89.1.239`) about a minute later. The gate had
not restarted (`RestartCount: 0` at that point) and served the next `initialize`
in 19 ms. Its own access log is the evidence, and it is on for exactly this
reason:

```
10.89.1.238 - - [12/Oct/2026:22:02:11 +0000] "POST /mcp HTTP/1.1" 200 2507   <- PostStart probe, after the recreate
10.89.1.238 - - [12/Oct/2026:22:03:38 +0000] "POST /mcp HTTP/1.1" 401 179    <- no bearer
10.89.1.238 - - [12/Oct/2026:22:03:38 +0000] "GET / HTTP/1.1" 404 153        <- default deny
```

No 502 anywhere in it. This is the runtime-DNS fix doing its job: a static
`proxy_pass` fails this case permanently, and the failure looks like a dead app
rather than a moved container.

**Steady state is a read.** A resync pass ran `PreStart` with
`restart_needed: false` and the PostStart probe logged the handshake; no container
was disturbed.

**Uninstall.** `ha-mcp` uninstall removed both containers and left the data dir
and the published bearer behind, which is what `clearData: false` means here and
what every other app does. Reinstalling reused the same bearer without a
re-mint.

**Revocation.** Uninstalling *Home Assistant* (the provider owns the credential)
removed the `Bloud ha-mcp` token from `.storage/auth` and left the three
system/normal tokens alone.

**Provider removed under the consumer.** With `ha-mcp` installed and Home
Assistant uninstalled, the env file dropped both `HOMEASSISTANT_*` keys, the
server exited 1 (no configuration), and the node went to `ERROR` while the gate
held the app up. Reinstalling brought it back with the new token. `ERROR` is
terminal by engine design (`levels.go`: "never retry without an explicit status
reset"), so this is the honest shape: a consumer whose provider was taken away
says so until the operator acts, rather than pretending to serve.

**The remote provider path is unit-tested, not live-verified.** Registering
someone else's Home Assistant and reading the env file that lands is pinned by
`services/host-agent/internal/engine/orchestrator/external_app_homeassistant_seam_test.go`:
the real resolver plus the real `ha-mcp` configurator against one shared secrets
store, asserting the file carries the registered endpoint and the operator's
token. No test reaches a Home Assistant over a network.

## Icon

selfh.st/icons has no ha-mcp entry, and the app is `headless`, so there is no tile
for an icon to appear on. The app ships none, like the other MCP wrappers.

## Validation strategy

Headless with `sso.strategy` of `none`: the app authenticates to its target with
a token rather than through the identity provider, so nothing here is exercised
from a browser.
