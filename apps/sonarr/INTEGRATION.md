# Sonarr Integration

## Status: implemented

Sonarr is a PVR for TV series: it monitors indexers, grabs releases, and
renames/organises the results into a library. Bloud installs it as a single
container behind Traefik forward-auth, with the instance put into Servarr's
own "External" authentication mode so it never presents a login form of its
own.

- Image: `lscr.io/linuxserver/sonarr:4.0.20.3014-ls325` (pinned, verified on GHCR)
- SSO strategy: `forward-auth` (Authentik)
- Route: `http://sonarr.<host>:<traefik port>` → `http://localhost:8989`
- Node / container: `apps-sonarr`, network `apps-net`

## What Bloud wires

| Layer | Value |
|-------|-------|
| Container | `apps-sonarr` (LinuxServer.io image, `PUID=1000`, `PGID=1000`, `TZ=Etc/UTC`) |
| Port | host `8989` → container `8989` (`port: 8989` in `metadata.yaml`) |
| Volumes | `{{appDataDir}}/config` → `/config`, `{{dataDir}}/media/shows` → `/shows`, `{{dataDir}}/downloads` → `/downloads` |
| Healthcheck | `curl -sf http://localhost:8989/ping` (5 s interval, 10 s timeout, 24 retries) |
| PreStart dirs | `<appDataDir>/config`, `<dataDir>/media/shows`, `<dataDir>/downloads`: the config dir, the media library and the shared downloads dir are chmodded `0777`, because the container writes them as LSIO's `abc` (uid 1000 → a host subuid under rootless podman, which the host agent is neither owner nor group member of). Without the media chmod, `/shows` is rejected as a root folder ("not writable by user 'abc'"); without the config chmod, Bloud's own later write of `config.xml` (which goes through a temp file *in that directory*) fails with EACCES and takes the node to ERROR; without the downloads chmod, an import that has to delete its source fails. The container's own init chowns only `/config` and `/run/sonarr-temp`, so nothing else normalises them |
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
test compares **values, not bytes**: Sonarr re-serialises `config.xml` with its
own serializer whenever settings are saved, and byte-comparing would report a
change on every reconciliation and restart the container in a loop.

Two upstream behaviours drive that rule:

- A key that appears **twice** is treated as absent by the app, which then
  appends its own default, so Bloud only ever writes single, well-formed keys
  through `pkg/xmlutil`.
- A `config.xml` that does not parse makes the app refuse to boot.

## The ApiKey this instance publishes

`apps/sonarr/metadata.yaml` declares `provides: {pvr: {secrets: [apiKey]}}`: a
provider's offer is keyed by the contract it satisfies, and what that contract
requires is defined once in `internal/catalog/contracts.go`. A declaration that
does not match it fails the catalog load, so a wrong or missing secret name
surfaces at startup rather than as a binding that is quietly half-empty.

The provider side of that rule is narrow on purpose: `apiKey` is the only secret
this app publishes under its `pvr` offer, and a name the contract does not carry
is a load failure rather than an extra secret, because a consumer can only
require what the contract names, so anything else could never reach one.

`PreStart` publishes the instance's own key with
`secrets.SetAppSecret(appName, servarr.SecretAPIKey, key)`, where
`servarr.SecretAPIKey` is the `"apiKey"` name the declaration and the publisher
agree on. Prowlarr and Seerr consume it as `PVRBinding.APIKey` through their
`pvr` binding (`AppState.Integrations.PVRs`), each only because it declares
`requires: [apiKey]` in its own `integrations.pvr` metadata: the declaration is
what makes the resolver hand that key over. Neither ever opens this instance's
`config.xml`.

The published value is adopted from the instance's own `config.xml`, never
generated here: that file is what the running instance authenticates with, and
the app itself can rewrite the key (a settings save in the UI). Publishing runs
on every `PreStart` and is idempotent, so a key the app regenerated is
re-published on the next pass. An empty `ApiKey` after the External-auth write is
an error, because `EnsureExternalAuth` generates one whenever the element is
absent.

## API calls

Base URL `http://localhost:8989`, every request carrying the key from
`config.xml` in `X-Api-Key` (the API also accepts `?apikey=`). The JSON is
mixed-case with string enums, unlike the PascalCase enum names in `config.xml`.

| Call | Purpose |
|------|---------|
| `GET /api/v3/config/host` | Read the host config; compare `authenticationMethod` case-insensitively with `external`. |
| `PUT /api/v3/config/host` | Only when it differs: set `authenticationMethod=external`, `authenticationRequired=enabled` in the document just read (read-modify-write, so the app's own `branch`/`allowedHosts` values are echoed back; the endpoint rejects a null `AllowedHosts` or an empty `Branch`). |
| `GET /api/v3/config/host` | Re-read and fail loudly, naming the observed value, if the instance still does not report `external`. |

PostStart runs this on every reconciliation. A settings edit through the
Sonarr UI can rewrite `config.xml` and flip the instance back to its forms
login; the next pass repairs it and logs `repaired external authentication` at
Info.

## Media-stack wiring: root folder and download client

Two further steps run in `PostStart`, after the auth verification. They are the
app-side half of the media stack: the orchestrator hands this configurator a
resolved binding for the provider, and the wiring itself is then written through
the two apps' own APIs.

### 1. Library root folder (needs no sibling)

| Call | Purpose |
|------|---------|
| `GET /api/v3/rootfolder` | Read the registered root folders. |
| `POST /api/v3/rootfolder`: `{"path":"/shows"}` | Only when `/shows` is absent. |

`/shows` is the mount `metadata.yaml` declares (`{{dataDir}}/media/shows` →
`/shows`), so the directory always exists on disk; the app simply never
registers it for itself. A root folder is what makes the instance usable and
what a request manager (Seerr) resolves `activeDirectory` against. This step is
unconditional (it runs whether or not any other media app is installed) and
idempotent: an instance that already has the path is left alone.

### 2. qBittorrent download client (provider from the integration binding)

The provider is resolved, not discovered: nothing is probed and no provider file
is read. `apps/sonarr/metadata.yaml` declares `downloadClient`, and the
orchestrator puts a `configurator.DownloadClientBinding` for `qbittorrent` in
`AppState.Integrations.DownloadClients`. That contract carries address and
reachability only, so the binding has no payload beyond the provider reference:
the consumer stores the address. `binding.Node`, `binding.Port`,
`binding.LocalURL` (`http://localhost:8081`, where the configurator's own
category write goes from the host) and `binding.Installed` all come from the
embedded `ProviderRef`, so their names are unchanged, and `binding.Installed`
decides wire versus prune.

| Call | Purpose |
|------|---------|
| `POST http://localhost:8081/api/v2/torrents/createCategory`: form `category=tv-sonarr` | Create the category the client stores, at the binding's `LocalURL`. `409 Conflict` (already exists) is accepted as already-done. |
| `GET /api/v3/downloadclient` | Read the configured clients. |
| `POST /api/v3/downloadclient/test` | Validate the document against qBittorrent; must succeed. |
| `POST /api/v3/downloadclient` | Only when no entry points at the binding's node and port; stores exactly the document that passed the test. |
| `DELETE /api/v3/downloadclient/<id>` | Only when `binding.Installed` is false: remove the entry Bloud wrote. |

The category write goes to the host-published WebUI port, which qBittorrent
leaves unauthenticated by design (its own configurator whitelists the proxy
subnet via `AuthSubnetWhitelistEnabled`); it is a host-local call and carries no
credentials.

The create body (the field set read from the pinned image's
`/downloadclient/schema`; `tvCategory` is the only per-app field name, Radarr
spells it `movieCategory`):

```json
{"name":"qBittorrent (Bloud)","implementation":"QBittorrent","configContract":"QBittorrentSettings",
 "protocol":"torrent","priority":1,"enable":true,"tags":[],
 "fields":[{"name":"host","value":"apps-qbittorrent"},
           {"name":"port","value":8081},
           {"name":"useSsl","value":false},
           {"name":"urlBase","value":""},
           {"name":"username","value":""},
           {"name":"password","value":""},
           {"name":"tvCategory","value":"tv-sonarr"}]}
```

The name is Bloud's own, not the vendor default: Servarr requires a unique name
per entry, so taking `qBittorrent` would collide with a client the operator added
under that name and leave Bloud's qBittorrent unwired. A collision is classified
as already-done either way, so it can never fail the node.

`host` is the binding's `Node`, the provider's container name
(`apps-qbittorrent`; container names are `apps-<catalogID>`, and a
single-container node's container *is* the node name), and `port` is its `Port`.
Both come from the provider's catalog metadata through the binding, not from a
constant in this package, so Sonarr resolves the provider over the shared
`apps-net` network and a port change in `apps/qbittorrent/metadata.yaml` needs no
edit here.

`POST /api/v3/downloadclient/test` is the proof the link works, and it runs
**before** the create: that order is load-bearing. The endpoint runs the
resource's shared validator, whose `Name must be unique` rule compares the
submitted document against every **stored** entry, so a body tested after its
own create is rejected with `400 Name/Should be unique` (verified against this
image: test-then-create `200`, the same body tested after the create `400` with
`{"propertyName":"Name","errorMessage":"Should be unique"}`). Testing first
validates the identical document: it makes Sonarr use those settings against
qBittorrent, which is where qBittorrent's subnet whitelist plus empty
credentials have to authenticate, and the create then stores exactly what
passed. A test failure fails the node, naming the sibling and the status, and
stores nothing.

Presence is decided on the entry's **host and port** (the binding's node and
port, the coordinates Bloud writes) and never on `implementation` alone: several
qBittorrent clients can live on one instance (a seedbox, a second daemon), and
the implementation is shared by all of them. Comparing stored field values is
still not done: a create requires a unique `name`, but the entry Bloud already
owns is identified by where it points, and `GET /api/v3/downloadclient` masks
every `PrivacyLevel.Password` field (`"********"`), so a stored secret could
never be diffed verbatim anyway.

**Why the category is created before the client.** qBittorrent never creates a
category by itself: `TorrentImpl::setCategory` returns false when the category
is not in the session (`src/base/bittorrent/torrentimpl.cpp`), so a torrent
added with `tvCategory` on a fresh instance silently ends up uncategorised and
the category feature does nothing. The consumer creates it (only the consumer
knows which category it wants) and treats qBittorrent's `409 Conflict` for an
existing category as already-done: `TorrentsController::createCategoryAction`
throws `APIErrorType::Conflict` when `SessionImpl::addCategory` returns false
for a known name (`src/base/bittorrent/sessionimpl.cpp`), and
`webapplication.cpp` maps that to HTTP 409. Any other failure is surfaced as an
error.

**Pruning.** When `binding.Installed` is false, `PostStart` removes the entry at
Bloud's own address (`GET /api/v3/downloadclient`, then `DELETE
/api/v3/downloadclient/<id>`) and logs `pruned stale download client` at Info. A
provider that was uninstalled must not leave a stored hostname behind that no
longer resolves; every grab would fail against a dead target. The binding still
carries the provider's node and port from its catalog metadata, which is how the
prune recognises the entry Bloud wrote. Clients the operator added point
somewhere else and are left alone: the provider being gone says nothing about
them.

A provider that is installed but not answering is a transient failure, not an
uninstall: the entry is kept, a warning is logged, and the next reconciliation
retries. `Installed` is what decides between wiring and pruning, and a probe
cannot stand in for it, because a probe could not tell "not installed" from
"restarting": pruning on a probe verdict deleted wiring that was still wanted.
ERROR is terminal in the orchestrator, so a restarting provider must not erase
the wiring.

The prune only runs when this configurator's `PostStart` runs: a full lifecycle
pass, or a staleness re-run. Uninstalling qBittorrent deletes its node, and a
removed node does not re-queue its dependents, so the stored entry survives until
Sonarr next runs its phases (a reboot, `./bloud dev`, or a reinstall). Making the
framework invalidate consumers when a provider is removed is the open
desired-state item in `docs/plans/media-stack-integration.md` §9 (F2).

A provider that is not installed is never an error: the instance works without a
download client, and a later reconciliation (or the provider's own staleness
trigger) wires the link. A provider that *answers and then rejects* the call is a
real fault and fails the node.

### What makes the link appear: a stacked install

The `downloadClient` integration in `apps/sonarr/metadata.yaml` is optional, so
`computeAppDeps` only creates the edge once qBittorrent is installed, and the
level ordering then runs the provider before its consumer: a stacked install
(qBittorrent **first**) wires the client in the same pass. `binding.Installed`
mirrors that same edge, so the configurator wires the client exactly while the
provider is wired to run before it. Installing qBittorrent later re-runs Sonarr's
`PostStart` through the framework's existing staleness path, which adds the
client then.

### Where the provider's address comes from

Nothing about the provider is duplicated here. `apps/qbittorrent/metadata.yaml`
declares its container (`apps-qbittorrent`) and its port (`8081`), the
orchestrator resolves the `downloadClient` binding from that metadata, and the
configurator reads the node, port and URLs off the binding. A port change in the
qBittorrent catalog entry therefore needs no consumer edit; `qbittorrentAppID` in
`apps/sonarr/configurator.go` only names the provider the label is matched
against.

## Security consequence (read this)

Servarr's `External` mode is **not** header-based SSO: the app installs
`NoAuthenticationHandler`, reads **no username header at all**, and treats
every request that reaches it as authenticated. There is no app-side auth to
fall back on.

Traefik's forward-auth middleware is therefore the *only* gate in front of
Sonarr, which means:

- the published host port **8989 is unauthenticated**; anything that can
  reach `localhost:8989` (or the host's IP on that port) has full admin access
  to the instance;
- the port must never be exposed to untrusted hosts. Bloud's dev VM does not
  port-forward it off the machine; do not publish it on an interface reachable
  from the LAN or the internet.

Bloud routes by host name, so the middleware applies to the app subdomain
regardless of the port.

## Files

| File | Purpose |
|------|---------|
| `apps/sonarr/metadata.yaml` | Container, volume, healthcheck, forward-auth SSO declaration |
| `apps/sonarr/configurator.go` | Directory creation, `config.xml` pre-seed, ApiKey publication, API verification, root folder, download-client wiring |
| `apps/sonarr/registration.go` | Registers the `apps-sonarr` factory |
| `apps/sonarr/configurator_test.go` | Paths/ports/wiring for this app |
| `services/host-agent/pkg/servarr/config.go` | Shared `config.xml` reader/writer |
| `services/host-agent/pkg/servarr/client.go` | Shared `X-Api-Key` client and auth verification |
| `services/host-agent/pkg/servarr/downloadclient.go` | Shared download-client surface (`GET`/`POST`/`DELETE` `/downloadclient`, the qBittorrent payload, `.../test`) |
| `services/host-agent/pkg/servarr/config_test.go`, `client_test.go`, `downloadclient_test.go` | The shared behaviour's test matrix (create/idempotence/preserve/repair/add/prune) |

## Verification

```bash
cd services/host-agent && go test ./pkg/servarr/...
cd apps && go test ./sonarr/...
```

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| Instance asks for a username/password | `AuthenticationMethod` was flipped back to a forms mode (or the key is duplicated). Reconcile: PostStart re-reads `/api/v3/config/host` and repairs it. |
| `401` from the API in host-agent logs | The `ApiKey` in `config.xml` does not match what the app has loaded (usually a stale container). Reconcile restarts the container on config change; check for a duplicated `ApiKey` element. Consumers do not read this file: they take the key from the host secret store. |
| Container never becomes healthy | `/ping` only turns 200 once the database and web host are up; first boot on a slow disk can use most of the 24 × 5 s window. |
| No download client in the UI | qBittorrent is not installed, so the `downloadClient` binding reports `Installed: false`: Bloud never wrote the client, or pruned the entry it had. Install qBittorrent: the dependency edge plus the staleness re-run wires the client on the next pass. |
| Grabs land uncategorised | The `tv-sonarr` category does not exist in qBittorrent. qBittorrent never creates categories by itself; `PostStart` creates it, so a reconcile fixes it. |
| `qBittorrent download client test failed` in the log | The provider cannot be reached from the Sonarr container at `apps-qbittorrent:8081`, or its WebUI subnet whitelist was reset by a config edit. |
| root folder missing (`/api/v3/rootfolder` empty) | `PostStart` has not run since the volume was created, or `POST /api/v3/rootfolder` failed (check the API key). A reconcile re-runs it. |
