# Authentik - Bloud Integration

## System App

Marked as `isSystem: true` - core infrastructure, not user-facing.

## Port & Network
- **HTTP:** 9001
- **HTTPS:** 9443
- **Network:** `apps-net`

## Data Storage
- **PostgreSQL:** `~/.local/share/bloud/authentik-postgres/`
- **Media:** `~/.local/share/bloud/authentik-media/`
- **Templates:** `~/.local/share/bloud/authentik-templates/`
- **Certificates:** `~/.local/share/bloud/authentik-certs/`
- **Blueprints:** `~/.local/share/bloud/authentik-blueprints/`

## Container Architecture

| Container | Purpose |
|-----------|---------|
| `apps-postgres` | Shared PostgreSQL |
| `apps-redis` | Session cache/task queue |
| `apps-authentik-server` | Web server |
| `apps-authentik-worker` | Background tasks |

Dependencies:
```
apps-postgres ───┐
                 ├─► apps-authentik-server
apps-redis ──────┤
                 └─► apps-authentik-worker
```

## Special Requirements

- **userns:** `keep-id` for proper bind mount permissions
- **Health waits:** PostgreSQL (`pg_isready`), Redis (`redis-cli ping`)
- **Secret key:** Minimum 50 characters

## Blueprint System

Auto-generates OAuth2/OIDC configs for apps. Example:
`~/.local/share/bloud/authentik-blueprints/actual-budget.yaml`

## SSO Integration for Other Apps

1. Add to app's `metadata.yaml`:
   ```yaml
   sso:
     strategy: native-oidc
     callbackPath: /oauth2/callback
   ```

2. Check in NixOS module:
   ```nix
   authentikEnabled = config.bloud.apps.authentik.enable or false;
   ```

## Health Check
- **Endpoint:** `/-/health/live/`
- **Timeout:** 90 seconds (slow startup)
