#!/usr/bin/env bash
# Bloud Test Integration Script
# Clean up all containers and data, then rebuild for fresh testing
#
# Usage: ./bloud-test-integration (do NOT run with sudo)

set -e

# Prevent running as root - this script must run as the normal user
if [ "$EUID" -eq 0 ]; then
  echo "ERROR: Do not run this script with sudo."
  echo "Run as: ./bloud-test-integration"
  echo ""
  echo "The script will call sudo internally for nixos-rebuild."
  exit 1
fi

echo "=== Bloud Test Integration - Clean Rebuild ==="
echo

# Stop all podman services
echo "→ Stopping all podman services..."
systemctl --user stop podman-*.service 2>/dev/null || true

# Wait for services to stop
sleep 2

# Remove all containers (force)
echo "→ Removing all containers..."
podman rm -af 2>/dev/null || true

# Remove all volumes
echo "→ Removing all volumes..."
podman volume rm -af 2>/dev/null || true

# Clean up data directory (use podman unshare for container-owned files)
echo "→ Cleaning up data directory..."
podman unshare rm -rf ~/.local/share/bloud-test
mkdir -p ~/.local/share/bloud-test

# Rebuild NixOS configuration
echo "→ Rebuilding NixOS configuration..."
sudo nixos-rebuild switch

echo
echo "=== Rebuild complete! Starting services... ==="
echo

# Reset failed states and oneshot services so they run fresh
echo "→ Resetting service states..."
systemctl --user reset-failed 2>/dev/null || true
systemctl --user stop miniflux-db-init.service 2>/dev/null || true

# Start all services - systemd handles dependency ordering and health checks
echo "→ Starting all services (systemd handles ordering)..."
systemctl --user start default.target

# Wait for services to initialize
echo "→ Waiting for services to initialize..."
sleep 15

# Show service status
echo
echo "→ Service status:"
systemctl --user list-units 'podman-*.service' --all --no-pager

echo
echo "✓ Done! Run 'bloud-test-integration' to verify services."
