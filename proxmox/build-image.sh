#!/bin/bash
# Build NixOS Proxmox VM template image
#
# This script builds the NixOS image for Proxmox VE.
# Run on a Linux machine with Nix installed (or use your Lima VM).
#
# Usage:
#   ./proxmox/build-image.sh                    # Build on existing nix-builder VM
#   ./proxmox/build-image.sh --local            # Build locally (requires Linux)
#
# After building, copy to Proxmox and restore:
#   scp ./proxmox/imgs/*.vma.zst root@proxmox:/var/lib/vz/dump/
#   qmrestore /var/lib/vz/dump/vzdump-qemu-nixos-*.vma.zst 100 --unique true
#   qm template 100  # Convert to template after verifying it boots
#

set -e

PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMG_DIR="$PROJECT_ROOT/proxmox/imgs"

build_local() {
    echo "Building NixOS Proxmox image locally..."
    cd "$PROJECT_ROOT"
    nix build .#proxmox-image -o result-proxmox-image

    mkdir -p "$IMG_DIR"
    cp "$(readlink result-proxmox-image)"/*.vma.zst "$IMG_DIR/"
    rm result-proxmox-image

    echo ""
    echo "Image built: $IMG_DIR/"
    ls -la "$IMG_DIR"/*.vma.zst
}

build_on_vm() {
    echo "Building NixOS Proxmox image on nix-builder VM..."

    # Check if nix-builder VM is running
    if ! limactl list | grep -q "nix-builder.*Running"; then
        echo "Starting nix-builder VM..."
        limactl start nix-builder 2>/dev/null || true
    fi

    # Copy project to VM's temp directory (to avoid 9p permission issues)
    echo "Copying project to VM..."
    limactl shell nix-builder -- rm -rf /tmp/bloud
    limactl shell nix-builder -- mkdir -p /tmp/bloud
    rsync -av --exclude='.git' --exclude='node_modules' --exclude='result*' \
        "$PROJECT_ROOT/" nix-builder:/tmp/bloud/

    # Build the image
    echo "Building image..."
    limactl shell nix-builder -- bash -c "cd /tmp/bloud && nix build .#proxmox-image -o result-proxmox-image"

    # Copy the result back
    mkdir -p "$IMG_DIR"
    limactl shell nix-builder -- bash -c "cat /tmp/bloud/result-proxmox-image/*.vma.zst" > "$IMG_DIR/nixos-proxmox.vma.zst"

    echo ""
    echo "Image built: $IMG_DIR/nixos-proxmox.vma.zst"
}

# Main
if [[ "$1" == "--local" ]]; then
    build_local
else
    build_on_vm
fi

echo ""
echo "To deploy to Proxmox:"
echo "  scp $IMG_DIR/*.vma.zst root@proxmox:/var/lib/vz/dump/"
echo "  ssh root@proxmox 'qmrestore /var/lib/vz/dump/vzdump-qemu-nixos-*.vma.zst 100 --unique true'"
echo "  ssh root@proxmox 'qm template 100'  # After verifying boot"
