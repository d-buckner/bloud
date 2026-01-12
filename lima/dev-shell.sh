#!/bin/bash
# Run a command in the bloud Lima VM as the bloud user
#
# Usage:
#   ./lima/dev-shell.sh <command>
#   ./lima/dev-shell.sh bash              # Interactive shell
#   ./lima/dev-shell.sh sudo nixos-rebuild switch --flake ...
#

set -e

VM_NAME="bloud"
VM_USER="bloud"
VM_PASSWORD="bloud"

# Check if VM is running
if ! limactl list 2>/dev/null | grep -q "$VM_NAME.*Running"; then
    echo "Error: VM '$VM_NAME' is not running"
    echo "Start it with: limactl start --name=$VM_NAME lima/nixos.yaml"
    exit 1
fi

# Get SSH port (limactl outputs newline-delimited JSON)
SSH_PORT=$(limactl list --format json 2>/dev/null | jq -r "select(.name==\"$VM_NAME\") | .sshLocalPort" | head -1)

if [ -z "$SSH_PORT" ] || [ "$SSH_PORT" = "null" ]; then
    echo "Error: Could not determine SSH port for VM"
    exit 1
fi

# Check if sshpass is available
if ! command -v sshpass &>/dev/null; then
    echo "Installing sshpass..."
    brew install hudochenkov/sshpass/sshpass
fi

# Run command via SSH
exec sshpass -p "$VM_PASSWORD" ssh \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -o PreferredAuthentications=password \
    -o PubkeyAuthentication=no \
    -o LogLevel=ERROR \
    -p "$SSH_PORT" \
    "$VM_USER@127.0.0.1" \
    "$@"
