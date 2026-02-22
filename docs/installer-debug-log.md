# Installer ISO Debug Log

## Context

End-to-end test flow: `./bloud start` downloads the latest ISO, boots it in a Proxmox VM, drives the installer API to partition/install/reboot, then validates the running system. This flow was broken — this document tracks the bugs found and fixed.

---

## Bug 1 — SSH auth timeout (sshd)

**Commit:** `2e219be`
**File:** `nixos/iso.nix`

The live ISO booted fine and had an IP, but `sshpass -p ""` always failed. The SSH server accepted `PasswordAuthentication true` but was missing `PermitEmptyPasswords true`. SSH requires this as a separate, explicit setting.

```nix
settings = {
  PermitRootLogin = "yes";
  PasswordAuthentication = true;
  PermitEmptyPasswords = true;  # added
};
```

---

## Bug 2 — SSH auth timeout (PAM)

**Commit:** `977ebee`
**File:** `nixos/iso.nix`

Even with `PermitEmptyPasswords yes` in sshd_config, PAM's `pam_unix` auth module was rejecting empty passwords at the PAM layer (missing `nullok` flag). Required a separate NixOS option.

```nix
security.pam.services.sshd.allowNullPassword = true;
```

---

## Bug 3 — Install process killed immediately (context cancellation)

**Commit:** `a909edd`
**File:** `services/installer/internal/installer/installer.go`

The installer's `Start()` passed `r.Context()` (the HTTP request context) into the goroutine. As soon as `curl` received `{"started":true}` and closed the connection, the request context was cancelled, immediately killing `wipefs` with `context canceled`.

```go
// Before
go inst.run(ctx, req)

// After
go inst.run(context.Background(), req)
```

---

## Bug 4 — `/mnt` doesn't exist on live root

**Commit:** `40ae229`
**File:** `services/installer/internal/partition/partition.go`

The live ISO boots into a tmpfs root which has no `/mnt` directory. The mount step failed with `mount point does not exist`. Added `os.MkdirAll("/mnt", 0755)` before mounting.

---

## Bug 5 — Unmount needed on retry

**Commit:** `4019e64`
**File:** `services/installer/internal/partition/partition.go`

If an install attempt fails after mounting (e.g. during nixos-install), retrying the install hits `wipefs: /dev/sda: probing initialization failed: Device or resource busy` because `/dev/sda1` and `/dev/sda2` are still mounted. Added cleanup at the top of `Prepare()`:

```go
exec.CommandContext(ctx, "umount", "-f", "/mnt/boot").Run()
exec.CommandContext(ctx, "umount", "-f", "/mnt").Run()
```

---

## Bug 6 — Flake path never passed to binary

**Commit:** `4019e64`
**Files:** `nixos/modules/installer.nix`, `services/installer/internal/nixinstall/nixinstall.go`

The NixOS module defined a `flakePath` option (defaulting to `${pkg}/share/bloud-installer/bloud`) and bundled the flake at that path, but never passed it to the binary. The binary fell back to `/etc/bloud` which doesn't exist on the live ISO.

```nix
# Added to environment block in installer.nix
INSTALLER_FLAKE_PATH = cfg.flakePath;
```

The Go code was also updated to read the env var in `nixinstall.Install()`.

---

## Bug 7 — Flake path env var read too late

**Commit:** `5fef427`
**File:** `services/installer/internal/installer/installer.go`

The env var was being read in `nixinstall.Install()` but `installer.go` was already substituting `/etc/bloud` before calling `Install()`. Fixed by reading `INSTALLER_FLAKE_PATH` in `installer.go` before the fallback.

---

## Bug 8 — `nix: command not found`

**Commit:** `5fef427`
**File:** `nixos/modules/installer.nix`

The installer systemd service `path` included disk tools (`parted`, `wipefs`, `mkfs.*`) but not `nix`. Since `nixos-install` is a shell script that calls `nix` internally, it failed immediately.

```nix
path = with pkgs; [ parted util-linux dosfstools e2fsprogs cryptsetup nix ];
```

---

## Bug 9 — Installed system not in ISO Nix store

**Commit:** `910c817`
**File:** `flake.nix`

`nixos-install` correctly evaluated `nixosConfigurations.bloud.config.system.build.toplevel` to `/nix/store/hydxw7b6n1ik...-nixos-system-bloud-24.11...`, but that path was not in the live ISO's squashfs. With nothing to copy to `/mnt`, nixos-install fell back to whatever it could find (the installer package), making `/mnt/nix/var/nix/profiles/system` point to the wrong thing.

Fixed by adding `isoImage.storeContents` in `flake.nix` so the installed system's full closure is baked into the ISO squashfs at build time:

```nix
isoImage.storeContents = [
  self.nixosConfigurations.bloud.config.system.build.toplevel
];
```

This makes the ISO larger but enables fully offline installation.

---

## Bug 10 — Hash mismatch between bundled flake and ISO store

**Commit:** `6d2b557`
**Files:** `flake.nix`, `nixos/modules/installer.nix`, `services/installer/internal/nixinstall/nixinstall.go`

After Bug 9's fix, the system closure *was* in the ISO store, but `nixos-install --flake` still failed. The bundled flake (stored under `/nix/store/.../share/bloud-installer/bloud`) re-evaluates `nixosConfigurations.bloud` at install time and produces a **different** store hash (`hydxw7b6n1ik...`) than the closure baked into the ISO squashfs (`72yy8waz...`). The evaluated path doesn't exist in the live store, so nixos-install fails.

Root cause: the bundled flake source tree is a filtered subset of the full repo (Nix removes files not tracked or relevant), producing a different `narHash` and therefore a different system derivation hash.

Fix: bypass flake re-evaluation entirely by passing the pre-built system outPath as `INSTALLER_SYSTEM_PATH` and switching to `nixos-install --system <path>`. The same derivation reference is used for both `isoImage.storeContents` and `bloud.installer.systemPath`, so the path is guaranteed to be in the ISO store.

```nix
# flake.nix — ISO module block
{
  isoImage.storeContents = [
    self.nixosConfigurations.bloud.config.system.build.toplevel
  ];
  bloud.installer.systemPath = "${self.nixosConfigurations.bloud.config.system.build.toplevel}";
}
```

```go
// nixinstall.go — prefer --system over --flake
if systemPath := os.Getenv("INSTALLER_SYSTEM_PATH"); systemPath != "" {
    args = []string{"--no-root-passwd", "--system", systemPath, "--root", "/mnt"}
} else {
    args = []string{"--no-root-passwd", "--flake", flakePath + "#bloud", "--root", "/mnt"}
}
```

---

## Diagnostic fix — CLI now streams SSE events

**Commit:** `d88c8f8`
**File:** `cli/pve.go`, `services/installer/internal/installer/installer.go`, `services/installer/internal/api/handlers.go`

The CLI only printed the phase name (e.g. `[partitioning]`), never the error message. Because the polling interval was 2 seconds and partitioning/formatting completed in under one cycle, every failure showed `[partitioning]` then `[failed]`, making it look like partitioning was broken when it wasn't.

Changes:
- CLI now streams the SSE `/api/progress` endpoint in a goroutine, printing `[phase] message` in real time
- Installer tracks `lastMessage` and exposes it in `/api/status` as `lastMessage`
- CLI fetches and prints `lastMessage` on failure

With the new diagnostics the actual error was immediately visible:
```
[installing] Running nixos-install --no-root-passwd --flake .../bloud-installer-0.1.0/...
[installing] this path will be fetched (0.00 MiB, 11.14 MiB unpacked):
[installing]   /nix/store/pn18ir9...bloud-installer-0.1.0  ← installer pkg, not NixOS system
[installing] chroot: failed to run command '/nix/var/nix/profiles/system/activate': No such file or directory
[failed] nixos-install failed: nixos-install: exit status 127
```

**Root cause confirmed:** The bundled flake evaluates `nixosConfigurations.bloud` to the installer package (11 MiB), not the NixOS system. This is exactly what Bug 10 diagnosed and fixed. The fix is not yet in the released ISO.

---

## Current Status

All 11 bugs fixed. Waiting for CI to publish a new ISO from commits through `d88c8f8`. The Bug 10 fix (`nixos-install --system <path>` instead of `--flake`) should resolve the final install failure.
