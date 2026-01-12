# Frontend Store & Type Safety Improvements

This document outlines improvements to the Svelte store architecture and establishes a shared type system between the Go backend and TypeScript frontend.

## Current Architecture

```
SQLite Database
    ↓ manual scanning
Go structs (InstalledApp)
    ↓ json.Marshal
JSON API responses
    ↓ fetch + JSON.parse
TypeScript types (App)
    ↓ Svelte stores
Frontend UI
```

**Problem:** Three separate type definitions (SQL schema, Go structs, TypeScript interfaces) must stay in sync manually.

---

## Part 1: Svelte Store Improvements

### 1.1 Runtime Validation for SSE Data

Add Zod validation to catch malformed server responses before they corrupt store state.

```typescript
// lib/stores/validation.ts
import { z } from 'zod';

export const AppStatusSchema = z.enum([
  'running', 'starting', 'installing', 'uninstalling', 'stopped', 'error'
]);

export const AppSchema = z.object({
  name: z.string(),
  display_name: z.string(),
  version: z.string().optional(),
  status: AppStatusSchema,
  port: z.number().optional(),
  is_system: z.boolean(),
  installed_at: z.string().optional(),
});

export const AppListSchema = z.array(AppSchema);

export type App = z.infer<typeof AppSchema>;
export type AppStatus = z.infer<typeof AppStatusSchema>;
```

Update SSE handler:

```typescript
// lib/stores/appActions.ts
import { AppListSchema } from './validation';

eventSource.onmessage = (e) => {
  const result = AppListSchema.safeParse(JSON.parse(e.data));
  if (!result.success) {
    console.error('Invalid SSE data:', result.error);
    error.set('Received invalid data from server');
    return;
  }
  apps.set(result.data);
  loading.set(false);
  error.set(null);
};
```

### 1.2 Connection Status Store

Expose SSE connection state so the UI can show disconnection warnings.

```typescript
// lib/stores/connection.ts
import { writable, derived } from 'svelte/store';

export type ConnectionStatus = 'connected' | 'connecting' | 'disconnected';

export const connectionStatus = writable<ConnectionStatus>('connecting');
export const lastConnected = writable<Date | null>(null);
export const reconnectAttempts = writable(0);

export const isOnline = derived(connectionStatus, ($status) => $status === 'connected');
```

Update SSE handlers:

```typescript
// lib/stores/appActions.ts
import { connectionStatus, lastConnected, reconnectAttempts } from './connection';

eventSource.onopen = () => {
  connectionStatus.set('connected');
  lastConnected.set(new Date());
  reconnectAttempts.set(0);
};

eventSource.onerror = () => {
  connectionStatus.set('disconnected');
  reconnectAttempts.update(n => n + 1);
  // ... existing reconnect logic
};
```

### 1.3 Per-Operation Loading States

Track which specific apps have operations in progress.

```typescript
// lib/stores/apps.ts
export const appOperations = writable<Map<string, 'installing' | 'uninstalling' | 'starting'>>(
  new Map()
);

// Helper to check if an app has an operation in progress
export const isOperating = derived(appOperations, ($ops) => {
  return (name: string) => $ops.has(name);
});

// Helper to get operation type
export const getOperation = derived(appOperations, ($ops) => {
  return (name: string) => $ops.get(name) ?? null;
});
```

Usage in actions:

```typescript
export async function installApp(name: string, choices: Record<string, string> = {}): Promise<void> {
  appOperations.update(ops => new Map(ops).set(name, 'installing'));

  try {
    const res = await fetch(`/api/apps/${name}/install`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ choices })
    });

    if (!res.ok) {
      const data = await res.json();
      throw new Error(data.error || 'Install failed');
    }
  } finally {
    appOperations.update(ops => {
      const next = new Map(ops);
      next.delete(name);
      return next;
    });
  }
}
```

### 1.4 Catalog Store with Caching

Move catalog data into a store with staleness tracking.

```typescript
// lib/stores/catalog.ts
import { writable, get } from 'svelte/store';
import type { CatalogApp } from './validation';

export const catalogApps = writable<CatalogApp[]>([]);
export const catalogLoading = writable(false);
export const catalogError = writable<string | null>(null);
export const catalogLastFetched = writable<Date | null>(null);

const CACHE_MAX_AGE_MS = 60_000; // 1 minute

export async function loadCatalog(force = false): Promise<void> {
  const lastFetch = get(catalogLastFetched);

  // Return cached data if still fresh
  if (!force && lastFetch && Date.now() - lastFetch.getTime() < CACHE_MAX_AGE_MS) {
    return;
  }

  catalogLoading.set(true);
  catalogError.set(null);

  try {
    const res = await fetch('/api/apps');
    if (!res.ok) throw new Error('Failed to load catalog');

    const data = await res.json();
    const apps = (data.apps ?? []).filter((a: CatalogApp) => !a.isSystem);

    catalogApps.set(apps);
    catalogLastFetched.set(new Date());
  } catch (err) {
    catalogError.set(err instanceof Error ? err.message : 'Failed to load catalog');
  } finally {
    catalogLoading.set(false);
  }
}
```

### 1.5 Store Facade Pattern

Create a single entry point that controls what consumers can access.

```typescript
// lib/stores/index.ts

// Read-only store exports (consumers can subscribe but not mutate)
export {
  apps,
  userApps,
  visibleApps,
  installedNames,
  loading,
  error,
  appOperations,
  isOperating,
  getOperation,
} from './apps';

export {
  connectionStatus,
  isOnline,
  lastConnected,
} from './connection';

export {
  catalogApps,
  catalogLoading,
  catalogError,
} from './catalog';

// Action exports (the only way to mutate state)
export {
  initApps,
  disconnectApps,
  installApp,
  uninstallApp,
  refreshApps,
} from './appActions';

export {
  loadCatalog,
} from './catalog';

// Types
export type { App, AppStatus, CatalogApp } from './validation';
```

### 1.6 Force Refresh Capability

Add manual refresh for when SSE might be stale.

```typescript
// lib/stores/appActions.ts
export async function refreshApps(): Promise<void> {
  loading.set(true);
  error.set(null);

  try {
    const res = await fetch('/api/apps/installed');
    if (!res.ok) throw new Error('Failed to fetch apps');

    const data = await res.json();
    const result = AppListSchema.safeParse(data.apps ?? []);

    if (!result.success) {
      throw new Error('Invalid response format');
    }

    apps.set(result.data);
  } catch (err) {
    error.set(err instanceof Error ? err.message : 'Refresh failed');
  } finally {
    loading.set(false);
  }
}
```

### 1.7 Optimistic Updates for Install

Add immediate UI feedback for installations.

```typescript
export async function installApp(name: string, choices: Record<string, string> = {}): Promise<void> {
  // Optimistic update: add placeholder immediately
  apps.update((current) => {
    if (current.some(a => a.name === name)) return current;
    return [...current, {
      name,
      display_name: name,
      version: '',
      status: 'installing' as const,
      is_system: false,
    }];
  });

  appOperations.update(ops => new Map(ops).set(name, 'installing'));

  try {
    const res = await fetch(`/api/apps/${name}/install`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ choices })
    });

    if (!res.ok) {
      // Rollback optimistic update
      apps.update((current) => current.filter(a => a.name !== name));
      const data = await res.json();
      throw new Error(data.error || 'Install failed');
    }
    // SSE will push the real app state
  } catch (err) {
    apps.update((current) => current.filter(a => a.name !== name));
    throw err;
  } finally {
    appOperations.update(ops => {
      const next = new Map(ops);
      next.delete(name);
      return next;
    });
  }
}
```

### 1.8 Store Reset for Testing/Logout

```typescript
// lib/stores/appActions.ts
export function resetStores(): void {
  disconnectApps();
  apps.set([]);
  loading.set(true);
  error.set(null);
  appOperations.set(new Map());
}
```

### 1.9 ESLint Rule for Store Imports

Prevent direct imports from internal store files.

```json
// .eslintrc.json (add to rules)
{
  "no-restricted-imports": ["error", {
    "patterns": [{
      "group": ["**/stores/apps", "**/stores/appActions", "**/stores/connection"],
      "message": "Import from '$lib/stores' instead of internal store files"
    }]
  }]
}
```

---

## Part 2: SQLite Schema Improvements

### 2.1 Add CHECK Constraints

Validate data at the database level.

```sql
-- db/schema.sql
CREATE TABLE IF NOT EXISTS apps (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    version TEXT,
    status TEXT NOT NULL DEFAULT 'stopped'
        CHECK (status IN ('running', 'starting', 'installing', 'uninstalling', 'stopped', 'error')),
    port INTEGER CHECK (port IS NULL OR (port >= 1 AND port <= 65535)),
    is_system INTEGER NOT NULL DEFAULT 0 CHECK (is_system IN (0, 1)),
    integration_config TEXT,
    installed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_apps_status ON apps(status);
CREATE INDEX IF NOT EXISTS idx_apps_is_system ON apps(is_system);
```

### 2.2 Add Migrations

Replace single schema file with versioned migrations.

```
services/host-agent/internal/db/migrations/
├── 001_initial.up.sql
├── 001_initial.down.sql
├── 002_add_check_constraints.up.sql
└── 002_add_check_constraints.down.sql
```

Example migration:

```sql
-- 001_initial.up.sql
CREATE TABLE apps (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    version TEXT,
    status TEXT NOT NULL DEFAULT 'stopped',
    port INTEGER,
    is_system INTEGER NOT NULL DEFAULT 0,
    integration_config TEXT,
    installed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- 001_initial.down.sql
DROP TABLE apps;
```

Use `golang-migrate` for migration execution:

```go
import "github.com/golang-migrate/migrate/v4"

//go:embed migrations/*.sql
var migrationsFS embed.FS

func (d *DB) Migrate() error {
    source, _ := iofs.New(migrationsFS, "migrations")
    driver, _ := sqlite3.WithInstance(d.db, &sqlite3.Config{})
    m, _ := migrate.NewWithInstance("iofs", source, "sqlite3", driver)
    return m.Up()
}
```

---

## Part 3: Shared Type System

### 3.1 Single Source of Truth Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    schema.sql (source of truth)             │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                         sqlc                                │
│  - Generates Go structs from schema + queries               │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│              Go structs (internal/db/models.go)             │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                         tygo                                │
│  - Generates TypeScript from Go structs                     │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│          TypeScript types (web/src/lib/generated/)          │
└─────────────────────────────────────────────────────────────┘
```

### 3.2 sqlc Configuration

Generate type-safe Go from SQL.

```yaml
# services/host-agent/sqlc.yaml
version: "2"
sql:
  - engine: "sqlite"
    queries: "internal/db/queries.sql"
    schema: "internal/db/schema.sql"
    gen:
      go:
        package: "db"
        out: "internal/db"
        emit_json_tags: true
        json_tags_case_style: "snake"
        emit_empty_slices: true
```

Example queries file:

```sql
-- internal/db/queries.sql

-- name: GetAllApps :many
SELECT * FROM apps ORDER BY name;

-- name: GetAppByName :one
SELECT * FROM apps WHERE name = ? LIMIT 1;

-- name: GetUserApps :many
SELECT * FROM apps WHERE is_system = 0 ORDER BY name;

-- name: InsertApp :one
INSERT INTO apps (name, display_name, version, status, port, is_system, integration_config)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateAppStatus :exec
UPDATE apps SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE name = ?;

-- name: UpdateIntegrationConfig :exec
UPDATE apps SET integration_config = ?, updated_at = CURRENT_TIMESTAMP WHERE name = ?;

-- name: DeleteApp :exec
DELETE FROM apps WHERE name = ?;
```

### 3.3 tygo Configuration

Generate TypeScript from Go structs.

```yaml
# tygo.yaml
packages:
  - path: "github.com/bloud/bloud-v3/services/host-agent/internal/db"
    output_path: "services/host-agent/web/src/lib/generated/types.ts"
    type_mappings:
      time.Time: string
      sql.NullString: "string | null"
      sql.NullInt64: "number | null"
```

### 3.4 Generation Script

```makefile
# Makefile
.PHONY: generate generate-go generate-ts check-generated

generate: generate-go generate-ts

generate-go:
	cd services/host-agent && sqlc generate

generate-ts:
	tygo generate

# CI check: ensure generated files are committed
check-generated: generate
	@git diff --exit-code services/host-agent/internal/db/ || \
		(echo "Go generated files are out of date. Run 'make generate'" && exit 1)
	@git diff --exit-code services/host-agent/web/src/lib/generated/ || \
		(echo "TypeScript generated files are out of date. Run 'make generate'" && exit 1)
```

### 3.5 Alternative: OpenAPI Spec as Source

If you prefer API-first design:

```yaml
# openapi.yaml
openapi: 3.0.0
info:
  title: Bloud Host Agent API
  version: 1.0.0

components:
  schemas:
    AppStatus:
      type: string
      enum: [running, starting, installing, uninstalling, stopped, error]

    App:
      type: object
      required: [name, display_name, status, is_system]
      properties:
        id:
          type: integer
        name:
          type: string
        display_name:
          type: string
        version:
          type: string
        status:
          $ref: '#/components/schemas/AppStatus'
        port:
          type: integer
          minimum: 1
          maximum: 65535
        is_system:
          type: boolean
        integration_config:
          type: object
          additionalProperties:
            type: string
        installed_at:
          type: string
          format: date-time
        updated_at:
          type: string
          format: date-time
```

Generate both:
- Go: `oapi-codegen -package api openapi.yaml > internal/api/types.gen.go`
- TypeScript: `npx openapi-typescript openapi.yaml -o web/src/lib/generated/api.ts`

---

## Part 4: Implementation Priority

### Phase 1: Quick Wins (Low effort, high impact)

| Task | Location | Effort |
|------|----------|--------|
| Add Zod validation to SSE handler | `appActions.ts` | 1 hour |
| Add CHECK constraints to schema | `schema.sql` | 30 min |
| Add connection status store | `stores/connection.ts` | 1 hour |
| Create store facade/index | `stores/index.ts` | 30 min |

### Phase 2: Store Improvements (Medium effort)

| Task | Location | Effort |
|------|----------|--------|
| Per-operation loading states | `stores/apps.ts` | 2 hours |
| Catalog caching store | `stores/catalog.ts` | 2 hours |
| Optimistic updates for install | `appActions.ts` | 1 hour |
| Force refresh capability | `appActions.ts` | 1 hour |

### Phase 3: Type Generation (Larger effort)

| Task | Location | Effort |
|------|----------|--------|
| Set up sqlc | `sqlc.yaml`, queries | 4 hours |
| Migrate store to sqlc-generated types | `store/apps.go` | 4 hours |
| Set up tygo | `tygo.yaml` | 2 hours |
| Add to CI pipeline | `Makefile`, CI config | 2 hours |

### Phase 4: Migrations (When needed)

| Task | Location | Effort |
|------|----------|--------|
| Set up golang-migrate | `db/` | 2 hours |
| Convert schema to migration | `migrations/` | 2 hours |
| Add migration to startup | `db.go` | 1 hour |

---

---

## Part 5: Unified WebSocket Architecture

### 5.1 Why WebSocket Over Multiple SSE Streams

As the application grows, we'll need real-time updates for:
- App status changes (current SSE)
- NixOS rebuild progress
- Log streaming
- Container resource stats (CPU, RAM)
- Host discovery events
- Settings synchronization

A single WebSocket connection is more efficient than 4-5 SSE streams and enables bidirectional communication.

### 5.2 Message Protocol Design

Use a typed message envelope for all WebSocket communication:

```typescript
// lib/stores/ws/protocol.ts

// Server → Client message types
export type ServerMessage =
  | { type: 'apps'; payload: App[] }
  | { type: 'app_status'; payload: { name: string; status: AppStatus } }
  | { type: 'rebuild_progress'; payload: RebuildProgress }
  | { type: 'rebuild_complete'; payload: RebuildResult }
  | { type: 'logs'; payload: LogEntry[] }
  | { type: 'stats'; payload: ContainerStats }
  | { type: 'hosts'; payload: Host[] }
  | { type: 'error'; payload: { code: string; message: string } }
  | { type: 'pong'; payload: { timestamp: number } };

// Client → Server message types
export type ClientMessage =
  | { type: 'subscribe'; payload: { channels: Channel[] } }
  | { type: 'unsubscribe'; payload: { channels: Channel[] } }
  | { type: 'ping'; payload: { timestamp: number } }
  | { type: 'logs_subscribe'; payload: { app: string; lines?: number } }
  | { type: 'logs_unsubscribe'; payload: { app: string } };

export type Channel = 'apps' | 'rebuild' | 'stats' | 'hosts';

// Payload types
export interface RebuildProgress {
  phase: 'evaluating' | 'building' | 'activating' | 'done' | 'error';
  message: string;
  percent?: number;
}

export interface RebuildResult {
  success: boolean;
  duration_ms: number;
  error?: string;
}

export interface LogEntry {
  app: string;
  timestamp: string;
  level: 'debug' | 'info' | 'warn' | 'error';
  message: string;
}

export interface ContainerStats {
  app: string;
  cpu_percent: number;
  memory_mb: number;
  memory_limit_mb: number;
}
```

### 5.3 WebSocket Connection Manager

```typescript
// lib/stores/ws/connection.ts
import { writable, get } from 'svelte/store';
import type { ServerMessage, ClientMessage, Channel } from './protocol';

export type ConnectionState = 'connecting' | 'connected' | 'disconnected';

export const connectionState = writable<ConnectionState>('disconnected');
export const lastConnected = writable<Date | null>(null);
export const reconnectAttempts = writable(0);

let ws: WebSocket | null = null;
let reconnectTimeout: ReturnType<typeof setTimeout> | null = null;
let pingInterval: ReturnType<typeof setInterval> | null = null;

const messageHandlers = new Map<ServerMessage['type'], (payload: unknown) => void>();

// Register a handler for a message type
export function onMessage<T extends ServerMessage['type']>(
  type: T,
  handler: (payload: Extract<ServerMessage, { type: T }>['payload']) => void
): () => void {
  messageHandlers.set(type, handler as (payload: unknown) => void);
  return () => messageHandlers.delete(type);
}

// Send a message to the server
export function send(message: ClientMessage): void {
  if (ws?.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(message));
  } else {
    console.warn('WebSocket not connected, message dropped:', message.type);
  }
}

// Subscribe to channels
export function subscribe(channels: Channel[]): void {
  send({ type: 'subscribe', payload: { channels } });
}

// Unsubscribe from channels
export function unsubscribe(channels: Channel[]): void {
  send({ type: 'unsubscribe', payload: { channels } });
}

export function connect(): void {
  if (ws?.readyState === WebSocket.OPEN) return;

  connectionState.set('connecting');

  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${protocol}//${window.location.host}/api/ws`);

  ws.onopen = () => {
    connectionState.set('connected');
    lastConnected.set(new Date());
    reconnectAttempts.set(0);

    // Subscribe to default channels
    subscribe(['apps']);

    // Start ping interval
    pingInterval = setInterval(() => {
      send({ type: 'ping', payload: { timestamp: Date.now() } });
    }, 30_000);
  };

  ws.onmessage = (event) => {
    try {
      const message: ServerMessage = JSON.parse(event.data);
      const handler = messageHandlers.get(message.type);
      if (handler) {
        handler(message.payload);
      } else {
        console.debug('Unhandled message type:', message.type);
      }
    } catch (err) {
      console.error('Failed to parse WebSocket message:', err);
    }
  };

  ws.onclose = () => {
    connectionState.set('disconnected');
    cleanup();
    scheduleReconnect();
  };

  ws.onerror = (err) => {
    console.error('WebSocket error:', err);
    ws?.close();
  };
}

export function disconnect(): void {
  cleanup();
  ws?.close();
  ws = null;
}

function cleanup(): void {
  if (pingInterval) {
    clearInterval(pingInterval);
    pingInterval = null;
  }
  if (reconnectTimeout) {
    clearTimeout(reconnectTimeout);
    reconnectTimeout = null;
  }
}

function scheduleReconnect(): void {
  const attempts = get(reconnectAttempts);
  // Exponential backoff: 1s, 2s, 4s, 8s, max 30s
  const delay = Math.min(1000 * Math.pow(2, attempts), 30_000);

  reconnectTimeout = setTimeout(() => {
    reconnectAttempts.update(n => n + 1);
    connect();
  }, delay);
}
```

### 5.4 Store Integration

Each store registers handlers for its message types:

```typescript
// lib/stores/apps.ts
import { writable, derived } from 'svelte/store';
import { onMessage } from './ws/connection';
import { AppListSchema } from './validation';
import type { App, AppStatus } from './validation';

export const apps = writable<App[]>([]);
export const loading = writable(true);
export const error = writable<string | null>(null);

// Derived stores (unchanged)
export const userApps = derived(apps, ($apps) => $apps.filter((a) => !a.is_system));
export const visibleApps = derived(apps, ($apps) =>
  $apps.filter((a) => !a.is_system && a.status !== 'uninstalling')
);

// Register WebSocket handlers
export function initAppStore(): () => void {
  const unsubscribers = [
    // Full app list updates
    onMessage('apps', (payload) => {
      const result = AppListSchema.safeParse(payload);
      if (result.success) {
        apps.set(result.data);
        loading.set(false);
        error.set(null);
      } else {
        console.error('Invalid apps payload:', result.error);
      }
    }),

    // Individual app status changes (more efficient for single updates)
    onMessage('app_status', ({ name, status }) => {
      apps.update((current) =>
        current.map((app) => (app.name === name ? { ...app, status } : app))
      );
    }),

    // Handle errors
    onMessage('error', ({ code, message }) => {
      if (code.startsWith('apps.')) {
        error.set(message);
      }
    }),
  ];

  return () => unsubscribers.forEach((unsub) => unsub());
}
```

```typescript
// lib/stores/rebuild.ts
import { writable } from 'svelte/store';
import { onMessage } from './ws/connection';
import type { RebuildProgress, RebuildResult } from './ws/protocol';

export const rebuildInProgress = writable(false);
export const rebuildProgress = writable<RebuildProgress | null>(null);
export const rebuildResult = writable<RebuildResult | null>(null);

export function initRebuildStore(): () => void {
  const unsubscribers = [
    onMessage('rebuild_progress', (progress) => {
      rebuildInProgress.set(progress.phase !== 'done' && progress.phase !== 'error');
      rebuildProgress.set(progress);
    }),

    onMessage('rebuild_complete', (result) => {
      rebuildInProgress.set(false);
      rebuildResult.set(result);
    }),
  ];

  return () => unsubscribers.forEach((unsub) => unsub());
}
```

```typescript
// lib/stores/logs.ts
import { writable, derived } from 'svelte/store';
import { onMessage, send } from './ws/connection';
import type { LogEntry } from './ws/protocol';

// Store logs per app, keep last 1000 lines
const MAX_LOGS = 1000;

export const logsByApp = writable<Map<string, LogEntry[]>>(new Map());

export function getLogsForApp(name: string) {
  return derived(logsByApp, ($logs) => $logs.get(name) ?? []);
}

export function subscribeToLogs(app: string, lines = 100): void {
  send({ type: 'logs_subscribe', payload: { app, lines } });
}

export function unsubscribeFromLogs(app: string): void {
  send({ type: 'logs_unsubscribe', payload: { app } });
}

export function initLogsStore(): () => void {
  return onMessage('logs', (entries) => {
    logsByApp.update((current) => {
      const next = new Map(current);

      for (const entry of entries) {
        const existing = next.get(entry.app) ?? [];
        const updated = [...existing, entry].slice(-MAX_LOGS);
        next.set(entry.app, updated);
      }

      return next;
    });
  });
}
```

### 5.5 Initialization

```typescript
// lib/stores/index.ts
import { connect, disconnect, connectionState } from './ws/connection';
import { initAppStore } from './apps';
import { initRebuildStore } from './rebuild';
import { initLogsStore } from './logs';

let cleanup: (() => void)[] = [];

export function initStores(): void {
  // Connect WebSocket
  connect();

  // Initialize all store handlers
  cleanup = [
    initAppStore(),
    initRebuildStore(),
    initLogsStore(),
  ];
}

export function destroyStores(): void {
  cleanup.forEach((fn) => fn());
  cleanup = [];
  disconnect();
}

// Re-export stores and connection state
export { connectionState } from './ws/connection';
export { apps, userApps, visibleApps, loading, error } from './apps';
export { rebuildInProgress, rebuildProgress, rebuildResult } from './rebuild';
export { logsByApp, getLogsForApp, subscribeToLogs, unsubscribeFromLogs } from './logs';
```

### 5.6 Go Backend WebSocket Handler

```go
// internal/api/websocket.go
package api

import (
	"encoding/json"
	"sync"

	"github.com/gorilla/websocket"
)

type MessageType string

const (
	MsgApps           MessageType = "apps"
	MsgAppStatus      MessageType = "app_status"
	MsgRebuildProgress MessageType = "rebuild_progress"
	MsgRebuildComplete MessageType = "rebuild_complete"
	MsgLogs           MessageType = "logs"
	MsgStats          MessageType = "stats"
	MsgHosts          MessageType = "hosts"
	MsgError          MessageType = "error"
	MsgPong           MessageType = "pong"
)

type ServerMessage struct {
	Type    MessageType `json:"type"`
	Payload interface{} `json:"payload"`
}

type ClientMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type Client struct {
	conn       *websocket.Conn
	send       chan ServerMessage
	hub        *Hub
	subscribed map[string]bool
	mu         sync.RWMutex
}

type Hub struct {
	clients    map[*Client]bool
	broadcast  chan ServerMessage
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan ServerMessage, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()

		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					// Client buffer full, disconnect
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Broadcast sends a message to all connected clients
func (h *Hub) Broadcast(msgType MessageType, payload interface{}) {
	h.broadcast <- ServerMessage{Type: msgType, Payload: payload}
}

// BroadcastToChannel sends to clients subscribed to a specific channel
func (h *Hub) BroadcastToChannel(channel string, msgType MessageType, payload interface{}) {
	msg := ServerMessage{Type: msgType, Payload: payload}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients {
		client.mu.RLock()
		subscribed := client.subscribed[channel]
		client.mu.RUnlock()

		if subscribed {
			select {
			case client.send <- msg:
			default:
				// Skip slow clients
			}
		}
	}
}
```

### 5.7 Channel-Based Subscriptions

The WebSocket supports selective subscriptions to reduce unnecessary traffic:

```typescript
// Subscribe only to what you need
subscribe(['apps']);                    // Always subscribed
subscribe(['rebuild']);                 // Only during rebuild view
subscribe(['stats']);                   // Only on dashboard
unsubscribe(['stats']);                 // When leaving dashboard
```

### 5.8 Migration Path from SSE

1. **Phase 1:** Implement WebSocket alongside SSE
   - Add `/api/ws` endpoint
   - Both work in parallel

2. **Phase 2:** Migrate stores to WebSocket
   - Update `appActions.ts` to use WebSocket
   - Keep SSE as fallback

3. **Phase 3:** Remove SSE
   - Delete `/api/apps/events` endpoint
   - Remove SSE code from frontend

4. **Phase 4:** Add new features
   - Rebuild progress
   - Log streaming
   - Container stats

---

## Appendix: File Structure After Changes

```
services/host-agent/
├── sqlc.yaml
├── internal/
│   ├── api/
│   │   ├── routes.go
│   │   ├── events.go          # SSE (deprecated after WebSocket)
│   │   ├── websocket.go       # WebSocket hub + handlers
│   │   └── ws_client.go       # Per-client WebSocket logic
│   └── db/
│       ├── schema.sql
│       ├── queries.sql
│       ├── migrations/
│       │   ├── 001_initial.up.sql
│       │   └── 001_initial.down.sql
│       ├── db.go
│       ├── models.go          # Generated by sqlc
│       └── queries.sql.go     # Generated by sqlc
└── web/
    └── src/
        └── lib/
            ├── generated/
            │   └── types.ts   # Generated by tygo
            └── stores/
                ├── index.ts           # Facade - main export
                ├── apps.ts            # App state + handlers
                ├── catalog.ts         # Catalog with caching
                ├── rebuild.ts         # Rebuild progress
                ├── logs.ts            # Log streaming
                ├── validation.ts      # Zod schemas
                └── ws/
                    ├── protocol.ts    # Message type definitions
                    └── connection.ts  # WebSocket manager
```

## Appendix: Message Flow Diagram

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              Frontend                                    │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  +layout.svelte                                                         │
│       │                                                                 │
│       │ onMount()                                                       │
│       ▼                                                                 │
│  initStores()  ──────────────────────────────────────────────────────┐  │
│       │                                                              │  │
│       ├─► connect()           // WebSocket connection                │  │
│       ├─► initAppStore()      // Register 'apps' handler            │  │
│       ├─► initRebuildStore()  // Register 'rebuild_*' handlers      │  │
│       └─► initLogsStore()     // Register 'logs' handler            │  │
│                                                                      │  │
│  ┌───────────────────────────────────────────────────────────────┐  │  │
│  │                    ws/connection.ts                            │  │  │
│  │  ┌─────────────┐      ┌──────────────────┐                    │  │  │
│  │  │  WebSocket  │◄────►│  messageHandlers │                    │  │  │
│  │  │  /api/ws    │      │  Map<type, fn>   │                    │  │  │
│  │  └─────────────┘      └──────────────────┘                    │  │  │
│  │         │                      │                               │  │  │
│  │         │ onmessage            │ dispatch                      │  │  │
│  │         ▼                      ▼                               │  │  │
│  │  { type: 'apps',    ───►  apps.set(payload)                   │  │  │
│  │    payload: [...] }       loading.set(false)                   │  │  │
│  │                                                                │  │  │
│  │  { type: 'rebuild_ ───►  rebuildProgress.set(payload)         │  │  │
│  │    progress', ... }                                            │  │  │
│  │                                                                │  │  │
│  │  { type: 'logs',   ───►  logsByApp.update(...)                │  │  │
│  │    payload: [...] }                                            │  │  │
│  └───────────────────────────────────────────────────────────────┘  │  │
│                                                                      │  │
│  Components subscribe to stores:                                     │  │
│    {#if $loading} ... {:else} {#each $apps as app} ...              │  │
│                                                                      │  │
└──────────────────────────────────────────────────────────────────────┘  │
                                    │                                      │
                                    │ WebSocket                            │
                                    ▼                                      │
┌─────────────────────────────────────────────────────────────────────────┐
│                              Backend                                     │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                         Hub                                      │   │
│  │  ┌─────────┐  ┌─────────┐  ┌─────────┐                          │   │
│  │  │ Client1 │  │ Client2 │  │ Client3 │  ...                     │   │
│  │  └─────────┘  └─────────┘  └─────────┘                          │   │
│  │       │            │            │                                │   │
│  │       └────────────┴────────────┘                                │   │
│  │                    │                                             │   │
│  │              Broadcast(type, payload)                            │   │
│  └─────────────────────────────────────────────────────────────────┘   │
│                         ▲                                               │
│                         │                                               │
│  ┌──────────────────────┴──────────────────────────────────────────┐   │
│  │                                                                  │   │
│  │  AppStore.Install()  ──► hub.Broadcast("apps", allApps)         │   │
│  │  AppStore.UpdateStatus() ──► hub.Broadcast("app_status", {...}) │   │
│  │  RebuildOrchestrator ──► hub.Broadcast("rebuild_progress", ...) │   │
│  │  LogStreamer         ──► hub.BroadcastToChannel("logs", ...)    │   │
│  │                                                                  │   │
│  └──────────────────────────────────────────────────────────────────┘   │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```
