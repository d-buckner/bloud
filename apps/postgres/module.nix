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
      #   local          — unix socket connections (system services running as postgres)
      #   127.0.0.1/32   — TCP localhost (host-agent, miniflux, etc.)
      #   ::1/128        — TCP IPv6 localhost
      #   10.89.0.0/24   — apps-net podman bridge subnet (Authentik containers)
      # The 10.89.0.0/24 subnet matches the fixed apps-net podman bridge network.
      # See podman-apps-network in nixos/bloud.nix — must be kept in sync.
      authentication = lib.mkForce ''
        local all all              trust
        host  all all 127.0.0.1/32 trust
        host  all all ::1/128      trust
        host  all all 10.89.0.0/24 trust
      '';

      settings = {
        port = appCfg.port;
        # Listen on localhost and the apps-net bridge gateway so Authentik containers
        # can connect. 10.89.0.1 is the host-side gateway of the apps-net podman network.
        # Must be kept in sync with the subnet in nixos/bloud.nix.
        listen_addresses = "127.0.0.1,10.89.0.1";
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
