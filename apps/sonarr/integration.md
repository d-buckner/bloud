# Sonarr - Bloud Integration

## Port & Network

- **Port:** 8989
- **Network:** `apps-net`
- **Category:** Media

## SSO Integration

Uses forward auth (Traefik middleware):
```yaml
sso:
  strategy: forward-auth
```

## Dependencies

```yaml
integrations:
  downloadClient:
    required: true
    compatible:
      - app: qbittorrent
        default: true
```

**Dependency graph:**
```
qBittorrent (required) ◄── Sonarr
```

## Health Check

- **Endpoint:** `/ping`
- **Interval:** 5s
- **Timeout:** 60s

## Storage Volumes

Sonarr uses shared volumes for media management:
- `/downloads` - Watches for completed downloads from qBittorrent
- `/tv` - TV series library (shared with Jellyfin)

## Upstream Apps

Sonarr can receive configuration from:
- **Prowlarr** - Pushes indexers automatically
- **Jellyseerr** - Sends TV series requests

## Configuration

Sonarr needs:
1. Download client connection (qBittorrent)
2. Root folder path (`/tv`)
3. Quality profiles
4. Indexers (manual or via Prowlarr)

Future: Configurator will automate qBittorrent connection via API.
