# mkPodmanApp - Helper for creating Bloud Podman container app modules
#
# Standard way to define Podman-based apps. Handles container setup, database init,
# volume mounts, and systemd service creation.
#
# Usage:
#   { config, pkgs, lib, ... }:
#   let
#     mkPodmanApp = import ../../nixos/lib/podman-app.nix { inherit config pkgs lib; };
#   in
#   mkPodmanApp {
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
#   envFile       - Environment file for secrets (loaded at container start)
#
# Pass appDir = ./. in the import to auto-derive native service deps from metadata.yaml:
#   mkPodmanApp = import ../../nixos/lib/podman-app.nix { inherit config pkgs lib; appDir = ./.; };

{ config, pkgs, lib, appDir ? null }:

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
  # Default to keep-id for rootless podman port forwarding to work correctly
  # Apps can override to null if they need different behavior
  userns ? (if network == "host" then null else "keep-id"),
  extraServices ? {},
  extraConfig ? {},
  # Environment file for secrets (loaded at container start, not Nix eval time)
  envFile ? null,
  # Additional images to pre-pull at VM startup (beyond the main `image`).
  # Use this for apps that run multiple containers with different images.
  pullImages ? [],
}:

let
  # Pass appDir so mkPodmanService auto-derives native deps from metadata.yaml.
  mkPodmanService = import ./podman-service.nix { inherit pkgs lib; inherit appDir; };
  nativeDeps = import ./metadata.nix { inherit pkgs lib; };

  # Used for the db-init service's after list (mkPodmanService handles the main container).
  nativeIntegrationDeps = if appDir == null then [] else nativeDeps (appDir + "/metadata.yaml");

  # References to other configs
  bloudCfg = config.bloud;
  traefikCfg = config.bloud.apps.traefik;
  postgresCfg = config.bloud.apps.postgres;
  authentikCfg = config.bloud.apps.authentik;
  appCfg = config.bloud.apps.${name};

  userHome = "/home/${bloudCfg.user}";
  configPath = "${userHome}/.local/share/${bloudCfg.dataDir}";
  appDataPath = "${configPath}/${name}";

  # Path to secrets directory (env files are written here by host-agent)
  secretsDir = "${configPath}";

  # Build the cfg object passed to environment function
  cfg = appCfg // {
    # Common values contributors will need
    externalHost = bloudCfg.externalHost;
    authentikExternalHost = bloudCfg.authentikExternalHost;
    traefikPort = traefikCfg.port;
    bloudUser = bloudCfg.user;
    configPath = configPath;
    appDataPath = appDataPath;
    secretsDir = secretsDir;
    # Postgres config (if available)
    postgresUser = postgresCfg.user or "apps";
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

  # Database init service (if database is specified).
  # Uses psql directly — postgres is now a native NixOS service, not a Podman container.
  dbInitService = lib.optionalAttrs (database != null) {
    "${serviceName}-db-init" = {
      description = "Initialize ${name} database";
      # postgres is a system service; by the time user services start it should be running.
      # Poll pg_isready as a safety check (no direct systemd dep across user/system boundary).
      after = [ "bloud-init-secrets.service" ] ++ nativeIntegrationDeps;
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
            if [ "$i" -eq 30 ]; then
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
            -c "GRANT ALL PRIVILEGES ON DATABASE \"${database}\" TO \"${cfg.postgresUser}\"" \
            || true
          echo "${name} database initialized"
        '';
      };
    };
  };

  # Extra systemd dependencies for database init (native deps handled by mkPodmanService)
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

  # Extract host paths from volume mounts for directory creation
  # Volume format: "hostPath:containerPath:options" or "hostPath:containerPath"
  # Filter out file mounts (paths ending with common file extensions) - those are created by configurators
  isFilePath = path: builtins.match ".*\\.(js|json|yaml|yml|toml|conf|cfg|ini|env)$" path != null;
  volumeHostPaths = builtins.filter (p: !isFilePath p) (map (v:
    let parts = lib.splitString ":" v;
    in builtins.elemAt parts 0
  ) allVolumes);

in
{
  options.bloud.apps.${name} = {
    enable = lib.mkEnableOption description;
  } // portOption // customOptions;

  config = lib.mkMerge [
    # Pre-pull container images at VM startup, even before the app is enabled.
    # This ensures images are cached when the user installs the app, avoiding
    # multi-minute internet pulls that would cause install timeout failures.
    { bloud.pullImages = [ image ] ++ pullImages; }

    (lib.mkIf appCfg.enable (lib.mkMerge [
    {
      # Create all directories needed for volume mounts
      # This ensures directories exist before podman tries to mount them
      # NOTE: In future, this should be handled via systemd tmpfiles or a dedicated setup service
      system.activationScripts."bloud-${name}-dirs" = lib.stringAfter [ "users" ] ''
        # Create base data directory
        mkdir -p ${appDataPath}
        # Create all volume mount directories
        ${lib.concatMapStrings (path: ''
          mkdir -p ${path}
        '') volumeHostPaths}
        # Fix ownership
        chown -R ${bloudCfg.user}:users ${appDataPath}
      '';

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
        } // lib.optionalAttrs (userns != null) { inherit userns; }
          // lib.optionalAttrs (envFile != null) { inherit envFile; }) // {
          # Pass BLOUD env vars so prestart hooks generate correct SSO URLs.
          # BLOUD_SSO_BASE_URL = externalHost: OAuth redirect URLs must use the mDNS
          # hostname (bloud.local) so the browser can reach the callback after Authentik auth.
          # BLOUD_SSO_AUTHENTIK_URL = authentikExternalHost: the OIDC discovery endpoint
          # URL sent to apps (e.g. OAUTH2_OIDC_DISCOVERY_ENDPOINT). Using bloud.local here
          # causes Authentik to return bloud.local authorization_endpoint URLs in the OIDC
          # discovery response, which the browser can follow. Requires the OUTPUT iptables
          # rule (port 80 → 8080) so bloud.local is reachable from host-network containers.
          environment = {
            BLOUD_APPS_DIR = config.bloud.appsDir;
            BLOUD_SSO_BASE_URL = config.bloud.externalHost;
            BLOUD_SSO_AUTHENTIK_URL = config.bloud.authentikExternalHost;
          };
        };
      } // dbInitService // resolvedExtraServices;
    }
    resolvedExtraConfig
  ]))
  ];
}
