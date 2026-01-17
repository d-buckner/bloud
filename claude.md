# Claude Code Guidelines for bloud-v3

## Critical: Development Environment Access

**ALWAYS access the application through port 8080 (Traefik). NEVER access Vite directly on port 5173.**

- All browser access: `http://localhost:8080`
- Service worker is registered on port 8080
- Iframe content is served from port 8080
- Everything is same-origin (8080)

Do NOT suggest or assume port 5173 is being used. Do NOT suggest proxying through Vite. The architecture is: Browser → Traefik (8080) → Vite/Apps.

## Debugging Principles

**THIS IS NON-NEGOTIABLE. Do not skip these steps.**

### Always Gather Evidence First
Before proposing any fix or making claims about root causes:
1. Gather actual evidence by running commands, adding logs, and observing output
2. Explain what evidence was gathered and what it shows
3. Walk through the reasoning step by step
4. Only then propose changes, with clear justification tied to the evidence

### Never Guess - Theory Without Data is Worthless
- Do not propose changes based on assumptions or theories
- Do not claim to know the cause without evidence
- If asked "why is this needed?", have concrete evidence ready
- A plausible-sounding theory is NOT evidence

### Anti-Pattern: Theorizing Without Data
**NEVER do this:**
```
"The issue is probably X because Y could happen" → proposes code change
```

**ALWAYS do this:**
```
"I suspect X. Let me add logging to verify" → gathers data → shows output →
confirms/refutes theory with evidence → THEN proposes fix
```

**Real example of what NOT to do:**
- User reports 404 errors on /api/v3/* requests
- BAD: "The issue is the SW update clears the clientAppMap" → proposes fix
- GOOD: "Let me add debug logging to see what clientId and clientApp values are" →
  observes: before SW update clientApp='radarr', after update clientApp=null →
  "Evidence confirms the SW update clears the map" → proposes fix

### Explain Before Executing
When debugging:
1. State what you're checking and why
2. Run the command or add the logging
3. Explain what the output means
4. Then decide next steps

## File Permissions

**IMPORTANT:** Always create new files with 644 permissions (readable by all). The Lima VM dev environment syncs files from the host, and files without read permissions will fail to sync.

## Project Structure

### Main Entry Point
- `nixos/bloud.nix` - The primary module for local testing with rootless podman

### Key Scripts
- `nixos/bloud-test-integration` - Cleanup + rebuild + start services
- `bloud-test-integration` (installed command) - Run integration tests
- `bloud-test` (installed command) - Show service URLs and usage info

### App Modules
Located in `apps/<name>/` with each app having:
- `metadata.yaml` - App catalog info (name, description, integrations, etc.)
- `module.nix` - NixOS module for the app
- `configure.go` - Go configurator for runtime integrations

Current implemented apps (14 total):
- **Infrastructure:** postgres, redis, traefik, authentik
- **Media:** jellyfin, jellyseerr
- **Arr stack:** prowlarr, radarr, sonarr, qbittorrent
- **Productivity:** miniflux, actual-budget, affine
- **Network:** adguard-home

### Helper Library
- `nixos/lib/podman-service.nix` - Creates systemd user services for podman containers
- `nixos/lib/authentik-blueprint.nix` - Generates Authentik OAuth2 blueprints

## Rootless Podman Notes

### Service States
- Services can be in `failed` state from previous runs
- `inactive/dead` means not started, not necessarily broken
- Check `journalctl --user -u <service>` for actual errors

### Debugging Steps
1. Check service status: `systemctl --user list-units 'podman-*.service' --all`
2. Check logs: `journalctl --user -u podman-<name>.service`
3. Check container state: `podman ps -a`
4. Check from container's UID perspective: `podman unshare ls -la <path>`

### Common Issues
- Stale data with wrong permissions from previous runs
- Services staying in failed state after cleanup (need manual restart or rebuild)
- UID mapping: host user maps to root inside container with rootless podman

### UID Mapping Details
With rootless podman, UIDs are remapped:
- Host UID 1000 (daniel) → Container UID 0 (root)
- Container UID 1000 → Host UID 100999 (from subuid range)

**Problem**: Containers running as non-root users (e.g., Authentik runs as UID 1000) can't write to directories owned by the host user.

**Solution**: Use `--userns=keep-id` which maps Host UID 1000 → Container UID 1000 (preserves UID).

### Cleanup with Container-Owned Files
Files created by containers may be owned by mapped UIDs that the host user can't delete.

**Solution**: Use `podman unshare rm -rf <path>` to delete from the container's UID namespace.

## Dependency Management

### systemd Dependencies
- `after` + `wants` = ordering only, doesn't wait for health
- `requires` = hard dependency, service fails if dependency fails
- For oneshot services with `RemainAfterExit=true`, dependent services wait for completion

### Health Checks
The `mkPodmanService` helper supports:
- `waitFor` - list of `{container, command}` to health check before starting
- `extraAfter` / `extraRequires` - additional systemd dependencies

Example:
```nix
mkPodmanService {
  name = "my-app";
  waitFor = [
    { container = "postgres"; command = "pg_isready -U user"; }
    { container = "redis"; command = "redis-cli ping"; }
  ];
  extraAfter = [ "my-init.service" ];
  extraRequires = [ "my-init.service" ];
}
```

### Testing Dependency Graph
```bash
# Check generated service file
systemctl --user cat podman-<name>.service

# Show dependency tree
systemctl --user list-dependencies podman-<name>.service

# Verify service configuration
systemd-analyze --user verify podman-<name>.service
```

## Architecture Decisions

### Shared Resource Architecture

**Design Principle:** Each Bloud host runs a maximum of **one instance** of each core infrastructure service:
- **1 PostgreSQL instance per host** - All apps requiring PostgreSQL share this single instance
- **1 Redis instance per host** - All apps requiring Redis share this single instance (currently used by Authentik)
- **1 Restic instance per host** - Single backup service for all app data (not yet implemented)

**Benefits:**
- Resource efficiency: Lower RAM and CPU usage vs. per-app instances
- Simplified operations: One service to monitor, backup, and maintain
- Better performance: Shared connection pooling and caching
- Data consistency: Single source of truth

**Implementation:**
- Apps connect via environment variables to shared services
- NixOS modules ensure only one instance is created per host
- Service dependencies ensure apps wait for shared infrastructure

### Embedded App Routing Architecture

**CRITICAL CONSTRAINT: No app-specific routes at root level.**

All embedded apps MUST be served under `/embed/{appName}/` paths. URL rewriting via service worker handles apps that use absolute paths.

See `docs/embedded-app-routing.md` for full details.

### Future: Systemd-Based Startup Architecture

**Current State (Dev Workaround):**
The dev environment has a startup ordering issue where NixOS services start before the host-agent binary is available. Current workarounds:
- `cli/test.go` builds the Go binary before `nixos-rebuild switch`
- Go configurators create directories in PreStart hooks
- System apps are registered in the database during reconciliation

**Future Production Architecture:**
All startup dependencies should be managed via systemd, not dev scripts:

1. **Host-Agent as Nix Derivation:**
   ```nix
   # Build host-agent as part of NixOS, not separately
   bloud.hostAgent = pkgs.buildGoModule { ... };
   ```

2. **Systemd Service Dependencies:**
   ```nix
   # bloud-host-agent.service starts before app services
   systemd.user.services.bloud-host-agent = {
     wantedBy = [ "bloud-apps.target" ];
     before = [ "bloud-apps.target" ];
   };

   # App services depend on host-agent being ready
   systemd.user.services."podman-myapp" = {
     after = [ "bloud-host-agent.service" ];
     requires = [ "bloud-host-agent.service" ];
   };
   ```

3. **Systemd Tmpfiles for Directories:**
   ```nix
   # Use tmpfiles.d instead of activation scripts for runtime dirs
   systemd.user.tmpfiles.rules = [
     "d /home/user/.local/share/bloud/myapp 0755 user users -"
   ];
   ```

4. **Health-Based Dependencies:**
   ```nix
   # Use sd-notify for proper health signaling
   systemd.user.services.bloud-host-agent = {
     serviceConfig.Type = "notify";
     # Service only reports ready after initialization complete
   };
   ```

**Migration Path:**
1. Package host-agent as Nix derivation
2. Create bloud-host-agent.service with proper dependencies
3. Update app services to require host-agent
4. Move directory creation to systemd tmpfiles
5. Remove dev script workarounds

## Local Development

Use the `./bloud` CLI which manages a Lima VM with hot reload. Works on macOS and Linux.

### Prerequisites

**macOS:**
```bash
brew install lima
```

**Linux (Debian/Ubuntu):**
```bash
curl -fsSL https://lima-vm.io/install.sh | bash
```

**Setup (all platforms):**
```bash
npm run setup    # Installs deps + builds ./bloud CLI
./bloud setup    # Checks prerequisites and downloads VM image
```

### The `./bloud` CLI

```bash
# Dev environment (persistent VM, ports 8080/3000/5173)
./bloud start          # Start dev environment (auto-starts VM if needed)
./bloud stop           # Stop dev services
./bloud status         # Show dev environment status
./bloud logs           # Show logs from dev services
./bloud attach         # Attach to tmux session (Ctrl-B D to detach)
./bloud shell          # Shell into VM
./bloud shell "cmd"    # Run a command in VM
./bloud rebuild        # Rebuild NixOS configuration

# Test environment (ephemeral VM, ports 8081/3001/5174)
./bloud test start     # Create fresh test VM and start services
./bloud test stop      # Stop services and destroy test VM
./bloud test status    # Show test environment status
./bloud test logs      # Show logs from test services
./bloud test attach    # Attach to test tmux session
./bloud test shell     # Shell into test VM
./bloud test rebuild   # Rebuild test VM NixOS config
```

### Typical Development Session

```bash
# Start dev environment (VM + services + port forwarding - all automatic)
./bloud start

# Edit files on your Mac:
#   - Go files (*.go) → auto-rebuild and restart
#   - Svelte files (*.svelte, *.ts) → Vite hot-reloads in browser

# Access the UI
open http://localhost:8080   # Web UI via Traefik (recommended)
open http://localhost:3000   # Go API (direct)

# View server output
./bloud attach   # Live tmux view (Ctrl-B D to detach)
./bloud logs     # Quick snapshot of recent output

# Check status
./bloud status

# When done
./bloud stop     # Stop dev servers (VM stays for fast restart)
```

### Running Tests in Isolation

The test environment runs on different ports so dev and tests can run simultaneously:

```bash
# Start ephemeral test VM (fresh state, isolated)
./bloud test start

# Run your integration tests against port 8081/3001
npm test

# Test VM auto-destroys when stopped
./bloud test stop
```

### After Changing NixOS Config

If you modify `.nix` files (like adding new apps):
```bash
./bloud rebuild   # Apply NixOS changes
```

### Hot Reload Architecture

The dev environment uses:
- **Custom Go watcher** - Polls for `*.go` file changes, syncs to local dir, rebuilds and restarts
- **Vite** - Svelte dev server with HMR (Hot Module Replacement)
- **tmux** - Session management so dev servers survive SSH disconnects
- **9p mount** - Your Mac's project directory is mounted in the VM
- **File sync** - Source files are synced from 9p mount to `/tmp/bloud-src` for compilation

Edit files in your Mac editor → Changes detected via polling → Files synced → Auto-rebuild/reload

### Getting the NixOS Image

The easiest way is to run `./bloud setup` which offers to download a pre-built image.

If you prefer to build manually (requires Linux with Nix):
```bash
cd lima && ./build-image.sh --local
```

See README.md for detailed instructions.

### Configurable Username

The bloud modules support configuring which user runs services:

```nix
# In your NixOS configuration
bloud = {
  enable = true;
  user = "bloud";  # Default: "daniel"
};
```

This is used by the VM configuration (`nixos/vm-dev.nix`) to run services as the `bloud` user.

### App Configuration (Generated)

Apps are enabled via generated `nixos/generated/apps.nix`:

```nix
# Generated by Bloud host-agent
bloud.apps.postgres.enable = true;
bloud.apps.miniflux.enable = true;
```

The host-agent writes this file and triggers `nixos-rebuild switch`.
