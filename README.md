# Bloud

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

## Quick Start

```bash
# Install Lima (macOS: brew install lima, Linux: see lima-vm.io)
git clone https://github.com/d-buckner/bloud.git
cd bloud
npm run setup    # Check prerequisites, download VM image
./bloud start    # Start dev environment
```

Access the web UI at **http://localhost:8080**

## Apps

| Category | Apps |
|----------|------|
| **Infrastructure** | PostgreSQL, Redis, Traefik, Authentik |
| **Media** | Jellyfin, Jellyseerr |
| **Productivity** | Miniflux (RSS), Actual Budget, Affine |
| **Network** | AdGuard Home |

---

## How It Works

Bloud makes self-hosting accessible through three core ideas:

1. **Dependency Graph** - Apps declare what they need ("I require a database"). Bloud figures out what to install and wire together.

2. **Declarative Deployment** - NixOS handles the actual containers. Enable an app, rebuild, and NixOS creates systemd services, volumes, networking - atomically.

3. **Idempotent Configuration** - Go configurators run as systemd hooks. StaticConfig (ExecStartPre) handles config files and directories. DynamicConfig (ExecStartPost) handles API calls and integrations. Both run on every service start.

### The Big Picture

Here's what happens when you install Miniflux:

```
┌─────────────────────────────────────────────────────────────────────┐
│                       User clicks "Install Miniflux"                │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│  1. PLANNING                                                        │
│                                                                     │
│     Graph analyzes: "Miniflux needs a database"                     │
│     PostgreSQL is the only compatible option                        │
│     → Auto-select postgres (no user choice needed)                  │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│  2. NIX GENERATION                                                  │
│                                                                     │
│     Write apps.nix:                                                 │
│       bloud.apps.postgres.enable = true;                            │
│       bloud.apps.miniflux.enable = true;                            │
│                                                                     │
│     Run: nixos-rebuild switch                                       │
│     → Containers created, systemd services started                  │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│  3. CONFIGURATION                                                   │
│                                                                     │
│     StaticConfig: Create directories, write Traefik SSO redirect    │
│     HealthCheck: Wait for /healthcheck to respond                   │
│     DynamicConfig: Set admin user theme via Miniflux API            │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│  4. RUNNING                                                         │
│                                                                     │
│     Miniflux is live, connected to postgres, SSO configured         │
└─────────────────────────────────────────────────────────────────────┘
```

## Project Structure

```
bloud/
├── apps/                          # App definitions
│   ├── miniflux/
│   │   ├── metadata.yaml          # What Miniflux needs (integrations, SSO, port)
│   │   ├── module.nix             # How to run the container
│   │   └── configurator.go        # Runtime configuration via API
│   ├── postgres/
│   ├── authentik/
│   └── ...
│
├── services/host-agent/           # The brain - Go server
│   ├── cmd/host-agent/            # Entry points
│   ├── internal/
│   │   ├── orchestrator/          # Install/uninstall coordination
│   │   ├── catalog/               # App graph and dependency resolution
│   │   ├── nixgen/                # Generates apps.nix
│   │   ├── store/                 # Database layer
│   │   └── api/                   # HTTP server
│   ├── pkg/configurator/          # Configurator interface
│   └── web/                       # Svelte frontend
│
├── nixos/
│   ├── bloud.nix                  # Main NixOS module
│   ├── lib/
│   │   ├── bloud-app.nix          # Helper for app modules
│   │   └── podman-service.nix     # Systemd service generator
│   └── generated/
│       └── apps.nix               # Generated by host-agent
│
└── docs/
```

## How Apps Are Defined

Each app has three files that work together:

### metadata.yaml - The Catalog Entry

This tells Bloud what the app is and what it needs:

```yaml
name: miniflux
displayName: Miniflux
description: Minimalist and opinionated feed reader
category: productivity
port: 8085

integrations:
  database:
    required: true              # Must have a database
    multi: false                # Only one at a time
    compatible:
      - app: postgres
        default: true

healthCheck:
  path: /embed/miniflux/healthcheck
  interval: 2
  timeout: 60

sso:
  strategy: native-oidc         # Miniflux handles OAuth2 itself
  callbackPath: /oauth2/oidc/callback
  providerName: Bloud SSO
  userCreation: true
  env:
    clientId: OAUTH2_CLIENT_ID
    clientSecret: OAUTH2_CLIENT_SECRET
    discoveryUrl: OAUTH2_OIDC_DISCOVERY_ENDPOINT
    redirectUrl: OAUTH2_REDIRECT_URL

routing:
  stripPrefix: false            # Miniflux serves at /embed/miniflux when BASE_URL is set
```

The `integrations` section is key. It declares dependencies without hardcoding them. Miniflux needs a database - and postgres is the compatible option.

### module.nix - The Container Definition

This is NixOS configuration for running the container:

```nix
mkBloudApp {
  name = "miniflux";
  description = "Miniflux RSS reader";
  image = "miniflux/miniflux:latest";
  port = 8085;
  database = "miniflux";  # Auto-creates postgres DB

  environment = cfg: {
    RUN_MIGRATIONS = "1";
    CREATE_ADMIN = "1";
    ADMIN_USERNAME = cfg.adminUsername;
    BASE_URL = "${cfg.externalHost}/embed/miniflux";
  };
}
```

The `mkBloudApp` helper handles the boilerplate - creating systemd services, setting up podman, managing volumes. When you specify `database = "miniflux"`, it automatically creates that database in the shared postgres instance.

### configurator.go - Runtime Configuration

Configurators run as systemd hooks (ExecStartPre and ExecStartPost):

```go
type Configurator struct{}

func (c *Configurator) Name() string { return "miniflux" }

// StaticConfig runs as ExecStartPre - before container starts
func (c *Configurator) StaticConfig(ctx context.Context, state *AppState) error {
    // Write Traefik SSO redirect config if SSO integration is available
    if _, hasSSO := state.Integrations["sso"]; hasSSO {
        return os.WriteFile(
            filepath.Join(c.traefikDir, "miniflux-sso.yml"),
            []byte(traefikSSOConfig),
            0644,
        )
    }
    return nil
}

// HealthCheck waits for the app to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
    url := fmt.Sprintf("http://localhost:%d/embed/miniflux/healthcheck", c.port)
    return configurator.WaitForHTTP(ctx, url, 60*time.Second)
}

// DynamicConfig runs as ExecStartPost - after container is healthy
func (c *Configurator) DynamicConfig(ctx context.Context, state *AppState) error {
    // Wait for app to be ready
    if err := c.HealthCheck(ctx); err != nil {
        return err
    }
    // Set light theme for admin user via Miniflux API
    return c.setUserTheme(ctx, 1, "light_serif")
}
```

## The Dependency Graph

When you install an app, Bloud builds a graph of what's needed:

```
User wants: Miniflux
            │
            ▼
┌─────────────────────────────────────┐
│  Miniflux.integrations:             │
│    database: required               │
│      compatible: [postgres]         │
└─────────────────────────────────────┘
            │
            ▼
Is postgres installed? No
            │
            ▼
┌─────────────────────────────────────┐
│  Install Plan:                      │
│    AutoConfig (only 1 option):      │
│      - database → postgres          │
│    No user choices needed           │
└─────────────────────────────────────┘
```

The graph also determines **execution order**. Apps that provide services must be configured before apps that consume them:

```
Level 0: postgres, redis          ← Infrastructure, no dependencies
         │
Level 1: authentik                ← Depends on postgres + redis
         │
Level 2: miniflux, jellyfin       ← Depend on postgres and authentik
```

Configuration runs level by level. Miniflux's DynamicConfig can assume postgres is already healthy.

## The Configuration Lifecycle

Configurators run as systemd hooks during service start:

```
┌─────────────────────────────────────────────────────────────────────┐
│  ExecStartPre: StaticConfig                                         │
│  ───────────────────────────                                        │
│  Runs BEFORE container starts                                       │
│                                                                     │
│  • Create directories (container will mount them)                   │
│  • Write config files (container reads on startup)                  │
│  • Generate Traefik routing rules                                   │
│                                                                     │
│  If this fails, container won't start                               │
└─────────────────────────────────────────────────────────────────────┘
                              │
                              ▼
                    [ Container starts ]
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│  HealthCheck (built into ExecStartPost)                             │
│  ───────────────────────────────────────                            │
│  Waits for the app to be ready                                      │
│                                                                     │
│  • Poll an HTTP endpoint until it responds                          │
│  • Check database connectivity                                      │
│  • Verify the app initialized properly                              │
└─────────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│  ExecStartPost: DynamicConfig                                       │
│  ────────────────────────────                                       │
│  Runs AFTER container is healthy                                    │
│                                                                     │
│  • Configure app via its REST API                                   │
│  • Set up integrations                                              │
│  • Register OAuth clients                                           │
│                                                                     │
│  This is where the "magic wiring" happens                           │
└─────────────────────────────────────────────────────────────────────┘
```

### Why Static vs Dynamic?

The key distinction is whether a restart is needed:

| Phase         | Restart Required | Examples                                      |
| ------------- | ---------------- | --------------------------------------------- |
| StaticConfig  | Yes              | Config files, environment vars, certificates  |
| DynamicConfig | No               | API calls, database records, runtime settings |

**StaticConfig** handles things the app reads on startup. If you change a config file while the app is running, it won't see the change until restart.

**DynamicConfig** handles things that can change while running. API calls modify the app's internal state immediately.

Example: Miniflux's SSO redirect config is written to a Traefik config file - that's StaticConfig. But setting the admin user's theme via the Miniflux API applies immediately - that's DynamicConfig.

### Idempotency

Every phase must be safe to run repeatedly:

```go
// GOOD: Idempotent - writes same content every time
func (c *Configurator) StaticConfig(ctx context.Context, state *AppState) error {
    return os.WriteFile(configPath, []byte(config), 0644)
}

// GOOD: Idempotent - API call sets to desired state
func (c *Configurator) DynamicConfig(ctx context.Context, state *AppState) error {
    return c.setUserTheme(ctx, 1, "light_serif")  // Always sets to light
}

// BAD: Not idempotent - appends on every call
func (c *Configurator) StaticConfig(ctx context.Context, state *AppState) error {
    f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
    f.WriteString("new line\n")  // Grows forever!
    return nil
}
```

If your configurator isn't idempotent, you'll have problems - systemd may run the hooks multiple times (service restarts, system reboots, etc.).

## Orchestration

Systemd is the single source of truth for service dependencies and lifecycle. Configurators run as **systemd hooks**:

```
┌─────────────────────────────────────────────────────────────────────┐
│  podman-app-a.service                                               │
│                                                                     │
│  [Unit]                                                             │
│  After=podman-database.service podman-auth-provider.service         │
│  Requires=podman-database.service                                   │
│                                                                     │
│  [Service]                                                          │
│  ExecStartPre=bloud-agent configure static app-a                    │
│  ExecStart=podman run app-a-image ...                               │
│  ExecStartPost=bloud-agent configure dynamic app-a                  │
└─────────────────────────────────────────────────────────────────────┘
```

**How it works:**

1. Systemd starts services in dependency order (After=/Requires=)
2. Before container starts: `ExecStartPre` runs StaticConfig
   - Writes config files, creates directories
3. Container starts and becomes healthy
4. After container healthy: `ExecStartPost` runs DynamicConfig
   - Configures integrations via API

Systemd handles everything: ordering, lifecycle, and running configurators at the right time.

### Container Invalidation

When a new app is installed that provides an integration, existing apps that consume that integration need to be reconfigured. This is handled through **container invalidation**.

**Example: Installing an auth provider when app-a is already running**

```
State: app-a running (no auth)
Action: User installs auth-provider

1. auth-provider installed and started
2. System checks: "Who has auth integration that auth-provider provides?"
   → app-a has auth integration, auth-provider is compatible
3. app-a marked as invalidated
4. app-a service restarted via systemctl
5. ExecStartPre runs StaticConfig (writes new config)
6. Container starts
7. ExecStartPost runs DynamicConfig (configures via API)
8. Auth configured
```

### Configuration State in Database

Integration state is tracked explicitly in postgres, not derived from NixOS configuration hashes. This gives us explicit control over what triggers reconfiguration.

**Schema concept:**

```sql
-- Tracks current integration state per app
CREATE TABLE app_integrations (
    app_name TEXT,
    integration_name TEXT,
    source_app TEXT,           -- Which app provides this integration
    configured_at TIMESTAMP,   -- When DynamicConfig last configured this
    PRIMARY KEY (app_name, integration_name)
);
```

**Flow when auth-provider is installed:**

```
┌─────────────────────────────────────────────────────────────────────┐
│  1. Orchestrator installs auth-provider                             │
│         │                                                           │
│         ▼                                                           │
│  2. Query graph: "What integrations does auth-provider provide?"    │
│     → auth                                                          │
│         │                                                           │
│         ▼                                                           │
│  3. Query installed apps: "Who has auth integration?"               │
│     → [app-a, app-b]                                                │
│         │                                                           │
│         ▼                                                           │
│  4. Update database: INSERT into app_integrations                   │
│     (app-a, auth, auth-provider, NULL)  -- NULL = not yet configured│
│         │                                                           │
│         ▼                                                           │
│  5. Restart affected services                                       │
│     systemctl restart podman-app-a podman-app-b                     │
│         │                                                           │
│         ▼                                                           │
│  6. Systemd runs ExecStartPre (StaticConfig)                        │
│     Then ExecStartPost (DynamicConfig) after healthy                │
│     UPDATE app_integrations SET configured_at = NOW()               │
└─────────────────────────────────────────────────────────────────────┘
```

**Benefits of database-tracked state:**

- Explicit record of what's configured vs. what needs configuration
- Configurators can check: "Has my integration state changed?"
- Go controls restarts directly, no NixOS hash tricks
- Can track configuration failures and retry

### Deferred Restart

We **mark** apps for invalidation immediately, but **defer** the actual restart. This provides two benefits:

**1. Deduplication**

If multiple changes affect the same app, it only restarts once:

```
Install service-x + service-y simultaneously
    │
    ├── app-a marked for invalidation (integration-x available)
    ├── app-a marked for invalidation (integration-y available)  ← same app
    │
    ▼
Issue restart for app-a
    │
    ▼
Restart once, configure both integrations
```

**2. Correct Ordering**

Systemd ensures apps restart in dependency order:

```
┌─────────────────────────────────────────────────────────────────────┐
│  Install auth-provider                                              │
│                                                                     │
│  Mark for invalidation:                                             │
│    - app-a (needs auth)                                             │
│    - app-b (needs auth)                                             │
│                                                                     │
│  Systemd restart order (respects After=/Requires=):                 │
│                                                                     │
│  Level 0: database, cache        ← no invalidation needed           │
│  Level 1: auth-provider          ← just installed, starting up      │
│  Level 2: app-a, app-b           ← restart after auth-provider      │
│                                                                     │
│  app-a and app-b don't restart until auth-provider is healthy.      │
└─────────────────────────────────────────────────────────────────────┘
```

**The rule:** Mark immediately, let systemd order the restarts. Apps only restart when their dependencies are ready.

---

## Generated Configuration

When you install an app, Bloud generates several configuration files:

### 1. NixOS Configuration (apps.nix)

Enables apps in NixOS. Each app's `module.nix` defines what `enable = true` means - usually creating a systemd service that runs a podman container.

```nix
# Generated by Bloud - DO NOT EDIT
{
  bloud.apps.postgres.enable = true;
  bloud.apps.authentik.enable = true;
  bloud.apps.miniflux.enable = true;
}
```

### 2. Traefik Routing (apps-routes.yml)

Dynamic routing configuration with routers, middlewares, and services for each app:

```yaml
# Generated by Bloud - DO NOT EDIT
http:
  routers:
    miniflux-backend:
      rule: "PathPrefix(`/embed/miniflux`)"
      middlewares:
        - miniflux-stripprefix
        - iframe-headers
        - embed-isolation
      service: miniflux

  middlewares:
    miniflux-stripprefix:
      stripPrefix:
        prefixes:
          - "/embed/miniflux"

  services:
    miniflux:
      loadBalancer:
        servers:
          - url: "http://localhost:8085"
```

### 3. Authentik Blueprints

SSO configuration for each app - OAuth providers, forward-auth configs, or LDAP:

```yaml
# miniflux.yaml - Generated by Bloud
version: 1
metadata:
  name: miniflux-sso-blueprint
  labels:
    managed-by: bloud

entries:
  - model: authentik_providers_oauth2.oauth2provider
    identifiers:
      name: Miniflux OAuth2 Provider
    attrs:
      client_id: miniflux-client
      client_secret: <derived-from-host-secret>
      redirect_uris:
        - url: "http://localhost:8080/embed/miniflux/oauth2/oidc/callback"
```

### 4. Secrets & Environment Files

Per-app `.env` files with database URLs, OAuth secrets, and admin passwords:

```bash
# miniflux.env
DATABASE_URL=postgres://apps:xxx@localhost:5432/miniflux?sslmode=disable
OAUTH2_CLIENT_SECRET=xxx
ADMIN_PASSWORD=xxx
```

### The Flow

```
Orchestrator.Install()
        │
        ├── Write apps.nix (NixOS config)
        ├── Write apps-routes.yml (Traefik routing)
        ├── Write authentik blueprints (SSO)
        └── Write secret env files
                │
                ▼
        nixos-rebuild switch
                │
                ├── Evaluates all NixOS modules
                ├── Builds new system configuration
                ├── Creates/updates systemd services
                └── Activates new configuration
                        │
                        ▼
                systemd starts containers
                        │
                        ▼
                Systemd hooks configure apps (StaticConfig/DynamicConfig)
```

NixOS provides atomic deploys - if something fails, the previous generation still exists. You can always `nixos-rebuild --rollback`.

## SSO Integration

Apps can use SSO three ways:

### Native OIDC

The app handles OAuth2 itself. Bloud generates Authentik blueprints to create the OAuth client, and passes credentials via environment variables.

```yaml
sso:
  strategy: native-oidc
  callbackPath: /oauth2/callback
  env:
    clientId: OAUTH2_CLIENT_ID
    clientSecret: OAUTH2_CLIENT_SECRET
```

Miniflux uses this - it has built-in OIDC support.

### Forward Auth

Traefik intercepts requests and checks authentication with Authentik. The app never sees auth - it just gets `X-Remote-User` headers.

```yaml
sso:
  strategy: forward-auth
```

Good for apps that don't speak OAuth2.

### None

App handles its own auth or doesn't need it.

```yaml
sso:
  strategy: none
```

## Shared Infrastructure

Instead of each app running its own database, all apps share:

- **PostgreSQL** - One instance, apps get separate databases
- **Redis** - Session storage, caching
- **Traefik** - Reverse proxy, routing, SSO middleware
- **Authentik** - Identity provider

This reduces resource usage and simplifies backups.

When an app declares `database: "miniflux"` in its module.nix, the postgres module automatically creates that database and user.

## Key Design Principles

### 1. Declarative Over Imperative

Apps declare what they need, not how to get it. The system figures out the how.

### 2. Idempotent Everything

Every operation can run repeatedly without side effects. Configurators run on every service start.

### 3. Fail Open, Log Clearly

If configuration fails, log it clearly. The next service restart will retry automatically via the systemd hooks.

### 4. Atomic Deploys

NixOS rebuilds are all-or-nothing. No partial states.

### 5. Single Source of Truth

`apps.nix` defines what's installed. Systemd hooks configure apps on every start to match.

---

## Implementation Status

This section tracks what's implemented vs. what's planned.

### Done

- [x] **App definitions** - `metadata.yaml` and `module.nix` structure
- [x] **Dependency graph** - Planning installs/uninstalls with auto-selection
- [x] **NixOS integration** - `apps.nix` generation and `nixos-rebuild`
- [x] **Orchestrator** - Install/uninstall coordination with queuing
- [x] **Configurator interface** - `PreStart`, `HealthCheck`, `PostStart` methods
- [x] **App configurators** - Miniflux, Authentik, Arr stack, etc.
- [x] **Shared infrastructure** - Single postgres/redis per host
- [x] **SSO integration** - Native OIDC and forward-auth strategies
- [x] **Traefik routing** - Dynamic config generation for apps

### In Progress / Planned

- [ ] **Rename configurator methods** - `PreStart` → `StaticConfig`, `PostStart` → `DynamicConfig`
- [ ] **Systemd hooks** - Run configurators via `ExecStartPre`/`ExecStartPost` instead of host-agent
- [ ] **Container invalidation** - Mark apps for restart when new integrations become available
- [ ] **app_integrations table** - Track integration state in database
- [ ] **Deferred restart** - Batch invalidations, let systemd order restarts
- [ ] **Remove watchdog references** - Clean up old self-healing code/comments from `pkg/configurator/interface.go`

### Not Planned

- ~~Periodic reconciliation~~ - Replaced by systemd hooks (event-driven)
- ~~Self-healing watchdog~~ - Configuration runs on service start only

### What's Not Built Yet

- Bootable USB image
- Multi-host orchestration
- Automatic backups

---

## Development

Development uses [Lima](https://lima-vm.io/) to run a NixOS VM on your local machine. The `./bloud` CLI manages the VM and development services.

### Prerequisites

| Requirement | macOS | Linux |
|-------------|-------|-------|
| **Lima** | `brew install lima` | [See install guide](https://lima-vm.io/docs/installation/) |
| **Node.js 18+** | `brew install node` | `sudo apt install nodejs npm` |
| **Go 1.21+** | `brew install go` | `sudo apt install golang` |

### CLI Commands

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

### Troubleshooting

**"Lima is not installed"**
- macOS: `brew install lima`
- Linux: `curl -fsSL https://lima-vm.io/install.sh | bash`

**"VM image not found"**
- Run `./bloud setup` to download the pre-built image
- Image location: `lima/imgs/nixos-24.11-lima.img`

**VM boots but services don't start**
- Check logs: `./bloud logs`
- Rebuild NixOS: `./bloud rebuild`
- Nuclear option: `./bloud destroy && ./bloud start`

### Debugging

```bash
# Check host-agent logs
journalctl --user -u bloud-host-agent -f

# Check app container logs (includes StaticConfig/DynamicConfig output)
journalctl --user -u podman-miniflux -f

# Check systemd service status
systemctl --user status podman-miniflux

# Restart a service to re-run configurators
systemctl --user restart podman-miniflux

# See what would be installed (includes dependencies)
curl http://localhost:8080/api/apps/miniflux/plan-install

# See what would be removed (includes dependents)
curl http://localhost:8080/api/apps/postgres/plan-remove
```

### Adding a New App

1. Create `apps/myapp/metadata.yaml` with integrations and port
2. Create `apps/myapp/module.nix` with container definition
3. Create `apps/myapp/configurator.go` implementing StaticConfig, HealthCheck, and DynamicConfig
4. Register the configurator in `internal/appconfig/register.go`

See [apps/adding-apps.md](apps/adding-apps.md) for details.

---

## Contributing

Contributions welcome! See:
- [apps/adding-apps.md](apps/adding-apps.md) - Adding new apps
- [docs/dev-workflow.md](docs/dev-workflow.md) - Development setup

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

## Further Reading

- [docs/design/graph-configurator-system.md](docs/design/graph-configurator-system.md) - Detailed configurator design
- [docs/embedded-app-routing.md](docs/embedded-app-routing.md) - How apps are served in iframes
- [docs/design/authentication.md](docs/design/authentication.md) - SSO and auth flows
- [docs/design/production-architecture.md](docs/design/production-architecture.md) - Production deployment

## Philosophy

- **Simplicity Over Features** - Opinionated defaults for 80% of users
- **Privacy by Default** - Everything runs locally on your hardware

## License

AGPL v3 - See [LICENSE](LICENSE) for details. If you modify Bloud and offer it as a service, you must share your changes.

---

**Built with NixOS, Podman, Go, and Svelte.**
