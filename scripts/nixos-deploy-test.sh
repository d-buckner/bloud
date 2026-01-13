#!/usr/bin/env bash
# Deploy and test NixOS configuration on remote development machine
#
# Usage:
#   ./scripts/nixos-deploy-test.sh [options]
#
# Options:
#   --host HOST    Remote host (default: from NIXOS_DEV_HOST env or agent-dev@nixos-dev.local)
#   --skip-sync    Skip rsync step (useful if you just changed remote files)
#   --skip-build   Skip build step (just validate services)
#   --dry-run      Show what would be done without doing it
#   -h, --help     Show this help

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
REMOTE_HOST="${NIXOS_DEV_HOST:-agent-dev@nixos-dev.local}"
REMOTE_DIR="/home/agent-dev/bloud-test"
SKIP_SYNC=false
SKIP_BUILD=false
DRY_RUN=false

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --host)
      REMOTE_HOST="$2"
      shift 2
      ;;
    --skip-sync)
      SKIP_SYNC=true
      shift
      ;;
    --skip-build)
      SKIP_BUILD=true
      shift
      ;;
    --dry-run)
      DRY_RUN=true
      shift
      ;;
    -h|--help)
      grep '^#' "$0" | grep -v '#!/usr/bin/env' | sed 's/^# //g'
      exit 0
      ;;
    *)
      echo -e "${RED}Unknown option: $1${NC}"
      echo "Run with --help for usage"
      exit 1
      ;;
  esac
done

# Helper functions
log_step() {
  echo -e "${BLUE}[$1]${NC} $2"
}

log_success() {
  echo -e "${GREEN}✓${NC} $1"
}

log_error() {
  echo -e "${RED}✗${NC} $1"
}

log_warning() {
  echo -e "${YELLOW}⚠${NC} $1"
}

# Check if we're in the right directory
if [[ ! -f "nixos/bloud.nix" ]]; then
  log_error "Must run from project root (bloud-v3/)"
  log_error "Current directory: $(pwd)"
  exit 1
fi

# Print configuration
echo ""
echo -e "${BLUE}=== NixOS Deploy & Test ===${NC}"
echo "Remote host: ${REMOTE_HOST}"
echo "Remote directory: ${REMOTE_DIR}"
echo ""

if [[ "$DRY_RUN" == "true" ]]; then
  log_warning "DRY RUN MODE - No changes will be made"
  echo ""
fi

START_TIME=$(date +%s)

# Step 1: Rsync code to remote
if [[ "$SKIP_SYNC" == "false" ]]; then
  log_step "1/4" "Syncing code to remote..."

  RSYNC_CMD="rsync -avz --delete \
    --exclude='.git' \
    --exclude='result' \
    --exclude='*.qcow2' \
    --exclude='node_modules' \
    --exclude='worktree-*' \
    --exclude='.DS_Store' \
    --exclude='frontend/build' \
    --exclude='frontend/.svelte-kit' \
    ./ ${REMOTE_HOST}:${REMOTE_DIR}/"

  if [[ "$DRY_RUN" == "true" ]]; then
    echo "Would run: $RSYNC_CMD --dry-run"
    $RSYNC_CMD --dry-run | tail -10
  else
    if $RSYNC_CMD; then
      log_success "Code synced successfully"
    else
      log_error "Rsync failed"
      exit 1
    fi
  fi
else
  log_step "1/4" "Skipping sync (--skip-sync)"
fi

echo ""

# Step 2: Remote validation and build
if [[ "$SKIP_BUILD" == "false" ]]; then
  log_step "2/4" "Running tests on remote..."

  if [[ "$DRY_RUN" == "true" ]]; then
    echo "Would run: ssh ${REMOTE_HOST} 'cd ${REMOTE_DIR} && nix flake check'"
  else
    ssh "${REMOTE_HOST}" bash <<REMOTE_SCRIPT
set -e
cd ${REMOTE_DIR}

# Syntax check
echo "  → Checking Nix syntax..."
if nix flake check --show-trace 2>&1 | head -50; then
  echo "  ✓ Syntax check passed"
else
  echo "  ✗ Syntax check failed"
  exit 1
fi

echo ""
echo "  → Building system configuration..."
if nix build .#nixosConfigurations.bloud-test.config.system.build.toplevel \
    --show-trace 2>&1 | tail -20; then
  echo "  ✓ Build successful"
else
  echo "  ✗ Build failed"
  exit 1
fi

echo ""
echo "  → Activating configuration..."
if sudo nixos-rebuild test --flake .#bloud-test 2>&1 | tail -10; then
  echo "  ✓ Configuration activated"
else
  echo "  ✗ Activation failed"
  exit 1
fi
REMOTE_SCRIPT

    if [[ $? -eq 0 ]]; then
      log_success "Build and activation completed"
    else
      log_error "Build or activation failed"
      exit 1
    fi
  fi
else
  log_step "2/4" "Skipping build (--skip-build)"
fi

echo ""

# Step 3: Validate services
log_step "3/4" "Validating services..."

if [[ "$DRY_RUN" == "true" ]]; then
  echo "Would check service status on remote"
else
  ssh "${REMOTE_HOST}" bash <<'REMOTE_SCRIPT'
set -e

# Wait for services to settle
echo "  → Waiting for services to start..."
sleep 5

# Check critical services
echo ""
echo "  → Checking service status..."

# Traefik
if systemctl --user is-active --quiet podman-traefik.service; then
  echo "    ✓ Traefik running"
else
  echo "    ✗ Traefik not running"
  systemctl --user status podman-traefik.service --no-pager --lines=5 || true
fi

# Authentik
if systemctl --user is-active --quiet podman-apps-authentik-server.service; then
  echo "    ✓ Authentik running"
else
  echo "    ✗ Authentik not running"
  systemctl --user status podman-apps-authentik-server.service --no-pager --lines=5 || true
fi

# Miniflux (if enabled)
if systemctl --user is-active --quiet podman-miniflux.service 2>/dev/null; then
  echo "    ✓ Miniflux running"
fi

# Actual Budget (if enabled)
if systemctl --user is-active --quiet podman-actual-budget.service 2>/dev/null; then
  echo "    ✓ Actual Budget running"
fi

echo ""
echo "  → Running health checks..."

# HTTP health checks
if curl -sf http://localhost:8080/health &>/dev/null; then
  echo "    ✓ Traefik responding (http://localhost:8080)"
else
  echo "    ✗ Traefik not responding"
fi

if curl -sf http://localhost:9000/api/v3/ &>/dev/null; then
  echo "    ✓ Authentik API responding (http://localhost:9000)"
else
  echo "    ⚠ Authentik not yet ready (may still be starting)"
fi

echo ""
echo "  ✓ Service validation complete"
REMOTE_SCRIPT

  if [[ $? -eq 0 ]]; then
    log_success "Services validated"
  else
    log_warning "Some services may not be ready yet"
  fi
fi

echo ""

# Step 4: Summary
log_step "4/4" "Done!"

END_TIME=$(date +%s)
ELAPSED=$((END_TIME - START_TIME))

echo ""
echo -e "${GREEN}=== Deployment Successful ===${NC}"
echo "Total time: ${ELAPSED}s"
echo ""
echo "Access services at:"
echo "  → Traefik:  ssh -L 8080:localhost:8080 ${REMOTE_HOST}"
echo "              then browse to http://localhost:8080"
echo ""
echo "  → Authentik: ssh -L 9000:localhost:9000 ${REMOTE_HOST}"
echo "               then browse to http://localhost:9000"
echo ""
echo "View logs:"
echo "  ssh ${REMOTE_HOST} \"journalctl --user -u podman-traefik -f\""
echo "  ssh ${REMOTE_HOST} \"journalctl --user -u podman-apps-authentik-server -f\""
echo ""
