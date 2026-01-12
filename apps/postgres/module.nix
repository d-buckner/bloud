{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
in
mkBloudApp {
  name = "postgres";
  description = "PostgreSQL database for apps";
  containerName = "apps-postgres";
  # serviceName must match app name for Go API health checks
  serviceName = "postgres";

  image = "postgres:16-alpine";
  # Expose on host port for apps using host networking (e.g., Miniflux with OIDC)
  port = 5432;

  options = {
    user = { default = "apps"; description = "PostgreSQL user"; };
    password = { default = "testpass123"; description = "PostgreSQL password"; };
    database = { default = "apps"; description = "Default database name"; };
  };

  environment = cfg: {
    POSTGRES_USER = cfg.user;
    POSTGRES_DB = cfg.database;
    POSTGRES_PASSWORD = cfg.password;
  };

  # Use explicit volume since data path is "apps-postgres" not "postgres"
  volumes = cfg: [ "${cfg.configPath}/apps-postgres:/var/lib/postgresql/data:Z" ];
  userns = "keep-id:uid=70,gid=70";

  # Create data directory manually
  extraConfig = cfg: {
    system.activationScripts.bloud-apps-postgres-dirs = lib.stringAfter [ "users" ] ''
      mkdir -p ${cfg.configPath}/apps-postgres
      chown -R ${cfg.bloudUser}:users ${cfg.configPath}/apps-postgres
    '';
  };
}
