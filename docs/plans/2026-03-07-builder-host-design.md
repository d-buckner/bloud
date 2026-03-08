# Builder Host Design

**Date:** 2026-03-07
**Status:** Approved

## Goal

Enable `./bloud start --build --install` to build the ISO locally on a
pre-existing builder VM, then run the normal install cycle — eliminating the
wait for GitHub Actions on every iteration.

## Approach: `BLOUD_BUILDER_HOST`

Add a `BLOUD_BUILDER_HOST` env var (e.g. `builder@192.168.0.105`) that points
to any SSH-accessible host with Nix installed. When set, `doBuild()` SSHes
directly to that host using the default SSH key — no Proxmox VM lifecycle, no
dedicated keypair, no passwords.

## What Gets Removed

The Proxmox-managed builder VM (VMID 9998, Ubuntu cloud image) never worked
reliably and is replaced entirely:

- `createBuilderVM` — Ubuntu cloud image download + VM creation
- `ensureBuilderKey` / `builderKeyPaths` — dedicated `~/.bloud/builder_rsa` keypair
- `builderCfg` / `waitForBuilderSSH` / `builderExec` / `builderExecStream`
- All `pveBuild*` constants (VMID, name, memory, cores, disk, image URL/file, key path)
- `destroy-builder` command (makes no sense for externally managed VMs)

## New Design

### Configuration

```
# .env
BLOUD_BUILDER_HOST=builder@192.168.0.105
```

### `setup-builder` (repurposed)

When `BLOUD_BUILDER_HOST` is set, provisions that host:
1. SSH to the host
2. Verify Nix is installed (error if not)
3. Install Go and Node via `nix profile install nixpkgs#go nixpkgs#nodejs`
4. Install git and rsync via apt if not present
5. Touch `~/.bloud-provisioned` sentinel file (idempotent)

### `doBuild()`

1. Read `BLOUD_BUILDER_HOST` — error if not set
2. SSH directly to host (default key, no sshpass)
3. Rsync source from project root (excluding `build/`, `node_modules/`, `.direnv/`)
4. Run build script on remote:
   - `export PATH="$HOME/.nix-profile/bin:$PATH"` (Go + Node from Nix profile)
   - Build host-agent binary (CGO_ENABLED=0, linux/amd64)
   - Build installer binary
   - Build frontend and installer-web
   - `git add -f build/` to make artifacts visible to Nix flake
   - `nix build .#packages.x86_64-linux.iso --no-link`
5. Get ISO store path, SCP ISO to local `/tmp/bloud-built.iso`
6. SCP ISO to Proxmox ISO storage
7. Return — normal `cmdStartPVE` flow continues

### SSH Helpers

Two new helpers for direct-SSH (no dedicated key):
- `builderSSHExec(host, cmd string) (string, error)`
- `builderSSHExecStream(host, cmd string) error`

Both use `ssh -o StrictHostKeyChecking=no -o ConnectTimeout=10 host cmd`.

### Build Directory

Remote build dir: `~/bloud` (relative to the builder user's home).

## Security

- All SSH uses key auth — keys were pre-installed via `ssh-copy-id`
- No passwords stored or passed anywhere
- `BLOUD_BUILDER_HOST` is the only credential needed

## Usage

```bash
# .env
BLOUD_BUILDER_HOST=builder@192.168.0.105

# First time: provision the builder
./bloud setup-builder

# Build ISO locally and run full install cycle
./bloud start --build --install

# Build ISO only (no install)
./bloud start --build
```
