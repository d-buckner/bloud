# *Arr Stack Implementation Plan

> **Status: IMPLEMENTED** - All apps (Radarr, Sonarr, Prowlarr, Jellyfin, Jellyseerr) have NixOS modules.

## Overview

This document outlines the plan for adding Radarr, Sonarr, Prowlarr, Jellyfin, and Jellyseerr to Bloud. These apps form the core media automation stack.

## App Dependency Graph

```
Level 3:              Jellyseerr
                          │
                ┌─────────┼─────────┐
                ▼         ▼         ▼
Level 2:    Jellyfin   Radarr    Sonarr ◄── Prowlarr
                          │         │
                          └────┬────┘
                               ▼
Level 1:                  qBittorrent
```

**Integration relationships:**
- **Jellyseerr** → Jellyfin (media server), Radarr, Sonarr (sends media requests)
- **Prowlarr** → Radarr, Sonarr (optional; adds them as "Applications" to push indexers)
- **Radarr/Sonarr** → qBittorrent (sends downloads)
- **Jellyfin** → reads from `/movies` and `/tv` library folders

## Implementation Order

Based on dependencies, implement in this order:

0. **qBittorrent update** - Switch to shared `downloads:/downloads` mount
1. **Radarr** - Depends on qBittorrent
2. **Sonarr** - Same as Radarr
3. **Prowlarr** - Optional dependency on Radarr/Sonarr (adds them as sync targets)
4. **Jellyfin** - Media server (required by Jellyseerr)
5. **Jellyseerr** - Depends on Radarr, Sonarr, and Jellyfin

---

## Phase 1: Radarr

### App Profile

| Field | Value |
|-------|-------|
| Image | `linuxserver/radarr:latest` |
| Port | 7878 |
| Category | media |
| SSO | forward-auth |

### Integrations

```yaml
integrations:
  downloadClient:
    required: true
    multi: false
    compatible:
      - app: qbittorrent
        default: true
```

### Configurator Tasks

**PreStart:**
- Create directories: `radarr/config`, `movies`, `downloads`

**PostStart:**
- Wait for API (`/api/v3/system/status`)
- Extract API key from config.xml
- Ensure download client configured:
  - Check for existing "Bloud: qBittorrent"
  - Create/update if missing or wrong settings
  - Set category to `radarr` (label only, not a directory path)
- Root folder configuration:
  - Ensure `/movies` root folder exists

### API Examples

```bash
# Get API key (from config.xml after first start)
grep -oP '(?<=<ApiKey>)[^<]+' config.xml

# List download clients
GET /api/v3/downloadclient

# Add download client
POST /api/v3/downloadclient
{
  "name": "Bloud: qBittorrent",
  "implementation": "QBittorrent",
  "configContract": "QBittorrentSettings",
  "fields": [
    {"name": "host", "value": "qbittorrent"},
    {"name": "port", "value": 8080},
    {"name": "username", "value": "admin"},
    {"name": "password", "value": "adminadmin"},
    {"name": "movieCategory", "value": "radarr"}
  ]
}

# Add root folder
POST /api/v3/rootfolder
{"path": "/movies"}
```

---

## Phase 2: Sonarr

### App Profile

| Field | Value |
|-------|-------|
| Image | `linuxserver/sonarr:latest` |
| Port | 8989 |
| Category | media |
| SSO | forward-auth |

### Integrations

```yaml
integrations:
  downloadClient:
    required: true
    multi: false
    compatible:
      - app: qbittorrent
        default: true
```

### Configurator Tasks

Same pattern as Radarr:

**PreStart:**
- Create directories: `sonarr/config`, `tv`, `downloads`

**PostStart:**
- Wait for API (`/api/v3/system/status`)
- Extract API key from config.xml
- Ensure download client configured (category: `sonarr`)
- Ensure `/tv` root folder exists

### Notes

- Sonarr v4 API is nearly identical to Radarr v3
- Can share configurator code via common `*arr` helper functions

---

## Phase 3: Prowlarr

### App Profile

| Field | Value |
|-------|-------|
| Image | `linuxserver/prowlarr:latest` |
| Port | 9696 |
| Category | media |
| SSO | forward-auth |

### Integrations

```yaml
integrations:
  movieManager:
    required: false
    multi: false
    compatible:
      - app: radarr
  tvManager:
    required: false
    multi: false
    compatible:
      - app: sonarr
```

### Configurator Tasks

**PreStart:**
- Create config directory

**PostStart:**
- Wait for API to be ready (`/api/v1/health`)
- Extract API key from config.xml
- If Radarr installed: add as Application (needs Radarr's API key + URL)
- If Sonarr installed: add as Application (needs Sonarr's API key + URL)

### API Examples

```bash
# Get Prowlarr API key (from config.xml after first start)
grep -oP '(?<=<ApiKey>)[^<]+' config.xml

# List applications
GET /api/v1/applications

# Add Radarr as application
POST /api/v1/applications
{
  "name": "Bloud: Radarr",
  "implementation": "Radarr",
  "configContract": "RadarrSettings",
  "fields": [
    {"name": "prowlarrUrl", "value": "http://prowlarr:9696"},
    {"name": "baseUrl", "value": "http://radarr:7878"},
    {"name": "apiKey", "value": "<radarr-api-key>"},
    {"name": "syncCategories", "value": [2000, 2010, 2020, 2030, 2040, 2045, 2050, 2060]}
  ]
}
```

### Notes

- Prowlarr adds Radarr/Sonarr as "Applications" to sync indexers to them
- When user adds indexers in Prowlarr UI, they auto-sync to connected apps
- Indexers themselves are user-configured (require accounts/API keys from indexer sites)

---

## Phase 4: Jellyfin

### App Profile

| Field | Value |
|-------|-------|
| Image | `jellyfin/jellyfin:latest` |
| Port | 8096 |
| Category | media |
| SSO | native-oidc |

### Integrations

```yaml
integrations: {}
# Jellyfin is a source app - provides media server to Jellyseerr
# Reads from shared library folders
```

### Configurator Tasks

**PreStart:**
- Create directories: `jellyfin/config`, `jellyfin/cache`

**PostStart:**
- Wait for API to be ready
- Configure library folders if not already set:
  - Movies library → `/movies`
  - TV library → `/tv`

### Notes

- Jellyfin supports OIDC natively for SSO
- Needs read access to `/movies` and `/tv` for library scanning
- Hardware transcoding can be added later (requires device passthrough)

---

## Phase 5: Jellyseerr

### App Profile

| Field | Value |
|-------|-------|
| Image | `fallenbagel/jellyseerr:latest` |
| Port | 5055 |
| Category | media |
| SSO | native-oidc (if supported) or forward-auth |

### Integrations

```yaml
integrations:
  mediaServer:
    required: true
    multi: false
    compatible:
      - app: jellyfin
  movieManager:
    required: false
    multi: false
    compatible:
      - app: radarr
  tvManager:
    required: false
    multi: false
    compatible:
      - app: sonarr
```

### Configurator Tasks

**PreStart:**
- Create config directory

**PostStart:**
- Wait for API to be ready
- Check if initial setup completed
- If Radarr installed: configure as movie service
- If Sonarr installed: configure as TV service

### Notes

- Jellyseerr requires a media server (Jellyfin/Plex) - may need to implement Jellyfin first
- Has a setup wizard on first run that may need handling
- SSO support: Check if native OIDC is available, otherwise use forward-auth

---

## Shared Infrastructure

### Common *arr Configurator Helpers

Create `services/host-agent/pkg/configurator/arr/` with shared utilities:

```go
package arr

// GetAPIKey extracts API key from config.xml
func GetAPIKey(configPath string) (string, error)

// WaitForAPI polls until the *arr API responds
func WaitForAPI(ctx context.Context, baseURL, apiKey string) error

// EnsureDownloadClient creates or updates a download client
func EnsureDownloadClient(ctx context.Context, client *Client, dc DownloadClientConfig) error

// EnsureRootFolder creates a root folder if missing
func EnsureRootFolder(ctx context.Context, client *Client, path string) error

// Client wraps *arr API calls
type Client struct {
    BaseURL string
    APIKey  string
    HTTP    *http.Client
}
```

### API Key Sharing

Configurators that need another app's API key simply read that app's `config.xml` directly:

```go
// Prowlarr configurator - Radarr/Sonarr are optional
if radarrKey, err := arr.GetAPIKey("~/.local/share/bloud/radarr/config/config.xml"); err == nil {
    // Radarr is installed, add as Application
    prowlarr.AddApplication("radarr", radarrKey)
}
// No error if file doesn't exist - Radarr just isn't installed
```

No registry or shared state needed - all configurators run on the same host with filesystem access. Optional dependencies gracefully skip if the config doesn't exist.

### Shared Directory Structure

Bloud uses an opinionated shared directory structure:

```
~/.local/share/bloud/
├── downloads/                  # qBittorrent downloads here
├── movies/                     # Radarr imports here, Jellyfin reads
├── tv/                         # Sonarr imports here, Jellyfin reads
├── qbittorrent/config/
├── radarr/config/
├── sonarr/config/
├── prowlarr/config/
├── jellyfin/
│   ├── config/
│   └── cache/
└── jellyseerr/config/
```

**Key design decisions:**

1. **Shared downloads folder** - qBittorrent and *arr apps all see `/downloads`
2. **Separate library folders** - Radarr imports to `/movies`, Sonarr to `/tv`
3. **Categories as labels** - Radarr uses category `radarr`, Sonarr uses `sonarr` - just labels to identify ownership, not directory paths
4. **Atomic moves** - All under same host filesystem = instant moves (no copy+delete)

**Container volume mounts:**

| App | Mounts |
|-----|--------|
| qBittorrent | `downloads:/downloads`, `qbittorrent/config:/config` |
| Radarr | `downloads:/downloads`, `movies:/movies`, `radarr/config:/config` |
| Sonarr | `downloads:/downloads`, `tv:/tv`, `sonarr/config:/config` |
| Prowlarr | `prowlarr/config:/config` |
| Jellyfin | `movies:/movies:ro`, `tv:/tv:ro`, `jellyfin/config:/config`, `jellyfin/cache:/cache` |
| Jellyseerr | `jellyseerr/config:/config` |

**Prerequisite:** Update qBittorrent before implementing *arr stack:
- Change volume mount to shared `downloads:/downloads`

---

## Testing Checklist

### Per-App Tests

- [ ] Container starts successfully
- [ ] Health check passes
- [ ] SSO login works (forward-auth)
- [ ] API key extraction works
- [ ] Configurator creates required resources

### Integration Tests

- [ ] Radarr can send downloads to qBittorrent
- [ ] Sonarr can send downloads to qBittorrent
- [ ] Prowlarr adds Radarr as Application
- [ ] Prowlarr adds Sonarr as Application
- [ ] Prowlarr syncs indexers to connected apps
- [ ] Jellyseerr can create requests in Radarr
- [ ] Jellyseerr can create requests in Sonarr

### Reconciliation Tests

- [ ] Removed download client gets recreated
- [ ] Changed settings get corrected
- [ ] New app installation triggers dependent reconfig

---

## Estimated Effort

| App | Module.nix | Configurator | Testing | Total |
|-----|-----------|--------------|---------|-------|
| qBittorrent update | Simple | Low | Low | ~1h |
| Radarr | Simple | Medium (download client, root folder) | Medium | ~4h |
| Sonarr | Simple | Low (reuse Radarr code) | Medium | ~2h |
| Prowlarr | Simple | Medium (app sync, API key extraction) | Medium | ~4h |
| Jellyfin | Simple | Medium (library setup) | Medium | ~3h |
| Jellyseerr | Simple | Medium (setup wizard) | Medium | ~4h |
| **Shared code** | - | Medium (arr helpers) | - | ~3h |

**Total: ~21 hours**
