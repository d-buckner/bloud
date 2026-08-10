# Plan: Layout Refactor — Server-Owned Positions + Polling

## Problem

The app layout gets out of sync because state is split across two owners:

- **Backend** owns app status (SQLite via `appStore`)
- **Frontend** owns grid positions (localStorage + `prefsStore` as a dump)

When the backend installs or removes an app (CLI, dependency resolution, converge cleanup)
the frontend's layout store is never notified. `layout.addApp()` / `layout.removeApp()` only
run when the *frontend* initiates the action. The startup reconciliation in `initApps()` patches
this on page load only.

SSE adds complexity (pub-sub hub, reconnect loop, `pendingInstalls`, `suppressStoreSync`) for
what is ultimately a polling problem: "what apps are installed and where are they?"

## Direction

**Server owns positions. GridStack is the single source of truth for layout behaviour.**

- Positions flow server → GridStack on load/poll
- GridStack settles items (auto-compaction, auto-position for new items)
- GridStack `change` event → full settled layout flows back to server in one write
- Frontend polls one endpoint; no SSE, no reconciliation, no split state

---

## Data Model

### Backend: `user_app_positions` table

New join table — keeps positions separate from the app record (apps are global,
positions are per-user).

```sql
CREATE TABLE user_app_positions (
    username     TEXT    NOT NULL,
    element_id   TEXT    NOT NULL,          -- catalog_id or widget id
    element_type TEXT    NOT NULL,          -- 'app' | 'widget'
    x            INTEGER,                  -- NULL = let GridStack autoPosition
    y            INTEGER,
    w            INTEGER NOT NULL DEFAULT 1,
    h            INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (username, element_id)
);
```

`x`/`y` are nullable. `NULL` means no stored position; the frontend passes
`autoPosition: true` to GridStack, which picks a cell, fires `change`, and the client
writes the full settled layout back.

### Frontend: `GridElement` shape (unchanged)

```typescript
interface GridElement {
    type: 'app' | 'widget'
    id: string
    x: number | null   // null → autoPosition
    y: number | null
    w: number
    h: number
}
```

---

## API Endpoints

### `GET /api/user/home`

Returns everything the home screen needs in one response.

```json
{
  "apps": [
    {
      "catalog_id": "jellyfin",
      "display_name": "Jellyfin",
      "status": "running",
      "sso_launch_path": "/if/flow/...",
      "x": 0, "y": 0, "w": 1, "h": 1
    },
    {
      "catalog_id": "navidrome",
      "display_name": "Navidrome",
      "status": "installing",
      "x": null, "y": null, "w": 1, "h": 1
    }
  ],
  "widgets": [
    { "id": "system-stats", "x": 2, "y": 0, "w": 2, "h": 3 }
  ]
}
```

Apps with `x: null` get `autoPosition: true` in GridStack. After GridStack settles,
the client writes back the full layout.

Apps installed via the backend (CLI, dependency) appear here with `x: null` on the
next poll. They show up on the grid automatically — no frontend reconciliation needed.

### `PUT /api/user/layout`

Called by `handleGridChange` after GridStack settles. Sends the **full settled layout**
— all items GridStack currently knows about, not just the one that moved. This is
correct because a single drag can reflow the entire grid (float: false compaction).

```json
[
  { "id": "jellyfin",     "type": "app",    "x": 0, "y": 0, "w": 1, "h": 1 },
  { "id": "navidrome",    "type": "app",    "x": 1, "y": 0, "w": 1, "h": 1 },
  { "id": "system-stats", "type": "widget", "x": 2, "y": 0, "w": 2, "h": 3 }
]
```

Server replaces all `user_app_positions` rows for this user. One write per completed
drag/resize — GridStack's `change` event fires on mouseup, not during the drag.

---

## Install / Uninstall Lifecycle

### Install (any path — UI, CLI, orchestrator)

1. `appStore.Install()` creates the app record with status `installing`
2. Insert a `user_app_positions` row with `x: null, y: null` for all users
3. Next poll returns the app with `x: null`
4. `syncGridFromStore` calls `addGridStackItem` with `autoPosition: true`
5. GridStack places it, fires `change`
6. `handleGridChange` → `PUT /api/user/layout` with full settled layout

### Uninstall (any path)

1. `appStore.Uninstall()` deletes the app record
2. `user_app_positions` row is deleted (cascade or explicit)
3. Next poll omits the app
4. `syncGridFromStore` removes the GridStack item
5. GridStack compacts remaining items, fires `change`
6. `handleGridChange` → `PUT /api/user/layout` with updated layout

No `layout.removeApp()` call needed anywhere. Removal is driven by the poll diff.

---

## Frontend: Replace SSE with Adaptive Polling

### `poller.ts` (replaces `sse.ts`)

```typescript
const IDLE_INTERVAL_MS   = 10_000  // all apps stable
const ACTIVE_INTERVAL_MS =  2_000  // any app transitioning

function isTransitioning(app): boolean {
    return ['installing', 'starting', 'uninstalling'].includes(app.status)
}
```

- Polls `GET /api/user/home`
- Uses active interval when any app is in a transitional state
- Returns to idle interval once all apps are stable
- No reconnection logic, no event source, no pub-sub

### `appFacade.ts` changes

- Remove `connectSSE` / `disconnectSSE`
- Remove `pendingInstalls` — server status is authoritative
- `initApps()` → fetch `GET /api/user/home`, then `startPolling()`
- `disconnectApps()` → `stopPolling()`

### `GridStackGrid.svelte` changes

- `handleGridChange` → call `PUT /api/user/layout` with the full node list
  (same `getGridItems()` snapshot it already builds)
- `addGridStackItem` → add `autoPosition: true` when `x == null || y == null`
- `suppressStoreSync` flag stays (still needed to prevent the `$effect` feedback loop)

### `layout.ts` store — deleted

Position state lives in the server response. No local store, no localStorage,
no debounced saves, no migration logic, no `findNextAvailablePosition`.

---

## Backend: Deletions

| File / symbol | Reason |
|---|---|
| `api/events.go` (`AppEventHub`) | No more SSE push |
| `handleAppEvents` in `api/sse.go` | Replaced by polling `GET /api/user/home` |
| `GET /api/apps/events` route | Removed |
| `appStore.SetOnChange(appHub.Broadcast)` in `server.go` | No subscribers |
| `prefsStore.GetLayout` / `SetLayout` | Replaced by `user_app_positions` table |
| `handleGetLayout` / `handleSetLayout` | Replaced by new endpoints |
| `GET /api/user/layout` + `PUT /api/user/layout` (old) | Replaced by new endpoints |

`handleSystemStatusStream` (system stats SSE widget) is **not** affected.

---

## What This Eliminates

| Current complexity | Why it exists | Gone |
|---|---|---|
| `pendingInstalls` set | SSE timing uncertainty | Server status is authoritative |
| Layout reconciliation in `initApps` | Split state can drift | Single source of truth |
| `AppEventHub` pub-sub | Push SSE updates | Replaced by polling |
| SSE reconnect loop | Connection management | `setInterval` needs none |
| Dual-write localStorage + API | Offline resilience | Not needed (local server) |
| `findNextAvailablePosition` | Frontend had to manage positions | GridStack + autoPosition |
| Layout migration code | Old format compat | Fresh schema, no migration needed |

`suppressStoreSync` stays — it prevents the GridStack↔`$effect` feedback loop which
exists regardless of where positions are stored.

---

## Implementation Order

1. **Backend: `user_app_positions` table**
   - Add to schema
   - Migrate existing `prefsStore` layout JSON rows into the new table on startup

2. **Backend: `GET /api/user/home`**
   - Join `apps` + `user_app_positions` for the current user
   - Enrich with `sso_launch_path` from catalog

3. **Backend: `PUT /api/user/layout`**
   - Replace all `user_app_positions` rows for the current user

4. **Backend: wire install/uninstall to position table**
   - `appStore.Install()` → insert position row with `x: null, y: null` for all users
   - `appStore.Uninstall()` → delete position row for all users

5. **Frontend: `poller.ts`**
   - Adaptive polling of `GET /api/user/home`
   - Calls back with `{ apps, widgets }`

6. **Frontend: update `GridStackGrid.svelte`**
   - `handleGridChange` → `PUT /api/user/layout` with full node list
   - `addGridStackItem` → `autoPosition: true` when x/y null

7. **Frontend: update `appFacade.ts`**
   - Replace SSE with poller
   - Remove `pendingInstalls`
   - Remove layout store usage

8. **Delete dead code**
   - `api/events.go`, SSE app handler, SSE app events route
   - `web/src/lib/api/sse.ts`
   - `web/src/lib/stores/layout.ts`
   - Old `GET /api/user/layout` + `PUT /api/user/layout` handlers
