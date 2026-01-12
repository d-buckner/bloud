# Actual Budget - Bloud Integration

## Port & Network
- **Port:** 5006
- **Network:** `apps-net`
- **Data:** `~/.local/share/bloud/actual-budget/` → `/data`

## Cross-Origin Isolation (COOP/COEP)

Required for SharedArrayBuffer (WebAssembly SQLite):

| Header | Value |
|--------|-------|
| `Cross-Origin-Opener-Policy` | `same-origin` |
| `Cross-Origin-Embedder-Policy` | `require-corp` |

The `embed-isolation` middleware is skipped for this app since it defines custom COEP in `metadata.yaml`.

## Iframe Embedding

Does **not** support base path configuration. Traefik requires explicit routes for absolute asset paths:

| Route | Priority | Purpose |
|-------|----------|---------|
| `/embed/actual-budget` | 100 | Main app |
| `/static/*`, `/kcab/*`, `/locale/*`, `/data/*` | 99 | Static assets |
| `/sync/*`, `/account/*`, `/admin/*`, etc. | 98 | API endpoints |

Routes defined in `apps/traefik/module.nix`.

## SSO (Optional)

When Authentik is enabled:
```nix
ACTUAL_OPENID_DISCOVERY_URL = "http://localhost:9001/application/o/actual-budget/.well-known/openid-configuration"
ACTUAL_OPENID_CLIENT_ID = "actual-budget-client"
ACTUAL_OPENID_CLIENT_SECRET = "actual-budget-secret-change-in-production"
```

## Known Issues

- **Absolute Asset Paths:** App generates absolute paths, requiring explicit Traefik routes instead of prefix stripping.
