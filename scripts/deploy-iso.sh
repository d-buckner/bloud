#!/usr/bin/env bash
# Deploy a Bloud ISO to Proxmox and boot a test VM.
# Usage: ./scripts/deploy-iso.sh <iso-url>
set -euo pipefail

PVE="${BLOUD_PVE_HOST:-root@192.168.0.62}"
VMID=9999
URL="$1"

echo "==> Destroying old VM (if any)"
ssh "$PVE" "qm stop $VMID 2>/dev/null; qm destroy $VMID --purge 2>/dev/null; true"

echo "==> Downloading ISO to Proxmox"
ssh "$PVE" "curl -L -o /var/lib/vz/template/iso/bloud-test.iso '$URL'"

echo "==> Creating VM $VMID"
ssh "$PVE" "qm create $VMID --name bloud-test --memory 4096 --cores 2 --ostype l26 \
  --cdrom local:iso/bloud-test.iso --boot order=ide2 \
  --net0 virtio,bridge=vmbr0 --agent enabled=1 --serial0 socket"

echo "==> Starting VM"
ssh "$PVE" "qm start $VMID"

echo "==> Done. Get IP with:"
echo "    ssh $PVE \"qm guest cmd $VMID network-get-interfaces | jq -r '.[].\\\"ip-addresses\\\"[]? | select(.\\\"ip-address-type\\\" == \\\"ipv4\\\") | .\\\"ip-address\\\"' | grep -v 127\""
