# OS Installer Design

## Philosophy

The installer should feel like **appliance setup**, not an OS installer. Think Synology DiskStation first boot, or Apple TV setup — you answer a couple of questions and it takes care of the rest. Users should never feel like they're partitioning a disk or installing Linux.

Strong defaults handle everything technical. Advanced options exist but are hidden. The experience is:

1. Boot machine from USB
2. Open browser on any device → `http://bloud.local`
3. Watch a progress screen
4. Machine reboots → Bloud setup wizard appears. Done.

The browser stays on `bloud.local` the entire time. When the machine reboots, the page waits, detects the installed system coming back up, and transitions directly into Bloud — no "go to bloud.local" instruction needed.

Account creation happens on first boot via the existing host-agent setup wizard. The installer does not collect credentials.

---

## Architecture: Clean Separation

The installer and host-agent are **separate binaries** with separate frontends. They share a Svelte component library (`packages/ui/`), but compile and deploy independently.

```
services/
  installer/
    cmd/installer/
    internal/
      api/          - HTTP handlers, routes, server
      disks/        - lsblk enumeration, auto-selection
      installer/    - state machine
      nixinstall/   - nixos-install wrapper
      partition/    - parted + mkfs
      sse/          - SSE event streaming
    web/            - SvelteKit installer UI
  host-agent/
    cmd/host-agent/
    internal/
      ...
    web/            - SvelteKit host-agent UI

packages/
  ui/               - shared Svelte components (Button, Input, LoadingSpinner, ProgressChecklist)
```

**Why separate:**
- The installer only exists on the live ISO. The installed system gets only host-agent — installer code is never present in a running Bloud instance.
- Installer needs system tools (`parted`, `nixos-install`) that the running system doesn't. These stay ISO-only.
- No latent unauthenticated endpoints shipping in production.
- The two services evolve independently.

**ISO vs installed system:**

| | Live ISO | Installed System |
|---|---|---|
| Service | `bloud-installer.service` | `bloud-host-agent.service` |
| Binary | `/bin/bloud-installer` | `/bin/host-agent` |
| Port | 3001 | 3000 |
| Auth | None (pre-setup) | Session + Authentik |

The installer service is not written to disk — it only exists in the live ISO environment.

---

## User Flow

### Happy Path (Single Disk)

```
Boot → bloud.local → Welcome → Installing... → [reboot] → Bloud setup wizard
```

Two screens, zero user inputs during installation. Account creation happens on first boot.

### Advanced Path (Multiple Disks or Custom Config)

```
Boot → bloud.local → Welcome (▼ Advanced) → Installing... → [reboot] → Bloud setup wizard
```

Same two screens. The Welcome screen surfaces Advanced options inline when needed.

---

## Screen Design

### Screen 1: Welcome

Shows what Bloud detected. If there's only one disk and no ambiguity, this is purely informational — the user just clicks Install.

```
┌────────────────────────────────────────────────┐
│                                                │
│         bloud                                  │
│                                                │
│  Your server is ready to set up.              │
│                                                │
│  ┌──────────────────────────────────────────┐ │
│  │ CPU   Intel Core i5                      │ │
│  │ Disk  Samsung 870 EVO · 500 GB           │ │
│  │ IP    192.168.1.42                       │ │
│  └──────────────────────────────────────────┘ │
│                                                │
│  All existing data will be erased.            │
│                                                │
│  ▸ Advanced                                   │
│                                                │
│              [ Install Bloud ]                 │
│                                                │
└────────────────────────────────────────────────┘
```

**Disk auto-selection logic:**
- Single disk → auto-select, show model name only, no picker
- Multiple disks → auto-select largest, show picker in Advanced (or inline if sizes are ambiguous — within 20% of each other)
- Boot device excluded from candidates
- No valid disk found → error state with guidance

**"All existing data will be erased" warning:**
- Only shown if the auto-selected disk has existing partitions
- Not shown for empty/unpartitioned disks

**Advanced (collapsed by default):**
- Disk selector (if multiple disks detected)
- Encryption toggle (on by default)

Hostname is not configurable — the device is always `bloud` and always reachable at `bloud.local`.

---

### Screen 2: Installing

A checklist of phases with a real-time log below. Not a percentage bar — those lie and feel disconnected from what's actually happening.

```
┌────────────────────────────────────────────────┐
│                                                │
│         bloud                                  │
│                                                │
│  Setting up your server...                    │
│                                                │
│  ✓  Partitioned disk                          │
│  ✓  Formatted partitions                      │
│  ●  Copying system files          (2 min)     │
│  ○  Finalizing configuration                  │
│  ○  Done                                      │
│                                                │
│  ┌──────────────────────────────────────────┐ │
│  │ unpacking nixos-system-bloud-25.05...    │ │
│  │ ...                                      │ │
│  └──────────────────────────────────────────┘ │
│                                                │
│  Don't turn off the machine.                  │
│                                                │
└────────────────────────────────────────────────┘
```

Phases:
1. Validating
2. Partitioning disk
3. Formatting
4. Installing (`nixos-install` — longest phase, ~2–4 min)
5. Configuring
6. Complete

Log lines stream via SSE. The log is secondary — the phase checklist is the primary UI. On failure: error state with the relevant log section highlighted and a "Try Again" button (returns to Welcome).

---

### Screen 3: Restarting

When installation completes, a Reboot button appears. Clicking it calls `POST /api/reboot`, which triggers `systemctl reboot`. The UI transitions to a waiting state. The user stays on `bloud.local`.

```
┌────────────────────────────────────────────────┐
│                                                │
│         bloud                                  │
│                                                │
│  Your server is restarting.                   │
│                                                │
│  You'll be redirected automatically when      │
│  it's ready.                                  │
│                                                │
└────────────────────────────────────────────────┘
```

**Reconnection logic (client-side):**

1. Install complete → user clicks Reboot → `POST /api/reboot` → `systemctl reboot`
2. Browser transitions to the Restarting screen
3. Client polls `GET /api/health` every 3 seconds
4. Machine goes down → requests fail → spinner continues (expected, not an error)
5. Installed system boots, host-agent starts
6. `/api/health` responds → browser navigates to `/` → Bloud setup wizard loads

The transition feels like the page "wakes up." No new tab, no URL change, no instruction.

**Edge case**: if the machine doesn't come back within ~5 minutes, show a subtle "Taking longer than expected — check that the machine is on" message without breaking the polling loop.

---

## Backend Design

### Service: `services/installer/`

```
services/installer/
  cmd/installer/
    main.go
  internal/
    api/
      handlers.go   - all /api/* handlers
      routes.go     - route registration
      server.go     - HTTP server setup
    disks/
      enumerate.go  - parse lsblk → disk list
      select.go     - auto-selection logic
    installer/
      installer.go  - state machine + SSE subscriber fan-out
    nixinstall/
      nixinstall.go - nixos-install wrapper
    partition/
      partition.go  - GPT layout, mkfs.vfat, mkfs.ext4, mount
    sse/
      sse.go        - SSE writer helpers
  web/
    src/routes/     - installer screens
    src/lib/steps/  - Welcome, Installing, Restarting components
```

### API

```
GET  /api/health      - Liveness check (200 OK = installer is up)
GET  /api/status      - System info (hostname, IPs, CPU, memory)
GET  /api/disks       - Available disks with auto-selection hint
POST /api/install     - Begin installation (point of no return)
GET  /api/progress    - SSE stream of install log events
POST /api/reboot      - Trigger reboot (only callable from complete state)
```

No auth on any endpoint — the installer binary only runs on the live ISO.

The installed host-agent also exposes `GET /api/health`. The Restarting screen polls this endpoint; when it responds after the machine was dark, the browser knows the installed system is up and navigates to `/`.

### Install State Machine

```
idle
  → validating
    → partitioning
      → formatting
        → installing     ← nixos-install, longest phase
          → configuring
            → complete
            → failed     ← catchable from any phase
```

A single goroutine runs sequentially. Each phase emits structured SSE events. The HTTP server stays responsive throughout.

---

## NixOS ISO (`nixos/iso.nix`)

- Runs `bloud-installer.service` (port 3001) instead of host-agent
- iptables NAT redirects port 80 → 3001 (rootless-friendly pattern)
- mDNS via Avahi so browsers can reach `http://bloud.local`
- Getty banner directing users to `http://bloud.local`
- Disk tools in environment: `parted`, `util-linux`, `dosfstools`, `e2fsprogs`, `cryptsetup`
- SSH enabled with password auth and empty root password for debug access

---

## Defaults Summary

| Setting | Default | Advanced Override |
|---------|---------|-------------------|
| Disk | Largest detected (excl. boot device) | Disk picker |
| Hostname | `bloud` (fixed) | — |
| Filesystem | ext4 | — |
| Encryption | On (toggle) | Toggle |
| Network | DHCP | — (post-install via Bloud UI) |
| Partitioning | GPT: 1MiB–513MiB EFI (FAT32) + 513MiB–100% root (ext4) | — |

---

## Decisions

1. **No credentials during installation**: Account creation is deferred entirely to the existing first-boot setup wizard in host-agent (Authentik is running by then). The installer never collects a username or password.

2. **Multiple disks, ambiguous sizes**: If the top two disks are within ~20% of each other in size, auto-selection has no clear winner. In that case, surface the disk picker inline on the Welcome screen (not buried in Advanced) with a brief explanation.

3. **Boot device exclusion**: Identifying which block device the ISO booted from is non-trivial (`findmnt`, `/proc/cmdline` parsing). Accidentally auto-selecting the USB drive would be catastrophic. Currently not implemented — flagged for careful follow-up.

4. **Pre-built artifacts**: The installer binary and frontend are built outside the Nix sandbox by CI (same pattern as host-agent). Artifacts go in `build/installer` and `build/installer-web/`. Use `scripts/build-installer.sh` for local builds.
