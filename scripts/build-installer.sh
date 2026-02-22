#!/usr/bin/env bash
# Build the installer artifacts (binary + frontend) needed before nix build.
# These are built outside the Nix sandbox using native toolchains.
# Run this before: nix build .#packages.x86_64-linux.iso

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$SCRIPT_DIR/.."

echo "==> Building installer Go binary..."
cd "$ROOT/services/installer"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -o "$ROOT/build/installer" \
  ./cmd/installer

echo "==> Building installer frontend..."
cd "$ROOT"
npm run build --workspace=services/installer/web

echo "==> Copying frontend build..."
rm -rf "$ROOT/build/installer-web"
cp -r "$ROOT/services/installer/web/build" "$ROOT/build/installer-web"

echo "==> Staging artifacts for Nix..."
git -C "$ROOT" add -f build/installer build/installer-web

echo ""
echo "Done. Artifacts ready at:"
echo "  build/installer"
echo "  build/installer-web/"
echo ""
echo "Now run: nix build .#packages.x86_64-linux.iso"
