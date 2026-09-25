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
(and on manual dispatch). It uploads the `.deb` as a workflow artifact and
publishes a GitHub pre-release tagged `deb-<UTC timestamp>` (titled
`bloud <date> (<sha>)`) with the `.deb` attached.

The same job also publishes a rolling `latest` tag carrying the fixed-name
asset `bloud_latest_amd64.deb`, clobbered on every push. That is what
`install.sh` downloads. GitHub's `releases/latest/download/...` shortcut skips
prereleases, and these builds stay prereleases while the project is alpha, so
the rolling pointer is a fixed tag rather than that shortcut. The version-named
release above stays the provenance record.

## Installing

```bash
curl -fsSL https://raw.githubusercontent.com/d-buckner/bloud/main/install.sh | sudo sh
```

[`install.sh`](../../install.sh) is deliberately thin: it fetches the rolling
asset and runs `apt-get install` on it. Everything a fresh install needs on
disk (the `bloud` user, its subuid ranges, `/var/lib/bloud`, linger, the
sysctl drop-in, the user service) is done by the package's own maintainer
scripts, described under "Service model" below. Nothing of that is repeated in
the installer on purpose: `install.sh` is not covered by the packaging tests,
so any provisioning logic that lived there could drift from the package it
installs.

The manual path is the same thing with the download made by hand:

```bash
sudo apt install ./bloud_*.deb
```

## Dependencies

The package declares what the runtime needs as Debian relationships, so a
normal `apt install ./bloud.deb` pulls it and `dpkg -i` reports what is
missing. Each entry is something host-agent or the maintainer scripts invoke
directly; none of it is inherited.

| Package | Floor | Why the package needs it |
|---|---|---|
| `podman` | `>= 5.0.0` | The container runtime, CLI and libpod socket. The floor is the API version host-agent calls: `internal/podman/client.go` addresses `/v5.0.0/...`, which podman 4.x (what Debian 12 ships) does not serve. |
| `uidmap` | | `newuidmap` and `newgidmap` map the `bloud` user into its user namespace. Without them no rootless container starts. |
| `passt \| slirp4netns` | | Rootless networking, as one alternative dependency. pasta (the `passt` binary) is podman 5's default rootless backend, and slirp4netns is the fallback netavark selects when pasta is absent, so either one satisfies the runtime. |
| `dbus-user-session` | | The per-user D-Bus session bus that rootless podman's systemd integration expects. |
| `ca-certificates` | | App images come from `docker.io` and `ghcr.io` over TLS, validated against the system trust store. |
| `adduser` | | `postinst` and `postrm` create and remove the `bloud` user with `adduser` and `deluser`. |
| `systemd` | | The user-level instance runs `bloud-host-agent.service`, and `loginctl enable-linger` (also systemd) keeps it and the podman socket alive across logout and reboot. |

The set is explicit rather than inherited on purpose. Debian's own `podman`
package lists `uidmap`, `dbus-user-session`, `passt`, `slirp4netns`, and
`ca-certificates` as its `Recommends`, and an `--no-install-recommends`
install drops them: a rootless runtime would then fail after install instead
of at it. Depending on what Bloud itself uses is what keeps the relationship
true on a minimal system.

`dpkg` never installs dependencies by itself. `dpkg -i bloud.deb` on a box
without podman unpacks the payload and stops with `dependency problems
prevent configuration of bloud: bloud depends on podman (>= 5.0.0)`. That
message is the dependency model working; `apt install ./bloud.deb`, or
`apt --fix-broken install` after a `dpkg -i`, is what pulls the set.

### Verifying the model

Two layers, one for the rendered control file and one for the real archive:

- `cd cli && go test -run 'TestDepends|TestPackageTemplate' .` checks the
  rendered control fields: the podman floor, the required set, valid Debian
  dependency syntax, and no duplicate packages. It runs in the fast
  validation tier.
- [`packaging/scripts/verify-deps.sh`](../../packaging/scripts/verify-deps.sh)
  checks a built `.deb` against a real Debian 13 index inside a container:
  every declared dependency exists in the release, the podman floor holds
  against the release candidate, apt resolves the set and its install plan
  pulls podman, and `dpkg -i` reports the set as unmet on a system without
  it. [`.github/workflows/deb-verify.yml`](../../.github/workflows/deb-verify.yml)
  runs it on every change to the packaging inputs; the container command for
  running it by hand is in the script header.

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
