# Radicale Integration

## Status: Working

[Radicale](https://radicale.org) is a lightweight CalDAV and CardDAV server:
calendars, contacts, and to-do lists, spoken to by the client on someone's
phone rather than by a browser. Bloud installs it as one container, generates
its INI configuration, and wires authentication to the Bloud identity
provider (Authentik) through the LDAP outpost.

- Image: `ghcr.io/kozea/radicale:3.8.1` (pinned)
- SSO strategy: `ldap`
- Public URL: `http://radicale.localhost:8080` (app subdomain on the Bloud
  base domain; `localhost:5232` direct for debugging)
- Storage: Radicale's own filesystem backend in `<appDataDir>/collections`.
  No Postgres, no Redis, no sidecars.

## Why the strategy is `ldap`

A CalDAV client authenticates with a username and password on every request.
It does not render HTML, it does not run JavaScript, and it cannot follow a
redirect to a login page and come back. `forward-auth` would put a 302 in
front of every calendar sync and break it, and `native-oidc` has nothing to
hand a `DAV/1.1` client.

So the app verifies the password the client sends against Bloud's directory,
the same way Jellyfin does. One Bloud account is one calendar login, and
disabling that account in Authentik stops the calendar too.

Radicale reads no environment variables for its settings. Everything below
lives in the INI file `PreStart` writes to `<appDataDir>/config/config`,
mounted read-only at `/config/config` and selected with `--config`, which
replaces the image's own default `--hosts` argument. That makes the Bloud-
generated file the only source of the bind address, the auth backend, and the
rights model.

## The two Authentik quirks that decide whether anyone can sign in

Both were found by breaking a live install against a running Authentik, and
both fail in the worst available way: the server starts cleanly, serves `401`,
and looks like the user mistyped their password.

### `uid` is not the username

Authentik's LDAP outpost serves the login name in `sAMAccountName` and `cn`.
Its `uid` attribute is a 64-character content hash:

```text
DN: cn=admin,ou=users,dc=ldap,dc=goauthentik,dc=io
    uid            = ['67fb3b10ec26820b061f07aafd8a70ae0a427b356b3ba91fa4f6b2f817418a2e']
    cn             = ['admin']
    sAMAccountName = ['admin']
```

Radicale's LDAP default filter is `(cn={0})`, and the filter most CalDAV
setup guides recommend is `(uid={0})`. The second matches nothing here. The
configurator uses `(sAMAccountName={0})`, the attribute Jellyfin also
settled on for the same reason (see the `LdapUidAttribute` note in
`apps/jellyfin`).

### The group tree collides with the user tree

Authentik also serves `ou=virtual-groups`, and a group named `admin` carries
the same `cn` and `sAMAccountName` as the admin user. Searched from the
directory base, every login name resolves to two entries:

```text
(sAMAccountName=admin) from dc=ldap,dc=goauthentik,dc=io
  -> cn=admin,ou=users,dc=ldap,dc=goauthentik,dc=io
  -> cn=admin,ou=virtual-groups,dc=ldap,dc=goauthentik,dc=io
```

Radicale requires exactly one entry for a login and rejects an ambiguous
filter outright, so nobody could sign in. The fix is to scope the search:
`ldap_base = ou=users,<BaseDN>`. `ldapSearchBase` does that and is
idempotent, so a provider that already reports the scoped base does not get
a doubled prefix pointing at a DN that does not exist.

### The third one, supplied by upstream

`ldap_ignore_attribute_create_modify_timestamp = true` is a workaround
Radicale carries for Authentik specifically: the outpost serves
`createTimestamp` and `modifyTimestamp` in a shape `ldap3` rejects. Without
it, authentication fails on a directory that is otherwise correct.

## The generated config

| Setting | Value | Why |
|---|---|---|
| `[server] hosts` | `0.0.0.0:<port>` | The container's bind. Comes from the config, not from the image's default args |
| `[auth] type` | `ldap`, or `denyall` with no provider | See [Locked until the provider arrives](#locked-until-the-provider-arrives) |
| `ldap_uri` | `ldap://apps-authentik-ldap:3389` | The outpost container on `apps-net`. Plain ldap: the hop is inside the container network |
| `ldap_base` | `ou=users,dc=ldap,dc=goauthentik,dc=io` | Scoped to the user tree; see the collision above |
| `ldap_reader_dn` | `cn=ldap-service,ou=users,...` | The service account Bloud provisions for directory reads |
| `ldap_secret_file` | `/config/ldap-secret` | The reader password, in its own file. Never inlined in the config |
| `ldap_filter` | `(sAMAccountName={0})` | See [`uid` is not the username](#uid-is-not-the-username) |
| `ldap_ignore_attribute_create_modify_timestamp` | `true` | Authentik's timestamp shape |
| `ldap_security` | `none` | No StartTLS on the internal hop |
| `realm` | `Bloud` | Shows up in the client's password prompt |
| `[storage] filesystem_folder` | `/var/lib/radicale/collections` | The one tree the container writes |
| `[storage] type` | `radicale_ics_sync.storage` | The vendored plugin; see [Aggregated calendar feeds](#aggregated-calendar-feeds) |
| `[storage] ics_config` | `/config/ics_sync.json` | The sync jobs Bloud writes (PreStart + the PostStart resync) |
| `[storage] hash_db` | `/var/lib/radicale/collections/ics_sync_hashes.json` | Inside the persisted tree, so deletions survive a restart |
| `[rights] type` | `owner_only` | See [Isolation model](#isolation-model) |
| `[web] type` | `internal` | Radicale's built-in browser UI at `/.web/` |
| `[headers] Access-Control-Allow-*` | CORS allow-list | Lets a browser SPA (Calino) call the DAV endpoint cross-origin; see [Browser clients](#browser-clients) |

### Locked until the provider arrives

With no LDAP provider bound, the config writes `type = denyall` rather than
falling back to Radicale's default, which is no authentication at all. An
install that is locked until its identity provider shows up converges when
the provider arrives. An install that is open until then is just open, and a
calendar is not something to leave open for one reconciliation cycle.

`PreStart` also deletes `ldap-secret` when the provider goes away, so a
credential nothing reads does not stay on disk.

## Aggregated calendar feeds

Apps that publish an ICS feed (Radarr, Sonarr) declare it under the
`icsFeed` contract, and Radicale subscribes to each one server-side. The user
adds one CalDAV account and inherits a calendar per feed, without pasting a
webcal URL into every device and without the feed's API key ever reaching a
client.

The fetching is done by a vendored storage plugin,
[`radicale-ics-sync`](https://pypi.org/project/radicale-ics-sync/), which
wraps Radicale's filesystem backend. It is vendored rather than built into a
custom image so Bloud ships no image pipeline of its own: the source is
embedded in the host-agent binary, written into `<appDataDir>/plugin` by
`PreStart`, mounted read-only at `/plugins`, and made importable with
`PYTHONPATH=/plugins`. See
[`plugin/PROVENANCE.md`](plugin/PROVENANCE.md) for the pinned version and the
two local changes.

`PreStart` writes the plugin tree and an initial `config/ics_sync.json`, one
job per feed binding. The feed URL is the provider's container address plus its
declared path, with the API key as the `?apikey=` query parameter the Servarr
feed endpoint requires. A feed with no published key yet is skipped rather than
written with an empty one.

The feed list is not fixed at install time: Radarr and Sonarr are installed
after Radicale more often than before it, and the orchestrator's only hook for
a RUNNING node is the PostStart resync, which never re-runs PreStart. So
`PostStart` re-renders `ics_sync.json` against the live bindings and restarts
the container when the bytes changed. PreStart still owns the first write (and
the recreate when the file changes before boot); PostStart owns the change that
happens after boot. A change requires a restart because the plugin starts one
polling thread per job when the storage backend is constructed. The writes are
compared and idempotent, so a steady-state reconciliation changes nothing.

### Who owns the synced calendars, and how they get created

The collection path is `<operator>/<provider>`, under the account of the user
who completed first-run setup (`PreferencesStore.FirstUser`). It has to be the
operator's own tree: `owner_only` rights mean a collection anywhere else is
invisible to the account they actually sign in with.

Bloud cannot create that collection over the DAV API. After first-run the
bootstrap `akadmin` account is deleted and Bloud never stores the operator's
password, so there is no credential to `MKCALENDAR` with. The plugin runs
inside the storage layer with write access, so the vendored copy creates the
calendar itself on the first sync (see `plugin/PROVENANCE.md`). That is the
one Bloud-side change to upstream; the alternative, a configurator that holds
a user's password, is the thing the `caldav` contract exists to avoid.

Synced calendars are read-only projections: an event removed upstream is
removed here too. Events the user creates belong in their own calendar.

## Isolation model

`owner_only` means a user can read and write only under their own top-level
path (`/<username>/...`). There is no sharing, no public calendars, and no
cross-user read in the default configuration. A calendar that two people
share is a follow-up that needs a rights backend change, not a setting.

This was verified against a live install: `ldap-service` requesting
`/admin/` gets `403 Forbidden`, not `401`. The identity was fine; the rights
model is what refused it.

## Browser clients

The built-in web UI at `/.web/` is same-origin, but a browser-based DAV client
is not: Calino is served from `calino.<host>` and calls `radicale.<host>`,
which the browser treats as cross-origin. Radicale sends no CORS headers by
default, so the generated config adds a `[headers]` section with an allow-list.

Two properties make that sufficient, and a wildcard origin safe:

- Radicale applies `[headers]` to every response, including `OPTIONS`. The
  browser's CORS preflight is an anonymous `OPTIONS` (it carries no
  credentials), and Radicale runs `do_OPTIONS` for an unauthenticated request,
  so the preflight returns `200` before the client has presented a password.
- Real DAV calls authenticate with Basic credentials the client sets in the
  `Authorization` header. There is no cookie or browser session to ride, so
  `Access-Control-Allow-Origin: *` exposes nothing to a caller who does not
  already hold the user's password, and Calino needs none of the third-party
  CORS proxy upstream offers as an alternative.

`Access-Control-Allow-Headers` covers what a CalDAV client sends
(`authorization`, `depth`, `if-match`, `destination`, `overwrite`), and
`Access-Control-Expose-Headers` covers what it reads back (`DAV`, `ETag`,
`Sync-Token`, `WWW-Authenticate`). A stricter origin list is possible once the
Bloud host set is passed to this configurator; the wildcard is the deliberate
choice for now, because it matches the per-request Basic-auth model rather than
a browser-session one.

## Storage and the rootless Podman uid

The upstream image runs as its own `radicale` uid (1000), not as the host
user. Under rootless Podman that maps to a subordinate uid the host agent
cannot become, so `<appDataDir>/collections` is created `0777` by
`PreStart` via `managedfile.EnsureWritable`. A directory left at `0755`
starts fine and fails on the first write, which surfaces as a `500` during a
client's first sync rather than at install time.

The config directory is mounted read-only. The generated files are written
`0644` (`managedfile.ModeSharedConfig`) because the reader is the container
uid, not the host uid that wrote them; `0600` would lock the app out of its
own configuration.

Teardown of the mapped-uid storage tree is handled by the orchestrator's
`podman unshare rm -rf` path (`Client.RemoveHostPath`), so uninstall removes
files the host user cannot remove directly.

## Verification

Verified against a live Bloud stack with a running Authentik LDAP outpost,
using the deployment's own generated config:

| Check | Result |
|---|---|
| Anonymous `PROPFIND /<user>/` | `401` with `WWW-Authenticate: Basic realm="Bloud"` |
| Authenticated `PROPFIND /<user>/` | `207 Multi-Status` |
| `MKCALENDAR` | `201 Created` |
| `PUT` an `.ics` event | `201 Created` |
| Wrong password | `401` |
| One user reading another's tree | `403 Forbidden` |
| `GET /.well-known/caldav` | `301` to the DAV root |
| `GET /.well-known/carddav` | `301` to the DAV root |

The same assertions are in `services/host-agent/internal/e2e/radicale_test.go`
(integration tier) and `e2e/tests/radicale.spec.ts` (browser tier).

`apps/radicale/configurator_test.go` covers the feed path: the storage section
names the plugin, the sync jobs compose the feed URL with the escaped key, an
incomplete binding writes no job, and the plugin tree plus `ics_sync.json` are
written once and recreated (not rewritten) on a change.
`services/host-agent/internal/engine/orchestrator/integration_bindings_test.go`
asserts the `icsFeed` binding carries the address, path, display name, and the
key only when the consumer required it.

## What is not wired

- **Sharing.** `owner_only` has no sharing. Radicale supports a `from_file`
  rights backend that would express it; that is a deliberate change to the
  rights model, not a default.
- **Address book clients that need vCard directory lookup.** Radicale serves
  contacts as a DAV collection; it is not a global address book that other
  apps query.
- **TLS.** Bloud serves plain HTTP today, so `ldap_security = none` on the
  internal hop matches. When Bloud serves HTTPS, the internal hop is still
  inside the container network, so this does not have to change with it.
