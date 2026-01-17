\# Bloud

**Home Cloud Operating System**

An opinionated, zero-config home server OS that makes self-hosting actually accessible. Install apps with automatic SSO integration—no manual OAuth configuration, no reverse proxy setup.

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
[![Status: Alpha](https://img.shields.io/badge/Status-Alpha-orange.svg)]()

> **Status:** Early alpha. Core infrastructure and web UI working.

## The Problem

Self-hosting is overwhelming. Setting up Immich, Nextcloud, and Jellyfin takes hours of configuring reverse proxies, SSL certificates, SSO, and making apps talk to each other.

## The Vision

- Flash USB drive, boot on any x86_64 hardware
- Access web UI, install apps with one click
- Everything pre-integrated: SSO automatic, related apps pre-configured
- Multi-host orchestration for scaling across machines

## What's Working Now

- **NixOS + Rootless Pdman** - Declarative configuration with atomic rollback
- **Automatic SSO** - Apps pre-configured with Authentik OAuth
- **Shared Infrastructure** - Single PostgreSQL/Redis instance per host
- **Service Dependencies** - Health checks, proper startup ordering
- **14 Apps** with NixOS modules and integrations

## Apps

| Category | Apps |
|----------|------|
| **Infrastructure** | PostgreSQL, Redis, Traefik, Authentik |
| **Media** | Jellyfin, Jellyseerr |
| **Productivity** | Miniflux (RSS), Actual Budget, Affine |
| **Network** | AdGuard Home |

## What's Not Built Yet

- Bootable USB image
- Multi-host orchestration
- Automatic backups

## How It Works

Bloud combines declarative configuration with a dependency graph and idempotent reconciliation to eliminate manual setup.

### Integration Graph

Each app declares what it needs: "I need PostgreSQL", "I support OAuth". Bloud builds a dependency graph from these declarations. Enable Miniflux, and the graph knows it needs a PostgreSQL database and Authentik OAuth. Enable any app with OAuth support, and it gets wired to Authentik automatically.

### Idempotent Reconciliation

Instead of fragile setup scripts that run once, Bloud uses Go configurators that reconcile desired state. They run on every startup: "Miniflux should have this OAuth provider configured. Does it? No? Add it. Yes? Move on." This means partial failures self-heal, and you can add new apps without re-running setup for existing ones.

### Declarative Everything

Apps are defined in `metadata.yaml` (what it needs) and `module.nix` (how to run it). Enable an app in your Nix config, rebuild, and NixOS generates the systemd units, creates the container, provisions the database, generates OAuth credentials, and starts everything in the right order.

### Shared Infrastructure

Instead of each app running its own PostgreSQL, all apps share one instance. Bloud creates databases and users automatically. Same for Redis. Fewer containers, less RAM, simpler backups.

### Health-Aware Startup

The graph also determines startup order. Services declare dependencies with health checks. PostgreSQL starts first and becomes healthy. Then Authentik. Then apps that need both. Systemd handles the orchestration.

---

The result: enable apps in a config file, rebuild, and the system figures out what to provision, how to connect everything, and what order to start it.

## Development

Development uses [Lima](https://lima-vm.io/) to run a NixOS VM on your local machine. The `./bloud` CLI manages the VM and development services.

### Prerequisites

| Requirement | macOS | Linux |
|-------------|-------|-------|
| **Lima** | `brew install lima` | [See install guide](https://lima-vm.io/docs/installation/) |
| **Node.js 18+** | `brew install node` | `sudo apt install nodejs npm` |
| **Go 1.21+** | `brew install go` | `sudo apt install golang` |
| **NixOS VM Image** | See below | See below |

> **Note:** `sshpass` is no longer required. SSH key auth is used automatically.

### Quick Start

The `npm run setup` command checks all prerequisites and offers to download the pre-built NixOS VM image (~2.5GB download, ~7GB extracted).

```bash
# 1. Clone and build CLI, check prerequisites and download VM image
git clone https://github.com/d-buckner/bloud.git
cd bloud
npm run setup

# 3. Start development environment
./bloud start
```

### Usage

```bash
./bloud setup      # Check prerequisites, download VM image
./bloud start      # Start dev environment (VM + services)
./bloud stop       # Stop dev services (VM stays running)
./bloud status     # Check what's running
./bloud logs       # View recent output
./bloud attach     # Attach to tmux session (Ctrl-B D to detach)
./bloud shell      # SSH into VM
./bloud rebuild    # Apply NixOS config changes
```

Access the web UI at **http://localhost:8080**

### Troubleshooting

**"Lima is not installed"**
- macOS: `brew install lima`
- Linux: `curl -fsSL https://lima-vm.io/install.sh | bash`

**"NixOS VM image not found"**
- The image must be built on Linux. See "NixOS VM Image" section above.
- Image should be at: `lima/imgs/nixos-24.11-lima.img`

**VM boots but services don't start**
- Check logs: `./bloud logs`
- Rebuild NixOS: `./bloud rebuild`
- Nuclear option: `./bloud destroy && ./bloud start`

See [claude.md](claude.md) for detailed development guidelines.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  Bloud OS (NixOS)                                       │
│                                                         │
│  ┌─────────────────────────────────────────────────┐   │
│  │  Shared Infrastructure (1 per host)             │   │
│  │  PostgreSQL · Redis · Authentik · Traefik       │   │
│  └─────────────────────────────────────────────────┘   │
│                                                         │
│  ┌─────────────────────────────────────────────────┐   │
│  │  Apps (Rootless Podman containers)              │   │
│  └─────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
```

## Contributing

Contributions welcome! See:
- [apps/adding-apps.md](apps/adding-apps.md) - Adding new apps
- [dev-workflow.md](dev-workflow.md) - Development setup

### Getting Started

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Open a Pull Request

### Reporting Issues

[Open an issue](https://github.com/d-buckner/bloud/issues) with:
- Clear description of the problem
- Steps to reproduce (for bugs)
- Your environment

## Documentation

- [dev-workflow.md](dev-workflow.md) - Development setup
- [apps/adding-apps.md](apps/adding-apps.md) - Adding apps
- [services-architecture.md](services-architecture.md) - Technical details

## Philosophy
- **Simplicity Over Features** - Opinionated defaults for 80% of users
- **Privacy by Default** - Everything runs locally on your hardware

## License

AGPL v3 - See [LICENSE](LICENSE) for details. If you modify Bloud and offer it as a service, you must share your changes.

---

**Built with NixOS, Podman Quadlets, Go, and Vite.**
