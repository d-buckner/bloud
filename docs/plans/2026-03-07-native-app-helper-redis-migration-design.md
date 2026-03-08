# Design: mkNativeApp Helper + Redis Migration

## Context

Postgres was migrated from Podman to a native NixOS service (`services.postgresql`) as Step 1
of the native NixOS services migration plan. This document covers the next two pieces:

1. A new `mkNativeApp` helper (`nixos/lib/native-app.nix`) for native NixOS service modules
2. Redis migration from Podman to `services.redis` (Step 2 in the migration plan)

The `mkPodmanApp` rename (from `mkBloudApp`) was completed separately in commit de0c182.

---

## Decision: mkNativeApp — Option B (Medium helper)

Three options were considered:

- **A — Thin (options boilerplate only):** `mkNativeApp` handles only enable/port options and
  the `mkIf` guard. Each module manually wires hooks and media group. Most transparent but
  10-20 lines of copy-paste per app across all 10 migrations.

- **B — Medium (options + hooks + media) ✓ CHOSEN:** `mkNativeApp` handles options, the `mkIf`
  guard, the `cfg` context, configurator hook injection, and media group wiring. Each module
  provides `nixosConfig = cfg: { ... }`. The things abstracted are genuinely error-prone to get
  right per-app; the actual service config stays transparent.

- **C — Full parity with mkPodmanApp:** Overkill. Native NixOS modules handle `ensureDatabases`,
  env files, etc. better than a wrapper.

---

## Design

### 1. `nixos/lib/native-app.nix` — `mkNativeApp`

**Interface:**

```nix
mkNativeApp {
  name = "redis";
  description = "Redis in-memory data store";
  port = 6379;                  # optional — creates bloud.apps.redis.port option
  serviceName = "redis-bloud";  # systemd service to hook into; defaults to name
  needsMedia = false;           # adds service user to media group; default false
  options = {};                 # additional custom options (same shape as mkPodmanApp)
  configuratorHooks = true;     # inject pre/post hooks; default true
  nixosConfig = cfg: { ... };   # the actual NixOS config
}
```

**What the helper generates:**

- `bloud.apps.<name>.enable` and optionally `.port` options
- `lib.mkIf appCfg.enable` guard around everything
- Configurator hooks injected into `systemd.services.<serviceName>.serviceConfig` using `+`
  prefix (runs as root — appropriate since native services run as their own system users, not
  as the bloud user)
- Media group membership via `users.users.<name>.extraGroups = [ "media" ]` when
  `needsMedia = true`

**`cfg` context passed to `nixosConfig`:**

Mirrors what `mkPodmanApp` provides: `port`, `externalHost`, `authentikExternalHost`,
`traefikPort`, `bloudUser`, `secretsDir`, `agentPath`, `authentikEnabled`, `appName`.

### 2. Redis migration

**`apps/redis/module.nix`** switches from `mkPodmanApp` to `mkNativeApp`:

```nix
mkNativeApp {
  name = "redis";
  description = "Redis in-memory data store";
  port = 6379;
  serviceName = "redis-bloud";  # services.redis.servers.bloud → redis-bloud.service
  nixosConfig = cfg: {
    services.redis.servers.bloud = {
      enable = true;
      port = cfg.port;
      save = [ { seconds = 60; changes = 1; } ];
      unixSocket = "/run/redis/bloud.sock";
      unixSocketPerm = 777;  # world-accessible, mirrors postgres socket approach
    };
  };
}
```

- NixOS manages state at `/var/lib/redis-bloud/` automatically
- `redis:alpine` is removed from `bloud.pullImages`

### 3. Authentik connectivity update

Authentik currently connects to redis via the container network
(`AUTHENTIK_REDIS__HOST = "apps-redis"`). After migration, the same rootless podman netns
boundary applies as with postgres: containers cannot reach system services via TCP.

**Solution:** Mount the unix socket into Authentik containers (same pattern as postgres).

Changes to `apps/authentik/module.nix`:
- Add `/run/redis:/run/redis:ro` volume to server and worker containers
- Drop `"apps-redis"` from `dependsOn` and `waitFor`
- Update `AUTHENTIK_REDIS__HOST` to connect via socket

**Open item:** The exact env var for Authentik redis unix socket needs verification during
implementation. The Python redis client supports `unix:///run/redis/bloud.sock` as a URL;
whether Authentik exposes this via `AUTHENTIK_REDIS__HOST` or a separate env var will be
confirmed by checking the Authentik container logs/config during the rebuild step.

---

## Files Changed

| File | Change |
|------|--------|
| `nixos/lib/native-app.nix` | New helper |
| `apps/redis/module.nix` | Migrate from `mkPodmanApp` to `mkNativeApp` |
| `apps/authentik/module.nix` | Mount redis socket, update env var, drop container dep |
