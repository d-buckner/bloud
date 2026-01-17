# Radarr - Bloud Integration

## Port & Network

- **Port:** 7878
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
qBittorrent (required) ◄── Radarr
```

## Health Check

- **Endpoint:** `/ping`
- **Interval:** 5s
- **Timeout:** 60s

## Storage Volumes

Radarr uses shared volumes for media management:
- `/downloads` - Watches for completed downloads from qBittorrent
- `/movies` - Movie library (shared with Jellyfin)

## Upstream Apps

Radarr can receive configuration from:
- **Prowlarr** - Pushes indexers automatically
- **Jellyseerr** - Sends movie requests

## Configuration

Radarr needs:
1. Download client connection (qBittorrent)
2. Root folder path (`/movies`)
3. Quality profiles
4. Indexers (manual or via Prowlarr)

Future: Configurator will automate qBittorrent connection via API.
