# AdGuard Home - Bloud Integration

## Port & Network
- **Web Interface:** 3080
- **DNS Server:** 53
- **Network:** `host` (not `apps-net`)
- **Data:** `~/.local/share/bloud/adguard-home/{work,conf}/`

## Host Networking

Uses `--network=host` because:
1. DNS must be accessible on port 53 from the host
2. Bridge networking conflicts with Podman's internal DNS (aardvark-dns)

## Unprivileged Port Binding

Requires sysctl to bind to port 53:
```nix
boot.kernel.sysctl."net.ipv4.ip_unprivileged_port_start" = 53;
```

## Iframe Embedding

- Route: `/embed/adguard-home`
- Prefix is stripped before forwarding
- `iframe-headers` middleware removes X-Frame-Options

## Traefik Routes

| Route | Priority | Service |
|-------|----------|---------|
| `/embed/adguard-home` | 100 | adguard-home |

DNS traffic (port 53/UDP) bypasses Traefik entirely.
