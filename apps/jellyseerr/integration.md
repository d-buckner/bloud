# Jellyseerr - Bloud Integration

## Port & Network

- **Port:** 5055
- **Network:** `apps-net`
- **Category:** Media

## SSO Integration

Uses forward auth (Traefik middleware):
```yaml
sso:
  strategy: forward-auth
```

## Dependencies

Jellyseerr integrates with multiple apps:

```yaml
integrations:
  mediaServer:
    required: true
    compatible: [jellyfin]
  movieManager:
    required: false
    compatible: [radarr]
  tvManager:
    required: false
    compatible: [sonarr]
```

**Dependency graph:**
```
Jellyfin (required) ◄── Jellyseerr
Radarr (optional)   ◄──┘
Sonarr (optional)   ◄──┘
```

## Health Check

- **Endpoint:** `/api/v1/status`
- **Interval:** 5s
- **Timeout:** 60s

## Configuration

Jellyseerr requires manual initial setup to connect to:
1. Jellyfin server (for user auth and media library)
2. Radarr (for movie requests)
3. Sonarr (for TV requests)

Future: Configurator will automate this setup via Jellyseerr's API.
