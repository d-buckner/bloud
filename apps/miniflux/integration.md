# Miniflux - Bloud Integration

## Port & Network
- **Port:** 8085
- **Network:** `host` (uses host networking for OIDC token exchange with Traefik)
- **Database:** Shared `apps-postgres`, database name: `miniflux`

## BASE_URL Configuration

Critical for iframe embedding:
```nix
BASE_URL = "http://localhost:8080/embed/miniflux";
```

When set:
1. Miniflux serves at `/embed/miniflux/` path
2. All generated links include this prefix
3. Traefik must **NOT** strip the prefix

## Routing: stripPrefix = false

```yaml
# metadata.yaml
routing:
  stripPrefix: false
```

Request flow:
- Request: `/embed/miniflux/feeds`
- Traefik forwards as-is
- Miniflux handles `/embed/miniflux/feeds`

## Database Initialization

Oneshot service `miniflux-db-init`:
```sql
CREATE DATABASE miniflux;
GRANT ALL PRIVILEGES ON DATABASE miniflux TO apps;
```

Runs before miniflux starts. Migrations run automatically (`RUN_MIGRATIONS=1`).

## Container Dependencies

```
apps-postgres ──► miniflux-db-init ──► miniflux
```

The db-init service waits for postgres before creating the miniflux database.

## SSO Integration

Native OIDC support:
```yaml
sso:
  strategy: native-oidc
  callbackPath: /oauth2/oidc/callback
  providerName: Bloud SSO
  userCreation: true
```

## Iframe Embedding

- Route: `/embed/miniflux` (priority 100)
- Middlewares: `iframe-headers`, `embed-isolation`
- COOP/COEP headers added for same-origin iframe access

## Health Check
- **Endpoint:** `/healthcheck`
