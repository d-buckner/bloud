# Lima VM Development Environment

This directory contains configuration for running a NixOS VM on macOS for full integration testing.

## Quick Start

```bash
# 1. Build the NixOS image (one-time, or after changing lima-image.nix)
./lima/build-image.sh

# 2. Start the VM
limactl delete bloud 2>/dev/null; limactl start --name=bloud lima/nixos.yaml

# 3. Wait for boot, then apply bloud config
./lima/dev rebuild

# 4. Start the dev environment
./lima/dev start
```

## Dev Commands

```bash
./lima/dev start           # Start host-agent + web dev server
./lima/dev stop            # Stop dev services
./lima/dev status          # Show what's running
./lima/dev logs            # Tail service logs
./lima/dev shell           # Interactive bash in VM
./lima/dev rebuild         # Rebuild NixOS config
./lima/dev install <app>   # Install an app via API
```

## Files

- `dev` - Unified CLI for all dev operations (start, stop, status, etc.)
- `nixos.yaml` - Lima VM configuration (CPU, memory, mounts, ports)
- `build-image.sh` - Builds NixOS image on nix-builder VM
- `dev-shell.sh` - Low-level SSH wrapper (used internally by `dev`)
- `start-dev.sh` - Dev server startup script (runs inside VM)

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                      macOS Host                         │
│                                                         │
│  ┌─────────────────────────────────────────────────┐   │
│  │              Lima VM (NixOS 24.11)               │   │
│  │                                                   │   │
│  │  ┌─────────────┐  ┌─────────────┐               │   │
│  │  │ host-agent  │  │   web UI    │               │   │
│  │  │  :3000      │  │   :5173     │               │   │
│  │  └─────────────┘  └─────────────┘               │   │
│  │                                                   │   │
│  │  ┌─────────────┐  ┌─────────────┐               │   │
│  │  │  postgres   │  │  miniflux   │  (apps)       │   │
│  │  │  container  │  │  container  │               │   │
│  │  └─────────────┘  └─────────────┘               │   │
│  │                                                   │   │
│  │  9p mount: ~/Projects/bloud-v3 → /home/bloud.linux/bloud-v3
│  └─────────────────────────────────────────────────┘   │
│                                                         │
│  Port forwards: 3000, 5173, 8085, etc.                 │
└─────────────────────────────────────────────────────────┘
```

## Port Forwards

| Port | Service |
|------|---------|
| 3000 | Host Agent API |
| 5173 | Svelte Dev Server |
| 8080 | Traefik Dashboard |
| 8085 | Miniflux |
| 9001 | Authentik |
| 5006 | Actual Budget |

## Common Tasks

### Rebuild NixOS config after changes
```bash
./lima/dev rebuild
```

### Check service status
```bash
./lima/dev shell "systemctl --user status podman-miniflux"
```

### View logs
```bash
./lima/dev shell "journalctl --user -u podman-miniflux -f"
```

### SSH into VM directly
```bash
./lima/dev shell
```

## Troubleshooting

### SSH not working
The VM uses password auth by default. Password for `bloud` user is `bloud`.

### 9p mount not appearing
The mount should auto-mount on boot. If not:
```bash
./lima/dev shell "sudo mount -t 9p -o trans=virtio,version=9p2000.L mount0 /home/bloud.linux/bloud-v3"
```

### Services not starting
Check if the network exists:
```bash
./lima/dev shell "podman network ls"
# If apps-net missing:
./lima/dev shell "systemctl --user restart podman-apps-network"
```

### Port forwarding not working
Lima guest agent may not be running. Services are still accessible via SSH tunnel or from within VM.
