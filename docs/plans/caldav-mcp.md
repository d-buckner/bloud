# Plan: Expose the calendars to agents via caldav-mcp

> Status: draft

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

### 2. Radicale rights

Switch `[rights] type` from `owner_only` to `from_file` with a rendered file:

```ini
[own]
user: .+
collection: {user}(/.*)?
permissions: rw

[caldav-service]
user: caldav-service
collection: <operator>(/.*)?
permissions: r
```

- `{user}` is Radicale's own `from_file` interpolation, so the `[own]` rule
  reproduces `owner_only` (each authenticated user reads/writes their own
  tree recursively).
- The `[caldav-service]` rule grants the agent **read-only, recursive** access
  to the operator's tree (`r` = read + descendants). `<operator>` is rendered
  from `operatorUsername()`; the rule is omitted until first-run exists, and
  the file is re-rendered on the same PostStart resync (plus restart) the feed
  jobs use.

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
pinned) with a Bloud-generated bearer, mirroring `apps/affine-mcp`:

- `integrations`:
  ```yaml
  caldav:  { required: true,  compatible: [{ app: radicale, default: true }] }
  appApi:  { required: true,  requires: [password], compatible: [{ app: radicale, default: true }] }
  ```
- `provides.mcp`: `secrets: [httpToken]`, `values: { serverName: caldav-mcp, path: /mcp }`.
- Configurator writes the image's config: `CALDAV_BASE_URL` from the `caldav`
  binding's `BaseURL` + `Path`, `CALDAV_USERNAME`/`CALDAV_PASSWORD` from the
  `appApi` binding, plus the generated MCP bearer. Hermes already consumes
  `mcp` as `multi: true`, so it registers this namespace automatically.

### 5. Validation

- Unit: rights rendering (own + agent rule, operator interpolation, empty
  operator), the `appApi` publication, the wrapper config rendering.
- Integration: install Radicale + caldav-mcp, then exercise the MCP endpoint
  as `caldav-service` and assert it can `list-calendars` and read the synced
  Radarr/Sonarr collections but not write them.
- e2e: a Hermes-facing assertion that the `caldav-mcp` namespace is registered.

## Non-goals

- No write access for the agent to the operator's calendars (read-only `r`).
  A write path is a separate decision about which principal owns agent-created
  events.
- No change to the `caldav` contract itself: browser clients still get the
  address and nothing else.
- No change to how the synced feeds are owned (still the operator's tree).

## Open items

- Read-only vs read-write scope for the agent (this plan: read-only).
- The pinned image + gateway choice (supergateway version) to satisfy
  `check:image-pins`.
