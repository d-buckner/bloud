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
| `[storage] type` | `filesystem` | Radicale's own backend. Feeds arrive over CalDAV from a sidecar, not through a plugin; see [Aggregated calendar feeds](#aggregated-calendar-feeds) |
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

The fetching is done by [`pimsync`](https://pimsync.whynothugo.nl/), running
as a sidecar container that talks to the server over CalDAV the way any client
does. It is not a plugin.

The previous implementation vendored `radicale-ics-sync`, a Python storage
plugin that wrapped Radicale's filesystem backend from inside the server
process. That coupling cost more than it saved: the sync shared a lifetime and
a failure domain with the server it fed, it could only place collections by
reaching into the storage layer, and every change to it meant shipping Python
bytes inside the host-agent binary. pimsync creates the collection it fills
over the protocol, restarts without disturbing the server, and is maintained
upstream.

Upstream publishes no container image, so Bloud runs
[`bleala/pimsync`](https://hub.docker.com/r/bleala/pimsync), a signed
multi-arch wrapper around upstream `0.6.0`, pinned by index digest. See
[Image provenance](#image-provenance).

### How the sidecar is wired

`apps-radicale-pimsync` is a second graph node in the same app. It depends on
`apps-radicale` and shares the app's data tree, so its own `PreStart` renders
its own config and reports `RestartNeeded` when that config changes. The
Radicale node no longer sees feed bindings at all: it serves whatever the
sidecar wrote.

The rendered file is `<appDataDir>/pimsync/pimsync.conf`, mounted read-only
at `/etc/pimsync/pimsync.conf`, and `<appDataDir>/pimsync-status` is mounted
writable for pimsync's own sync-state database.

Each feed becomes one read-only `webcal` storage and one `one_way` pair that
pushes it into the CalDAV target. `one_way` matters: a feed is a projection of
somebody else's system, so a change on the target side is drift to overwrite
rather than a conflict to resolve, and items the feed does not have are
removed. That is what makes the sync never produce a conflict nobody is around
to settle.

### Why the container parks instead of idling

`pimsync daemon` with zero configured pairs exits immediately with an error,
which under `restartPolicy: always` is a crash loop. A Bloud instance with no
feed provider installed is a completely ordinary state, so the container's
entrypoint is a loop that checks the rendered config for `pair` blocks: with
none it sleeps, with any it runs the daemon. A daemon that dies is retried on
the next turn rather than taking the container down.

That is also why the catalog grew an `entrypoint` field. The image's own
entrypoint is a supervision script driven by a pile of `PIMSYNC_*`
environment variables, and passing arguments cannot replace it: `command`
sets the argument list the entrypoint receives, not the entrypoint itself.
Overriding it keeps Bloud out of that env-var contract entirely.

The feed list is not fixed at install time: Radarr and Sonarr are installed
after Radicale more often than before it. That is fine here in a way it was not
with the plugin, because the sidecar's config is re-rendered by its own
`PreStart` on every pass that touches the node, and the change lands as a
recreate of the sidecar rather than a restart of the calendar server.

### Who owns the synced calendars, and how they get created

The collection path is `calendar-service/<provider>`, under a Bloud service
account rather than under a person. That choice is the whole sharing story: a
collection is only shareable if its owner is not somebody's private tree, and
the alternative (the first-run operator owns it) makes the family's calendars
a side effect of who set the box up first.

`calendar-service` is provisioned by the Authentik configurator alongside
`ldap-service` and `caldav-service`, from the top-level
`calendarServicePassword` secret. Its credential is published into Radicale's
own app-secret scope (`authentik.CalendarOwnerSecretKey`) and is deliberately
not offered through any contract: it can write, and advertising a write
credential in a contract is how it ends up in a container that was only meant
to read.

The family calendar is created over DAV, by the configurator, as that
account, in `PostStart`. That is the one point in the lifecycle where the
server is up and the credential exists. It is idempotent (PROPFIND, then
MKCALENDAR only if absent) and it warns rather than fails, so a pass that
cannot reach the server leaves the node converged and retries next time.

Synced calendars are read-only projections: an event removed upstream is
removed here too.

## Isolation model

`owner_only` means a user can read and write only under their own top-level
path (`/<username>/...`), so one Bloud account cannot read another's calendar
or contacts by default. Cross-user access goes through Radicale's **native
sharing**: the configurator enables `[sharing] type = csv` with map shares
and writes `sharing.csv`, mounting each shared collection into every
recipient's own tree as a virtual collection. The rights model stays
`owner_only`; sharing is what makes `list-calendars` see anything outside
your own tree.

The recipients are every active Bloud user plus the agent's service account,
and the grants are:

| Collection | Grant | Who |
|---|---|---|
| `calendar-service/<feed>` | `Rr` read-only | every user, and the agent |
| `calendar-service/family` | `RWrw` read-write | every user, and the agent |

The family calendar is the thing people add events to, and it is where
agent-created events land. The feeds stay read-only for everyone: they are
projections of someone else's system.

The user list comes from the identity provider through the `sso` binding's
`apiToken`, the same path AFFiNE's shared-workspace invites take. Inactive
accounts are dropped. Because that list is what every share is rendered from,
a failed read is treated as *unknown* rather than as *empty*: the file already
on disk is left untouched instead of being rewritten with nobody in it. A
provider that blinks must not take the family's calendars away.

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

`apps/radicale/pimsync_test.go` covers the sidecar: the scfg quoting rules,
that each feed renders one `webcal` storage and one `one_way` pair, that a
target with no owner credential renders no pairs (so the container parks
rather than failing on every connection), that incomplete feeds are skipped,
that the render is order-independent, and that the config and the status
directory land where the mounts expect them. `configurator_test.go` covers the
server side: the storage section is the native filesystem backend with no
trace of the retired plugin, and the sharing database is re-rendered on every
pass.
`services/host-agent/internal/engine/orchestrator/integration_bindings_test.go`
asserts the `icsFeed` binding carries the address, path, display name, and the
key only when the consumer required it.

## Image provenance

`docker.io/bleala/pimsync` at index digest
`sha256:0a92e4a733a101daf8ccd141e84894908791f831f7b7b7f067e357bcee48171b`.

Upstream [`pimsync`](https://pimsync.whynothugo.nl/) publishes no container
image. This is [Bleala/Pimsync-DOCKERIZED](https://github.com/Bleala/Pimsync-DOCKERIZED)
release `1.0.9`, which packages upstream pimsync `0.6.0`. The wrapper is
multi-arch (`amd64`, `arm64`) and cosign-signed upstream.

It is pinned by digest rather than by tag for the usual reason, and one that is
specific here: the tag tracks a wrapper release, and a wrapper release can move
underneath us while the packaged pimsync version stays the same. The digest
fixes both at once.

The trade-off is that this is a third-party image, not one Bloud builds. It is
used because Bloud has no image-build infrastructure and the alternative, a
Bloud-owned `Containerfile`, would be a bigger thing to own than the sync
itself. The image runs as its own unprivileged uid (`1000`), reads only the
rendered config and its own status directory, and reaches nothing but the
Radicale container and the feed providers.

## What is not wired

- **Address book clients that need vCard directory lookup.** Radicale serves
  contacts as a DAV collection; it is not a global address book that other
  apps query.
- **TLS.** Bloud serves plain HTTP today, so `ldap_security = none` on the
  internal hop matches. When Bloud serves HTTPS, the internal hop is still
  inside the container network, so this does not have to change with it.
- **Migration of pre-existing feed collections.** Installs made before the
  shared-calendar owner existed have their synced feeds under the operator's
  tree. They re-sync under `calendar-service/` and the old collections are
  left behind. Alpha: documented, not migrated.
- **A user who already owns a collection named `family`.** The family
  calendar is mounted at `/<user>/family/`, so a personal collection with
  that name collides with the mount. Nothing detects it; the name is reserved
  by convention.
