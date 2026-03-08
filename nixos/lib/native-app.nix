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
