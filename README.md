# bloud

An open-source home server. You add an app; the reverse proxy, the unified login, the database,
and the wiring between apps happen automatically.

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
[![Status: Alpha](https://img.shields.io/badge/Status-Alpha-orange.svg)]()

Self-hosting is kind of unreasonably hard. Not the installing part. The part after: the proxy
rules, the OAuth clients, the database credentials, the API keys you paste from one web UI into
another. Bloud moves that job out of your head and into the software.

## try it

Debian 13, x86_64.

```bash
curl -fsSL https://raw.githubusercontent.com/d-buckner/bloud/main/install.sh | sudo sh
```

Open the dashboard. Set your host under Settings, then Hosts. Install Jellyfin.

That's the whole setup. What it does underneath: the installer fetches the published `.deb`
and hands it to `apt`, which pulls the real dependency set (Podman 5, `uidmap`,
`dbus-user-session`, and the rest). The `.deb` then provisions the machine itself: a dedicated
unprivileged `bloud` user with its own subuid ranges, linger enabled, the sysctl that lets a
rootless container bind port 80, and the user-level host-agent service.

The script is short, and you should read it before piping it to a shell:
[install.sh](install.sh). To do it by hand instead, grab the `.deb` from
[the releases page](https://github.com/d-buckner/bloud/releases) and run
`sudo apt install ./bloud_*.deb`.

**Please don't expose bloud to the public internet yet.** It's alpha, it serves plain HTTP,
and there is no mechanism yet for getting security updates to apps or to Bloud itself. Keep it
on your LAN for now.

## what just happened

When you clicked install:

1. The API pushed an intent onto a typed queue. It did not install anything itself.
2. The engine resolved the dependency graph. Jellyfin needs Authentik; Authentik needs
   PostgreSQL and Redis; everything needs Traefik.
3. Containers came up in topological order, in parallel within each level.
4. Secrets were generated. An LDAP binding was created in Authentik. Routes were written to
   Traefik.
5. Jellyfin was verified through its own API, not through our own bookkeeping.
6. The loop started again. It runs forever, every few seconds, and does nothing when nothing has
   changed.

Step 6 is why the design looks the way it does. Installing an app once is a script. Bringing it
back after a power cut, with nobody watching, is the part that needs an engine. The loop that
installed Jellyfin is the loop that has to recover Jellyfin, so there is no separate recovery
path to write; a reboot is just another disturbance it reads and responds to.

## why an engine

Getting a container running isn't the hard part, `podman run` will do that. The hard part is
everything the container needs from the rest of the system: a route in Traefik, an OIDC client
or an LDAP binding in Authentik, a database with a password nobody has to copy by hand, and all
of it still correct a year later.

Bloud handles that with a **reconciliation loop**, the same shape as a Kubernetes controller.
Each app declares what it provides and what it consumes, the engine resolves those declarations
into a graph, works out the wiring, and then keeps checking its work.

Two rules hold that together:

- **Single writer.** Only the orchestrator writes lifecycle state or performs side effects. HTTP
  handlers submit intents and never mutate anything themselves.
- **Idempotent configurators.** `PreStart` and `PostStart` run on every cycle, not just on
  install. `PreStart` brings the config on disk in line with what the app should have, and does
  nothing when it already matches.

A template can write one config file once. Nothing but a loop keeps every config file in
someone's homelab correct through upgrades, crashes, and reboots.

## the full graph

Every app, the containers each one declares, and the edges that connect them.

CI draws this rather than anyone updating it by hand. A headless browser renders it with the
same components that draw the developer graph inside a running Bloud, fed the catalog snapshot
that `./bloud depgraph --json` builds from every app's `metadata.yaml`. The catalog is the only
input, so the picture can be drawn on a machine with nothing installed.

![The Bloud catalog: every app, the containers it declares, and the integrations between them](docs/assets/dependency-graph.png)

The text form of the same graph is in
[docs/architecture/dependency-graph.md](docs/architecture/dependency-graph.md), and that is
what `--write` refreshes and `--check` gates. Add an app and both of them regenerate; a merge
that touches neither the catalog nor the renderer leaves both alone.

```bash
npm run graph:image   # rebuild the picture locally
```




## catalog

The catalog is small on purpose. A half-supported app is worse than no app at all, because it
looks like an answer right up until the first time you depend on it. Every entry here carries
the same contract and we verify each one of them: install, shared login, persistence, reboot,
removal.

`./bloud catalogdoc --write` generates the list below from every app's `metadata.yaml`, the
same file the graph is drawn from, so a new app shows up here whether or not anyone remembers
to mention it.

<!-- BEGIN GENERATED CATALOG LIST -->
<!-- Generated by `./bloud catalogdoc --write` from `apps/*/metadata.yaml`. Do not edit by hand. -->
- **AFFiNE**: AI-native knowledge base that unifies docs, databases, and whiteboards
- **Calino**: Browser calendar for the CalDAV calendars Bloud already serves
- **Hermes**: Self-improving AI agent with persistent memory, scheduled automations, and a web dashboard
- **Home Assistant**: Open-source home automation platform
- **Immich**: Self-hosted photo and video management
- **Jellyfin**: Free software media system for streaming movies, TV, and music
- **Navidrome**: Modern music server and streamer compatible with Subsonic/Airsonic clients
- **Paperless-ngx**: Document management system that turns scans and PDFs into a searchable archive
- **Prowlarr**: Indexer manager that syncs indexers to Sonarr, Radarr, and other PVRs
- **qBittorrent**: BitTorrent client with a web interface
- **Radarr**: Movie collection manager for Usenet and BitTorrent users
- **Radicale**: CalDAV and CardDAV server for calendars, contacts, and to-do lists
- **Seerr**: Request and discovery manager for your media server
- **Sonarr**: PVR for TV series that monitors, grabs, and organises episodes
- **Vaultwarden**: Lightweight, Bitwarden-compatible password manager

Plus the system apps: **Authentik** (security) and **Traefik** (network).
<!-- END GENERATED CATALOG LIST -->

The media stack is where this shows up most. Sonarr, Radarr, Prowlarr, qBittorrent, and Seerr
are five separate projects that only become a pipeline once they're wired to each other, and
that wiring is the tedious part by hand: add qBittorrent as a download client in each PVR with
its own category and folder, get the indexers from Prowlarr into both, then connect Seerr to
Jellyfin and to the PVRs so a request actually lands somewhere. Bloud does all of it from the
declarations, so five apps is five clicks rather than an afternoon of copy-paste.

We ship no media, no indexers, and no trackers, and Bloud has no view on what you point it at.
It's built for things you have the right to use.

## one login

<!-- BEGIN GENERATED LOGIN TABLE -->
<!-- Generated by `./bloud catalogdoc --write` from `apps/*/metadata.yaml`. Do not edit by hand. -->
| Strategy | Apps |
|---|---|
| **LDAP** | Jellyfin, Radicale, Seerr |
| **Forward auth** | Calino, Navidrome, Prowlarr, qBittorrent, Radarr, Sonarr |
| **Native OIDC** | AFFiNE, Hermes, Home Assistant, Immich, Paperless-ngx, Vaultwarden |
<!-- END GENERATED LOGIN TABLE -->

Clients that speak a native protocol, a Subsonic player or a TV app talking to Jellyfin, keep
the login path their protocol defines. Bloud fronts the web UIs and stays out of the way of
those.

## what is not done yet

Alpha in specific ways, so you know which gaps you're signing up for.

- **No TLS.** Plain HTTP only, and this is the biggest gap. Let's Encrypt on Traefik, or
  Tailscale Serve, is the planned follow-up. Fine on your LAN if you accept it; not acceptable
  off-LAN.
- **Sharing in progress.** Core sharing works. Tailnet outpost auth is still in development.
- **No `bloud init`.** First-run host config happens in the dashboard.
- **Debian 13 only.** A support contract has to be true somewhere before it spreads.
- **The loop is not yet hardened against every failure mode.** The auth bypass is remotely
  forgeable and is the current shipping blocker. Full ledger:
  [docs/operations/tech-debt.md](docs/operations/tech-debt.md).

## developing it

```bash
npm run setup            # pick backend, check prereqs, build ./bloud
./bloud dev              # build + deploy + run (Ctrl-C to stop)
./bloud install jellyfin # through the real API
./bloud validate --tier fast
```

Backends: Lima on macOS (automatic), QEMU on Linux (default), native on Linux CI
(`BLOUD_BACKEND=native`). On the native backend `./bloud dev` hot-reloads: save a
Go file and the host-agent rebuilds and restarts in about 3s with the app containers
left running, and the dashboard hot-reloads through vite. Use `--no-watch` for the
one-shot build-deploy-run. Add `--reset` to wipe the runtime first (the same wipe as
`./bloud reset -y`, no prompt) and come up from empty data. Apps land at
`http://<app>.localhost:8080`.

The integration tier runs the real graph path rather than a shortcut: host-agent deployed as a
systemd user service, Jellyfin installed through `POST /api/apps/jellyfin/install`, the
orchestrator converged, behavioral tests run inside the VM. Tests assert what the app's own API
reports, not what our config values happen to be.

```bash
./bloud validate --tier integration   # real install/reconcile flow
./bloud e2e lifecycle                 # install -> restart -> uninstall -> cleanup
```

## ai disclosure

While the high level technical design and architecture are done by me personally, much of the
low level implementation is done by LLM. For me, this is done with local models hosted on my
own hardware (qwen3.8-flash-next at the time of writing). If this does not align with the values
you want your software to have, I understand and this project may not be for you.

## further reading

- [docs/README.md](docs/README.md): index of all documentation
- [docs/specs/spec.md](docs/specs/spec.md): authoritative first-release plan
- [docs/specs/reconciler-spec.md](docs/specs/reconciler-spec.md): reconciler subsystem design
- [docs/architecture/overview.md](docs/architecture/overview.md): component overview
- [docs/guides/contributing-apps.md](docs/guides/contributing-apps.md): how to add an app
- [docs/features/sharing.md](docs/features/sharing.md): federated sharing design

## license

AGPL v3. See [LICENSE](LICENSE).
