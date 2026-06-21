# Bloud

**Home Cloud Integration Platform**

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
[![Status: Alpha](https://img.shields.io/badge/Status-Alpha-orange.svg)]()

> **Status:** Early alpha. Core infrastructure and web UI working.
>
> **Architecture direction:** The current implementation runs as a NixOS appliance. The
> first release target is a portable Bloud binary installed on Debian, while preserving the
> dashboard, same-origin app experience, dependency graph, configurators, and shared login.
> [SPEC.md](SPEC.md) is the authoritative first-release plan. Other architecture documents
> provide supporting detail and defer to it.

---

Self-hosting's hard part isn't installation. Every platform has solved that. The hard part is  when you add a second service and realize the first one doesn't know it exists.

To connect two services manually, you typically:

- Generate an API key in one service
- Open the second service's settings, paste the key
- Create an OAuth client in your identity provider - client ID, secret, callback URL
- Paste those back into the first service
- Register the second service with the identity provider separately
- Write a reverse proxy rule for each
- Provision and wire a database for each
- Remember all of this when you reinstall or migrate

On every other platform, *you* are the integration layer — the person who knows which credentials go where and how the pieces fit together. You don't configure services. You configure the relationships between services. That knowledge lives in your head, not in the platform.

**Bloud flips this.** Apps declare what they provide and what they consume. The system holds the integration knowledge and wires everything automatically - on install, on restart, forever.

```mermaid
graph LR
    DB[(Database)]
    IP[Identity Provider]
    S1[Service]
    S2[Service]
    RP[Reverse Proxy]

    DB -->|credentials| S1
    DB -->|credentials| S2
    DB -->|credentials| IP
    S1 -->|SSO client| IP
    S2 -->|SSO client| IP
    S1 <-->|API key| S2
    S1 -->|route| RP
    S2 -->|route| RP
    IP -->|auth middleware| RP
```

Every edge in this graph is a manual step on every other platform. Bloud generates all of them and regenerates them correctly every time a service starts.

---

## The Vision

- Install Bloud on a supported Linux server
- Access the web dashboard and install supported apps with one click
- Automatically maintain SSO, routing, credentials, databases, and app-to-app connections
- Reconcile integrations safely after dependency changes, failures, upgrades, and reboot

---

## Current Development Quick Start

The current development implementation runs as a bootable NixOS ISO. It remains the
reference runtime while the portable Linux runtime is built.

For **development**, you'll need a NixOS machine (see [Local Development](#local-development) below).

```bash
git clone https://codeberg.org/d-buckner/bloud.git
cd bloud
npm run setup    # Install deps, build CLI
./bloud setup    # Check prerequisites, apply NixOS config
./bloud start    # Start dev environment
```

Access the web UI at **http://bloud.local** (port 80, through Traefik).

---

## Current App Catalog

| Category           | Apps                                  |
| ------------------ | ------------------------------------- |
| **Infrastructure** | PostgreSQL, Redis, Traefik, Authentik |
| **Media**          | Jellyfin                              |
| **Productivity**   | Miniflux (RSS)                        |
| **Network**        | AdGuard Home                          |
| **Utility**        | qBittorrent                           |

---

## How It Works

#### Lifecycle

```mermaid
flowchart TD
    HA["`**host-agent**
    *Single binary — server + orchestrator*`"]

    S[Startup]

    DG["`**Load / create persisted
    app dependency graph**
    *Target desired state only — not the whole catalog.
    First run creates the graph of system apps
    and stores it in a new SQLite DB.*`"]

    R["`**Reconciliation**
    *Process each level deepest → shallowest.
    See detail diagram below.*`"]

    API["`**API request**
    *POST /api/apps/{name}/install
    POST /api/apps/{name}/uninstall*`"]

    Q["`**Operation queue**
    *5 s batch window, dedup per app.
    Serializes concurrent requests.*`"]

    HA --> S --> DG --> R
    API --> Q -->|graph mutated| DG
```

#### Reconciliation detail (per level)

```mermaid
flowchart TD
    L["`**For each level** *(deepest deps → shallowest consumers)*`"]

    SC["`**StaticConfig / PreStart**
    *Runs BEFORE container starts.
    Write config files, env, secrets.
    Returns whether managed output changed.*`"]

    CS["`**Container start**
    *Pull image if needed, create/start container.
    Converges to desired spec via revision hash.*`"]

    HC["`**HealthCheck**
    *Poll until ready (60 s timeout).
    Blocks consumers from proceeding.*`"]

    DC["`**DynamicConfig / PostStart**
    *Runs AFTER container is healthy.
    Idempotent API calls — register OAuth client,
    create DB user, wire app-to-app integrations.*`"]

    NL{More levels?}

    TR["`**Regenerate Traefik routes**
    *Update apps-routes.yml.
    Also runs on startup and uninstall.*`"]

    L --> SC --> CS --> HC --> DC --> NL
    NL -->|yes| L
    NL -->|no| TR
```

### 1. The Dependency Graph

Every app declares its integrations in `metadata.yaml`:

```yaml
# example metadata.yaml
integrations:
  database:
    required: true
    compatible:
      - app: postgres
        default: true
  sso:
    required: false
    compatible:
      - app: authentik
```

When you install an app, Bloud builds a graph of everything it needs. Dependencies with only one compatible option are auto-selected. If postgres isn't installed yet, it goes in first. If it's already there, the new app just gets wired to it.

The graph also determines **execution order** — infrastructure first, then dependents:

```
Level 0: postgres, redis          ← No dependencies
Level 1: authentik                ← Needs postgres + redis
Level 2: jellyfin, miniflux       ← Need postgres and authentik
```

### 2. Declarative Container Definitions

Each app has a `module.nix` defining how to run the container:

```nix
mkBloudApp {
  name = "miniflux";
  image = "miniflux/miniflux:latest";
  port = 8085;
  database = "miniflux";  # Auto-creates postgres DB and user

  environment = cfg: {
    BASE_URL = "${cfg.externalHost}/embed/miniflux";
  };
}
```

The `mkBloudApp` helper handles systemd services, podman networking, volumes, and dependency wiring. When you enable an app, NixOS creates everything atomically.

### 3. Idempotent Configurators

Configurators turn dependency-graph edges into working integrations. They are not limited to
SSO or app-local setup. They can provision databases and credentials, write provider
endpoints, create API keys, register apps with each other, configure Authentik, and apply
runtime settings.

Static configuration runs before service start and reports whether managed startup inputs
changed. Dynamic configuration runs after required services are healthy and performs
idempotent runtime API operations. The current implementation exposes these phases as
`PreStart` and `PostStart`; the portable architecture formalizes them as `StaticConfig` and
`DynamicConfig`.

```go
// PreStart: current static-configuration hook, before the service starts
func (c *Configurator) PreStart(ctx context.Context, state *AppState) error {
    // Ensure directories exist
    if err := os.MkdirAll(filepath.Join(state.DataPath, "config"), 0755); err != nil {
        return err
    }

    // Write app config with required settings for Bloud's embedding
    ini, _ := configurator.LoadINI(configPath)
    ini.EnsureKeys("Preferences", map[string]string{
        "WebUI\\HostHeaderValidation": "false",
        "WebUI\\CSRFProtection":       "false",
        "Downloads\\SavePath":         "/downloads",
    })
    return ini.Save(configPath)
}

// PostStart: current dynamic-configuration hook, after the service is healthy
// Used for API calls and inter-app integration
func (c *Configurator) PostStart(ctx context.Context, state *AppState) error {
    return nil // most apps are fully configured by PreStart
}
```

Configurators always write the *desired* state — running them again produces the same result.
See [SPEC.md](SPEC.md) for the authoritative target and
[Portable Runtime Architecture](docs/portable-runtime-architecture.md) for
the component overview.

### 4. Container Invalidation

When a new integration becomes available, existing apps that can use it get automatically reconfigured:

```
Service A installed (no SSO)
Identity provider installed
    │
    ▼
Graph: "Who declared an SSO integration?"
    → Service A
    │
    ▼
Service A marked for reconfiguration
Service A restarted in dependency order
PreStart + PostStart wire the new integration
```

No user action needed. Apps gain integrations as you install their dependencies.

---

## Shared Infrastructure

Each Bloud host runs exactly one instance of each infrastructure service. All apps share them:

- **PostgreSQL** — one instance, apps get separate databases and users
- **Redis** — session storage and caching, shared across apps
- **Traefik** — reverse proxy, routing, SSO middleware
- **Authentik** — identity provider, SSO for all apps

---

## SSO Integration

Apps get SSO automatically. Three strategies depending on the app:

| Strategy         | How It Works                                                          | Example Apps              |
| ---------------- | --------------------------------------------------------------------- | ------------------------- |
| **Native OIDC**  | App handles OAuth2 itself; Bloud provides credentials via env vars    | Miniflux                  |
| **Forward Auth** | Traefik checks auth with Authentik before the request reaches the app | AdGuard Home, qBittorrent |
| **LDAP**         | Authentik LDAP for apps that don't speak OAuth2                       | Jellyfin                  |

All three are configured automatically at install time.

---

## Current Project Structure

```
bloud/
├── apps/                          # App definitions
│   ├── miniflux/
│   │   ├── metadata.yaml          # Integrations, SSO, port, routing
│   │   ├── module.nix             # Container definition
│   │   └── configurator.go        # PreStart/PostStart hooks
│   ├── postgres/
│   ├── authentik/
│   └── ...
│
├── services/host-agent/           # Go backend
│   ├── cmd/host-agent/
│   ├── internal/
│   │   ├── orchestrator/          # Install/uninstall coordination
│   │   ├── catalog/               # App graph and dependency resolution
│   │   ├── nixgen/                # Generates apps.nix, Traefik config, blueprints
│   │   ├── store/                 # Database layer
│   │   └── api/                   # HTTP API
│   ├── pkg/configurator/          # Configurator interface
│   └── web/                       # Svelte frontend
│
├── nixos/
│   ├── bloud.nix                  # Main NixOS module
│   ├── lib/
│   │   ├── bloud-app.nix          # mkBloudApp helper
│   │   └── podman-service.nix     # Systemd service generator
│   └── generated/
│       └── apps.nix               # Generated by host-agent on install
│
└── cli/                           # ./bloud CLI (Go)
```

---

## Local Development

Development uses a Lima VM (Debian 13 with rootless Podman).

```bash
brew install lima                                    # macOS
limactl create --name=bloud-dev dev/lima.yaml        # Create VM
limactl start bloud-dev                              # Start VM
limactl shell bloud-dev bash dev/setup.sh            # Provision VM
npm run setup                                        # Install deps + build ./bloud CLI
```

### CLI Commands

```bash
./bloud start          # Start dev environment
./bloud stop           # Stop dev services
./bloud status         # Show status
./bloud logs           # Show logs
./bloud attach         # Attach to tmux session (Ctrl-B D to detach)
./bloud shell [cmd]    # Run a command or open a shell
./bloud install <app>  # Install app via API
./bloud uninstall <app> # Uninstall app via API
```

### Integration Testing

```bash
./bloud e2e lifecycle              # Full portable-runtime lifecycle E2E
./bloud e2e lifecycle --host-only  # Skip browser tests, verify host state only
./bloud e2e lifecycle --keep       # Leave deployment running after tests
```

### Debugging

```bash
# Check host-agent logs
journalctl --user -u bloud-host-agent -f

# Check app container logs (includes PreStart/PostStart output)
journalctl --user -u podman-miniflux -f

# Restart a service to re-run configurators
systemctl --user restart podman-miniflux

# Check install plan (shows resolved dependency graph)
curl http://localhost/api/apps/miniflux/plan-install
```

### Contributing a New App

1. Create `apps/myapp/metadata.yaml` — integrations, SSO strategy, port
2. Create `apps/myapp/module.nix` — container definition using `mkBloudApp`
3. Create `apps/myapp/configurator.go` — PreStart, HealthCheck, PostStart
4. Register in `services/host-agent/internal/appconfig/register.go`

See [docs/contributing-apps.md](docs/contributing-apps.md) for details.

---

## Further Reading

- [docs/portable-runtime-architecture.md](docs/portable-runtime-architecture.md) — Component overview and diagram
- [docs/contributing-apps.md](docs/contributing-apps.md) — How to add a new app
- [docs/migration-design-rules.md](docs/migration-design-rules.md) — Migration review checklist

---

## Contributing

Contributions welcome.

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Open a Pull Request

[Open an issue](https://codeberg.org/d-buckner/bloud/issues) with a clear description and steps to reproduce for bugs.

---

## License

AGPL v3 — See [LICENSE](LICENSE) for details. If you modify Bloud and offer it as a service, you must share your changes.

---

**Built with NixOS, Podman, Systemd, Go, and Svelte.**
