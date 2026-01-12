#!/usr/bin/env bash
set -e

# Dev Sync - Rsync project to NixOS dev machine and trigger rebuild
#
# Usage:
#   ./scripts/dev-sync.sh                    # Sync and rebuild
#   ./scripts/dev-sync.sh --sync-only        # Just sync, no rebuild
#   ./scripts/dev-sync.sh --rebuild-only     # Just rebuild, no sync
#   ./scripts/dev-sync.sh --status           # Check rebuild status
#
# Environment variables:
#   NIXOS_DEV_HOST     - SSH host for rsync (default: nixos-dev.local)
#   NIXOS_DEV_PORT     - HTTP port for dev server (default: 9999)
#   NIXOS_DEV_PATH     - Remote path for code (default: /home/daniel/bloud-test)
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Configuration (override with env vars)
NIXOS_DEV_HOST="${NIXOS_DEV_HOST:-nixos-dev.local}"
NIXOS_DEV_PORT="${NIXOS_DEV_PORT:-9999}"
NIXOS_DEV_PATH="${NIXOS_DEV_PATH:-/home/daniel/bloud-test}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[OK]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

show_help() {
    cat << EOF
Bloud Dev Sync - Fast NixOS development workflow

Usage: $(basename "$0") [OPTIONS]

Options:
  --sync-only       Only sync files, don't trigger rebuild
  --rebuild-only    Only trigger rebuild, don't sync
  --status          Check current rebuild status
  --test            Use 'nixos-rebuild test' instead of switch
  --dry-run         Use 'nixos-rebuild dry-run' to preview changes
  -h, --help        Show this help message

Environment:
  NIXOS_DEV_HOST    SSH host (default: nixos-dev.local)
  NIXOS_DEV_PORT    Dev server HTTP port (default: 9999)
  NIXOS_DEV_PATH    Remote path (default: /home/daniel/bloud-test)

Examples:
  # Full workflow: sync and rebuild
  ./scripts/dev-sync.sh

  # Just sync code (for manual testing)
  ./scripts/dev-sync.sh --sync-only

  # Preview changes without applying
  ./scripts/dev-sync.sh --dry-run

  # Use a different host
  NIXOS_DEV_HOST=192.168.1.100 ./scripts/dev-sync.sh
EOF
}

sync_code() {
    log_info "Syncing code to ${NIXOS_DEV_HOST}:${NIXOS_DEV_PATH}"

    rsync -avz --delete \
        --exclude='.git' \
        --exclude='result' \
        --exclude='*.qcow2' \
        --exclude='node_modules' \
        --exclude='worktree-*' \
        --exclude='.claude' \
        --exclude='.turbo' \
        "$PROJECT_ROOT/" "${NIXOS_DEV_HOST}:${NIXOS_DEV_PATH}/"

    log_success "Code synced"
}

trigger_rebuild() {
    local endpoint="rebuild"
    local url="http://${NIXOS_DEV_HOST}:${NIXOS_DEV_PORT}/${endpoint}"

    log_info "Triggering rebuild at ${url}"
    echo ""

    # Stream the response
    curl -X POST --no-buffer -f "${url}" 2>&1 || {
        log_error "Rebuild failed or server not reachable"
        echo ""
        echo "Troubleshooting:"
        echo "  1. Is the dev server running? ssh ${NIXOS_DEV_HOST} systemctl status bloud-dev-server"
        echo "  2. Is port ${NIXOS_DEV_PORT} open? curl http://${NIXOS_DEV_HOST}:${NIXOS_DEV_PORT}/health"
        echo "  3. Check dev server logs: ssh ${NIXOS_DEV_HOST} journalctl -u bloud-dev-server -f"
        exit 1
    }
}

check_status() {
    local url="http://${NIXOS_DEV_HOST}:${NIXOS_DEV_PORT}/status"

    log_info "Checking rebuild status..."
    curl -sf "${url}" | python3 -m json.tool 2>/dev/null || {
        log_error "Could not reach dev server at ${url}"
        exit 1
    }
}

# Parse arguments
SYNC_ONLY=false
REBUILD_ONLY=false
CHECK_STATUS=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --sync-only)
            SYNC_ONLY=true
            shift
            ;;
        --rebuild-only)
            REBUILD_ONLY=true
            shift
            ;;
        --status)
            CHECK_STATUS=true
            shift
            ;;
        --test)
            # Would need to communicate to server, for now just note
            log_warn "--test not yet implemented, using default rebuild command"
            shift
            ;;
        --dry-run)
            log_warn "--dry-run not yet implemented, using default rebuild command"
            shift
            ;;
        -h|--help)
            show_help
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            show_help
            exit 1
            ;;
    esac
done

# Main
echo "╔════════════════════════════════════════════════════════════╗"
echo "║              Bloud Dev Sync                                ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo ""

if [[ "$CHECK_STATUS" == "true" ]]; then
    check_status
    exit 0
fi

START_TIME=$(date +%s)

if [[ "$REBUILD_ONLY" != "true" ]]; then
    sync_code
fi

if [[ "$SYNC_ONLY" != "true" ]]; then
    trigger_rebuild
fi

END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))

echo ""
log_success "Total time: ${DURATION}s"
