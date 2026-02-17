# Container Invalidation Design

> **Status: Design Proposal — Not Implemented.** This document describes a planned approach to container invalidation. None of the schemas, return values, or orchestration flows described here exist in the codebase yet. See the "In Progress / Planned" section of the README for current status.

## Core Concept

**StaticConfig determines whether restart is needed.**

```
changed := app.StaticConfig()
if changed {
    systemctl restart app
}
```

StaticConfig:
- Writes config files, env vars, certificates
- Returns `changed: bool` indicating if output differs from previous state
- Is idempotent - safe to run anytime

If StaticConfig returns `changed == true`, the container must restart to pick up the new config. If `changed == false`, no restart needed.

## StaticConfig vs DynamicConfig

| Phase | When | Returns | Restart? |
|-------|------|---------|----------|
| StaticConfig | Before container starts (or on-demand) | `changed: bool` | If changed |
| DynamicConfig | After container healthy | error only | Never triggers restart |

**StaticConfig** handles things the container reads on startup:
- Environment variables
- Config files mounted into container
- Traefik routing rules
- SSL certificates

**DynamicConfig** handles runtime integration:
- API calls to configure connections
- Database records
- Settings that can change while running

## Invalidation Flow

Two phases: **Mark** (immediate) and **Process** (after operation completes).

### Phase 1: Mark Invalid

When an event occurs, mark affected apps in `app_invalidations`:

```
┌─────────────────────────────────────────────────────────────────────┐
│  1. Event occurs (e.g., qbittorrent installed)                      │
│         │                                                           │
│         ▼                                                           │
│  2. Find potentially affected apps                                  │
│     → Apps that declare download-client integration                 │
│     → [radarr, sonarr]                                              │
│         │                                                           │
│         ▼                                                           │
│  3. Mark in database (deduplicates automatically):                  │
│     INSERT INTO app_invalidations (app_name, reason, created_at)    │
│     VALUES ('radarr', 'provider:qbittorrent', NOW())                │
│     ON CONFLICT DO NOTHING                                          │
└─────────────────────────────────────────────────────────────────────┘
```

### Phase 2: Process Invalidations

After install/uninstall completes, process all pending invalidations:

```
┌─────────────────────────────────────────────────────────────────────┐
│  1. Get pending invalidations:                                      │
│     SELECT app_name FROM app_invalidations                          │
│         │                                                           │
│         ▼                                                           │
│  2. For each app, run StaticConfig:                                 │
│     changed := radarr.StaticConfig()                                │
│         │                                                           │
│         ▼                                                           │
│  3. Remove from pending (whether changed or not):                   │
│     DELETE FROM app_invalidations WHERE app_name = 'radarr'         │
│         │                                                           │
│         ▼                                                           │
│  4. If changed == true:                                             │
│     systemctl restart podman-radarr                                 │
│         │                                                           │
│         ▼                                                           │
│  5. Systemd runs:                                                   │
│     ExecStartPre  → StaticConfig (returns false, already written)   │
│     Container starts                                                │
│     ExecStartPost → DynamicConfig                                   │
└─────────────────────────────────────────────────────────────────────┘
```

### Why Two Phases?

**1. Deduplication** - Multiple events marking same app = one row, one check

```
Install qbittorrent + prowlarr simultaneously
    │
    ├── Mark radarr (reason: provider:qbittorrent)
    ├── Mark radarr (reason: provider:prowlarr)  ← ON CONFLICT DO NOTHING
    │
    ▼
Process: radarr.StaticConfig() runs once
```

**2. Crash Safety** - Pending invalidations persist in database

**3. Batching** - All restarts happen after operation completes

### StaticConfig Idempotency

StaticConfig runs twice:
1. First run (process phase): Detects changes, writes new config, returns `changed=true`
2. Second run (ExecStartPre): Config already matches desired state, returns `changed=false`

This is the idempotent pattern - StaticConfig always writes desired state and reports whether it changed.

## What Triggers StaticConfig Check?

| Event | Run StaticConfig for |
|-------|---------------------|
| Provider installed | Apps that declare that integration |
| Provider uninstalled | Apps that had integration from that provider |
| Provider config changed | Apps with integration from that provider |
| App installed | The app itself (initial setup) |

In all cases, StaticConfig decides whether restart is needed.

## Data Model

```sql
-- Tracks DynamicConfig state (runtime integrations)
CREATE TABLE app_integrations (
    app_name TEXT NOT NULL,
    integration_name TEXT NOT NULL,
    source_app TEXT NOT NULL,
    configured_at TIMESTAMP,  -- When DynamicConfig last succeeded
    PRIMARY KEY (app_name, integration_name)
);

-- Tracks apps that need StaticConfig check
CREATE TABLE app_invalidations (
    app_name TEXT PRIMARY KEY,
    reason TEXT NOT NULL,       -- Why invalidated (for debugging)
    created_at TIMESTAMP NOT NULL
);
```

**app_invalidations:** Apps marked for StaticConfig check. Persists across crashes. Processed in batch after install/uninstall completes.

**StaticConfig state:** No database table needed. Compare against the actual config file - it's the source of truth.

## Complete Example: Install qbittorrent

**Initial state:**
- radarr running (no download-client)
- sonarr running (no download-client)
- qbittorrent not installed

**User action:** Click "Install qbittorrent" in UI

---

### Step 1: Install qbittorrent

```
Orchestrator.Install("qbittorrent")
    │
    ├── Write apps.nix: bloud.apps.qbittorrent.enable = true
    ├── nixos-rebuild switch
    │     → Creates podman-qbittorrent.service
    │     → Systemd starts the service
    │
    ▼

QBITTORRENT SERVICE LIFECYCLE
─────────────────────────────────────────────────────────────────────────────
10:29:50 │ ExecStartPre: bloud-agent configure static qbittorrent
         │   → qbittorrent.StaticConfig(state)
         │   → Writes /config/qbittorrent.env (admin password, etc.)
         │   → Returns changed=true (first run)
         │
10:29:51 │ ExecStart: podman start qbittorrent
         │   → Container starts
         │
10:29:55 │ ExecStartPost: bloud-agent configure dynamic qbittorrent
         │   → qbittorrent.HealthCheck() - polls until healthy
         │   → qbittorrent.DynamicConfig(state)
         │   → Configures initial settings via API
         │
10:29:55 │ SERVICE READY: podman-qbittorrent.service active (running)
─────────────────────────────────────────────────────────────────────────────

qbittorrent is now running and healthy
```

---

### Step 2: Mark consumers invalid

```
orchestrator.markConsumersInvalid("qbittorrent")
    │
    ├── catalog.GetProvides("qbittorrent") → ["download-client"]
    ├── catalog.GetConsumers("download-client") → ["radarr", "sonarr"]
    │
    ▼
INSERT INTO app_invalidations VALUES ('radarr', 'provider:qbittorrent', NOW())
INSERT INTO app_invalidations VALUES ('sonarr', 'provider:qbittorrent', NOW())

Database now contains:
┌──────────┬──────────────────────┬─────────────────────┐
│ app_name │ reason               │ created_at          │
├──────────┼──────────────────────┼─────────────────────┤
│ radarr   │ provider:qbittorrent │ 2024-01-15 10:30:00 │
│ sonarr   │ provider:qbittorrent │ 2024-01-15 10:30:00 │
└──────────┴──────────────────────┴─────────────────────┘
```

---

### Step 3: Process invalidations

```
orchestrator.processInvalidations()
    │
    ├── SELECT app_name FROM app_invalidations → ["radarr", "sonarr"]
    │
    ▼
For radarr:
    │
    ├── radarr.StaticConfig(state):
    │       state.Integrations["download-client"] = {Source: "qbittorrent", ...}
    │       │
    │       ├── Desired config (keys we manage):
    │       │     DOWNLOAD_CLIENT_HOST=qbittorrent
    │       │     DOWNLOAD_CLIENT_PORT=8080
    │       │
    │       ├── Read current /config/radarr.env, extract our managed keys
    │       │     (file doesn't have these keys yet)
    │       │
    │       ├── desired ≠ current → write keys to file
    │       │
    │       └── Return changed=true
    │
    ├── DELETE FROM app_invalidations WHERE app_name = 'radarr'
    │
    └── Add "radarr" to toRestart list

For sonarr:
    │
    ├── sonarr.StaticConfig(state):
    │       (same pattern)
    │       └── Return changed=true
    │
    ├── DELETE FROM app_invalidations WHERE app_name = 'sonarr'
    │
    └── Add "sonarr" to toRestart list

toRestart = ["radarr", "sonarr"]
```

---

### Step 4: Orchestrator restarts services in dependency order

```
TIME     EVENT
─────────────────────────────────────────────────────────────────────────────
10:30:00 │ Step 3 completed - StaticConfig returned changed=true for:
         │   radarr, sonarr
         │
         ▼
10:30:00 │ ORCHESTRATOR BUILDS RESTART ORDER:
         │   graph.AddApp("radarr", deps=[])  // no deps on other apps being restarted
         │   graph.AddApp("sonarr", deps=[])
         │   graph.Walk() → [radarr, sonarr] (no deps between them, order doesn't matter)
         │
         ▼
10:30:00 │ ORCHESTRATOR RESTARTS IN ORDER:
         │   systemctl --user restart podman-radarr.service
         │   systemctl --user restart podman-sonarr.service
         │
         ▼
─────────────────────────────────────────────────────────────────────────────
         │ RADARR SERVICE LIFECYCLE
─────────────────────────────────────────────────────────────────────────────
10:30:01 │ ExecStop: podman stop radarr
         │   → Container stops
         │
10:30:02 │ ExecStart: podman start radarr
         │   → Container starts with env vars from managed.env
         │   → DOWNLOAD_CLIENT_HOST=qbittorrent
         │   → DOWNLOAD_CLIENT_PORT=8080
         │
10:30:05 │ ExecStartPost: bloud-agent configure dynamic radarr
         │   → radarr.HealthCheck() - polls /health until 200
         │   → radarr.DynamicConfig(state)
         │   → POST /api/v3/downloadclient
         │       {name: "qbittorrent", host: "localhost", port: 8080}
         │   → UPDATE app_integrations SET configured_at = NOW()
         │       WHERE app_name = 'radarr' AND integration_name = 'download-client'
         │
10:30:05 │ SERVICE READY: podman-radarr.service active (running)
         │
─────────────────────────────────────────────────────────────────────────────
         │ SONARR SERVICE LIFECYCLE (parallel with radarr)
─────────────────────────────────────────────────────────────────────────────
10:30:01 │ ExecStop: podman stop sonarr
         │
10:30:02 │ ExecStart: podman start sonarr
         │
10:30:07 │ ExecStartPost: bloud-agent configure dynamic sonarr
         │   → POST /api/v3/downloadclient
         │   → UPDATE app_integrations SET configured_at = NOW()
         │
10:30:07 │ SERVICE READY: podman-sonarr.service active (running)
         │
─────────────────────────────────────────────────────────────────────────────
10:30:07 │ ALL RESTARTS COMPLETE
```

**Key points:**
- Orchestrator determines restart order using dependency graph traversal
- Services with dependencies are restarted in correct order (dependents after dependencies)
- StaticConfig already ran and wrote config in Step 3; restart picks up new config
- DynamicConfig runs in ExecStartPost after container is healthy

---

### Final state

```
┌─────────────────────────────────────────────────────────────────────┐
│  qbittorrent: running                                               │
│  radarr: running, download-client configured to qbittorrent         │
│  sonarr: running, download-client configured to qbittorrent         │
│                                                                     │
│  app_invalidations: empty (all processed)                           │
│                                                                     │
│  app_integrations:                                                  │
│  ┌──────────┬─────────────────┬─────────────┬─────────────────────┐ │
│  │ app_name │ integration     │ source_app  │ configured_at       │ │
│  ├──────────┼─────────────────┼─────────────┼─────────────────────┤ │
│  │ radarr   │ download-client │ qbittorrent │ 2024-01-15 10:30:05 │ │
│  │ sonarr   │ download-client │ qbittorrent │ 2024-01-15 10:30:07 │ │
│  └──────────┴─────────────────┴─────────────┴─────────────────────┘ │
└─────────────────────────────────────────────────────────────────────┘
```

## Example: No restart needed (config unchanged)

**Scenario:** qbittorrent port changes, radarr already configured

```
1. qbittorrent restarts with new port

2. Mark radarr invalid → INSERT INTO app_invalidations

3. Process invalidations:
   radarr.StaticConfig():
     - Desired: DOWNLOAD_CLIENT_HOST=qbittorrent (same)
     - Current: DOWNLOAD_CLIENT_HOST=qbittorrent (same)
     - Return changed=false

4. DELETE FROM app_invalidations (radarr)

5. toRestart = [] → no restarts

6. qbittorrent's DynamicConfig updates radarr via API
   (radarr didn't need restart, DynamicConfig handles port change)
```

## Configurator Interface

Update the configurator interface to return change status:

```go
type Configurator interface {
    Name() string

    // StaticConfig writes config and returns whether it changed
    StaticConfig(ctx context.Context, state *AppState) (changed bool, err error)

    // HealthCheck waits for app to be ready
    HealthCheck(ctx context.Context) error

    // DynamicConfig configures runtime integrations
    DynamicConfig(ctx context.Context, state *AppState) error
}
```

## Implementation Notes

1. **StaticConfig must be pure** - Given same inputs (integrations available, config values), always produces same output. This makes the `changed` detection reliable.

2. **StaticConfig can run anytime** - Not just in systemd hooks. The orchestrator can run it to check if restart is needed.

3. **DynamicConfig doesn't trigger restarts** - It runs after container is healthy and handles runtime config only.

4. **Deduplication** - If multiple events affect the same app, run StaticConfig once. If it returns changed, restart once.

## Dependency Graph Traversal

The orchestrator builds the dependency graph from our own data (metadata + database), not systemd.

### Why use our data instead of systemd D-Bus?

The systemd `After=/Requires=` dependencies are *generated from* our app metadata anyway (in NixOS modules). Querying D-Bus would just be asking systemd for data we already have.

**Benefits of using our data:**
- No D-Bus complexity (connection management, IPC)
- Easier to test (mock database vs mock D-Bus)
- Faster (no IPC overhead)
- Single source of truth (metadata.yaml defines integrations)

### Data sources

| Source | What it provides |
|--------|------------------|
| `metadata.yaml` | App declares `provides` and `consumes` integrations |
| `app_integrations` table | `source_app` column shows which app provides each integration |

### Building the dependency graph

Build graph from integration relationships:

```go
// AppGraph builds dependency order from our integration data
type AppGraph struct {
    nodes map[string][]string  // app -> apps it depends on
}

func (g *AppGraph) AddApp(app string, dependsOn []string) {
    g.nodes[app] = dependsOn
}

// Walk visits apps in dependency order (dependencies first)
func (g *AppGraph) Walk(visitor func(app string) error) error {
    order := g.topoSort()  // Kahn's algorithm
    for _, app := range order {
        if err := visitor(app); err != nil {
            return err
        }
    }
    return nil
}

// WalkReverse visits apps in reverse order (dependents first, for shutdown)
func (g *AppGraph) WalkReverse(visitor func(app string) error) error {
    order := g.topoSort()
    slices.Reverse(order)
    for _, app := range order {
        if err := visitor(app); err != nil {
            return err
        }
    }
    return nil
}
```

### When to use each method

| Method | Use case |
|--------|----------|
| `Walk()` | Restart in dependency order (dependencies first) |
| `WalkReverse()` | Shutdown in dependency order (dependents first) |

## Algorithm

```go
// Mark phase - called when events occur
func (o *Orchestrator) markInvalid(apps []string, reason string) {
    for _, app := range apps {
        o.db.Exec(`
            INSERT INTO app_invalidations (app_name, reason, created_at)
            VALUES (?, ?, NOW())
            ON CONFLICT DO NOTHING
        `, app, reason)
    }
}

// Process phase - called after operation completes
func (o *Orchestrator) processInvalidations() error {
    pending := o.db.Query(`SELECT app_name FROM app_invalidations`)

    // Run StaticConfig for each, collect those that changed
    var toRestart []string
    for _, app := range pending {
        changed, err := o.runStaticConfig(app)
        if err != nil {
            o.logger.Warn("StaticConfig failed", "app", app, "err", err)
        }
        o.db.Exec(`DELETE FROM app_invalidations WHERE app_name = ?`, app)
        if changed {
            toRestart = append(toRestart, app)
        }
    }

    // Restart in dependency order
    if len(toRestart) > 0 {
        return o.restartInOrder(toRestart)
    }
    return nil
}

// restartInOrder restarts services respecting their dependencies
func (o *Orchestrator) restartInOrder(apps []string) error {
    graph := NewAppGraph()

    // Build graph from our integration data
    for _, app := range apps {
        // Get apps this one depends on (via integrations)
        deps := o.store.GetIntegrationSources(app)  // Returns source_app values
        graph.AddApp(app, deps)
    }

    // Walk in dependency order (dependencies restarted before dependents)
    return graph.Walk(func(app string) error {
        return exec.Command("systemctl", "--user", "restart", "podman-"+app+".service").Run()
    })
}
```

**Key points:**
- StaticConfig returns `changed bool` - only restart if config actually changed
- Orchestrator collects apps needing restart, then restarts in dependency order
- Graph built from `app_integrations` table (no D-Bus needed)
- Dependencies restarted before dependents

## Implementation Phases

### Phase 1: Update Configurator Interface

Update interface to return change status:

```go
// Before
type Configurator interface {
    PreStart(ctx context.Context, state *AppState) error
    HealthCheck(ctx context.Context) error
    PostStart(ctx context.Context, state *AppState) error
}

// After
type Configurator interface {
    StaticConfig(ctx context.Context, state *AppState) (changed bool, err error)
    HealthCheck(ctx context.Context) error
    DynamicConfig(ctx context.Context, state *AppState) error
}
```

**Files:**
- `services/host-agent/pkg/configurator/interface.go`

**Tests:**
- Interface compiles
- Existing configurators fail to compile (expected)

### Phase 2: Update All Configurators

Update each configurator to:
1. Rename `PreStart` → `StaticConfig`, `PostStart` → `DynamicConfig`
2. Return `changed bool` from StaticConfig
3. Compare desired state vs actual state to determine changed

**Pattern for detecting changes:**

Compare desired config against the actual file - no database needed. Each configurator knows which keys it manages.

```go
func (c *MyConfigurator) StaticConfig(ctx context.Context, state *AppState) (bool, error) {
    // Keys we manage
    managedKeys := []string{"OAUTH2_CLIENT_ID", "OAUTH2_CLIENT_SECRET", "DATABASE_URL"}

    // Desired values for our managed keys
    desired := map[string]string{
        "OAUTH2_CLIENT_ID":     state.OAuth.ClientID,
        "OAUTH2_CLIENT_SECRET": state.OAuth.ClientSecret,
        "DATABASE_URL":         state.Database.URL,
    }

    // Read current file and extract only our managed keys
    current := c.readManagedKeys(configPath, managedKeys)

    // Compare our keys only
    if maps.Equal(desired, current) {
        return false, nil  // Our config unchanged
    }

    // Write our managed keys (preserve user's other keys)
    if err := c.writeManagedKeys(configPath, desired); err != nil {
        return false, err
    }

    return true, nil  // Our config changed
}
```

**Key insight:**
- Compare against actual file, not database (prevents drift)
- Each configurator declares which keys it manages
- User edits to other keys are preserved and don't trigger restarts

**Files:**
- `apps/*/configurator.go` (all app configurators)
- `services/host-agent/internal/appconfig/register.go`

**Tests:**
- Each configurator returns `changed=true` on first run
- Each configurator returns `changed=false` on second run (idempotent)

### Phase 3: Update Orchestrator to Use Change Detection

Update orchestrator to:
1. Run StaticConfig and check return value
2. Only restart if `changed == true`

```go
func (o *Orchestrator) runStaticConfig(app string) (bool, error) {
    cfg := o.configurators[app]
    state := o.buildAppState(app)
    return cfg.StaticConfig(ctx, state)
}

func (o *Orchestrator) ensureConfigured(app string) error {
    changed, err := o.runStaticConfig(app)
    if err != nil {
        return err
    }
    if changed {
        return o.restartService(app)
    }
    return nil
}
```

**Files:**
- `services/host-agent/internal/orchestrator/orchestrator_nix.go`

**Tests:**
- App with unchanged config → no restart
- App with changed config → restart triggered

### Phase 4: Add Invalidation Marking + Processing

**4a. Add InvalidationStore:**

```go
type InvalidationStore interface {
    Mark(appName, reason string) error
    GetPending() ([]string, error)
    Remove(appName string) error
}
```

**4b. Mark on provider events:**

```go
func (o *Orchestrator) markConsumersInvalid(provider string) error {
    // Get what this provider provides
    integrations := o.catalog.GetProvides(provider)

    // Find installed apps that consume these integrations
    consumers := o.catalog.GetConsumers(integrations)

    // Mark each (deduplicates automatically)
    for _, consumer := range consumers {
        o.invalidations.Mark(consumer, "provider:"+provider)
    }
    return nil
}
```

**4c. Process after operation completes (with restart):**

```go
func (o *Orchestrator) processInvalidations() error {
    pending, _ := o.invalidations.GetPending()

    // Run StaticConfig for each, collect those that changed
    var toRestart []string
    for _, app := range pending {
        changed, err := o.runStaticConfig(app)
        if err != nil {
            o.logger.Warn("StaticConfig failed", "app", app, "err", err)
        }
        o.invalidations.Remove(app)
        if changed {
            toRestart = append(toRestart, app)
        }
    }

    // Restart in dependency order
    if len(toRestart) > 0 {
        return o.restartInOrder(toRestart)
    }
    return nil
}
```

**4d. Restart in dependency order:**

```go
func (o *Orchestrator) restartInOrder(apps []string) error {
    graph := NewAppGraph()

    for _, app := range apps {
        deps := o.store.GetIntegrationSources(app)
        graph.AddApp(app, deps)
    }

    return graph.Walk(func(app string) error {
        return exec.Command("systemctl", "--user", "restart", "podman-"+app+".service").Run()
    })
}
```

**Files:**
- `services/host-agent/internal/store/invalidations.go` (new)
- `services/host-agent/internal/orchestrator/orchestrator_nix.go`
- `services/host-agent/internal/orchestrator/app_graph.go` (new)
- `services/host-agent/internal/catalog/catalog.go` (add GetProvides, GetConsumers)

**Tests:**
- Mark same app twice → only one row in database
- Process invalidations → StaticConfig runs for each pending
- Apps with changed config → restarted in dependency order
- Apps with unchanged config → not restarted

### Phase 5: Wire Up Events

Call invalidation from install/uninstall flows:

```go
func (o *Orchestrator) Install(app string) error {
    // ... existing install logic ...

    // After provider is healthy, invalidate consumers
    if o.catalog.HasProvides(app) {
        o.markConsumersInvalid(app)
        o.processInvalidations()  // Runs StaticConfig, restarts if changed
    }
    return nil
}

func (o *Orchestrator) Uninstall(app string) error {
    // Before uninstall, invalidate consumers
    if o.catalog.HasProvides(app) {
        o.markConsumersInvalid(app)
        o.processInvalidations()
    }

    // ... existing uninstall logic ...
}
```

**Files:**
- `services/host-agent/internal/orchestrator/orchestrator_nix.go`

**Tests:**
- Full integration: install provider → consumer StaticConfig returns changed → consumer restarted
- Full integration: uninstall provider → consumer reconfigured without that integration

## Summary

| Phase | What | Validates |
|-------|------|-----------|
| 1 | Update interface | New contract defined |
| 2 | Update configurators | Change detection works |
| 3 | Orchestrator uses changes | StaticConfig writes on change |
| 4 | Invalidation store + mark/process + restart | Deduplication, crash safety, ordered restart |
| 5 | Wire up install/uninstall | End-to-end flow works |

Each phase builds on the previous and has clear tests to validate before moving on.

**Key architectural decision:** Go orchestrator handles restarts directly, using our own integration data (from `app_integrations` table) to determine dependency order. No D-Bus needed - we already have the data. StaticConfig returns `changed bool` so we only restart when config actually changed.
