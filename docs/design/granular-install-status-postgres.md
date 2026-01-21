# Design: Granular Install Status + PostgreSQL Migration

> **Status: IMPLEMENTED** - PostgreSQL migration complete. Granular status tracking working.

## Overview

This document describes two related changes to the host-agent:

1. **PostgreSQL Migration** - Replace SQLite with the shared PostgreSQL instance
2. **Granular Install Status** - Add detailed installation phase tracking

These changes improve user experience during app installation by showing meaningful progress states, while aligning the host-agent's data storage with the shared infrastructure architecture.

## Goals

- Show users what's actually happening during installation (not just a spinner)
- Use the shared PostgreSQL instance (consistent with our "one instance per host" architecture)
- Keep changes minimal and incremental

## Non-Goals

- Real-time progress percentages (nixos-rebuild is opaque)
- Parsing Nix build logs for detailed progress
- Multi-host database replication

---

## Current State

### Database
- SQLite file at `{dataDir}/state/bloud.db`
- 2 active tables: `apps`, `catalog_cache`
- Driver: `modernc.org/sqlite` (pure Go)

### Install Status
Simple string enum with coarse states:
```
installing → starting → running
                     → error
```

Users see a spinner for the entire `installing` phase, which can take 10 seconds to several minutes depending on whether containers need to be pulled.

---

## Proposed Changes

### 1. Granular Install Status

Add new status values that map to observable phase boundaries:

| Status | Description | When Set |
|--------|-------------|----------|
| `queued` | Installation requested, waiting to start | API receives install request |
| `configuring` | Generating Nix config and blueprints | Before writing config files |
| `building` | Running nixos-rebuild switch | Before exec nixos-rebuild |
| `starting` | Rebuild complete, waiting for health | After nixos-rebuild succeeds |
| `running` | Health check passed | After health check succeeds |
| `stopping` | Uninstall in progress | API receives uninstall request |
| `stopped` | App disabled but data retained | After uninstall completes |
| `error` | Health check failed or app unhealthy | After health check timeout/failure |
| `failed` | Installation/rebuild failed | After nixos-rebuild fails |

**State Diagram:**
```
                    ┌─────────────────────────────────────┐
                    │                                     │
                    ▼                                     │
install ──► queued ──► configuring ──► building ──► starting ──► running
                            │              │           │           │
                            │              │           │           │
                            ▼              ▼           ▼           │
                         failed         failed      error ◄───────┘
                                                       │
                                                       ▼
                                          uninstall ──► stopping ──► (deleted)
```

### 2. PostgreSQL Migration

Connect to the shared PostgreSQL instance instead of SQLite.

**Connection:**
- Use existing `bloud-postgres` container
- Database: `bloud_host_agent` (new database in shared instance)
- User: `bloud` (shared user, or dedicated `host_agent` user)
- Connection string via environment variable

**Schema Changes:**

```sql
-- PostgreSQL version of apps table
CREATE TABLE IF NOT EXISTS apps (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    version TEXT,
    status TEXT NOT NULL DEFAULT 'stopped',
    port INTEGER,
    is_system BOOLEAN NOT NULL DEFAULT FALSE,
    integration_config JSONB DEFAULT '{}',
    installed_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_apps_status ON apps(status);
CREATE INDEX IF NOT EXISTS idx_apps_name ON apps(name);

-- PostgreSQL version of catalog_cache table
CREATE TABLE IF NOT EXISTS catalog_cache (
    name TEXT PRIMARY KEY,
    yaml_content TEXT NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);
```

**Key Differences from SQLite:**
| SQLite | PostgreSQL |
|--------|------------|
| `INTEGER PRIMARY KEY AUTOINCREMENT` | `SERIAL PRIMARY KEY` |
| `INTEGER` for booleans | `BOOLEAN` |
| `TEXT` for JSON | `JSONB` (queryable) |
| `TIMESTAMP` | `TIMESTAMPTZ` |

**Query Compatibility:**
- `ON CONFLICT ... DO UPDATE` - supported in both
- `CURRENT_TIMESTAMP` - supported in both
- All other queries are standard SQL

---

## Implementation Plan

### Phase 1: PostgreSQL Migration (~4-6 hours)

1. **Add PostgreSQL driver**
   - Add `github.com/jackc/pgx/v5` to go.mod
   - Keep SQLite driver for potential fallback

2. **Update database initialization**
   ```go
   // db/db.go
   func InitDB(connString string) (*sql.DB, error) {
       if strings.HasPrefix(connString, "postgres://") {
           return initPostgres(connString)
       }
       return initSQLite(connString) // fallback for dev/testing
   }
   ```

3. **Create PostgreSQL schema**
   - New file: `db/schema_postgres.sql`
   - Update schema runner to detect database type

4. **Update NixOS module**
   ```nix
   # In host-agent module or bloud.nix
   bloud.apps.postgres.databases = [ "bloud_host_agent" ];

   # Pass connection string to host-agent
   environment.DATABASE_URL = "postgres://bloud:password@localhost:5432/bloud_host_agent";
   ```

5. **Update configuration**
   - Add `DATABASE_URL` environment variable support
   - Default to SQLite path for backward compatibility

### Phase 2: Granular Status (~2-3 hours)

1. **Update Go types**
   ```go
   // store/apps.go
   const (
       StatusQueued      = "queued"
       StatusConfiguring = "configuring"
       StatusBuilding    = "building"
       StatusStarting    = "starting"
       StatusRunning     = "running"
       StatusStopping    = "stopping"
       StatusStopped     = "stopped"
       StatusError       = "error"
       StatusFailed      = "failed"
   )
   ```

2. **Update orchestrator**
   ```go
   // orchestrator/orchestrator_nix.go - InstallApp()

   func (o *NixOrchestrator) InstallApp(name string, ...) error {
       // Phase 1: Queue
       o.appStore.UpdateStatus(name, StatusQueued)

       // Phase 2: Configure
       o.appStore.UpdateStatus(name, StatusConfiguring)
       if err := o.generateConfigs(name); err != nil {
           o.appStore.UpdateStatus(name, StatusFailed)
           return err
       }

       // Phase 3: Build
       o.appStore.UpdateStatus(name, StatusBuilding)
       if err := o.runNixosRebuild(); err != nil {
           o.appStore.UpdateStatus(name, StatusFailed)
           return err
       }

       // Phase 4: Starting (health check)
       o.appStore.UpdateStatus(name, StatusStarting)
       go o.waitForHealthy(name)

       return nil
   }
   ```

3. **Update TypeScript types**
   ```typescript
   // web/src/lib/types.ts
   export type AppStatus =
       | 'queued'
       | 'configuring'
       | 'building'
       | 'starting'
       | 'running'
       | 'stopping'
       | 'stopped'
       | 'error'
       | 'failed';

   export const AppStatus = {
       Queued: 'queued',
       Configuring: 'configuring',
       Building: 'building',
       Starting: 'starting',
       Running: 'running',
       Stopping: 'stopping',
       Stopped: 'stopped',
       Error: 'error',
       Failed: 'failed'
   } as const;
   ```

4. **Update frontend display**
   ```typescript
   // Helper for user-friendly status text
   export function getStatusDisplay(status: AppStatus): { text: string; icon: string } {
       switch (status) {
           case 'queued': return { text: 'Queued...', icon: 'clock' };
           case 'configuring': return { text: 'Configuring...', icon: 'settings' };
           case 'building': return { text: 'Building...', icon: 'hammer' };
           case 'starting': return { text: 'Starting...', icon: 'play' };
           case 'running': return { text: 'Running', icon: 'check' };
           case 'stopping': return { text: 'Stopping...', icon: 'square' };
           case 'stopped': return { text: 'Stopped', icon: 'pause' };
           case 'error': return { text: 'Error', icon: 'alert' };
           case 'failed': return { text: 'Failed', icon: 'x' };
       }
   }
   ```

---

## Migration Strategy

### Database Migration

Since this is a development project with no production data to preserve:

1. **Clean cut-over** - Drop SQLite, start fresh with PostgreSQL
2. **No data migration** - Reinstall apps after migration
3. **Keep SQLite code** - Optional fallback for isolated testing

For future production migrations, we'd add:
- Schema versioning table
- Migration scripts
- Backup/restore procedures

### Rollback Plan

If PostgreSQL causes issues:
1. Set `DATABASE_URL` to SQLite path
2. Rebuild NixOS config
3. Reinstall apps

---

## Testing

### Unit Tests
- Mock database interface for store tests
- Test all status transitions
- Test PostgreSQL-specific query syntax

### Integration Tests
- Install app, verify status progression: `queued → configuring → building → starting → running`
- Fail during build, verify: `queued → configuring → building → failed`
- Health check timeout, verify: `... → starting → error`

### Manual Testing
```bash
# Watch status changes in real-time
watch -n 0.5 'curl -s localhost:3000/api/apps/installed | jq ".[] | {name, status}"'

# Install an app and observe
curl -X POST localhost:3000/api/apps/miniflux/install
```

---

## Open Questions

1. **Shared vs dedicated PG user?**
   - Option A: Use shared `bloud` user (simpler)
   - Option B: Create `host_agent` user with limited permissions (more secure)
   - Recommendation: Start with shared user, add dedicated user later if needed

2. **Connection pooling?**
   - pgx has built-in pooling
   - For single-host, probably not needed
   - Recommendation: Use defaults, tune if we see connection issues

3. **SQLite for tests?**
   - Could keep SQLite for fast unit tests
   - Recommendation: Use PostgreSQL in tests too (via testcontainers or shared instance)

---

## Timeline

| Task | Estimate |
|------|----------|
| PostgreSQL driver + connection handling | 1-2 hours |
| Schema conversion + migration | 1-2 hours |
| NixOS module updates | 1 hour |
| Granular status types (Go + TS) | 1 hour |
| Orchestrator status updates | 1 hour |
| Frontend display updates | 1 hour |
| Testing + fixes | 1-2 hours |
| **Total** | **7-11 hours** |

Realistically: **1-2 days** of focused work.
