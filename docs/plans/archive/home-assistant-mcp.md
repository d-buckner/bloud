> Status: Implemented and live-verified; archived 2026-10-15. Two details of this design did not
> survive contact with Home Assistant, both corrected below and explained in
> [`apps/ha-mcp/INTEGRATION.md`](../../../apps/ha-mcp/INTEGRATION.md) and
> [`apps/homeassistant/INTEGRATION.md`](../../../apps/homeassistant/INTEGRATION.md).

# Plan: `apps/ha-mcp`, a Home Assistant MCP provider

**Last updated:** 2026-10-12

> Issue: [d-buckner/bloud#223](https://github.com/d-buckner/bloud/issues/223),
> "Add home assistant mcp", pointing at
> [homeassistant-ai/ha-mcp](https://github.com/homeassistant-ai/ha-mcp).
> The contract model lives in [features/mcp.md](../../features/mcp.md); this file
> is the design for one new provider.

## The shape

One new app, no browser UI, and two containers: the MCP server, and a small gate
in front of it:

```
homeassistant (provides appApi)  ──►  ha-mcp (provides mcp)  ──►  hermes (integrates mcp)
```

`apps/ha-mcp` is the same wrapper shape as `apps/dav-mcp`: a headless app that
serves streamable HTTP on one port, spends a provider credential to authenticate
to its target, generates the bearer its own listener accepts, and publishes it
under the `mcp` contract. Hermes picks it up by adding one `compatible:` entry.
The orchestrator, the contract resolver, and Hermes' own configurator stay as
they are: the work is in `apps/`, plus `validation.yaml` and one comment on the
`appApi` contract.

Two problems the wrapper has to solve, in order of how much they cost:

1. ha-mcp needs a Home Assistant **long-lived access token**, and Bloud has to
   mint it without a human in the loop.
2. ha-mcp's HTTP mode does not accept a bearer token. Its gate is a secret URL
   path. Bloud's `mcp` contract publishes a bearer, and Hermes sends
   `Authorization: Bearer <token>`.

## Verified facts about ha-mcp

All from the upstream repository and docs site, checked 2026-10-12.

| Fact | Value | Source |
| --- | --- | --- |
| Docker image | `ghcr.io/homeassistant-ai/ha-mcp` | README, Docker mode |
| HTTP entrypoint | `ha-mcp-web` (stdio is `ha-mcp`) | Docker mode docs |
| Default HTTP port | `8086` (`MCP_PORT`) | Docker mode docs |
| Target address | `HOMEASSISTANT_URL` (required) | Docker mode docs |
| Target credential | `HOMEASSISTANT_TOKEN`, a long-lived access token | Docker mode docs |
| Public URL of this server | `MCP_BASE_URL` (required) | Docker mode docs |
| Client gate | `MCP_SECRET_PATH`, default `/mcp`; "authenticates by URL-path secrecy" | Docker mode docs, SECURITY.md |
| Settings UI | served at `<secret path>/settings`; `HA_MCP_DISABLE_SETTINGS_UI=1` turns it off | Docker mode docs |
| Host/origin guard | `MCP_HTTP_ALLOWED_HOSTS`, `MCP_HTTP_ALLOWED_ORIGINS`, `MCP_HTTP_HOST_ORIGIN_PROTECTION` | Docker mode docs |
| Bind address | `MCP_HOST`, default `0.0.0.0` | Docker mode docs |
| Persistent state | `/home/mcpuser/.ha-mcp` | Docker mode docs |
| Read-only posture | `READ_ONLY_MODE` feature flag, off by default | settings reference |
| Latest release | `v8.6.0` | GitHub releases |

Two things the search did not settle, both cheap to check with a container:
whether the image exposes an unauthenticated health path, and the uid the volume
must be writable by. See "Verify before implementing".

## What upstream would rather you do

Upstream's recommended install is the **HA-MCP Custom Component**: the full
server runs in-process inside Home Assistant, provisions its own admin token, and
publishes a webhook URL as the credential. It needs HACS or a manual
`custom_components/` copy plus a config-entry flow, and its connect URL is
generated at runtime and only readable on admin surfaces.

Bloud is not doing that here. The in-process server is a per-instance manual
setup whose credential is minted inside the target app, which is the opposite of
what Bloud's contract model needs: a provider Bloud installs, configures, and
reconciles. The Docker mode is the supported shape for a server outside Home
Assistant, and it is the one the issue links to.

## Decision 1: the target credential is a long-lived token Bloud mints

Home Assistant has no REST endpoint to create a long-lived access token. The
documented paths are the user profile page and the WebSocket command
`auth/long_lived_access_token`
([auth API](https://developers.home-assistant.io/docs/auth_api/), updated
2026-08-19). So Bloud has to drive the WebSocket command.

The mint belongs to `apps/homeassistant`, not to the wrapper:

1. `homeassistant` adds a `provides: appApi` offer: `username` as the value, and
   as the secret a **long-lived access token** minted for a named client
   (`Bloud ha-mcp`), not the break-glass owner password.
2. The mint runs in `homeassistant`'s `PostStart`, on demand and once: log in
   with the durable owner password through the existing `appclient`
   (`POST /auth/login_flow`, `POST /auth/login_flow/{flow_id}`, `POST /auth/token`
   are already the onboarding path), open `GET /api/websocket` with the resulting
   short-lived token, send `auth/long_lived_access_token`, store the returned
   string with `SetAppSecret`.
3. Every later pass is a read: verify the stored token with one authenticated
   `GET /api/config`, re-mint only on 401. Not `GET /api/`: Home Assistant's auth
   middleware marks a bad bearer as unauthenticated and answers 200 on a view that
   does not require auth, so only an auth-required view turns "gone" into a 401.
   Home Assistant lists long-lived tokens per user, so a mint on every resync
   would litter the owner's profile and trip invariant 2's resync-cost watch for
   no reason. (A pass that could not reach Home Assistant at all is inconclusive
   and keeps the stored token, for the same reason.)
4. `Remove` revokes the token over the WebSocket API: `auth/refresh_tokens` to
   list the account's tokens, `auth/delete_refresh_token` to delete the one whose
   `client_name` is this integration. Not `POST /auth/token` with `action:
   revoke`: that endpoint matches a refresh token, and a long-lived access token
   has none.

Why the provider mints rather than handing the wrapper the owner password:

- The break-glass password stays inside Home Assistant's own data tree. Invariant
  15 says an app never reads another app's state; publishing the raw password
  would put a credential that can log in as the owner into a second app's env
  file, where a long-lived token that can be revoked cannot.
- The wrapper stays a thin wrapper: it writes an env file and starts a container.
  The WebSocket client lands in one app that already owns Home Assistant's auth,
  instead of in every future consumer.

The first cost is one sentence of contract wording. `appApi`'s secret is documented as
"the account's real password, not a Bloud invention", because AFFiNE removed its
programmatic tokens. Home Assistant has programmatic tokens, and a long-lived
access token is a credential Home Assistant's own sign-in validates, which is the
rule the wording is protecting. Amend the comment in
`services/host-agent/internal/catalog/contracts.go` to say the secret is the
credential the provider's sign-in validates, and name Home Assistant as the case
where that is a token rather than a password.

The second cost is eagerness. A provider publishes whether or not a consumer is
installed, so a Home Assistant install with no ha-mcp carries one long-lived
token named for a companion that never arrived. It is revocable, it is inert, and
it is one row on the owner's profile.

Fallback, and the answer if that stray token matters: publish the owner password
as `appApi.password` and let `apps/ha-mcp` do the mint. The mint then happens
exactly when a consumer exists. The price is one more place the break-glass
password appears, and a WebSocket client in the wrapper instead of in the app that
owns Home Assistant's auth.

The account is the existing `bloud-bootstrap-admin` owner. ha-mcp's automation,
dashboard, and config tools need admin, and Bloud already documents that owner as
the break-glass account it cannot delete. A dedicated service account is a
follow-up, not a v1 requirement.

## Decision 2: a bearer gate in front of a server that has no bearer

The `mcp` contract requires a provider to publish `httpToken`
(`validateContractSecrets` in
`services/host-agent/internal/catalog/loader.go`), and Hermes drops a binding
with an empty token and always writes a bearer header. ha-mcp ignores an
`Authorization` header entirely. Publishing a token the endpoint does not check
would be the one thing the contract exists to prevent: a credential that
authenticates nothing.

So the wrapper ships a second container that does check it:

```
hermes ──bearer──► apps-ha-mcp-gate (nginx, published :8086)
                        │  one location: /mcp
                        ▼
                   apps-ha-mcp (ha-mcp-web, :8086, unpublished, secret path)
```

- `apps-ha-mcp-gate` is a pinned `docker.io/library/nginx` (pin the current
  alpine release at implementation time). Its config is generated by the
  configurator with `managedfile.Write`, like every other config Bloud writes.
- The gate is default-deny: `location = /mcp` returns 401 unless `Authorization`
  equals the published bearer exactly, then proxies to
  `http://apps-ha-mcp:8086/<internal secret path>`; `location = /healthz` answers
  the container health check; everything else is 404, because a path that does not
  exist should not advertise that a gate is behind it. `proxy_buffering off` and a
  long read timeout, because streamable HTTP is a long-lived response.
- The upstream is a **variable with a `resolver`**, not a literal hostname. nginx
  resolves a literal `proxy_pass` host once, at config load, and never again: a
  server container recreated at a new address leaves the gate answering 502 forever
  (measured on podman). `set $upstream apps-ha-mcp; proxy_pass http://$upstream:8086;`
  with `resolver <podman's DNS> valid=10s` re-resolves on a timer instead. The
  resolver address is only knowable from inside the container, so the config
  carries a `@RESOLVER@` placeholder that the container's entrypoint substitutes
  from its own `/etc/resolv.conf` before starting nginx.
- The `ha-mcp` container declares no `ports:` entry, so it is reachable only on
  `apps-net`, and its own `MCP_SECRET_PATH` is a random value Bloud generates and
  never publishes. Two independent secrets: the bearer for the outside, the path
  for the inside. The path is stored through `SetAppSecret` like the bearer, and
  what keeps it private is that no contract declares it: a consumer cannot require
  a secret the contract does not list.
- The app's `port:` is the gate's published port, so Traefik routes
  `ha-mcp.<host>` at the gate and the dashboard's derived URL is the gated one.
  This is not a style choice: Traefik's generated service URL is
  `http://localhost:<app.Port>` (`writeService` in
  `services/host-agent/internal/traefikgen/generator.go`), so the app's `port:`
  is the host-published port, exactly one container may publish it, and a route
  that skipped the gate would reach the ungated server.
- `dependsOn` orders the gate behind the server. The gate's container healthcheck
  is `wget` on `/healthz` (the alpine image has busybox `wget` and no `curl`):
  liveness means nginx is up and serving, nothing more. "Is it a gate, and does the
  thing behind it speak MCP" is the node's `PostStart`, which POSTs a real
  `initialize` to `/mcp` carrying the published bearer and requires the server's
  identity back. Splitting them that way is what makes a 502 visible: a healthcheck
  that only asks whether nginx is alive cannot see a gate that cannot reach its
  server.

The alternative is to change the contract instead: let a provider declare that it
has no bearer, publish the secret path as the `path` value, and teach Hermes to
omit the `Authorization` header for such a provider. That is honest about what
ha-mcp is, and it is a smaller container graph. It is also a change to a core
contract (`Contract.Secrets` has no notion of optionality today), to Hermes, and
to the meaning of every existing `mcp` offer, for a property only this provider
wants. The gate keeps the change inside `apps/ha-mcp/`.

## Decision 3: headless, settings UI off

`headless: true`, like dav-mcp: installed, reconciled, and routed, but no tile,
because there is no page to open. Set `HA_MCP_DISABLE_SETTINGS_UI=1` in v1 and
proxy only the MCP endpoint. The settings UI is where tool enable/disable,
feature flags, and backups live, and it is a second secret path with its own
session model; env feature flags cover the settings Bloud needs to set. If a
tile is wanted later, the gate already has the shape to serve it.

## metadata sketch

```yaml
name: ha-mcp
displayName: Home Assistant MCP
description: MCP tool server for Home Assistant, so an agent can control devices and manage automations
category: infrastructure
port: 8086
headless: true

integrations:
  proxy:
    required: true
    multi: false
    compatible:
      - app: traefik
        default: true
  appApi:
    required: true
    multi: false
    requires:
      - password # the long-lived access token Home Assistant minted
    compatible:
      - app: homeassistant
        default: true

provides:
  mcp:
    secrets: [httpToken]
    values:
      serverName: homeassistant
      path: /mcp # the gate's public path; the internal path stays private

containers:
  - name: apps-ha-mcp
    image: ghcr.io/homeassistant-ai/ha-mcp:v8.6.0 # verify the tag shape
    network: apps-net
    restartPolicy: always
    # no ports: only the gate is reachable from outside the network
    envFile: "{{appDataDir}}/config/env"
    volumes:
      - source: "{{appDataDir}}/ha-mcp"
        destination: /home/mcpuser/.ha-mcp
  - name: apps-ha-mcp-gate
    image: docker.io/library/nginx:<pinned>-alpine
    network: apps-net
    restartPolicy: always
    dependsOn: [apps-ha-mcp]
    ports:
      - host: 8086
        container: 8086
```

The public path is static because the bearer is the credential the contract
promises. The `MCP_SECRET_PATH` the gate proxies to is random and never published,
so it is Bloud's own state, not a contract value.

## Configurator responsibilities

`PreStart` (idempotent, runs on every pass including resyncs):

- Read the `appApi` binding. An empty token means Home Assistant has not
  published yet: fail the pass, never write an empty credential.
- Render `{{appDataDir}}/config/env` with `HOMEASSISTANT_URL` from the binding's
  `LocalURL`, `HOMEASSISTANT_TOKEN`, `MCP_BASE_URL` from the app's own address,
  `MCP_PORT=8086`, `MCP_SECRET_PATH` from the generated internal secret,
  `HA_MCP_DISABLE_SETTINGS_UI=1`, and the `MCP_HTTP_ALLOWED_HOSTS` entry the
  container-name dial needs. `managedfile.Write` reports `changed=false` when the
  bytes match, so a steady-state pass touches nothing.
- Render the gate's nginx config the same way.
- Generate and publish `httpToken` once, through the secrets provider, the way
  affine-mcp and dav-mcp do.

`PostStart`: the real gate is an MCP `initialize` handshake through the gate with
the bearer, asserting a tool list comes back. That is the behavioral assertion
dav-mcp makes, and it is the one that can fail loudly without flapping a
container. A container-level liveness probe does the dumb job only.

`Remove`: nothing Home Assistant-side to undo (the token belongs to
`homeassistant`), so the wrapper needs no `Remover` unless the internal secret
needs cleanup, which the data wipe covers.

## Wiring and docs

- `apps/homeassistant/metadata.yaml`: add the `provides: appApi` offer, and the
  mint in its configurator. Its `INTEGRATION.md` gains the token lifecycle.
- `apps/hermes/metadata.yaml`: add `ha-mcp` to the `mcp` contract's
  `compatible:` list. No configurator change: Hermes already renders one
  `mcp_servers` entry per binding.
- `apps/registry.go` plus `apps/ha-mcp/registration.go`: node name and blank
  import, kept in step by `TestRegisterAll`.
- `apps/ha-mcp/icon.png`: ha-mcp is not in the selfh.st set, so take the upstream
  logo and record its provenance in `apps/ha-mcp/INTEGRATION.md`.
- `validation.yaml`: an `apps:` entry (auth strategy `none`, globs, no
  `e2e-project` unless a browser journey exists).
- Generated docs: `./bloud depgraph --write` and `./bloud catalogdoc --write`
  refresh the README blocks; `docs/features/mcp.md` gains ha-mcp in the provider
  list.

## Security posture to state in `INTEGRATION.md`

- The bearer is instance-level: every agent using this endpoint acts as the same
  Home Assistant admin. That is the boundary `docs/features/mcp.md` already
  states as a non-goal; ha-mcp makes it concrete, since the tool set includes
  automation and config writes.
- The published port is on `0.0.0.0` of the Bloud host, like every app today. The
  gate is what makes that survivable. Loopback-only publishing is already tracked
  as Phase 4 of `docs/plans/mcp-integrations.md`.
- `READ_ONLY_MODE` exists upstream and is off by default. Leave it off: an agent
  that can only read Home Assistant is not worth a container. Document it as the
  opt-in for a household that wants a read-only agent.
- The internal secret path and the bearer are both in
  `{{appDataDir}}/config/env`, which is the app's own tree and is backed up with
  it. Nothing secret goes on a command line or into a container `environment:`
  value that the dashboard can read.

## Verify before implementing

Each of these is one container run, not a research task:

1. The exact ghcr tag for a release (`v8.6.0` vs `8.6.0`), and that `ha-mcp-web`
   is the entrypoint name in that image.
2. Whether any path answers unauthenticated, for the container healthcheck. If
   none does, use a TCP probe the way `apps/homeassistant` does with its own
   python3.
3. The uid that owns `/home/mcpuser/.ha-mcp`, and whether the Bloud-managed
   volume mount needs an ownership fix.
4. Whether `MCP_HTTP_HOST_ORIGIN_PROTECTION` rejects a dial to
   `http://apps-ha-mcp:8086`, and which `MCP_HTTP_ALLOWED_HOSTS` value it wants.
5. `auth/long_lived_access_token` parameter name and unit in the pinned Home
   Assistant release (`lifespan` in days vs `lifespan_minutes`), and whether the
   WebSocket login needs the same built-in client id the onboarding flow uses.
6. How many long-lived tokens one Home Assistant user may hold, and that revoking
   through `POST /auth/token` works with a long-lived token.

## Tests

- `apps/ha-mcp/configurator_test.go`: env file and nginx config render, the
  no-token case fails the pass, a second pass writes nothing (`changed=false`),
  and the published `httpToken` survives a resync unchanged.
- `apps/homeassistant`: the mint is cached (a second pass makes no WebSocket
  call), a 401 re-mints, and the token is revoked on removal.
- `apps/conformance_test.go`: a `{Dir: "ha-mcp", Node: "apps-ha-mcp",
  DefaultPort: 8086}` row, with a preseed for the published bearer so the harness
  reaches the config-writing path.
- `apps/configtest`: the declared wait budgets fit inside `AppPhaseBudget`.
- Integration (`services/host-agent/internal/e2e`): install Home Assistant,
  install ha-mcp through the real API, then assert behaviorally over the MCP
  endpoint: `initialize`, `tools/list`, and one read-only tool call that names a
  real Home Assistant entity. Assert through the protocol, not through config
  values.
- No Playwright spec in v1: there is no UI to drive.

## Sequencing

1. Spike the container: one `docker run` with a hand-made long-lived token, to
   close items 1 to 4 above.
2. `apps/homeassistant`: the mint, the cache, the revoke, and the `appApi` offer.
   Shippable on its own.
3. `apps/ha-mcp`: server container, env file, published `httpToken`.
4. The gate container and its generated config, plus the PostStart handshake
   through it.
5. Hermes `compatible:` entry, validation.yaml, generated docs, integration test.
