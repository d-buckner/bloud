# qBittorrent Integration

## Status: complete (config + API verification)

qBittorrent is a BitTorrent client with a web UI. Bloud runs the
LinuxServer.io image, hands it Bloud's downloads directory, and puts it behind
Traefik forward-auth: the WebUI's own login is deliberately bypassed, because
qBittorrent has no external/header authentication mode to delegate to
Authentik.

- Image: `lscr.io/linuxserver/qbittorrent:5.2.3_v2.0.14-ls476` (pinned)
- SSO strategy: `forward-auth` (Authentik), no bypass paths: the whole app is
  behind the gate
- WebUI: `http://qbittorrent.localhost:8080` via Traefik, listening on host
  port 8081 inside the VM
- Data: `{{appDataDir}}/config` → `/config`, `{{dataDir}}/downloads` →
  `/downloads`

## Architecture

```
         browser ──► Traefik :8080 ──► forward-auth (Authentik)
                        │                     │
                        │  <id>.<host>        └─ unauthenticated only when Authentik says yes
                        ▼
                  localhost:8081 ──► apps-qbittorrent  (apps-net, :8081)
                                                      └─ /downloads ({{dataDir}}/downloads)
```

One container, `apps-qbittorrent`, on `apps-net`. 8081 is published on the host
because Traefik runs on the host network and routes `<id>.<host>` to
`http://localhost:<metadata port>`.

## The auth model (read this before touching the conf)

qBittorrent's WebUI understands exactly two things: its own username/password
login, and a **subnet whitelist** that skips that login. There is no
trusted-header or external-auth mode (unlike Navidrome's `ND_EXTAUTH_*`), so
Bloud cannot tell qBittorrent "Authentik already let this user in". Instead the
WebUI login is disabled for the addresses requests actually arrive from, and
Traefik's forward-auth middleware becomes the only gate:

> **The published WebUI port is unauthenticated.** Anyone who can reach
> `localhost:8081` on the Bloud host (or the host's port 8081 from elsewhere)
> gets in without a password. Keep it on the host's private side and let only
> Traefik reach it; do not expose 8081 in any public-proxy config, and do not
> add qBittorrent to a host port-forward list.

The window is bounded by CIDR, which is the only knob available: the whitelist
is evaluated against the connecting address, and every request arrives over the
docker network / host gateway, so the entry has to cover that subnet
(`0.0.0.0/0`). This is the same posture as Navidrome's
`ND_EXTAUTH_TRUSTEDSOURCES: 0.0.0.0/0`: the trust is placed in the proxy
boundary, not in the app.

## Managed conf keys

`PreStart` merges only the keys below into
`<appDataDir>/config/qBittorrent/qBittorrent.conf` (host) =
`/config/qBittorrent/qBittorrent.conf` (container). The file is loaded, the keys
are ensured, and it is written back **only when one of them actually changed**:
qBittorrent owns this file, rewrites it on a 5 s dirty timer, and preserves keys
it does not know, so a blanket overwrite would fight the app and a spurious
"changed" would recreate the container on every reconciliation. Keys Bloud does
not manage keep their values (the INI round-trip is value-preserving; comments
and key ordering are not).

| Section | Key | Value | Why |
|---------|-----|-------|-----|
| `[LegalNotice]` | `Accepted` | `true` | Accept the first-run notice; otherwise the app can gate on a prompt no operator can answer in a container. |
| `[Preferences]` | `WebUI\Port` | `8081` | Port the WebUI listens on. Must match the published host port Traefik targets. |
| `[Preferences]` | `WebUI\Address` | `*` | Listen on all container interfaces; reachability is decided by the published host port. |
| `[Preferences]` | `WebUI\ServerDomains` | `*` | Accept any server domain: Bloud routes by host, and users may reach the app under several hosts. |
| `[Preferences]` | `WebUI\HostHeaderValidation` | `false` | See below. |
| `[Preferences]` | `WebUI\AuthSubnetWhitelistEnabled` | `true` | Turn on the subnet bypass. |
| `[Preferences]` | `WebUI\AuthSubnetWhitelist` | `0.0.0.0/0` | The bypass itself (see the auth model above). One CIDR keeps the serialised value stable so an exact comparison never flaps. |
| `[Preferences]` | `WebUI\LocalHostAuth` | `false` | Loopback is in the same trust boundary as the proxy subnet (nothing but Bloud's routing reaches this WebUI), so localhost is not asked for a password either. |
| `[Preferences]` | `Downloads\SavePath` | `/downloads/` | Completed downloads land on the mounted volume. |
| `[Preferences]` | `Downloads\TempPath` | `/downloads/incomplete/` | Directory Bloud pre-creates for incomplete downloads. qBittorrent only uses it when the operator enables "keep incomplete torrents in" (`Downloads\TempPathEnabled`, left at the app's default); the path is set so that switching it on cannot point off the mounted volume. |
| `[Preferences]` | `Connection\UPnP` | `false` | No port-punching through the VM's NAT; the peer port is published explicitly. |

### `WebUI\HostHeaderValidation` must be `false`

With validation on (the default), qBittorrent rejects every request whose
`Host` header carries a port different from the port the WebUI itself is
listening on: it answers **401** with no useful body. Traefik preserves the
browser's header verbatim (`Host: qbittorrent.localhost:8080`), so every
proxied request would be rejected before forward-auth even mattered. Bloud
routes by host in Traefik, so the app-level check adds nothing.

### The `WEBUI_PORT` coupling

LinuxServer.io's init script starts the daemon as
`qbittorrent-nox --webui-port=${WEBUI_PORT}` (default 8080), and
`--webui-port` **overwrites and persists `WebUI\Port`**. So the conf key and the
env var are two views of the same setting:

- `metadata.yaml`: `WEBUI_PORT: "8081"` and published `8081 → 8081`
- `configurator.go`: `webUIPort = 8081`, written as `WebUI\Port`

Change one without the other and the app either listens on a port nothing
routes to, or rewrites the conf back to a different port on every boot.

## Peer port

`TORRENTING_PORT=6881` pins the BitTorrent listen port (qBittorrent 5.x picks a
random one otherwise), and `6881` is published on the host for both **tcp and
udp** (udp is uTP). The env variable is used rather than the conf key because it
is migration-independent: LSIO passes it on the command line, so the value
survives conf rewrites. Opening it is what lets inbound peers connect;
outbound-only torrenting works without it but is far slower.

## Lifecycle

| Phase | Action |
|-------|--------|
| `PreStart` | Creates `<appDataDir>/config/qBittorrent`, `<dataDir>/downloads` and `<dataDir>/downloads/incomplete`, makes each of them (and the conf file, when it exists) writable by the daemon's user *and* by the host agent, then merges the managed keys above. Returns `changed=true` only when the conf content changed. |
| `PostStart` | `GET /api/v2/app/version` (anonymous) must answer **200**. It answers 403 while the requesting address is not whitelisted, so this is the behavioural proof that the running process loaded the bypass, not just that the file on disk says so. Retried on a constant cadence (~30 attempts at 2 s, inside the orchestrator's 150 s PostStart budget). |
| `Remove` | No-op; container and data removal are the orchestrator's job. |

`PreStart` runs on the host and writes as the host-agent's own user; the daemon
runs as LSIO's `abc` (`PUID=1000`, a host subuid under rootless podman). The
image's init chowns `/config` **recursively** but `/downloads` only at its mount
root, non-recursively and only when it is a mount point, so neither the conf
file nor the subdirectories Bloud pre-creates are normalised to the daemon's
user, and both identities have to be able to write them. `PreStart` therefore
chmods the conf file `0666` and the directories `0777` (see the row above), which
is what keeps the merge working after the first boot: `INIFile.Save` rewrites the
conf in place, so a file the container owns would otherwise make the next repair
fail with EACCES and take the node to ERROR.

## Files

| File | Purpose |
|------|---------|
| `apps/qbittorrent/metadata.yaml` | Container, ports (8081 tcp, 6881 tcp+udp), volumes, healthcheck, forward-auth SSO |
| `apps/qbittorrent/configurator.go` | Managed conf keys + PostStart API verification |
| `apps/qbittorrent/registration.go` | Node registration (`apps-qbittorrent`) |
| `apps/qbittorrent/configurator_test.go` | Unit tests for the conf merge, idempotency, and the API probe |
| `apps/qbittorrent/icon.png` | Catalog icon |

## Verification

```bash
cd apps && go test ./qbittorrent/...
```

The tests cover: a fresh conf receiving every managed key (including the port
and the whitelist), an existing conf keeping keys Bloud does not own while the
managed ones are corrected, a second `PreStart` reporting no change, and
`PostStart` succeeding on 200 / failing with the observed status on 403.

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| Every request through Traefik returns 401 | `WebUI\HostHeaderValidation` is back to `true` (the app rewrote the conf, or PreStart did not run). Reconcile; the configurator re-applies it and reports `changed`, which recreates the container. |
| Node goes healthy but the UI shows qBittorrent's login page | The whitelist did not load into the running process. Check `WebUI\AuthSubnetWhitelist=0.0.0.0/0` in the conf, then let `PostStart` fail and the node restart. |
| UI loads but downloads never start | Outbound trackers unreachable, or `Downloads\SavePath` no longer points at `/downloads/`. |
| Peers cannot connect inbound | Host udp 6881 is not published or is filtered upstream; check the port mapping and the VM's firewall. |
| Slow first boot | Expected: LSIO's init chowns the config tree (and the downloads mount root) before the daemon starts. The healthcheck window covers ~60 s. |
| Downloads fail to start with a permission error | `Downloads\TempPath` (`/downloads/incomplete/`) is enabled but that directory is not writable by the daemon: it must be `0777`, which is what `PreStart` enforces; check that `PreStart` ran (a mode an operator tightened is re-applied on the next pass). |
| The WebUI auth keys come back after an operator changed them | Expected: `PreStart` owns them. The reconcile writes the file and reports `changed`, which recreates the container so the daemon loads them. |
