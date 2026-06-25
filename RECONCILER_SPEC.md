# Reconciler-Owned Architecture Specification

**Status:** Approved design, not yet implemented
**Last updated:** 2026-06-23

## Motivation

The host-agent currently has three inconsistent patterns for mutations and side effects:

1. **Orchestrator-owned with mutex serialization.** Install and uninstall go through
   `EnqueueInstall`/`EnqueueUninstall`, which hold `operationMu` and run the full
   lifecycle synchronously (record intent, create container, health check, post-start
   config, update status). The reconciler runs *after* as a convergence safety net.

2. **API handlers doing direct store writes + side effects.** Tailnet settings
   (`handleSetTailnet`) write to the tailnet store then spawn async goroutines to start
   sidecars and ensure gateway/proxies. Remote app handlers write to the remote app store
   then call `RegenerateRoutes()`. Rename writes directly to the app store.

3. **Reconciler as post-hoc convergence.** The reconciler runs PreStart, HealthCheck, and
   PostStart in dependency order for all installed apps. It is triggered after
   install/uninstall and on startup, but has no role in the other mutation paths.

This split creates several problems:

- **Race conditions.** A tailnet save spawns async goroutines that read from app stores
  while an install is concurrently writing to them. The mutex only serializes
  install/uninstall, not the other mutation paths.
- **No transactional guarantees.** There is no mechanism to ensure that a set of related
  mutations (install app + regenerate routes + start sidecar) either all complete or are
  retried as a unit.
- **No batching or debouncing.** Three rapid installs each trigger their own
  `RegenerateRoutes()` call. There is no way to coalesce them.
- **Inconsistent error handling.** Some paths return errors synchronously, others fire
  and forget in goroutines. The UI has no consistent way to know what succeeded.

## Core Model

Everything collapses into one pattern:

```
Caller  ->  enqueue Intent  ->  Reconciler processes queue  ->  Store mutations + side effects
```

The reconciler becomes the **single writer** to all stores and the **single executor** of
all side effects. Everything else is a reader that submits intents.

### Definitions

- **Intent:** A typed, immutable request describing a desired state change. Created by API
  handlers, enqueued for the reconciler to process. Intents carry an ID for
  logging/debugging.
- **Store (desired state):** The database records (apps, tailnet connections, remote apps,
  shares). These represent what the world *should* look like. Only the reconciler writes
  to stores.
- **Runtime (actual state):** The containers, sidecars, gateway, Traefik config, health
  endpoints. This is what the world *actually* looks like. The reconciler reads runtime
  state and converges it to match the stores.
- **Convergence:** The process of diffing desired state (stores) against actual state
  (runtime) and executing idempotent steps to make actual match desired.

## Design Decisions

### 1. API Response Model

API handlers that submit intents return **202 Accepted** with the intent ID. The frontend
watches the existing SSE stream (`GET /api/apps/events`) for status updates. The SSE
infrastructure already handles real-time app status transitions
(`installing -> starting -> running`).

### 2. Store Ownership

The reconciler owns **all writes**. API handlers and other components can read from stores
freely. Reads don't cause inconsistency; the dangerous thing is concurrent writes and side
effects, which the reconciler serializes.

Stores affected:
- `AppStoreInterface` (`store/interfaces.go`) — all write methods
- `TailnetStoreInterface` (`store/interfaces.go`) — Create, Delete
- `RemoteAppStoreInterface` (`store/interfaces.go`) — Create, Delete, SetCredential, SetStatus
- `ShareStoreInterface` (`store/interfaces.go`) — Create, Revoke

Stores **not** affected (remain directly writable):
- `PreferencesStoreInterface` — user layout preferences have no side effects and no
  interaction with the reconciler

### 3. Intent Types

Typed structs with compile-time safety. Each action is its own struct implementing a
common `Intent` interface. The reconciler uses a type switch for exhaustive handling.

### 4. Queue Design

Single FIFO queue. All intents are processed in order. This is the simplest model that
provides the transactional guarantees we need.

### 5. Reconciler Trigger

Event-driven with debounce. The reconciler sleeps until an intent is enqueued, waits a
short debounce window (~200ms) for more intents to accumulate, then drains the queue and
runs the convergence loop.

### 6. Dependency Resolution

Fully owned by the reconciler during convergence. The install intent carries only the app
name. The reconciler resolves dependencies, auto-installs what's needed, and binds
integrations. The `PlanInstall`/`PlanRemove` read-only API endpoints are removed.

### 7. Error Handling

No rollback. If an app fails (health check timeout, container won't start), it goes to
`error` status. Successfully installed dependencies stay installed. The user can retry or
uninstall.

### 8. Intent Tracking

Intent IDs exist for logging and debugging. They are returned in the 202 response and
logged throughout processing. The frontend does **not** use intent IDs — it continues
watching app status via SSE as it does today. Intent status is queryable via API for
troubleshooting but is not part of the primary UX flow.

## Reconciler Cycle

Each cycle has two phases:

### Phase 1: Drain Queue (Apply Intents to Stores)

Pull all pending intents from the FIFO queue. For each intent, apply the corresponding
store mutations. No side effects — no containers, no config files, no API calls. Just
store writes that represent what the world *should* look like.

Examples:
- `InstallAppIntent{AppName: "radarr"}` -> `appStore.Install("radarr", ...)`
- `UninstallAppIntent{AppName: "radarr"}` -> `appStore.UpdateStatus("radarr", "uninstalling")`
- `SetTailnetIntent{...}` -> `tailnetStore.Create(conn)`
- `AddRemoteAppIntent{...}` -> `remoteAppStore.Create(app)`

If three install intents arrive in quick succession, they all get drained, the store
reflects all three, and one convergence pass handles them all.

### Phase 2: Converge (Make Actual Match Desired)

Read all stores. Read runtime state. Diff. Execute idempotent steps:

1. **Sync container state.** For each app in the store, check if the container exists and
   is running. Fix mismatches (create missing containers, update status for crashed ones).

2. **Resolve dependencies.** For each app marked `installing`, resolve its dependency
   graph from the catalog. If dependencies aren't installed, create store records for
   them. This handles the case where `InstallAppIntent{radarr}` implicitly requires
   qBittorrent — the reconciler figures this out and installs both.

3. **Ensure apps (dependency-ordered).** Compute execution levels (leaf nodes first). For
   each app at each level:
   - PreStart: write config files and directories
   - Ensure container exists (Quadlet + systemd)
   - HealthCheck: wait for app to be healthy
   - PostStart: configure via APIs
   - SSO provisioning (if applicable)
   - Sidecar management (if tailnet is active)
   - Update status to `running`

4. **Handle uninstalls.** For each app marked `uninstalling`:
   - Stop sidecar (if running)
   - Remove container
   - Delete from store
   - Optionally delete data directory and database

5. **Routing convergence.** After all app state is settled:
   - Ensure gateway running (if tailnet active)
   - Reconcile remote app proxies
   - Regenerate Traefik routes

6. **Optional dependency dispatch.** Detect apps that transitioned to healthy this cycle.
   Notify parent apps with optional integrations to reconfigure.

7. **Tailnet teardown.** If tailnet was deleted (no active connection in store but
   sidecars/gateway exist in runtime), stop and purge all sidecars, gateway, and remote
   proxies.

## Intent Catalog

```go
// Intent is the interface all intent types implement.
type Intent interface {
    intentMarker()
    IntentID() string
}

type InstallAppIntent struct {
    ID      string
    AppName string
}

type UninstallAppIntent struct {
    ID        string
    AppName   string
    ClearData bool
}

type RenameAppIntent struct {
    ID          string
    AppName     string
    DisplayName string
}

type SetTailnetIntent struct {
    ID         string
    Name       string
    Type       string // "tailscale" or "headscale"
    AuthKey    string
    ControlURL string
}

type DeleteTailnetIntent struct {
    ID string
}

type AddRemoteAppIntent struct {
    ID          string
    AppID       string
    TailnetAddr string
    HostLabel   string
}

type DeleteRemoteAppIntent struct {
    ID          string
    RemoteAppID string
}

type CreateShareIntent struct {
    ID      string
    AppName string
}

type RevokeShareIntent struct {
    ID      string
    ShareID string
}

type ClearAppDataIntent struct {
    ID      string
    AppName string
}
```

## What Changes

### API Handlers -> Thin Intent Submitters

| Current Handler | Current Behavior | New Behavior |
|-----------------|-----------------|--------------|
| `handleInstall` | Parse choices, `EnqueueInstall()`, wait for result, return `InstallResult` | Enqueue `InstallAppIntent`, return 202 |
| `handleUninstall` | Parse clearData, `EnqueueUninstall()`, wait for result, return `UninstallResult` | Enqueue `UninstallAppIntent`, return 202 |
| `handleClearData` | If installed: enqueue uninstall. Else: direct FS + DB cleanup | Enqueue `ClearAppDataIntent`, return 202 |
| `handleRename` | Direct `appStore.UpdateDisplayName()` | Enqueue `RenameAppIntent`, return 202 |
| `handleSetTailnet` | Validate, store write, async sidecar + gateway goroutines | Validate, enqueue `SetTailnetIntent`, return 202 |
| `handleDeleteTailnet` | Store delete, sync stop/purge sidecars + gateway + proxies | Enqueue `DeleteTailnetIntent`, return 202 |
| `handleAddRemoteApp` | Validate, store write, `RegenerateRoutes()` | Validate, enqueue `AddRemoteAppIntent`, return 202 |
| `handleDeleteRemoteApp` | Store delete, `RegenerateRoutes()` | Enqueue `DeleteRemoteAppIntent`, return 202 |
| `handleCreateInvite` | Validate, store write, generate token | Enqueue `CreateShareIntent`, return 202 |
| `handleRevokeShare` | `shareStore.Revoke()` | Enqueue `RevokeShareIntent`, return 202 |
| `handlePlanInstall` | Read-only dependency planning | **Removed** |
| `handlePlanRemove` | Read-only removal impact analysis | **Removed** |

### Orchestrator Interface

The `AppOrchestrator` interface (`orchestrator/interface.go`) changes significantly.
`EnqueueInstall`/`EnqueueUninstall` are removed — the reconciler owns enqueueing. The
orchestrator becomes a lower-level runtime that the reconciler calls for container
lifecycle operations (ensure container, remove container, regenerate routes). The
`operationMu` mutex is removed — serialization is handled by the reconciler's single-
threaded processing loop.

### Server Wiring

The `Server` struct (`api/server.go`) gains a reference to the intent queue. API handlers
call `queue.Enqueue(intent)` instead of calling the orchestrator directly. The
`triggerReconcile()` method is replaced by the queue's notification mechanism (the
debounced wake-up).

Helper methods on Server that perform side effects are removed:
- `ensureSidecarsForRunningApps()` — moves into reconciler convergence
- `ensureGatewayAndProxies()` — moves into reconciler convergence
- `stopAllSidecarsAndPurge()` — moves into reconciler convergence

### Startup Sequence

On startup, the reconciler runs a convergence pass with no intents in the queue. This
replaces the current `SyncContainerState()` + `ReconcileState()` calls. The convergence
loop naturally handles: fixing DB status for crashed containers, restarting stopped apps,
regenerating routes, ensuring sidecars for the active tailnet connection.

## What Doesn't Change

- **Read endpoints.** All GET handlers continue reading from stores directly. No change
  to `handleListApps`, `handleListInstalledApps`, `handleAppMetadata`,
  `handleGetTailnet`, `handleListRemoteApps`, `handleListShares`, etc.
- **SSE stream.** `AppEventHub` continues broadcasting on store changes. The reconciler
  writes to stores, which triggers `onChange`, which triggers `Broadcast()`, which pushes
  to SSE subscribers. The frontend receives status updates exactly as it does today.
- **User preferences.** Layout reads/writes (`handleGetLayout`, `handleSetLayout`) remain
  direct. They have no side effects and no interaction with the reconciler.
- **Auth endpoints.** Login, logout, callback, setup — unchanged.
- **Configurator interface.** `PreStart`, `HealthCheck`, `PostStart` remain the same.
  They are called by the reconciler during convergence, same as today.
- **Catalog.** Read-only, unchanged.
- **Idempotency guarantees.** All configurator methods remain idempotent. The convergence
  loop can safely re-run all phases.

## Data Flow: Install Example (New)

```
User clicks "Install Radarr"
    |
    v
handleInstall (api/routes.go)
    | POST /api/apps/radarr/install
    | Validate app exists in catalog
    | Create InstallAppIntent{ID: "int_abc123", AppName: "radarr"}
    | queue.Enqueue(intent)
    | Return 202 Accepted {intentId: "int_abc123"}
    |
    v
Queue notifies reconciler (debounce ~200ms)
    |
    v
Reconciler wakes up
    |
    +-- Phase 1: Drain Queue
    |   | Pull InstallAppIntent{radarr} from queue
    |   | appStore.Install("radarr", ...) -> status="installing"
    |   | onChange -> Broadcast -> SSE: clients see "installing"
    |
    +-- Phase 2: Converge
        |
        +-- Resolve dependencies
        |   | Catalog says radarr needs download-client
        |   | qBittorrent is the only compatible provider, not installed
        |   | appStore.Install("qbittorrent", ...) -> status="installing"
        |
        +-- Compute execution levels
        |   | Level 0: qbittorrent (no deps)
        |   | Level 1: radarr (depends on qbittorrent)
        |
        +-- Ensure apps in level order
        |   |
        |   +-- Level 0: qbittorrent
        |   |   | PreStart -> write config files
        |   |   | containers.Ensure() -> create Quadlet + systemd unit
        |   |   | appStore.UpdateStatus("starting") -> SSE broadcast
        |   |   | HealthCheck -> wait for ready
        |   |   | PostStart -> configure via API
        |   |   | Start sidecar (if tailnet active)
        |   |   | appStore.UpdateStatus("running") -> SSE broadcast
        |   |
        |   +-- Level 1: radarr
        |       | PreStart -> write config (with qbittorrent binding)
        |       | containers.Ensure() -> create container
        |       | appStore.UpdateStatus("starting") -> SSE broadcast
        |       | HealthCheck -> wait for ready
        |       | PostStart -> configure (add qbittorrent as download client)
        |       | Start sidecar (if tailnet active)
        |       | appStore.UpdateStatus("running") -> SSE broadcast
        |
        +-- Routing convergence
        |   | Regenerate Traefik routes (includes both new apps)
        |
        +-- Optional dep dispatch
            | Check for newly-healthy apps with optional dep parents
```

## Data Flow: Multiple Rapid Installs (Batching)

```
User installs Radarr, Sonarr, and Bazarr in quick succession
    |
    v
Three intents enqueued within 200ms:
    InstallAppIntent{radarr}
    InstallAppIntent{sonarr}
    InstallAppIntent{bazarr}
    |
    v
Reconciler wakes after debounce
    |
    +-- Phase 1: Drain Queue
    |   | Pull all three intents
    |   | appStore.Install("radarr", ...)
    |   | appStore.Install("sonarr", ...)
    |   | appStore.Install("bazarr", ...)
    |
    +-- Phase 2: Converge
        |
        +-- Resolve dependencies
        |   | radarr needs download-client -> qbittorrent (not installed)
        |   | sonarr needs download-client -> qbittorrent (already in store from radarr resolution)
        |   | bazarr needs subtitle-manager -> none needed (standalone)
        |   | Deduplication is natural: qbittorrent only installed once
        |
        +-- Compute levels, ensure all four apps
        |
        +-- ONE RegenerateRoutes() call at the end (not three)
```

## Boundary: What the Reconciler Does NOT Own

- **Input validation.** API handlers still validate request bodies, check required fields,
  verify auth. Invalid requests are rejected with 400 before any intent is created.
- **Catalog reads.** Checking if an app exists in the catalog before creating an install
  intent is a read operation, fine for the handler to do.
- **Intent ID generation.** The API handler generates the intent ID (UUID) and returns it
  in the 202 response.

## Implementation Plan

All work happens on a dedicated branch (`reconciler-architecture`) off `main`. Each phase
leaves the system in a working state. No big-bang cutover.

### Phase 1: Intent Types + Queue

**What:** Define all typed intent structs. Build the FIFO queue with debounce wake-up.
Pure infrastructure — nothing calls it yet, no behavior changes.

**Auto-verify:**
- Unit tests: enqueue/drain ordering is FIFO
- Unit tests: debounce behavior (intent arrives, wait ~200ms, drain fires; second intent
  during window resets timer and both drain together)
- Unit tests: concurrent enqueue safety
- Unit tests: queue reports pending count
- Existing `./bloud validate --tier fast` still passes (nothing changed in live paths)

**Manual:** Nothing visible. This is plumbing.

---

### Phase 2: Reconciler Loop Skeleton

**What:** New reconciler struct with the two-phase cycle (drain + converge). The converge
phase is a no-op stub initially — it just logs "convergence pass complete." Wire the
queue's debounce wake-up to trigger the reconciler. The reconciler starts on server boot
and stops on shutdown.

**Auto-verify:**
- Unit tests: reconciler starts, accepts intents, drains them, calls converge stub
- Unit tests: reconciler shuts down cleanly (drains remaining intents, stops)
- Unit tests: multiple intents within debounce window are drained together
- Existing tests still pass

**Manual:** Start the host-agent, check logs for "reconciler started." Enqueue a test
intent programmatically, see logs for "drained N intents" and "convergence pass complete."

---

### Phase 3: Install/Uninstall Through Reconciler

Split into two sub-phases to avoid a risky cutover.

#### Phase 3a: Convergence Handles App Lifecycle

**What:** Implement the convergence logic for app lifecycle — dependency resolution,
`ensureApp` (PreStart, container creation, health check, PostStart, sidecar), uninstall
(stop sidecar, remove container, delete from store). The reconciler calls into the
existing orchestrator's lower-level methods, not reimplementing them.

The old path still works. Nothing calls the new path in production yet.

**Auto-verify:**
- Unit tests: convergence sees an app in "installing" status, calls ensureApp, transitions
  to "running"
- Unit tests: convergence sees an app in "uninstalling" status, removes container, deletes
  from store
- Unit tests: dependency resolution — install intent for Radarr creates qBittorrent record
  in store, converges both in level order
- Unit tests: batching — two installs that share a dependency produce one install of the
  shared dep
- Existing tests still pass

**Manual:** Not yet — the new path is tested in isolation, the old path still serves
traffic.

#### Phase 3b: Cut Over Install/Uninstall Handlers

**What:** Switch `handleInstall` and `handleUninstall` from calling
`orchestrator.EnqueueInstall()` to enqueueing intents. Return 202 instead of waiting for
the result. Remove `handlePlanInstall` and `handlePlanRemove` endpoints.

**Auto-verify:**
- `./bloud validate --tier fast` passes (unit tests updated for new handler behavior)
- `./bloud e2e lifecycle` passes — full install/uninstall lifecycle through the real
  system. This is the critical gate.

**Manual:**
- `./bloud dev` + open UI
- Install an app, observe: 202 response, SSE shows
  `installing -> starting -> running`
- Uninstall the app, observe: 202 response, SSE shows `uninstalling -> gone`
- Install an app with dependencies (e.g., Radarr), verify both the dependency and the app
  come up
- Install two apps rapidly, verify both install and only one `RegenerateRoutes` runs
  (check logs)

---

### Phase 4: Tailnet + Sidecars Through Reconciler

**What:** `handleSetTailnet` and `handleDeleteTailnet` become intent submitters.
Convergence handles: start sidecars for running apps when tailnet is active, stop/purge
sidecars when tailnet is deleted, ensure gateway, reconcile remote proxies.

Remove `ensureSidecarsForRunningApps()`, `ensureGatewayAndProxies()`,
`stopAllSidecarsAndPurge()` from Server.

**Auto-verify:**
- Unit tests: `SetTailnetIntent` drain writes to tailnet store
- Unit tests: convergence sees active tailnet + running apps without sidecars -> starts
  sidecars
- Unit tests: convergence sees no tailnet + existing sidecars -> stops and purges
- `./bloud validate --tier fast` passes

**Manual:**
- Configure a tailnet in the UI, verify sidecars start (`podman ps` in VM)
- Delete the tailnet, verify sidecars stop and purge
- Install an app while tailnet is active, verify sidecar starts automatically during
  convergence

---

### Phase 5: Remote Apps + Routing Through Reconciler

**What:** `handleAddRemoteApp` and `handleDeleteRemoteApp` become intent submitters.
Routing convergence (gateway, remote proxies, Traefik config) runs as part of the
convergence pass, not as ad-hoc calls scattered across handlers.

**Auto-verify:**
- Unit tests: `AddRemoteAppIntent` drain writes to remote app store
- Unit tests: convergence regenerates routes when remote app store has entries
- Unit tests: convergence ensures gateway when tailnet active + remote apps exist
- `./bloud validate --tier fast` passes

**Manual:**
- Add a remote app, verify Traefik routes update (check routes config file in VM)
- Delete a remote app, verify routes update
- Add two remote apps rapidly, verify one route regeneration (check logs)

---

### Phase 6: Remaining Intents + Cleanup

**What:** Move remaining operations into intents:
- `handleRename` -> `RenameAppIntent`
- `handleClearData` -> `ClearAppDataIntent`

Then cleanup:
- Remove `EnqueueInstall`/`EnqueueUninstall` from `AppOrchestrator` interface
- Remove `operationMu` from orchestrator
- Remove `Install`/`Uninstall` public methods (reconciler calls lower-level methods
  directly)
- Remove `triggerReconcile()` from Server
- Remove old reconciler (`reconcile.go`) — replaced by the new one
- Delete `ROUTING_RECONCILER_PLAN.md` (superseded)

**Auto-verify:**
- All unit tests updated and passing
- `./bloud validate --tier fast` passes
- `./bloud e2e lifecycle` passes

**Manual:**
- Full walkthrough: install app, rename it, configure tailnet, add remote app, uninstall,
  clear data
- Verify no goroutines doing store writes outside the reconciler (code review)

---

### Open Questions

1. **Phase 3 sub-phasing.** Phase 3 is split into 3a (build the new path, test in
   isolation) and 3b (cut over live handlers). This avoids a risky single-step cutover.

2. **Share intents.** `handleCreateInvite` currently generates a JWT token and returns it
   in the response. If it becomes an intent with a 202, the caller doesn't get the token
   back synchronously. Since this is a store write with no side effects (no containers, no
   routing, no reconciliation needed), it may be better to keep share creation as a direct
   operation outside the reconciler. Same for `handleRevokeShare`. These are excluded from
   the implementation phases pending a decision.
