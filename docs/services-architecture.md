# Bloud Services Architecture

**High-Level Design Document**

Version: 1.0
Date: January 2026
Status: Design Phase

---

## Table of Contents

1. [Overview](#overview)
2. [System Architecture](#system-architecture)
3. [Host Agent Service](#host-agent-service)
4. [Key User Flows](#key-user-flows)
5. [Security & Privacy](#security--privacy)
6. [Data Models](#data-models)
7. [API Design](#api-design)
8. [Build Phases](#build-phases)
9. [Open Questions](#open-questions)

---

## Overview

Bloud is a Go service that provides zero-config home server management:

**Host Agent (runs on each Bloud device)**
- Web UI for app management
- App installation/removal via NixOS rebuilds
- Multi-host discovery and orchestration
- Local state management (SQLite)

**Design Principles:**
- **Local-first:** Everything runs on your hardware
- **Privacy-first:** Your data stays on your network
- **Zero-config:** Automation handles complexity (Nix generation, etc)

---

## System Architecture

### Shared Resource Architecture

**Design Principle:** Each Bloud host runs a maximum of **one instance** of each core infrastructure service:
- **1 PostgreSQL instance** - Shared database for all apps that need PostgreSQL ✅ Implemented
- **1 Redis instance** - Planned for apps that need Redis (currently Authentik runs its own embedded Redis)
- **1 Restic instance** - Planned for backup service (not yet implemented)

**Rationale:**
- **Resource efficiency**: Single instances use less RAM and CPU than per-app instances
- **Simplified management**: One database to monitor, backup, and maintain
- **Performance**: Connection pooling and caching more effective with shared instances
- **Consistency**: Single source of truth for data, shared cache for apps

Apps are configured to connect to the shared infrastructure automatically via environment variables and service dependencies.

### High-Level Overview

```
┌─────────────────────────────────────────────────────────────┐
│  User's Home Network                                        │
│                                                             │
│  ┌──────────────────┐         ┌──────────────────┐         │
│  │  Host 1          │  mDNS   │  Host 2          │         │
│  │  bloud.local     │◄───────►│  bloud2.local    │         │
│  │                  │         │                  │         │
│  │  ┌────────────┐  │         │  ┌────────────┐  │         │
│  │  │Host Agent  │  │         │  │Host Agent  │  │         │
│  │  │(Go service)│  │         │  │(Go service)│  │         │
│  │  │  - Web UI  │  │         │  │  - Web UI  │  │         │
│  │  │  - APIs    │  │         │  │  - APIs    │  │         │
│  │  │  - SQLite  │  │         │  │  - SQLite  │  │         │
│  │  └────────────┘  │         │  └────────────┘  │         │
│  │        ↕         │         │        ↕         │         │
│  │  NixOS Rebuild   │         │  NixOS Rebuild   │         │
│  │        ↕         │         │        ↕         │         │
│  │  ┌────────────┐  │         │  ┌────────────┐  │         │
│  │  │Podman Apps │  │         │  │Podman Apps │  │         │
│  │  │- Miniflux  │  │         │  │- Jellyfin  │  │         │
│  │  │- Authentik │  │         │  │- Immich    │  │         │
│  │  └────────────┘  │         │  └────────────┘  │         │
│  └──────────────────┘         └──────────────────┘         │
└─────────────────────────────────────────────────────────────┘
```

### Access Patterns

```
User Browser → http://bloud.local:8080
→ Host Agent (direct connection)
→ Serves Web UI
```

---

## Host Agent Service

**Language:** Go
**Runs on:** Each Bloud device (NixOS systemd service)
**Port:** 8080 (HTTP)
**State:** SQLite database

### Responsibilities

1. **Web UI Server**
   - Serve frontend (SvelteKit with SSR/SSG)
   - Initial page rendered server-side, then hydrated on client
   - REST API for UI interactions
   - WebSocket for real-time updates (service status, logs)

2. **App Management**
   - List available apps (from catalog)
   - Install app → generate/update NixOS config → trigger rebuild
   - Uninstall app → remove from config → trigger rebuild
   - App status monitoring (systemd unit status)

3. **Multi-Host Discovery**
   - mDNS announcements (Avahi/Bonjour)
   - Discover other hosts (bloud*.local pattern)
   - Peer-to-peer communication (REST API between hosts)
   - App distribution visualization

4. **App Migration**
   - Trigger: User drags app from host1 → host2 in UI
   - Update Nix configs on both hosts
   - Rsync data from source to destination
   - Rebuild both hosts
   - Verify migration success

5. **System Monitoring**
   - CPU, RAM, disk usage
   - Container status (podman ps)
   - Service logs (journalctl integration)
   - Network status

### Technology Stack

**Core:**
- Go 1.21+
- SQLite (state, app catalog cache, host registry)
- Standard library HTTP server

**Key Libraries (TBD):**
- Router: `chi` or `gorilla/mux`
- SQLite: `mattn/go-sqlite3` or `modernc.org/sqlite`
- WebSocket: `gorilla/websocket`
- mDNS: `hashicorp/mdns` or `grandcat/zeroconf`

### State Schema (SQLite)

```sql
-- Installed apps on this host
CREATE TABLE apps (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,  -- e.g., "miniflux"
    version TEXT,
    status TEXT,  -- "running", "stopped", "error"
    installed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Discovered peer hosts
CREATE TABLE hosts (
    id INTEGER PRIMARY KEY,
    hostname TEXT NOT NULL UNIQUE,  -- e.g., "bloud2.local"
    ip_address TEXT,
    last_seen TIMESTAMP,
    status TEXT  -- "online", "offline"
);
```

### Dashboard & Widget System

**Design Philosophy:** The host agent home page is a customizable drag-and-drop dashboard where users can arrange widgets from installed apps. This provides a personalized home experience while giving app developers a way to surface key information and actions.

#### Widget Plugin Architecture

**Core Concepts:**
- **Widget**: A self-contained UI component provided by an app
- **Widget Manifest**: Declarative definition of widget capabilities (size, refresh rate, configuration)
- **Widget API**: Standardized interface for data fetching and actions
- **Dashboard Layout**: User-defined arrangement stored in SQLite

#### Widget Discovery & Registration

Apps can provide widgets by including a widget manifest in their catalog definition:

```yaml
# catalog/miniflux.yaml
name: miniflux
displayName: Miniflux
widgets:
  - id: unread-count
    name: Unread Articles
    description: Shows count of unread RSS articles
    defaultSize: small          # small (1x1), medium (2x1), large (2x2)
    resizable: true
    refreshInterval: 300        # seconds (0 = no auto-refresh)
    component: /widgets/miniflux-unread.js  # Svelte component

  - id: recent-articles
    name: Recent Articles
    description: List of recently published articles
    defaultSize: medium
    resizable: true
    refreshInterval: 600
    component: /widgets/miniflux-recent.js
    config:                     # Optional: widget configuration schema
      - name: maxItems
        type: number
        default: 5
        label: "Number of articles to show"
```

**Widget Component Interface:**

Widgets are Svelte components that follow a standard contract:

```typescript
// Widget Component Props Interface
interface WidgetProps {
  config: Record<string, any>;     // User configuration from widget settings
  size: 'small' | 'medium' | 'large';
  isEditing: boolean;              // Dashboard in edit mode
}

// Widget Component Events
interface WidgetEvents {
  'refresh': () => void;           // User manually triggered refresh
  'configure': () => void;         // Open configuration dialog
  'error': (error: Error) => void; // Widget encountered an error
}
```

**Example Widget Component:**

```svelte
<!-- widgets/miniflux-unread.svelte -->
<script lang="ts">
  import { onMount } from 'svelte';

  export let config: { maxItems: number };
  export let size: 'small' | 'medium' | 'large';
  export let isEditing: boolean;

  let unreadCount = 0;
  let loading = true;

  async function fetchUnreadCount() {
    const res = await fetch('/api/apps/miniflux/widget/unread-count');
    const data = await res.json();
    unreadCount = data.count;
    loading = false;
  }

  onMount(fetchUnreadCount);
</script>

<div class="widget widget-{size}">
  {#if loading}
    <div class="loading">Loading...</div>
  {:else}
    <div class="metric">
      <div class="value">{unreadCount}</div>
      <div class="label">Unread Articles</div>
    </div>
  {/if}
</div>
```

#### Widget API Endpoints

Apps expose widget data through standardized API endpoints:

```
GET  /api/apps/:appName/widgets                    # List available widgets for app
GET  /api/apps/:appName/widget/:widgetId/data      # Fetch widget data
POST /api/apps/:appName/widget/:widgetId/action    # Execute widget action
```

**Backend Implementation (App-Specific):**

Each app can implement custom widget data providers. The host agent acts as a proxy:

```
User Dashboard Request
  ↓
Host Agent (GET /api/apps/miniflux/widget/unread-count/data)
  ↓
Miniflux Container (via internal API call)
  ↓
Returns: { "count": 42, "timestamp": "2026-01-15T10:30:00Z" }
```

#### Dashboard Layout Management

**User Dashboard Configuration:**

Dashboard layouts are persisted in the host agent's database. Each user's layout stores:
- Widget ID (e.g., "miniflux:unread-count")
- Position (x, y coordinates on grid)
- Size (columns and rows)
- Widget-specific configuration (JSON)

**Note:** User management and multi-user support is a broader architectural decision that needs to be designed holistically across all components (host agent, Authentik SSO). Database schema will be defined as part of that larger effort.

**Dashboard API Endpoints:**

```
GET  /api/dashboard/layout           # Get current user's dashboard layout
POST /api/dashboard/layout           # Save dashboard layout (after drag/drop)
POST /api/dashboard/widgets/add      # Add widget to dashboard
DELETE /api/dashboard/widgets/:id    # Remove widget from dashboard
PUT  /api/dashboard/widgets/:id      # Update widget config/position
```

#### Drag-and-Drop Implementation

**Technology:** Svelte DnD (or similar drag-and-drop library)

**User Flow:**
1. User clicks "Edit Dashboard" → enters edit mode
2. Widgets become draggable, grid overlay appears
3. User drags widget to new position → layout updates in real-time
4. User resizes widget by dragging handles → recalculates grid size
5. User clicks "Done Editing" → saves layout via API

**Grid System:**
- 12-column responsive grid (similar to Bootstrap)
- Small widgets: 1x1 (4 columns)
- Medium widgets: 2x1 (6 columns)
- Large widgets: 2x2 (12 columns on mobile, 6 columns on desktop)

**Example Layout State:**

```json
{
  "widgets": [
    {
      "id": "widget_1",
      "widgetId": "miniflux:unread-count",
      "x": 0,
      "y": 0,
      "cols": 1,
      "rows": 1,
      "config": {}
    },
    {
      "id": "widget_2",
      "widgetId": "jellyfin:recently-watched",
      "x": 1,
      "y": 0,
      "cols": 2,
      "rows": 1,
      "config": { "maxItems": 3 }
    }
  ]
}
```

#### Widget Lifecycle & Performance

**Widget Loading:**
1. Dashboard loads → fetches user's layout from API
2. For each widget, loads component asynchronously (code splitting)
3. Widget mounts → fetches initial data from widget API
4. Auto-refresh based on `refreshInterval` (if > 0)

**Performance Optimizations:**
- **Lazy loading**: Widgets load only when in viewport (Intersection Observer)
- **Code splitting**: Each widget is a separate bundle
- **Caching**: Widget data cached with SWR (stale-while-revalidate)
- **Throttling**: Drag/drop position updates throttled to 60fps
- **Virtual scrolling**: For dashboards with many widgets

**Error Handling:**
- Widget fetch fails → show error state in widget card
- Widget component crashes → error boundary catches, shows fallback
- Refresh fails → keep showing stale data with "outdated" indicator

#### Widget Configuration UI

**Configuration Dialog:**
- Triggered by clicking "Configure" icon on widget (in edit mode)
- Renders form based on widget's config schema
- Saves to `dashboard_layout.config` JSON column
- Widget receives new config via props, re-fetches data

**Example Config Schema:**

```yaml
# From widget manifest
config:
  - name: maxItems
    type: number
    default: 5
    min: 1
    max: 20
    label: "Number of items"

  - name: showThumbnails
    type: boolean
    default: true
    label: "Show thumbnails"

  - name: sortBy
    type: select
    options:
      - { value: "date", label: "Date" }
      - { value: "title", label: "Title" }
    default: "date"
    label: "Sort by"
```

#### Security & Isolation

**Widget Sandboxing:**
- Widgets run in same context as dashboard (same-origin)
- API calls authenticated via user session cookies
- Widgets cannot access data from other apps (enforced by backend API)
- No inline scripts (Content Security Policy)

**API Authorization:**
```
GET /api/apps/miniflux/widget/unread-count/data
  ↓
Host Agent checks:
  1. User is authenticated (session valid)
  2. Miniflux is installed on this host
  3. User has permission to access Miniflux (SSO check)
  ↓
Proxies to Miniflux container's widget API
```

#### Example Widgets by App

**System Widgets (Built-in):**
- CPU/RAM/Disk usage sparklines
- Service status (running/stopped containers)
- Network traffic graph
- Recent system logs

**App Widgets:**
- **Miniflux**: Unread count, recent articles
- **Jellyfin**: Recently watched, continue watching
- **Immich**: Photo of the day, recent uploads, storage usage
- **Actual Budget**: Net worth trend, recent transactions
- **Sonarr/Radarr**: Upcoming releases, download queue
- **Nextcloud**: Storage usage, recent files
- **Authentik**: Failed login attempts, active sessions

#### Default Dashboard

**New User Experience:**

When a user first logs in, the dashboard shows:
1. **Welcome widget** (instructions, quick links)
2. **System status widget** (CPU, RAM, disk)
3. **Installed apps widget** (grid of app icons with status)
4. **Add widgets button** (prominent, guides user to customize)

Once user installs apps, suggested widgets appear in "Add Widget" dialog.

#### Future Enhancements

**Phase 2:**
- Widget marketplace (share custom widgets)
- Cross-app widgets (e.g., "Media Overview" combining Jellyfin + Immich)
- Widget themes (dark mode, color schemes)
- Dashboard templates (import/export layouts)

**Phase 3:**
- Mobile-optimized widget layouts
- Widget actions (e.g., "Play" button in Jellyfin widget)
- Real-time updates via WebSocket (no polling)
- Dashboard sharing (family members see same layout)

---

### API Endpoints (Host Agent)

**App Management:**
```
GET  /api/apps                      # List all available apps
GET  /api/apps/installed            # List installed apps on this host
GET  /api/apps/:name/plan-install   # Get installation plan (choices, auto-config, dependents)
POST /api/apps/:name/install        # Install an app (with optional integration choices)
GET  /api/apps/:name/plan-remove    # Get removal plan (blockers, will-unconfigure)
POST /api/apps/:name/uninstall      # Uninstall an app
GET  /api/apps/:name/status         # Get app status
GET  /api/apps/:name/logs           # Stream logs (WebSocket upgrade)
```

**Dashboard & Widgets:**
```
GET  /api/dashboard/layout                        # Get user's dashboard layout
POST /api/dashboard/layout                        # Save dashboard layout
POST /api/dashboard/widgets/add                   # Add widget to dashboard
DELETE /api/dashboard/widgets/:id                 # Remove widget
PUT  /api/dashboard/widgets/:id                   # Update widget config/position
GET  /api/apps/:appName/widgets                   # List available widgets for app
GET  /api/apps/:appName/widget/:widgetId/data     # Fetch widget data
POST /api/apps/:appName/widget/:widgetId/action   # Execute widget action
```

**Multi-Host:**
```
GET  /api/hosts                     # List discovered hosts
GET  /api/hosts/:hostname/apps      # List apps on remote host
POST /api/hosts/:hostname/apps/:name/migrate  # Migrate app to this host
```

**System:**
```
GET  /api/system/status        # CPU, RAM, disk, network
GET  /api/system/logs          # System logs (WebSocket upgrade)
```

---

## Key User Flows

### Flow 1: Initial Setup

```
1. User flashes USB drive, boots hardware
2. NixOS boots, host agent starts automatically
3. User opens browser → http://bloud.local:8080
4. First-time setup wizard:
   - Create admin user (stored in Authentik)
5. Dashboard loads, shows app catalog
6. User installs apps (Miniflux, Actual Budget, etc)
7. Host agent generates Nix configs, triggers rebuild
8. Apps appear in dashboard (with SSO auto-configured)
```

### Flow 2: Install App

```
1. User browses app catalog in dashboard
2. Clicks "Install Jellyfin"
3. Host agent:
   - Checks if prerequisites met (shared Postgres? Authentik?)
   - Generates nixos/apps/jellyfin.nix
   - Inserts Authentik blueprint (OAuth2 auto-config)
   - Updates imports in bloud.nix
4. Host agent triggers: nixos-rebuild switch
5. NixOS builds, starts containers:
   - Pulls Jellyfin image
   - Creates systemd service
   - Waits for dependencies (Postgres, Authentik)
   - Starts Jellyfin
6. Dashboard polls for status
7. Shows: "Jellyfin ready at http://bloud.local/jellyfin"
8. User clicks link, auto-logged in via SSO
```

### Flow 3: Add Second Host

```
1. User plugs USB into second machine, boots
2. Second host agent starts, announces via mDNS (bloud2.local)
3. First host discovers second host
4. Dashboard shows: "New host detected: bloud2.local"
5. User clicks "Add to cluster"
6. First host sends cluster token to second host
7. Second host joins (shares SQLite schema, config)
8. Dashboard now shows both hosts with their apps
9. User drags Jellyfin from bloud.local → bloud2.local
10. Host agents coordinate migration:
    - Host 1: Disable Jellyfin in Nix config, rebuild
    - Rsync data: host1:/data/jellyfin → host2:/data/jellyfin
    - Host 2: Enable Jellyfin in Nix config, rebuild
    - Host 2: Import data, start Jellyfin
11. Dashboard updates: Jellyfin now on bloud2.local
```

---

## Security & Privacy

### Design Principles

- All data stays on your local network
- SSO via Authentik (self-hosted identity provider)
- Container isolation via rootless Podman

### Encryption

**Transport:**
- Host agent can serve HTTPS (Let's Encrypt cert via DNS-01)
- Internal container traffic on isolated podman network

### Authentication

- SSO handled by Authentik (OpenID Connect)
- Session-based authentication for web UI
- API endpoints protected by session cookies

---

## Data Models

### App Catalog Schema

Apps are defined declaratively in YAML (versioned in git). The schema includes **integrations** that define explicit app-to-app compatibility:

```yaml
# catalog/radarr.yaml
name: radarr
image: lscr.io/linuxserver/radarr:latest

integrations:
  downloadClient:
    required: true
    multi: false  # Only one download client at a time
    compatible:
      - app: qbittorrent
        default: true  # Opinionated recommendation
      - app: deluge
      - app: transmission
```

```yaml
# catalog/jellyseerr.yaml
name: jellyseerr
image: fallenbagel/jellyseerr:latest

integrations:
  mediaServer:
    required: true
    multi: false
    compatible:
      - app: jellyfin
        default: true
      - app: plex

  pvr:
    required: true
    multi: true  # Can have BOTH radarr AND sonarr
    compatible:
      - app: radarr
        category: movies
      - app: sonarr
        category: tv
```

Apps without integrations (leaf nodes):

```yaml
# catalog/qbittorrent.yaml
name: qbittorrent
image: lscr.io/linuxserver/qbittorrent:latest
integrations: {}
```

### App Integration Graph

The host agent maintains a **dependency graph** that enables intelligent app installation:

**Core Data Structures:**

```go
// AppGraph manages app relationships
type AppGraph struct {
    Apps       map[string]*AppDefinition  // All known apps
    Installed  []string                   // Currently installed apps
    dependents map[string][]IntegrationRef // Reverse index: who needs this app?
}

// IntegrationRef is a back-pointer
type IntegrationRef struct {
    App         string  // e.g., "jellyseerr"
    Integration string  // e.g., "pvr"
}
```

**How It Works:**

When loading the catalog, the graph builds a reverse index (`dependents`) from integrations:

```json
{
  "dependents": {
    "qbittorrent": [
      {"app": "radarr", "integration": "downloadClient"},
      {"app": "sonarr", "integration": "downloadClient"}
    ],
    "radarr": [
      {"app": "jellyseerr", "integration": "pvr"}
    ],
    "jellyfin": [
      {"app": "jellyseerr", "integration": "mediaServer"}
    ]
  }
}
```

**Installation Planning:**

When installing an app, `PlanInstall()` computes:

1. **Choices** - Required integrations with no compatible apps installed (user must pick)
2. **AutoConfig** - Integrations with exactly one compatible app installed (auto-configure)
3. **Dependents** - Installed apps that will integrate with the new app

```
Example: Install radarr (qbittorrent installed, jellyseerr installed)

PlanInstall("radarr") returns:
{
  "app": "radarr",
  "canInstall": true,
  "choices": [],  // qbittorrent is installed, no choice needed
  "autoConfig": [
    {"target": "radarr", "source": "qbittorrent", "integration": "downloadClient"}
  ],
  "dependents": [
    {"target": "jellyseerr", "source": "radarr", "integration": "pvr"}
  ]
}
```

**Removal Planning:**

`PlanRemove()` checks if removal would break other apps:

```
Example: Remove qbittorrent (radarr installed, depends on it)

PlanRemove("qbittorrent") returns:
{
  "app": "qbittorrent",
  "canRemove": false,
  "blockers": ["radarr requires a downloadClient"]
}
```

**User Experience:**

```
┌─────────────────────────────────────────────────────────┐
│  Install Radarr                                          │
├─────────────────────────────────────────────────────────┤
│                                                          │
│  Radarr will be configured with:                        │
│  • qBittorrent (download client)                        │
│                                                          │
│  After installation:                                     │
│  • Jellyseerr will be updated to use Radarr for movies  │
│                                                          │
│  [Install Radarr]                                        │
└─────────────────────────────────────────────────────────┘
```

If no download client is installed:

```
┌─────────────────────────────────────────────────────────┐
│  Install Radarr                                          │
├─────────────────────────────────────────────────────────┤
│                                                          │
│  Radarr requires a download client.                     │
│                                                          │
│  ● qBittorrent (recommended)                            │
│  ○ Deluge                                               │
│  ○ Transmission                                          │
│                                                          │
│  [Install qBittorrent + Radarr]                         │
└─────────────────────────────────────────────────────────┘
```

**Implementation:** `services/host-agent/internal/catalog/`
- `types.go` - AppDefinition, Integration, CompatibleApp
- `graph.go` - AppGraph with dependents index
- `plan.go` - PlanInstall, PlanRemove logic

### Migration Plan Schema

When migrating app between hosts:

```json
{
  "migration_id": "mig_abc123",
  "app": "jellyfin",
  "source_host": "bloud.local",
  "dest_host": "bloud2.local",
  "steps": [
    {
      "step": 1,
      "action": "disable_app_source",
      "status": "completed",
      "timestamp": "2026-01-15T10:30:00Z"
    },
    {
      "step": 2,
      "action": "rsync_data",
      "status": "in_progress",
      "progress": "42%",
      "eta_seconds": 120
    },
    {
      "step": 3,
      "action": "enable_app_dest",
      "status": "pending"
    }
  ]
}
```

---

## API Design

### REST API Conventions

**Base URL:**
- Host Agent: `http://bloud.local:8080/api/v1`

**Authentication:**
- Session cookies (after Authentik SSO login)

**Response Format:**
```json
{
  "success": true,
  "data": { ... },
  "error": null
}
```

**Error Format:**
```json
{
  "success": false,
  "data": null,
  "error": {
    "code": "APP_ALREADY_INSTALLED",
    "message": "Jellyfin is already installed on this host",
    "details": { "app": "jellyfin", "host": "bloud.local" }
  }
}
```

**Pagination:**
```json
{
  "success": true,
  "data": [ ... ],
  "pagination": {
    "page": 1,
    "pageSize": 20,
    "total": 42,
    "hasMore": true
  }
}
```

### WebSocket Events

Host agent uses WebSocket for real-time updates:

**Connection:**
```
ws://bloud.local:8080/api/v1/events
```

**Event Types:**
```json
// App installation progress
{
  "type": "app.install.progress",
  "data": {
    "app": "jellyfin",
    "step": "building_container",
    "progress": 75
  }
}

// Service status change
{
  "type": "service.status",
  "data": {
    "service": "podman-jellyfin",
    "status": "running",
    "since": "2026-01-15T10:30:00Z"
  }
}

// Host discovered
{
  "type": "host.discovered",
  "data": {
    "hostname": "bloud2.local",
    "ip": "192.168.1.42",
    "apps": ["immich", "nextcloud"]
  }
}

// Migration progress
{
  "type": "migration.progress",
  "data": {
    "migration_id": "mig_abc123",
    "step": 2,
    "action": "rsync_data",
    "progress": 42,
    "eta_seconds": 120
  }
}
```

---

## Build Phases

### Phase 0: Prototype Validation (Current)

**Status:** In progress
**Goal:** Validate NixOS automation approach

- [x] NixOS + Podman infrastructure
- [x] Shared PostgreSQL/Redis
- [x] Authentik SSO integration
- [x] 4 apps working (Traefik, Authentik, Miniflux, Actual Budget)
- [ ] App manifest format (YAML catalog)
- [ ] Manual Nix config generation (test the pattern)

### Phase 1: Host Agent MVP

**Goal:** Build local-only experience, prove value

**Deliverables:**
1. **Web UI (Frontend)**
   - Dashboard (system status, installed apps)
   - App catalog browser
   - App installation wizard
   - System logs viewer
   - Settings page

2. **Host Agent (Backend)**
   - REST API server (Go)
   - SvelteKit SSR integration (serve rendered pages)
   - App catalog loader (YAML parser)
   - Nix config generator (template-based)
   - NixOS rebuild trigger (`nixos-rebuild switch` via exec)
   - Service status monitor (systemd via dbus)
   - SQLite state management

3. **Multi-Host Discovery (Basic)**
   - mDNS announcements
   - Peer discovery (scan for bloud*.local)
   - Host registry (SQLite)
   - Cross-host API calls (for app lists)

4. **Testing & Dogfooding**
   - Use it yourself on test hardware
   - Install 10+ apps
   - Document pain points
   - Refine UX

**Success Criteria:**
- Can install/uninstall apps via Web UI
- NixOS rebuilds work reliably
- Multiple hosts discover each other
- Dashboard shows real-time status
- Good enough to demo to friends

### Phase 2: Integration & Polish

**Goal:** End-to-end experience working smoothly

**Deliverables:**
1. **App Migration**
   - Drag-and-drop in UI
   - Rsync automation
   - Config coordination
   - Progress indicators

2. **Monitoring & Observability**
   - Host agent health checks
   - Alerting (service down)

3. **Documentation**
   - Architecture docs
   - API docs (OpenAPI/Swagger)
   - Troubleshooting guide

4. **Testing**
   - End-to-end tests (Playwright/Cypress)
   - Integration tests

**Success Criteria:**
- Full user journey works (install → configure → migrate apps)
- Robust error handling
- Good observability
- Ready for alpha users

---

## Open Questions

### Technical Decisions TBD

1. **Frontend Framework: SvelteKit (DECIDED)**
   - Server-side rendering (SSR) for initial page load
   - Static site generation (SSG) for cacheable content
   - Client-side hydration for interactivity
   - Benefits: Fast initial load, good SEO, progressive enhancement
   - Smallest bundle size among major frameworks

2. **Host Agent Database**
   - SQLite in-process (simpler, no daemon)
   - Embedded Postgres (pgx-based, more features)
   - Just files (YAML/JSON state)

3. **Migration Data Transfer**
   - Rsync (battle-tested, incremental)
   - Syncthing (continuous sync, could enable HA)
   - Custom protocol (optimized for Bloud)

4. **NixOS Rebuild Strategy**
   - Full rebuild every time (slow but safe)
   - Targeted rebuilds (faster but complex)
   - Caching layer (speed up repeated builds)

5. **App Catalog Distribution**
   - Git repo (versioned catalog)
   - Embedded in host agent binary (updates = new binary)

### Product Decisions TBD

1. **Multi-User Support**
   - Single admin per device (v1)
   - Multiple users with roles (v2)
   - Family accounts (v3)

2. **App Catalog Curation**
   - Only official apps (curated)
   - Community submissions (reviewed by maintainers)
   - Fully open (anyone can add, caveat emptor)

---

## Next Steps

1. **Finalize Phase 1 Scope**
   - Choose frontend framework
   - Design Web UI mockups (Figma?)
   - Define app catalog schema (finalize YAML format)

2. **Setup Development Environment**
   - Go project structure (host agent)
   - Frontend boilerplate
   - Local development workflow (hot reload, etc)

3. **Start Building**
   - Host agent: API server skeleton
   - Frontend: Dashboard page
   - First integration: "List installed apps"

4. **Iterate Rapidly**
   - Build → test → dogfood → refine
   - Focus on core loop: browse apps → install → works

---

**Document Owner:** Daniel
**Last Updated:** January 2026
**Status:** Living document (will evolve as we build)
