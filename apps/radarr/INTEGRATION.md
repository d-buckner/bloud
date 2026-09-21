# Radarr Integration

## Status: implemented

Radarr is a movie collection manager for Usenet and BitTorrent users: it
monitors indexers for wanted films, hands releases to a download client, and
renames/organises the results into a library. Bloud installs it as a single
container behind Traefik forward-auth, with the instance put into Servarr's
own "External" authentication mode so it never presents a login form of its
own.

- Image: `lscr.io/linuxserver/radarr:6.4.4.10685-ls317` (pinned, verified on GHCR)
- SSO strategy: `forward-auth` (Authentik)
- Route: `http://radarr.<host>:<traefik port>` → `http://localhost:7878`
- Node / container: `apps-radarr`, network `apps-net`

## What Bloud wires

| Layer | Value |
|-------|-------|
| Container | `apps-radarr` (LinuxServer.io image, `PUID=1000`, `PGID=1000`, `TZ=Etc/UTC`) |
| Port | host `7878` → container `7878` (`port: 7878` in `metadata.yaml`) |
| Volumes | `{{appDataDir}}/config` → `/config`, `{{dataDir}}/media/movies` → `/movies`, `{{dataDir}}/downloads` → `/downloads` |
| Healthcheck | `curl -sf http://localhost:7878/ping` (5 s interval, 10 s timeout, 24 retries) |
| PreStart dirs | `<appDataDir>/config`, `<dataDir>/media/movies`, `<dataDir>/downloads`: the config dir, the media library and the shared downloads dir are chmodded `0777`, because the container writes them as LSIO's `abc` (uid 1000 → a host subuid under rootless podman, which the host agent is neither owner nor group member of). Without the media chmod, `/movies` is rejected as a root folder ("not writable by user 'abc'"); without the config chmod, Bloud's own later write of `config.xml` (which goes through a temp file *in that directory*) fails with EACCES and takes the node to ERROR; without the downloads chmod, an import that has to delete its source fails. The container's own init chowns only `/config` and `/run/radarr-temp`, so nothing else normalises them |
| PreStart file | `<appDataDir>/config/config.xml` (mounted at `/config/config.xml`) |

`/ping` is the only route the app leaves anonymous: it answers `200
{"status":"OK"}` once the web host is up and `503` (plain text, "is starting
up") while it boots, which is what the healthcheck waits out.

## config.xml keys Bloud writes

All three Servarr instances share `services/host-agent/pkg/servarr`, which
owns these keys:

| Key | Value | Why |
|-----|-------|-----|
| `AuthenticationMethod` | `External` | Installs the app's `NoAuthenticationHandler`. |
| `AuthenticationRequired` | `Enabled` | Required alongside `External`: the app only validates `AllowedHosts` as non-empty when `AuthenticationRequired != Enabled`, and Bloud deliberately leaves `AllowedHosts` empty so ASP.NET host filtering stays off (the instance must not bake in a public hostname). |
| `ApiKey` | 32 lowercase hex chars | Generated with `crypto/rand` **only when the element is absent**; an instance's own key is never replaced. |

Everything else in the file (port, URL base, branch, download-client and
indexer settings the app writes itself) is preserved. The file is only
rewritten when one of the keys above holds a different value, and the change
test compares **values, not bytes**: Radarr re-serialises `config.xml` with its
own serializer whenever settings are saved, and byte-comparing would report a
change on every reconciliation and restart the container in a loop.

Two upstream behaviours drive that rule:

- A key that appears **twice** is treated as absent by the app, which then
  appends its own default, so Bloud only ever writes single, well-formed keys
  through `pkg/xmlutil`.
- A `config.xml` that does not parse makes the app refuse to boot.

## API calls

Base URL `http://localhost:7878`, every request carrying the key from
`config.xml` in `X-Api-Key` (the API also accepts `?apikey=`). The JSON uses
camel-case keys with string enums, unlike the PascalCase enum names in `config.xml`.

| Call | Purpose |
|------|---------|
| `GET /api/v3/config/host` | Read the host config; compare `authenticationMethod` case-insensitively with `external`. |
| `PUT /api/v3/config/host` | Only when it differs: set `authenticationMethod=external`, `authenticationRequired=enabled` in the document just read (read-modify-write, so the app's own `branch`/`allowedHosts` values are echoed back; the endpoint rejects a null `AllowedHosts` or an empty `Branch`). |
| `GET /api/v3/config/host` | Re-read and fail loudly, naming the observed value, if the instance still does not report `external`. |

PostStart runs this on every reconciliation. A settings edit through the
Radarr UI can rewrite `config.xml` and flip the instance back to its forms
login; the next pass repairs it and logs `repaired external authentication` at
Info.

## Media-stack wiring: root folder and download client

Two further steps run in `PostStart`, after the auth verification. They are the
app-side half of the media stack: no orchestrator, engine or store change is
involved, and the host never learns that these apps know about each other.

### 1. Library root folder (needs no sibling)

| Call | Purpose |
|------|---------|
| `GET /api/v3/rootfolder` | Read the registered root folders. |
| `POST /api/v3/rootfolder`: `{"path":"/movies"}` | Only when `/movies` is absent. |

`/movies` is the mount `metadata.yaml` declares (`{{dataDir}}/media/movies` →
`/movies`), so the directory always exists on disk; the app simply never
registers it for itself. A root folder is what makes the instance usable and
what a request manager (Seerr) resolves `activeDirectory` against. This step is
unconditional (it runs whether or not any other media app is installed) and
idempotent: an instance that already has the path is left alone.

### 2. qBittorrent download client (provider discovered by probe)

The provider is discovered the same way `apps/seerr` discovers Jellyfin: the
consumer asks the **host's published port** whether the provider is there.

| Call | Purpose |
|------|---------|
| `GET http://localhost:8081/api/v2/app/version` | Availability probe. `200` → the provider is installed and up; anything else (connection refused, timeout, non-2xx) → skip and prune. |
| `POST http://localhost:8081/api/v2/torrents/createCategory`: form `category=movie-radarr` | Create the category the client stores. `409 Conflict` (already exists) is accepted as already-done. |
| `GET /api/v3/downloadclient` | Read the configured clients. |
| `POST /api/v3/downloadclient/test` | Validate the document against qBittorrent; must succeed. |
| `POST /api/v3/downloadclient` | Only when no entry has `implementation == "QBittorrent"`; stores exactly the document that passed the test. |

Both the probe and the category write go to the host-published WebUI port,
which qBittorrent leaves unauthenticated by design (its own configurator
whitelists the proxy subnet via `AuthSubnetWhitelistEnabled`); they are
host-local calls and carry no credentials.

The create body (the field set read from the pinned image's
`/downloadclient/schema`; `movieCategory` is the only per-app field name,
which Sonarr spells `tvCategory`):

```json
{"name":"qBittorrent (Bloud)","implementation":"QBittorrent","configContract":"QBittorrentSettings",
 "protocol":"torrent","priority":1,"enable":true,"tags":[],
 "fields":[{"name":"host","value":"apps-qbittorrent"},
           {"name":"port","value":8081},
           {"name":"useSsl","value":false},
           {"name":"urlBase","value":""},
           {"name":"username","value":""},
           {"name":"password","value":""},
           {"name":"movieCategory","value":"movie-radarr"}]}
```

`host` is the provider's container name (`apps-qbittorrent`; container names are
`apps-<catalogID>`, and a single-container node's container *is* the node name),
so Radarr resolves the provider over the shared `apps-net` network.

`POST /api/v3/downloadclient/test` is the proof the link works, and it runs
**before** the create: that order is load-bearing. The endpoint runs the
resource's shared validator, whose `Name must be unique` rule compares the
submitted document against every **stored** entry, so a body tested after its
own create is rejected with `400 Name/Should be unique` (verified against this
image: test-then-create `200`, the same body tested after the create `400` with
`{"propertyName":"Name","errorMessage":"Should be unique"}`). Testing first
validates the identical document: it makes Radarr use those settings against
qBittorrent, which is where qBittorrent's subnet whitelist plus empty
credentials have to authenticate, and the create then stores exactly what
passed. A test failure fails the node, naming the sibling and the status, and
stores nothing.

Presence is decided on the entry's **host and port** (the coordinates Bloud
writes) and never on `implementation` alone: several qBittorrent clients can
live on one instance (a seedbox, a second daemon), and the implementation is
shared by all of them. Comparing stored field values is still not done: a
create requires a unique `name`, but the entry Bloud already owns is identified
by where it points, and `GET /api/v3/downloadclient` masks every
`PrivacyLevel.Password` field (`"********"`), so a stored secret could never be
diffed verbatim anyway.

**Why the category is created before the client.** qBittorrent never creates a
category by itself: `TorrentImpl::setCategory` returns false when the category
is not in the session (`src/base/bittorrent/torrentimpl.cpp`), so a torrent
added with `movieCategory` on a fresh instance silently ends up uncategorised
and the category feature does nothing. The consumer creates it (only the
consumer knows which category it wants) and treats qBittorrent's `409 Conflict`
for an existing category as already-done:
`TorrentsController::createCategoryAction` throws `APIErrorType::Conflict` when
`SessionImpl::addCategory` returns false for a known name
(`src/base/bittorrent/sessionimpl.cpp`), and `webapplication.cpp` maps that to
HTTP 409. Any other failure is surfaced as an error.

**Pruning.** When the probe fails, `PostStart` removes the entry at Bloud's own
address (`GET /api/v3/downloadclient`, then `DELETE
/api/v3/downloadclient/<id>`) and logs `pruned stale download client` at Info. A
provider that was uninstalled must not leave a stored hostname behind that no
longer resolves; every grab would fail against a dead target. Clients the
operator added point somewhere else and are left alone: the provider being gone
says nothing about them.

The prune only runs when this configurator's `PostStart` runs: a full lifecycle
pass, or a staleness re-run. Uninstalling qBittorrent deletes its node, and a
removed node does not re-queue its dependents, so the stored entry survives until
Radarr next runs its phases (a reboot, `./bloud dev`, or a reinstall). Making the
framework invalidate consumers when a provider is removed is the open
desired-state item in `docs/plans/media-stack-integration.md` §9 (F2).

A missing provider is never an error: the instance works without a download
client, and a later reconciliation (or the provider's own staleness trigger)
wires the link. A provider that *answers and then rejects* the call is a real
fault and fails the node; a provider that is merely restarting or briefly 5xx-ing
is logged and retried on the next reconciliation, because ERROR is terminal in
the orchestrator.

### What makes the link appear: a stacked install

The `downloadClient` integration in `apps/radarr/metadata.yaml` is optional, so
`computeAppDeps` only creates the edge once qBittorrent is installed, and the
level ordering then runs the provider before its consumer: a stacked install
(qBittorrent **first**) wires the client in the same pass. Installing
qBittorrent later re-runs Radarr's `PostStart` through the framework's existing
staleness path, which adds the client then.

### Port constant coupling

`qbittorrentAppID`/`qbittorrentPort` in `apps/radarr/configurator.go` mirror the
provider's `apps/qbittorrent/metadata.yaml`. That duplication is the accepted
cost of wiring the link app-side; a port change in the qBittorrent catalog entry
must be mirrored here (grep `qbittorrentPort`).

## Security consequence (read this)

Servarr's `External` mode is **not** header-based SSO: the app installs
`NoAuthenticationHandler`, reads **no username header at all**, and treats
every request that reaches it as authenticated. There is no app-side auth to
fall back on.

Traefik's forward-auth middleware is therefore the *only* gate in front of
Radarr, which means:

- the published host port **7878 is unauthenticated**; anything that can
  reach `localhost:7878` (or the host's IP on that port) has full admin access
  to the instance;
- the port must never be exposed to untrusted hosts. Bloud's dev VM does not
  port-forward it off the machine; do not publish it on an interface reachable
  from the LAN or the internet.

Bloud routes by host name, so the middleware applies to the app subdomain
regardless of the port.

## Files

| File | Purpose |
|------|---------|
| `apps/radarr/metadata.yaml` | Container, volume, healthcheck, forward-auth SSO declaration |
| `apps/radarr/configurator.go` | Directory creation, `config.xml` pre-seed, API verification, root folder, download-client wiring |
| `apps/radarr/registration.go` | Registers the `apps-radarr` factory |
| `apps/radarr/configurator_test.go` | Paths/ports/wiring for this app |
| `services/host-agent/pkg/servarr/config.go` | Shared `config.xml` reader/writer |
| `services/host-agent/pkg/servarr/client.go` | Shared `X-Api-Key` client and auth verification |
| `services/host-agent/pkg/servarr/downloadclient.go` | Shared download-client surface (`GET`/`POST`/`DELETE` `/downloadclient`, the qBittorrent payload, `.../test`) |
| `services/host-agent/pkg/servarr/config_test.go`, `client_test.go`, `downloadclient_test.go` | The shared behaviour's test matrix (create/idempotence/preserve/repair/add/prune) |

## Verification

```bash
cd services/host-agent && go test ./pkg/servarr/...
cd apps && go test ./radarr/...
```

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| Instance asks for a username/password | `AuthenticationMethod` was flipped back to a forms mode (or the key is duplicated). Reconcile: PostStart re-reads `/api/v3/config/host` and repairs it. |
| `401` from the API in host-agent logs | The `ApiKey` in `config.xml` does not match what the app has loaded (usually a stale container). Reconcile restarts the container on config change; check for a duplicated `ApiKey` element. |
| Container never becomes healthy | `/ping` only turns 200 once the database and web host are up; first boot on a slow disk can use most of the 24 × 5 s window. |
| No download client in the UI | qBittorrent was not installed (or not answering `localhost:8081`) when `PostStart` last ran. Install/start it: the dependency edge plus the staleness re-run adds the client on the next pass. |
| Grabs land uncategorised | The `movie-radarr` category does not exist in qBittorrent. qBittorrent never creates categories by itself; `PostStart` creates it, so a reconcile fixes it. |
| `qBittorrent download client test failed` in the log | The provider cannot be reached from the Radarr container at `apps-qbittorrent:8081`, or its WebUI subnet whitelist was reset by a config edit. |
| root folder missing (`/api/v3/rootfolder` empty) | `PostStart` has not run since the volume was created, or `POST /api/v3/rootfolder` failed (check the API key). A reconcile re-runs it. |
