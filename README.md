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
- Automatic routing through Traefik, over HTTP and HTTPS (Bloud generates its own local CA)
- Reliable reconciliation after failures and reboot
- Per-app databases, isolated from each other
- Your server reachable as `localhost`, `bloud.local` (mDNS, no DNS setup), or your own domain
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
| **Productivity** | AFFiNE | AI-native knowledge base: docs, databases, whiteboards |
| **Productivity** | AppFlowy | Open-source Notion alternative: docs and databases |

## One login everywhere

Apps get SSO automatically, using whatever strategy fits them:

| Strategy | How it works | Apps |
|---|---|---|
| **LDAP** | Authentik supplies credentials for apps that don't speak OAuth2 | Jellyfin |
| **Forward Auth** | Traefik asks Authentik before reaching the app | Navidrome |
| **Native OIDC** | The app speaks OpenID Connect directly to Authentik | Immich, AFFiNE |
| **None** | The app manages its own auth; Bloud still wires your account in (e.g. into the app's own OIDC provider) | AppFlowy |

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

Sharing work is in progress (Phase 6 of the [release plan](specs/spec.md)).

## Status

Alpha. We're working on the sharing and federation layer, then packaging for a
one-command install on Debian 13.

## Local development

Everything goes through the `./bloud` CLI. `npm run setup` picks your runtime
backend, checks prerequisites, and builds the CLI; `./bloud dev` is the whole loop — it builds host-agent and the
frontend, deploys them to the runtime, and runs the agent (Ctrl-C to stop).
There is no hot reload: re-run `./bloud dev` after any code change.

### Backends

The runtime is a Debian 13 environment with rootless Podman. Where it runs is
a per-checkout preference: `./bloud setup` chooses it (or the first runtime
command prompts and saves the answer) into gitignored
`.bloud/preferences.yaml`, and `BLOUD_BACKEND` overrides it:

| Backend | Platform | Chosen as | Prerequisites |
|---|---|---|---|
| **Lima** | macOS | automatic — the only applicable backend | `brew install lima` |
| **QEMU** | Linux | the default choice | `qemu-system-x86_64` |
| **Native** | Linux (CI) | a prompt choice, or `BLOUD_BACKEND=native` | podman + user-level systemd |

```bash
npm run setup            # Choose backend, check prereqs, build ./bloud
```

Every backend provisions itself on first run — `./bloud dev` creates the Lima
VM from `dev/lima.yaml`, provisions the QEMU VM under `.bloud/qemu/`, or sets
up the native runtime in `/var/tmp/bloud-native-runtime` — then builds,
deploys, and starts the agent. No separate create/start step.

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

Apps are then served through Traefik at `http://<app>.localhost:8080` (and
`http://<app>.bloud.local` via the port-80 front proxy).

### Validation

```bash
./bloud validate                     # Changed-file-based (default)
./bloud validate --tier fast         # Unit tests only (~30s)
./bloud validate --tier integration # Real install/reconcile flow on the runtime
./bloud e2e                         # Playwright against a running ./bloud dev
./bloud e2e lifecycle               # Full build → deploy → install → verify → uninstall
./bloud e2e app                     # Single app's spec on its own runtime (CI)
```

## Project structure

```
bloud/
├── apps/                          # App catalog (one dir per app)
│   ├── jellyfin/
│   │   ├── metadata.yaml          # Integrations, SSO, port, container spec
│   │   ├── configurator.go        # PreStart/PostStart runtime hooks
│   │   └── icon.png
│   ├── affine/                    # + appflowy/, authentik/, immich/,
│   └── traefik/                   #   navidrome/
│
├── services/host-agent/           # Go backend + Svelte frontend
│   ├── cmd/host-agent/            # Entry point, bootstrap, front-proxy
│   ├── internal/
│   │   ├── orchestrator/          # Intent queue, reconciler, container management
│   │   ├── catalog/               # App discovery from metadata.yaml
│   │   ├── integration/           # Typed integration resolver
│   │   ├── store/                 # SQLite persistence
│   │   ├── tlsca/                 # Local CA + per-host TLS
│   │   ├── mdns/                  # bloud.local advertisement
│   │   └── api/                   # HTTP API (chi router)
│   ├── pkg/
│   │   ├── authentik/             # Authentik REST API client
│   │   └── configurator/          # Configurator interface + helpers
│   └── web/                       # Svelte frontend
│
├── cli/                           # ./bloud CLI (lima / qemu / native backends)
├── e2e/                           # Playwright browser tests
├── dev/                           # VM configs (lima.yaml, qemu.yaml)
├── specs/                         # Release + subsystem specs
├── validation.yaml                # ./bloud validate manifest
└── docs/                          # Architecture and contribution guides
```

## Further reading

- [specs/spec.md](specs/spec.md): Authoritative first-release plan
- [specs/reconciler-spec.md](specs/reconciler-spec.md): Reconciler subsystem design
- [docs/architecture/overview.md](docs/architecture/overview.md): Component overview
- [docs/guides/contributing-apps.md](docs/guides/contributing-apps.md): How to add a new app
- [docs/features/sharing.md](docs/features/sharing.md): Federated sharing design and implementation plan

## Contributing

Contributions welcome, especially new apps with verified support contracts. Open an
issue with a clear description before starting significant work.

## License

AGPL v3. See [LICENSE](LICENSE) for details.
