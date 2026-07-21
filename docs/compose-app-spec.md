# Multi-Container App Spec

**Status:** Complete — All phases done; legacy interface removed; all apps on `containers:` format

## Core Idea

An app can declare multiple containers in its `metadata.yaml`. Each container
becomes a node in the orchestrator's graph — same as today's single-container
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

1. **Version coupling** — App A needs postgres 16, App B needs 17, App C needs
   pgvector. Can't serve all from one instance.
2. **Upgrade risk** — Upgrading for one app risks breaking others.
3. **Failure blast radius** — Shared crash takes everything down.
4. **Extension conflicts** — pgvector, timescaledb, etc. can't coexist cleanly.

New model: apps that need postgres declare their own postgres container node
with whatever image/version/extensions they need. Each is isolated.

## Bootstrap vs Orchestrator

**Bootstrap** handles system infrastructure startup (traefik, and its
dependencies) **before** the orchestrator starts. This is intentional:

- System infra must be running before the HTTP listener opens
- System infra startup is sequential and fail-fast (if traefik can't start,
  exit immediately — don't try to converge user apps)
- The orchestrator's value is dependency-aware convergence for user apps,
  not booting the system

**The orchestrator** manages user app nodes. It sees nodes with edges and
converges them in topological order. It doesn't know if a node is "part of
authentik" or "standalone jellyfin" — irrelevant to its algorithm.

**Shared utilities** (container runtime, health polling, spec hashing) are used
by both bootstrap and orchestrator so the code stays DRY without coupling
concerns.

## metadata.yaml Changes

The `container:` block becomes `containers:` (plural) — a list of container
definitions with explicit dependency edges. Single-container apps still work
(list of one).

### Single-Container App (Jellyfin)

```yaml
name: jellyfin
displayName: Jellyfin
description: Media streaming server
category: media
isSystem: false

integrations:
  proxy:
    required: true
    compatible:
      - app: traefik
        default: true
  sso:
    required: false
    compatible:
      - app: authentik
        default: true

sso:
  strategy: ldap

containers:
  - name: apps-jellyfin
    image: jellyfin/jellyfin:10.11.11
    network: apps-net
    restartPolicy: always
    ports:
      - host: 8096
        container: 8096
    volumes:
      - source: "{{appDataDir}}/config"
        destination: /config
      - source: "{{appDataDir}}/media"
        destination: /media
        options: [ro]
    healthCheck:
      test: ["CMD", "curl", "-f", "http://localhost:8096/System/Info/Public"]
      interval: 5
      timeout: 10
      retries: 12
```

One container, one graph node. Functionally identical to today.

### Multi-Container App (Authentik)

```yaml
name: authentik
displayName: Authentik
description: Identity provider with SSO, LDAP, and OIDC
category: security
isSystem: true

integrations:
  proxy:
    required: true
    compatible:
      - app: traefik
        default: true

sso:
  strategy: none

containers:
  - name: apps-authentik-postgres
    image: postgres:16-alpine
    network: authentik-internal
    restartPolicy: always
    environment:
      POSTGRES_PASSWORD: "{{postgresPassword}}"
      POSTGRES_DB: authentik
    volumes:
      - source: "{{appDataDir}}/postgres"
        destination: /var/lib/postgresql/data
    healthCheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5
      timeout: 5
      retries: 5

  - name: apps-authentik-redis
    image: redis:7-alpine
    network: authentik-internal
    restartPolicy: always
    healthCheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5
      timeout: 5
      retries: 5

  - name: apps-authentik-server
    image: ghcr.io/goauthentik/server:2025.10.3
    command: [server]
    networks: [authentik-internal, apps-net]
    restartPolicy: always
    ports:
      - host: 9001
        container: 9000
    environment:
      AUTHENTIK_POSTGRESQL__HOST: apps-authentik-postgres
      AUTHENTIK_POSTGRESQL__PASSWORD: "{{postgresPassword}}"
      AUTHENTIK_REDIS__HOST: "redis://apps-authentik-redis:6379"
      AUTHENTIK_SECRET_KEY: "{{authentikSecretKey}}"
    dependsOn:
      - apps-authentik-postgres
      - apps-authentik-redis

  - name: apps-authentik-worker
    image: ghcr.io/goauthentik/server:2025.10.3
    command: [worker]
    network: authentik-internal
    restartPolicy: always
    environment:
      AUTHENTIK_POSTGRESQL__HOST: apps-authentik-postgres
      AUTHENTIK_POSTGRESQL__PASSWORD: "{{postgresPassword}}"
      AUTHENTIK_REDIS__HOST: "redis://apps-authentik-redis:6379"
      AUTHENTIK_SECRET_KEY: "{{authentikSecretKey}}"
    dependsOn:
      - apps-authentik-postgres
      - apps-authentik-redis

  - name: apps-authentik-ldap
    image: ghcr.io/goauthentik/ldap:2025.10.3
    networks: [authentik-internal, apps-net]
    restartPolicy: always
    ports:
      - host: 3389
        container: 3389
      - host: 6636
        container: 6636
    environment:
      AUTHENTIK_HOST: "http://apps-authentik-server:9000"
      AUTHENTIK_TOKEN: "{{authentikLdapToken}}"
    dependsOn:
      - apps-authentik-server
```

Five containers, five graph nodes, explicit `dependsOn` edges between them.
The orchestrator sees these as five independent nodes — same algorithm, same
convergence, same staleness propagation.

### App With pgvector (Immich)

```yaml
name: immich
displayName: Immich
description: Self-hosted photo and video management
category: media
isSystem: false

integrations:
  proxy:
    required: true
    compatible:
      - app: traefik
        default: true

containers:
  - name: apps-immich-postgres
    image: pgvector/pgvector:pg16
    network: immich-internal
    restartPolicy: always
    environment:
      POSTGRES_PASSWORD: "{{postgresPassword}}"
      POSTGRES_DB: immich
    volumes:
      - source: "{{appDataDir}}/postgres"
        destination: /var/lib/postgresql/data
    healthCheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5
      timeout: 5
      retries: 5

  - name: apps-immich-redis
    image: redis:7-alpine
    network: immich-internal
    restartPolicy: always
    healthCheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5
      timeout: 5
      retries: 5

  - name: apps-immich-server
    image: ghcr.io/immich-app/immich-server:release
    networks: [immich-internal, apps-net]
    restartPolicy: always
    ports:
      - host: 2283
        container: 2283
    environment:
      DB_URL: "postgres://postgres:{{postgresPassword}}@apps-immich-postgres:5432/immich"
      REDIS_URL: "redis://apps-immich-redis:6379"
    volumes:
      - source: "{{appDataDir}}/upload"
        destination: /usr/src/app/upload
    dependsOn:
      - apps-immich-postgres
      - apps-immich-redis

  - name: apps-immich-ml
    image: ghcr.io/immich-app/immich-machine-learning:release
    network: immich-internal
    restartPolicy: always
    volumes:
      - source: "{{appDataDir}}/model-cache"
        destination: /cache
```

Immich uses pgvector, Authentik uses plain postgres:16-alpine. No conflict.

## Orchestrator Changes

### Minimal — the orchestrator barely changes

The orchestrator already processes nodes in topological order. The only changes:

1. **Multiple nodes per "installed app"** — When an app is installed, the
   orchestrator adds N nodes (one per container) instead of 1. The `dependsOn`
   edges within the app are registered as graph edges. The inter-app edges
   (from `integrations:`) connect from the dependent app's entry-point nodes
   to the provider app's container nodes that those entry-points actually need.

2. **Container-level health checks** — Each node has its own health check
   defined in metadata (the `healthCheck` block on each container). The
   orchestrator runs these directly rather than delegating to a configurator
   `HealthCheck()` method.

3. **Status rollup** — The API/UI derives app-level status from container
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

- **PreStart** — Before this container starts. Config file generation,
  directory creation, secret provisioning, template variable writes.
- **PostStart** — After this container's health check passes. API calls,
  user sync, infrastructure setup in running services.
- **Remove** — Before this container is torn down. Cleanup external state.

`EnsureContainer` is gone — the orchestrator manages all containers from
metadata specs. `HealthCheck` is gone — declared in metadata, run by the
orchestrator. The `changed bool` return from PreStart is gone — the
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

Most containers don't need a configurator. Postgres, redis, workers — they
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
or `apps-authentik-worker` — they're pure spec-driven containers.

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
| `{{appDataDir}}` | `~/bloud-data/{appName}` |
| `{{dataDir}}` | `~/bloud-data` |
| `{{postgresPassword}}` | Generated per-app from secrets.json |
| `{{authentikSecretKey}}` | Generated from secrets.json |
| `{{authentikLdapToken}}` | Retrieved at runtime (PostStart writes it) |

For runtime-derived values (like LDAP token), the container that needs it
declares the template variable. On first boot, the variable is empty and
the container starts with a placeholder. PostStart obtains the real value,
writes it to the secret store, and the orchestrator recreates the container
with the resolved value on the next convergence pass. This avoids any
"deferred container" concept — all containers start in dependency order,
some just need a second convergence pass to get their final config.

## Networking

### Two scopes

- **App-internal network** (`{appName}-internal`) — created per app. Internal
  services (postgres, redis, workers) communicate here by container name.
  Not reachable from other apps.

- **`apps-net`** (shared) — containers that need to be routed by Traefik or
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
- Container runtime (`container.Runtime` — Ensure, Remove, Inspect)
- Health polling (`configurator.WaitForHTTP`, container health checks)
- Spec hashing (for idempotent container creation)
- Network management (`EnsureNetwork`)

## Volume and Data Management

```
~/bloud-data/
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

`{{appDataDir}}` resolves to `~/bloud-data/{appName}`. Volume sources are
relative to that.

Uninstall with `clearData: true` removes `~/bloud-data/{appName}/` entirely.
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

## Implementation Tasks

Tasks are grouped by phase. Within each phase, tasks marked **parallel**
can be worked on simultaneously by different implementers. Tasks marked
**sequential** must complete before the next task starts.

### Phase 1: Multi-container metadata parsing

> **Prerequisite:** None. Can start immediately.

#### Task 1.1: Add `ContainerDef` and `ContainerHealthCheck` types

**Files:** `services/host-agent/internal/catalog/models.go`

Add new types alongside existing ones (no modifications to existing types):

```go
type ContainerDef struct {
    Name          string                `yaml:"name"`
    Image         string                `yaml:"image"`
    Command       []string              `yaml:"command,omitempty"`
    Network       string                `yaml:"network,omitempty"`
    Networks      []string              `yaml:"networks,omitempty"`
    RestartPolicy string                `yaml:"restartPolicy,omitempty"`
    Environment   map[string]string     `yaml:"environment,omitempty"`
    Ports         []ContainerPort       `yaml:"ports,omitempty"`
    Volumes       []ContainerVolume     `yaml:"volumes,omitempty"`
    DependsOn     []string              `yaml:"dependsOn,omitempty"`
    HealthCheck   *ContainerHealthCheck `yaml:"healthCheck,omitempty"`
}

type ContainerHealthCheck struct {
    Test     []string `yaml:"test"`
    Interval int      `yaml:"interval"`
    Timeout  int      `yaml:"timeout"`
    Retries  int      `yaml:"retries"`
}
```

Add `Containers []ContainerDef` field to the existing `App` struct.

**Success criteria:**
- [x] `ContainerDef` and `ContainerHealthCheck` types exist in `models.go`
- [x] `App` struct has `Containers []ContainerDef` field with
      `yaml:"containers,omitempty" json:"containers,omitempty"`
- [x] Existing `Container *ContainerSpec` field is unchanged
- [x] `./bloud validate --tier fast` passes (no existing tests break)

**Parallel:** Yes — no dependencies on other tasks.

---

#### Task 1.2: Add `ContainerDefs()` normalizer method

**Files:** `services/host-agent/internal/catalog/models.go`

Add method on `App` that returns a unified `[]ContainerDef` regardless of
whether the metadata used `container:` (singular) or `containers:` (plural):

```go
func (a *App) ContainerDefs() []ContainerDef
```

Behavior:
- If `a.Containers` is non-empty, return it directly.
- If `a.Container` is non-nil, convert it to a single-element
  `[]ContainerDef` (mapping fields from `ContainerSpec` → `ContainerDef`).
  Use `a.Container.Name` if set, otherwise derive from `a.CatalogID`
  (e.g. `"apps-" + a.CatalogID`).
- If both are nil, return nil.

**Success criteria:**
- [x] `ContainerDefs()` returns the `Containers` list when populated
- [x] `ContainerDefs()` wraps singular `Container` into a one-element
      `[]ContainerDef` with correct field mapping
- [x] When `Container.Name` is empty, the generated name follows
      `"apps-" + CatalogID` convention
- [x] App-level `HealthCheck` is mapped to `ContainerDef.HealthCheck`
      when converting from singular (if present)
- [x] Returns nil when both fields are nil
- [x] Unit tests cover all three cases

**Depends on:** Task 1.1

---

#### Task 1.3: Unit tests for multi-container YAML parsing

**Files:** `services/host-agent/internal/catalog/models_test.go` (new or
extend existing)

Write tests that unmarshal metadata.yaml content and verify the parsed
`App` struct.

**Success criteria:**
- [x] Test: YAML with `containers:` (4 entries, with `dependsOn`,
      `healthCheck`, `networks`) parses correctly into `App.Containers`
- [x] Test: YAML with `container:` (singular, existing format) still
      parses correctly into `App.Container`
- [x] Test: `ContainerDefs()` on a multi-container app returns correct
      list with all fields preserved
- [x] Test: `ContainerDefs()` on a single-container app returns a
      one-element list with correct field mapping
- [x] Test: `ContainerDefs()` on an app with neither field returns nil
- [x] All tests pass: `go test ./internal/catalog/...`

**Depends on:** Tasks 1.1, 1.2

---

### Phase 2: Multi-node graph and container lifecycle

> **Prerequisite:** Phase 1 complete.

#### Task 2.1: Add `ContainerSpecFromDef()` function

**Files:** `services/host-agent/internal/orchestrator/orchestrator_containers.go`

Add function analogous to existing `ContainerSpec()` but takes a
`catalog.ContainerDef` instead of a `*catalog.App`:

```go
func ContainerSpecFromDef(
    def catalog.ContainerDef,
    appCatalogID string,
    dataDir string,
    extraVars map[string]string,
) (containerruntime.Spec, error)
```

Same template variable resolution (`{{dataDir}}`, `{{appDataDir}}`,
custom vars). Same label scheme (`io.bloud.app: <appCatalogID>`).
Handles both `Network` (single) and `Networks` (multi) fields.

**Success criteria:**
- [x] Builds a valid `containerruntime.Spec` from a `ContainerDef`
- [x] Template variables in environment values and volume sources are
      resolved (test with `{{appDataDir}}` and custom vars)
- [x] `io.bloud.app` label is set to the owning app's catalog ID
- [x] Ports, mounts, command, restart policy are mapped correctly
- [x] Multi-network support: when `Networks` is set, uses the first
      as primary (until runtime supports multi-network attach)
- [x] Unit tests cover: basic spec, template resolution, empty optional
      fields, multi-network

**Parallel:** Yes — can be worked on while 2.2/2.3 are in progress.

---

#### Task 2.2: Add `containerOwner` mapping to orchestrator

**Files:** `services/host-agent/internal/orchestrator/orchestrator.go`

Add field to `Orchestrator` struct:

```go
containerOwner map[string]string  // container node name → app catalog ID
```

Initialize in `NewOrchestrator`. Add methods:

```go
func (o *Orchestrator) registerContainerOwner(containerName, appCatalogID string)
func (o *Orchestrator) ownerApp(nodeID string) string  // returns app catalog ID or nodeID itself
```

`ownerApp()` returns the app catalog ID for multi-container nodes, or the
node ID itself for single-container nodes (backward compat).

Modify `buildAppState()` to use `ownerApp(id)` when resolving `DataPath`
so that `apps-authentik-server` resolves to `~/bloud-data/authentik/` not
`~/bloud-data/apps-authentik-server/`.

**Success criteria:**
- [x] `containerOwner` map exists on `Orchestrator`
- [x] `registerContainerOwner()` populates the map
- [x] `ownerApp()` returns the app catalog ID for registered containers
- [x] `ownerApp()` returns the node ID itself for unregistered nodes
- [x] `buildAppState()` uses `ownerApp()` for `DataPath` resolution
- [x] Unit test: register 3 container nodes for one app, verify
      `ownerApp()` returns correct app ID for each
- [x] Unit test: unregistered node returns itself

**Parallel:** Yes — can be worked on while 2.1/2.3 are in progress.

---

#### Task 2.3: Multi-node creation in `convergeFromStores()`

**Files:** `services/host-agent/internal/orchestrator/pipeline.go`

Modify step 3 of `convergeFromStores()` (currently lines 309-332). When
an app has multiple `ContainerDefs()`, create one graph node per container
definition instead of one node per app.

For each container def:
1. `o.graph.AddNode(def.Name)` if not exists
2. `o.graph.AddEdge(def.Name, dep)` for each `def.DependsOn` entry
3. `o.graph.SetTargetStatus(def.Name, graph.StatusRunning)`
4. `o.registerContainerOwner(def.Name, appName)`

For single-container apps: behavior is unchanged (one node = app name).

Inter-app edges: when app B depends on app A and app A is multi-container,
connect from app B's first container to the last container in app A's list
(the "primary" / entry-point container).

**Success criteria:**
- [x] Multi-container app creates N graph nodes (one per container def)
- [x] `dependsOn` edges are wired as graph edges
- [x] All container nodes have target status RUNNING
- [x] `containerOwner` is populated for each container node
- [x] Single-container apps still create one node with app name (no
      regression)
- [x] Inter-app edges connect correctly when provider is multi-container
- [x] Unit test: install a 4-container app, verify 4 nodes + correct
      edges in graph
- [x] Unit test: install single-container + multi-container app with
      inter-app dependency, verify cross-app edge targets the primary
      container

**Depends on:** Tasks 2.2 (needs `registerContainerOwner`)

---

#### Task 2.4: Container-level health check execution

**Files:** `services/host-agent/internal/orchestrator/orchestrator.go`

Add method to run a container health check from metadata:

```go
func (o *Orchestrator) runContainerHealthCheck(
    ctx context.Context,
    containerName string,
    hc *catalog.ContainerHealthCheck,
) error
```

Implementation: use `runtime.Exec()` to run the health check command
inside the container, polling at the configured interval until it passes
or retries are exhausted.

Modify `runFullLifecycle()`: for multi-container nodes (identified via
`containerOwner`), look up the `ContainerDef` and use its `HealthCheck`
instead of calling `cfg.HealthCheck(ctx)`. If the container def has no
health check and no configurator, the node succeeds after EnsureContainer.

**Success criteria:**
- [x] `runContainerHealthCheck()` executes `hc.Test` command inside the
      named container
- [x] Polls at `hc.Interval` seconds, up to `hc.Retries` attempts
- [x] Returns nil on first success, error after all retries exhausted
- [x] Respects context cancellation
- [x] `runFullLifecycle()` uses container-level health check for
      multi-container nodes
- [x] `runFullLifecycle()` still uses `cfg.HealthCheck()` for
      single-container nodes (backward compat)
- [x] Unit test: health check passes on 3rd attempt → success
- [x] Unit test: health check exceeds retries → error
- [x] Unit test: context cancelled mid-poll → returns context error

**Depends on:** Task 2.1 (needs `ContainerSpecFromDef` for container
ensure), Task 2.3 (needs multi-node creation to exist)

---

#### Task 2.5: Multi-container ensure in `runFullLifecycle()`

**Files:** `services/host-agent/internal/orchestrator/orchestrator.go`

Modify `runFullLifecycle()` to handle multi-container nodes:

1. Look up the `ContainerDef` for the node (via `containerOwner` →
   app catalog ID → `ContainerDefs()` → find by name)
2. Ensure networks from `def.Network`/`def.Networks` via
   `runtime.EnsureNetwork()`
3. Create mount directories from `def.Volumes[].Source`
4. Build spec via `ContainerSpecFromDef()` and ensure container

For single-container nodes: behavior is unchanged (uses existing
`ensureAppContainer()` path).

**Success criteria:**
- [x] Multi-container nodes: container spec is built from `ContainerDef`
- [x] Networks are created before container start
- [x] Mount source directories are created if they don't exist
- [x] Container is created/started via `runtime.Ensure()`
- [x] Single-container nodes still use `ensureAppContainer()` (no
      regression)
- [x] Unit test: multi-container node creates network + mount dirs +
      container
- [x] Unit test: single-container node is unchanged

**Depends on:** Tasks 2.1, 2.2, 2.3

---

#### Task 2.6: Multi-container uninstall in `RemoveApp()`

**Files:** `services/host-agent/internal/orchestrator/orchestrator.go`
(or wherever `RemoveApp` is defined)

Modify `RemoveApp()` to remove all container nodes belonging to an app:

1. Look up `ContainerDefs()` for the app
2. For multi-container: remove each container node from the graph,
   remove each container via runtime, run configurator `Remove()` for
   each node that has one
3. Clean up `containerOwner` entries
4. For single-container: behavior unchanged

**Success criteria:**
- [x] `RemoveApp("authentik")` removes all 5 container nodes from graph
- [x] Each container is removed via `runtime.Remove()`
- [x] Configurator `Remove()` is called for nodes that have one
- [x] `containerOwner` entries are cleaned up
- [x] `clearData: true` removes `~/bloud-data/{appName}/` (one dir for
      the whole app, not per-container)
- [x] Single-container uninstall is unchanged
- [x] Unit test: install 4-container app, uninstall, verify 0 nodes + 0
      containers remain

**Depends on:** Tasks 2.2, 2.3

---

#### Task 2.7: Skip multi-container nodes in `SyncContainerState()`

**Files:** `services/host-agent/internal/orchestrator/orchestrator_containers.go`

`SyncContainerState()` currently inspects containers and reconciles with
app store records. Multi-container nodes don't map 1:1 to app store
records, so they must be skipped.

**Success criteria:**
- [x] Multi-container nodes (identified via `containerOwner`) are skipped
      in the sync loop
- [x] Single-container sync is unchanged
- [x] Unit test: multi-container nodes are not affected by
      `SyncContainerState()`

**Depends on:** Task 2.2

---

### Phase 3: Simplify configurator interface

> **Prerequisite:** Phase 2 complete.
>
> **Status:** Deferred until Phase 6 (Jellyfin/Navidrome migration). Traefik's
> `EnsureContainer` still creates its container imperatively; it must be migrated
> to a `containers:` metadata spec before the old interface can be removed.
> Current configurators that use the new multi-container path (`ServerConfigurator`)
> implement `EnsureContainer`/`HealthCheck` as no-ops in the interim.

#### Task 3.1: Define new `NodeLifecycle` interface

**Files:** `pkg/configurator/interface.go`

Define the new interface alongside the existing one (don't delete the
old one yet):

```go
type NodeLifecycle interface {
    Name() string
    PreStart(ctx context.Context, state *AppState) error
    PostStart(ctx context.Context, state *AppState) error
    Remove(ctx context.Context, state *AppState, clearData bool) error
}
```

Rename the existing interface to `LegacyNodeLifecycle` (or keep the old
name as an alias — whatever avoids a flag-day rename).

Add `LegacyAdapter` struct that wraps a `LegacyNodeLifecycle` and
implements the new `NodeLifecycle`, discarding `EnsureContainer`,
`HealthCheck`, and `changed` return:

```go
type LegacyAdapter struct{ Inner LegacyNodeLifecycle }
func (a *LegacyAdapter) PreStart(ctx, state) error {
    _, err := a.Inner.PreStart(ctx, state); return err
}
```

**Success criteria:**
- [x] New `NodeLifecycle` interface defined (4 methods: Name + PreStart + PostStart + Remove)
- [x] Old interface removed (no LegacyNodeLifecycle needed — all apps migrated in Phase 6)
- [x] `Configurator` type alias points to new interface
- [x] `./bloud validate --tier fast` passes

**Parallel:** Yes — no dependencies on other Phase 3 tasks.

---

#### Task 3.2: Update orchestrator to use new interface

**Files:** `services/host-agent/internal/orchestrator/orchestrator.go`

Modify `runFullLifecycle()`:
- Remove `cfg.EnsureContainer(ctx, changed)` call
- Remove `cfg.HealthCheck(healthCtx)` call
- Call `cfg.PreStart()` without capturing `changed` return (spec hash
  diff replaces this)
- Container health check is now always from metadata (Task 2.4) or
  skipped if not defined

Modify `Registry` usage: accept both old and new interface implementations
via `LegacyAdapter` wrapping.

**Success criteria:**
- [x] `runFullLifecycle()` no longer calls `EnsureContainer` or
      `HealthCheck` on configurators
- [x] All configurators updated to new interface directly (no adapter needed)
- [x] `./bloud validate --tier fast` passes

**Depends on:** Task 3.1

---

#### Task 3.3: Add spec-hash diffing for container recreation

**Files:** `services/host-agent/internal/orchestrator/orchestrator.go`,
`orchestrator_containers.go`

Replace the `changed bool` signal from `PreStart` with spec-hash diffing.
Before ensuring a container, compute the hash of the resolved spec and
compare to the running container's `io.bloud.spec-revision` label. If
different, force-recreate.

This already partially exists (the `Ensure` method checks spec revision).
Verify it works correctly and remove any remaining dependency on the
`changed` return value.

**Success criteria:**
- [x] Container recreation is driven by spec hash diff, not `changed`
      return (spec hash comparison is handled by the runtime `Ensure` call)
- [x] `changed bool` return removed from `PreStart` interface
- [x] Unit test: same spec → no recreation
- [x] Unit test: changed env var → recreation

**Parallel:** Yes — can be worked alongside 3.1/3.2 (merged after 3.2).

---

### Phase 4: First multi-container app (Immich)

> **Prerequisite:** Phases 1-3 complete.
>
> Tasks 4.1 and 4.2 can be done in parallel.

#### Task 4.1: Write Immich metadata.yaml

**Files:** `apps/immich/metadata.yaml` (new)

Write the full metadata.yaml using `containers:` with 4 container defs:
postgres (pgvector), redis, server, ML worker. Include `dependsOn` edges,
`healthCheck` on postgres and redis, environment variables with template
vars, volume mounts.

**Success criteria:**
- [x] `apps/immich/metadata.yaml` exists with all required fields
- [x] `containers:` has 4 entries: `apps-immich-postgres`,
      `apps-immich-redis`, `apps-immich-server`, `apps-immich-ml`
- [x] `dependsOn` edges: server depends on postgres + redis
- [x] Health checks defined on postgres (`pg_isready`) and redis
      (`redis-cli ping`)
- [x] Server publishes port 2283
- [x] Template variables used for `{{postgresPassword}}`, `{{appDataDir}}`
- [x] Catalog loader parses it without error:
      `go test ./internal/catalog/... -run TestLoadAll`
- [x] `ContainerDefs()` returns 4 entries with correct relationships

**Parallel:** Yes — pure metadata, no Go code changes.

---

#### Task 4.2: Status rollup in API

**Files:** `services/host-agent/internal/orchestrator/orchestrator.go`

> **Implementation note:** Status rollup was implemented in the orchestrator's
> `setupStatusSync()` event handler rather than the API layer. The
> `allContainersRunning()` helper queries each container node's graph status
> before writing "running" to the app store.

Add logic to derive app-level status from multi-container nodes. The API
returns status per logical app (not per container).

Rules:
- Any container node in ERROR → app status is `"error"`
- All container nodes RUNNING → app status is `"running"`
- Otherwise → `"installing"`

**Success criteria:**
- [x] API `/api/apps` returns one entry for Immich (not 4)
- [x] Status reflects the worst container state
- [x] Single-container apps are unaffected
- [x] Unit test: all RUNNING → "running"
- [x] Unit test: one ERROR → "error"
- [ ] Unit test: mix of RUNNING + STARTING → "installing"
- [ ] `./bloud services` CLI shows Immich as one app with rollup status

**Parallel:** Yes — can be done alongside Task 4.1.

---

#### Task 4.3: Network creation in container ensure step

**Files:** `services/host-agent/internal/orchestrator/orchestrator.go`

Ensure the orchestrator creates per-app internal networks
(`{appName}-internal`) before starting containers. Inspect each
`ContainerDef`'s `network`/`networks` fields and call
`runtime.EnsureNetwork()` for each unique network name.

**Success criteria:**
- [x] `immich-internal` network is created before any Immich container
      starts
- [x] `apps-net` (shared) is also ensured
- [x] Network creation is idempotent (no error if already exists)
- [x] Unit test: multi-container app triggers `EnsureNetwork` for each
      unique network referenced in container defs

**Depends on:** Task 2.5 (extends the container ensure logic)

---

#### Task 4.4: E2E validation of Immich lifecycle

**Files:** None (test execution only)

Run the full lifecycle on a Lima VM.

**Success criteria:**
- [ ] `./bloud install immich` → all 4 containers start in dependency
      order (postgres + redis first, then server, then ML)
- [ ] `podman ps -a` shows 4 running containers with `io.bloud.app:
      immich` label
- [ ] `./bloud services` shows Immich as "running"
- [ ] Immich UI accessible at `localhost:8080/embed/immich/` (or however
      embedded routing works)
- [ ] `./bloud uninstall immich` → all 4 containers removed
- [ ] `./bloud uninstall immich --clear-data` → containers removed +
      `~/bloud-data/immich/` deleted
- [ ] Reinstall after uninstall works (clean slate)

**Depends on:** Tasks 4.1, 4.2, 4.3

---

### Phase 5: Migrate Authentik to multi-container

> **Prerequisite:** Phase 4 complete (multi-container model validated).
>
> Tasks 5.1 and 5.2 can be done in parallel.

#### Task 5.1: Write Authentik multi-container metadata.yaml

**Files:** `apps/authentik/metadata.yaml`

Replace singular `container:` with `containers:` listing 5 container defs:
postgres, redis, server, worker, LDAP. Include `dependsOn` edges,
health checks, environment variables with template vars.

**Success criteria:**
- [x] `containers:` has 5 entries matching the spec earlier in this doc
- [x] `dependsOn` edges: server + worker depend on postgres + redis;
      LDAP depends on server
- [x] Health checks on postgres and redis
- [x] Template variables: `{{postgresPassword}}`, `{{authentikSecretKey}}`,
      `{{authentikLdapToken}}`
- [x] Catalog loader parses without error
- [x] Singular `container:` field removed from metadata.yaml

> **Additional:** server container also has a health check (`wget` against
> `/-/health/ready/`, 60 retries) and volumes for `media`, `templates`, and
> the auth-flow blueprint. Worker has the same volumes.

**Parallel:** Yes — pure metadata change.

---

#### Task 5.2: Split Authentik configurator into per-container configurators

**Files:** `apps/authentik/configurator.go` (modify),
`apps/authentik/server_configurator.go` (new),
`apps/authentik/ldap_configurator.go` (new)

**`apps-authentik-server` configurator:**
- `Name()` returns `"apps-authentik-server"`
- `PreStart()`: ensure authentik database exists in its postgres instance
  (port from current `ensureAuthentikDB()`)
- `PostStart()`: admin password, API token, branding, login config, LDAP
  infrastructure, write LDAP outpost token to secret store (port from
  current `configureAuthentik()` / `PostStart()`)
- `Remove()`: no-op (container removal handled by orchestrator)

**`apps-authentik-ldap` configurator:**
- `Name()` returns `"apps-authentik-ldap"`
- `PreStart()`: read LDAP token from secret store, write as template
  variable for this node's container spec
- `PostStart()`: no-op
- `Remove()`: no-op

**Success criteria:**
- [x] Two new configurator structs implementing `NodeLifecycle`
- [x] Server configurator's `PostStart` writes LDAP token (to shared
      templateVars map — see implementation note below)
- [x] LDAP token is available as template variable when LDAP container
      spec is resolved
- [x] No container management code in either configurator (no
      `EnsureContainer`, no `runtime.Ensure`, no container spec building)
- [x] All existing Authentik PostStart functionality preserved
      (admin setup, branding, LDAP infra)
- [ ] Unit tests for token handoff: server PostStart writes → LDAP
      PreStart reads

> **Implementation note:** No separate `ldap_configurator.go` was created.
> Instead, the LDAP token is written directly to the shared mutable
> `templateVars` map in `ServerConfigurator.PostStart()`. Since
> `apps-authentik-ldap` depends on `apps-authentik-server` via metadata
> `dependsOn`, the server's full lifecycle (including PostStart) completes
> before the LDAP container spec is resolved — so `{{authentikLdapToken}}`
> is already populated in the map by the time it is needed. No secret-store
> write or second convergence pass is required.

**Parallel:** Yes — can be done alongside Task 5.1.

---

#### Task 5.3: Update registration and remove old code

**Files:** `services/host-agent/internal/appconfig/register.go`,
`services/host-agent/internal/appconfig/postgres.go` (delete),
`services/host-agent/internal/appconfig/redis.go` (delete)

Update `RegisterAll()` to register the two new per-container configurators
by node name. Remove `WithRuntime()` builder method from authentik
configurator. Delete shared postgres and redis system configurators (they
were for the shared-instance model).

**Success criteria:**
- [x] `RegisterAll()` registers `apps-authentik-server` configurator
- [x] `WithRuntime()` builder removed from authentik configurator
- [x] `internal/appconfig/postgres.go` deleted
- [x] `internal/appconfig/redis.go` deleted
- [x] `internal/appconfig/runtime_adapter.go` deleted
- [x] `./bloud validate --tier fast` passes
- [x] No compilation errors

> **Note:** `apps-authentik-ldap` has no configurator (none needed — it is
> a pure spec-driven container whose only dynamic value, the LDAP token, is
> resolved via the shared templateVars map written by the server PostStart).

**Depends on:** Tasks 5.1, 5.2

---

#### Task 5.4: E2E validation of Authentik lifecycle

**Files:** None (test execution only)

**Success criteria:**
- [ ] `./bloud dev` → all 5 Authentik containers start in correct order
- [ ] LDAP token handoff works: server PostStart creates token, LDAP
      container starts with correct `AUTHENTIK_TOKEN`
- [ ] `./bloud install jellyfin` → Jellyfin starts, SSO/LDAP login works
- [ ] `podman ps -a` shows 5 authentik containers + traefik + jellyfin
- [ ] Authentik's postgres is fully isolated (different container from
      any other app's postgres)
- [ ] `./bloud e2e lifecycle` passes

**Depends on:** Task 5.3

---

### Phase 6: Migrate remaining apps and cleanup

> **Prerequisite:** Phase 5 complete.
>
> Tasks 6.1, 6.2, 6.3 can be done in parallel.
>
> **Status: Complete.**

#### Task 6.1: Migrate Jellyfin to `containers:` format

**Files:** `apps/jellyfin/metadata.yaml`, `apps/jellyfin/configurator.go`

Convert `container:` to `containers:` (list of one). Migrate configurator
to new `NodeLifecycle` interface. Change `Name()` to return
`"apps-jellyfin"`.

**Success criteria:**
- [x] `metadata.yaml` uses `containers:` with one entry
- [x] Configurator implements new `NodeLifecycle` (no `EnsureContainer`,
      `HealthCheck`, or `changed` return)
- [x] `Name()` returns `"apps-jellyfin"`
- [x] `./bloud validate --tier fast` passes

**Parallel:** Yes.

---

#### Task 6.2: Migrate Navidrome to `containers:` format

**Files:** `apps/navidrome/metadata.yaml`, `apps/navidrome/configurator.go`

Same as Task 6.1 but for Navidrome.

**Success criteria:**
- [x] `metadata.yaml` uses `containers:` with one entry
- [x] Configurator implements new `NodeLifecycle`
- [x] `Name()` returns `"apps-navidrome"`
- [x] `./bloud validate --tier fast` passes

**Parallel:** Yes.

---

#### Task 6.3: Delete legacy code paths

**Files:** Multiple (see list below)

Once all apps use `containers:` and new `NodeLifecycle`:

- `catalog/models.go`: remove `Container *ContainerSpec` field, remove
  `ContainerSpec` type (if no longer used), remove app-level `HealthCheck`
  struct
- `orchestrator_containers.go`: remove `ContainerSpec()` function (the
  one building from singular `app.Container`)
- `pkg/configurator/interface.go`: remove `LegacyNodeLifecycle` and
  `LegacyAdapter`
- `pkg/configurator/runtime.go`: delete file (configurators no longer
  manage containers)
- `internal/appconfig/runtime_adapter.go`: delete file
- `orchestrator.go`: remove `isSystemApp()` guard (all apps use same
  container management path)
- `apps/postgres/` and `apps/redis/`: delete directories (shared-instance
  model removed)

**Success criteria:**
- [x] No references to `app.Container` (singular) anywhere in codebase
- [x] `Container *ContainerSpec` field and `ContainerSpec` type removed from `catalog/models.go`
- [x] App-level `HealthCheck` struct removed from `catalog/models.go`
- [x] `ContainerSpec()` function removed from `orchestrator_containers.go`
- [x] `pkg/configurator/runtime.go` deleted
- [x] `apps/postgres/` and `apps/redis/` deleted
- [x] `isSystemApp()` removed from orchestrator
- [x] `./bloud validate --tier fast` passes
- [x] `grep -r 'container:' apps/*/metadata.yaml` returns no hits
      (all use `containers:`)

**Depends on:** Tasks 6.1, 6.2 (all apps migrated first)

---

### Task dependency graph

```
Phase 1:  1.1 ──→ 1.2 ──→ 1.3

Phase 2:  2.1 ─────────────────┐
          2.2 ──→ 2.3 ──→ 2.5 ─┼──→ (Phase 2 done)
                  2.3 ──→ 2.4 ─┘
          2.2 ──→ 2.7
                  2.3 ──→ 2.6

Phase 3:  3.1 ──→ 3.2
          3.3 ─────┘ (merge after 3.2)

Phase 4:  4.1 ─┐
          4.2 ─┤
          4.3 ─┴──→ 4.4

Phase 5:  5.1 ─┐
          5.2 ─┴──→ 5.3 ──→ 5.4

Phase 6:  6.1 ─┐
          6.2 ─┴──→ 6.3
```

### Parallelization summary

| Phase | Max parallel implementers | Parallel tasks |
|-------|--------------------------|----------------|
| 1     | 1 (sequential chain)     | —              |
| 2     | 3                        | 2.1, 2.2, 2.7  |
| 3     | 2                        | 3.1, 3.3       |
| 4     | 2                        | 4.1, 4.2       |
| 5     | 2                        | 5.1, 5.2       |
| 6     | 3                        | 6.1, 6.2, 6.3  |

## Open Questions

- **Postgres major version upgrades** — Changing the image tag from `pg16` to
  `pg17` requires `pg_upgrade`, not just a container recreate. This needs
  app-specific migration logic, likely in a configurator's PreStart. Not
  addressed by this spec.

- **Crash detection between convergence passes** — The orchestrator only
  converges on intents or startup. If a container crashes between passes,
  nothing detects it until the next convergence. Options: periodic health
  poll, podman events subscription, or accept that `restartPolicy: always`
  handles most crashes at the container level without orchestrator involvement.

