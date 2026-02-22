# OS Installer — Implementation Agent Plan

See `os-installer.md` for the full design.

## Phase 1 (parallel)

### Agent A — Shared infrastructure
**Goal:** Create the scaffold that Phase 2 agents build on.
- Set up `services/installer/` directory with Go module (`codeberg.org/d-buckner/bloud-v3/services/installer`)
- `cmd/installer/main.go` — minimal HTTP server skeleton
- `internal/disks/` — enumerate disks via lsblk, auto-select largest non-boot disk
- `internal/sse/` — SSE event streaming (modelled on host-agent pattern)
- Add `services/installer/web` as npm workspace, SvelteKit skeleton
- Extract shared Svelte components into `packages/ui/` npm workspace: Button, Input, LoadingSpinner, ProgressChecklist
- Update root `package.json` workspaces to include new packages

### Agent B — Host-agent changes
**Goal:** Two small targeted changes to host-agent.
1. Add `"mode": "normal"` to `GET /api/health` response
2. Frontend setup wizard reads `?setup_user=<username>` query param and pre-fills username field

---

## Phase 2 (parallel, after Phase 1)

### Agent C — Installer backend
**Goal:** Full Go installer service.
- Disk listing API using Phase 1 disks package
- Install state machine: idle → validating → partitioning → formatting → installing → configuring → complete/failed
- Partition + format target disk (parted, mkfs.vfat, mkfs.ext4)
- nixos-install wrapper with config generation
- SSE progress stream using Phase 1 sse package
- Reboot endpoint (only callable from complete state)

### Agent D — Installer frontend
**Goal:** Full SvelteKit installer UI.
- Welcome screen: system info, disk summary, Advanced collapse (disk picker, encryption toggle)
- Account screen: username + password + confirm
- Installing screen: phase checklist + SSE log stream
- Restarting screen: spinner + reconnection polling (`GET /api/health` every 3s, navigate to `/` on `mode: normal`)
- Uses shared components from Phase 1 packages/ui

---

## Phase 3 (after Phase 2)

### Agent E — NixOS + build pipeline
**Goal:** Wire everything into the ISO and CI.
- `nixos/iso.nix`: getty banner, `bloud-installer.service`, install packages, remove host-agent from ISO
- `nixos/packages/installer.nix`: package installer binary (same pattern as host-agent.nix)
- `.github/workflows/build-iso.yml`: build installer binary + frontend alongside host-agent
- Update root `package.json` scripts for installer build
