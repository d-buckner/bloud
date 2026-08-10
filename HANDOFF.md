# Bloud Dev Environment — Handoff

## Current State

The dev environment is **partially running** with several issues that need resolution.

### What's Working
- ✅ Lima VM `bloud-dev` is running (Debian 13, aarch64, vz)
- ✅ Host-agent binary built and deployed to VM at `/var/tmp/bloud-dev-runtime/host-agent/`
- ✅ Frontend served on `http://localhost:3000`
- ✅ System apps auto-installed in database: `traefik`, `authentik`
- ✅ Authentik sub-containers running:
  - `apps-authentik-postgres` (RUNNING)
  - `apps-authentik-redis` (RUNNING)
  - `apps-authentik-worker` (RUNNING)

### What's Broken

#### 1. Traefik Health Check Failing
- **Error**: `podman exec apps-traefik [/bin/sh -c wget -qO- http://localhost:8080/ping | grep -q ok]` exits with status 1
- **Why**: Health check command format issue — the `CMD-SHELL` wrapper is being converted correctly, but Traefik might not be listening or the command needs adjustment
- **Impact**: Traefik container shows as FAILED in orchestrator, blocks dependent apps

#### 2. Authentik Server Container Removal Error
- **Error**: 
  ```
  ensure container: remove container returned status 500: 
  container already exists
  ```
- **Why**: Orchestrator tries to remove container before creating, but container from previous run still exists with dependent containers
- **Impact**: Authentik server can't be recreated, blocks LDAP and worker containers

#### 3. Redis Connectivity (Host-Agent → Container)
- **Error**: `dial tcp [::1]:6379: connect: connection refused`
- **Why**: Host-agent connects to `localhost:6379` but Redis is inside podman network as `apps-authentik-redis`
- **Impact**: Authentication disabled, sessions broken
- **Fix needed**: Set `BLOUD_REDIS_ADDR=apps-authentik-redis:6379` or expose Redis port

#### 4. Authentication Disabled
- **Log**: `authentication disabled (missing Authentik client, Redis, or base URL)`
- **Why**: Redis unreachable (see #3)
- **Impact**: No SSO, no session management

## Fixes Already Applied

### File: `services/host-agent/internal/api/router.go`
- **Line 128**: Pass `catalogCache` to `initOrchestratorHelper`
- **Line 281**: Add `catalogCache catalog.CacheInterface` parameter to `initOrchestratorHelper` signature
- **Line 353**: Pass `catalogCache` to `NewOrchestrator` instead of `nil`

### File: `services/host-agent/internal/orchestrator/orchestrator.go`
- **Lines 3-19**: Added `"strings"` import
- **Lines 598-601**: Skip "host" network in `ensureContainerFromDef` (network mode, not bridge)
- **Lines 622-629**: Skip file mounts in `os.MkdirAll` (check for `.yml`, `.yaml`, `.json`, `.conf` extensions)
- **Lines 770-795**: Added `convertHealthCheckTest()` function to convert Docker-style health checks to podman exec format:
  - `["CMD-SHELL", "cmd"]` → `["/bin/sh", "-c", "cmd"]`
  - `["CMD", "exec", "arg"]` → `["exec", "arg"]`
- **Line 803**: Use `convertHealthCheckTest(hc.Test)` instead of `hc.Test` directly

## Key Files Modified

```
services/host-agent/internal/api/router.go
services/host-agent/internal/orchestrator/orchestrator.go
```

## Environment Details

### VM Shell Access
```bash
# Use sh -c wrapper (single-quoted commands fail)
limactl shell bloud-dev sh -c 'command'

# Check containers
limactl shell bloud-dev sh -c 'podman ps -a'

# Check logs
limactl shell bloud-dev sh -c 'journalctl --user -u host-agent -f'
```

### Paths Inside VM
- Host-agent: `/var/tmp/bloud-dev-runtime/host-agent/`
- Data: `/var/tmp/bloud-dev-runtime/data/`
- Secrets: `/var/tmp/bloud-dev-runtime/data/secrets.json`
- Traefik dynamic routes: `/var/tmp/bloud-dev-runtime/data/traefik/dynamic/`

### Port Mapping (Host → VM)
- 3000 → host-agent API
- 5432 → postgres
- 8080 → traefik
- 8096 → jellyfin
- 9000 → authentik

## Next Steps

### Priority 1: Fix Redis Connectivity
The host-agent needs to connect to Redis inside the podman network. Options:
1. Set environment variable: `BLOUD_REDIS_ADDR=apps-authentik-redis:6379`
2. Or expose Redis port through Lima (add to `dev/lima.yaml` port forwards)
3. Or run Redis on host and mount into VM

### Priority 2: Fix Traefik Health Check
- Check if Traefik container is actually running: `limactl shell bloud-dev sh -c 'podman inspect apps-traefik'`
- Verify Traefik config exists: `limactl shell bloud-dev sh -c 'cat /var/tmp/bloud-dev-runtime/data/traefik/traefik.yml'`
- Try manual health check: `limactl shell bloud-dev sh -c 'podman exec apps-traefik wget -qO- http://127.0.0.1:8080/ping'`
- The health check might need to use `127.0.0.1` instead of `localhost` (DNS resolution in containers)

### Priority 3: Fix Container Removal
The `EnsureContainer` function needs to handle "already exists" error gracefully:
- Check if container exists before removing
- Or use `podman rm -f` with error handling
- Or skip removal if container exists and is running

### Priority 4: Manual Workaround (While Debugging)
Skip auth by temporarily commenting out Redis check in `server.go` or start a Redis container manually:
```bash
limactl shell bloud-dev sh -c 'podman run -d --name dev-redis redis:7-alpine'
```

## Testing Commands

```bash
# Check host-agent health
curl -s http://localhost:3000/api/health

# Check installed apps
curl -s http://localhost:3000/api/apps | jq

# Check orchestrator status
curl -s http://localhost:3000/api/orchestrator/status | jq

# View logs
limactl shell bloud-dev sh -c 'journalctl --user -u host-agent -f'
```

## Architecture Reference

```
Host (macOS)
  └── Lima VM (bloud-dev)
        └── host-agent (Go, port 3000)
              ├── Manages containers via Podman API
              ├── Orchestrator (app lifecycle)
              ├── Traefik route generation
              └── Web dashboard (Svelte)
        └── Podman containers:
              ├── apps-traefik (port 8080, host network)
              ├── apps-authentik-postgres
              ├── apps-authentik-redis
              ├── apps-authentik-server
              ├── apps-authentik-worker
              ├── apps-authentik-ldap
              └── apps-jellyfin (when installed)
```

## Useful Commands

```bash
# Restart dev environment
./bloud dev

# Reset everything (keeps VM)
./bloud reset && ./bloud dev

# Destroy VM
./bloud destroy
limactl create --name=bloud-dev dev/lima.yaml
limactl start bloud-dev
limactl shell bloud-dev bash dev/setup.sh
./bloud dev

# Check VM status
limactl list
```

## Documentation

- `AGENTS.md` — Quick reference for dev setup
- `dev/setup.sh` — One-time VM provisioning script
- `dev/compose.yml` — Reference podman-compose stack (not used by `./bloud dev`)
- `services/host-agent/` — Host-agent source code
- `cli/dev.go` — `./bloud dev` implementation
