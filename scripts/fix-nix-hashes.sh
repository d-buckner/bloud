#!/usr/bin/env bash
#
# Compute and fix the Nix build hashes for frontend (npm) and host-agent (Go).
#
# Run this on a Linux machine with Nix after the initial checkout.
# It builds each derivation, captures the correct hash from the error output,
# and patches the .nix files in place.
#
# Usage: ./scripts/fix-nix-hashes.sh

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

log()  { echo -e "${GREEN}[+]${NC} $*"; }
err()  { echo -e "${RED}[-]${NC} $*"; }

FRONTEND_NIX="nixos/packages/frontend.nix"
HOST_AGENT_NIX="nixos/packages/host-agent.nix"

fix_hash() {
  local nix_file="$1"
  local attr="$2"
  local label="$3"

  log "Building $label (expecting hash mismatch)..."
  # Build and capture the correct hash from the error
  local output
  if output=$(nix build "$attr" 2>&1); then
    log "$label already builds successfully — no hash fix needed"
    return 0
  fi

  local correct_hash
  correct_hash=$(echo "$output" | grep -oP 'got:\s+\Ksha256-[A-Za-z0-9+/=]+' | head -1)

  if [ -z "$correct_hash" ]; then
    err "Could not extract hash from build output for $label"
    echo "$output" | tail -20
    return 1
  fi

  log "Got hash: $correct_hash"
  # Replace fakeHash or existing sha256 placeholder
  sed -i "s|lib\.fakeHash|\"$correct_hash\"|" "$nix_file"
  sed -i "s|\"sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"|\"$correct_hash\"|" "$nix_file"

  log "Updated $nix_file"
}

# Fix frontend (npm) hash first since host-agent depends on it
fix_hash "$FRONTEND_NIX" ".#nixosConfigurations.iso.config.bloud.host-agent.package.frontend" "frontend (npm)"

# Fix host-agent (Go vendor) hash
fix_hash "$HOST_AGENT_NIX" ".#nixosConfigurations.iso.config.bloud.host-agent.package" "host-agent (Go)"

log "Done. Try building the ISO:"
echo "  nix build .#packages.x86_64-linux.iso"
