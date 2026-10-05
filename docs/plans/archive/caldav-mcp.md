# Plan: Expose the calendars to agents via caldav-mcp

> Status: superseded. Shipped on a different base.
>
> This plan specified `io.github.dominik1001/caldav-mcp` v0.10.0 bridged by
> supergateway. That shape never converged: it blocks the MCP `initialize`
> handshake on a full DAV login (24.4s measured) against a 15s client timeout.
> The shipped app wraps `PhilflowIO/dav-mcp` instead, which binds its port
> before it logs in. See
> [caldav-mcp-dav-mcp.md](../caldav-mcp-dav-mcp.md) for the port and
> [apps/dav-mcp/INTEGRATION.md](../../../apps/dav-mcp/INTEGRATION.md) for
> the live-verified shape.
>
> The shipped app is also renamed: the catalog entry is `dav-mcp`, not
> `caldav-mcp`, because the wrapped package changed and the old name described a
> package that is no longer in the tree. The body of this plan keeps naming
> `caldav-mcp` where it means the upstream npm package, which is what it was
> written against.

## Goal

Give Bloud's MCP harness (Hermes) calendar tools: list/create/update/delete
events and to-dos against the same Radicale server the operator already has,
including the aggregated Radarr/Sonarr feeds.

`caldav-mcp` (`io.github.dominik1001/caldav-mcp`, v0.10.0, MIT) is a stdio MCP
server that speaks CalDAV over Basic auth (`CALDAV_BASE_URL`, `CALDAV_USERNAME`,
`CALDAV_PASSWORD`) and exposes `list-calendars`, `list-events`, `create-event`,
`update-event`, `delete-event`, and the matching to-do tools. It is the natural
"calendar namespace" for Hermes.

## The blocker this plan resolves

Two facts collide:

- Bloud's `caldav` contract deliberately carries **no credential**: the person
  authenticates to the DAV server with their own password, which never crosses
  an app boundary (invariant 15).
- Radicale's `owner_only` rights mean the aggregated Radarr/Sonarr collections
  live under the operator's own tree (`/admin/radarr`, `/admin/sonarr`) and are
  readable only by that operator.

A machine client therefore needs a credential that can see the operator's
calendars. The chosen shape: a **Bloud-provisioned service account** with a
scoped read grant, so no real user password is ever stored or forwarded.

## Design

```
Hermes ── consumes `mcp` ──> caldav-mcp (wrapper) ── CalDAV Basic ──> Radicale
                                │  ^
                                │  └── CALDAV_BASE_URL   (from `caldav` binding)
                                └───── CALDAV_USERNAME/PASSWORD (from `appApi` binding,
                                       the caldav-service account)
```

### 1. Service account

Mirror the existing `ldap-service` account (`pkg/authentik/ldap.go`):

- Add a top-level secret `caldavServicePassword` to `secrets.json` (same
  shape as `ldapBindPassword`).
- Provision an Authentik service account `caldav-service` (type
  `service_account`, `is_active`) and set its password for LDAP direct bind,
  exactly as `EnsureLDAPInfrastructure` does for `ldap-service`.
- The account is a plain directory user, so Radicale authenticates it over
  LDAP like any other account.

### 2. Shared calendars (native sharing)

Radicale 3.8.x has a native `[sharing]` subsystem (csv database + `map`
shares), so the rights model stays `owner_only` and cross-user access goes
through sharing instead:

```ini
[sharing]
type = csv
collection_by_map = true
permit_create_map = true
```

Bloud writes `sharing.csv` as the single writer: one `map` share per feed,
mounting the operator's collection into the agent's tree as a read-only
virtual collection:

```csv
ShareType;PathOrToken;PathMapped;Conversion;Owner;User;Permissions;...
map;/caldav-service/radarr/;/admin/radarr/;none;admin;caldav-service;Rr;...
```

`PathOrToken` is the virtual path in the recipient's tree and `PathMapped` the
owner's real collection; `EnabledByOwner`/`EnabledByUser` are pre-set true so
no accept step is needed. The share is what makes `list-calendars` enumerate
the feeds, because CalDAV discovery lists the authenticated principal's own
home. The CSV is re-rendered on the same PostStart resync (plus restart) the
feed jobs use.

### 3. Credential publication

Reuse the existing `appApi` contract rather than adding new vocabulary. It is
already "a credential into another app's own API, for a companion that is not
a browser"; Radicale's API is DAV, and the companion is caldav-mcp.

- `apps/radicale/metadata.yaml` gains:
  ```yaml
  provides:
    caldav: { values: { path: / } }
    appApi: { secrets: [password], values: { username: caldav-service } }
  ```
- The Radicale configurator publishes the generated password under
  `SetAppSecret("radicale", "password", …)`, resolved by the existing
  `appApi` binding arm (`Username` + `Password`).

### 4. The wrapper app (`apps/caldav-mcp`)

`caldav-mcp` is stdio-only, and Bloud's `mcp` contract is streamable-HTTP, so
the wrapper runs `caldav-mcp` behind a stdio→HTTP gateway (supergateway,
pinned), mirroring `apps/affine-mcp`:

- `integrations`:
  ```yaml
  caldav:  { required: true,  compatible: [{ app: radicale, default: true }] }
  appApi:  { required: true,  requires: [password], compatible: [{ app: radicale, default: true }] }
  ```
- `provides.mcp`: `secrets: [httpToken]`, `values: { serverName: caldav-mcp, path: /mcp }`.
- Configurator writes a `run.sh` (exporting `CALDAV_BASE_URL` from the `caldav`
  binding and `CALDAV_USERNAME`/`CALDAV_PASSWORD` from the `appApi` binding),
  which supergateway runs as the stdio server. Hermes already consumes `mcp` as
  `multi: true`, so it registers this namespace automatically.

Two supergateway facts shape the implementation and were not anticipated in
this draft: the latest release (4.1.0) has **no `--config`/`--apiKey`** (the
per-server env/bearer config file is only on `main`, which `check:image-pins`
rejects as rolling), so the bearer is published but not enforced, and the
CalDAV env goes through a generated shell script rather than a config file.

### 5. Validation

- Unit: rights rendering (root/own/agent rules, operator interpolation), the
  `appApi` publication, the wrapper run-script rendering.
- Integration: install Radicale + caldav-mcp, then exercise the MCP endpoint as
  `caldav-service` and assert it can list tools and read the synced collections
  over DAV directly.
- e2e: a Hermes-facing assertion that the `caldav-mcp` namespace is registered.

## Non-goals

- No write access for the agent to the operator's calendars (`Rr`). A write
  path is a separate decision about which principal owns agent-created events.
- No change to the `caldav` contract itself: browser clients still get the
  address and nothing else.
- No change to how the synced feeds are owned (still the operator's tree).

## Open items

- **Family sharing.** The map-share mechanism is proven for the agent; sharing
  every feed to every Bloud user (the "whole family" model, mirroring the
  shared AFFiNE workspace) needs the identity provider's user list in the
  Radicale configurator (`sso` binding + `authentik.ListUsers`).
- Read-only vs read-write scope for the agent (this plan: read-only).
- Bearer enforcement: blocked on a supergateway release carrying `--apiKey`.
