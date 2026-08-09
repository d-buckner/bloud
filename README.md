# Bloud

An open-source home server. You add an app; the reverse proxy, SSL, SSO, and
databases get set up for you.

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
[![Status: Alpha](https://img.shields.io/badge/Status-Alpha-orange.svg)]()

---

## The problem

Self-hosting is kind of unreasonably hard. Installing one service is manageable.
Everything around it is not. Every service needs its own reverse proxy rules, SSL, SSO
wiring, and database, and keeping it all connected and running is a pile of manual,
easy-to-forget glue.

To connect services together, you typically have to:

- Generate an API key in one service and paste it into the other
- Create an OAuth client in your identity provider (client ID, secret, callback URL)
  and paste those back into the first service
- Register each service with the reverse proxy separately
- Provision and wire up a database for each
- Remember all of it when you reinstall or migrate

On every other platform, you are the integration layer. Bloud flips this. Apps declare
what they provide and what they consume. Bloud holds that integration knowledge and does
the wiring itself, on install and on every restart.

The broader goal is that digital ownership and control shouldn't be reserved for people
who enjoy being their own sysadmin. Owning your photos, music, and data should be
accessible to more than the self-hosting crowd. Sharing them should be too.

## What you get

Install Bloud on a Debian box and get:

- One dashboard for all your services
- One account and a shared login across every app
- One-click app installation, dependencies included
- Automatic inter-app configuration: API keys, OIDC clients, LDAP setup
- Automatic routing through Traefik
- Reliable reconciliation after failures and reboot
- Per-app databases, isolated from each other
- Sharing with people who don't need to manage anything

Bloud's differentiator isn't container installation. It's that apps declare what they
provide and consume, and Bloud keeps those relationships working.

## How it works

Each app ships a portable manifest declaring its integrations:

```yaml
integrations:
  database:
    required: true
    compatible: [{ app: postgres, default: true }]
  sso:
    required: false
    compatible: [{ app: authentik }]
```

When you install an app, Bloud resolves its dependency graph and starts everything in
order. Apps that need storage (like Immich) declare their own PostgreSQL and Redis
containers, each with its own isolated database. Apps that don't (like Jellyfin) just
declare the integrations they use.

```
Level 0: traefik          ← System infra, starts first
Level 1: jellyfin          ← Proxy + SSO; no database of its own
Level 1: immich             ← Self-contained: postgres + redis + server + ML
```

Apps run as rootless Podman containers managed directly by a small Go service
(`host-agent`) that handles orchestration, configuration, and the web dashboard. It
uses an intent-driven reconciler: all changes flow through a queue, and the reconciler
continuously makes reality match what you asked for. It's safe to retry after crashes,
failures, and reboots, and it makes no changes when the system already matches.

## App catalog

The catalog is deliberately small. Each app ships a verified support contract covering
install, shared login, persistence, reboot, and removal. We'd rather support fewer apps
well.

| Category | App | What it gives you |
|---|---|---|
| **Infrastructure** | Traefik | Reverse proxy and routing (system) |
| **Infrastructure** | Authentik | Identity provider (one login everywhere) |
| **Media** | Jellyfin | Movies, TV, and music streaming |
| **Media** | Navidrome | Your music library with Subsonic-compatible clients |
| **Photos** | Immich | Private photo and video management |

## One login everywhere

Apps get SSO automatically, using whatever strategy fits them:

| Strategy | How it works | Apps |
|---|---|---|
| **LDAP** | Authentik supplies credentials for apps that don't speak OAuth2 | Jellyfin |
| **Forward Auth** | Traefik asks Authentik before reaching the app | Navidrome |

Native-protocol clients (a Subsonic music player, a TV app talking to Jellyfin) have
their own documented login path.

## Sharing

Self-hosting's other barrier: even if you can run software, your friends and family
usually can't. Bloud's sharing is built on Tailscale or a self-hosted Headscale so the
other person stays a guest, not a sysadmin.

- **Per-app sharing.** Share Jellyfin with your parents without sharing the rest of
  your server.
- **Direct, revocable invites.** A single-use token for a specific person, revocable at
  any time.
- **Nothing to install on their side.** A friend's Bloud instance proxies your shared
  app locally, so even a TV or game console can use it. Smart clients can connect
  directly for lower latency.

Sharing work is in progress (Phase 6 of the [release plan](SPEC.md)).

## Status

Alpha. We're working on the sharing and federation layer, then packaging for a
one-command install on Debian 13.

## Local development

Development runs inside a **Lima VM** (Debian 13 with rootless Podman).

### Prerequisites

```bash
brew install lima
npm run setup    # Check prerequisites + build ./bloud CLI
```

### First-time setup

```bash
limactl create --name=bloud-dev dev/lima.yaml
limactl start bloud-dev
limactl shell bloud-dev bash dev/setup.sh
```

### Daily development

```bash
./bloud dev              # Build + deploy + run host-agent (Ctrl-C to stop)
./bloud stop             # Stop host-agent
./bloud status           # Lima VM + host-agent status
./bloud services         # App container status
./bloud logs             # Stream host-agent logs
./bloud install <app>    # Install an app via API
./bloud uninstall <app>  # Uninstall an app via API
```

### Validation

```bash
./bloud validate                    # Changed-file-based (default)
./bloud validate --tier fast        # Unit tests only (~30s)
./bloud validate --tier integration # Against real services in Lima VM
./bloud e2e lifecycle              # Full build → deploy → install → verify → uninstall
```

## Project structure

```
bloud/
├── apps/                          # App definitions
│   ├── jellyfin/
│   │   ├── metadata.yaml          # Integrations, SSO, port, container spec
│   │   ├── configurator.go        # PreStart/PostStart runtime hooks
│   │   └── icon.png
│   ├── authentik/
│   ├── immich/
│   ├── navidrome/
│   └── traefik/
│
├── services/host-agent/           # Go backend + Svelte frontend
│   ├── cmd/host-agent/            # Entry point, bootstrap
│   ├── internal/
│   │   ├── orchestrator/          # Intent queue, reconciler, container management
│   │   ├── catalog/               # App discovery from metadata.yaml
│   │   ├── integration/           # Typed integration resolver
│   │   ├── store/                 # SQLite persistence
│   │   └── api/                   # HTTP API (chi router)
│   ├── pkg/
│   │   ├── authentik/             # Authentik REST API client
│   │   └── configurator/          # Configurator interface + helpers
│   └── web/                       # Svelte frontend
│
├── cli/                           # ./bloud command-line tool
├── dev/                           # Lima VM config + setup scripts
└── docs/                          # Architecture and contribution guides
```

## Further reading

- [SPEC.md](SPEC.md): Authoritative first-release plan
- [docs/portable-runtime-architecture.md](docs/portable-runtime-architecture.md): Component overview
- [docs/contributing-apps.md](docs/contributing-apps.md): How to add a new app
- [docs/sharing.md](docs/sharing.md): Federated sharing design and implementation plan

## Contributing

Contributions welcome, especially new apps with verified support contracts. Open an
issue with a clear description before starting significant work.

## License

AGPL v3. See [LICENSE](LICENSE) for details.
