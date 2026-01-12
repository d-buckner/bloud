# mkBloudApp - Helper for creating Bloud app modules
#
# Standard way to define apps. Handles container setup, database init,
# volume mounts, and systemd service creation.
#
# Usage:
#   { config, pkgs, lib, ... }:
#   let
#     mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
#   in
#   mkBloudApp {
#     name = "myapp";
#     description = "My App description";
#     image = "myapp/myapp:latest";
#     port = 8080;
#     # ... see parameters below
#   }
#
# Parameters:
#   name          - App name (used for container, service, options path)
#   description   - Human-readable description for the enable option
#   image         - Container image
#   port          - Host port to expose
#   containerPort - Container port (defaults to port)
#   options       - Additional NixOS options { optName = { default, description, type? }; }
#   environment   - Function (cfg -> attrset) returning environment variables
#   volumes       - List of volume mounts OR function (cfg -> list) for dynamic volumes
#   dataDir       - true (creates ~/.local/share/bloud/<name>:/data) or string (custom path)
#   database      - Database name (auto-creates postgres db + init service)
#   dependsOn     - List of container dependencies (without "apps-" prefix for convenience)
#   waitFor       - List of { container, command } for health checks
#   network       - Container network (defaults to "apps-net")
#   cmd           - Container command (list of strings)
#   userns        - User namespace mode (e.g., "keep-id", "keep-id:uid=70,gid=70")
#   containerName - Override container name (defaults to name)
#   serviceName   - Override systemd service name (defaults to containerName)
#   extraServices - Additional systemd services (attrset OR function cfg -> attrset)
#   extraConfig   - Additional NixOS config (attrset OR function cfg -> attrset)

{ config, pkgs, lib }:

{
  name,
  description,
  image,
  port ? null,
  containerPort ? port,
  containerName ? name,
  serviceName ? containerName,
  options ? {},
  environment ? (_: {}),
  volumes ? [],
  dataDir ? false,
  database ? null,
  dependsOn ? [],
  waitFor ? [],
  network ? "apps-net",
  cmd ? [],
  userns ? null,
  extraServices ? {},
  extraConfig ? {},
}:

let
  mkPodmanService = import ./podman-service.nix { inherit pkgs lib; };

  # References to other configs
  bloudCfg = config.bloud;
  traefikCfg = config.bloud.apps.traefik;
  postgresCfg = config.bloud.apps.postgres;
  authentikCfg = config.bloud.apps.authentik;
  appCfg = config.bloud.apps.${name};

  userHome = "/home/${bloudCfg.user}";
  configPath = "${userHome}/.local/share/${bloudCfg.dataDir}";
  appDataPath = "${configPath}/${name}";

  # Build the cfg object passed to environment function
  cfg = appCfg // {
    # Common values contributors will need
    externalHost = bloudCfg.externalHost;
    traefikPort = traefikCfg.port;
    bloudUser = bloudCfg.user;
    configPath = configPath;
    appDataPath = appDataPath;
    # Postgres config (if available)
    postgresUser = postgresCfg.user or "apps";
    postgresPassword = postgresCfg.password or "testpass123";
    # Authentik/SSO config (if available)
    authentikEnabled = authentikCfg.enable or false;
    # App name for SSO client ID derivation
    appName = name;
  };

  # Convert simple dependsOn entries to full container names
  # "postgres" -> "apps-postgres", "apps-network" stays as-is
  normalizeDep = dep:
    if dep == "apps-network" then "apps-network"
    else if lib.hasPrefix "apps-" dep then dep
    else "apps-${dep}";

  normalizedDependsOn = map normalizeDep dependsOn;

  # Build volumes list (volumes can be a list or a function)
  dataDirVolume =
    if dataDir == true then [ "${appDataPath}:/data:z" ]
    else if builtins.isString dataDir then [ "${appDataPath}:${dataDir}:z" ]
    else [];

  resolvedVolumes = if builtins.isFunction volumes then volumes cfg else volumes;
  allVolumes = dataDirVolume ++ resolvedVolumes;

  # Resolve extraConfig (can be attrset or function)
  resolvedExtraConfig = if builtins.isFunction extraConfig then extraConfig cfg else extraConfig;

  # Resolve extraServices (can be attrset or function)
  resolvedExtraServices = if builtins.isFunction extraServices then extraServices cfg else extraServices;

  # Build custom options
  mkOption = name: optCfg: lib.mkOption {
    type = optCfg.type or lib.types.str;
    default = optCfg.default;
    description = optCfg.description or "Option ${name}";
  };

  customOptions = lib.mapAttrs mkOption options;

  # Database init service (if database is specified)
  dbInitService = lib.optionalAttrs (database != null) {
    "${serviceName}-db-init" = {
      description = "Initialize ${name} database";
      after = [ "podman-postgres.service" ];
      requires = [ "podman-postgres.service" ];
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
            if ${pkgs.podman}/bin/podman exec apps-postgres psql -U ${cfg.postgresUser} -d ${cfg.postgresUser} -c "SELECT 1" &>/dev/null; then
              echo "Postgres is ready"
              break
            fi
            if [ $i -eq 30 ]; then
              echo "ERROR: Postgres not ready after 60 seconds, giving up"
              exit 1
            fi
            echo "Waiting for postgres... ($i/30)"
            sleep 2
          done
          ${pkgs.podman}/bin/podman exec apps-postgres psql -U ${cfg.postgresUser} -c "CREATE DATABASE ${database};" 2>/dev/null || echo "Database ${database} already exists"
          ${pkgs.podman}/bin/podman exec apps-postgres psql -U ${cfg.postgresUser} -c "GRANT ALL PRIVILEGES ON DATABASE ${database} TO ${cfg.postgresUser};" || true
          echo "${name} database initialized"
        '';
      };
    };
  };

  # Extra systemd dependencies for database init
  dbExtraAfter = lib.optionals (database != null) [ "${serviceName}-db-init.service" ];
  dbExtraRequires = lib.optionals (database != null) [ "${serviceName}-db-init.service" ];

  # Port option (only if port is specified)
  portOption = lib.optionalAttrs (port != null) {
    port = lib.mkOption {
      type = lib.types.int;
      default = port;
      description = "Port to expose ${name} on";
    };
  };

in
{
  options.bloud.apps.${name} = {
    enable = lib.mkEnableOption description;
  } // portOption // customOptions;

  config = lib.mkIf appCfg.enable (lib.mkMerge [
    {
      # Create data directory if needed
      system.activationScripts = lib.optionalAttrs (dataDir != false) {
        "bloud-${name}-dirs" = lib.stringAfter [ "users" ] ''
          mkdir -p ${appDataPath}
          chown -R ${bloudCfg.user}:users ${appDataPath}
        '';
      };

      # Main container service
      systemd.user.services = {
        "podman-${serviceName}" = mkPodmanService ({
          name = containerName;
          image = image;
          environment = environment cfg;
          volumes = allVolumes;
          network = network;
          dependsOn = [ "apps-network" ] ++ normalizedDependsOn;
          extraAfter = dbExtraAfter;
          extraRequires = dbExtraRequires;
          # Bloud configurator hooks (uses dev path for now, will be packaged later)
          bloudAppName = name;
          bloudAgentPath = config.bloud.agentPath;
          inherit waitFor cmd;
        # Only add port mappings for non-host networking (host networking binds directly)
        } // lib.optionalAttrs (port != null && network != "host") {
          ports = [ "${toString appCfg.port}:${toString containerPort}" ];
        } // lib.optionalAttrs (userns != null) { inherit userns; });
      } // dbInitService // resolvedExtraServices;
    }
    resolvedExtraConfig
  ]);
}
