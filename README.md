# Bloud

**Home Cloud Integration Platform**

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
[![Status: Alpha](https://img.shields.io/badge/Status-Alpha-orange.svg)]()

Self-hosting's hard part isn't installation. Every platform has solved that. The hard
part is when you add a second service and realize the first one doesn't know it exists.

To connect two services manually, you typically:

- Generate an API key in one service, paste it into the second
- Create an OAuth client in your identity provider — client ID, secret, callback URL — paste those back into the first
- Register each service with the reverse proxy separately
- Provision and wire a database for each
- Remember all of this when you reinstall or migrate

On every other platform, *you* are the integration layer. **Bloud flips this.** Apps
declare what they provide and what they consume. Bloud holds the integration knowledge
and wires everything automatically — on install, on restart, forever.

---

## What You Get

Install Bloud on a Debian server and get:

- One web dashboard
- One account and shared login (SSO) across all apps
- One-click app installation
- Automatic inter-app configuration (API keys, OIDC clients, LDAP setup)
- Automatic routing through Traefik
- Reliable reconciliation after failures and reboot
- Per-app databases when needed (each app provisions its own PostgreSQL/Redis)

---

## App Catalog

| Category | Apps |
|---|---|
| **Infrastructure** | Traefik, Authentik |
| **Media** | Jellyfin, Navidrome |
| **Photos** | Immich |
| **Productivity** | Miniflux (RSS) |
| **Network** | AdGuard Home |
| **Utility** | qBittorrent |

---

## SSO Integration

Apps get SSO automatically. Three strategies depending on the app:

| Strategy | How It Works | Example Apps |
|---|---|---|
| **Native OIDC** | App handles OAuth2 itself; Bloud provides credentials | Miniflux, Immich |
| **Forward Auth** | Traefik checks auth with Authentik before reaching the app | AdGuard Home, qBittorrent |
| **LDAP** | Authentik LDAP for apps that don't speak OAuth2 | Jellyfin |

---

## How It Works

Each app declares its integrations in `metadata.yaml`:

```yaml
integrations:
  database:
    required: true
    compatible: [{ app: postgres, default: true }]
  sso:
    required: false
    compatible: [{ app: authentik }]
```

When you install an app, Bloud resolves its full dependency graph and starts things in
order. Apps that need a database (like Immich or Authentik) declare their own
PostgreSQL and Redis containers in `containers:` — each app gets its own isolated
database. Apps that don't need one (like Jellyfin) just declare the integrations they do.

```
Level 0: traefik              ← System infra, starts first
Level 1: jellyfin              ← Proxy + SSO; no database of its own
Level 1: immich                 ← Self-contained: postgres + redis + server + ML
```

Apps run as rootless Podman containers managed by Quadlet systemd units. A Go binary
(`host-agent`) handles orchestration, configuration, and the web dashboard.

---

## Project Structure

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
├── services/host-agent/           # Go backend + SvelteKit frontend
│   ├── cmd/host-agent/            # Entry point, bootstrap
│   ├── internal/
│   │   ├── orchestrator/          # Install/uninstall, intent queue, Quadlet units
│   │   ├── catalog/               # App discovery from metadata.yaml
│   │   ├── integration/           # Typed integration resolver
│   │   ├── store/                 # SQLite persistence
│   │   └── api/                   # HTTP API (chi router)
│   ├── pkg/
│   │   ├── authentik/             # Authentik REST API client
│   │   └── configurator/          # Configurator interface + helpers
│   └── web/                       # SvelteKit frontend
│
└── dev/                           # Lima VM config + setup scripts
```

---

## Local Development

Development uses a **Lima VM** (Debian 13 with rootless Podman).

### Prerequisites

```bash
brew install lima
npm run setup    # Check prerequisites + build ./bloud CLI
```

### First-Time Setup

```bash
limactl create --name=bloud-dev dev/lima.yaml
limactl start bloud-dev
limactl shell bloud-dev bash dev/setup.sh
```

### Daily Development

```bash
./bloud dev              # Build + deploy + run host-agent (Ctrl-C to stop)
./bloud stop             # Stop host-agent
./bloud status           # Lima VM + host-agent status
./bloud services         # App container status
./bloud logs             # Stream host-agent logs
./bloud attach           # Shell into Lima VM
./bloud install <app>    # Install an app via API
./bloud uninstall <app>  # Uninstall an app via API
```

### Validation

```bash
./bloud validate                    # Changed-file-based (default)
./bloud validate --tier fast        # Unit tests only (~30s)
./bloud validate --tier integration # Against real services in Lima VM
./bloud validate --dry-run          # Show what would run
```

### E2E Lifecycle Test

```bash
./bloud e2e lifecycle              # Full: build → deploy → install → verify → uninstall
./bloud e2e lifecycle --host-only  # Skip Playwright, verify host state only
./bloud e2e lifecycle --keep       # Leave running after tests
```

---

## Further Reading

- [SPEC.md](SPEC.md) — Authoritative first-release plan
- [docs/portable-runtime-architecture.md](docs/portable-runtime-architecture.md) — Component overview
- [docs/contributing-apps.md](docs/contributing-apps.md) — How to add a new app
- [docs/sharing.md](docs/sharing.md) — Federated sharing design and implementation plan

---

## Contributing

Contributions welcome. Open an issue with a clear description before starting significant
work.

---

## License

AGPL v3 — See [LICENSE](LICENSE) for details.
