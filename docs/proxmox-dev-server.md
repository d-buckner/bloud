# Proxmox Dev Server Setup

Run Bloud natively on a Proxmox VM with full development tooling.

## Prerequisites

- Proxmox VE with a NixOS VM installed
- SSH access to the VM (see [Enable SSH](#enable-ssh) if needed)

The flake defaults to BIOS/GRUB boot on `/dev/sda`. If your VM uses EFI or a different disk, create a local override file (see [Boot Loader Override](#boot-loader-override)).

## Enable SSH

On your NixOS VM (via Proxmox console), edit `/etc/nixos/configuration.nix`:

```nix
services.openssh = {
  enable = true;
  settings.PasswordAuthentication = true;  # For initial setup
};
```

Apply and set a password:

```bash
sudo nixos-rebuild switch
passwd  # Set password for your user
```

Then from your local machine:

```bash
ssh-copy-id <user>@<vm-ip>
```

## Deployment

SSH into your VM and run:

```bash
# Clone the repo
git clone <your-repo-url> ~/bloud
cd ~/bloud

# Build the host-agent
cd services/host-agent
go build -o /tmp/host-agent ./cmd/host-agent

# Apply NixOS configuration
cd ~/bloud
sudo nixos-rebuild switch --flake .#dev-server --impure
```

This configures:
- `bloud` user with lingering (services start at boot)
- Rootless podman
- Core infrastructure: postgres, redis, traefik, authentik
- Dev packages: go, air, nodejs, tmux, etc.
- Firewall rules for all bloud ports

## Verification

```bash
# Check system service started user services
systemctl status bloud-user-services

# Check containers (as bloud user)
sudo -u bloud podman ps

# Or if logged in as bloud:
systemctl --user status bloud-apps.target
podman ps
```

Services should be accessible at:
- Traefik: http://VM_IP:8080
- Authentik: http://VM_IP:9001

## Development Workflow

1. SSH into VM or use VS Code Remote
2. Edit code in `~/bloud`
3. For Go changes:
   ```bash
   cd services/host-agent
   go build -o /tmp/host-agent ./cmd/host-agent
   # Restart affected services
   ```
4. For NixOS config changes:
   ```bash
   sudo nixos-rebuild switch --flake .#dev-server --impure
   ```

## Troubleshooting

### Services not starting

```bash
# Check bloud-user-services
journalctl -u bloud-user-services

# Check user services
sudo -u bloud journalctl --user -u bloud-apps.target
sudo -u bloud journalctl --user -u podman-apps-postgres.service
```

### Container issues

```bash
sudo -u bloud podman ps -a       # List all containers
sudo -u bloud podman logs <name>  # View logs
```

## Boot Loader Override

The flake defaults to BIOS/GRUB on `/dev/sda`. To override for EFI or different disk:

1. Check your disk: `lsblk`
2. Create `~/bloud/nixos/local.nix`:

```nix
{ lib, ... }:
{
  # For different BIOS disk:
  boot.loader.grub.device = lib.mkForce "/dev/vda";

  # Or for EFI boot:
  # boot.loader.grub.device = lib.mkForce "nodev";
  # boot.loader.grub.efiSupport = true;
  # boot.loader.grub.efiInstallAsRemovable = true;
}
```

3. Add to flake.nix modules list (local change, don't commit)
