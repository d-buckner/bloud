# affine-mcp Integration

## Status: Complete (unit + contract verified; no browser journey)

`affine-mcp` runs [affine-mcp-server](https://github.com/DAWNCR0W/affine-mcp-server),
a third-party MCP server for AFFiNE. It exposes 106 canonical tools over
AFFiNE's GraphQL and WebSocket APIs, including read-write document, database,
comment, and organization workflows. Bloud wires it to AFFiNE's owner account
and publishes its MCP endpoint to the `mcp` contract, so a harness such as
Hermes can register it as a tool namespace.

- Image: `ghcr.io/dawncr0w/affine-mcp-server:3.8.5` (pinned)
- Port: 9222 (the container's `PORT`; the image defaults to 3000, which would
  collide with the host-agent, so `PORT` is set to 9222 in the metadata)
- MCP endpoint: `http://<host>:9222/mcp`, bearer-authenticated
- SSO strategy: `none` (there is no user-facing login; a harness authenticates
  with the bearer Bloud generates)

## Why a wrapper

Bloud's design rule is that the provider should be the app, and AFFiNE ships its
own streamable-HTTP MCP server with scoped `aff_mcp_v1.*` credentials, so the
first version of this integration used it. That server is `READ_ONLY` and
workspace-scoped (`doc_search`, `read_document`; the write tools are gated behind
`env.dev` or a canary channel), which is not enough for an agent that authors
documents or touches databases.

This app replaced it. It is a separate container (a wrapper), it consumes a
credential into AFFiNE through `appApi`, and it is now the only provider of the
`mcp` contract in the catalog: AFFiNE's built-in provider was removed, along with
its Manticore search sidecar and its `aff_mcp_v1` minting path. A harness
registers `affine-mcp` and gets 106 read-write tools over the shared workspace.

The history is in
[`docs/plans/mcp-integrations.md`](../../docs/plans/mcp-integrations.md); the
design is in [`docs/features/mcp.md`](../../docs/features/mcp.md).

## Credential flow

Two credentials cross this app's boundary, in opposite directions.

### Into AFFiNE: the `appApi` contract

The wrapper is not a browser, so it cannot follow an SSO redirect into AFFiNE.
AFFiNE 0.27 removed its personal-access-token API, so the only credential its
GraphQL surface still validates is an account password. `apps/affine` therefore
provides the `appApi` contract:

```yaml
# apps/affine/metadata.yaml
provides:
  appApi:
    secrets: [password]
    runtimeValues: [username]
```

The password is the bootstrap owner's (the same one the AFFiNE configurator
signs in with), and the username is the operator's SSO identity. This app
consumes it with `requires: [password]`; the username and the workspace scope
arrive without being asked for. A required `default: true` provider turns the
integration into a graph edge, so AFFiNE converges to `RUNNING` and publishes
the credential before this node's `PreStart` runs.

**Why not an AFFiNE-issued token?** Because AFFiNE issues none that GraphQL
accepts. Its one scoped credential, `aff_mcp_v1.<id>.<secret>`, is validated by
`McpCredentialService.authenticate` only on `POST
/api/workspaces/<id>/mcp`; the general API guard takes a session cookie or a
session JWT (15-minute TTL) and explicitly routes `aff_mcp_v1.*` tokens away
from the JWT path (`core/auth/token.ts`). This wrapper speaks GraphQL and
WebSocket, so the account password is the only durable credential it can use,
and this is true on AFFiNE 0.27.4 and on current canary alike. A dedicated
least-privilege account would be the improvement; AFFiNE exposes no service
account or token API, which is the roadmap's open question.

### From the wrapper: the MCP bearer

The wrapper authenticates its own listener with a static shared secret
(`AFFINE_MCP_HTTP_TOKEN`). Bloud generates it on the first pass, persists it in
the secrets store, writes the same value into the config file, and publishes it
under `provides.mcp.secrets.httpToken`. A harness receives it as
`MCPBinding.Token` and sends it as `Authorization: Bearer ...`.

This is the one provider whose `httpToken` Bloud invents rather than the
provider minting. The rule still holds: the token is a credential the provider
validates, because Bloud is the one configuring the provider's listener.

## Configuration

The image reads `$XDG_CONFIG_HOME/affine-mcp/config`. `metadata.yaml` sets
`XDG_CONFIG_HOME=/data/config` and mounts `{{appDataDir}}/config` there, so the
configurator writes `<dataDir>/config/affine-mcp/config`.

Per-install values (the AFFiNE credential and the MCP bearer) go in that file:

```ini
AFFINE_BASE_URL=http://apps-affine:3010
AFFINE_EMAIL=<owner email>
AFFINE_PASSWORD=<owner password>
AFFINE_MCP_AUTH_MODE=bearer
AFFINE_MCP_HTTP_TOKEN=<generated>
```

Everything else is static and lives in the container environment:

| Variable | Value | Why |
|---|---|---|
| `XDG_CONFIG_HOME` | `/data/config` | Where the image looks for `affine-mcp/config`. |
| `PORT` / `AFFINE_MCP_HTTP_HOST` | `9222` / `0.0.0.0` | The port the host publishes and Traefik routes to. The image's default 3000 collides with the host-agent. |
| `MCP_TRANSPORT` | `http` | Streamable HTTP is the only transport that crosses a container boundary. |
| `AFFINE_ALLOW_INSECURE_HTTP` | `true` | Bloud reaches AFFiNE by container name over plain HTTP inside the app network; the server refuses a non-loopback plain-HTTP destination without this opt-in. The traffic never leaves the podman network. |
| `AFFINE_TOOL_PROFILE` | `full` | All 106 tools, including the destructive ones (`delete_workspace` and `delete_doc` require an exact-match confirmation argument). Set `read_only`, `core`, or `authoring` here to reduce the surface. |
| `AFFINE_CLIENT_VERSION` / `AFFINE_WS_CLIENT_VERSION` | `0.27.4` | AFFiNE rejects sign-in with `403 UNSUPPORTED_CLIENT_VERSION` when the client version trails the server. Keep these at the AFFiNE image version in `apps/affine/metadata.yaml`. |

`ModeSharedConfig` (0644) is deliberate. The image runs as its own non-root
`affine` user, a subordinate uid under rootless podman, so a 0600 file written
by the host agent would be unreadable to the server. The credential is bounded
by the app's data directory rather than by the file mode.

## Workspace scoping

The wrapper serves every workspace its credential can see through one URL
(`/mcp`). Every workspace-scoped wrapper tool resolves `args.workspaceId ||
AFFINE_WORKSPACE_ID`, so Bloud pins `AFFINE_WORKSPACE_ID` to the shared
workspace it provisions. AFFiNE publishes that id as the `appApi.workspaceId`
value from the same settled workspace the AI profile is registered against, so
the agent's default and the AI wiring cannot disagree about which workspace
Bloud owns.

This is what makes the agent work on `shared` without the operator or the agent
naming a workspace. Without the pin, an agent has to call `list_workspaces` and
choose on every call, and can write to the wrong workspace. If the provider has
not published a scope yet (an early pass, or a future `appApi` provider with
none), the pin is omitted and the wrapper falls back to the documented "agent
supplies the id" mode; an empty `AFFINE_WORKSPACE_ID` is never written, because
the wrapper would read that as a configured, empty scope.

## Container graph

```
apps-affine  ──(appApi)──>  apps-affine-mcp  ──(mcp)──>  hermes
```

The wrapper joins `apps-net` so it can resolve `apps-affine` by container name,
and Traefik routes the public hostname to the published 9222. The MCP path is
static (`/mcp`) because the server serves every workspace through one endpoint;
workspace selection is the pinned `AFFINE_WORKSPACE_ID`, not the URL.

## Health

- Container healthcheck: `GET /healthz` (process liveness, unauthenticated),
  matching the image's own `HEALTHCHECK`.
- `PostStart`: `GET /readyz` (checks AFFiNE GraphQL reachability). Unlike the
  AFFiNE configurator, a failure here is returned rather than swallowed: a
  wrapper that cannot reach its target is not serving MCP, so the node should
  retry. `/readyz` is unauthenticated, so a failure is a transport or config
  fault, never a missing harness bearer.

## Icon

`icon.png` is AFFiNE's icon from the selfh.st set:

```
https://cdn.jsdelivr.net/gh/selfhst/icons@main/png/affine.png
```

The set has no `affine-mcp` entry and the upstream project ships no logo, so the
icon is reused unchanged and documented here rather than invented. This is the
one catalog icon whose subject is another app; it is accurate, because the
wrapper is an AFFiNE capability rather than a separate product.

## Security notes

- **The credential is the AFFiNE owner.** This is the open question the roadmap
  records. The wrapper reaches AFFiNE with the bootstrap owner's authority, and
  the tool profile is the boundary, not a scoped credential. A dedicated
  least-privilege AFFiNE account would be the next step; AFFiNE offers no
  first-class service account today.
- **The tool surface is read-write by default.** That is the point of the
  wrapper relative to the built-in server, but it means an agent can modify and
  delete content. Set `AFFINE_TOOL_PROFILE` to a narrower profile if that is not
  wanted.
- **The MCP bearer is a long-lived instance credential.** One bearer means every
  harness acts as one principal, the same instance-level boundary the `mcp`
  contract states for every provider.
