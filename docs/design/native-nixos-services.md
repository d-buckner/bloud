# Native NixOS Services Migration

## Context & Motivation

All Bloud apps currently run as rootless Podman containers under the `bloud` user. This
approach carries significant accidental complexity:

- UID remapping between host and container requires `--userns=keep-id` workarounds
- Shared directories need careful ownership management across the UID namespace
- Container networking adds a layer of indirection for apps that don't benefit from isolation
- Container images must be pulled, versioned, and kept separate from NixOS system updates

Many of the apps Bloud ships are well-maintained in nixpkgs with production-grade NixOS
modules that handle user creation, directory setup, systemd integration, and database
initialization declaratively. For these apps, native NixOS services are strictly simpler.

The goal is to migrate well-maintained apps to native NixOS services while keeping apps
with no good nixpkgs module (or complex integration requirements) as Podman containers.

---

## Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Isolation model | Dedicated system user per app | Native NixOS modules create users by default; eliminates UID remapping |
| Shared directory access | `media` group (Option A) | Standard Unix approach, clear semantics |
| Shared directory location | `/var/lib/bloud/` | Conventional for system services; easy to put on a dedicated mount |
| App-specific state | `/var/lib/<appname>` | NixOS module defaults; no need to override |
| Authentik | Stay as Podman | `services.authentik` is too new; integration is complex |
| Jellyseerr, actual-budget, affine | Stay as Podman | No suitable nixpkgs module |
| Migration cadence | One app at a time | Validate each before proceeding |

---

## App Inventory

| App | Current | Target | NixOS Module | Notes |
|-----|---------|--------|-------------|-------|
| postgres | Podman | **NixOS** | `services.postgresql` | Foundational; migrate first |
| redis | Podman | **NixOS** | `services.redis` | Simple, standalone |
| traefik | Podman | **NixOS** | `services.traefik` | Infrastructure; config generation changes |
| adguard-home | Podman | **NixOS** | `services.adguardhome` | Standalone, no shared dirs |
| miniflux | Podman | **NixOS** | `services.miniflux` | Uses postgres; good DB pattern test |
| prowlarr | Podman | **NixOS** | `services.prowlarr` | Arr stack; no shared media dirs |
| qbittorrent | Podman | **NixOS** | `services.qbittorrent` | Backend only; Flood UI stays as Podman |
| flood | Podman | **Podman** | — | No nixpkgs module; connects to qbittorrent via localhost |
| sonarr | Podman | **NixOS** | `services.sonarr` | Needs `media` group |
| radarr | Podman | **NixOS** | `services.radarr` | Needs `media` group |
| jellyfin | Podman | **NixOS** | `services.jellyfin` | Needs `media` group; LDAP plugin handling changes |
| authentik | Podman | **Podman** | — | Too complex; revisit separately |
| jellyseerr | Podman | **Podman** | — | No nixpkgs module |
| actual-budget | Podman | **Podman** | — | No nixpkgs module |
| affine | Podman | **Podman** | — | No nixpkgs module |

### Note: qbittorrent + Flood

The current setup runs qBittorrent as a backend with Flood as the web UI frontend. Flood is
not in nixpkgs. The migration uses `services.qbittorrent` for the backend while keeping the
Flood container.

The key change is networking: currently Flood connects to qBittorrent via the container
network (`http://apps-qbittorrent:8080`). With a native qbittorrent service, Flood connects
via localhost (`http://localhost:8080`). The subnet auth whitelist in qBittorrent's config is
replaced with `WebUI\LocalHostAuth=false` since both processes now run on the same host.

---

## Directory Layout

### Before

```
/home/bloud/.local/share/bloud/
  postgres/         ← container volume (postgres data)
  redis/            ← container volume (redis data)
  jellyfin/config/  ← container volume
  jellyfin/cache/   ← container volume
  sonarr/config/    ← container volume
  radarr/config/    ← container volume
  qbittorrent/      ← container volume
  flood/            ← container volume
  downloads/        ← shared (qbittorrent writes, sonarr/radarr read)
  media/
    shows/          ← shared (sonarr writes, jellyfin reads)
    movies/         ← shared (radarr writes, jellyfin reads)
  traefik/          ← traefik config files
  miniflux.env      ← secrets
  postgres.env      ← secrets
```

### After

```
/var/lib/bloud/
  media/
    shows/          ← owner: bloud:media, mode: 775
    movies/         ← owner: bloud:media, mode: 775
  downloads/        ← owner: bloud:media, mode: 775

/var/lib/postgresql/  ← managed by services.postgresql
/var/lib/redis/       ← managed by services.redis
/var/lib/jellyfin/    ← managed by services.jellyfin
/var/lib/sonarr/      ← managed by services.sonarr
/var/lib/radarr/      ← managed by services.radarr
/var/lib/prowlarr/    ← managed by services.prowlarr
/var/lib/qbittorrent/ ← managed by services.qbittorrent
/var/lib/miniflux/    ← managed by services.miniflux (if applicable)
/var/lib/adguardhome/ ← managed by services.adguardhome
/var/lib/traefik/     ← traefik dynamic config (written by host-agent)

# Secrets remain in bloud's data dir (written by host-agent at runtime)
/home/bloud/.local/share/bloud/
  miniflux.env
  postgres.env
  ... (other runtime secrets)
```

NixOS modules create their own state directories and handle ownership automatically. Bloud
only needs to manage `/var/lib/bloud/` and the Traefik dynamic config directory.

---

## Media Group Pattern

A `media` system group provides shared read/write access to `/var/lib/bloud/media` and
`/var/lib/bloud/downloads` across all relevant service users.

```nix
users.groups.media = {};

# Added to each app that needs shared dir access
users.users.sonarr.extraGroups  = [ "media" ];
users.users.radarr.extraGroups  = [ "media" ];
users.users.jellyfin.extraGroups = [ "media" ];
users.users.qbittorrent.extraGroups = [ "media" ];

# Operator access for browsing files
users.users.bloud.extraGroups = [ "media" ];

# Shared directories: owner bloud, group media, group-writable
systemd.tmpfiles.rules = [
  "d /var/lib/bloud              0755 bloud media -"
  "d /var/lib/bloud/media        0775 bloud media -"
  "d /var/lib/bloud/media/shows  0775 bloud media -"
  "d /var/lib/bloud/media/movies 0775 bloud media -"
  "d /var/lib/bloud/downloads    0775 bloud media -"
];
```

The `media` group membership and tmpfiles rules are set once at the system level, not per-app.
Each app's `module.nix` declares `needsMedia = true` and the helper wires in the group membership.

---

## New Helper: `nixos-app.nix`

A new `nixos/lib/nixos-app.nix` helper is introduced alongside the existing `bloud-app.nix`.
It is deliberately thinner than `bloud-app.nix` because NixOS service modules do most of the
heavy lifting (user creation, directory setup, systemd unit).

**What the helper provides:**

- Standard `options.bloud.apps.<name>.enable` and `.port` option declarations
- `lib.mkIf appCfg.enable` guard
- The `cfg` context object (externalHost, traefikPort, agentPath, etc.)
- Media group membership wiring (when `needsMedia = true`)
- Configurator hook injection into the service's systemd unit

**What it does NOT do:**

- Configure the NixOS service itself — each `module.nix` does this directly
- Manage app-specific state directories — NixOS modules handle this
- Create container networking, volumes, or pull images

**Rough interface:**

```nix
mkNixosApp {
  name = "sonarr";
  description = "TV series collection manager and downloader";
  port = 8989;
  needsMedia = true;   # adds to media group, ensures /var/lib/bloud dirs exist

  # Direct NixOS config — full access to all NixOS options
  nixosConfig = cfg: {
    services.sonarr = {
      enable = true;
      # sonarr creates its own user and /var/lib/sonarr by default
    };
  };
}
```

The helper merges its cross-cutting config with the `nixosConfig` returned by each module.

---

## Database Initialization Pattern

### Before (shell script in activation)

```nix
"${serviceName}-db-init" = {
  serviceConfig.ExecStart = pkgs.writeShellScript "..." ''
    podman exec apps-postgres psql -U apps -c "CREATE DATABASE miniflux;"
  '';
};
```

### After (declarative via services.postgresql)

```nix
services.postgresql = {
  ensureDatabases = [ "miniflux" ];
  ensureUsers = [{
    name = "miniflux";
    ensureDBOwnership = true;
  }];
};
```

`services.postgresql` generates a one-shot systemd service (`postgresql-ensure-users.service`)
that runs after postgres is ready. Apps that use postgres declare their database in their own
`module.nix` and get the correct systemd ordering for free via `after = [ "postgresql.service" ]`.

Passwords for database users are handled via `services.postgresql.authentication` (pg_hba rules)
or environment files passed to the app service, depending on the app's needs.

---

## Configurator Hooks

The host-agent `configure prestart <app>` and `configure poststart <app>` hooks still run for
native services. Instead of being wired via `mkPodmanService`, they attach to the system
service unit directly.

```nix
systemd.services.sonarr.serviceConfig = {
  ExecStartPre = "${config.bloud.agentPath} configure prestart sonarr";
  ExecStartPost = "${config.bloud.agentPath} configure poststart sonarr";
};
```

NixOS allows multiple `ExecStartPre` entries (prefix with `+` to run as root if needed, or
leave unprefixed to run as the service user). The helper injects these automatically when
`configuratorHooks = true` (default).

---

## Traefik Migration Notes

The current traefik `module.nix` uses activation scripts to write static config files and
mounts them read-only into the container. With `services.traefik`:

- Static config is set via `services.traefik.staticConfigOptions` (Nix attrs → YAML)
- Dynamic config directory (`/var/lib/traefik/dynamic/`) is watched by Traefik at runtime
- The host-agent continues to write `apps-routes.yml` to the dynamic config dir, same as today
- The `traefik` system user needs write access to its own dynamic config dir

The main change is that static config moves from activation-script-written YAML files to
declarative NixOS options, which is strictly better.

---

## Migration Order

Infrastructure first, then validate the DB pattern, then media apps in dependency order.

| Step | App | Rationale |
|------|-----|-----------|
| 1 | **postgres** | Most foundational; establishes the DB init pattern |
| 2 | **redis** | Simple, standalone; validates basic nixos-app.nix helper |
| 3 | **traefik** | Infrastructure; unblocks config generation cleanup |
| 4 | **adguard-home** | Standalone, no shared dirs; straightforward migration |
| 5 | **miniflux** | First DB-dependent app; validates `ensureDatabases` pattern end-to-end |
| 6 | **prowlarr** | Arr stack foundation; standalone (no shared media) |
| 7 | **qbittorrent** | First app with shared dirs; validates media group + downloads path |
| 8 | **sonarr** | Depends on qbittorrent (downloads) and prowlarr |
| 9 | **radarr** | Parallel to sonarr; same pattern |
| 10 | **jellyfin** | Reads from both media dirs; last in the media stack |

Each step: update `module.nix` → rebuild → smoke test → commit before proceeding to the next.

---

## Out of Scope

- **Authentik** — remains as Podman; revisit separately when `services.authentik` matures
- **jellyseerr, actual-budget, affine** — remain as Podman; no suitable nixpkgs module
- **`bloud-app.nix`** — not changed; continues to serve the remaining Podman-based apps
- **host-agent configurator logic** — Go code is unchanged; only how hooks are wired changes
- **Traefik routing config generation** — host-agent continues writing `apps-routes.yml`
- **Secrets management** — runtime env files written by host-agent are unchanged
- **Data migration tooling** — out of scope; this is a greenfield ISO approach

---

## Open Questions

1. **Jellyfin LDAP plugin** — currently installed via a oneshot service that unzips into the
   container volume. With native Jellyfin, plugins live in `/var/lib/jellyfin/plugins/`. The
   same approach can work but ownership needs care (jellyfin user must own the dir). Evaluate
   during step 10.

2. **miniflux secrets** — `services.miniflux` has a `secretKeyFile` option. Need to confirm
   whether OAuth client secrets can be passed via the existing env file mechanism or need to
   use this option instead.

3. **qbittorrent ↔ Flood auth** — the current setup uses a subnet whitelist so Flood can
   connect to qBittorrent's WebUI across the container network. With native qbittorrent,
   `WebUI\LocalHostAuth=false` is sufficient since Flood connects via localhost. Verify the
   qbittorrent init config script is updated to set this instead of the subnet rules.
