# OS Installer Design

## Philosophy

The installer should feel like **appliance setup**, not an OS installer. Think Synology DiskStation first boot, or Apple TV setup — you answer a couple of questions and it takes care of the rest. Users should never feel like they're partitioning a disk or installing Linux.

Strong defaults handle everything technical. Advanced options exist but are hidden. The experience is:

1. Boot machine from USB
2. Open browser on any device → `http://bloud.local`
3. Enter a username and password
4. Watch a progress screen
5. Bloud appears. Done.

The browser stays on `bloud.local` the entire time. When the machine reboots, the page waits, detects the installed system coming back up, and transitions directly into Bloud — no "go to bloud.local" instruction needed.

---

## Architecture: Clean Separation

The installer and host-agent are **separate binaries** with separate frontends. They share Go packages and a Svelte component library, but compile and deploy independently.

```
services/
  installer/              ← new service
    cmd/installer/
    internal/
      api/
      disks/
      partition/
      nixinstall/
    web/                  ← SvelteKit app, shares $lib/components with host-agent
  host-agent/
    cmd/host-agent/
    internal/
      ...
    web/
      src/lib/components/ ← shared component library
```

**Why separate:**
- The installer only exists on the live ISO. The installed system gets only host-agent — installer code is never present in a running Bloud instance.
- Installer needs system tools (`parted`, `nixos-install`) that the running system doesn't. These stay ISO-only.
- No latent unauthenticated endpoints shipping in production.
- The two services evolve independently.

**What's shared:**
- Go packages for disk enumeration, SSE streaming, logging
- Svelte component library (buttons, inputs, loading states, progress indicators)
- Design tokens and CSS

**ISO vs installed system:**

| | Live ISO | Installed System |
|---|---|---|
| Service | `bloud-installer.service` | `bloud-host-agent.service` |
| Binary | `/bin/bloud-installer` | `/bin/host-agent` |
| Port | 3000 | 3000 |
| Auth | None (pre-setup) | Session + Authentik |

The installer service is not written to disk — it only exists in the live ISO environment.

---

## User Flow

### Happy Path (Single Disk)

```
Boot → bloud.local → Welcome → Create Account → Installing... → [reboot] → Bloud
```

Three screens, two user inputs (username + password). Everything else is automatic.

### Advanced Path (Multiple Disks or Custom Config)

```
Boot → bloud.local → Welcome (▼ Advanced) → Create Account → Installing... → [reboot] → Bloud
```

Same three screens. The Welcome screen surfaces Advanced options inline when needed.

---

## Screen Design

### Screen 1: Welcome

Shows what Bloud detected. If there's only one disk and no ambiguity, this is purely informational — the user just clicks Continue.

```
┌────────────────────────────────────────────────┐
│                                                │
│         bloud                                  │
│                                                │
│  Your server is ready to set up.              │
│                                                │
│  ┌──────────────────────────────────────────┐ │
│  │ 💻  Intel Core i5  ·  16 GB RAM          │ │
│  │ 💾  Samsung 870 EVO  ·  500 GB           │ │
│  │ 🌐  192.168.1.42  ·  bloud.local         │ │
│  └──────────────────────────────────────────┘ │
│                                                │
│  Bloud will be installed on the Samsung        │
│  drive. All existing data will be erased.      │
│                                                │
│  ▸ Advanced                                   │
│                                                │
│              [ Continue → ]                    │
│                                                │
└────────────────────────────────────────────────┘
```

**Disk auto-selection logic:**
- Single disk → auto-select, show model name only, no picker
- Multiple disks → auto-select largest, show picker in Advanced (or inline if sizes are ambiguous — within 20% of each other)
- Boot device excluded from candidates (detect and filter the device the ISO booted from)
- No valid disk found → error state with guidance

**"All existing data will be erased" warning:**
- Only shown if the auto-selected disk has existing partitions
- Not shown for empty/unpartitioned disks — no reason to alarm users unnecessarily

**Advanced (collapsed by default):**
- Disk selector (if multiple disks detected)
- Encryption toggle (on by default if TPM2 present, off otherwise)

Hostname is not configurable — the device is always `bloud` and always reachable at `bloud.local`.

---

### Screen 2: Create Your Account

```
┌────────────────────────────────────────────────┐
│                                                │
│         bloud                                  │
│                                                │
│  Create your admin account.                   │
│                                                │
│  Username                                      │
│  ┌──────────────────────────────────────────┐ │
│  │                                          │ │
│  └──────────────────────────────────────────┘ │
│                                                │
│  Password                                      │
│  ┌──────────────────────────────────────────┐ │
│  │                                          │ │
│  └──────────────────────────────────────────┘ │
│                                                │
│  Confirm password                              │
│  ┌──────────────────────────────────────────┐ │
│  │                                          │ │
│  └──────────────────────────────────────────┘ │
│                                                │
│              [ Install Bloud ]                 │
│                                                │
└────────────────────────────────────────────────┘
```

Same validation as the existing setup wizard. "Install Bloud" is the point of no return — disk write begins immediately on submit.

---

### Screen 3: Installing

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
1. Partitioning disk
2. Formatting
3. Copying system files (`nixos-install` — longest phase, ~2–4 min)
4. Applying configuration
5. Done

Log lines stream via SSE (same pattern as app install logs in host-agent). The log is secondary — the phase checklist is the primary UI. On failure: error state with the relevant log section highlighted and a "Try Again" button (restarts from disk selection).

---

### Screen 3b: Restarting

When installation completes, the installer triggers a reboot and transitions the UI into a waiting state. The user stays on `bloud.local` — no instructions, no navigation needed.

```
┌────────────────────────────────────────────────┐
│                                                │
│         bloud                                  │
│                                                │
│                                                │
│              ◌  Restarting...                 │
│                                                │
│                                                │
└────────────────────────────────────────────────┘
```

**Reconnection logic (client-side):**

1. Install complete → installer calls `POST /api/reboot`, triggers system reboot
2. Browser transitions to the Restarting screen
3. Client polls `GET /api/health` every 3 seconds
4. Machine goes down → requests fail → spinner continues (expected, not an error)
5. Installed system boots, host-agent starts
6. `/api/health` responds with `{ "mode": "normal" }` (installer would have responded `{ "mode": "installer" }`)
7. Browser detects `mode: normal` → navigates to `/` → normal Bloud UI loads

The transition feels like the page "wakes up." No new tab, no URL change, no instruction. Bloud just appears.

**Edge case**: if the machine doesn't come back within ~5 minutes, show a subtle "Taking longer than expected — check that the machine is on" message without breaking the polling loop.

---

## Backend Design

### New service: `services/installer/`

```
services/installer/
  cmd/installer/
    main.go
  internal/
    api/
      routes.go       - all /api/* handlers
      install.go      - start install, SSE progress
      disks.go        - disk listing
      status.go       - system info
      reboot.go       - trigger reboot after completion
    disks/
      enumerate.go    - parse lsblk → disk model
      select.go       - auto-selection + boot device exclusion
    partition/
      layout.go       - GPT: EFI (512MB vfat) + root (ext4)
      format.go       - mkfs.vfat, mkfs.ext4
    nixinstall/
      config.go       - generate nixos config for installed system
      install.go      - shell out to nixos-install
    installer.go      - state machine
  web/
    src/routes/       - installer screens
    src/lib/          - shared component imports
```

### API

```
GET  /api/health      - Mode indicator ({ "mode": "installer" | "normal" })
GET  /api/status      - System info + install phase
GET  /api/disks       - Available disks with auto-selection hint
POST /api/install     - Begin installation (point of no return)
GET  /api/progress    - SSE stream of install log events
POST /api/reboot      - Trigger reboot (only callable from complete state)
```

No auth on any endpoint — the installer binary only runs on the live ISO.

Critically, the installed host-agent also exposes `GET /api/health` returning `{ "mode": "normal" }`. This is the signal the browser uses to detect that the reboot completed and the installed system is up.

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

A single goroutine runs sequentially. Each phase emits structured SSE events. The HTTP server stays responsive throughout (status, progress, and health endpoints remain available).

### NixOS Config Generation

Before calling `nixos-install`, the installer generates the target system's NixOS configuration. Key differences from the ISO config:

- No `bloud-installer.service`
- Same `bloud.nix`, app modules, Traefik, Authentik
- Hostname: `bloud` (fixed)
- First-boot secrets init service (carried over)
- LUKS config if encryption was selected

The user's credentials are **not** written to disk. Account creation is deferred — after reboot, the existing first-user-setup wizard in host-agent handles it (Authentik is now running). The installer passes the username as a pre-fill hint in the installed config so the setup screen can greet the user by name.

---

## NixOS ISO Changes (`nixos/iso.nix`)

```nix
# Getty banner on all consoles
services.getty.greetingLine = ''
  \e[1;34m╔════════════════════════════════╗\e[0m
  \e[1;34m║       Welcome to Bloud        ║\e[0m
  \e[1;34m║                               ║\e[0m
  \e[1;34m║  Visit http://bloud.local     ║\e[0m
  \e[1;34m║  on any device to get started.║\e[0m
  \e[1;34m╚════════════════════════════════╝\e[0m
'';

# Tools required by installer, not needed on installed system
environment.systemPackages = with pkgs; [
  parted
  dosfstools        # mkfs.vfat
  e2fsprogs         # mkfs.ext4
  nixos-install-tools
  util-linux        # lsblk, wipefs, findmnt
];

# Installer service instead of host-agent
systemd.services.bloud-installer = { ... };

# No bloud-host-agent.service on the ISO
```

---

## Defaults Summary

| Setting | Default | Advanced Override |
|---------|---------|-------------------|
| Disk | Largest detected (excl. boot device) | Disk picker |
| Hostname | `bloud` (fixed) | — |
| Filesystem | ext4 | — (v1) |
| Encryption | On if TPM2 present, off otherwise | Toggle |
| Network | DHCP | — (post-install via Bloud UI) |
| Partitioning | GPT: 512MB EFI + rest root | — (v1) |

---

## Decisions

1. **Credentials handoff**: Account creation is deferred entirely to the existing first-user-setup wizard after reboot. The installer collects username/password only to pre-fill the post-reboot setup screen — the password is never persisted anywhere. The user types their password twice (installer → setup wizard), which is acceptable given it eliminates any on-disk credential risk.

2. **Multiple disks, ambiguous sizes**: If the top two disks are within ~20% of each other in size, auto-selection has no clear winner. In that case, surface the disk picker inline on the Welcome screen (not buried in Advanced) with a brief explanation. The 20% threshold may need tuning against real hardware scenarios.

3. **Boot device exclusion**: Identifying which block device the ISO booted from is non-trivial (`findmnt`, `/proc/cmdline` parsing). Flagged as needing careful implementation — accidentally auto-selecting the USB drive would be catastrophic. Approach left open for implementation.
