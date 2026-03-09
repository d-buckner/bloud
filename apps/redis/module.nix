{ config, pkgs, lib, ... }:

let
  mkNativeApp = import ../../nixos/lib/native-app.nix { inherit config pkgs lib; };
in
mkNativeApp {
  name = "redis";
  description = "Redis in-memory data store";
  port = 6379;
  # services.redis.servers.bloud creates systemd service "redis-bloud.service"
  serviceName = "redis-bloud";
  # Redis has no configurator — no runtime secrets or config injection needed
  configuratorHooks = false;

  nixosConfig = cfg: {
    services.redis.servers.bloud = {
      enable = true;
      port = cfg.port;
      # Persist data: save a snapshot every 60s if at least 1 key changed
      # Mirrors the previous container: cmd = [ "--save" "60" "1" ]
      # Format: listOf (listOf int) — [ [seconds changes] ]
      save = [ [ 60 1 ] ];
      # Unix socket for Authentik container access (see authentik/module.nix).
      # Same pattern as postgres: rootless podman containers can't reach system
      # services via TCP due to netns isolation, so we mount the socket.
      # NixOS redis module creates RuntimeDirectory=redis-bloud → /run/redis-bloud/
      # so the socket must live under that directory.
      unixSocket = "/run/redis-bloud/redis.sock";
      unixSocketPerm = 777;
    };

    # Canonical alias so app modules can declare `redis.service` as a dependency
    # without knowing the NixOS-generated service name (redis-bloud.service).
    # Convention: native apps expose {appName}.service for systemd dependency tracking.
    systemd.services.redis = {
      description = "Redis (Bloud canonical alias)";
      requires = [ "redis-bloud.service" ];
      after = [ "redis-bloud.service" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${pkgs.coreutils}/bin/true";
      };
      wantedBy = [ "multi-user.target" ];
    };
  };
}
