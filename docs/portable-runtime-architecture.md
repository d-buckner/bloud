# Portable Runtime Architecture

Bloud manages apps (Jellyfin, Immich, etc.) on a single Linux host. The **portable
runtime** is the Go binary (`host-agent`) that orchestrates app lifecycle, configuration,
and integration via the Podman API.

> **Naming note:** earlier docs called this component the *reconciler*. It was refactored
> into the **orchestrator** (`internal/orchestrator/`), which owns both intent draining
> and the convergence loop. `RECONCILER_SPEC.md` describes that target architecture as
> implemented.

## Component Diagram

```mermaid
graph TD
    CLI["./bloud CLI<br/>(macOS, validates + deploys)"]
    API["Host-Agent API<br/>:3000"]
    CAT["Catalog"]
    ORC["Orchestrator"]
    PLAN["Catalog AppGraph / Planner"]
    REG["Configurator Registry"]
    AK_CFG["Authentik Configurator"]
    JF_CFG["Jellyfin Configurator"]
    NM_CFG["Navidrome Configurator"]
    AK_CLIENT["Authentik Client"]
    STORE["App Store<br/>(SQLite)"]
    GRAPH["Lifecycle Graph<br/>(target/actual status)"]

    CLI -->|validate, deploy| API
    API -->|install, uninstall| ORC
    API --> CAT
    ORC --> REG
    ORC --> PLAN
    ORC --> STORE
    ORC --> CAT
    ORC --> GRAPH
    PLAN --> CAT
    REG --> AK_CFG
    REG --> JF_CFG
    REG --> NM_CFG
    AK_CFG --> AK_CLIENT

    subgraph "Infrastructure Containers"
        AK["Authentik<br/>(SSO + LDAP)"]
        LDAP["LDAP Outpost"]
        TR["Traefik"]
    end

    subgraph "App Containers"
        JF["Jellyfin"]
        IM["Immich<br/>(postgres + redis + server + ML)"]
    end

    AK_CLIENT --> AK
    AK --> LDAP
    JF -->|LDAP bind| LDAP
```

## Components

### Host-Agent CLI (`services/host-agent/cmd/host-agent/`)

Entry point. Runs as a systemd user service (API mode) or executes one-shot commands.

| Subcommand | Purpose |
|---|---|
| *(none)* | Start the REST API server on `:3000` |
| `configure` | One-shot configure commands (prestart/poststart/etc.) |
| `init-secrets` | Generate and persist initial secrets |

### Catalog (`internal/catalog/`)

Discovers apps by reading `apps/*/metadata.yaml`. Each app declares its name, port,
SSO strategy, integration requirements, and container spec. The catalog is held in an
in-memory `MemoryCache` and refreshed from disk on demand. The `catalog.AppGraph` (a.k.a.
the planner) resolves dependency plans via `PlanInstall`/`PlanRemove`.

### Dependency Resolution (planner)

Apps declare integrations in `metadata.yaml`:

```yaml
integrations:
  database:
    required: true
    compatible: [{ app: postgres, default: true }]
  sso:
    required: false
    compatible: [{ app: authentik }]
```

Resolution happens through `catalog.AppGraph.PlanInstall` during the install intent:
- Exactly one installed compatible provider → auto-config binding.
- Multiple / none for a required integration → produces an integration *choice*.
- Optional integrations with no compatible provider → no binding.

> Note: `internal/integration/` (resolver.go) is a leftover from an earlier design and is
> not wired into the running system — dependency resolution is owned by the planner +
> orchestrator. Apps that need databases (Immich, Authentik) declare their own postgres
> and redis containers in `containers:`; they do not use the integration resolver.

### Orchestrator (`internal/orchestrator/`)

All mutations flow through a typed intent queue with debounce. The orchestrator is the
single writer to all stores and the single executor of all side effects. It is also the
owner of the lifecycle graph (`graph.Graph`) that tracks desired (`targetStatus`) vs
observed (`actualStatus`) per node.

Intent types (`intent.go`):
- **InstallAppIntent** — install an app by name
- **UninstallAppIntent** — remove an app (with optional `clearData`)
- **RenameAppIntent** — change an app's display name
- **SetTailnetIntent / DeleteTailnetIntent** — tailnet configuration changes
- **AddRemoteAppIntent / DeleteRemoteAppIntent** — remote app management
- **ClearAppDataIntent** — wipe app data
- *(CreateShareIntent / RevokeShareIntent are defined but shares are currently written
  directly by the sharing API module)*

The orchestrator drains the intent queue, applies intents to stores (desired state), then
converges actual state toward desired: sync container state, handle uninstalls, populate
the graph, converge tailnet, run a topological reconcile pass (per-level concurrent,
phases `INITIALIZING→PRESTART→STARTING→POSTSTART→RUNNING`), and finally regenerate
Traefik routes before promoting nodes to RUNNING.

```go
type Intent interface {
    intentMarker()  // sealed interface
    IntentID() string
}
```

### Configurator Framework (`pkg/configurator/`)

Generic interface for app-specific runtime configuration that can't be expressed in
static container definitions (API calls, credential rotation, plugin setup).

```go
type NodeLifecycle interface {
    Name() string
    PreStart(ctx context.Context, state *AppState) (changed bool, err error)
    PostStart(ctx context.Context, state *AppState) error
    Remove(ctx context.Context, state *AppState, clearData bool) error
}
```

**`AppState`** carries typed integration outputs into each configurator:

```go
type AppState struct {
    DataPath      string
    BloudDataPath string
    SSOEnabled    bool
    LDAP          *LDAPOutput  // host, port, baseDN, bindUser, bindPassword
}
```

**Implementations:**
- **Authentik** — sets admin password, ensures API token, creates LDAP infrastructure
- **Jellyfin** — completes setup wizard, creates libraries, configures LDAP plugin
- **Navidrome** — SSO/config wiring

### Authentik Client (`pkg/authentik/`)

Manages the Authentik identity provider via its REST API. Key operations:
`EnsureLDAPInfrastructure`, `EnsureBloudOAuthApp` (idempotent OIDC bootstrap), SSO
provisioning, and forward-auth provider creation for tailnet access.

### App Store (`internal/store/`)

SQLite-backed persistence for installed apps, their status, and resolved integration
bindings. The orchestrator reads desired state from here and (as single writer) is the
only author of lifecycle status. Schema lives in `internal/db/schema.sql`. The lifecycle
graph has SQLite backing tables (`graph_nodes`, `graph_edges`) but the running
orchestrator currently uses an in-memory repository (see REVIEW.md §C2).

### Container Runtime (`internal/container/`, `internal/orchestrator/`)

Apps run as Podman containers created and managed directly by the orchestrator through
the Podman API (`internal/podman/`). The orchestrator builds a container spec from
`metadata.yaml`, creates/starts containers, and removes them on uninstall.

Container specs are defined in `metadata.yaml` under the `containers:` key (one def per
container, multi-container apps expand to one graph node each) and rendered with template
variables (data paths, passwords, etc.) at install time.

## Data Flow: Installing an App

```
User clicks "Install Jellyfin"
  → API submits InstallAppIntent to the intent queue (202 accepted)
  → Orchestrator drains intent → applyInstallIntent
      → catalogGraph.PlanInstall(app) resolves dependencies
      → records dependency providers + target app in the store
  → Convergence pass:
      1. SyncContainerState (align DB with reality)
      2. populateGraphNodes — build DAG from installed store records
      3. Reconcile — for each container node (topological order):
         a. PreStart (configurator, if registered)
         b. Ensure container (create/start, idempotent)
         c. Health check (from container metadata)
         d. PostStart (configurator, if registered)
      4. RegenerateRoutes (Traefik dynamic config) — then promote nodes to RUNNING
  → All apps healthy, SSO/LDAP/login works
```

For apps with databases (e.g. Immich), the dependency graph includes their per-app
postgres and redis containers declared in `containers:` metadata.

> ⚠️ **Known wiring gap:** the production router currently constructs the orchestrator
> with `CatalogGraph: nil`, which short-circuits `applyInstallIntent` before recording
> the app — installs no-op until this is wired. See REVIEW.md §C1.

## Validation Tiers

| Tier | Scope | Command |
|---|---|---|
| `fast` | Unit tests, type checks | `./bloud validate --tier fast` |
| `integration` | Backend against real services in Lima VM | `./bloud validate --tier integration` |

## Dev Environment

Lima VM (Debian, Apple Virtualization) + rootless Podman:

```
macOS host
  └── Lima VM "bloud-dev"
        ├── Podman (rootless)
        │   ├── Authentik + LDAP Outpost
        │   ├── Traefik  :8080
        │   └── App containers (Jellyfin, Immich w/ its own postgres+redis, etc.)
        └── host-agent binary (:3000, systemd user service)
```

Ports 3000 and 8080 are forwarded to macOS localhost by `./bloud dev`.
