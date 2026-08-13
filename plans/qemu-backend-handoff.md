# Handoff: QEMU Backend — Real-VM Smoke Test

**Branch:** `qemu-backend` (clean). **Owner note (confirmed by user):** *Bloud does
NOT use `podman-compose` in the actual runtime.* `dev/compose.yml` is a legacy
"podman compose for the Bloud dev stack" that predates the host-agent's
self-managed container runtime. The host-agent orchestrates every container
itself from catalog `metadata.yaml` `containers:` blocks.

**Status:** The QEMU VM now boots, provisions, and is reachable. The CLI deploy
loop (`./bloud dev`) still fails at one legacy step. This doc records what is
done, verified, and what the next agent must resolve.

---

## Goal

A working QEMU VM driven by the new `QEMUBackend` (`cli/backend/qemu.go`):
`BLOUD_BACKEND=qemu ./bloud dev` should provision `.bloud/qemu/bloud-qemu/`,
boot an x86_64 KVM guest, rsync the project in, deploy the host-agent, and run it
(host-agent on host `localhost:3000`).

## Current state (verified live)

- **VM is running right now.** `qemu-system-x86_64` (pid in
  `.bloud/qemu/bloud-qemu/bloud-qemu.pid` = `2113291`) with KVM.
- Guest provisioned: `/var/tmp/bloud-qemu-ready` present, `podman 5.4.2`,
  `rsync` installed, user `bloud` (uid pinned to host uid).
- Project rsynced into guest at `/home/daniel/projects/bloud`
  (`dev/compose.yml` present).
- `ssh -p 2222 -i .bloud/qemu/bloud-qemu/id_ed25519 bloud@127.0.0.1` works.
- Manual guest check confirms `podman-compose up -d postgres redis` works in the
  guest (pulled `postgres:16-alpine`) — so the project copy is usable.

## What was fixed (all verified; `go test ./cli/...` passes)

`cli/backend/qemu.go` + `cli/backend/qemu_test.go` + `cli/dev.go`:

1. **`cmdDev` never called `bk.Create()`** — added `bk.Create(ctx)` before
   `Host()` so QEMU self-boots (idempotent for Lima too).
2. **`qemu-img resize` / QEMU `-m` reject `GiB`** — `qemuMemory "4GiB"→"4G"`,
   `qemuDisk "30GiB"→"30G"` (error: "Parameter 'size' expects ... suffix k,M,G,T").
3. **`-virtfs` option `security_mode=native` is invalid** — valid values are
   `passthrough|mapped-xattr|mapped-file|none`; dropped invalid `mode=0777`,
   added `id=host0`. Used `security_model=passthrough`.
4. **Guest uid pinned to host euid** (`os.Geteuid()`) in cloud-init so
   passthrough ownership maps host↔guest (both uid 1000 here).
5. **Host-forward ports are env-overridable** — `BLOUD_QEMU_FWD_<guestport>`
   (e.g. `BLOUD_QEMU_FWD_9000=9100`) to dodge host conflicts. **Required on this
   host**: host port `9000` is occupied by an unidentified listener (no pid, no
   container, no netavark/pasta — a ghost socket; `3000` held a `graph-game` vite
   server that was killed to free it). On a clean Debian host the defaults work.
6. **Readiness poll timeout 3min → 10min** — first-boot cloud-init package
   install exceeded 3 min.
7. **Duplicate-launch collision** — `ensureRunning` now checks `vmAlive()`
   (pidfile `/proc/<pid>` exists) and waits instead of spawning a second QEMU
   that hits the pidfile lock ("cannot create PID file: ... Resource temporarily
   unavailable").
8. **Marker-check quoting bug** — `ssh ... bash -c "test -f <marker>"` was
   joined by ssh so `bash -c` only ran bare `test` (always exit 1). Fixed to
   `runSSH("test","-f",marker)` so it forms one remote command.
9. **Debian cloud kernel has no virtio-9p / virtio-fs modules** and
   `linux-modules-extra-6.12.101+deb13-cloud-amd64` is unavailable → **dropped the
   9p live mount** (removed from user-data; removed the `host0` fstab line) and
   replaced it with `syncProject()`: `rsync -a -e "ssh -p 2222 -i key"` of the
   project dir into the guest each `Create` (excludes `.git .forgejo .bloud
   node_modules build dist coverage bloud`). Added `rsync` to cloud-init
   packages. This is incremental, not a live mount — host edits propagate on the
   next `./bloud dev`.

## BLOCKER — RESOLVED (this doc supersedes)

The legacy compose step in `cmdDev` was **removed**. Per the user, the shared
postgres/redis compose stack is **out of date** — apps are responsible for their
own containers now (see `apps/authentik/metadata.yaml` `containers:` blocks:
`apps-authentik-postgres`, `apps-authentik-redis`, plus `apps-immich-postgres`;
the host-agent orchestrator manages them all from the catalog). There is no
shared `localhost:5432/6379` infra anymore. `cli/dev.go` no longer runs
`podman-compose up -d postgres redis`; the cleanup step's stale comment was
updated to match.

**Verified end-to-end** (`BLOUD_BACKEND=qemu BLOUD_QEMU_FWD_9000=9100 ./bloud dev`):
provision (no-op) → cleanup → go build → frontend build → rsync deploy →
host-agent starts in the guest → system apps converge → `./bloud status` shows
`QEMU VM: bloud-qemu Running` + `Host agent: Running (localhost:3000)`; `curl
http://localhost:3000/health` returns `{"status":"ok"}`; guest containers all Up:
`apps-traefik apps-authentik-postgres apps-authentik-redis
apps-authentik-server apps-authentik-worker apps-authentik-ldap`.

### New fixes made while resolving

1. **OpenSSH arg-joining bug (`cli/executor/ssh.go`)** — root cause of the
   cosmetic podman-help output AND a real `mkdir: missing operand` deploy
   failure. OpenSSH joins every argv after the host with spaces and does NOT
   re-quote, so `ssh ... bash -c <script>` split the script on whitespace and
   `bash -c` only saw its first word (e.g. `podman rm -f ...` became a bare
   `podman` → top-level help to stdout; `mkdir -p <path>` lost its operand).
   Fixed by wrapping the script in `shellQuote(...)` so the joined remote
   command is `bash -c '<script>'`. Updated 2 ssh-executor test expectations.
2. **cloud-init never chowned `qemuRemoteDir` (`cli/backend/qemu.go`
   `buildUserData`)** — `/var/tmp/bloud-qemu-runtime` was root-owned 755, so the
   guest `bloud` user could not create `host-agent`/`data`. Added a chown in the
   runcmd.
3. **podman socket not running in guest (`buildUserData`)** — host-agent's
   `podman.NewClient()` requires `/run/user/1000/podman/podman.sock`, which only
   exists when `podman.socket` runs. cloud-init enabled linger but never started
   the socket; added `systemctl --user enable --now podman.socket` to runcmd.

### Env note for this host
`npm ci` was required once (web workspace `node_modules` missing) plus
`npm approve-scripts esbuild` (allow-scripts blocked esbuild's postinstall;
vite build needs it). Both are one-time host prep, not code.

### Secondary issue — RESOLVED
The podman-help output was the executor arg-joining bug above, not `-t 2`
(`-t, --time` IS valid in podman 5.4.2). Fixed by the ssh.go quoting change.

## Expected `./bloud dev` flow — VERIFIED (2026-08-13)

Provision (no-op when guest up) → remove legacy compose step → host-side
`go build` host-agent → `npm run build --workspace=@bloud/host-agent-web` →
rsync binary+frontend to `/var/tmp/bloud-qemu-runtime/host-agent` → run
host-agent in foreground with `BLOUD_DATA_DIR`, `BLOUD_APPS_DIR`,
`BLOUD_TRAEFIK_DYNAMIC_DIR` → host-agent converges system apps (traefik,
authentik + its own postgres/redis) → `./bloud status` shows
"QEMU VM running + host-agent running". All confirmed live.

## Env to use on THIS host

```bash
BLOUD_BACKEND=qemu BLOUD_QEMU_FWD_9000=9100 ./bloud dev
```

`9100` is free. `3000` was freed by killing the unrelated `graph-game` vite
server (pid 93115). Do not kill the mystery `9000` listener — it has no
identifiable owner.

## Test updates already made

`cli/backend/qemu_test.go`: launch args `-m 4G` (was `-m 4GiB`); user-data
asserts `chown bloud:bloud` (was the 9p mount); command sequence now includes
`rsync`; virtfs passthrough. Full suite passes:
`cd cli && go build ./... && go test ./... -count=1`.

## Stale docs to update (low priority)

- `plans/qemu-backend.md` — still describes the 9p/virtiofs live mount,
  `security_mode=native`, `30GiB`; now the design is rsync + passthrough + G
  suffixes + env-overridable forward ports.
- `dev/qemu.yaml` — same stale details.

## Useful commands

```bash
# rebuild CLI
cd cli && go build -o ../bloud .
# status of the running guest
KEY=.bloud/qemu/bloud-qemu/id_ed25519
ssh -p 2222 -i $KEY -o StrictHostKeyChecking=accept-new bloud@127.0.0.1 'podman --version; test -f /var/tmp/bloud-qemu-ready && echo READY'
# teardown when done
BLOUD_BACKEND=qemu ./bloud destroy   # kills qemu via pidfile, rm .bloud/qemu
```
