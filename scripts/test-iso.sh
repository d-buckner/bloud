#!/usr/bin/env bash
#
# Bloud ISO Test Script for Proxmox (runs from macOS or any SSH client)
#
# Automates: deploy ISO -> create VM -> boot -> verify -> teardown
#
# Prerequisites:
#   - SSH access to Proxmox host (root)
#   - sshpass installed locally (brew install hudochenkov/sshpass/sshpass)
#   - jq installed on Proxmox host
#
# Usage:
#   ./scripts/test-iso.sh <iso-url-or-path>              # Deploy, verify, teardown
#   ./scripts/test-iso.sh <iso-url-or-path> --keep       # Keep VM for debugging
#   ./scripts/test-iso.sh --skip-deploy                   # Reuse existing VM
#   ./scripts/test-iso.sh --help

set -euo pipefail

# ── Configuration ─────────────────────────────────────────────────
PVE_HOST="${BLOUD_PVE_HOST:-root@192.168.0.62}"
VMID="${BLOUD_PVE_VMID:-9999}"
VM_MEMORY=4096
VM_CORES=2
VM_NAME="bloud-test-iso"
ISO_STORAGE="/var/lib/vz/template/iso"
ISO_FILENAME="bloud-test.iso"

SSH_USER="bloud"
SSH_PASS="bloud"
BOOT_TIMEOUT=180
SERVICE_TIMEOUT=300

# ── Argument parsing ─────────────────────────────────────────────
KEEP=false
SKIP_DEPLOY=false
ISO_SOURCE=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --keep)       KEEP=true; shift ;;
    --skip-deploy) SKIP_DEPLOY=true; shift ;;
    --pve-host)   PVE_HOST="$2"; shift 2 ;;
    --vmid)       VMID="$2"; shift 2 ;;
    -h|--help)
      echo "Usage: $0 [<iso-url-or-path>] [options]"
      echo ""
      echo "Arguments:"
      echo "  <iso-url-or-path>   URL to download or local path to ISO file"
      echo "                      If omitted, uses latest GitHub release"
      echo ""
      echo "Options:"
      echo "  --keep              Keep VM running after tests"
      echo "  --skip-deploy       Skip ISO deploy, reuse existing VM $VMID"
      echo "  --pve-host <host>   Proxmox SSH target (default: root@192.168.0.62)"
      echo "  --vmid <id>         VM ID (default: 9999)"
      echo ""
      echo "Environment:"
      echo "  BLOUD_PVE_HOST      Override Proxmox SSH target"
      echo "  BLOUD_PVE_VMID      Override VM ID"
      exit 0
      ;;
    -*)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
    *)
      ISO_SOURCE="$1"; shift ;;
  esac
done

# ── Helpers ───────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log()  { echo -e "${GREEN}[+]${NC} $*"; }
warn() { echo -e "${YELLOW}[!]${NC} $*"; }
err()  { echo -e "${RED}[-]${NC} $*"; }

PASSED=0
FAILED=0

pve() {
  ssh -o ConnectTimeout=10 "$PVE_HOST" "$@"
}

vm_ssh() {
  sshpass -p "$SSH_PASS" ssh \
    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=5 -o LogLevel=ERROR \
    "$SSH_USER@$VM_IP" "$@"
}

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

get_vm_ip() {
  pve "qm guest cmd $VMID network-get-interfaces 2>/dev/null \
    | jq -r '.[].\"ip-addresses\"[]? | select(.\"ip-address-type\" == \"ipv4\") | .\"ip-address\"' \
    | grep -v '^127\.' \
    | head -1" 2>/dev/null || true
}

cleanup() {
  if [ "$KEEP" = true ]; then
    warn "Keeping VM $VMID running (--keep). IP: ${VM_IP:-unknown}"
    warn "Teardown: ssh $PVE_HOST 'qm stop $VMID && qm destroy $VMID --purge'"
    return
  fi

  if [ "$SKIP_DEPLOY" = true ]; then
    return
  fi

  log "Tearing down VM $VMID..."
  pve "qm stop $VMID 2>/dev/null || true; sleep 3; qm destroy $VMID --purge 2>/dev/null || true"
  log "VM destroyed"
}

# ── Step 1: Deploy ISO ───────────────────────────────────────────
if [ "$SKIP_DEPLOY" = false ]; then

  # Resolve ISO source
  if [ -z "$ISO_SOURCE" ]; then
    log "Finding latest GitHub release..."
    ISO_SOURCE=$(gh release view --json assets --jq '.assets[] | select(.name | endswith(".iso")) | .url' 2>/dev/null || true)
    if [ -z "$ISO_SOURCE" ]; then
      err "No ISO source provided and no GitHub release found"
      exit 1
    fi
    log "Using latest release: $ISO_SOURCE"
  fi

  # Upload ISO to Proxmox
  if [[ "$ISO_SOURCE" == http* ]]; then
    log "Downloading ISO to Proxmox..."
    pve "curl -L -o '$ISO_STORAGE/$ISO_FILENAME' '$ISO_SOURCE'" 2>&1 | tail -1
  else
    log "Copying ISO to Proxmox..."
    scp "$ISO_SOURCE" "$PVE_HOST:$ISO_STORAGE/$ISO_FILENAME"
  fi

  # ── Step 2: Create and start VM ──────────────────────────────────
  # Clean up any existing VM
  if pve "qm status $VMID" > /dev/null 2>&1; then
    warn "VM $VMID already exists, destroying..."
    pve "qm stop $VMID 2>/dev/null || true; sleep 3; qm destroy $VMID --purge 2>/dev/null || true"
  fi

  log "Creating VM $VMID..."
  pve "qm create $VMID \
    --name $VM_NAME \
    --memory $VM_MEMORY \
    --cores $VM_CORES \
    --ostype l26 \
    --cdrom 'local:iso/$ISO_FILENAME' \
    --boot 'order=ide2' \
    --net0 'virtio,bridge=vmbr0' \
    --agent enabled=1 \
    --serial0 socket"

  log "Starting VM..."
  pve "qm start $VMID"
fi

trap cleanup EXIT

# ── Step 3: Wait for SSH ─────────────────────────────────────────
log "Waiting for VM to boot (timeout: ${BOOT_TIMEOUT}s)..."
VM_IP=""
for i in $(seq 1 "$BOOT_TIMEOUT"); do
  VM_IP=$(get_vm_ip)
  if [ -n "$VM_IP" ]; then
    if sshpass -p "$SSH_PASS" ssh \
         -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
         -o ConnectTimeout=3 -o LogLevel=ERROR \
         "$SSH_USER@$VM_IP" "true" 2>/dev/null; then
      break
    fi
  fi
  VM_IP=""
  if [ $((i % 15)) -eq 0 ]; then
    echo "  ... waiting ($i/${BOOT_TIMEOUT}s)"
  fi
  sleep 1
done

if [ -z "$VM_IP" ]; then
  err "Timeout: VM did not become reachable via SSH within ${BOOT_TIMEOUT}s"
  exit 1
fi

log "VM is up at $VM_IP"

# ── Step 4: Wait for services ────────────────────────────────────
log "Waiting for Bloud services to start (timeout: ${SERVICE_TIMEOUT}s)..."
for i in $(seq 1 "$SERVICE_TIMEOUT"); do
  if vm_ssh "curl -sf http://localhost:3000/api/health" 2>/dev/null; then
    log "Services are up (took ${i}s)"
    break
  fi
  if [ "$i" -eq "$SERVICE_TIMEOUT" ]; then
    warn "Timeout waiting for services — running checks anyway"
  fi
  if [ $((i % 15)) -eq 0 ]; then
    echo "  ... waiting ($i/${SERVICE_TIMEOUT}s)"
  fi
  sleep 1
done

# ── Step 5: Health checks ────────────────────────────────────────
echo ""
log "Running health checks..."
echo ""

check "bloud-pull-images completed" \
  vm_ssh "systemctl --user show bloud-pull-images.service -p ActiveState --value | grep -qE 'active|inactive'"

check "bloud-apps target is active" \
  vm_ssh "systemctl --user is-active bloud-apps.target"

check "host-agent service is active" \
  vm_ssh "systemctl is-active bloud-host-agent.service"

check "host-agent API responds" \
  vm_ssh "curl -sf http://localhost:3000/api/health"

check "traefik routes to host-agent" \
  vm_ssh "curl -sf http://localhost:8080/api/health"

check "web UI is served" \
  vm_ssh "curl -sf http://localhost:8080/ | grep -q html"

check "podman containers are running" \
  vm_ssh "podman ps --format '{{.Names}}' | grep -q apps"

check "mDNS is active" \
  vm_ssh "systemctl is-active avahi-daemon.service"

# ── Step 6: Container details ────────────────────────────────────
echo ""
log "Container status:"
vm_ssh "podman ps --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}'" 2>/dev/null || true

echo ""
log "Pull-images service log:"
vm_ssh "journalctl --user -u bloud-pull-images.service --no-pager -n 20" 2>/dev/null || true

# ── Step 7: Report ────────────────────────────────────────────────
echo ""
echo "════════════════════════════════════════════════════════════"
if [ "$FAILED" -eq 0 ]; then
  echo -e "  ${GREEN}All $PASSED checks passed${NC}"
else
  echo -e "  ${GREEN}$PASSED passed${NC}, ${RED}$FAILED failed${NC}"
fi
echo "  VM IP: $VM_IP"
echo "════════════════════════════════════════════════════════════"

if [ "$FAILED" -gt 0 ]; then
  exit 1
fi
