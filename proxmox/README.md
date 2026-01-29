# Proxmox NixOS Template

Build and deploy NixOS VM templates for Proxmox VE.

## Quick Start

### 1. Build the image

```bash
# From project root, using your Lima nix-builder VM
./proxmox/build-image.sh

# Or if you have Nix on Linux
./proxmox/build-image.sh --local
```

### 2. Deploy to Proxmox

```bash
# Copy image to Proxmox
scp ./proxmox/imgs/*.vma.zst root@proxmox:/var/lib/vz/dump/

# Restore as VM (use any free VMID)
ssh root@proxmox 'qmrestore /var/lib/vz/dump/vzdump-qemu-nixos-*.vma.zst 100 --unique true'

# Boot and verify it works
ssh root@proxmox 'qm start 100'

# Convert to template
ssh root@proxmox 'qm template 100'
```

### 3. Clone VMs from template

```bash
# Full clone (independent disk)
qm clone 100 101 --name bloud-prod --full

# Set cloud-init options (optional - template has SSH key baked in)
qm set 101 --ciuser bloud --ipconfig0 ip=dhcp
# Or static IP:
qm set 101 --ipconfig0 ip=192.168.1.50/24,gw=192.168.1.1

# Start the VM
qm start 101
```

## Template Details

- **Username:** `bloud` (with sudo, passwordless)
- **SSH key:** Pre-configured for immediate access
- **Initial passwords:** `bloud` for bloud user, `nixos` for root (change after first login)
- **Cloud-init:** Enabled for per-VM network/SSH config via Proxmox
- **QEMU Guest Agent:** Enabled for Proxmox integration
- **Podman:** Pre-installed and configured for rootless containers

## After Cloning

SSH into the new VM and deploy your bloud configuration:

```bash
# SSH in (IP shown in Proxmox console or via qm guest cmd 101 network-get-interfaces)
ssh bloud@<vm-ip>

# Change passwords
passwd
sudo passwd root

# Clone bloud repo and deploy
git clone https://github.com/your/bloud.git
cd bloud
# ... deploy your services
```

## Customizing the Template

Edit `nixos/proxmox-image.nix` to change:
- Default packages
- User configuration
- Network settings
- VM resource defaults (cores, memory)

Then rebuild with `./proxmox/build-image.sh`.
