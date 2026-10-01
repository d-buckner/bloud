> Status: draft

# Plan: Aggregate app calendars into one CalDAV endpoint

## Goal

Connect one third-party calendar app to one endpoint and see every event
Bloud has, without pasting a pile of per-app URLs.

```
Radarr  ──┐
Sonarr  ──┼── provides: icsFeed ──> Bloud resolver ──> Radicale's sync config
Hermes  ──┘                                                   │
                                                              ▼
                                                   [ storage: ics-sync ]
                                                              │
                                     ONE CalDAV server: bloud.example.com
                                                              │
                              DAVx⁵ / Fossify / Thunderbird / AFFiNE / Calino
                              ONE account → every calendar
```

Aggregation happens **server-side, in the storage layer**. The client adds one
account and inherits whatever feeds the catalog currently declares. Installing
a new feed-capable app makes a new calendar appear; no client changes.

## Why the obvious alternatives fail

**Client-side subscriptions (webcal://).** The user adds each feed URL to each
device. That is the pile of endpoints the goal is to eliminate, and it only
works in clients that poll feeds.

**Server-advertised pointers (`VSUBSCRIBED` / `CS:source`).** Radicale stores
a pointer and the *client* follows it. It reduces typing but not the number of
things: the phone still ends up running a poller per feed. It also hands the
feed URL, API key included, to every enrolled device.

**A Bloud-side sync daemon.** Rejected. Bloud moving data between apps on a
timer is exactly what the contract system exists to avoid, and it invents a
service with no owner.

The aggregation belongs in Radicale's storage layer because that is where the
data already lives, and because a storage plugin is a supported extension
point rather than a workaround.

## Verified facts

All checked against the pinned images in this catalog unless marked otherwise.

### Radicale 3.8.1 (`ghcr.io/kozea/radicale:3.8.1`)

- **Cannot fetch feeds.** The only outbound HTTP in the whole package is
  `auth/oauth2.py`. There is no subscriber, importer, or webcal support.
- **Storage is pluggable.** `utils.load_plugin` resolves `[...] type` to
  `radicale.<module>.<type>` only for known internal types; anything else is
  imported verbatim as a dotted module path. A third-party storage backend is
  a first-class extension point, not a hack.
- `VSUBSCRIBED` and `CS:source` exist (`app/propfind.py`) but are stored
  metadata echoed to clients. Nothing fetches them server-side.

### Radarr `6.4.4.10685-ls317` / Sonarr `4.0.20.3014-ls325`

Both publish an ICS feed. Verified live on Radarr:

| Request | Result |
|---|---|
| `GET /feed/v3/calendar/Radarr.ics` | `401` anonymous |
| `GET ?apikey=<key>` | `200`, `Content-Type: text/calendar`, `X-WR-CALNAME: Radarr Movies Calendar` |
| `GET` with `X-Api-Key` header | `200` |
| `GET` with HTTP Basic | `401` |

Sonarr carries the identical code path (`CalendarFeedController`,
`GetCalendarFeed`, `Sonarr.ics` in `Sonarr.Api.V3.dll`).

The UI also composes `unmonitored`, `asAllDay`, `releaseTypes`, and tag
parameters, plus a `webcal://` variant.

**The query-parameter form is the one that matters.** Calendar clients cannot
set headers, which is why Radarr accepts `?apikey=` at all.

### AFFiNE 0.27.4

AFFiNE is a CalDAV **client** with a declarative server-side config block:

```js
caldav: {
  enabled: false, allowCustomProvider: false,
  providers: [{ id, label, serverUrl, authType: "auto"|"basic"|"digest",
                requiresAppPassword, docsUrl }],
  allowInsecureHttp: false, allowedHosts: [],
  blockPrivateNetwork: true, requestTimeoutMs: 10000, maxRedirects: 5
}
```

- `blockPrivateNetwork: true` by default. `http://apps-radicale:5232` is
  refused twice over: private network, and not https. AFFiNE must dial
  Radicale through the **public origin**, or Bloud must set
  `blockPrivateNetwork: false` plus `allowInsecureHttp: true`.
- `caldav_oauth_unsupported` exists: basic/digest is the whole auth story,
  which matches Radicale's LDAP-Basic model.
- **No plain-ICS import.** `calendarSubscription` is account-scoped
  (calendars *within* a CalDAV account), and there is no `.ics` import in the
  English i18n. A raw feed URL cannot be given to AFFiNE directly; it needs
  a CalDAV server root. This is the reason Radicale is not redundant.

### Client ecosystem (from upstream docs, not verified locally)

- **DAVx⁵** is CalDAV/CardDAV only. Its FAQ directs `.ics`/webcal
  subscriptions to a separate app, **ICSx⁵**. Since 1.8 it can detect
  CalDAV-advertised webcal feeds and hand them to ICSx⁵.
- **iOS cannot replace Apple Calendar here.** Third-party apps get no
  equivalent of Android's sync adapters.
- **Calino is in the catalog** (`apps/calino`, required provider: Radicale).
  It is a browser SPA rather than a sync client, so it benefits from the
  one-endpoint goal directly, but it sits behind a different problem first:
  it dials `radicale.<host>` from `calino.<host>`, which is cross-origin,
  and Radicale sends no CORS headers. Nothing in this plan fixes that, and
  nothing in it is blocked by it either. See the "What is not wired" section
  of `apps/calino/INTEGRATION.md`.

## The plugin: `radicale-ics-sync`

This already exists: `radicale-ics-sync` on PyPI (v0.1.0, 2026-06-20). It
is a **storage** plugin that wraps the filesystem backend and syncs external
feeds into existing collections.

```ini
[storage]
type = radicale_ics_sync.storage
filesystem_folder = /data/collections
ics_config = /config/ics_sync.json
```

```json
[
  {
    "feed": "https://radarr.example/feed/v3/calendar/Radarr.ics?apikey=...",
    "collection": "alice/radarr",
    "sync_interval": 3600,
    "include_patterns": [],
    "exclude_patterns": []
  }
]
```

Properties that matter for this design:

- **`ics_sync.json` changes require a restart.** This maps directly onto
  `configurator.MustRestart` in `PreStart`. The existing no-op-if-unchanged
  contract is a good fit.
- **The collection must already exist.** The plugin will not create it.
  Something has to `MKCALENDAR` first, which needs the server running, so it
  lands in `PostStart`, not `PreStart`. That is a real sequencing constraint.
- **The API key stays on the server.** It appears only in `ics_sync.json`.
  Clients never see it. This is strictly better than the pointer model, which
  broadcasts the key to every enrolled device.
- **Filtering is `SUMMARY`-only.** `LOCATION` and `DESCRIPTION` are not
  supported yet.
- **Local edits are overwritten.** The README headline claims local changes
  survive; the limitations section says upstream changes fully overwrite local
  edits and field-level merge is only planned. Treat synced calendars as
  read-only regardless of what the client offers.

## What Bloud does

**A contract.** Apps that publish a feed declare it:

```yaml
# apps/radarr/metadata.yaml
provides:
  icsFeed:
    secrets: [apiKey]
    values:
      path: /feed/v3/calendar/Radarr.ics
      fileName: Radarr.ics
      displayName: Radarr Movies
```

**Radicale consumes them, many.** Same shape as Hermes taking every MCP
namespace:

```yaml
# apps/radicale/metadata.yaml
integrations:
  icsFeed:
    required: false
    multi: true
    requires: [apiKey]
```

**The configurator writes the plugin config.** `renderConfig` gains
`type = radicale_ics_sync.storage` and a `ics_config` path; `PreStart`
renders the resolved bindings into `ics_sync.json` beside the existing
`ldap-secret`, and returns `MustRestart` when the bytes change. Nothing else
in the reconciler changes.

**The address must be public.** The feed URL written into `ics_sync.json` is
the Traefik-fronted public URL, not `LocalURL`. AFFiNE's
`blockPrivateNetwork` default is the reason that rule needs to be explicit in
the contract rather than left to each consumer.

## Open questions

1. **Who owns the aggregated collections?** `collection` is
   `username/calendar-name`, and `[rights] type = owner_only` means only that
   user sees it. Options: sync into the operator's own user, or a dedicated
   shared user that everyone is a member of. Radicale has no shared-collection
   model under `owner_only`, so this decides whether one Bloud user or a
   group is required.
2. **The image.** The plugin must be pip-installed into the image's venv.
   `ghcr.io/kozea/radicale:3.8.1` does not ship it, and `npm run
   check:image-pins` requires a pinned image. A custom-built image is
   required, which is a new thing for this catalog.
3. **Dependency risk.** v0.1.0, single author, published 2026-06-20. Bloud
   would be putting the calendar story on top of it. Vendor it, or accept the
   churn?
4. **Collection creation.** The plugin needs the collection to pre-exist.
   Does the configurator `MKCALENDAR` as the sync principal in `PostStart`,
   or pre-create the filesystem layout in `PreStart`? The filesystem route
   fights the plugin's own bookkeeping.
5. **Feed availability.** A Radarr that is down should degrade one calendar,
   not the node. Need to confirm the plugin caches rather than erroring the
   collection.
6. **DAVx⁵ credentials.** DAVx⁵ authenticates with Basic using the real
   Bloud/Authentik password stored on the phone. Radicale has no app-password
   or scoped-token mechanism; it validates against LDAP directly. Worth
   resolving before this becomes the recommended phone story.

## Non-goals

- **No Bloud-side sync daemon.** The plugin owns fetching.
- **Not the `VSUBSCRIBED` pointer model.** It fails the one-endpoint goal and
  leaks the feed credential to clients.
- **Not Hermes MCP tooling.** Giving an agent calendar tools is a separate
  contract and a separate credential problem; see the MCP design.
- **Not a write-back path.** Synced feeds are read-only projections. Events
  the user creates belong to their own calendar, not to Radarr.
