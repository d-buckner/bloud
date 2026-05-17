{ config, pkgs, lib, ... }:

let
  appCfg = config.bloud.apps.immich;
  bloudCfg = config.bloud;
  postgresCfg = config.bloud.apps.postgres;

  # Pass appDir so mkPodmanService auto-derives native deps (postgres, redis) from metadata.yaml
  mkPodmanService = import ../../nixos/lib/podman-service.nix { inherit pkgs lib; appDir = ./.; };
  # ML container has no native integration deps
  mkPodmanServicePlain = import ../../nixos/lib/podman-service.nix { inherit pkgs lib; };

  userHome = "/home/${bloudCfg.user}";
  configPath = "${userHome}/.local/share/${bloudCfg.dataDir}";
  appDataPath = "${configPath}/immich";

  postgresUser = postgresCfg.user or "apps";

  serverImage = "ghcr.io/immich-app/immich-server:v1.130.3";
  mlImage = "ghcr.io/immich-app/immich-machine-learning:v1.130.3";
in
{
  options.bloud.apps.immich = {
    enable = lib.mkEnableOption "Immich self-hosted photo and video backup";

    port = lib.mkOption {
      type = lib.types.int;
      default = 2283;
      description = "Port to expose Immich on";
    };
  };

  config = lib.mkIf appCfg.enable {
    # Enable Immich's required infrastructure
    bloud.apps.postgres.enable = true;
    bloud.apps.redis.enable = true;

    # Enable pgvector extension for Immich's ML-powered search features.
    # Immich's own migrations run CREATE EXTENSION IF NOT EXISTS vector on first start.
    # The apps role is SUPERUSER (set by bloud-postgresql-setup), so this works automatically.
    services.postgresql.extensions = ps: [ ps.pgvector ];

    # Create Immich directories for volume mounts
    system.activationScripts.bloud-immich-dirs = lib.stringAfter [ "users" ] ''
      mkdir -p ${appDataPath}/{upload,model-cache}
      chown -R ${bloudCfg.user}:users ${appDataPath}
    '';

    systemd.user.services = {
      # Immich Server — main application container
      podman-apps-immich-server = mkPodmanService {
        name = "apps-immich-server";
        image = serverImage;
        ports = [ "${toString appCfg.port}:2283" ];
        environment = {
          # Connect to native postgres via Unix socket (avoids rootless podman netns boundary).
          # pg driver interprets a host starting with / as a Unix socket directory.
          DB_URL = "postgresql://${postgresUser}@/immich?host=/run/postgresql&sslmode=disable";
          DB_VECTOR_EXTENSION = "pgvector";
          # Connect to native redis via Unix socket
          REDIS_SOCKET = "/run/redis-bloud/redis.sock";
          # ML service on the same apps-net network
          MACHINE_LEARNING_URL = "http://apps-immich-machine-learning:3003";
          # Bloud env vars for configurator hooks
          BLOUD_APPS_DIR = config.bloud.appsDir;
          BLOUD_SSO_BASE_URL = config.bloud.externalHost;
          BLOUD_SSO_AUTHENTIK_URL = config.bloud.authentikExternalHost;
        };
        volumes = [
          # Photo/video upload storage
          "${appDataPath}/upload:/usr/src/app/upload:z"
          # Mount postgres Unix socket
          "/run/postgresql:/run/postgresql:ro"
          # Mount redis Unix socket
          "/run/redis-bloud:/run/redis-bloud:ro"
        ];
        network = "apps-net";
        dependsOn = [ "apps-network" ];
        userns = "keep-id";
        # Wire up configurator (creates admin user, configures OIDC)
        bloudAppName = "immich";
        bloudAgentPath = bloudCfg.agentPath;
      };

      # Immich Machine Learning — optional but included for full functionality
      podman-apps-immich-machine-learning = mkPodmanServicePlain {
        name = "apps-immich-machine-learning";
        image = mlImage;
        environment = {
          MACHINE_LEARNING_CACHE_FOLDER = "/cache";
        };
        volumes = [
          # Persist downloaded ML models across restarts
          "${appDataPath}/model-cache:/cache:z"
        ];
        network = "apps-net";
        dependsOn = [ "apps-network" ];
        userns = "keep-id";
      };

      # Alias service so other apps can declare immich.service as a dependency
      immich = {
        description = "Immich readiness alias";
        after = [ "podman-apps-immich-server.service" ];
        requires = [ "podman-apps-immich-server.service" ];
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
          ExecStart = "${pkgs.coreutils}/bin/true";
        };
      };
    };
  };
}
