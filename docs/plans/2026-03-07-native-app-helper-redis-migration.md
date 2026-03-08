# mkNativeApp Helper + Redis Migration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create a `mkNativeApp` helper for native NixOS service modules, then migrate redis from a Podman container to `services.redis`, updating Authentik to connect via unix socket.

**Architecture:** `nixos/lib/native-app.nix` is a thin wrapper that handles the boilerplate (options, `mkIf` guard, `cfg` context, configurator hooks, media group) while delegating actual service config to each app's `nixosConfig` function. Redis connectivity for Authentik uses unix socket bind-mount — the same pattern already proven for postgres.

**Tech Stack:** Nix/NixOS modules, `services.redis` (nixpkgs), systemd, rootless Podman (Authentik stays containerised)

---

## Task 1: Create `nixos/lib/native-app.nix`

**Files:**
- Create: `nixos/lib/native-app.nix`

Nix modules don't have unit tests — validation is via rebuild in Task 2. This task is just writing the file correctly.

**Step 1: Write the helper**

```nix
# nixos/lib/native-app.nix
#
# mkNativeApp - Helper for creating native NixOS service app modules
#
# Unlike mkPodmanApp (which creates Podman containers), this helper wraps
# native NixOS service modules (services.redis, services.sonarr, etc.).
# It handles the repeated boilerplate while leaving actual service config
# to the caller's nixosConfig function.
#
# Usage:
#   { config, pkgs, lib, ... }:
#   let
#     mkNativeApp = import ../../nixos/lib/native-app.nix { inherit config pkgs lib; };
#   in
#   mkNativeApp {
#     name = "redis";
#     description = "Redis in-memory data store";
#     port = 6379;
#     serviceName = "redis-bloud";
#     nixosConfig = cfg: {
#       services.redis.servers.bloud = { ... };
#     };
#   }
#
# Parameters:
#   name             - App name (sets bloud.apps.<name>.* options)
#   description      - Human-readable description for the enable option
#   port             - Host port (optional; creates bloud.apps.<name>.port option)
#   serviceName      - NixOS systemd service to attach configurator hooks to
#                      (defaults to name; e.g. "redis-bloud" for services.redis.servers.bloud)
#   needsMedia       - Add service user to media group (default: false)
#   options          - Additional bloud.apps.<name>.* options, same shape as mkPodmanApp
#   configuratorHooks - Inject pre/post configurator hooks (default: true)
#   nixosConfig      - Function (cfg -> nixos attrset) — the actual service config

{ config, pkgs, lib }:

{
  name,
  description,
  port ? null,
  serviceName ? name,
  needsMedia ? false,
  options ? {},
  configuratorHooks ? true,
  nixosConfig,
}:

let
  bloudCfg = config.bloud;
  traefikCfg = config.bloud.apps.traefik;
  authentikCfg = config.bloud.apps.authentik;
  appCfg = config.bloud.apps.${name};

  userHome = "/home/${bloudCfg.user}";
  secretsDir = "${userHome}/.local/share/${bloudCfg.dataDir}";

  # Context object passed to nixosConfig — mirrors what mkPodmanApp provides
  cfg = appCfg // {
    externalHost = bloudCfg.externalHost;
    authentikExternalHost = bloudCfg.authentikExternalHost;
    traefikPort = traefikCfg.port;
    bloudUser = bloudCfg.user;
    secretsDir = secretsDir;
    agentPath = bloudCfg.agentPath;
    authentikEnabled = authentikCfg.enable or false;
    appName = name;
  };

  mkOption = optName: optCfg: lib.mkOption {
    type = optCfg.type or lib.types.str;
    default = optCfg.default;
    description = optCfg.description or "Option ${optName}";
  };

  customOptions = lib.mapAttrs mkOption options;

  portOption = lib.optionalAttrs (port != null) {
    port = lib.mkOption {
      type = lib.types.int;
      default = port;
      description = "Port for ${name}";
    };
  };

  resolvedNixosConfig = nixosConfig cfg;

  # Inject configurator pre/post hooks into the native systemd service.
  # Uses + prefix so hooks run as root — needed because native services run
  # as their own system users (not as the bloud user), but the configurator
  # binary and its output files are owned by the bloud user.
  # NOTE: If a future app's NixOS module already sets ExecStartPre/Post,
  # use a list here and handle ordering per-app.
  hookConfig = lib.optionalAttrs (configuratorHooks && serviceName != null) {
    systemd.services.${serviceName}.serviceConfig = {
      ExecStartPre = lib.mkBefore [ "+${bloudCfg.agentPath} configure prestart ${name}" ];
      ExecStartPost = [ "+${bloudCfg.agentPath} configure poststart ${name}" ];
    };
  };

  # Add the service's system user to the media group (for sonarr, radarr, jellyfin, etc.)
  mediaConfig = lib.optionalAttrs needsMedia {
    users.users.${name}.extraGroups = [ "media" ];
  };

in
{
  options.bloud.apps.${name} = {
    enable = lib.mkEnableOption description;
  } // portOption // customOptions;

  config = lib.mkIf appCfg.enable (lib.mkMerge [
    resolvedNixosConfig
    hookConfig
    mediaConfig
  ]);
}
```

**Step 2: Set correct permissions**

```bash
chmod 644 nixos/lib/native-app.nix
```

No commit yet — validate by using it in Task 2.

---

## Task 2: Migrate redis to native NixOS service

**Files:**
- Modify: `apps/redis/module.nix`

**Step 1: Rewrite apps/redis/module.nix**

Replace the entire file with:

```nix
{ config, pkgs, lib, ... }:

let
  mkNativeApp = import ../../nixos/lib/native-app.nix { inherit config pkgs lib; };
in
mkNativeApp {
  name = "redis";
  description = "Redis in-memory data store";
  port = 6379;
  # services.redis.servers.bloud creates systemd service "redis-bloud.service"
  serviceName = "redis-bloud";

  nixosConfig = cfg: {
    services.redis.servers.bloud = {
      enable = true;
      port = cfg.port;
      # Persist data: save a snapshot every 60s if at least 1 key changed
      # Mirrors the previous container: cmd = [ "--save" "60" "1" ]
      save = [ { seconds = 60; changes = 1; } ];
      # Unix socket for Authentik container access (see authentik/module.nix).
      # Same pattern as postgres: rootless podman containers can't reach system
      # services via TCP due to netns isolation, so we mount the socket.
      unixSocket = "/run/redis/bloud.sock";
      unixSocketPerm = 777;
    };
  };
}
```

**Step 2: Rebuild**

```bash
./bloud rebuild
```

Expected: rebuild succeeds, all health checks pass.

If rebuild fails with a Nix evaluation error, check:
- Typos in the `services.redis.servers.bloud` option names
- Whether `save` takes `{ seconds; changes; }` attrs in your nixpkgs version

To verify the exact option shape for your nixpkgs pin:
```bash
./bloud shell "nix eval --impure 'nixpkgs#redis' --apply 'x: x.meta.description'"
# Or check nixpkgs source:
./bloud shell "cat \$(nix eval --raw nixpkgs#path)/nixos/modules/services/databases/redis.nix | grep -A5 'save ='"
```

**Step 3: Verify redis is running**

```bash
./bloud shell "systemctl status redis-bloud.service --no-pager"
```

Expected: `Active: active (running)`

```bash
./bloud shell "ls -la /run/redis/"
```

Expected: socket file `bloud.sock` with permissions `srwxrwxrwx`

**Step 4: Smoke-test redis via the socket**

```bash
./bloud shell "redis-cli -s /run/redis/bloud.sock ping"
```

Expected: `PONG`

```bash
./bloud shell "redis-cli -s /run/redis/bloud.sock set test-key hello && redis-cli -s /run/redis/bloud.sock get test-key"
```

Expected: `OK` then `hello`

**Step 5: Confirm old redis container is gone**

```bash
./bloud shell "podman ps -a --filter name=apps-redis"
```

Expected: empty (no apps-redis container)

**Step 6: Commit**

```bash
git add nixos/lib/native-app.nix apps/redis/module.nix
git commit -m "Migrate redis from Podman to native NixOS services.redis"
```

---

## Task 3: Update Authentik to use redis unix socket

**Files:**
- Modify: `apps/authentik/module.nix`

Authentik currently connects to redis via the container network
(`AUTHENTIK_REDIS__HOST = "apps-redis"`). The `apps-redis` container no longer exists.
The fix: bind-mount the redis unix socket into the Authentik containers, same as postgres.

**Step 1: Determine Authentik's redis socket env var**

Authentik's Python redis client supports unix sockets. First check what env var to use:

```bash
./bloud shell "podman exec apps-authentik-server env | grep -i redis"
```

This shows the current redis-related env vars set in the container. Then check Authentik docs
or source for unix socket support. The most likely approaches (try in order):

Option A — Path as host (Python redis client treats host starting with `/` as socket path):
```
AUTHENTIK_REDIS__HOST = "/run/redis/bloud.sock"
```

Option B — URL format (if Authentik supports `AUTHENTIK_REDIS__URL`):
```
AUTHENTIK_REDIS__URL = "unix:///run/redis/bloud.sock"
```

Check the running Authentik container logs for redis connection errors to see which format
it expects:
```bash
./bloud shell "journalctl --user -u podman-apps-authentik-server.service -n 50 --no-pager | grep -i redis"
```

**Step 2: Update apps/authentik/module.nix**

In the `podman-apps-authentik-server` service config, make these changes:

1. **Add redis socket volume mount** (alongside the existing postgres socket mount):
   ```nix
   # In volumes list for both server and worker:
   "/run/redis:/run/redis:ro"
   ```

2. **Update AUTHENTIK_REDIS__HOST** (use whichever env var worked in Step 1):
   ```nix
   # Replace:
   AUTHENTIK_REDIS__HOST = "apps-redis";
   # With (Option A):
   AUTHENTIK_REDIS__HOST = "/run/redis/bloud.sock";
   ```

3. **Drop apps-redis from dependsOn** in server and worker:
   ```nix
   # Remove "apps-redis" from:
   dependsOn = [ "apps-network" "apps-redis" ];
   # Becomes:
   dependsOn = [ "apps-network" ];
   ```

4. **Drop redis waitFor check** from the server:
   ```nix
   # Remove this entry from waitFor:
   { container = "apps-redis"; command = "redis-cli ping"; }
   ```

5. **Remove redis:alpine from pullImages** — find the line that pulls redis and delete it.
   In `apps/authentik/module.nix`, the `bloud.pullImages` list currently includes redis via
   the redis module. Verify it's no longer there after the redis module change:
   ```bash
   grep -r "redis:alpine" apps/
   ```
   Expected: no matches.

**Step 3: Rebuild**

```bash
./bloud rebuild
```

Expected: rebuild succeeds, all health checks pass.

If Authentik fails to connect to redis (check logs below), revisit the env var from Step 1.

**Step 4: Verify Authentik is healthy**

```bash
./bloud shell "journalctl --user -u podman-apps-authentik-server.service -n 30 --no-pager"
```

Expected: no redis connection errors. Look for lines like:
- `Starting authentik server` without redis errors
- No `ConnectionError` or `redis.exceptions` lines

```bash
./bloud shell "journalctl --user -u podman-apps-authentik-worker.service -n 30 --no-pager"
```

Same check for the worker.

**Step 5: Full health check**

```bash
./bloud rebuild
```

Expected: all 8 health checks pass (the rebuild runs health checks automatically).

Optionally, test the Authentik login flow manually:
```
open http://localhost:8080/if/flow/default-authentication-flow/
```

**Step 6: Commit**

```bash
git add apps/authentik/module.nix
git commit -m "Connect Authentik to native redis via unix socket"
```

---

## Troubleshooting

### redis-bloud.service fails to start

```bash
./bloud shell "journalctl -u redis-bloud.service -n 50 --no-pager"
```

Common causes:
- Port 6379 already in use (old container still running): `./bloud shell "podman stop apps-redis && podman rm apps-redis"`
- Data directory permissions: check `/var/lib/redis-bloud/` ownership

### Authentik can't connect to redis

If `AUTHENTIK_REDIS__HOST = "/run/redis/bloud.sock"` doesn't work, check the container logs
for the exact error message format, then try:

```bash
./bloud shell "podman exec apps-authentik-server python -c \"import redis; r = redis.Redis(unix_socket_path='/run/redis/bloud.sock'); print(r.ping())\""
```

This tests the Python redis client directly to confirm the socket is accessible inside the
container. If this returns `True`, the issue is env var format, not socket access.

### mkNativeApp hookConfig conflict

If a future app's NixOS module already sets `ExecStartPre`, the `lib.mkBefore` in hookConfig
may conflict. Solution: set it as a list instead:

```nix
hookConfig = lib.optionalAttrs (configuratorHooks && serviceName != null) {
  systemd.services.${serviceName}.serviceConfig.ExecStartPre =
    lib.mkBefore [ "+${bloudCfg.agentPath} configure prestart ${name}" ];
};
```

And handle `ExecStartPost` similarly. Note this per-app when it arises.
