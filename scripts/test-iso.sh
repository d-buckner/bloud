#!/usr/bin/env bash
#
# Bloud ISO Test Script for Proxmox
#
# Automates: build ISO -> create VM -> boot -> verify -> teardown
#
# Prerequisites:
#   - Nix installed on the Proxmox host
#   - This repo cloned on the Proxmox host
#   - Run from the repo root
#
# Usage:
#   ./scripts/test-iso.sh              # Full cycle: build, deploy, verify, teardown
#   ./scripts/test-iso.sh --keep       # Keep VM running after tests (for debugging)
#   ./scripts/test-iso.sh --skip-build # Reuse existing ISO
#   ./scripts/test-iso.sh --vmid 9999  # Use specific VM ID

set -euo pipefail

# Defaults
VMID=9999
KEEP=false
SKIP_BUILD=false
ISO_STORAGE="/var/lib/vz/template/iso"
VM_MEMORY=4096
VM_CORES=2
VM_NAME="bloud-test-iso"
SSH_PORT=22
SSH_USER="bloud"
SSH_PASS="bloud"
BOOT_TIMEOUT=180
SERVICE_TIMEOUT=120

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --keep)
      KEEP=true
      shift
      ;;
    --skip-build)
      SKIP_BUILD=true
      shift
      ;;
    --vmid)
      VMID="$2"
      shift 2
      ;;
    -h|--help)
      echo "Usage: $0 [--keep] [--skip-build] [--vmid <id>]"
      echo ""
      echo "Options:"
      echo "  --keep        Keep VM running after tests (for debugging)"
      echo "  --skip-build  Reuse existing ISO (skip nix build)"
      echo "  --vmid <id>   VM ID to use (default: 9999)"
      exit 0
      ;;
    *)
      echo "Unknown option: $1"
      exit 1
      ;;
  esac
done

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log()  { echo -e "${GREEN}[+]${NC} $*"; }
warn() { echo -e "${YELLOW}[!]${NC} $*"; }
err()  { echo -e "${RED}[-]${NC} $*"; }

PASSED=0
FAILED=0

check() {
  local name="$1"
  shift
  echo -n "  Checking $name... "
  if "$@" > /dev/null 2>&1; then
    echo -e "${GREEN}PASS${NC}"
    ((PASSED++))
  else
    echo -e "${RED}FAIL${NC}"
    ((FAILED++))
  fi
}

# Get VM IP address via QEMU guest agent
get_vm_ip() {
  qm guest cmd "$VMID" network-get-interfaces 2>/dev/null \
    | jq -r '.[].["ip-addresses"][]? | select(.["ip-address-type"] == "ipv4") | .["ip-address"]' \
    | grep -v '^127\.' \
    | head -1
}

ssh_cmd() {
  sshpass -p "$SSH_PASS" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=5 "$SSH_USER@$VM_IP" "$@"
}

cleanup() {
  if [ "$KEEP" = true ]; then
    warn "Keeping VM $VMID running (--keep). IP: ${VM_IP:-unknown}"
    warn "Teardown manually: qm stop $VMID && qm destroy $VMID --purge"
    return
  fi

  log "Tearing down VM $VMID..."
  qm stop "$VMID" 2>/dev/null || true
  sleep 3
  qm destroy "$VMID" --purge 2>/dev/null || true
  log "VM destroyed"
}

# ── Step 1: Build ISO ──────────────────────────────────────────────
if [ "$SKIP_BUILD" = false ]; then
  log "Building ISO (this may take a while)..."
  nix build .#packages.x86_64-linux.iso --out-link result-iso
  ISO_PATH=$(readlink -f result-iso/iso/*.iso)
  log "ISO built: $ISO_PATH"
else
  ISO_PATH=$(readlink -f result-iso/iso/*.iso 2>/dev/null || true)
  if [ -z "$ISO_PATH" ] || [ ! -f "$ISO_PATH" ]; then
    err "No existing ISO found. Run without --skip-build first."
    exit 1
  fi
  log "Reusing existing ISO: $ISO_PATH"
fi

# ── Step 2: Deploy ISO to Proxmox storage ──────────────────────────
ISO_NAME="bloud-test.iso"
log "Copying ISO to Proxmox storage..."
cp "$ISO_PATH" "$ISO_STORAGE/$ISO_NAME"

# ── Step 3: Create and start VM ────────────────────────────────────
# Clean up any existing VM with this ID
if qm status "$VMID" > /dev/null 2>&1; then
  warn "VM $VMID already exists, destroying..."
  qm stop "$VMID" 2>/dev/null || true
  sleep 3
  qm destroy "$VMID" --purge 2>/dev/null || true
fi

log "Creating VM $VMID..."
qm create "$VMID" \
  --name "$VM_NAME" \
  --memory "$VM_MEMORY" \
  --cores "$VM_CORES" \
  --ostype l26 \
  --cdrom "local:iso/$ISO_NAME" \
  --boot "order=ide2" \
  --net0 "virtio,bridge=vmbr0" \
  --agent enabled=1 \
  --serial0 socket

log "Starting VM..."
qm start "$VMID"

# Set up cleanup trap
trap cleanup EXIT

# ── Step 4: Wait for SSH ───────────────────────────────────────────
log "Waiting for VM to boot (timeout: ${BOOT_TIMEOUT}s)..."
VM_IP=""
for i in $(seq 1 "$BOOT_TIMEOUT"); do
  VM_IP=$(get_vm_ip)
  if [ -n "$VM_IP" ]; then
    # Try SSH connection
    if sshpass -p "$SSH_PASS" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
         -o ConnectTimeout=3 "$SSH_USER@$VM_IP" "true" 2>/dev/null; then
      break
    fi
  fi
  VM_IP=""
  if [ $((i % 10)) -eq 0 ]; then
    echo "  ... waiting ($i/${BOOT_TIMEOUT}s)"
  fi
  sleep 1
done

if [ -z "$VM_IP" ]; then
  err "Timeout: VM did not become reachable via SSH within ${BOOT_TIMEOUT}s"
  exit 1
fi

log "VM is up at $VM_IP"

# ── Step 5: Wait for services to stabilize ─────────────────────────
log "Waiting for Bloud services to start (timeout: ${SERVICE_TIMEOUT}s)..."
for i in $(seq 1 "$SERVICE_TIMEOUT"); do
  if ssh_cmd "curl -sf http://localhost:3000/api/health" 2>/dev/null; then
    break
  fi
  if [ $((i % 10)) -eq 0 ]; then
    echo "  ... waiting ($i/${SERVICE_TIMEOUT}s)"
  fi
  sleep 1
done

# ── Step 6: Run health checks ──────────────────────────────────────
echo ""
log "Running health checks..."
echo ""

check "host-agent service is active" \
  ssh_cmd "systemctl is-active bloud-host-agent.service"

check "bloud-apps target is active" \
  ssh_cmd "sudo -u bloud systemctl --user is-active bloud-apps.target"

check "host-agent API responds" \
  ssh_cmd "curl -sf http://localhost:3000/api/health"

check "traefik routes to host-agent" \
  ssh_cmd "curl -sf http://localhost:8080/api/health"

check "web UI is served" \
  ssh_cmd "curl -sf http://localhost:8080/ | grep -q 'html'"

check "podman containers are running" \
  ssh_cmd "podman ps --format '{{.Names}}' | grep -q apps"

check "mDNS is active" \
  ssh_cmd "systemctl is-active avahi-daemon.service"

# ── Step 7: Report ─────────────────────────────────────────────────
echo ""
echo "════════════════════════════════════════════════════════════"
if [ "$FAILED" -eq 0 ]; then
  echo -e "  ${GREEN}All $PASSED checks passed${NC}"
else
  echo -e "  ${GREEN}$PASSED passed${NC}, ${RED}$FAILED failed${NC}"
fi
echo "════════════════════════════════════════════════════════════"

if [ "$FAILED" -gt 0 ]; then
  exit 1
fi
