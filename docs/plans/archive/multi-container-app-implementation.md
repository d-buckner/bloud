# Implementation Tasks: the multi-container app model

> Status: done, archived 2026-10-15. This is the phase-by-phase task list that
> drove the model now specified in [`../../specs/app-spec.md`](../../specs/app-spec.md).
> It is a record of finished work, kept because the task breakdown shows how the
> migration was sequenced.

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

**Parallel:** Yes (no dependencies on other tasks).

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

**Files:** `services/host-agent/internal/engine/orchestrator/orchestrator_containers.go`

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

**Parallel:** Yes (can be worked on while 2.2/2.3 are in progress).

---

#### Task 2.2: Add `containerOwner` mapping to orchestrator

**Files:** `services/host-agent/internal/engine/orchestrator/orchestrator.go`

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

**Parallel:** Yes (can be worked on while 2.1/2.3 are in progress).

---

#### Task 2.3: Multi-node creation in `convergeFromStores()`

**Files:** `services/host-agent/internal/engine/orchestrator/pipeline.go`

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

**Files:** `services/host-agent/internal/engine/orchestrator/orchestrator.go`

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
- [x] Unit test: health check passes on the third attempt → success
- [x] Unit test: health check exceeds retries → error
- [x] Unit test: context canceled mid-poll → returns context error

**Depends on:** Task 2.1 (needs `ContainerSpecFromDef` for container
ensure), Task 2.3 (needs multi-node creation to exist)

---

#### Task 2.5: Multi-container ensure in `runFullLifecycle()`

**Files:** `services/host-agent/internal/engine/orchestrator/orchestrator.go`

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

**Files:** `services/host-agent/internal/engine/orchestrator/orchestrator.go`
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

**Files:** `services/host-agent/internal/engine/orchestrator/orchestrator_containers.go`

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
name as an alias, whatever avoids a flag-day rename).

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
- [x] Old interface removed (no LegacyNodeLifecycle needed: all apps migrated in Phase 6)
- [x] `Configurator` type alias points to new interface
- [x] `./bloud validate --tier fast` passes

**Parallel:** Yes (no dependencies on other Phase 3 tasks).

---

#### Task 3.2: Update orchestrator to use new interface

**Files:** `services/host-agent/internal/engine/orchestrator/orchestrator.go`

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

**Files:** `services/host-agent/internal/engine/orchestrator/orchestrator.go`,
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

**Parallel:** Yes (can be worked alongside 3.1/3.2; merged after 3.2).

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

**Parallel:** Yes (pure metadata, no Go code changes).

---

#### Task 4.2: Status rollup in API

**Files:** `services/host-agent/internal/engine/orchestrator/orchestrator.go`

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

**Parallel:** Yes (can be done alongside Task 4.1).

---

#### Task 4.3: Network creation in container ensure step

**Files:** `services/host-agent/internal/engine/orchestrator/orchestrator.go`

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

**Parallel:** Yes (pure metadata change).

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
      templateVars map: see implementation note below)
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
> before the LDAP container spec is resolved, so `{{authentikLdapToken}}`
> is already populated in the map by the time it is needed. No secret-store
> write or second convergence pass is required.

**Parallel:** Yes (can be done alongside Task 5.1).

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

> **Note:** `apps-authentik-ldap` has no configurator (none needed: it is
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
| 1     | 1 (sequential chain)     | none           |
| 2     | 3                        | 2.1, 2.2, 2.7  |
| 3     | 2                        | 3.1, 3.3       |
| 4     | 2                        | 4.1, 4.2       |
| 5     | 2                        | 5.1, 5.2       |
| 6     | 3                        | 6.1, 6.2, 6.3  |

