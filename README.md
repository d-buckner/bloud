# Bloud

An open-source home server. You add an app; the reverse proxy, SSL, SSO, and the wiring between apps happen automatically.

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
[![Status: Alpha](https://img.shields.io/badge/Status-Alpha-orange.svg)]()

Bloud is for people who want to own their data without becoming a sysadmin. Install it on
a Debian box, add apps from the catalog, and the parts nobody enjoys happen on their own:
routing, logins, databases, API keys, and the connections between apps. It keeps them
working after a crash or a reboot.

## The problem

Self-hosting one service is manageable. Everything around it is not. Every app needs its
own reverse proxy rules, its own login story, its own database, and its own way of talking
to the other apps, so most of the work is manual glue that is easy to forget.

To connect two services, you usually have to:

- Generate an API key in one service and paste it into the other
- Create an OAuth client in your identity provider, then paste the client ID, secret, and
  callback URL back into the first service
- Register each service with the reverse proxy separately
- Provision a database and wire it up
- Remember all of it when you reinstall or migrate

On every other platform, you are the integration layer. Bloud moves that job into the
software. Apps declare what they provide and what they consume; Bloud holds the knowledge,
works out the API keys, OIDC clients, LDAP wiring, database credentials, and routes, and
keeps all of it correct on install, after a crash, and on every reboot.

The larger goal is that owning your photos, music, and documents shouldn't be reserved for
people who enjoy being their own sysadmin. Sharing them shouldn't be either.

## What you get

Install Bloud on a Debian box and get:

- One dashboard for all your services
- One account, shared across every app
- One-click app installation, dependencies included
- Inter-app configuration handled for you: API keys, OIDC clients, LDAP, database credentials
- Routing through Traefik over HTTP (TLS is a planned follow-up)
- Reliable reconciliation after failures and reboots
- A database per app, isolated from the others
- Your server reachable at `localhost` or on your own domain (reach-by-name over real DNS
  or Tailscale is a planned follow-up)
- Sharing with people who don't want to manage anything

Anyone can run `podman run`, so the differentiator isn't container installation. It is the
**engine**: the reconciliation layer that reads what each app declares, works out the wiring,
and keeps those relationships correct forever.

## How it works

Each app ships a declarative manifest that declares its integrations. Install an app and
Bloud resolves its dependency graph, then starts everything in order.

Apps that need storage declare their own containers: Immich brings PostgreSQL and Redis,
AFFiNE brings Postgres and Redis, Paperless-ngx brings Postgres, Redis, Gotenberg, and Tika.
Apps that don't, like Jellyfin, just declare what they consume.

### The engine

The heart of Bloud is a reconciliation control loop, built the way Kubernetes controllers
are. You declare intent (the apps you want, plus what each app provides and consumes); the
engine continuously drives reality toward it, converging the dependency graph level by level
and re-converging on every crash or reboot.

Every change is pushed onto a typed **intent queue**. The engine drains it, resolves the
graph, and runs each node through its lifecycle
(`INITIALIZING → PRESTART → STARTING → POSTSTART → RUNNING`), creating containers, generating
Traefik routes, and provisioning SSO clients and secrets until what is running matches what
you asked for. Like a Kubernetes controller, it is idempotent: when reality already matches
intent, it does nothing. It is also the single writer. The HTTP API submits intents and never
mutates state itself.

That is what makes Bloud self-healing rather than a one-shot installer. The same loop that
installed your apps is the loop that brings them back after a power cut.

## App catalog

The catalog is deliberately small. Each app ships a verified support contract covering
install, shared login, persistence, reboot, and removal. We would rather support fewer apps
well.

| App | Category | What it gives you |
|---|---|---|
| Traefik | Infrastructure | Reverse proxy and routing (system) |
| Authentik | Infrastructure | Identity provider for one login everywhere (system) |
| Jellyfin | Media | Movies, TV, and music streaming |
| Navidrome | Media | Your music library, with Subsonic-compatible clients |
| Immich | Media | Private photo and video management |
| Sonarr | Media | TV series library |
| Radarr | Media | Movie library |
| Prowlarr | Media | Indexer manager, syncing indexers to the PVRs |
| qBittorrent | Media | BitTorrent client with a web interface |
| Seerr | Media | Request and discovery front end for the media server |
| Home Assistant | Productivity | Open-source home automation |
| AFFiNE | Productivity | Knowledge base: docs, databases, whiteboards |
| Hermes | Productivity | AI agent with persistent memory and scheduled automations |
| Paperless-ngx | Productivity | Scans and PDFs become a searchable archive |
| Vaultwarden | Security | Bitwarden-compatible password manager |

### The media stack

Sonarr, Radarr, Prowlarr, qBittorrent, and Seerr are five separate projects that only become
useful once they talk to each other. Bloud wires them:

- Sonarr and Radarr get qBittorrent as a download client, with their own categories and
  library folders (`/shows`, `/movies`).
- Prowlarr syncs your indexers into Sonarr and Radarr, so you configure them in one place.
- Seerr becomes the request front end: someone in the house asks for a title and it lands in
  the right PVR.
- Seerr onboards to Jellyfin by itself, so people sign in with the account they already have.

Where the media comes from is up to you. Bloud ships no media, indexers, or trackers, and
takes no position on what you point it at. It is built for things you have the right to use:

- **Public-domain and freely licensed archives.** The [Internet Archive](https://archive.org)
  is the natural fit: feature films, television, and radio that are free to download and
  share, alongside a growing set of other public-domain and Creative Commons collections.
  Jellyfin plays them, the PVRs organize them, and Seerr gives everyone else a way to ask
  for them.
- **Your own collection.** Movies and shows you have bought or already own, plus home video
  and personal recordings.

Whether a particular source is legal to use depends on where you live and what rights you
hold, and that is your call to make.

## One login everywhere

Apps get SSO automatically, using whichever strategy fits them:

| Strategy | How it works | Apps |
|---|---|---|
| **LDAP** | Authentik supplies credentials for apps that don't speak OAuth2 | Jellyfin |
| **Forward auth** | Traefik asks Authentik before reaching the app | Navidrome, Sonarr, Radarr, Prowlarr, qBittorrent |
| **Native OIDC** | The app speaks OpenID Connect directly to Authentik | Home Assistant, Immich, AFFiNE, Hermes, Paperless-ngx, Vaultwarden |

Native-protocol clients (a Subsonic music player, a TV app talking to Jellyfin) keep their
own documented login path.

## The full graph

Every app in the catalog, the containers each one declares, and the edges that connect them.
The diagram below is generated from the `metadata.yaml` files on every merge to `main`, so it
is a view of the catalog rather than a picture someone has to remember to update.
`./bloud depgraph` prints it and `./bloud depgraph --write` refreshes this section; the same
structure drives the developer graph in the dashboard.

<!-- BEGIN GENERATED DEPENDENCY GRAPH -->
<!-- Generated by `./bloud depgraph --write` from `apps/*/metadata.yaml`. Do not edit by hand. -->

```mermaid
flowchart TD

    subgraph app_authentik["Authentik (system)"]
        c_authentik_postgres["postgres"]
        c_authentik_redis["redis"]
        c_authentik_server["server"]
        c_authentik_worker["worker"]
        c_authentik_ldap["ldap"]
        c_authentik_server --> c_authentik_postgres
        c_authentik_server --> c_authentik_redis
        c_authentik_worker --> c_authentik_postgres
        c_authentik_worker --> c_authentik_redis
        c_authentik_ldap --> c_authentik_server
    end

    subgraph app_traefik["Traefik (system)"]
        c_traefik["traefik"]
    end

    subgraph app_affine["AFFiNE"]
        c_affine_postgres["postgres"]
        c_affine_redis["redis"]
        c_affine["affine"]
        c_affine --> c_affine_postgres
        c_affine --> c_affine_redis
    end

    subgraph app_hermes["Hermes"]
        c_hermes["hermes"]
    end

    subgraph app_homeassistant["Home Assistant"]
        c_homeassistant["homeassistant"]
    end

    subgraph app_immich["Immich"]
        c_immich_postgres["postgres"]
        c_immich_redis["redis"]
        c_immich_ml["ml"]
        c_immich_server["server"]
        c_immich_server --> c_immich_postgres
        c_immich_server --> c_immich_redis
    end

    subgraph app_jellyfin["Jellyfin"]
        c_jellyfin["jellyfin"]
    end

    subgraph app_navidrome["Navidrome"]
        c_navidrome["navidrome"]
    end

    subgraph app_paperless_ngx["Paperless-ngx"]
        c_paperless_ngx_postgres["postgres"]
        c_paperless_ngx_redis["redis"]
        c_paperless_ngx_gotenberg["gotenberg"]
        c_paperless_ngx_tika["tika"]
        c_paperless_ngx["paperless-ngx"]
        c_paperless_ngx --> c_paperless_ngx_postgres
        c_paperless_ngx --> c_paperless_ngx_redis
    end

    subgraph app_prowlarr["Prowlarr"]
        c_prowlarr["prowlarr"]
    end

    subgraph app_qbittorrent["qBittorrent"]
        c_qbittorrent["qbittorrent"]
    end

    subgraph app_radarr["Radarr"]
        c_radarr["radarr"]
    end

    subgraph app_seerr["Seerr"]
        c_seerr["seerr"]
    end

    subgraph app_sonarr["Sonarr"]
        c_sonarr["sonarr"]
    end

    subgraph app_vaultwarden["Vaultwarden"]
        c_vaultwarden["vaultwarden"]
    end

    %% Cross-app integration edges
    app_affine -->|native-oidc| app_authentik
    app_hermes -->|native-oidc| app_authentik
    app_homeassistant -->|native-oidc| app_authentik
    app_immich -->|native-oidc| app_authentik
    app_jellyfin -->|ldap| app_authentik
    app_navidrome -->|forward-auth| app_authentik
    app_paperless_ngx -->|native-oidc| app_authentik
    app_prowlarr -->|forward-auth| app_authentik
    app_prowlarr -->|pvr| app_sonarr
    app_qbittorrent -->|forward-auth| app_authentik
    app_radarr -->|forward-auth| app_authentik
    app_radarr -->|downloadClient| app_qbittorrent
    app_seerr -->|mediaServer| app_jellyfin
    app_seerr -->|pvr| app_sonarr
    app_sonarr -->|forward-auth| app_authentik
    app_sonarr -->|downloadClient| app_qbittorrent
    app_traefik -->|proxy| app_affine
    app_traefik -->|proxy| app_authentik
    app_traefik -->|proxy| app_hermes
    app_traefik -->|proxy| app_homeassistant
    app_traefik -->|proxy| app_immich
    app_traefik -->|proxy| app_jellyfin
    app_traefik -->|proxy| app_navidrome
    app_traefik -->|proxy| app_paperless_ngx
    app_traefik -->|proxy| app_prowlarr
    app_traefik -->|proxy| app_qbittorrent
    app_traefik -->|proxy| app_radarr
    app_traefik -->|proxy| app_seerr
    app_traefik -->|proxy| app_sonarr
    app_traefik -->|proxy| app_vaultwarden
    app_vaultwarden -->|native-oidc| app_authentik
```

_Each box is one app; the nodes inside it are that app's containers, with an arrow from a container to every container it depends on. Arrows between boxes are integrations: a `proxy` arrow is drawn from the proxy to the apps it routes, and an SSO arrow is labeled with the app's strategy (`ldap`, `forward-auth`, `native-oidc`)._
<!-- END GENERATED DEPENDENCY GRAPH -->



## Sharing

Self-hosting has a second barrier: even if you can run software, your friends and family
usually can't. Bloud's sharing is built on Tailscale or a self-hosted Headscale, so the other
person stays a guest rather than becoming a sysadmin.

- **Per-app sharing.** Share Jellyfin with your parents without sharing the rest of your server.
- **Direct, revocable invites.** A single-use token for a specific person, revocable at any
  time.
- **Nothing to install on their side.** A friend's Bloud instance proxies your shared app
  locally, so even a TV or game console can use it. Smart clients can connect directly for
  lower latency.

Sharing is in progress (Phase 6 of the [release plan](docs/specs/spec.md)).

## Status

Alpha. The next milestones are the sharing and federation layer, then packaging for a
one-command install on Debian 13. Known gaps and the plan to close them are tracked in
[docs/operations/tech-debt.md](docs/operations/tech-debt.md).

## AI disclosure

While the high level technical design and architecture are done by me personally, much of the low level implementation is done by LLM. For me, this is done with local models hosted on my own infrastructure. If this does not align with the values you want your software to have, I understand, this project may not be for you.

## Local development

Everything goes through the `./bloud` CLI. `npm run setup` picks your runtime backend, checks
prerequisites, and builds the CLI. `./bloud dev` is the whole loop: it builds host-agent and
the frontend, deploys them to the runtime, and runs the agent (Ctrl-C to stop). There is no
hot reload, so re-run `./bloud dev` after any code change.

### Backends

The runtime is a Debian 13 environment with rootless Podman. Where it runs is a per-checkout
preference: `./bloud setup` chooses it (or the first runtime command prompts and saves the
answer) into the gitignored `.bloud/preferences.yaml`, and `BLOUD_BACKEND` overrides it:

| Backend | Platform | Chosen as | Prerequisites |
|---|---|---|---|
| **Lima** | macOS | automatic: the only applicable backend | `brew install lima` |
| **QEMU** | Linux | the default choice | `qemu-system-x86_64` |
| **Native** | Linux (CI) | a prompt choice, or `BLOUD_BACKEND=native` | podman + user-level systemd |

```bash
npm run setup            # Choose backend, check prereqs, build ./bloud
```

Every backend provisions itself on first run: `./bloud dev` creates the Lima VM from
`dev/lima.yaml`, provisions the QEMU VM under `.bloud/qemu/`, or sets up the native runtime in
`/var/tmp/bloud-native-runtime`, then builds, deploys, and starts the agent. There is no
separate create or start step.

### Daily development

```bash
./bloud dev              # Build + deploy + run host-agent (Ctrl-C to stop)
./bloud stop             # Stop host-agent
./bloud status           # Runtime + host-agent status
./bloud services         # App container status
./bloud logs             # Stream host-agent logs
./bloud install <app>    # Install an app via API
./bloud uninstall <app>  # Uninstall an app via API
./bloud attach           # Shell on the runtime (VM backends)
./bloud reset            # Wipe runtime data (keeps the VM)
./bloud destroy          # Delete the VM
```

Apps are then served through Traefik at `http://<app>.localhost:8080`.

### Apps that need a dev switch

Bloud serves apps over plain HTTP until it has a TLS layer. One app cannot work that way on
its own: **Vaultwarden's web vault refuses any server whose URL is not `https://`**, so past
its sign-in page nothing works (SSO, creating an account, opening the vault). To develop or
test it, opt in when you start the runtime:

```bash
BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP=1 ./bloud dev     # or `1` in your shell before ./bloud e2e
```

The variable is forwarded to the host-agent on every backend. It is off by default, only
honored for `localhost` names, and it weakens a security check in that one app's web client,
so it is for development and browser tests only (CI sets it for the `vaultwarden` job).
Without it Vaultwarden still installs and its server side works; only the browser flows past
the sign-in page fail, and the Playwright spec skips its sign-in rung. See
[`apps/vaultwarden/INTEGRATION.md`](apps/vaultwarden/INTEGRATION.md#plain-http) for what it
does and why.

### Validation

```bash
./bloud validate                     # Changed-file-based (default)
./bloud validate --tier fast         # Unit tests only (~30s)
./bloud validate --tier integration  # Real install/reconcile flow on the runtime
./bloud e2e                          # Playwright against a running ./bloud dev
./bloud e2e lifecycle                # Full build → deploy → install → verify → uninstall
./bloud e2e app                      # Single app's spec on its own runtime (CI)
```

## Project structure

```
bloud/
├── apps/                          # App catalog, one directory per app
│   ├── jellyfin/
│   │   ├── metadata.yaml          # Integrations, SSO, port, container spec
│   │   ├── configurator.go        # PreStart/PostStart runtime hooks
│   │   └── icon.png
│   └── ...                        # affine, authentik, hermes, homeassistant,
│                                  # immich, navidrome, paperless-ngx, prowlarr,
│                                  # qbittorrent, radarr, seerr, sonarr, traefik,
│                                  # vaultwarden
│
├── services/host-agent/           # Go backend + Svelte frontend
│   ├── cmd/host-agent/            # Entry point, bootstrap
│   ├── internal/
│   │   ├── engine/                # ★ The differentiator: the reconcile engine
│   │   │   ├── orchestrator/      #   Typed intent queue + lifecycle reconciler
│   │   │   └── graph/             #   The dependency graph the engine converges
│   │   ├── catalog/               # App discovery from metadata.yaml
│   │   ├── sso/                   # OIDC / LDAP / forward-auth wiring
│   │   ├── secrets/               # Per-instance generated keys
│   │   ├── traefikgen/            # Route generation from the graph
│   │   ├── store/                 # SQLite persistence
│   │   └── api/                   # HTTP API: submits intents, never writes state
│   ├── pkg/
│   │   ├── authentik/             # Authentik REST API client
│   │   ├── servarr/               # Sonarr / Radarr / Prowlarr API client
│   │   └── configurator/          # Configurator interface + helpers
│   └── web/                       # Svelte frontend
│
├── cli/                           # ./bloud CLI (lima / qemu / native backends)
├── e2e/                           # Playwright browser tests
├── dev/                           # VM configs (lima.yaml, qemu.yaml)
├── docs/                          # Specs, architecture, guides, features, operations
├── validation.yaml                # ./bloud validate manifest
└── .github/                       # CI and release workflows
```

## Further reading

- [docs/README.md](docs/README.md): Index of all documentation
- [docs/specs/spec.md](docs/specs/spec.md): Authoritative first-release plan
- [docs/specs/reconciler-spec.md](docs/specs/reconciler-spec.md): Reconciler subsystem design
- [docs/architecture/overview.md](docs/architecture/overview.md): Component overview
- [docs/guides/contributing-apps.md](docs/guides/contributing-apps.md): How to add a new app
- [docs/features/sharing.md](docs/features/sharing.md): Federated sharing design and implementation plan

## Contributing

Contributions welcome, especially new apps with verified support contracts. Open an issue
with a clear description before starting significant work.

## License

AGPL v3. See [LICENSE](LICENSE) for details.
