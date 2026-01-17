# AFFiNE - Bloud Integration

## Port & Network

- **Port:** 3010
- **Network:** `apps-net`
- **Category:** Productivity
- **Database:** Shared PostgreSQL

## SSO Integration

Native OIDC support:
```yaml
sso:
  strategy: native-oidc
  callbackPath: /oauth/callback
  providerName: Bloud SSO
  userCreation: true
```

## Database Integration

```yaml
integrations:
  database:
    required: true
    compatible:
      - app: postgres
        default: true
```

Uses the shared PostgreSQL instance. Database created automatically by db-init service.

## Routing

```yaml
routing:
  stripPrefix: true
```

Traefik strips `/embed/affine` prefix before forwarding to app.

## Health Check

- **Endpoint:** `/`
- **Interval:** 5s
- **Timeout:** 120s (longer due to initial startup)

## Data Storage

AFFiNE stores:
- Document data in PostgreSQL
- Blob storage (images, attachments) in local filesystem
