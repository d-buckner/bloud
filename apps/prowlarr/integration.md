# Prowlarr - Bloud Integration

## Port & Network

- **Port:** 9696
- **Network:** `apps-net`
- **Category:** Media

## SSO Integration

Uses forward auth (Traefik middleware):
```yaml
sso:
  strategy: forward-auth
```

## Role in Arr Stack

Prowlarr is an indexer manager that syncs indexers to other *arr apps:

```yaml
integrations:
  movieManager:
    required: false
    compatible: [radarr]
  tvManager:
    required: false
    compatible: [sonarr]
```

**Integration flow:**
```
Prowlarr ──► Radarr (pushes indexers)
         ──► Sonarr (pushes indexers)
```

Prowlarr is optional - Radarr/Sonarr can manage their own indexers, but Prowlarr centralizes this management.

## Health Check

- **Endpoint:** `/ping`
- **Interval:** 5s
- **Timeout:** 60s

## Configuration

Prowlarr needs to be configured with:
1. Indexers (torrent/usenet sources)
2. Applications (Radarr, Sonarr) to sync indexers to

Future: Configurator will automate the Radarr/Sonarr connection via API.
