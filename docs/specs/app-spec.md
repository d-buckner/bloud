# Multi-Container App Spec

**Status:** Complete (all phases done; legacy interface removed; all apps on `containers:` format)

> This is the multi-container model design document. The canonical metadata field
> reference is `services/host-agent/internal/catalog/models.go`; to add an app, follow
> [docs/guides/contributing-apps.md](../guides/contributing-apps.md).

## Implementation Status

All six phases are done. The multi-container model ships as the `containers:` block,
every catalog app declares its containers there, and the legacy single-container
interface is gone.

| Phase | What it was | State |
|---|---|---|
| 1-2 | Multi-container metadata parsing and the orchestrator lifecycle | done |
| 3 | The `NodeLifecycle` interface | done; the legacy interface was removed in phase 6 |
| 4 | Immich on multiple containers | done, and covered by the integration tier |
| 5 | Authentik migration, per-container configurators | done |
| 6 | Jellyfin and Navidrome migration, legacy cleanup | done |

The phase-by-phase task list that drove this work is archived at
[`docs/plans/archive/multi-container-app-implementation.md`](../plans/archive/multi-container-app-implementation.md),
because it is a record of finished work and not a specification.

## Core Idea

An app can declare multiple containers in its `metadata.yaml`. Each container
becomes a node in the orchestrator's graph, same as today's single-container
apps. The orchestrator doesn't know or care that several nodes "belong to" one
logical app. It just sees nodes with dependency edges and converges them.

"Authentik" in the UI is a presentation concern. In the graph, it's 5 nodes:

```
apps-authentik-postgres ──┐
                          ├── apps-authentik-server ── apps-authentik-ldap
apps-authentik-redis ─────┘         │
                           apps-authentik-worker
```

The orchestrator processes these identically to how it processes `apps-jellyfin`
depending on `apps-traefik`. No special sub-graph logic, no "app grouping"
concept in the orchestrator.

## Why Per-App Databases

The shared-postgres model (one instance for all apps) doesn't work because:

1. **Version coupling**: App A needs postgres 16, App B needs 17, App C needs
   pgvector. Can't serve all from one instance.
2. **Upgrade risk**: Upgrading for one app risks breaking others.
3. **Failure blast radius**: Shared crash takes everything down.
4. **Extension conflicts**: pgvector, timescaledb, etc. can't coexist cleanly.

New model: apps that need postgres declare their own postgres container node
with whatever image/version/extensions they need. Each is isolated.

## Bootstrap vs Orchestrator

**Bootstrap** handles system infrastructure startup (traefik, and its
dependencies) **before** the orchestrator starts. This is intentional:

- System infra must be running before the HTTP listener opens
- System infra startup is sequential and fail-fast (if traefik can't start,
  exit immediately: don't try to converge user apps)
- The orchestrator's value is dependency-aware convergence for user apps,
  not booting the system

**The orchestrator** manages user app nodes. It sees nodes with edges and
converges them in topological order. It doesn't know if a node is "part of
authentik" or "standalone jellyfin", irrelevant to its algorithm.

**Shared utilities** (container runtime, health polling, spec hashing) are used
by both bootstrap and orchestrator so the code stays DRY without coupling
concerns.

## metadata.yaml Changes

The `container:` block became `containers:` (plural): a list of container
definitions with explicit dependency edges. A single-container app is a list of
one, so the shape is the same for both.

This document does not repeat the field list. Three sources answer it, and each
is authoritative for one question:

| Question | Source |
|---|---|
| What fields exist, and what does the loader reject? | `internal/catalog/models.go` (the types, their yaml tags, and the load-time validation) |
| How do I write an app? | [docs/guides/contributing-apps.md](../guides/contributing-apps.md) |
| What does a real one look like? | `apps/authentik/metadata.yaml` (five containers with `dependsOn`), `apps/immich/metadata.yaml` (a bundled pgvector postgres), `apps/jellyfin/metadata.yaml` (the single-container shape) |

Two properties are worth stating here, because they are the point of the model.

- **One container, one graph node.** A `containers:` list of five produces five
  nodes and explicit `dependsOn` edges between them. The orchestrator converges
  them with the same algorithm it uses for a single-container app, concurrently
  within a level. There is no sub-graph and no app-grouping concept.
- **A bundled database is the app's own.** An app that needs postgres, redis, or
  any other backing service declares the container itself, with the image and the
  version it needs, and mounts its data under `{{appDataDir}}`. Nothing is shared
  between apps, so a version bump or a crash stays inside one app.

## Orchestrator Changes

### Minimal: the orchestrator barely changes

The orchestrator already processes nodes in topological order. The only changes:

1. **Multiple nodes per "installed app"**: When an app is installed, the
   orchestrator adds N nodes (one per container) instead of 1. The `dependsOn`
   edges within the app are registered as graph edges. The inter-app edges
   (from `integrations:`) connect from the dependent app's entry-point nodes
   to the provider app's container nodes that those entry-points actually need.

   The same declaration also resolves into the consumer's configurator input:
   each contract becomes a typed slice of bindings in `AppState.Integrations`
   (`PVRs`, `MediaServers`, `DownloadClients`, `SSO`, `ModelSources`, `Inference`), each
   provider contributing its catalog id, container name, port, a `BaseURL` (what
   the app stores) and a `LocalURL` (how the configurator reaches it from the
   host), plus that contract's payload from the provider's `provides` metadata
   (credentials from the host store, values such as an inference endpoint path
   from the metadata). A consumer also declares which of the contract's secrets it reads
   (`integrations.<contract>.requires`), and only those are resolved, so
   integrating with a provider never hands an app a credential it did not ask
   for. The bindings mirror the edges `computeAppDeps` builds, so they never
   describe a provider the graph does not order, and `Installed` is the same
   condition as the edge. Consumers therefore never read a provider's files
   or probe its port; see invariant 15 in `AGENTS.md`.

2. **Container-level health checks**: Each node has its own health check
   defined in metadata (the `healthCheck` block on each container). The
   orchestrator runs these directly rather than delegating to a configurator
   `HealthCheck()` method.

3. **Status rollup**: The API/UI derives app-level status from container
   nodes: all RUNNING → "running", any ERROR → "error", otherwise
   "installing". This is a read-path concern, not orchestrator logic.

### What stays the same

- Topological-level convergence algorithm
- Staleness propagation (dependency restarted → downstream re-checks)
- Error handling (ERROR is terminal per-node until reset)
- Intent processing (install/uninstall intents)
- Route regeneration after convergence
- Per-node configurator PreStart/PostStart (just registered by container name now)

## Configurators: Per-Container, Not Per-App

Configurators register against **container node names**, not app names.
The registry maps `"apps-authentik-server"` → configurator, not
`"authentik"` → configurator. This matches the orchestrator's flat-node
worldview.

### Interface

```go
type NodeLifecycle interface {
    Name() string  // container node name, e.g. "apps-authentik-server"
    PreStart(ctx context.Context, state *AppState) error
    PostStart(ctx context.Context, state *AppState) error
    Remove(ctx context.Context, state *AppState, clearData bool) error
}
```

- **PreStart**: Before this container starts. Config file generation,
  directory creation, secret provisioning, template variable writes.
- **PostStart**: After this container's health check passes. API calls,
  user sync, infrastructure setup in running services.
- **Remove**: Before this container is torn down. Cleanup external state.

`EnsureContainer` is gone: the orchestrator manages all containers from
metadata specs. `HealthCheck` is gone: declared in metadata, run by the
orchestrator. The `changed bool` return from PreStart is gone: the
orchestrator diffs spec hashes to decide container recreation.

### Per-Node Lifecycle

The orchestrator runs this sequence for every node, in dependency order:

```
1. PreStart (if configurator registered)
2. Resolve template variables → build container spec
3. Ensure container (create/start, idempotent via spec hash)
4. Wait for health check (from metadata)
5. PostStart (if configurator registered)
6. Mark node RUNNING
```

Most containers don't need a configurator. Postgres, redis, workers: they
start from their spec and their health check is sufficient. Only containers
that need imperative runtime configuration register one.

### How This Solves the LDAP Token Problem

The LDAP outpost token is created by Authentik's API during server setup.
With per-container configurators and dependency ordering, this resolves
naturally:

```
apps-authentik-server:
  PostStart:
    1. Set admin password
    2. Create API token
    3. Push branding
    4. Configure login page
    5. Create LDAP infrastructure
    6. Write LDAP outpost token to secret store ← key step

apps-authentik-ldap:
  dependsOn: [apps-authentik-server]
  PreStart:
    1. Read LDAP token from secret store
    2. Write it as template variable for this node's spec
```

The orchestrator processes `apps-authentik-server` first (full lifecycle
including PostStart). Only then does it process `apps-authentik-ldap`. By
that time, the token exists. No deferred containers, no placeholder values,
no second convergence pass. Just dependency ordering.

### Example: Authentik Configurators

Two configurators, registered by container name:

**`apps-authentik-server` configurator:**
```go
func (c *ServerConfigurator) Name() string { return "apps-authentik-server" }

func (c *ServerConfigurator) PreStart(ctx context.Context, state *AppState) error {
    // Ensure authentik database exists in its postgres
    return c.ensureDatabase()
}

func (c *ServerConfigurator) PostStart(ctx context.Context, state *AppState) error {
    // Admin password, API token, branding, login config, LDAP infra
    // Writes LDAP outpost token to secret store
    return c.configureAuthentik(ctx)
}
```

**`apps-authentik-ldap` configurator:**
```go
func (c *LDAPConfigurator) Name() string { return "apps-authentik-ldap" }

func (c *LDAPConfigurator) PreStart(ctx context.Context, state *AppState) error {
    // Read token from secret store, make available as template var
    return c.resolveOutpostToken()
}
```

No configurator needed for `apps-authentik-postgres`, `apps-authentik-redis`,
or `apps-authentik-worker`: they're pure spec-driven containers.

### Example: Jellyfin Configurator

Single container, single configurator:

```go
func (c *JellyfinConfigurator) Name() string { return "apps-jellyfin" }

func (c *JellyfinConfigurator) PreStart(ctx context.Context, state *AppState) error {
    // Download LDAP plugin, create directories
    return c.preparePlugins(ctx)
}

func (c *JellyfinConfigurator) PostStart(ctx context.Context, state *AppState) error {
    // Setup wizard, media libraries, LDAP config
    return c.configure(ctx, state)
}
```

Functionally identical to today, just registered by container name instead
of app name.

## Template Variables

Container specs use `{{var}}` syntax, same as today. Variables are resolved
from config/secrets at startup:

| Variable | Source |
|----------|--------|
| `{{appDataDir}}` | `$BLOUD_DATA_DIR/apps/{appName}` |
| `{{dataDir}}` | `$BLOUD_DATA_DIR` |
| `{{postgresPassword}}` | Generated per-app from secrets.json |
| `{{authentikSecretKey}}` | Generated from secrets.json |
| `{{authentikLdapToken}}` | Retrieved at runtime (PostStart writes it) |

For runtime-derived values (like LDAP token), the container that needs it
declares the template variable. On first boot, the variable is empty and
the container starts with a placeholder. PostStart obtains the real value,
writes it to the secret store, and the orchestrator recreates the container
with the resolved value on the next convergence pass. This avoids any
"deferred container" concept: all containers start in dependency order,
some just need a second convergence pass to get their final config.

## Networking

### Two scopes

- **App-internal network** (`{appName}-internal`): created per app. Internal
  services (postgres, redis, workers) communicate here by container name.
  Not reachable from other apps.

- **`apps-net`** (shared): containers that need to be routed by Traefik or
  reached by other apps join this network. A container can be on both networks.

### Traefik routing

Traefik runs on host network. It routes to app containers via
`localhost:{published_port}`. Only containers that publish ports and join
`apps-net` are routable. Internal services (per-app postgres, redis) don't
publish ports and are invisible outside their app network.

### DNS resolution

Within `{appName}-internal`, containers resolve each other by container name
(podman network DNS). Cross-app communication goes through `apps-net` using
container names. The orchestrator ensures both networks exist before starting
containers.

## Bootstrap Phase

System infrastructure starts in a bootstrap phase **before** the orchestrator:

```
Bootstrap:
  1. Start Traefik (config files + container)
  2. Health check Traefik

Orchestrator starts:
  3. Converge all installed apps (including authentik's sub-graph)
  4. Ready() signal → HTTP listener opens
```

Bootstrap is sequential, fail-fast. If Traefik can't start, the process exits.
The orchestrator handles everything else including multi-container system apps
like Authentik.

**Shared utilities** between bootstrap and orchestrator:
- Container runtime (`container.Runtime`: Ensure, Remove, Inspect)
- Health polling (`configurator.WaitForHTTP`, container health checks)
- Spec hashing (for idempotent container creation)
- Network management (`EnsureNetwork`)

## Volume and Data Management

```
$BLOUD_DATA_DIR/
  apps/
    authentik/
      postgres/         ← apps-authentik-postgres data
      media/            ← apps-authentik-server media
    jellyfin/
      config/           ← apps-jellyfin config
      media/            ← apps-jellyfin media
    immich/
      postgres/         ← apps-immich-postgres data
      upload/           ← apps-immich-server uploads
      model-cache/      ← apps-immich-ml cache
```

`{{appDataDir}}` resolves to `$BLOUD_DATA_DIR/apps/{appName}`. Volume
sources are relative to that. Every app's private state sits under `apps/`
so one operator-level path covers all of it, and an app named `media` or
`downloads` cannot collide with the shared trees one level above.

Uninstall with `clearData: true` removes
`$BLOUD_DATA_DIR/apps/{appName}/` entirely.
Uninstall with `clearData: false` removes containers but preserves data.

## App Store Model

The app store still tracks logical apps (one row per app). Container nodes
are ephemeral graph state, not persisted. On restart:

1. App store says "authentik is installed"
2. Orchestrator reads authentik's metadata.yaml → creates 5 graph nodes
3. Convergence inspects each container → already running → no-op
4. Health checks pass → nodes marked RUNNING → app status "running"

No schema change to the app store. The graph is rebuilt from metadata on
every startup (same as today).

## Open Questions

- **Postgres major version upgrades**: Changing the image tag from `pg16` to
  `pg17` requires `pg_upgrade`, not just a container recreate. This needs
  app-specific migration logic, likely in a configurator's PreStart. Not
  addressed by this spec.

- **Crash detection between convergence passes** (**closed**): the concern was
  that a container dying between passes went unnoticed. Two changes closed it.
  Container drift is now written into the graph node, so the next pass acts on it
  (ledger item 8, 2026-09-27), and the self-healing timer submits a pass on an idle
  ~60s cadence, so that next pass always comes (ledger item 25, 2026-09-30).
  `restartPolicy: always` still handles the common case at the container level.

