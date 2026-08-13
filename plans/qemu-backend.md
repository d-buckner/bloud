# Plan: QEMU Backend (Parallel, Selectable)

**Status:** Implementing — phases 1-3 (transport, QEMUBackend, CLI wiring) are done on
`qemu-backend`; remaining: e2e integration, real-VM smoke test, plan doc cleanup.
**Last updated:** 2026-06-24

---

## Summary

Add a second `backend.Backend` implementation that provisions and manages a QEMU VM
directly (`qemu-img` + `qemu-system-x86_64`), reachable over real SSH — in parallel with
the existing Lima backend. The backend is selected via env var/flag; Lima stays the
default. The executor layer is generalized behind a `Transport` abstraction so both
backends share one `SSHHost`/`SSHExecutor` path.

Decisions (confirmed with owner):

- **Backend role:** QEMU is a parallel, selectable backend; Lima remains default.
- **Platform split:** Lima is the backend **for macOS** generally; QEMU is the backend
  **for Linux** generally. Each host uses its native accelerator — Lima uses vz (Apple
  Virtualization) on macOS; QEMU uses **KVM** (`-accel kvm`) on Linux.
- **Executor scope:** generalize the shared transport — Lima uses `limactl shell`,
  QEMU uses real `ssh -p <port>`.
- **Guest arch:** `x86_64`, matching the typical Linux host and an amd64 Debian 13
  cloud image.

Why QEMU: removes the `limactl`/Lima dependency for VM provisioning on Linux, giving a
self-contained, scriptable VM lifecycle (`qemu-img`/`qemu-system-*`) with the same
guest-side runtime (podman + host-agent). The CLI already has a generic-SSH precedent in
`e2e_lifecycle.go` (`BLOUD_E2E_SSH_TARGET`, ssh + rsync), which this plan formalizes.

---

## 1. Current Lima Architecture (Coupling Points)

The Lima backend is coupled to `limactl` at four layers:

| Layer | File | Lima coupling |
|------|------|---------------|
| Backend lifecycle | `cli/backend/lima.go` | `limactl list/create/start/delete` |
| Remote executor | `cli/executor/ssh.go` | `limactl shell --start`, `limactl copy`, `limactl shell` |
| Host readiness | `cli/executor/host.go` | `limactl list --json` + `IsVMNameRunning/Present` |
| CLI wiring | `cli/dev.go` | `devBackend()` returns `*backend.LimaBackend`; `cmdAttach` asserts `*executor.SSHExecutor` |

`LimaBackend` implements `backend.Backend` (`Create`/`Destroy`/`Host`). Its `Host()` returns
`executor.NewSSHHost(instance, NewSSHExecutor(instance), LocalExecutor{}, ports, dataDirs)`.
The `LocalExecutor` argument exists only for `Ready()`'s `limactl list --json` poll.

Guest-side runtime is identical regardless of VM: podman + host-agent on Debian, with
ports host-agent 3000 / postgres 5432 / traefik 8080 / jellyfin 8096 / authentik 9000, and
`devRemoteDir = /var/tmp/bloud-dev-runtime`.

---

## 2. Generalize the Transport (executor refactor)

### Goal

One `SSHHost` + `SSHExecutor` path that works for both transports:

- **Lima transport** — shells via `limactl shell --start <instance> bash -c`, copies via
  `limactl copy`, interactive via `limactl shell`, ready via `limactl list --json`.
- **QEMU transport** — shells via `ssh -p <port> -i <key> <user>@127.0.0.1 bash -c`, copies
  via `rsync` (matches e2e precedent), interactive via `ssh -t`, ready via
  `ssh ... true`.

### `executor/ssh.go`

Rename the constructor and parameterize the command-building strategies:

```go
// Transport reaches a runtime guest (Lima via limactl, QEMU via ssh).
// SSHExecutor implements Transport.
type Transport interface {
    executor.Executor                  // Run, RunStream, CopyTo, CopyFrom
    InteractiveShell(ctx context.Context, stdout, stderr io.Writer, stdin io.Reader) error
    Ready() bool
}

type SSHExecutor struct {
    newCmd   func(ctx context.Context, name string, args ...string) *exec.Cmd
    runCmd   func(ctx context.Context, spec RunSpec) *exec.Cmd  // remote command
    copyTo   func(ctx context.Context, from, to string, recursive bool) *exec.Cmd
    copyFrom func(ctx context.Context, from, to string) *exec.Cmd
    inter    func(ctx context.Context) *exec.Cmd                 // interactive shell
    ready    func() bool                                         // guest reachability
}

// NewLimactlExecutor — existing Lima behavior (limactl shell/copy/list).
func NewLimactlExecutor(instance string) *SSHExecutor

// NewSSHExecutor — real ssh/rsync transport for a QEMU (or any) guest.
func NewSSHExecutor(conn SSHConn) *SSHExecutor
```

`SSHConn` carries the connection parameters:

```go
type SSHConn struct {
    Host    string // 127.0.0.1
    Port    int    // 2222 (hostfwd from guest :22)
    User    string // bloud
    KeyFile string // generated ephemeral key (project .bloud/qemu)
}
```

The shared `BuildRemoteScript(spec)` already renders a bash script; it works unchanged for
both transports (`bash -c <script>`).

### `executor/host.go`

Drop the `local`/`instance` fields — readiness moves onto the transport:

```go
type SSHHost struct {
    remote   Transport
    ports    map[string]string
    dataDirs DataDirs
}

func NewSSHHost(remote Transport, ports map[string]string, dataDirs DataDirs) *SSHHost
func (h *SSHHost) Executor() Executor { return h.remote } // Transport satisfies Executor
func (h *SSHHost) Ready() bool        { return h.remote.Ready() }
```

`IsVMNameRunning` / `IsVMNamePresent` / `vmStatus` stay in `host.go` — still used by
`LimaBackend.Create` and the Lima transport's `Ready()`.

### Caller updates

- `LimaBackend.Host()` → `executor.NewSSHHost(executor.NewLimactlExecutor(b.instance), ports, dataDirs)`.
- `cmdAttach` keeps its `*executor.SSHExecutor` type-assert — both backends return that concrete type.

---

## 3. QEMUBackend Lifecycle & Provisioning

### Runtime layout (project-scoped, gitignored)

Runtime artifacts live under `.bloud/qemu/<instance>/` (`.bloud/` is already gitignored):

```
.bloud/qemu/bloud-qemu/
├── debian-13-genericcloud-amd64.qcow2   # downloaded base image (cache)
├── bloud-qemu.qcow2                     # overlay boot disk (30GiB)
├── seed.iso                             # cloud-init NoCloud seed (built once)
├── id_ed25519 / id_ed25519.pub          # generated ephemeral SSH key
└── bloud-qemu.pid                       # qemu pidfile
```

### `backend/qemu.go`

```go
// QEMUBackend provisions and manages a QEMU VM via qemu-img / qemu-system-x86_64.
type QEMUBackend struct {
    instance   string
    projectDir string
    dir        string            // runtime dir: .bloud/qemu/<instance>
    arch       string            // "x86_64"
    newCmd     func(ctx context.Context, name string, args ...string) *exec.Cmd
}

func NewQEMUBackend(instance, projectDir string) *QEMUBackend
```

**Create(ctx)** — idempotent, four steps:

1. **Ensure disk image.** If `.qcow2` missing: download
   `debian-13-genericcloud-amd64.qcow2` from
   `https://cloud.debian.org/images/cloud/trixie/latest/...`, then create the overlay
   disk and resize to 30GiB:

   ```sh
   qemu-img create -f qcow2 -F qcow2 -b debian-13-genericcloud-amd64.qcow2 bloud-qemu.qcow2
   qemu-img resize bloud-qemu.qcow2 30G
   ```

   Debian cloud images auto-grow partition 1 on first boot via cloud-init.

2. **Ensure seed.** If `seed.iso` missing: generate an ephemeral SSH keypair
   (`ssh-keygen -t ed25519`), write `user-data` (cloud-config) + `meta-data`, and build a
   NoCloud `cidata` ISO:

   ```sh
   mkisofs -output seed.iso -volid cidata -joliet -rock user-data meta-data
   ```

   `user-data` mirrors `dev/lima.yaml`'s provision section: create user `bloud` with
   passwordless sudo + the generated `ssh_authorized_keys`, install
   `podman podman-compose golang-go unzip curl jq ldap-utils`, and run
   `loginctl enable-linger bloud` (user-level podman persists after ssh logout).
   `meta-data` carries `instance-id: <instance>` + `local-hostname: <instance>`.

3. **Ensure running.** If the pidfile process is alive and SSH is ready, skip launch.
   Otherwise launch headless QEMU in the background:

   ```sh
   qemu-system-x86_64 \
     -machine q35,accel=kvm -cpu max -m 4G -smp 4 \
     -drive file=bloud-qemu.qcow2,if=virtio \
     -drive file=seed.iso,media=cdrom,readonly=on \
     -netdev user,id=net0,hostfwd=tcp::2222-:22,hostfwd=tcp::3000-:3000, \
                hostfwd=tcp::5432-:5432,hostfwd=tcp::8080-:8080, \
                hostfwd=tcp::8096-:8096,hostfwd=tcp::9000-:9000 \
     -device virtio-net-pci,netdev=net0 \
     -display none -daemonize -pidfile bloud-qemu.pid
   ```

   `-accel kvm` requires access to `/dev/kvm` (add a preflight check — see §5). On
   hosts without `/dev/kvm`, fall back to TCG (`-accel tcg`), which is slow but works.

   Guest gets slirp DHCP (`10.0.2.15`); NoCloud needs no external metadata server.

4. **Wait for ready.** Poll SSH readiness (`ssh -p 2222 -i key -o StrictHostKeyChecking=accept-new -o ConnectTimeout=5 bloud@127.0.0.1 true`) until up (bounded retry). Return error if the guest never comes up.

**Destroy(ctx)** — kill the qemu process via the pidfile (`kill $(cat pid)` then `rm -f pid`), and remove the runtime dir. Matches Lima's `delete --force` semantics (full teardown).

**Host()** — returns the shared SSH host over the QEMU transport:

```go
func (b *QEMUBackend) Host() executor.Host {
    return executor.NewSSHHost(
        executor.NewSSHExecutor(executor.SSHConn{
            Host: "127.0.0.1", Port: 2222, User: "bloud", KeyFile: ...,
        }),
        map[string]string{ "host-agent": "3000", "postgres": "5432", "traefik": "8080",
            "jellyfin": "8096", "authentik": "9000" },
        executor.DataDirs{
            HostAgentDir: "/var/tmp/bloud-qemu-runtime/host-agent",
            DataDir:      "/var/tmp/bloud-qemu-runtime/data",
            AppsDir:      filepath.Join(b.projectDir, "apps"),
        },
    )
}
```

Ports and `devRemoteDir` mirror Lima exactly (same guest runtime, same host-agent env).

### Provisioning split

- **First boot (cloud-init seed, one-shot):** packages + user + lingering — the analogue
  of Lima's `provision` block.
- **After SSH up:** the CLI's `cmdDev` runs the existing compose/deploy steps unchanged.
  `dev/setup.sh` remains the manual bootstrap (init-secrets, LDAP plugin, blueprints,
  `.env`) — run once inside the guest, exactly as with Lima.

---

## 4. CLI Wiring (Parallel, Selectable)

### `cli/dev.go`

Change `devBackend()` to return the `backend.Backend` interface and branch on selection:

```go
func backendName() string {
    if v := os.Getenv("BLOUD_BACKEND"); v != "" { return v }
    return "lima" // default
}

func devBackend() (backend.Backend, error) {
    root, err := getProjectRoot()
    if err != nil { return nil, err }
    switch backendName() {
    case "qemu":
        return backend.NewQEMUBackend(qemuInstance(), root), nil
    default:
        return backend.NewLimaBackend(limaInstance(), root), nil
    }
}
```

- Add `qemuInstance()` (env `BLOUD_QEMU_INSTANCE`, default `bloud-qemu`), mirroring `limaInstance()`.
- `cmdDev` currently builds `backend.NewLimaBackend(...)` directly (not via `devBackend()`);
  switch it to `devBackend()` so the backend selection applies to `dev` too.
- `cmdStop/cmdStatus/cmdLogs/cmdServices/cmdReset/cmdAttach/cmdShell/cmdDestroy` already call
  `devBackend()` + `Host()`; they work against the interface. Only user-facing strings
  ("Lima VM", "limactl start") need generalizing.
- `cmdAttach` keeps `*executor.SSHExecutor` assertion (both backends return it).

### Selection surface

- Env var `BLOUD_BACKEND=qemu|lima` (default `lima`). Documented in help + README.
- Optional later: `./bloud dev --backend=qemu` flag. Keep env-var-only for the first pass
  to avoid threading a flag through every command.

---

## 5. Preflight / Setup / Help Text

- `cli/setup.go`: add `{"qemu-system-x86_64", "QEMU"}` to the prerequisite check list
  (Linux hosts), plus a `/dev/kvm` readability check (`-r /dev/kvm`). Print the QEMU
  quick-start alongside the Lima one.
- `cli/main.go` `printUsage()`: rename "Dev (Lima VM)" → "Dev (VM)" and make the
  quick-start section mention both `BLOUD_BACKEND` values.
- `cmdStart`/`cmdStatus`/`cmdReset`/`cmdDestroy` string literals: branch on `backendName()`
  for "Lima VM"/"QEMU VM"/"limactl start"/"qemu" phrasing (small helper).

---

## 6. Config Reference (`dev/qemu.yaml`)

Add a human-readable reference alongside `dev/lima.yaml` (the backend hardcodes the spec,
consistent with `lima.go`; the yaml documents it):

```yaml
# QEMU VM for Bloud local development (x86_64 via KVM on Linux).
instance: bloud-qemu
arch: x86_64
accel: kvm
machine: q35
cpus: 4
memory: 4GiB
disk: 30GiB
image:
  url: "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2"
ssh:
  host: 127.0.0.1
  port: 2222      # hostfwd from guest :22
  user: bloud
portForwards:
  - guestPort: 22     # ssh -> host 2222
  - guestPort: 3000   # host-agent
  - guestPort: 5432   # postgres
  - guestPort: 8080   # traefik
  - guestPort: 8096   # jellyfin
  - guestPort: 9000   # authentik
provision:
  - mode: cloud-init  # NoCloud seed: packages + bloud user + lingering
```

---

## 7. E2E Integration

- e2e already supports `BLOUD_E2E_SSH_TARGET` (generic `ssh <target> bash -se --` +
  `rsync`). With QEMU, point e2e at the guest via that variable (e.g. `bloud@127.0.0.1`)
  — or extend `sshTarget` to accept an optional port/key once QEMU SSH is stable.
- Default e2e path stays Lima (`BLOUD_E2E_LIMA_INSTANCE`) — unchanged.

---

## 8. Tests

- `cli/backend/qemu_test.go` — mirror `lima_test.go`: fake `qemu-img`/`qemu-system-x86_64`
  and record invocations. Cover:
  - Create when image+seed+guest already running (no-op).
  - Create when guest stopped → relaunch qemu + wait for ready.
  - Create when disk missing → download + `qemu-img create/resize`.
  - Create when seed missing → ssh-keygen + mkisofs.
  - Destroy → kill pidfile process, rm runtime dir.
  - Host() → ports/dataDirs match Lima's map.
- `cli/executor/ssh_test.go` — update for the generalized `SSHExecutor`:
  - `NewLimactlExecutor` builds `limactl shell --start` (existing expectations).
  - `NewSSHExecutor` builds `ssh -p 2222 -i key -o StrictHostKeyChecking=accept-new ...`
    for Run/RunStream, `rsync -a` for CopyTo/CopyFrom, `ssh -t` for InteractiveShell, and
    `ssh ... true` for Ready().
- `cli/executor/sshhost_test.go` — update `SSHHost` for the new constructor
  (transport instead of instance/local/remote).
- `cli/executor/host.go` `IsVMNameRunning` helpers — unchanged (Lima-only), keep tests.
- `cli/backend/lima_test.go` — update `fakeLimaBackend` `Host()` expectation if the
  constructor signature changed (ports/dataDirs assertions unchanged).

---

## Files

| File | Change |
|------|--------|
| `cli/backend/backend.go` | No change (interface already fits QEMU) |
| `cli/backend/qemu.go` | New: `QEMUBackend` (Create/Destroy/Host) |
| `cli/backend/qemu_test.go` | New: lifecycle + Host tests |
| `cli/executor/ssh.go` | Generalize `SSHExecutor`; add `Transport`, `SSHConn`, `NewLimactlExecutor`, `NewSSHExecutor` |
| `cli/executor/ssh_test.go` | Tests for both transports |
| `cli/executor/host.go` | `SSHHost` holds `Transport`; drop local/instance; `Ready()` delegates |
| `cli/executor/sshhost_test.go` | Update constructor |
| `cli/backend/lima.go` | `Host()` uses `NewLimactlExecutor`; drop `LocalExecutor` arg |
| `cli/backend/lima_test.go` | Update `Host()` expectation |
| `cli/dev.go` | `devBackend()` returns interface + `backendName()`; `cmdDev` uses `devBackend()`; `qemuInstance()`; generalize strings |
| `cli/setup.go` | Add QEMU preflight check + quick-start |
| `cli/main.go` | Generalize help text ("Dev (VM)") |
| `dev/qemu.yaml` | New reference config |
| `docs/architecture/overview.md` | Note the second backend + env selection |

---

## Dependency Graph

```
1. Transport generalization (executor)   ─ standalone, no deps
   └─ required by (2) and (3)

2. QEMUBackend lifecycle (backend/qemu.go)
   └─ depends on (1): Host() uses NewSSHExecutor

3. CLI wiring (selectable backend)
   └─ depends on (2): devBackend() branches to QEMUBackend
   └─ depends on (1): cmdAttach still asserts *SSHExecutor
```

Order: (1) first, then (2), then (3). (1) is a pure refactor with no behavior change and
can land alone. (2) builds the VM lifecycle on top. (3) flips selection.

---

## Verification

```bash
# After (1) — transport refactor, no behavior change:
cd cli && go test ./... -count=1

# After (2) — QEMU backend:
#   BLOUD_BACKEND=qemu ./bloud dev        → creates .bloud/qemu/bloud-qemu/, boots VM
#   qemu-img info .bloud/qemu/bloud-qemu/bloud-qemu.qcow2   → 30GiB, backing file set
#   ssh -p 2222 -i .bloud/qemu/bloud-qemu/id_ed25519 bloud@127.0.0.1 \
#       'podman version'                    → podman installed via cloud-init
#   ./bloud status                          → VM running + host-agent running (qemu branch)
#   ./bloud attach                          → interactive ssh shell into guest

# After (3) — selection:
#   default ./bloud dev                     → Lima (unchanged)
#   BLOUD_BACKEND=qemu ./bloud dev          → QEMU
#   BLOUD_BACKEND=qemu ./bloud destroy      → kills qemu, rm .bloud/qemu

# Full regression:
cd cli && go test ./... -count=1
cd services/host-agent && go build ./... && go test ./... -count=1
```
