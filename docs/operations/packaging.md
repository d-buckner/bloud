# Packaging

Bloud ships as a Debian package (`.deb`). `./bloud package` builds it from the
repository. There is no `bloud init` preflight yet, so a fresh install still
needs its first-run host configuration done in the dashboard (Settings, then
Hosts).

## Building

```bash
./bloud package                     # host architecture, version from git describe
./bloud package --arch amd64        # explicit architecture
./bloud package --version 1.2.3     # explicit version
./bloud package --out dist          # output directory (default: dist)
```

The command builds the host-agent binary for `linux/<arch>`, builds the frontend
static bundle, stages the on-disk catalog, and hands the payload to
[nfpm](https://nfpm.goreleaser.com/) through `packaging/nfpm.yaml.tmpl`. The
output is `dist/bloud_<version>_<arch>.deb`.

`.github/workflows/release.yml` runs the same command on every push to `main`
(and on manual dispatch) and uploads the `.deb` as a workflow artifact.

## Install layout

| Path | Contents |
|---|---|
| `/usr/lib/bloud/host-agent` | host-agent binary |
| `/usr/lib/bloud/web/build` | frontend static bundle served by host-agent |
| `/usr/share/bloud/apps` | app catalog (`metadata.yaml` plus icons) |
| `/usr/lib/systemd/user/bloud-host-agent.service` | user service unit |
| `/etc/sysctl.d/99-bloud-unprivileged-ports.conf` | lets the rootless Traefik container bind port 80 |
| `/var/lib/bloud` | runtime data: SQLite database, `secrets.json`, Traefik dynamic config |

## Service model

The package installs a dedicated unprivileged `bloud` system user and runs the
host agent as a user-level systemd service (`bloud-host-agent.service`) under
it. This matches the rootless Podman model the runtime assumes (see the
"Supported Environment" section of [../specs/spec.md](../specs/spec.md)):

- `postinst` creates the user, adds its `/etc/subuid` and `/etc/subgid` ranges,
  creates `/var/lib/bloud`, enables linger, and enables and starts the user
  service.
- Traefik runs in a host-network container. The packaged sysctl drop-in
  (`net.ipv4.ip_unprivileged_port_start=0`) lets that rootless container bind
  port 80, the canonical Traefik entrypoint.
- `postrm purge` removes the user and `/var/lib/bloud`; a plain remove or an
  upgrade keeps the data directory.

## Status

Package building and installation are implemented. A `bloud init` preflight
command and the clean-Debian acceptance run remain part of Phase 7 in
[../specs/spec.md](../specs/spec.md).
