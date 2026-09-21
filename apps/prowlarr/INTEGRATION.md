# Prowlarr Integration

## Status: implemented

Prowlarr is an indexer manager: it holds the indexer definitions (Torznab/
Newznab, tracker APIs) and syncs them to the PVRs that actually grab releases
(Sonarr, Radarr, and other Servarr instances). Bloud installs it as a single
container behind Traefik forward-auth, puts the instance into Servarr's own
"External" authentication mode so it never presents a login form of its own,
and wires the PVRs that are installed into Prowlarr's application-sync list.

- Image: `lscr.io/linuxserver/prowlarr:2.6.5.5623-ls161` (pinned, verified on GHCR)
- SSO strategy: `forward-auth` (Authentik)
- Route: `http://prowlarr.<host>:<traefik port>` → `http://localhost:9696`
- Node / container: `apps-prowlarr`, network `apps-net`

## What Bloud wires

| Layer | Value |
|-------|-------|
| Container | `apps-prowlarr` (LinuxServer.io image, `PUID=1000`, `PGID=1000`, `TZ=Etc/UTC`) |
| Port | host `9696` → container `9696` (`port: 9696` in `metadata.yaml`) |
| Volumes | `{{appDataDir}}/config` → `/config` (no library or download mount: Prowlarr moves no files) |
| Healthcheck | `curl -sf http://localhost:9696/ping` (5 s interval, 10 s timeout, 24 retries) |
| PreStart dirs | `<appDataDir>/config`, `<dataDir>/downloads` (the latter is the shared Bloud download root, created even though Prowlarr mounts nothing from it) |
| PreStart file | `<appDataDir>/config/config.xml` (mounted at `/config/config.xml`) |
| PostStart | Verify/repair external auth, then reconcile one application entry per installed PVR |

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

Everything else in the file (port, URL base, branch, indexer settings and
application sync state the app writes itself) is preserved. The file is only
rewritten when one of the keys above holds a different value, and the change
test compares **values, not bytes**: Prowlarr re-serialises `config.xml` with
its own serializer whenever settings are saved, and byte-comparing would
report a change on every reconciliation and restart the container in a loop.

Two upstream behaviours drive that rule:

- A key that appears **twice** is treated as absent by the app, which then
  appends its own default, so Bloud only ever writes single, well-formed keys
  through `pkg/xmlutil`.
- A `config.xml` that does not parse makes the app refuse to boot.

## API calls

Base URL `http://localhost:9696`, **API root `/api/v1`**: Prowlarr never moved
to v3, unlike Sonarr and Radarr. Every request carries the key from
`config.xml` in `X-Api-Key` (the API also accepts `?apikey=`). The JSON uses
camel-case keys with string enums, unlike the PascalCase enum names in
`config.xml`.

| Call | Purpose |
|------|---------|
| `GET /api/v1/config/host` | Read the host config; compare `authenticationMethod` case-insensitively with `external`. |
| `PUT /api/v1/config/host` | Only when it differs: set `authenticationMethod=external`, `authenticationRequired=enabled` in the document just read (read-modify-write, so the app's own `branch`/`allowedHosts` values are echoed back; the endpoint rejects a null `AllowedHosts` or an empty `Branch`). |
| `GET /api/v1/config/host` | Re-read and fail loudly, naming the observed value, if the instance still does not report `external`. |
| `GET /api/v1/applications` | Read the application-sync list (the PVRs Prowlarr pushes indexers to). |
| `POST /api/v1/applications` | Create one application entry. Answers `201 Created` with the stored document, or `400` plus a validation-error array when the PVR rejects the configuration. |
| `DELETE /api/v1/applications/{id}` | Remove an entry. Answers `200` with an empty body, whether or not the id existed. |
| `POST /api/v1/applications/test` | Validate an entry (including the connection to the PVR) **without saving it**. `200 {}` on success, `400` with a validation-error array on failure (e.g. `API Key is invalid`). Because it runs the resource's shared validator it also rejects a `name` an existing entry already holds (`Should be unique`), so it is only meaningful for a document that is not stored yet. |

PostStart runs all of this on every reconciliation. A settings edit through the
Prowlarr UI can rewrite `config.xml` and flip the instance back to its forms
login; the next pass repairs it and logs `repaired external authentication` at
Info.

## Indexer sync (Prowlarr → Sonarr / Radarr)

Bloud's other apps declare their providers; Prowlarr owns this direction, so
`metadata.yaml` declares the PVRs as an optional `pvr` integration and this
configurator does the wiring. For every PVR that is installed, `PostStart`
ensures exactly one entry in Prowlarr's application-sync list.

### Discovery

Two facts come from outside Prowlarr, and the framework deliberately does not
supply either:

| Fact | How Bloud gets it |
|------|-------------------|
| Is the PVR installed? | Probe `http://localhost:<port>/ping` on the host: Servarr's anonymous readiness route (`200` up, `503` while booting). Same approach as `apps/seerr`'s Jellyfin probe. |
| What is the PVR's API key? | `pkg/servarr.SiblingConfigPath(BLOUD_DATA_DIR, "<app>")` → `<BLOUD_DATA_DIR>/<app>/config/config.xml`, then its `<ApiKey>` (the file-convention approach `apps/navidrome` uses for Authentik's token). |

The ports are constants in this package, because a `pvr` integration carries no
addresses. Each one names its provider:

| PVR | Provider metadata | Probe | Container address |
|-----|-------------------|-------|-------------------|
| Sonarr | `apps/sonarr/metadata.yaml` (`port: 8989`) | `http://localhost:8989/ping` | `http://apps-sonarr:8989` |
| Radarr | `apps/radarr/metadata.yaml` (`port: 7878`) | `http://localhost:7878/ping` | `http://apps-radarr:7878` |

A provider port change is therefore a greppable edit in two places. The
`pvr:` integration is what makes the graph order Prowlarr after an installed PVR
and re-run this `PostStart` when that PVR changes state: it is the ordering and
retry mechanism, not the addressing.

The wiring addresses containers on `apps-net`, never the host: the calls stay
inside the container network and never traverse Traefik, so forward-auth is not
in the path.

### The document Bloud writes

```json
{"name":"Sonarr (Bloud)","implementation":"Sonarr","configContract":"SonarrSettings",
 "syncLevel":"fullSync","tags":[],
 "fields":[{"name":"prowlarrUrl","value":"http://apps-prowlarr:9696"},
           {"name":"baseUrl","value":"http://apps-sonarr:8989"},
           {"name":"apiKey","value":"<Sonarr config.xml ApiKey>"}]}
```

Radarr is identical with `Radarr (Bloud)` / `Radarr` / `RadarrSettings` /
`http://apps-radarr:7878`. The name is Bloud's own, not the implementation's:
Prowlarr requires a unique name per entry, so `Sonarr` would collide with an
entry the operator added under that name. Entries Bloud wrote before, and entries
the operator renamed, are recognized by their **address** instead (see
"Idempotency" below) and keep their name.

`syncCategories` and `animeSyncCategories` are omitted so the instance keeps its
own schema defaults (`[5000,5010,5020,5030,5040,5045,5050,5090]` and `[5070]` on
the pinned image). `syncLevel: fullSync` is the level that also removes an
indexer from the PVR again after it is deleted in Prowlarr.

A `GET` returns the whole schema for each entry, not just the fields Bloud
wrote: `syncCategories` is an array of numbers, `syncAnimeStandardFormatSearch`
is a boolean, and an empty advanced field has no `value` key at all. The reader
tolerates that (only string values are captured, everything else is ignored),
so an unrelated schema field can never make the list undecodable.

### The `prowlarrUrl` / `baseUrl` default trap

Both fields have `http://localhost:*` schema defaults (Prowlarr's own
`SonarrSettings`/`RadarrSettings` constructors set them). Inside the Prowlarr
container `localhost` is **Prowlarr itself**, so an application created with the
defaults syncs indexers that point the PVR back at Prowlarr's port instead of at
the Prowlarr instance the PVR should query. Both URLs are therefore always sent
explicitly. An entry holding the defaults is *not* Bloud's (the address is part
of the entry's identity), so it is left alone rather than rewritten, and Bloud
creates its own entry beside it.

### Idempotency, repair, and the masked API key

- The reconcile is `GET` → compare → write only on difference. A new or drifted
  entry is **tested before it is created**: `POST /api/v1/applications/test`
  validates the document and the connection to the PVR without saving anything,
  so a PVR that is unreachable or rejects the key fails the node (the error
  names the PVR and the HTTP status) and leaves **no half-written entry**
  behind. Prowlarr runs the same test again inside the create, which is why a
  rejection can also surface there.
- That order is not a preference, it is a constraint: `/applications/test` runs
  the resource's shared validator, which rejects a document whose `name` another
  entry already holds (`400 Should be unique`). Testing after the create
  therefore always fails against a real instance (verified on the pinned image).
- **Identity is the address.** `GET` → `findApplication` matches the entry on
  `implementation` **and** `baseUrl` (the PVR's container address Bloud writes).
  The name is not part of the identity (it is the field the operator changes in
  Prowlarr's UI, so a renamed entry is not drift and is not rewritten), and
  neither is the implementation alone: an operator may keep their own entry for
  the same PVR (a remote one, a second instance), and Bloud neither overwrites
  nor prunes it. With the address fixed, the reserved name means Bloud can always
  create its own entry beside one of theirs.
- An entry that matches is a no-op: no `POST`, no `PUT`, no test call. Drift on
  the sync level or on a **readable** key is repaired with `PUT
  /api/v1/applications/{id}` (the same validator and connection test as a create,
  run against the entry's own id), so the name and the tags the operator owns on
  it survive. (A delete + create, the earlier approach, discarded them.)
- **Prowlarr never returns a stored key.** Every non-empty field it marks
  `PrivacyLevel.ApiKey` (which both `SonarrSettings.ApiKey` and
  `RadarrSettings.ApiKey` are) comes back as `********`, so a stored key cannot
  be compared. The comparison therefore treats a masked `apiKey` as matching, and
  the match is then *verified by use*: `POST /api/v1/applications/testall` makes
  the instance test the entries it holds and answers `{id, isValid}` per entry.
  An entry the instance reports unreachable (a PVR that was purged and
  reinstalled keeps its address but mints a fresh key) is re-pushed in place
  with the key its `config.xml` holds now. A *readable* key (empty or absent) is
  compared verbatim, so a keyless entry is repaired. Without the verdict (an
  instance that cannot answer) the masked match is accepted for that pass rather
  than rewritten on every reconciliation.
- Prowlarr rejects a second entry with the same `name` with `400` and the
  message `Should be unique`; that is classified as already-done, not as an
  error, because an entry created outside Bloud is not a misconfiguration. With
  the reserved name, a collision means the operator created an entry under it,
  which is logged and left exactly as it is.

### Pruning

A PVR that does not answer its `/ping` probe is not installed (or not up yet):
Bloud skips that link and logs at Info. When Bloud had wired that PVR before, it
also deletes the application entry: an uninstall must not leave Prowlarr pushing
indexers at a hostname that no longer resolves, and the key left behind in
`<BLOUD_DATA_DIR>/<app>/config/config.xml` must not be able to resurrect the
link. The prune names the removed application in the log. It deletes the entry at
Bloud's own address only: an operator's entry for the same implementation points
elsewhere and is theirs. The prune runs when `PostStart` runs, so an uninstall
alone leaves the entry until Prowlarr's next full lifecycle pass: the framework
does not re-queue consumers when a provider's node is removed
(`docs/plans/media-stack-integration.md` §9 F2).

A sibling that answers on its port but has no `ApiKey` on disk is a different
case: it cannot be wired at all, and that is a misconfiguration rather than a
race, so it is an error. A sibling that answers and then fails *transiently* (a
5xx, a connection reset mid-call) is logged and retried on the next
reconciliation instead; ERROR is terminal in the orchestrator, so a restarting
PVR must not park this app in `failed`.

## Security consequence (read this)

Servarr's `External` mode is **not** header-based SSO: the app installs
`NoAuthenticationHandler`, reads **no username header at all**, and treats
every request that reaches it as authenticated. There is no app-side auth to
fall back on.

Traefik's forward-auth middleware is therefore the *only* gate in front of
Prowlarr, which means:

- the published host port **9696 is unauthenticated**; anything that can
  reach `localhost:9696` (or the host's IP on that port) has full admin access
  to the instance (and, through the sync payloads, to the API keys of the
  PVRs it manages);
- the port must never be exposed to untrusted hosts. Bloud's dev VM does not
  port-forward it off the machine; do not publish it on an interface reachable
  from the LAN or the internet.

Bloud routes by host name, so the middleware applies to the app subdomain
regardless of the port.

## Files

| File | Purpose |
|------|---------|
| `apps/prowlarr/metadata.yaml` | Container, volume, healthcheck, forward-auth SSO declaration, optional `pvr` integration |
| `apps/prowlarr/configurator.go` | Directory creation, `config.xml` pre-seed, auth verification, PVR application reconcile |
| `apps/prowlarr/api.go` | Typed surface over `/api/v1/applications` (list/create/delete/test) |
| `apps/prowlarr/registration.go` | Registers the `apps-prowlarr` factory |
| `apps/prowlarr/configurator_test.go` | Paths/ports/wiring for this app, with `httptest` fakes for Prowlarr and the PVR probes |
| `services/host-agent/pkg/servarr/config.go` | Shared `config.xml` reader/writer, `SiblingConfigPath` |
| `services/host-agent/pkg/servarr/client.go` | Shared `X-Api-Key` client and auth verification |
| `services/host-agent/pkg/servarr/config_test.go`, `client_test.go` | The shared behaviour's test matrix (create/idempotence/preserve/repair) |

## Verification

```bash
cd services/host-agent && go test ./pkg/servarr/...
cd apps && go test ./prowlarr/...
```

Against a running stack, the wired state is one `GET` per PVR:

```bash
curl -s -H "X-Api-Key: $(grep -o '<ApiKey>[^<]*' <BLOUD_DATA_DIR>/prowlarr/config/config.xml | cut -d'>' -f2)" \
  http://localhost:9696/api/v1/applications
```

Two entries (`Sonarr`, `Radarr`), each with `prowlarrUrl: http://apps-prowlarr:9696`
and `baseUrl: http://apps-<pvr>:<port>`; the `apiKey` field always reads back as
`********`.

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| Instance asks for a username/password | `AuthenticationMethod` was flipped back to a forms mode (or the key is duplicated). Reconcile: PostStart re-reads `/api/v1/config/host` and repairs it. |
| `401` from the API in host-agent logs | The `ApiKey` in `config.xml` does not match what the app has loaded (usually a stale container). Reconcile restarts the container on config change; check for a duplicated `ApiKey` element. |
| `GET /api/v1/applications` is empty although a PVR is installed | The PVR's `/ping` did not answer: it is not installed, is still booting, or its published port moved (update the constant in `configurator.go`). The skip is logged at Info with the probe error. |
| An application entry keeps being replaced on every reconciliation | Its `baseUrl`/`prowlarrUrl` are not the container addresses: most often Prowlarr's own `http://localhost:*` defaults, which inside the container mean Prowlarr itself. |
| Sync to Sonarr/Radarr fails with an auth error | Prowlarr is configured with the public URL (through Traefik, so forward-auth intercepts) instead of the container address, or the PVR rejected Prowlarr's key. Use `apps-sonarr:8989` / `apps-radarr:7878` and re-test the connection in Prowlarr's UI. |
| `prowlarr: testing the Sonarr application → 400 ... API Key is invalid` in host-agent logs | The PVR rejected the key read from its own `config.xml`: the running container has not picked that key up (the file was rewritten after the app booted) or it was edited by hand. Restart the PVR; Bloud re-reads the file on every pass. |
| `prowlarr: testing the Sonarr application → 400 ... Should be unique` | Another entry in Prowlarr already uses the name, and it is not the PVR entry Bloud manages (a different implementation renamed to `Sonarr`). Rename or remove it; Bloud will not overwrite an entry it cannot identify. |
| Container never becomes healthy | `/ping` only turns 200 once the database and web host are up; first boot on a slow disk can use most of the 24 × 5 s window. |
