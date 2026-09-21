# Seerr Integration

Seerr (https://github.com/seerr-team/seerr) is a request and discovery manager
for a media server: users browse Jellyfin's libraries and request titles, which
Seerr hands to the download stack. Bloud installs it as a single container,
drives its first-run wizard through its own API, and wires the PVRs (Sonarr,
Radarr) that fulfil the requests, so users land on a usable request UI rather
than a setup form.

- Image: `ghcr.io/seerr-team/seerr:v3.4.1` (pinned; latest release, published
  2026-07-30, digest `sha256:f4768de5f616248d723e05891f3345a1402123775d03bf0890dbfedc0831bda1`)
- Container: `apps-seerr` on `apps-net`, port `5055` → `5055`
- Volume: `{{appDataDir}}/config` → `/app/config` (settings.json, SQLite
  database, logs, cache; SQLite is Seerr's default, so no database container)
- Environment: `TZ=Etc/UTC`, `PORT=5055`, `LOG_LEVEL=info`
- Healthcheck (upstream's own; the image ships busybox `wget` but no `curl`):
  `wget --no-verbose --tries=1 --spider http://localhost:5055/api/v1/settings/public || exit 1`

## Authentication: why there is no SSO integration

`metadata.yaml` declares `sso.strategy: none` and no `integrations.sso`, and
that is not an oversight; Seerr has no way to delegate authentication:

- **No OIDC in any stable release.** OIDC exists only as an unmerged PR
  (seerr-team/seerr#2715, milestone v3.6.0) with a `preview-new-oidc` image.
- **No trusted-header / forward-auth mode at all.** The only credential paths
  are Seerr's own session cookie and the `X-API-Key` header
  (`server/middleware/auth.ts` `checkUser`); nothing reads `X-authentik-*`,
  `X-Forwarded-User` or similar.

Putting an Authentik forward-auth outpost in front of Seerr would gate the
traffic but leave Seerr itself unauthenticated, so end users sign in with their
**Jellyfin account** (Jellyfin is LDAP-backed through Authentik, so it is the
same identity the rest of the catalog uses) or with a local Seerr account.
Bloud uses `X-API-Key` for its own admin-only onboarding calls.

## The Jellyfin connection after onboarding

Seerr mints its Jellyfin API key *into Jellyfin* while it onboards and stores it
as `settings.jellyfin.apiKey` (`server/routes/auth.ts`: the mint sits inside the
first-admin branch, so an initialized instance never mints another one). Purging
Jellyfin's data (a normal Bloud operation) therefore leaves Seerr holding a key
the new Jellyfin never issued, and every Jellyfin-backed feature fails with 401
(library list and scan, "Play on Jellyfin", metadata refresh) while media added
later never appears. Nothing in Seerr's UI explains it, and it is permanent
without operator action.

On the initialized path Bloud therefore verifies the stored key by *using* it (
`GET /System/Info` with `X-Emby-Token`) and repairs it when Jellyfin answers
401/403:

| # | Request | Auth | Purpose |
|---|---------|------|---------|
| C1 | `GET /System/Info` on Jellyfin with the stored key | `X-Emby-Token` | 200 → the key still works, nothing to do. 401/403 → stale |
| C2 | `POST /Users/AuthenticateByName` on Jellyfin | bootstrap admin credentials | Obtain the admin token the mint call needs |
| C3 | `POST /Auth/Keys?App=Seerr` (204), then `GET /Auth/Keys` | `X-Emby-Token` | Mint a key for Bloud and read it back (Jellyfin returns tokens in clear in the list) |
| C4 | `POST /api/v1/settings/jellyfin` with `{"apiKey": …}` | `X-API-Key` | Store it. Seerr tests the key against Jellyfin before persisting, so a key Jellyfin rejects is refused rather than saved |
| C5 | Library sync (step 4's calls) | `X-API-Key` | Re-fetch and re-enable the libraries, which were fetched with the dead key |

Failure is a warning, not a node error: Seerr itself keeps working (its own
login, requests and PVR links are unaffected), so a repair that cannot complete (
Jellyfin unreachable, the admin password changed) is logged and retried on the
next reconciliation. Jellyfin being *unreachable* is not treated as a stale key:
only a definitive 401/403 is.

## Onboarding flow (PostStart)

PostStart runs on every reconciliation and is idempotent. Steps, with the exact
requests (all paths are described once, in `api.go`):

| # | Request | Auth | Purpose |
|---|---------|------|---------|
| 1 | `GET /api/v1/settings/public` | none | Read `initialized`. `true` → the wizard is done: the Jellyfin connection is re-checked (see "The Jellyfin connection after onboarding"), then jump to step 6. |
| 2 | `GET http://localhost:8096/System/Info/Public` | none | Guard: is a media server installed? |
| 3 | `POST /api/v1/auth/jellyfin` | none | Verify the Jellyfin admin credentials **and** create admin user id 1 |
| 4 | `GET /api/v1/settings/jellyfin/library?sync=true`, then `GET /api/v1/settings/jellyfin/library?enable=<ids>`, then `POST /api/v1/settings/jellyfin/sync` | `X-API-Key` | Enable every library and start the scan (best-effort) |
| 5 | `POST /api/v1/settings/initialize`, then `GET /api/v1/settings/public` | `X-API-Key` | Mark setup complete and confirm the flag flipped |
| 6 | PVR reconcile, see below | `X-API-Key` | Give Seerr a DVR entry per installed PVR so requests can be fulfilled |

Details that matter:

- **Step 2 is a deferral, never a failure.** Seerr's first admin can only be
  created from a Jellyfin *administrator* login, so with no Jellyfin there is
  nothing to do: PostStart logs a warning (`No media server is installed, so
  Seerr's onboarding is deferred…`) and returns `nil`. A later reconciliation
  re-runs PostStart once Jellyfin is installed. The probe is unauthenticated and
  unretried (a 5 s timeout) because "not installed", "still booting" and
  "unreachable" are all the same answer here.
  The node stays RUNNING rather than ERROR on purpose: ERROR is terminal, so an
  app installed before its provider would never converge once the provider
  appeared. The cost is stated plainly: an instance that has never finished
  onboarding serves its first-run wizard to anyone who can reach it, and whoever
  completes it becomes Seerr's admin. With no SSO integration there is no gate in
  front of that, which is why the deferral is logged at Warn: the operator can
  see it. Putting the Seerr route behind Authentik forward-auth would close the
  window at the cost of a second login (the same trade-off as
  `apps/navidrome`).
- **Step 3 is the only non-interactive way to create Seerr's first admin.** The
  body is exactly what Seerr's own wizard sends
  (`src/components/Setup/JellyfinSetup.tsx:122`, read by
  `server/routes/auth.ts:235`):

  ```json
  {
    "username": "bloud-bootstrap-admin",
    "password": "<deps.Secrets.GenerateAppAdminPassword(\"jellyfin\")>",
    "hostname": "apps-jellyfin",
    "port": 8096,
    "useSsl": false,
    "urlBase": "",
    "email": "bloud-admin@localhost",
    "serverType": 2
  }
  ```

  `username`/`password` are Jellyfin's Bloud-managed bootstrap admin
  (`apps/jellyfin/configurator.go` creates it, never deletes it, and derives
  its password from the same secrets key). `hostname` must be the container
  DNS name (`localhost` would resolve *inside* the Seerr container) and
  `serverType: 2` is `MediaServerType.JELLYFIN` (`server/constants/server.ts`).
  In the same call Seerr also mints its own Jellyfin API key and stores the
  server id, so no separate media-server configuration step is needed.
  **Re-runs are handled explicitly:** once Jellyfin is configured, Seerr
  rejects a hostname with `500 {"error":"Jellyfin hostname already
  configured"}` (`server/routes/auth.ts:263-266`). That response is declared as
  *already done* rather than an error, because it is exactly the state a
  reconciliation finds after a crash between this call and step 5: the admin
  already exists, so onboarding continues to the admin steps instead of failing
  forever.
- **Step 4 is best-effort**: a Jellyfin with no libraries yet must not fail the
  node, because Seerr is still perfectly usable (an admin can toggle libraries
  in Settings → Jellyfin). A failure is logged as a warning. Note the
  version-specific shape: in **v3.4.1** the library list is synced *and*
  enabled through query flags on `GET /api/v1/settings/jellyfin/library`
  (`server/routes/settings/index.ts:333-393`; the wizard's own UI does the same
  at `src/components/Settings/SettingsJellyfin.tsx:161-175`). A sync without
  `?enable` means "no library enabled", hence the second call that enables all
  discovered ids. The newer `POST /jellyfin/library/sync` +
  `PUT /jellyfin/library/{id}` pair only exists on the `develop` branch, which
  is in no release; do not port it here until the pinned tag has it.
- **Step 5 must be verified.** `settings/initialize` is what flips
  `settings.public.initialized` (`server/routes/settings/index.ts:808-817`);
  without it every visit lands on the setup wizard. PostStart re-reads
  `/settings/public` and returns an error naming the observed value if it is
  still `false`.
- **Step 6 runs on both paths.** An initialized instance skips steps 2-5 but
  still reconciles the PVRs, which is what makes a PVR installed *after*
  Seerr work: the optional `pvr` integration in `metadata.yaml` creates the
  graph edge once the provider is installed, and the staleness path re-runs
  Seerr's PostStart, which now finds the new sibling. Whenever onboarding is
  deferred (no Jellyfin), the PVR step is deferred with it: the DVR write
  needs the admin user step 3 creates.

## PVR wiring (Seerr → Sonarr / Radarr)

Seerr fulfils a request by handing it to a PVR, which it stores as a **DVR
entry** in one of two lists (`settings.radarr`, `settings.sonarr`:
`server/lib/settings/index.ts:68-104`; the CRUD surface is
`server/routes/settings/radarr.ts` and `sonarr.ts`):

| # | Request | Auth | Purpose |
|---|---------|------|---------|
| 1 | `GET http://localhost:<port>/ping` | none | Is this PVR installed? (`<port>`: 8989 Sonarr, 7878 Radarr) |
| 2 | `<BloudDataPath>/<pvr>/config/config.xml` (file read) | n/a | The sibling's `ApiKey` (`pkg/servarr.SiblingConfigPath` + `APIKey`) |
| 3 | `GET http://localhost:<port>/api/v3/qualityprofile` | `X-Api-Key: <sibling key>` | Pick the profile the entry names |
| 4 | `GET /api/v1/settings/<radarr\|sonarr>` | `X-API-Key` | Does an entry for `apps-<pvr>` exist, and is it current? |
| 5 | `POST /api/v1/settings/<pvr>` (create) or `PUT /api/v1/settings/<pvr>/<id>` (repair) | `X-API-Key` | Store the entry |
| 6 | `DELETE /api/v1/settings/<pvr>/<id>` | `X-API-Key` | Prune the entry of a PVR that is gone |

The entry Bloud creates (Sonarr shown; Radarr is the same body with
`hostname: apps-radarr`, `port: 7878`, `activeDirectory: "/movies"` and **no**
`seriesType`/`animeSeriesType`/`enableSeasonFolders`; `RadarrSettings` has no
such fields, so sending them would be a shape Seerr never stores itself):

```json
{
  "name": "Sonarr",
  "hostname": "apps-sonarr",
  "port": 8989,
  "apiKey": "<Sonarr config.xml ApiKey>",
  "useSsl": false,
  "baseUrl": "",
  "activeProfileId": 4,
  "activeProfileName": "HD-1080p",
  "activeDirectory": "/shows",
  "isDefault": true,
  "is4k": false,
  "syncEnabled": true,
  "tags": [],
  "seriesType": "standard",
  "animeSeriesType": "standard",
  "enableSeasonFolders": true
}
```

Details that matter:

- **Ports and container names are duplicated from the providers' catalog
  entries.** `apps/sonarr/metadata.yaml` (8989) and `apps/radarr/metadata.yaml`
  (7878) publish the ports held as constants in `configurator.go`, and
  container names are `apps-<catalog id>` by invariant. Bloud does not hand a
  configurator its provider's address, so this coupling is the accepted cost of
  keeping cross-app wiring app-side: a provider port change is a greppable edit
  in `apps/seerr` and `apps/prowlarr` (which holds the same constants for the
  same PVRs). The probe runs on the host's published port; the DVR entry stores
  the container name, because inside `apps-net` that is what resolves.
- **Ordering is the DAG's, not luck.** `metadata.yaml` declares the optional
  `pvr` integration, so `computeAppDeps` brings each installed PVR up before
  Seerr, and the PVR's own configurator created its root folder, `/shows`
  (Sonarr) / `/movies` (Radarr), before its container was declared healthy.
  That is why `activeDirectory` can be the fixed shared path rather than
  something Bloud discovers, and why the quality-profile read has something to
  talk to at all.
- **Quality profiles are read, not assumed.** `activeProfileId` and
  `activeProfileName` come from the PVR's own `/api/v3/qualityprofile` list:
  `HD-1080p` when it is there, otherwise the first profile. Both images ship
  ids 1-6 with `HD-1080p` = 4, but an admin can add profiles, and a DVR entry
  stores both the id and the name: a hardcoded 4 would be wrong for a PVR
  whose profile set was edited.
- **An entry is identified by its hostname.** `hostname: apps-sonarr` means
  Bloud wrote it and may repair it; anything else (Seerr's own UI defaults to
  `localhost`) belongs to the admin and is left alone. Matching by name instead
  would make a hand-added entry look like Bloud's.
- **Drift is repaired with `PUT`, not `POST`.** `POST /settings/<pvr>` *always*
  appends with a fresh id (`server/routes/settings/radarr.ts:15-37`), so using
  it for an update would add a second entry for the same PVR on every
  reconciliation that saw drift. Create is POST (answered `201`); repair is
  `PUT /settings/<pvr>/<id>` (answered `200`, `:77-108`).
- **An unreadable key is required on the host, not guessed.** The
  `<ApiKey>` element the PVR wrote into its own `config.xml` is the source of
  truth (`pkg/servarr.SiblingConfigPath`/`APIKey`, the same convention
  `apps/navidrome` uses for Authentik's token). A PVR that answers but has no
  readable key yet is skipped with a warning: writing an entry with an empty
  key would only have to be corrected later.
- **Prune rule.** A PVR that does not answer its probe gets its entry
  (`hostname: apps-<pvr>`) deleted, so requests do not keep pointing at a
  hostname that no longer resolves. The sibling's `config.xml` is the trace
  that separates "was installed and is gone" from "was never installed":
  without it nothing can have been wired, and a stack with no PVR installed
  makes **no** Seerr call at all. Consequence: a PVR uninstalled *with* a data
  purge leaves no trace and its entry stays; remove it in Seerr → Settings →
  Services.
- **Failure policy.** Missing PVR → Info, skip (and prune when traced), never
  an error: the instance stays healthy and the next reconciliation retries.
  Present but not ready (key unreadable, profile list returning 5xx or
  unreachable) → Warn, skip. Present and *rejecting* (the PVR answers
  `4xx` to the profile read with the key from its own config, or Seerr rejects
  the DVR list/write) → an error naming the sibling and the status, because
  that is a misconfiguration an operator has to see.
- **Updates are compared field by field.** Only the fields Bloud writes are
  compared (`dvrSettings.sameWiring`), and a matching entry means no write at
  all: ids and `tags` are Seerr's, and an admin's edits to the rest are not
  drift Bloud should undo.

## Never pre-seed settings.json (verified failure)

**Rule: Bloud never writes `settings.json`.** PreStart only creates the mounted
config directory and makes it writable (`0777`); it writes no config file and
therefore always reports `changed=false`.

This was learned the hard way. Seerr's settings merge is **shallow per
top-level key**, `mergeSettings(this.data, parsedJson)`
(`server/lib/settings/index.ts:836`) behaves like `{...defaults, ...parsed}`,
so a partial file:

```json
{ "main": { "apiKey": "<32 hex characters>" } }
```

does not merge into `main`, it **replaces the whole `main` object** and erases
its defaults. A live v3.4.1 container confirmed it: after boot, the rewritten
`settings.json` kept the top-level defaults (`clientId`, `sessionSecret`,
`vapidPrivate`/`vapidPublic`) but `main` contained only the seeded `apiKey`.
With `main.mediaServerType` undefined, `checkOverseerrMerge()`
(`server/lib/overseerrMerge.ts:12-17`) no longer takes its early return and
treats a fresh install as a legacy Overseerr database: it runs
`INSERT INTO migrations (timestamp, name) VALUES ...` before any migration has
run, fails with `SQLITE_ERROR: no such table: migrations`, logs "Failed to
insert migration records" and calls `process.exit(1)`. The container then
crash-loops (~1 s restarts, healthcheck never passing, "Connection refused").

The API key is read **after** boot instead, from the file Seerr writes for
itself at `<DataPath>/config/settings.json`: `settings.load()` runs and
persists the file before the HTTP listener starts (`server/index.ts` awaits
`getSettings().load()` before `listen`), minting `main.apiKey` when it is
absent (`server/lib/settings/index.ts:806-810`). PostStart reads that key and
uses `X-API-Key` for the admin-only calls: `checkUser`
(`server/middleware/auth.ts`) maps a matching key to admin user id 1, which is
why step 3 must run before them. A missing file or missing key is a hard error
naming the path, never a silent unauthenticated attempt.

## The config directory must be world-writable

The image runs as `node` (uid 1000, `Dockerfile`: `USER node:node`) and
Bloud's container definitions have no `user:` field, so the mounted config
directory is created by the host agent and would be owned by a different
identity. PreStart therefore creates it, and re-asserts its mode, as `0777`
(same precedent as `apps/authentik/server_configurator.go`). Without it Seerr
cannot write `settings.json`, `db/db.sqlite3` or `logs/` on first boot and the
container never becomes healthy.

## Deliberately not wired

Two Seerr settings are **host-dependent absolute URLs**, so Bloud leaves them
as the user (or a future host-change hook) sets them rather than baking in a
value that goes stale:

- `main.applicationUrl`: public URL of the Seerr instance; used to build
  action links and logos in notification emails. Empty by default, and only
  needed for email features.
- `jellyfin.externalHostname`: public URL used for "Play on Jellyfin" links
  and avatars.

Both change whenever the Bloud public host changes, and neither affects
routing, auth or API access. Until Bloud grows a host-change hook they stay
user-settable in Seerr → Settings → General / Jellyfin. If Bloud does grow one,
these are the two writes to add (`POST /api/v1/settings/main` and
`POST /api/v1/settings/jellyfin`): they are idempotent admin calls.

Also unmanaged for now: Seerr's download-client connection (Seerr fulfils
through the PVR, which owns the download client: `apps/sonarr` and
`apps/radarr` wire qBittorrent themselves), and `network.trustProxy` (only
relevant for correct client IPs behind a proxy).

## Files

| File | Purpose |
|------|---------|
| `apps/seerr/metadata.yaml` | Container, port 5055, volume, healthcheck, `sso.strategy: none`, the optional `mediaServer` and `pvr` integrations |
| `apps/seerr/api.go` | Typed client for the onboarding and DVR endpoints (paths/payloads, with v3.4.1 source citations) |
| `apps/seerr/configurator.go` | PreStart (config dir only) and PostStart (onboarding + PVR wiring, sibling discovery) |
| `apps/seerr/configurator_test.go` | Fake Seerr, fake Jellyfin and fake PVRs; flow, guard, PVR payload/idempotency/drift/prune tests |
| `apps/seerr/registration.go` | Registers the `apps-seerr` node |

## Verification

```bash
cd apps && go test ./seerr/...
```

The tests assert behavior, not config text: the exact `auth/jellyfin` payload,
the sync-then-enable library calls, `X-API-Key` on every admin call,
`settings/initialize` as the last onboarding write followed by the confirmation
read (PVR writes come after it), the early return of the *wizard* on an
already-initialized instance, the silent deferral with no usable Jellyfin, the
resume when Jellyfin is already configured, the error when `initialized` does
not flip, and PreStart: the config dir exists as `0777`, `settings.json` is
*not* created by Bloud, and a pre-existing app-written file stays byte-identical
across runs.

For the PVR step: the exact create payload for both PVRs (Radarr without the
Sonarr-only fields), `X-API-Key` on the DVR calls and the sibling's key on the
profile read, creation after `settings/initialize`, no Seerr call at all when no
PVR is installed, a PVR installed after onboarding being wired on a PostStart
re-run, nothing written on a second reconcile, drift repaired with `PUT` on the
existing id (never a second `POST`), the stale entry pruned when the probe fails
while an admin's own entry is left alone, the `HD-1080p` → first-profile
fallback, a 5xx profile list skipped without failing, and a 4xx profile list
failing with the sibling named.

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| Stuck on the setup wizard | PostStart did not complete: check host-agent logs for `settings/initialize did not mark the instance initialized` or for the auth step. |
| Container restart-loops at ~1 s with `SQLITE_ERROR: no such table: migrations` | `settings.json` was pre-seeded with a partial file, so `main` lost its defaults and `checkOverseerrMerge` treated the fresh DB as a legacy Overseerr install. Bloud must never write that file (see above); delete it and let Seerr regenerate it. |
| `Jellyfin is not installed yet; Seerr onboarding is deferred` on every reconcile | Expected while Jellyfin is absent; install Jellyfin and reconcile again. |
| `<path> has no main.apiKey; Seerr onboarding cannot authenticate` | `settings.json` is missing or was replaced without a key: the container has not completed a boot (Seerr writes that file before it starts listening). Check the container's logs; Bloud never writes this file. |
| Requests stay "Requested" and never move to the download queue | No PVR wired: look for `PVR is not installed; Seerr will not fulfil through it` (install Sonarr/Radarr), `PVR is running but its API key is not readable yet` (the sibling's `config.xml` is missing; check its container), or an error naming the sibling and a status (its key was rejected). The PVR also needs its root folder registered and a download client: `apps/sonarr`/`apps/radarr` own that. |
| Seerr keeps a services entry for a PVR that is gone | The PVR was uninstalled with a data purge, so its entry could not be pruned automatically (see the prune rule); delete it in Seerr → Settings → Services. |
| Users can see no libraries | Library sync is best-effort and failed (logged as a warning); enable libraries in Seerr → Settings → Jellyfin, or check the Jellyfin connection. |
| Container reports unhealthy / permission denied in logs | The config directory is not writable by uid 1000: PreStart's `0777` was not applied (check the mount source path). |
