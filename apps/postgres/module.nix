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
      #   local          — unix socket connections (system services and rootless containers
      #                    that mount /run/postgresql — e.g. Authentik)
      #   127.0.0.1/32   — TCP localhost (host-agent, native NixOS services like miniflux)
      #   ::1/128        — TCP IPv6 localhost
      #   10.89.0.0/24   — apps-net bridge subnet (reserved; not currently reachable from
      #                    rootless podman due to user/root netns isolation — containers use
      #                    the Unix socket path instead)
      # NOTE: rootless podman containers cannot reach postgres via the bridge gateway IP
      # (10.89.0.1) because the bridge is in the user network namespace and postgres runs in
      # the root namespace. Containers that need postgres must mount /run/postgresql and set
      # POSTGRESQL_HOST to the socket directory. See apps/authentik/module.nix for the pattern.
      authentication = lib.mkForce ''
        local all all              trust
        host  all all 127.0.0.1/32 trust
        host  all all ::1/128      trust
        host  all all 10.89.0.0/24 trust
      '';

      settings = {
        port = appCfg.port;
        # Listen on all interfaces so Authentik containers (and any future workloads)
        # can connect regardless of which bridge address is in use. Access control is
        # enforced by pg_hba.conf above — only local sockets and the apps-net subnet
        # are trusted. This avoids binding to a specific interface IP that may not exist
        # at service startup time (e.g. the podman bridge comes up later as a user service).
        listen_addresses = lib.mkForce "*";
      };

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
