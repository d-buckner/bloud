# CLI Backend Abstraction

**Status:** Draft  
**Created:** 2026-08-09  
**Authors:** Daniel

## Problem

The `./bloud` CLI is tightly coupled to Lima as the only supported runtime environment. This limits:
- Linux users (no Lima on Linux by default)
- macOS users without Virtualization framework access
- Developers who prefer native Debian/Ubuntu boxes
- Future backends (Docker, WSL, Kubernetes)

Every dev command in `cli/dev.go` hardcodes Lima operations: `limactl shell`, `limactl copy`, port forwarding assumptions, and VM lifecycle management.

## Solution

Introduce a **backend abstraction** that separates:
1. **Runtime host** — how to communicate with the running environment
2. **Backend** — how to provision and manage the environment

This lets the host-agent and CLI work with any backend without Lima-specific code in the core.

## Architecture

### Three Layers

```
┌─────────────────────────────────────────┐
│           CLI Commands                  │
│  (dev, status, stop, install, etc.)     │
└────────────────┬────────────────────────┘
                 │ uses
                 ▼
┌─────────────────────────────────────────┐
│            Backend Layer                │
│  (LimaBackend, NativeBackend, ...)      │
│  - Provision: create/destroy/verify     │
│  - Describe: ports, data dirs           │
│  - Provide: runtime Host                │
└────────────────┬────────────────────────┘
                 │ provides
                 ▼
┌─────────────────────────────────────────┐
│            Host Layer                   │
│  (SSHHost, LocalHost, ...)              │
│  - Executor()  : run commands           │
│  - Ports()     : port mapping           │
│  - DataDirs()  : volume locations       │
│  - Ready()     : health check           │
└────────────────┬────────────────────────┘
                 │ provides
                 ▼
┌─────────────────────────────────────────┐
│           Executor Layer                │
│  (SSHExecutor, LocalExecutor, ...)      │
│  - Run()     : execute command          │
│  - CopyFrom() : download file           │
│  - CopyTo()  : upload file              │
└─────────────────────────────────────────┘
```

### Layer 1: Executor

**Purpose:** Command execution and file transfer abstraction.

**Interface:**
```go
type Executor interface {
    Run(ctx context.Context, spec RunSpec) (ExecResult, error)
    CopyFrom(ctx context.Context, from, to string) error
    CopyTo(ctx context.Context, from, to string) error
}

type RunSpec struct {
    Command string
    Args    []string
    Env     map[string]string
    Stdin   io.Reader
    Dir     string
    AsRoot  bool
}

type ExecResult struct {
    ExitCode int
    Stdout   string
    Stderr   string
}
```

**Implementations:**
- `LocalExecutor` — wraps `os/exec` + `os` filesystem (for native backends)
- `SSHExecutor` — wraps SSH for commands + scp for files (for Lima, QEMU, etc.)
- Future: `DockerExecutor`, `KubeExecutor`, `PodmanExecutor`

**Why not "Shell"?** "Shell" implies terminal semantics. "Executor" is clearer: it executes commands and transfers files. SSH is an implementation detail.

### Layer 2: Host

**Purpose:** Runtime environment description.

**Interface:**
```go
type Host interface {
    Executor() Executor
    Ports() map[string]string
    DataDirs() DataDirs
    Ready() bool
}

type DataDirs struct {
    HostAgentDir string  // where host-agent binary lives
    DataDir      string  // app data, secrets, traefik config
    AppsDir      string  // mounted apps directory
}
```

**Implementations:**
- `SSHHost` — wraps an SSHExecutor, returns ports/dirs from Lima config
- `LocalHost` — wraps a LocalExecutor, returns paths from env vars

**Key insight:** The host-agent doesn't care how the host was provisioned. It just calls `host.Executor().Run(ctx, spec)` to execute setup scripts, install apps, manage containers.

### Layer 3: Backend

**Purpose:** Provisioning and lifecycle management.

**Interface:**
```go
type Backend interface {
    Create(ctx context.Context) error
    Destroy(ctx context.Context) error
    Host() Host
}
```

**Implementations:**
- `LimaBackend` — wraps `limactl`, provisions Debian VM with Podman
- `NativeBackend` — provisions a real Debian box with podman, systemd service
- Future: `DockerBackend`, `QEMUBackend`, `KubeBackend`

**Responsibilities:**
1. Check/install prerequisites (lima, podman, etc.)
2. Create and provision the environment (run setup.sh, configure services)
3. Provide a runtime Host once ready
4. Clean up on destroy

## Existing Code → Migration Path

### Current State

`cli/dev.go` has a `Host` interface and `Lima` struct:

```go
type Host interface {
    SSH(ctx context.Context) (io.Closer, error)
    CP(localPath, remotePath string) error
    VMRunning() bool
    VMReady() bool
    Setup(ctx context.Context) error
    Ports() map[string]string
    DataDirs() DataDirs
    Destroy(ctx context.Context) error
    Stop(ctx context.Context) error
    Start(ctx context.Context) error
}

type Lima struct {
    projectDir string
}

func (l *Lima) SSH(ctx context.Context) (io.Closer, error) { ... }
func (l *Lima) CP(localPath, remotePath string) error { ... }
// ... etc
```

This is **partially correct** — the `Host` interface exists but mixes concerns:
- Runtime communication (SSH, CP)
- Lifecycle management (Setup, Start, Stop, Destroy)
- Description (Ports, DataDirs)

### Migration Plan

**Phase 1: Introduce Executor**
1. Create `cli/executor/` package with `Executor`, `RunSpec`, `ExecResult` types
2. Create `LocalExecutor` (os/exec wrapper)
3. Refactor `Lima.SSH()` and `Lima.CP()` into `SSHExecutor`
4. Update `cli/vm/exec.go` to use Executor instead of direct SSH calls

**Phase 2: Refactor Host**
1. Redefine `cli/executor/Host` to use `Executor() Executor` instead of `SSH()`/`CP()`
2. Create `SSHHost` that wraps `SSHExecutor`
3. Create `LocalHost` that wraps `LocalExecutor`
4. Update all callers to use `host.Executor()` instead of `host.SSH()`/`host.CP()`

**Phase 3: Introduce Backend layer**
1. Create `cli/backend/` package with `Backend` interface
2. Create `LimaBackend` that wraps the current `Lima` struct
3. Move lifecycle methods (Setup, Start, Stop, Destroy) from `Host` to `Backend`
4. Update CLI commands to use `Backend` instead of direct `Host`

**Phase 4: Add native backend**
1. Create `NativeBackend` for direct Debian/Ubuntu installation
2. Implement `LocalHost` with local executor
3. Test on a real Linux box

## Examples

### LimaBackend

```go
type LimaBackend struct {
    projectDir string
}

func (b *LimaBackend) Create(ctx context.Context) error {
    // 1. Check lima installed
    // 2. Check/create VM (limactl list, limactl start)
    // 3. Verify VM is ready
    // 4. Return SSHHost
}

func (b *LimaBackend) Destroy(ctx context.Context) error {
    // limactl delete bloud-dev
}

func (b *LimaBackend) Host() Host {
    return &SSHHost{
        executor: SSHExecutor{...},
        ports: map[string]string{
            "host-agent": "3000",
            "postgres":   "5432",
            "traefik":    "8080",
            "jellyfin":   "8096",
            "authentik":  "9000",
        },
        dataDirs: DataDirs{
            HostAgentDir: "/var/tmp/bloud-dev-runtime/host-agent",
            DataDir:      "/var/tmp/bloud-dev-runtime/data",
            AppsDir:      "/Users/daniel/Projects/bloud/apps",
        },
    }
}
```

### NativeBackend (hypothetical)

```go
type NativeBackend struct {
    host string  // hostname or IP for the Debian box
}

func (b *NativeBackend) Create(ctx context.Context) error {
    // 1. Check podman installed on remote box
    // 2. Run setup.sh on remote box via SSH
    // 3. Start host-agent as systemd service
    // 4. Return LocalHost (host-agent runs on same machine)
}

func (b *NativeBackend) Host() Host {
    return &LocalHost{
        executor: LocalExecutor{...},
        ports: map[string]string{
            "host-agent": "3000",
            "postgres":   "5432",
            "traefik":    "8080",
            "jellyfin":   "8096",
            "authentik":  "9000",
        },
        dataDirs: DataDirs{
            HostAgentDir: "/opt/bloud/host-agent",
            DataDir:      "/var/lib/bloud",
            AppsDir:      "/opt/bloud/apps",
        },
    }
}
```

## Future Backends

### DockerBackend

```go
type DockerBackend struct {
    dockerHost string  // e.g., "localhost" or "192.168.1.100"
}

func (b *DockerBackend) Host() Host {
    return &DockerHost{
        executor: DockerExecutor{...},
        ports:    map[string]string{...},
        dataDirs: DataDirs{...},
    }
}
```

### QEMUBackend (Linux)

```go
type QEMUBackend struct {
    config QEMUConfig
}

func (b *QEMUBackend) Create(ctx context.Context) error {
    // 1. Check QEMU installed
    // 2. Create/verify VM
    // 3. Wait for SSH to be available
    // 4. Return SSHHost
}
```

## Benefits

1. **Developer experience:** Linux users can run natively without Lima
2. **Flexibility:** Future backends (Docker, k8s) plug in cleanly
3. **Testability:** Local executor makes unit tests easier
4. **Separation of concerns:** Runtime code doesn't know about provisioning
5. **Incremental migration:** Can migrate Lima first, then add other backends

## Risks

1. **Complexity:** Three layers might be overkill for now
   - Mitigation: Start with just Lima, add native later
2. **SSH overhead:** Local executor avoids SSH for native, but Lima still needs it
   - Mitigation: Acceptable tradeoff for flexibility
3. **Migration effort:** Refactoring existing code takes time
   - Mitigation: Do it incrementally, keep Lima working throughout

## Decision Points

**Decision 1: Layer count**
- Three layers (Executor → Host → Backend) vs. two (Host → Backend)
- Current draft: three layers
- Rationale: Executor is a distinct concern (how to communicate) from Host (what's available)

**Decision 2: Name "Executor"**
- "Executor" vs. "Shell" vs. "Transport" vs. "CommandBus"
- Current draft: "Executor"
- Rationale: Clear, unambiguous, doesn't imply terminal semantics

**Decision 3: Migration order**
- Refactor existing Lima code first, then add native
- Or design native first, refactor to match
- Current draft: refactor Lima first (less risk)

## Next Steps

1. Get approval on this spec
2. Create implementation plan with tasks
3. Phase 1: Introduce Executor
4. Phase 2: Refactor Host
5. Phase 3: Introduce Backend layer
6. Phase 4: Add NativeBackend
