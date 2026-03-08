# Postgres Native NixOS Migration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the rootless Podman `apps-postgres` container with `services.postgresql` (native NixOS service).

**Architecture:** PostgreSQL 16 runs as a system-level NixOS service under the `postgres` system user. Trust auth is used for localhost connections (unix socket + TCP) and for the podman `apps-net` container subnet (for Authentik containers). The apps-net bridge gets a fixed subnet (`10.89.0.0/24`) so Authentik can reliably reach native postgres at gateway `10.89.0.1`. App databases are declared via `services.postgresql.ensureDatabases` (idempotent, runs on every boot). The `apps` postgres role is granted SUPERUSER to avoid per-database schema permission issues (appropriate for a homelab).

**Tech Stack:** Nix/NixOS modules, `services.postgresql`, `psql`/`pg_isready` from the postgresql package, rootless podman.

**Data note:** This migration starts with a fresh PostgreSQL data directory. The old data lived in `/home/bloud/.local/share/bloud/apps-postgres` (container volume). The new data is at `/var/lib/postgresql/`. Run `bloud-reset` before applying to wipe old container state.

---

## Context — what changes and why

| File | What changes | Why |
|------|-------------|-----|
| `apps/postgres/module.nix` | Full rewrite: `mkBloudApp` → `services.postgresql` | The whole point |
| `nixos/bloud.nix` | (1) Network gets fixed subnet; (2) `bloud-db-init` uses `psql` instead of `podman exec`; (3) integration test uses `psql` | No more `apps-postgres` container |
| `nixos/lib/bloud-app.nix` | `dbInitService` uses `psql` instead of `podman exec apps-postgres` | Podman apps (miniflux etc.) still need DB init, but there's no more container |
| `apps/authentik/module.nix` | (1) postgres host → `10.89.0.1`; (2) remove postgres container dependency; (3) remove `authentik-db-init` service (replaced by `ensureDatabases`) | Authentik stays as Podman but must connect to host postgres |

---

## Task 1: Rewrite `apps/postgres/module.nix`

**Files:**
- Modify: `apps/postgres/module.nix`

The current module wraps a Podman container with `mkBloudApp`. The new module uses `services.postgresql` directly and adds a system-level setup service to grant SUPERUSER to the `apps` role (needed because PostgreSQL 15+ revokes `CREATE` on the `public` schema from `PUBLIC`, and we use a single `apps` user across all databases).

**Step 1: Replace the file**

```nix
{ config, pkgs, lib, ... }:

let
  appCfg = config.bloud.apps.postgres;
in
{
  options.bloud.apps.postgres = {
    enable = lib.mkEnableOption "PostgreSQL database for apps";

    user = lib.mkOption {
      type = lib.types.str;
      default = "apps";
      description = "PostgreSQL role used by all Bloud apps";
    };

    database = lib.mkOption {
      type = lib.types.str;
      default = "apps";
      description = "Default database name (also used as the role's owned database)";
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 5432;
      description = "PostgreSQL port";
    };
  };

  config = lib.mkIf appCfg.enable {
    services.postgresql = {
      enable = true;
      package = pkgs.postgresql_16;

      # Trust auth: homelab machine — if you can log in to the box you can access the DB.
      # Covers:
      #   local   — unix socket connections (system services running as postgres)
      #   127.0.0.1/32 — TCP localhost (host-agent, miniflux, etc.)
      #   ::1/128       — TCP IPv6 localhost
      #   10.89.0.0/24  — apps-net podman bridge subnet (Authentik containers)
      authentication = lib.mkForce ''
        local all all              trust
        host  all all 127.0.0.1/32 trust
        host  all all ::1/128      trust
        host  all all 10.89.0.0/24 trust
      '';

      # Declarative database creation — idempotent, runs on every boot via ExecStartPost.
      # Each app that needs a DB adds to this list in its own module.nix.
      ensureDatabases = [ appCfg.database "bloud" ];

      # Create the apps role. ensureDBOwnership=true makes it own the "apps" database.
      ensureUsers = [{
        name = appCfg.user;
        ensureDBOwnership = true;
      }];
    };

    # Grant SUPERUSER to the apps role so it can access all databases and schemas
    # without per-database GRANT statements (PostgreSQL 15+ removed public schema grants).
    # This runs on every boot after postgresql.service and is idempotent.
    systemd.services.bloud-postgresql-setup = {
      description = "Grant SUPERUSER to Bloud apps PostgreSQL role";
      after = [ "postgresql.service" ];
      requires = [ "postgresql.service" ];
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        User = "postgres";
        ExecStart = pkgs.writeShellScript "bloud-postgres-setup" ''
          ${config.services.postgresql.package}/bin/psql \
            -c "ALTER ROLE ${appCfg.user} SUPERUSER LOGIN;"
        '';
      };
    };
  };
}
```

**Step 2: Verify the Nix eval is valid**

Run from the project root (on the dev NixOS machine):
```bash
nix eval .#nixosConfigurations.dev-server.config.services.postgresql.enable --impure
```
Expected: `true`

```bash
nix eval .#nixosConfigurations.dev-server.config.services.postgresql.ensureDatabases --impure
```
Expected: `[ "apps" "bloud" ]`

**Step 3: Commit**

```bash
git add apps/postgres/module.nix
git commit -m "feat: migrate postgres from podman container to native services.postgresql"
```

---

## Task 2: Fix apps-net subnet and update `bloud-db-init` in `bloud.nix`

**Files:**
- Modify: `nixos/bloud.nix`

Three sub-changes in one file:

**2a — Fixed subnet for apps-net**

The `podman-apps-network` service currently creates the network with an auto-assigned subnet. Authentik containers need to reach native postgres at the host's bridge gateway IP. We pin the subnet to `10.89.0.0/24` (gateway `10.89.0.1`) so the pg_hba rule is stable.

Find the `ExecStart` in `podman-apps-network`:
```nix
# OLD
ExecStart = "${pkgs.bash}/bin/bash -c '${pkgs.podman}/bin/podman network exists apps-net || ${pkgs.podman}/bin/podman network create apps-net'";
```
Replace with:
```nix
# NEW — fixed subnet so pg_hba rules are stable
ExecStart = "${pkgs.bash}/bin/bash -c '${pkgs.podman}/bin/podman network exists apps-net || ${pkgs.podman}/bin/podman network create --subnet 10.89.0.0/24 --gateway 10.89.0.1 apps-net'";
```

**2b — Simplify `bloud-db-init`**

The `bloud-db-init` user service used to create the `bloud` database via `podman exec apps-postgres psql`. Now the database is created declaratively by `services.postgresql.ensureDatabases`. The service becomes a readiness check.

Find the `bloud-db-init` service in `bloud.nix` and replace the entire service block:

```nix
# Before: after = [ "podman-apps-postgres.service" ]; requires = [ ... ]
# After: no container dependency; postgres is a system service that starts before us

systemd.user.services.bloud-db-init = {
  description = "Wait for PostgreSQL to be ready (bloud database managed declaratively)";
  wantedBy = [ "bloud-apps.target" ];
  before = [ "bloud-apps.target" ];
  serviceConfig = {
    Type = "oneshot";
    RemainAfterExit = true;
    ExecStart = pkgs.writeShellScript "bloud-db-init" ''
      set -e
      echo "Waiting for PostgreSQL to be ready..."
      for i in $(seq 1 30); do
        if ${config.services.postgresql.package}/bin/pg_isready \
            -h 127.0.0.1 \
            -U ${config.bloud.apps.postgres.user} > /dev/null 2>&1; then
          echo "PostgreSQL is ready"
          exit 0
        fi
        if [ "$i" -eq 30 ]; then
          echo "Timeout waiting for PostgreSQL"
          exit 1
        fi
        echo "Waiting... ($i/30)"
        sleep 1
      done
    '';
  };
};
```

**2c — Update `bloud-test-integration` script**

Find the PostgreSQL test inside `bloud-test-integration`:
```bash
# OLD
if ${pkgs.podman}/bin/podman exec apps-postgres psql -U apps -d apps -c "SELECT 1" &>/dev/null; then
```
Replace with:
```bash
# NEW
if ${config.services.postgresql.package}/bin/psql -U apps -h 127.0.0.1 -d apps -c "SELECT 1" &>/dev/null; then
```

Also update the `bloud-test` script hint that says `podman exec apps-postgres psql -U apps`:
```bash
# OLD
echo "  podman exec apps-postgres psql -U apps  - Access PostgreSQL"
# NEW
echo "  psql -U apps -h 127.0.0.1              - Access PostgreSQL"
```

**Step: Verify the Nix eval**

```bash
nix eval .#nixosConfigurations.dev-server.config.systemd.user.services.bloud-db-init --impure 2>&1 | head -20
```
Should not reference `podman-apps-postgres.service`.

**Step: Commit**

```bash
git add nixos/bloud.nix
git commit -m "fix: update bloud.nix for native postgres (fixed subnet, simplified bloud-db-init, update integration test)"
```

---

## Task 3: Update `bloud-app.nix` — fix `dbInitService`

**Files:**
- Modify: `nixos/lib/bloud-app.nix`

The `dbInitService` is used by remaining Podman-based apps that have a `database` parameter (e.g., miniflux). It currently uses `podman exec apps-postgres psql`. Replace with direct `psql` calls.

Find `dbInitService` in `nixos/lib/bloud-app.nix` (around line 137) and replace the entire `dbInitService` binding:

```nix
# Database init service (if database is specified)
# Uses psql directly — postgres is now a native NixOS service, not a container.
dbInitService = lib.optionalAttrs (database != null) {
  "${serviceName}-db-init" = {
    description = "Initialize ${name} database";
    # postgres is a system service; by the time user services start it's running.
    # Poll pg_isready as a safety check.
    after = [ "bloud-init-secrets.service" ];
    before = [ "podman-${serviceName}.service" ];
    wantedBy = [ "bloud-apps.target" ];
    partOf = [ "bloud-apps.target" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = pkgs.writeShellScript "${name}-db-init" ''
        set -e
        echo "Waiting for postgres to be ready..."
        for i in {1..30}; do
          if ${config.services.postgresql.package}/bin/pg_isready \
              -h 127.0.0.1 \
              -U ${cfg.postgresUser} > /dev/null 2>&1; then
            echo "Postgres is ready"
            break
          fi
          if [ $i -eq 30 ]; then
            echo "ERROR: Postgres not ready after 30 seconds, giving up"
            exit 1
          fi
          echo "Waiting for postgres... ($i/30)"
          sleep 2
        done
        ${config.services.postgresql.package}/bin/psql \
          -U ${cfg.postgresUser} \
          -h 127.0.0.1 \
          -c "CREATE DATABASE \"${database}\"" 2>/dev/null \
          || echo "Database ${database} already exists"
        ${config.services.postgresql.package}/bin/psql \
          -U ${cfg.postgresUser} \
          -h 127.0.0.1 \
          -c "GRANT ALL PRIVILEGES ON DATABASE \"${database}\" TO ${cfg.postgresUser}" \
          || true
        echo "${name} database initialized"
      '';
    };
  };
};
```

Also update `dbExtraAfter` and `dbExtraRequires` — they generate `after`/`requires` entries for the main app service. They currently reference `${serviceName}-db-init.service`, which is still correct (the db-init service still exists, just uses psql now). No changes needed to those bindings.

**Step: Verify**

```bash
nix eval .#nixosConfigurations.dev-server.config.systemd.user.services.miniflux-db-init --impure 2>&1 | grep -v "podman exec"
```
Should not contain `podman exec`.

**Step: Commit**

```bash
git add nixos/lib/bloud-app.nix
git commit -m "fix: update bloud-app.nix dbInitService to use psql directly (postgres is now native)"
```

---

## Task 4: Update `apps/authentik/module.nix`

**Files:**
- Modify: `apps/authentik/module.nix`

Four sub-changes:

**4a — Add `authentik` database to `ensureDatabases`**

In the `config = lib.mkIf appCfg.enable {` block, add:
```nix
# Authentik database — created declaratively by services.postgresql (merged with postgres module)
services.postgresql.ensureDatabases = [ "authentik" ];
```

**4b — Remove `authentik-db-init` service**

Delete the entire `authentik-db-init` service from `systemd.user.services`. It's replaced by `services.postgresql.ensureDatabases = [ "authentik" ]` above.

**4c — Update Authentik server container's postgres connection**

In `podman-apps-authentik-server`, change:
```nix
# OLD
AUTHENTIK_POSTGRESQL__HOST = "apps-postgres";
```
To:
```nix
# NEW — native postgres on host, reachable from containers at the apps-net gateway
AUTHENTIK_POSTGRESQL__HOST = "10.89.0.1";
```

Remove `apps-postgres` from `dependsOn`:
```nix
# OLD
dependsOn = [ "apps-network" "apps-postgres" "apps-redis" ];
# NEW
dependsOn = [ "apps-network" "apps-redis" ];
```

Remove the postgres `waitFor` entry:
```nix
# OLD
waitFor = [
  { container = "apps-postgres"; command = "pg_isready -U ${postgresUser}"; }
  { container = "apps-redis"; command = "redis-cli ping"; }
];
# NEW
waitFor = [
  { container = "apps-redis"; command = "redis-cli ping"; }
];
```

Update `extraAfter` and `extraRequires` — remove `authentik-db-init.service` (keep `bloud-db-init.service`):
```nix
# OLD
extraAfter = [ "bloud-db-init.service" "authentik-db-init.service" ];
extraRequires = [ "bloud-db-init.service" "authentik-db-init.service" ];
# NEW
extraAfter = [ "bloud-db-init.service" ];
extraRequires = [ "bloud-db-init.service" ];
```

**4d — Same changes for Authentik worker**

Apply the same postgres host, `dependsOn`, `waitFor`, and `extraAfter`/`extraRequires` changes to `podman-apps-authentik-worker`. It also has `AUTHENTIK_POSTGRESQL__HOST = "apps-postgres"` and similar dependency arrays.

**Step: Verify**

```bash
nix eval .#nixosConfigurations.dev-server.config.services.postgresql.ensureDatabases --impure
```
Expected: `[ "apps" "bloud" "authentik" ]` (merged from postgres + authentik modules)

```bash
nix eval .#nixosConfigurations.dev-server.config.systemd.user.services --impure 2>&1 | grep "authentik-db-init" || echo "PASS: authentik-db-init removed"
```
Expected: `PASS: authentik-db-init removed`

**Step: Commit**

```bash
git add apps/authentik/module.nix
git commit -m "fix: update authentik to connect to native postgres at host gateway (10.89.0.1)"
```

---

## Task 5: Rebuild and verify

**Step 1: Reset state (wipe old container postgres data)**

On the dev NixOS machine:
```bash
# Wipe old container state so we start fresh
bloud-reset --include-secrets
# When prompted: y
```

Also destroy the old apps-net network (it has wrong subnet):
```bash
podman network rm apps-net 2>/dev/null || true
```

Also wipe old postgresql data dir if it exists from a previous attempt:
```bash
sudo rm -rf /var/lib/postgresql/
```

**Step 2: Apply the NixOS configuration**

```bash
sudo nixos-rebuild switch --flake .#dev-server --impure
```
Expected: completes without errors.

**Step 3: Verify PostgreSQL system service**

```bash
systemctl status postgresql.service
```
Expected: `active (running)`

```bash
systemctl status bloud-postgresql-setup.service
```
Expected: `active (exited)` — means the SUPERUSER grant ran successfully.

**Step 4: Verify databases exist**

```bash
psql -U apps -h 127.0.0.1 -l
```
Expected: shows `apps`, `bloud`, `authentik` databases in the list.

**Step 5: Verify apps user is superuser**

```bash
psql -U apps -h 127.0.0.1 -c "\du apps"
```
Expected: `Superuser` in the attributes column.

**Step 6: Start user services**

```bash
sudo systemctl restart bloud-user-services
```

Wait ~30 seconds for services to start, then:

```bash
systemctl --user status bloud-apps.target
```
Expected: `active`

```bash
systemctl --user status bloud-db-init.service
```
Expected: `active (exited)` with "PostgreSQL is ready" in the log.

**Step 7: Verify Authentik can reach postgres**

```bash
journalctl --user -u podman-apps-authentik-server -n 50
```
Expected: No "could not connect to database" or "Connection refused" errors. Should see Authentik startup logs.

```bash
systemctl --user status podman-apps-authentik-server
```
Expected: `active (running)` (may take ~60s for Authentik to complete startup)

**Step 8: Verify apps-net has the correct subnet**

```bash
podman network inspect apps-net | grep -A2 '"subnet"'
```
Expected: `"subnet": "10.89.0.0/24"` and `"gateway": "10.89.0.1"`

**Step 9: Verify Authentik can actually query postgres**

```bash
podman exec apps-authentik-server ak shell -c "from django.db import connection; cursor = connection.cursor(); cursor.execute('SELECT version()'); print(cursor.fetchone())"
```
Expected: prints a PostgreSQL version string.

**Step 10: Run integration tests**

```bash
bloud-test-integration
```
Expected: All checks pass, especially the PostgreSQL check (now using `psql` directly).

---

## Out of scope / follow-up

- **Data migration from old container volume**: Not planned. Greenfield start for homelab.
- **`bloud-reset` script**: Still references `podman unshare rm -rf` on user data dir. PostgreSQL data is now at `/var/lib/postgresql/` (system-owned). Document that reset no longer wipes DB; users must `sudo rm -rf /var/lib/postgresql/` for a full reset. Update in a later pass.
- **`nixos-app.nix` helper**: Will be created when migrating the next batch of apps (redis, traefik, etc.).
- **postgres.env file**: Still generated by `secrets/manager.go` but unused. Clean up in a later pass.
- **Per-app postgres users** (miniflux, etc.): Future improvement. Current setup keeps the single `apps` superuser.
