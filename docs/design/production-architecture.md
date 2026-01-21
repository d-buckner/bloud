# Production Architecture

## Overview

In production, Bloud exposes a single port (80) with all traffic routed through Traefik to the host-agent. This simplifies the security model and eliminates dev-only services like Vite.

## Traffic Flow

```
Internet → Port 80 (socat) → Port 8080 (Traefik) → host-agent (3000)
                                    ↓
                              /embed/* → embedded apps (Jellyfin, etc.)
```

## Routing

All routes go through Traefik on port 8080:

| Route | Backend | Purpose |
|-------|---------|---------|
| `/api/*` | host-agent:3000 | REST API |
| `/auth/*` | host-agent:3000 | OAuth login/callback/logout |
| `/embed/*` | app containers | Embedded app iframes |
| `/outpost.goauthentik.io/*` | authentik:9001 | SSO integration |
| `/*` | host-agent:3000 | Static SPA (built frontend) |

## Port 80 Access

Port 80 requires root privileges. Since Traefik runs in rootless podman on port 8080, we use a system-level `socat` service to forward:

```
Port 80 (root) → Port 8080 (rootless Traefik)
```

This is configured as a systemd service:

```nix
systemd.services.port-80-forward = {
  description = "Forward port 80 to Traefik on 8080";
  wantedBy = [ "multi-user.target" ];
  after = [ "network.target" ];
  serviceConfig = {
    ExecStart = "${pkgs.socat}/bin/socat TCP-LISTEN:80,fork,reuseaddr TCP:127.0.0.1:8080";
    Restart = "always";
  };
};
```

## Frontend Serving

In production, the host-agent serves the pre-built SPA:

1. Build: `npm run build` in `services/host-agent/web/` outputs to `web/build/`
2. Host-agent checks for `web/build/` directory on startup
3. Serves static files with SPA fallback (missing files serve `index.html`)

See [routes.go:86-119](../services/host-agent/internal/api/routes.go#L86-L119) for implementation.

## Comparison: Dev vs Production

| Aspect | Development | Production |
|--------|-------------|------------|
| Frontend | Vite dev server (5173) | host-agent serves static build |
| Exposed ports | 80, 3000, 5173, 8080 | 80 only |
| Hot reload | Yes (Vite HMR) | No |
| Lima port forwards | Multiple | Port 80 only |

## Firewall Configuration

Production firewall should only allow:

```nix
networking.firewall.allowedTCPPorts = [
  22   # SSH (optional, for admin access)
  80   # HTTP via socat → Traefik
];
```

All other ports (3000, 5173, 8080, 9001, etc.) remain internal.

## Implementation Checklist

- [ ] Add `port-80-forward` systemd service to NixOS config
- [ ] Update Traefik `bloud-ui` service to point to host-agent (3000) instead of Vite (5173)
- [ ] Create production NixOS module (or add production mode toggle)
- [ ] Update Lima config to forward only port 80 for production testing
- [ ] Add frontend build step to deployment pipeline
- [ ] Restrict firewall to port 80 (and 22 if needed)

## Future: HTTPS

For HTTPS (port 443), the architecture extends to:

```
Internet → Port 443 (socat) → Port 8443 (Traefik with TLS) → backends
```

Traefik handles TLS termination with Let's Encrypt or provided certificates.
