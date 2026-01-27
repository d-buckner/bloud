# LDAP Infrastructure Design Investigation

## Goal
Make LDAP infrastructure part of core Authentik system, always present, so that LDAP apps (like Jellyfin) can be installed without timing issues or manual intervention.

## Problem Statement
Currently, installing Jellyfin with LDAP SSO fails because:
1. LDAP outpost container starts before LDAP infrastructure exists in Authentik
2. The prestart script waits/polls for infrastructure that was never created
3. API calls to create infrastructure fail with "Token invalid/expired"

## Desired End State
```
./bloud test start
# Wait for system ready
POST /api/apps/jellyfin/install
# Jellyfin installs successfully, LDAP already available
```

---

## Investigation 1: Systemd Timings and Dependencies

**Question**: When exactly do prestart/poststart run relative to systemd service states and `After=` dependencies?

**Files to read**:
- [ ] `nixos/lib/podman-service.nix` - mkPodmanService helper
- [ ] Generated systemd service files in VM

**Findings**:

`mkPodmanService` creates services with `Type=notify` and `NotifyAccess=all`. The container uses `--sdnotify=conmon` so conmon sends the ready notification when the container is running.

For `After=` dependencies: systemd waits for the dependency to reach "started" state. For `Type=notify`, this means waiting until sd_notify is received (container running). **It does NOT wait for ExecStartPost to complete.**

This means if Service B has `After=Service A`, Service B's ExecStartPre can begin as soon as Service A's container is running, even if A's ExecStartPost is still executing.

---

## Investigation 2: Prestart/Poststart Implementation

**Question**: How does `mkPodmanService` implement prestart/poststart? How do they relate to Go configurators?

**Files to read**:
- [x] `nixos/lib/podman-service.nix` - mkPodmanService helper
- [x] `apps/authentik/module.nix` - how Authentik uses prestart/poststart
- [ ] `services/host-agent/pkg/configurator/` - Go configurator interface

**Findings**:

`ExecStartPre` runs these steps in order (all before container starts):
1. `podman rm -f <name>` - cleanup (with `-` prefix so failure OK)
2. Health check script (if `waitFor` specified) - polls until deps ready
3. Bloud StaticConfig script (if `bloudAppName` set) - calls `host-agent configure static <appName>`
4. Custom `preStartScript` (if provided)

`ExecStartPost` runs after container starts:
1. Bloud DynamicConfig script (if `bloudAppName` set) - calls `host-agent configure dynamic <appName>`

**For LDAP outpost currently:**
- Has `waitFor` that checks authentik-server health: `curl -sf http://localhost:9000/-/health/ready/`
- Has custom `preStartScript` that queries Django shell for outpost token
- Waits up to 2 minutes for outpost to be created (by blueprint)
- Writes token to `envFile`, container reads it via `--env-file=`

**Key insight**: The `envFile` parameter works because `mkPodmanService` passes `--env-file=${envFile}` to `podman run`. The prestart script writes to this file before the container starts.

---

## Investigation 3: Authentik Token Management

**Question**: Where does the bootstrap token come from? How is it created? Why is the API rejecting it?

**Files to read**:
- [ ] `apps/authentik/module.nix` - AUTHENTIK_BOOTSTRAP_TOKEN env var
- [ ] `services/host-agent/internal/config/config.go` - BLOUD_AUTHENTIK_TOKEN
- [ ] `services/host-agent/internal/authentik/client.go` - how token is used

**To test in VM**:
- [x] Check if token exists in Authentik database
- [x] Check Authentik logs for bootstrap token creation
- [x] Test API with different token formats

**Findings**:

1. **Bootstrap token does NOT exist in database**
   - Only token present is the auto-generated outpost token
   - No token for akadmin user despite `AUTHENTIK_BOOTSTRAP_TOKEN` being set

2. **Bootstrap completes suspiciously fast (5ms)**
   ```
   "Starting authentik bootstrap" timestamp: 1768431619.9336169
   "Finished authentik bootstrap" timestamp: 1768431619.938796
   ```
   This suggests bootstrap was skipped (thought data already existed?)

3. **Manually created tokens work fine**
   - Created token via Django shell: `Token.objects.get_or_create(...)`
   - API calls with that token succeed

4. **Per [Authentik docs](https://docs.goauthentik.io/install-config/automated-install)**:
   - `AUTHENTIK_BOOTSTRAP_TOKEN` only read on first startup
   - The string IS the token key for API auth
   - Known issue [#7546](https://github.com/goauthentik/authentik/issues/7546): bootstrap vars not applied in some cases

**Root cause**: Bootstrap token creation is unreliable. We cannot depend on it.

**Solution**: Create the API token programmatically in Authentik's DynamicConfig phase, which runs after the container is healthy.

**Update (2026-01-15)**: Fresh test environment investigation:
- `AUTHENTIK_BOOTSTRAP_TOKEN` env var IS being passed correctly
- Bootstrap runs in ~5ms (very fast, no detailed logs)
- Our Django shell code creates `bloud-api-token` successfully
- Token with bootstrap key exists in DB (created by our code)

**Outstanding question**: Would bootstrap create a token if we removed our Django shell code? Need to test:
1. Remove Django shell token creation
2. Fresh start
3. Check if bootstrap creates token
See roadmap.md tech debt item for follow-up.

---

## Design Decisions

### Decision 1: LDAP is Core Infrastructure

LDAP infrastructure is always present as part of Authentik, not created on-demand.

**Rationale**:
- Eliminates timing issues between app install and LDAP setup
- Matches mental model: "Authentik provides SSO" (OAuth + LDAP)
- Similar to how embedded outpost is always present for forward-auth
- Minimal overhead (one additional container)

### Decision 2: API Token Created in Authentik DynamicConfig

The Authentik Go configurator creates the API token in its DynamicConfig phase.

**Rationale**:
- `AUTHENTIK_BOOTSTRAP_TOKEN` is unreliable (known issues, first-boot-only)
- DynamicConfig runs after container is healthy, guarantees Authentik is ready
- Single place for Authentik initialization logic
- Idempotent (check if exists, create if not)

### Decision 3: LDAP Setup in LDAP Outpost StaticConfig

The LDAP outpost's StaticConfig phase:
1. Calls Authentik API to ensure LDAP infrastructure exists
2. Queries for outpost token
3. Writes token to env file for container

**Rationale**:
- StaticConfig runs before container starts, so token is available
- `waitFor` already ensures Authentik is healthy before StaticConfig runs
- API calls are idempotent (ensure = create if not exists)
- No separate oneshot service needed

### Decision 4: Token Passed via Env File

LDAP outpost token written to file, container reads via `--env-file=`.

**Rationale**:
- mkPodmanService already supports `envFile` parameter
- PreStart writes file, ExecStart reads it (correct ordering)
- Secure (file permissions 600)

---

## Implementation Plan

### Phase 1: Fix Token Creation

1. **Modify Authentik configurator DynamicConfig** (`apps/authentik/configurator.go`)
   - After health check, ensure API token exists via Django shell
   - Token identifier: `bloud-api-token`
   - Token key: read from config or generate deterministically

2. **Store token for host-agent**
   - Write token to `$DATA_DIR/authentik/api-token`
   - Host-agent reads this on startup

### Phase 2: LDAP as Core Infrastructure

3. **Enable LDAP by default** (`apps/authentik/module.nix`)
   - Change `ldap.enable` default to `true`
   - LDAP outpost always runs as part of Authentik

4. **Modify LDAP StaticConfig** (`apps/authentik/module.nix`)
   - Replace "wait for blueprint" with "ensure via API"
   - Call host-agent or use curl to create LDAP infrastructure
   - Query outpost token, write to env file

### Phase 3: Expose API Endpoint

5. **Add internal API endpoint** (`services/host-agent/internal/api/`)
   - `POST /api/internal/ensure-ldap-infrastructure`
   - Calls existing `EnsureLDAPInfrastructure` method
   - Used by LDAP prestart script

### Phase 4: Test End-to-End

6. **Test fresh install**
   ```
   ./bloud test start
   # Wait for ready
   POST /api/apps/jellyfin/install
   # Verify LDAP works, no manual intervention
   ```
