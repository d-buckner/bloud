# Lima VM Configuration

This directory contains Lima VM configuration for NixOS development.

> **For development workflow**, see the main [README.md](../README.md).
> Use `./bloud start` from the project root.

## Files

- `nixos.yaml` - Lima VM configuration (dev environment)
- `test-nixos.yaml.template` - Lima VM configuration template (test environment)
- `build-image.sh` - Builds VM image
- `start-dev.sh` - Dev server startup script (runs inside VM)
- `start-test.sh` - Test server startup script (runs inside VM)
- `imgs/` - VM images directory

## Troubleshooting

### 9p mount not appearing

The mount should auto-mount on boot. If not:
```bash
./bloud shell "sudo mount -t 9p -o trans=virtio,version=9p2000.L mount0 /home/bloud.linux/bloud"
```

### Services not starting

Check if the network exists:
```bash
./bloud shell "podman network ls"
# If apps-net missing:
./bloud shell "systemctl --user restart podman-apps-network"
```

### Port forwarding not working

Lima guest agent may not be running. Services are still accessible via SSH tunnel.
Check status with `./bloud status`.
