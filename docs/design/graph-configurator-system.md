# Bloud App Graph and Configurator System

## Overview

Bloud manages self-hosted applications through a two-phase system:

1. **Nix Generation** - Compiles desired app state to `.nix` files and runs `nixos-rebuild switch`
2. **Runtime Configuration** - Configurators ensure apps are properly set up and integrated

Both phases are designed to be **idempotent** - running them multiple times produces the same result without side effects. A 5-minute watchdog runs reconciliation continuously to self-heal any corruption or drift.

```
┌─────────────────────────────────────────────────────────────────────┐
│                        User Request                                  │
│                    "Install Radarr"                                  │
└─────────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│                      PHASE 1: Planning                               │
│  ┌─────────────┐    ┌──────────────┐    ┌─────────────────────┐    │
│  │ App Graph   │───▶│ Plan Install │───▶│ User Choices (UI)   │    │
│  │ (catalog)   │    │              │    │ "Use qBittorrent"   │    │
│  └─────────────┘    └──────────────┘    └─────────────────────┘    │
└─────────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    PHASE 2: Nix Generation                           │
│  ┌─────────────┐    ┌──────────────┐    ┌─────────────────────┐    │
│  │ Transaction │───▶│ Generate     │───▶│ nixos-rebuild       │    │
│  │ Builder     │    │ apps.nix     │    │ switch (atomic)     │    │
│  └─────────────┘    └──────────────┘    └─────────────────────┘    │
└─────────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│                 PHASE 3: Runtime Configuration                       │
│  ┌─────────────┐    ┌──────────────┐    ┌─────────────────────┐    │
│  │ PreStart    │───▶│ HealthCheck  │───▶│ PostStart           │    │
│  │ (files/dirs)│    │ (wait ready) │    │ (APIs/integrations) │    │
│  └─────────────┘    └──────────────┘    └─────────────────────┘    │
└─────────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    Watchdog (every 5 minutes)                        │
│         Runs full reconciliation cycle for self-healing             │
└─────────────────────────────────────────────────────────────────────┘
```

---

## App Graph

### Purpose

The App Graph represents relationships between applications and enables:
- Dependency resolution (what needs to be installed together)
- Integration planning (what can connect to what)
- Removal safety (what would break if we remove this)

### Data Model

```go
// AppDefinition represents an application in the catalog
type AppDefinition struct {
    Name         string
    Image        string
    Integrations map[string]Integration
}

// Integration defines how an app connects to other apps
type Integration struct {
    Required   bool            // Must have at least one source
    Multi      bool            // Can have multiple sources
    Compatible []CompatibleApp // Apps that can fulfill this integration
}

// CompatibleApp defines a specific app that can fulfill an integration
type CompatibleApp struct {
    App      string // App name (e.g., "qbittorrent")
    Default  bool   // Suggested default choice
    Category string // For multi-category integrations (e.g., "movies", "tv")
}
```

### Example: Radarr Definition

```yaml
name: radarr
image: lscr.io/linuxserver/radarr:latest
integrations:
  downloadClient:
    required: true
    multi: false
    compatible:
      - app: qbittorrent
        default: true
      - app: transmission
      - app: deluge
  indexer:
    required: false
    multi: true
    compatible:
      - app: prowlarr
```

### Graph Operations

#### PlanInstall(appName)

Computes what's needed to install an app:

```go
type InstallPlan struct {
    CanInstall bool
    Blockers   []string      // Why it can't be installed
    Choices    []Choice      // User decisions needed
    AutoConfig []AutoConfig  // Auto-resolved integrations
    Dependents []ConfigTask  // Installed apps that will integrate with this
}
```

**Logic:**
1. For each integration on the app:
   - 0 installed + required → User must choose from compatible list
   - 0 installed + optional → Skip (user can add later)
   - 1 installed → Auto-configure
   - N installed + multi=false → User must choose one
   - N installed + multi=true → Auto-configure all

#### PlanRemove(appName)

Computes if an app can be safely removed:

```go
type RemovePlan struct {
    CanRemove       bool
    Blockers        []string  // Required integrations with no alternatives
    WillUnconfigure []string  // Apps that will lose this integration
}
```

**Logic:**
1. Find all installed apps that depend on this app
2. For each dependent's required integration:
   - Check if alternatives exist
   - Block removal if no alternatives

---

## Nix Generation

### Purpose

Nix generation is the **source of truth** for what apps are installed. The host-agent generates `.nix` files that NixOS evaluates and applies atomically.

### Transaction Model

```go
type Transaction struct {
    Apps map[string]AppConfig
}

type AppConfig struct {
    Name         string
    Enabled      bool
    Integrations map[string]string  // integration -> source app
}
```

### Generated Output

```nix
# Generated by Bloud - DO NOT EDIT MANUALLY
{ config, lib, pkgs, ... }:

{
  imports = [
    ../../apps/radarr
    ../../apps/qbittorrent
  ];

  bloud.apps.radarr = {
    enable = true;
    integrations = {
      downloadClient = "qbittorrent";
    };
  };
}
```

### Flow

1. **Load Current State** - Parse existing `apps.nix`
2. **Build Transaction** - Add/remove/modify apps
3. **Generate Config** - Write new `apps.nix`
4. **Atomic Apply** - `nixos-rebuild switch`
5. **Rollback on Failure** - NixOS handles this automatically

### Why Nix?

- **Atomic** - All-or-nothing changes, no partial states
- **Rollback** - One command to revert any change
- **Reproducible** - Same config = same system
- **Declarative** - Describe what, not how
- **No Manual Podman** - NixOS manages containers via systemd

---

## Runtime Configurators

### Purpose

After Nix deploys containers, configurators ensure apps are properly configured and integrated. Each configurator implements a three-phase lifecycle that runs during every reconciliation cycle for self-healing.

### The Configurator Interface

```go
// Configurator handles app-specific configuration
// All methods must be idempotent - safe to call repeatedly
type Configurator interface {
    // Name returns the app name this configurator handles
    Name() string

    // PreStart runs before/after container starts
    // For: config files, directories, certificates, initial setup
    // Called every reconciliation - must be idempotent
    PreStart(ctx context.Context, state *AppState) error

    // HealthCheck waits for the app to be ready for configuration
    // For: waiting for web UI, API, database to accept connections
    // Returns nil when ready, error on timeout
    HealthCheck(ctx context.Context) error

    // PostStart runs after container is healthy
    // For: API calls, integrations, runtime configuration
    // Called every reconciliation - must be idempotent
    PostStart(ctx context.Context, state *AppState) error
}

// AppState contains everything a configurator needs
type AppState struct {
    Name         string              // App name
    DataPath     string              // ~/.local/share/bloud/<app>
    ConfigPath   string              // App-specific config location
    Port         int                 // Host port
    Integrations map[string][]string // integration name -> source app names
    Options      map[string]any      // App-specific options from Nix config
}
```

### Lifecycle Phases

Configurators are integrated directly into the systemd service lifecycle via `ExecStartPre` and `ExecStartPost`:

```
┌─────────────────────────────────────────────────────────────┐
│  ExecStartPre: bloud-agent configure prestart qbittorrent   │
│  • Ensure config files exist with correct content           │
│  • Create required directories                              │
│  • Generate certificates if needed                          │
│  • Container won't start if this fails                      │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────────┐
│  Container starts (podman)                                   │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────────┐
│  ExecStartPost: bloud-agent configure poststart qbittorrent │
│  • Wait for app to respond (HealthCheck)                    │
│  • Configure app settings via API                           │
│  • Set up integrations (download clients, indexers, etc.)   │
└─────────────────────────────────────────────────────────────┘
```

**Systemd integration ensures:**
- PreStart runs BEFORE container starts (config files ready for volume mounts)
- Container won't start if PreStart fails
- PostStart runs AFTER container starts (waits for health, then configures)
- Watchdog still runs every 5 minutes for self-healing

### Configuration Strategies

Different apps require different approaches in each lifecycle phase:

| Strategy | PreStart | PostStart | Example Apps |
|----------|----------|-----------|--------------|
| **Config file** | Write/update config file | - | qBittorrent, Prometheus |
| **API-based** | Ensure directories exist | Configure via REST API | Radarr, Sonarr, Prowlarr |
| **Hybrid** | Write initial config | Fine-tune via API | Authentik, Jellyfin |
| **Nix-only** | No configurator needed | No configurator needed | Postgres, Redis, Traefik |

### Example: qBittorrent Configurator

qBittorrent reads config on startup but also has an API. We use PreStart to ensure correct initial config:

```go
type QBitConfigurator struct {
    httpClient *http.Client
}

func (c *QBitConfigurator) Name() string {
    return "qbittorrent"
}

func (c *QBitConfigurator) PreStart(ctx context.Context, state *AppState) error {
    configPath := filepath.Join(state.DataPath, "config/qBittorrent/qBittorrent.conf")

    // Load existing config - preserves user's settings
    config, err := ini.Load(configPath)
    if err != nil {
        config = ini.Empty()  // Fresh install
    }

    // Only set keys Bloud needs to control (for iframe embedding)
    // User's other settings (download paths, speed limits, etc.) are preserved
    section := config.Section("Preferences")
    section.Key("WebUI\\HostHeaderValidation").SetValue("false")
    section.Key("WebUI\\CSRFProtection").SetValue("false")
    section.Key("WebUI\\ClickjackingProtection").SetValue("false")
    section.Key("WebUI\\AuthSubnetWhitelistEnabled").SetValue("true")
    section.Key("WebUI\\AuthSubnetWhitelist").SetValue("10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16")

    return config.SaveTo(configPath)
}

func (c *QBitConfigurator) HealthCheck(ctx context.Context) error {
    return waitForHTTP(ctx, "http://localhost:8080/api/v2/app/version", 30*time.Second)
}

func (c *QBitConfigurator) PostStart(ctx context.Context, state *AppState) error {
    // qBittorrent is a source app - it doesn't consume integrations
    // Nothing to configure via API
    return nil
}
```

### Example: Radarr Configurator

Radarr is configured entirely via API after startup:

```go
type RadarrConfigurator struct {
    httpClient *http.Client
    baseURL    string
    apiKey     string
}

func (c *RadarrConfigurator) Name() string {
    return "radarr"
}

func (c *RadarrConfigurator) PreStart(ctx context.Context, state *AppState) error {
    // Ensure data directories exist
    dirs := []string{
        filepath.Join(state.DataPath, "config"),
        filepath.Join(state.DataPath, "movies"),
    }
    for _, dir := range dirs {
        if err := os.MkdirAll(dir, 0755); err != nil {
            return err
        }
    }
    return nil
}

func (c *RadarrConfigurator) HealthCheck(ctx context.Context) error {
    return waitForHTTP(ctx, c.baseURL+"/api/v3/system/status", 60*time.Second)
}

func (c *RadarrConfigurator) PostStart(ctx context.Context, state *AppState) error {
    // Configure each integration
    for integration, sources := range state.Integrations {
        switch integration {
        case "downloadClient":
            if err := c.ensureDownloadClients(ctx, sources); err != nil {
                return err
            }
        case "indexer":
            if err := c.ensureIndexers(ctx, sources); err != nil {
                return err
            }
        }
    }
    return nil
}

// ensureDownloadClients ensures exactly these download clients exist
func (c *RadarrConfigurator) ensureDownloadClients(ctx context.Context, sources []string) error {
    // Get current download clients
    current, err := c.getDownloadClients(ctx)
    if err != nil {
        return err
    }

    desired := make(map[string]bool)
    for _, s := range sources {
        desired[s] = true
    }

    // Remove clients that shouldn't exist (Bloud-managed only)
    for _, client := range current {
        if strings.HasPrefix(client.Name, "Bloud: ") && !desired[client.Source] {
            c.deleteDownloadClient(ctx, client.ID)
        }
    }

    // Add/update clients that should exist
    for _, source := range sources {
        c.upsertDownloadClient(ctx, source)
    }

    return nil
}
```

### Example: App Without Configurator

Some apps are fully configured via Nix (environment variables, mounted configs):

```nix
# Postgres - no runtime configurator needed
# All configuration via environment variables and Nix options
mkBloudApp {
  name = "postgres";
  image = "postgres:16";
  environment = cfg: {
    POSTGRES_USER = cfg.user;
    POSTGRES_PASSWORD = cfg.password;
    POSTGRES_DB = cfg.database;
  };
  # No configure.go needed - Nix handles everything
}
```

### Idempotency Requirements

Every method must be safe to call repeatedly:

```go
// GOOD: Idempotent - writes same content every time
func (c *QBitConfigurator) PreStart(ctx context.Context, state *AppState) error {
    return writeConfig(path, buildConfig(state))
}

// GOOD: Idempotent - checks existence before creating
func (c *RadarrConfigurator) ensureDownloadClients(ctx context.Context, sources []string) error {
    current := c.getCurrentClients(ctx)
    for _, source := range sources {
        if !current.Has(source) {
            c.createClient(ctx, source)
        }
    }
    return nil
}

// BAD: Not idempotent - appends on every call
func (c *BadConfigurator) PreStart(ctx context.Context, state *AppState) error {
    f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
    f.WriteString("new line\n")  // Grows forever!
    return nil
}
```

### Minimal Field Configuration

Configurators only set fields required for the integration to work:

```go
// Only connection fields - let app defaults handle the rest
downloadClient := DownloadClient{
    Name:     "Bloud: qBittorrent",
    Host:     "qbittorrent",           // Container hostname
    Port:     8080,                     // Container port
    Username: "admin",                  // Default credentials
    Password: "adminadmin",
    // NOT setting: category, priority, removeCompleted, etc.
}
```

**Rationale:**
- Minimal footprint - just wire up the connection
- App defaults handle everything else
- Less likely to conflict with app updates
- User can customize non-Bloud-managed settings

### Configuration Ownership (Future)

**Core Principle: Bloud manages its settings, users manage theirs via app UIs.**

Users configure apps through their native web UIs - changing download paths, adding quality profiles, tweaking settings. These get persisted to config files or databases. Bloud should preserve these while ensuring our specific configurations are correct.

**For now:** Configurators can use simple approaches. As we encounter conflicts with user settings, we'll implement proper merge strategies:
- API resources: Use `"Bloud: "` prefix to identify our resources
- Config files: Merge specific keys rather than overwriting entire files

### CLI Interface

The host-agent exposes a `configure` subcommand for systemd integration:

```bash
# Run PreStart for an app (called by ExecStartPre)
bloud-agent configure prestart <app-name>

# Run PostStart for an app (called by ExecStartPost)
# Includes HealthCheck - waits for app to be ready before configuring
bloud-agent configure poststart <app-name>

# Run full reconciliation for all apps (manual trigger)
bloud-agent configure reconcile
```

**Exit codes:**
- `0` - Success
- `1` - Configuration failed (container should not start for prestart)

**Generated systemd service:**

```nix
# In nixos/lib/podman-service.nix or bloud-app.nix
systemd.user.services."podman-${name}" = {
  serviceConfig = {
    ExecStartPre = "${bloud-agent}/bin/bloud-agent configure prestart ${name}";
    ExecStart = "${pkgs.podman}/bin/podman run ...";
    ExecStartPost = "${bloud-agent}/bin/bloud-agent configure poststart ${name}";
  };
};
```

---

## Orchestration Flow

### Execution Order

Configurators execute in **dependency order** (breadth-first from leaves). Apps that provide integrations must be configured before apps that consume them.

```
Example dependency graph:

    Jellyseerr
       ↓
   ┌───┴───┐
   ▼       ▼
 Radarr  Sonarr
   │       │
   └───┬───┘
       ▼
  qBittorrent

Execution order (BFS from leaves):

  Level 0: qBittorrent    ← PreStart, HealthCheck, PostStart
  Level 1: Radarr, Sonarr ← PreStart, HealthCheck, PostStart (parallel)
  Level 2: Jellyseerr     ← PreStart, HealthCheck, PostStart
```

**Why this matters:**
- Radarr's API must be available before Jellyseerr can add it as a media server
- If Radarr isn't healthy yet, the Jellyseerr configuration would fail
- Each level completes before the next begins

**Parallel execution within levels:**
Apps at the same level have no dependencies on each other and can be configured in parallel (e.g., Radarr and Sonarr simultaneously).

### Install Flow

```
1. API Request: POST /api/apps/radarr/install
                {choices: {downloadClient: "qbittorrent"}}
                        │
                        ▼
2. Plan:        graph.PlanInstall("radarr")
                → Needs qbittorrent, user chose it
                        │
                        ▼
3. Transaction: {
                  radarr: {enabled: true, integrations: {downloadClient: "qbittorrent"}},
                  qbittorrent: {enabled: true}
                }
                        │
                        ▼
4. PreStart:    Run PreStart for qbittorrent, radarr
                → Config files created, directories ready
                        │
                        ▼
5. Generate:    Write apps.nix with imports and config
                        │
                        ▼
6. Rebuild:     nixos-rebuild switch (atomic)
                → Containers start via systemd
                        │
                        ▼
7. Configure:   For each app in dependency order:
                  → HealthCheck (wait for ready)
                  → PostStart (API configuration)
                        │
                        ▼
8. Done:        Return success
```

### Uninstall Flow

```
1. API Request: POST /api/apps/qbittorrent/uninstall
                        │
                        ▼
2. Plan:        graph.PlanRemove("qbittorrent")
                → WillUnconfigure: ["radarr"]
                        │
                        ▼
3. Unconfigure: radarr.PostStart with empty downloadClient sources
                → Removes "Bloud: qBittorrent" from Radarr
                        │
                        ▼
4. Transaction: {qbittorrent: {enabled: false}, ...}
                        │
                        ▼
5. Generate:    Update apps.nix
                        │
                        ▼
6. Rebuild:     nixos-rebuild switch (atomic)
                        │
                        ▼
7. Done:        Container stopped and removed
```

### Reconciliation Loop

The core self-healing mechanism. Runs on triggers and every 5 minutes via watchdog:

```go
func (o *Orchestrator) Reconcile(ctx context.Context) error {
    desired := o.loadDesiredState() // from apps.nix

    // Phase 1: PreStart for all apps
    // Ensures config files and directories are correct
    for _, app := range desired.Apps {
        if cfg := o.getConfigurator(app.Name); cfg != nil {
            if err := cfg.PreStart(ctx, app.State); err != nil {
                o.log.Warn("prestart failed", "app", app.Name, "err", err)
                // Continue - don't block other apps
            }
        }
    }

    // Phase 2: Rebuild if Nix config changed
    if o.nixConfigChanged() {
        if err := o.rebuild(ctx); err != nil {
            return fmt.Errorf("rebuild failed: %w", err)
        }
    }

    // Phase 3: HealthCheck + PostStart in dependency order
    for _, app := range o.topologicalSort(desired.Apps) {
        cfg := o.getConfigurator(app.Name)
        if cfg == nil {
            continue // No configurator for this app
        }

        // Wait for app to be ready
        if err := cfg.HealthCheck(ctx); err != nil {
            o.log.Warn("health check failed, skipping", "app", app.Name, "err", err)
            continue // Will retry next reconciliation
        }

        // Configure the app
        if err := cfg.PostStart(ctx, app.State); err != nil {
            o.log.Warn("poststart failed", "app", app.Name, "err", err)
            // Continue - will retry next reconciliation
        }
    }

    return nil
}
```

### Watchdog

The watchdog ensures continuous self-healing:

```go
func (o *Orchestrator) StartWatchdog(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            if err := o.Reconcile(ctx); err != nil {
                o.log.Error("watchdog reconcile failed", "err", err)
            }
        }
    }
}
```

### When Reconciliation Runs

| Trigger | Purpose |
|---------|---------|
| Agent startup | Recover from rollbacks, crashes, manual changes |
| After install/uninstall | Apply new configuration |
| After `nixos-rebuild` | Catch manual Nix changes |
| Manual API call | User-triggered repair |
| **Watchdog (every 5 min)** | Continuous self-healing |

---

## Version Management and Rollback

A core promise of Bloud is that users can **safely undo any change**. This is achieved through the combination of NixOS generations and idempotent reconciliation.

### The Challenge

Nix tracks system state in generations, but runtime configuration (API calls to apps) happens outside Nix:

```
Timeline:
─────────────────────────────────────────────────────────────────────
Gen 1: radarr + qbittorrent     │  Config: "Bloud: qBittorrent"
       ↓                        │
Gen 2: radarr + transmission    │  Config: "Bloud: Transmission"
       ↓                        │
Rollback to Gen 1               │  Config: ??? still has Transmission
─────────────────────────────────────────────────────────────────────
```

When `nixos-rebuild --rollback` runs:
- `apps.nix` reverts to the previous version
- Containers restart with old configuration
- But app databases (where Radarr stores download clients) aren't affected

### The Solution: Reconciliation After Rollback

The idempotent lifecycle model resolves this automatically:

```
1. Rollback:     User runs nixos-rebuild --rollback
                 apps.nix reverts to Gen 1 (radarr + qbittorrent)
                          │
                          ▼
2. Rebuild:      NixOS applies old config atomically
                 - qbittorrent container starts
                 - transmission container stops
                          │
                          ▼
3. Agent:        Host agent starts, begins reconciliation
                          │
                          ▼
4. PreStart:     Ensures config files match desired state
                          │
                          ▼
5. HealthCheck:  Waits for apps to be ready
                          │
                          ▼
6. PostStart:    radarr.PostStart with downloadClient: ["qbittorrent"]
                 - Removes Transmission download client
                 - Creates qBittorrent download client
                          │
                          ▼
7. Consistent:   Both deployment AND configuration match Gen 1
```

**Key insight:** The Nix config (`apps.nix`) is the source of truth for desired state. Reconciliation syncs runtime configuration to match.

### Rollback Scenarios

#### Scenario 1: App Change Broke Something

```bash
# User installed transmission, it's not working
# Rollback to when qbittorrent was configured

sudo nixos-rebuild --rollback switch

# Agent starts, runs reconciliation
# PreStart ensures qbittorrent config is correct
# PostStart removes Transmission from Radarr, adds qBittorrent
# System is back to working state
```

#### Scenario 2: Configuration Got Corrupted

```bash
# User accidentally deleted download client in Radarr UI
# Or config file got corrupted

# Wait up to 5 minutes for watchdog, or force immediately:
curl -X POST http://localhost:8080/api/reconcile

# PreStart rewrites config files
# PostStart recreates the download client via API
```

#### Scenario 3: Full System Restore

```bash
# Restore from backup to different hardware
# Nix config restored, but app databases are fresh

# Agent starts, runs reconciliation
# PreStart creates all config files
# PostStart configures all integrations from scratch
# System matches desired state without manual intervention
```

### What Gets Versioned vs. What Doesn't

| Component | Versioned? | Rollback Method |
|-----------|------------|-----------------|
| Container images | Yes (Nix) | `nixos-rebuild --rollback` |
| Container config (ports, volumes) | Yes (Nix) | `nixos-rebuild --rollback` |
| Config files (via PreStart) | Yes (via reconcile) | Rollback + reconcile |
| Integration wiring (via PostStart) | Yes (via reconcile) | Rollback + reconcile |
| App data (movies, shows) | No | Restic backup/restore |
| User customizations in apps | No | Restic backup/restore |
| Quality profiles, indexers | No | Restic backup/restore |

### API Endpoints

```bash
# Trigger manual reconciliation
POST /api/reconcile
Response: {
  "success": true,
  "reconciled": ["qbittorrent", "radarr", "sonarr"],
  "errors": []
}

# Check reconciliation status
GET /api/reconcile/status
Response: {
  "lastRun": "2024-01-15T10:30:00Z",
  "nextRun": "2024-01-15T10:35:00Z",
  "healthy": ["qbittorrent", "radarr"],
  "unhealthy": [],
  "errors": []
}
```

### Edge Cases

**App not ready after rollback:**
The reconciler runs HealthCheck before PostStart. If an app doesn't become healthy, PostStart is skipped. The watchdog will retry in 5 minutes.

**Manual changes get overwritten:**
Bloud fully manages config files and integrations. Any manual changes will be reset on next reconciliation. Use `unmanaged` mode if you need manual control.

**Container restart after PreStart:**
If PreStart changes a config file that requires container restart, the container may need to be restarted. For apps with APIs, prefer using PostStart to change settings dynamically.

**Database schema changes:**
If rolling back to an older app version with a different database schema, the app itself may have issues. This is outside Bloud's scope - the app handles its own migrations.

---

## Power User Escape Hatches

For users who want to bypass Bloud's configuration management:

### Per-Integration Override

```yaml
# In Bloud settings
overrides:
  radarr:
    downloadClient: unmanaged  # Bloud won't configure download clients
```

### Per-App Override

```yaml
overrides:
  radarr: unmanaged  # Bloud deploys only, user configures everything
```

### Global Deployment-Only Mode

```yaml
mode: deployment-only  # No runtime configuration at all
```

**Note:** In unmanaged mode, Bloud still deploys containers via Nix but doesn't run any configurator methods. User is responsible for all app configuration.

---

## FAQs

### Why the three-phase lifecycle (PreStart/HealthCheck/PostStart)?

Different configuration needs happen at different times:

- **PreStart**: Config files must exist before the container reads them
- **HealthCheck**: Can't configure an app that isn't ready yet
- **PostStart**: API calls require the app to be running

This separation makes each phase focused and testable.

### Why not configure everything through Nix?

Many self-hosted apps don't support declarative configuration:
- Radarr/Sonarr store settings in SQLite, configured via web UI/API
- No environment variables for "add download client"
- Would require patching apps or maintaining custom forks

Configurators bridge this gap by making API calls that simulate what a user would do in the UI.

### Will Bloud overwrite my custom settings?

Bloud aims to preserve user settings configured through app UIs. We use naming prefixes (like "Bloud: ") to identify our resources and avoid touching user-created ones. Detailed merge strategies will be implemented as needed.

### What if a configurator fails?

- Nix deployment already succeeded (containers running)
- Configuration failure is logged but doesn't roll back containers
- Watchdog will retry in 5 minutes
- User can manually configure via app UI or trigger reconciliation

### What if I manually change something Bloud configured?

Next reconciliation (within 5 minutes) will reset it to match the desired state. If you want manual control:
1. Set that integration to `unmanaged` in Bloud settings
2. Or set the whole app to `unmanaged`

### Why a 5-minute watchdog?

- Fast enough to catch issues quickly
- Slow enough to not waste resources
- Idempotent operations are cheap when nothing changed
- Provides continuous self-healing without manual intervention

### What about apps that don't have APIs?

Some apps are configured entirely through:
- Environment variables → Handled in Nix
- Config files → PreStart writes them
- First-run wizards → May require manual setup (documented)

For apps without APIs, PostStart may be empty or just verify settings.

### How do I debug configuration issues?

```bash
# Check reconciliation status
curl http://localhost:8080/api/reconcile/status

# Force reconciliation and see results
curl -X POST http://localhost:8080/api/reconcile

# Check app-specific status
curl http://localhost:8080/api/apps/radarr/status

# Check host-agent logs
journalctl --user -u bloud-host-agent -f
```

### What triggers reconciliation?

- **Watchdog**: Every 5 minutes (continuous self-healing)
- **Agent startup**: Recover from crashes, rollbacks
- **After install/uninstall**: Apply new configuration
- **After nixos-rebuild**: Catch manual Nix changes
- **Manual API call**: User-triggered repair

### Can I run Bloud without NixOS?

The current design assumes NixOS for deployment. Alternatives would require:
- Different deployment backend (Docker Compose, Podman Quadlet)
- Same configurator system works regardless of deployment method

### Does the watchdog cause unnecessary load?

No. All operations are idempotent:
- PreStart: Writes same config file (or no-ops if unchanged)
- HealthCheck: Single HTTP request
- PostStart: API calls that verify current state before changing

When nothing has changed, a reconciliation cycle is very lightweight.

---

## File Locations

```
apps/
├── qbittorrent/
│   ├── metadata.yaml      # App catalog info
│   ├── module.nix         # NixOS module (container definition) - future: auto-generated
│   └── configurator.go    # Configurator implementation
├── radarr/
│   ├── metadata.yaml
│   ├── module.nix
│   └── configurator.go
└── ...

services/host-agent/
├── pkg/configurator/          # Public API (importable by apps)
│   ├── interface.go           # Configurator interface definition
│   ├── registry.go            # Configurator registry
│   ├── health.go              # Common health check utilities
│   └── ini.go                 # INI file merge utility
├── internal/
│   ├── appconfig/
│   │   └── register.go        # Registers all app configurators
│   ├── catalog/
│   │   ├── graph.go           # App graph and dependency tracking
│   │   ├── plan.go            # Install/remove planning
│   │   └── types.go           # AppDefinition, Integration, etc.
│   ├── nixgen/
│   │   ├── generator.go       # Nix config generation
│   │   └── rebuilder.go       # nixos-rebuild wrapper
│   └── orchestrator/
│       ├── orchestrator.go    # Interface
│       ├── orchestrator_nix.go # NixOS implementation
│       └── reconcile.go       # Reconciler with level-based execution
└── ...

nixos/
├── lib/
│   ├── bloud-app.nix      # App module helper
│   └── podman-service.nix # Container service helper
└── generated/
    └── apps.nix           # Generated app configuration
```

---

## Future Work

### Auto-generate module.nix from metadata.yaml

**Goal:** Eliminate hand-written `module.nix` files entirely. The declarative container definition should come from `metadata.yaml`, with all imperative logic in `configurator.go`.

**Current state:**
```
apps/qbittorrent/
├── metadata.yaml      # App info, integrations, ports
├── module.nix         # Container definition (hand-written Nix)
└── configurator.go    # Runtime configuration (Go)
```

**Future state:**
```
apps/qbittorrent/
├── metadata.yaml      # App info, integrations, ports, container config
└── configurator.go    # All runtime logic (Go)
```

**How it would work:**

1. Extend `metadata.yaml` to include container configuration:
   ```yaml
   name: qbittorrent
   image: linuxserver/qbittorrent:latest
   port: 8086
   containerPort: 8080

   environment:
     PUID: "1000"
     PGID: "1000"
     TZ: "Etc/UTC"
     WEBUI_PORT: "8080"

   volumes:
     - source: config
       target: /config
     - source: downloads
       target: /downloads

   integrations:
     # ... existing integration config
   ```

2. Host-agent generates `module.nix` files at build/install time from metadata
3. All imperative logic (directories, config files, API calls) stays in `configurator.go`

**Benefits:**
- Single source of truth for app definitions
- No Nix knowledge required to add new apps
- Easier to validate and test app configurations
- Configurator handles all runtime complexity
