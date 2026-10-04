# immich-mcp Integration

## Status: Shipped (unit + contract verified; no browser journey)

`immich-mcp` runs [ImmichMCP](https://github.com/barryw/ImmichMCP), a
third-party MCP server for Immich. It exposes 49 tools over Immich's REST API:
asset search and metadata, smart search (CLIP), albums, people, tags, shared
links, activities, uploads, and bulk mutations. Bloud wires it to an Immich API
key and publishes its MCP endpoint to the `mcp` contract, so a harness such as
Hermes can register it as a tool namespace.

- Images: `ghcr.io/barryw/immichmcp:v3.3.3` (the tool server) and
  `docker.io/library/caddy:2.11.6-alpine` (the authenticated edge)
- Published port: 9223 (the Caddy edge; the tool server publishes nothing)
- MCP endpoint: `http://<host>:9223/mcp`, bearer-authenticated
- SSO strategy: `none` (there is no user-facing login; a harness authenticates
  with the bearer Bloud generates, and the app authenticates to Immich with a
  key Immich minted)
- Dashboard: `headless: true`. The app serves an MCP endpoint and a liveness
  probe, so the dashboard draws no tile for it. It is otherwise ordinary: it
  stays in the catalog, in `GET /api/apps/installed`, and in the developer
  graph, and Bloud installs, reconciles, and routes it like every other app.

## Why there is an edge container

The upstream image serves `/mcp` with **no inbound authentication at all**. If
Bloud published that port it would be a full read-write Immich surface open to
anything that can reach the host, and the `mcp` contract's `httpToken` would be
a token nothing validated: exactly the "a token named token that authenticates
against nothing" failure
[`docs/features/mcp.md`](../../docs/features/mcp.md) forbids.

So the app runs two containers. `apps-immich-mcp-upstream` is the tool server,
on `apps-net`, with no published port. `apps-immich-mcp` is a Caddy reverse
proxy that owns the app's port, requires `Authorization: Bearer <httpToken>` on
every request but its own `/health`, and refuses the rest with 401. The token
Bloud publishes is therefore a credential a container this app runs actually
checks, and the upstream is unreachable from anywhere but the edge.

The liveness path is answered by Caddy itself rather than proxied. The upstream
`dependsOn` the edge (the edge's `PreStart` writes the upstream's config file),
so the proxy has to be able to become healthy while the tool server is still
down, or the first install would wait on a health check that waits on the
container it gates.

## Credential flow

Two credentials cross this app's boundary.

### Into Immich: the `appToken` contract

The wrapper is not a browser, so it cannot follow an SSO redirect into Immich.
Immich mints scoped API keys, so it publishes one through the `appToken`
contract instead of the account password `appApi` hands to a target with no
token API:

```yaml
# apps/immich/metadata.yaml
provides:
  appToken:
    secrets: [token]
```

`apps/affine`'s `appApi` exists because AFFiNE removed its token API and a
password is the only durable credential left; Immich has the better mechanism,
so it uses it. The difference is not cosmetic: the key is provider-enforced and
revocable, and the operator can see and delete it in Immich's own API-keys
screen.

The key is created with the `all` permission set. That is not a wider grant than
the account already has: an Immich API key cannot exceed the account that owns
it. Curating a smaller set would not reduce what the principal can reach, only
which of the account's own powers the companion may exercise, and the wrapper
exposes the full read-write tool surface while upstream adds tools between
releases, so a curated list would go stale as a silent 403.

The wrapper reads it from the resolved `appToken` binding (an `AppTokenBinding`
carries the key and the address) and writes it into the generated
`appsettings.json`. A required `default: true` provider turns the integration
into a graph edge, so Immich converges to `RUNNING` and publishes the key before
this app's `PreStart` runs.

**Which account the key belongs to, and the limit that implies.**
`apps/immich` mints the key for Bloud's internal bootstrap admin
(`bloud-admin@localhost`), because Immich offers no API to mint a key for
another account and the operator's account is an OIDC user whose password Bloud
never sees. An Immich key acts as its owner, and Immich's asset access is
owner / album / partner only, so **the wrapper's tools see the internal admin's
library, not the operator's**. On a Bloud instance the operator's photos belong
to their SSO account, which means the library tools act on an account that
starts empty.

The follow-up is to make the operator's SSO identity the admin account: Immich
links an OIDC login to an existing user with the same normalized email
(`auth.service.ts`, "link by email"), so giving the bootstrap admin the
operator's address would make the operator the account the key belongs to.
`configurator.Deps.OperatorEmail` already exists and AFFiNE uses it for exactly
this reason. That change is not made here because it alters the identity of an
existing install's admin account, which is a product decision and not this
app's to make.

### From the wrapper: the MCP bearer

Caddy authenticates the edge with a static shared secret. Bloud generates it on
the first pass, persists it in the secrets store, writes the same value into the
generated Caddyfile, and publishes it under `provides.mcp.secrets.httpToken`. A
harness receives it as `MCPBinding.Token` and sends it as `Authorization:
Bearer ...`.

As with `affine-mcp`, Bloud invents this token rather than the provider minting
it, and the rule still holds: it is a credential the provider validates, because
Bloud is the one configuring the listener that checks it.

## Generated configuration

`PreStart` writes two files into `{{appDataDir}}/config` and `metadata.yaml`
mounts each one where its container reads it. Everything else is static.

### `appsettings.json` → `/app/appsettings.json`

The upstream image reads `IMMICH_BASE_URL` / `IMMICH_API_KEY` from the
environment first, then `Immich:BaseUrl` / `Immich:ApiKey` from the
content-root `appsettings.json`. The credential is minted at runtime and a
container spec renders only static metadata, so the generated file is the only
delivery channel:

```json
{
  "Immich": {
    "BaseUrl": "http://apps-immich-server:2283",
    "ApiKey": "<minted by Immich>"
  }
}
```

An empty `ApiKey` is written as an empty string rather than omitted. The image
binds its options lazily and cannot validate the key at startup, so either form
lets the container come up and answer its liveness probe; the credential is
what `/health/ready` exercises, and a missing key fails that probe. A pass that
has not received a key therefore parks the node in ERROR rather than reporting
RUNNING with every tool call 401ing, and the self-healing pass re-drives it
once Immich has published.

### `Caddyfile` → `/etc/caddy/Caddyfile`

```caddyfile
{
	admin off
	auto_https off
}

:9223 {
	@bloud-health path /health
	handle @bloud-health {
		respond "ok" 200
	}
	@bloud-mcp header Authorization "Bearer <generated>"
	handle @bloud-mcp {
		reverse_proxy apps-immich-mcp-upstream:5000
	}
	handle {
		respond "unauthorized" 401
	}
}
```

When the app has no bearer at all (CLI/test contexts with no secrets store), the
`@bloud-mcp` block is omitted and only the liveness route remains, rather than a
literal empty bearer being written into a matcher.

### Static environment

| Variable | Value | Why |
|---|---|---|
| `MCP_PORT` | `5000` | The upstream's internal port, fixed by its image. |
| `MCP_LOG_LEVEL` | `Information` | The image's default. |
| `DOWNLOAD_MODE` | `url` | Tools return asset URLs rather than inline base64, which keeps large photos out of the model's context. |
| `IMMICH_TOOL_MODE` | `static` | Exposes all 49 tools up front. `gateway` exposes only `immich_tools_list` / `immich_tools_enable` and depends on the client acting on `notifications/tools/list_changed`; Hermes registers a namespace at startup and enables no categories, so gateway mode would hand it two meta-tools instead of the library tools. |

Both generated files use `ModeHostOnly` (0600). Both containers run as root
inside rootless podman, which is the host uid that wrote the file, so the
credential is readable where it is needed and by nobody else.

## Container graph

```
apps-immich-server  ──(appToken)──>  apps-immich-mcp  ──(mcp)──>  hermes
                                          │
                                          └─ apps-immich-mcp-upstream
```

The edge is the app's primary node (metadata lists it last), so the `mcp`
binding's `Node`, `Port`, and `BaseURL` all point at it. The upstream edge
`dependsOn` the primary because the primary's `PreStart` writes the upstream's
config; the proxy starting before its backend is the ordinary case and costs
nothing, while a backend started before its config exists exits at boot.

`apps-immich-mcp-upstream` joins `apps-net` so it resolves `apps-immich-server`
by container name. The edge also joins `apps-net`, which is where its upstream
lives.

## Health

- Edge container healthcheck: `GET /health`, answered by Caddy itself.
- Upstream container healthcheck: `GET /health/ready`, which pings Immich. A
  wrapper that cannot reach the library fails its health phase and retries,
  rather than reporting RUNNING while every tool call 401s.
- `PostStart` re-renders `appsettings.json` and, when it changed, restarts the
  upstream through `Deps.RestartContainer` so it re-reads the file. The
  PostStart resync is the only path that runs on every pass for a node already
  at RUNNING, and it is therefore what notices a key Immich replaced after the
  wrapper came up. The edge's own config is not touched there: its one variable
  is the MCP bearer, which changes only when the secrets store is cleared or the
  app is reinstalled, and both drive `PreStart`.

## Revocation and rotation

- The Immich key is Immich's. `apps/immich`'s `PostStart` validates the stored
  key on every pass (`GET /api/api-keys/me`) and, when Immich rejects it, mints a
  replacement (clearing any key already carrying `bloud-immich-mcp` first, since
  Immich reveals a secret exactly once). The operator can also revoke the key in
  Immich's API-keys screen; the next Immich pass notices and mints a new one,
  and this app's `PostStart` restarts the upstream with it.
- The MCP bearer is Bloud's. Clearing `httpToken` for `immich-mcp` in the
  secrets store, or reinstalling the app, rotates it on the next `PreStart`,
  which rewrites the Caddyfile and recreates the edge.

## Uninstall

Both containers and the app's data directory go with the app. The Immich API key
survives unless the operator deletes it in Immich; `apps/immich` reuses the same
named key on a later reinstall, so a reinstall does not accumulate rows.

## Icon

`icon.png` is Immich's icon from the selfh.st set:

```
https://cdn.jsdelivr.net/gh/selfhst/icons@main/png/immich.png
```

The set has no `immich-mcp` entry and the upstream project ships no logo, so the
icon is reused unchanged and documented here rather than invented. It is
accurate: the wrapper is an Immich capability rather than a separate product.

## Known limits

- **The tools act as Bloud's internal Immich admin**, not as the operator. See
  "Which account the key belongs to" above. This is the app's most important
  limitation.
- **The tool surface is read-write.** Assets can be uploaded, updated, and
  deleted, and albums, tags, people, and shared links can be changed. Every
  destructive tool requires an explicit `confirm: true` and bulk operations
  default to `dryRun: true`, but the capability is real.
- **One bearer means one principal.** Every harness acts as the same Immich
  account, the instance-level boundary the `mcp` contract states for every
  provider.
- **The out-of-band upload endpoint is not reachable without the bearer.**
  `immich_assets_upload_init` returns a URL for the MCP server's
  `/upload/{sessionId}` path, which the edge requires the bearer for. The
  headline "upload a local folder" flow is `immich_assets_upload_authorize`,
  which returns an Immich shared-link URL and is unaffected; `upload` and
  `upload_from_path` also work through `/mcp`.
