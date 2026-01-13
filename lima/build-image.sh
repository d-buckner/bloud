#!/bin/bash
# Build NixOS Lima image for development
#
# This script builds the NixOS image on a Linux machine (or existing Lima VM).
# Run this when you update lima-image.nix or lima-init.nix.
#
# Usage:
#   ./lima/build-image.sh                    # Build on existing nix-builder VM
#   ./lima/build-image.sh --local            # Build locally (requires Linux)
#

set -e

PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMG_DIR="$PROJECT_ROOT/lima/imgs"

build_local() {
    echo "Building NixOS image locally..."
    cd "$PROJECT_ROOT"
    nix build .#lima-image -o result-lima-image

    mkdir -p "$IMG_DIR"
    cp "$(readlink result-lima-image)/nixos.img" "$IMG_DIR/nixos-24.11-lima.img"
    rm result-lima-image

    echo "Image built: $IMG_DIR/nixos-24.11-lima.img"
}

build_on_vm() {
    echo "Building NixOS image on nix-builder VM..."

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
    limactl shell nix-builder -- bash -c "cd /tmp/bloud && nix build .#lima-image -o result-lima-image"

    # Copy the result back
    mkdir -p "$IMG_DIR"
    limactl shell nix-builder -- cat /tmp/bloud/result-lima-image/nixos.img > "$IMG_DIR/nixos-24.11-lima.img"

    echo "Image built: $IMG_DIR/nixos-24.11-lima.img"
}

# Main
if [[ "$1" == "--local" ]]; then
    build_local
else
    build_on_vm
fi

echo ""
echo "To start/recreate the VM:"
echo "  limactl delete bloud 2>/dev/null; limactl start --name=bloud lima/nixos.yaml"
